package clockbound

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"testing"
	"time"
)

type rustProjection struct {
	EarliestSec int64  `json:"earliest_sec"`
	EarliestNS  int64  `json:"earliest_ns"`
	LatestSec   int64  `json:"latest_sec"`
	LatestNS    int64  `json:"latest_ns"`
	Status      Status `json:"status"`
}

type differentialCase struct {
	Name      string          `json:"name"`
	Snapshot  string          `json:"snapshot"`
	Counter   uint64          `json:"counter"`
	Policy    string          `json:"policy"`
	Rust      *rustProjection `json:"rust"`
	RustError string          `json:"rust_error"`
}

// exactProjection is an independent arbitrary-precision interpretation of the
// protocol formula. It deliberately shares no integer helpers with production.
func exactProjection(s clockSnapshot, tsc uint64) (center, radius *big.Rat) {
	delta := new(big.Int).SetUint64(tsc - s.asOfTSC)
	numerator := new(big.Int).Mul(delta, new(big.Int).SetUint64(s.periodFrac))
	numerator.Mul(numerator, big.NewInt(1000000000))
	elapsed := new(big.Rat).SetFrac(numerator, new(big.Int).Lsh(big.NewInt(1), uint(64+int(s.periodShift))))
	drift := new(big.Rat).Mul(new(big.Rat).Set(elapsed), new(big.Rat).SetFrac64(int64(s.maxDriftPPB), 1000000000))
	periodError := new(big.Rat).SetFrac(new(big.Int).SetUint64(s.periodErrFrac), new(big.Int).Lsh(big.NewInt(1), uint(64+int(s.periodErrShift))))
	estimate := new(big.Rat).Mul(new(big.Rat).Set(elapsed), periodError)
	radius = new(big.Rat).Add(new(big.Rat).SetInt64(s.boundNanos), drift)
	radius.Add(radius, estimate)
	center = new(big.Rat).Add(rationalTime(s.asOf), elapsed)
	return center, radius
}

func ceilPositive(r *big.Rat) *big.Int {
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(r.Num(), r.Denom(), rem)
	if rem.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}

func TestDifferentialAgainstActualRust(t *testing.T) {
	f, err := os.Open("testdata/rust-differential.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	checked, sameStatus, conservativeStatus, rejections := 0, 0, 0, 0
	var maxCenterShift, maxRadiusExtra, maxExactExtra int64
	for scanner.Scan() {
		var item differentialCase
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		data, err := hex.DecodeString(item.Snapshot)
		if err != nil {
			t.Fatal(err)
		}
		snap, err := readClockSnapshot(data, nil)
		if item.Policy == "malformed" || item.Policy == "uninitialized" {
			want := ErrMalformedSegment
			if item.Policy == "uninitialized" {
				want = ErrNotInitialized
			}
			if !errors.Is(err, want) {
				t.Fatalf("%s: got %v want %v", item.Name, err, want)
			}
			rejections++
			continue
		}
		if err != nil {
			t.Fatalf("%s: decode: %v", item.Name, err)
		}
		got, err := snap.atTSC(item.Counter)
		if item.Policy == "counter_backwards" {
			if !errors.Is(err, ErrCounterBackwards) {
				t.Fatalf("%s: regressed counter accepted", item.Name)
			}
			rejections++
			continue
		}
		if err != nil || item.Rust == nil || item.RustError != "" {
			t.Fatalf("%s: unexpected mismatch Go=%v Rust=%v", item.Name, err, item.RustError)
		}
		legacy := item.Rust
		legacyTrusted := legacy.Status == StatusSynchronized || legacy.Status == StatusFreeRunning
		if got.IsTrusted() && !legacyTrusted {
			t.Fatalf("%s: upgraded untrusted Rust status %v -> %v", item.Name, legacy.Status, got.Status)
		}
		if got.Status == legacy.Status {
			sameStatus++
		} else {
			safer := legacyTrusted && got.Status == StatusUnknown || legacy.Status == StatusSynchronized && got.Status == StatusFreeRunning
			if !safer {
				t.Fatalf("%s: unexpected status transition %v -> %v", item.Name, legacy.Status, got.Status)
			}
			conservativeStatus++
		}
		center, radius := exactProjection(snap, item.Counter)
		exactEarliest := new(big.Rat).Sub(new(big.Rat).Set(center), radius)
		exactLatest := new(big.Rat).Add(new(big.Rat).Set(center), radius)
		if rationalTime(got.Earliest).Cmp(exactEarliest) > 0 || rationalTime(got.Latest).Cmp(exactLatest) < 0 {
			t.Fatalf("%s: Go excluded exact-model time", item.Name)
		}
		// Precision budget: at most four extra ns of radius beyond ceil(exact
		// radius). Outward drift/period rounding and center quantization explain it.
		exactExtra := new(big.Int).Sub(big.NewInt(int64(got.ErrorBound)), ceilPositive(radius))
		if exactExtra.Sign() < 0 || exactExtra.Cmp(big.NewInt(4)) > 0 {
			t.Fatalf("%s: unjustified interval widening: %s ns", item.Name, exactExtra)
		}
		if exactExtra.Int64() > maxExactExtra {
			maxExactExtra = exactExtra.Int64()
		}
		legacyEarliest := time.Unix(legacy.EarliestSec, legacy.EarliestNS).UTC()
		legacyLatest := time.Unix(legacy.LatestSec, legacy.LatestNS).UTC()
		legacyRadius := legacyLatest.Sub(legacyEarliest) / 2
		legacyCenter := legacyEarliest.Add(legacyRadius)
		centerShift := got.Earliest.Add(got.ErrorBound).Sub(legacyCenter)
		if centerShift < 0 {
			centerShift = -centerShift
		}
		if int64(centerShift) > maxCenterShift {
			maxCenterShift = int64(centerShift)
		}
		if extra := int64(got.ErrorBound - legacyRadius); extra > maxRadiusExtra {
			maxRadiusExtra = extra
		}
		checked++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if checked < 800 || rejections != 4 {
		t.Fatalf("incomplete corpus: %d normal, %d policy rejections", checked, rejections)
	}
	t.Logf("Rust cases=%d; status equal=%d, conservative=%d; policy rejections=%d; max center shift=%dns; max extra radius vs Rust=%dns, vs exact ceil=%dns", checked, sameStatus, conservativeStatus, rejections, maxCenterShift, maxRadiusExtra, maxExactExtra)
}

var projectionBenchmarkSink Result

func BenchmarkProjection(b *testing.B) {
	s := snapshotFromGolden(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := s.atTSC(2024 + uint64(i&15))
		if err != nil {
			b.Fatal(err)
		}
		projectionBenchmarkSink = r
	}
}
