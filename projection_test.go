package clockbound

import (
	"errors"
	"math"
	"math/big"
	"math/rand"
	"testing"
	"time"
)

func TestShift192AgainstBigInteger(t *testing.T) {
	random := rand.New(rand.NewSource(193))
	for i := 0; i < 4000; i++ {
		words := [3]uint64{random.Uint64(), random.Uint64(), random.Uint64()}
		if i%4 == 0 {
			words[2] = 0
		}
		shift := i % 330
		number := new(big.Int)
		for j := 2; j >= 0; j-- {
			number.Lsh(number, 64)
			number.Or(number, new(big.Int).SetUint64(words[j]))
		}
		quotient := new(big.Int).Rsh(new(big.Int).Set(number), uint(shift))
		got, fractional, err := shift192(words, shift)
		if quotient.BitLen() > 64 {
			if !errors.Is(err, ErrOutOfRange) {
				t.Fatalf("overflow accepted: %x >> %d", words, shift)
			}
			continue
		}
		residue := new(big.Int).Sub(number, new(big.Int).Lsh(new(big.Int).Set(quotient), uint(shift)))
		if err != nil || got != quotient.Uint64() || fractional != (residue.Sign() != 0) {
			t.Fatalf("%x >> %d: got %d/%v/%v want %s", words, shift, got, fractional, err, quotient)
		}
	}
}

func rationalTime(t time.Time) *big.Rat {
	n := new(big.Int).Mul(big.NewInt(t.Unix()), big.NewInt(1000000000))
	n.Add(n, big.NewInt(int64(t.Nanosecond())))
	return new(big.Rat).SetInt(n)
}

func TestProjectionContainsExactRationalInterval(t *testing.T) {
	random := rand.New(rand.NewSource(718231))
	for i := 0; i < 3000; i++ {
		base := time.Date(2300, 1, 1, 0, 0, 0, int(random.Int31n(1000000000)), time.UTC)
		snap := clockSnapshot{asOfTSC: 1000, asOf: base, voidAfter: base.Add(120 * time.Second),
			periodFrac: random.Uint64() | 1, periodErrFrac: random.Uint64(), periodShift: uint8(20 + i%236), periodErrShift: uint8(i % 256),
			boundNanos: random.Int63n(1 << 54), maxDriftPPB: uint32(random.Int31n(999999999)), status: StatusSynchronized}
		delta := uint64(random.Int63n(1 << 35))
		got, err := snap.atTSC(snap.asOfTSC + delta)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		// Independent arbitrary-precision oracle: no production multiplication or
		// rounding helper is reused here.
		elapsedNumerator := new(big.Int).Mul(new(big.Int).SetUint64(delta), new(big.Int).SetUint64(snap.periodFrac))
		elapsedNumerator.Mul(elapsedNumerator, big.NewInt(1000000000))
		elapsed := new(big.Rat).SetFrac(elapsedNumerator, new(big.Int).Lsh(big.NewInt(1), uint(64+int(snap.periodShift))))
		drift := new(big.Rat).Mul(new(big.Rat).Set(elapsed), new(big.Rat).SetFrac64(int64(snap.maxDriftPPB), 1000000000))
		periodError := new(big.Rat).SetFrac(new(big.Int).SetUint64(snap.periodErrFrac), new(big.Int).Lsh(big.NewInt(1), uint(64+int(snap.periodErrShift))))
		estimate := new(big.Rat).Mul(new(big.Rat).Set(elapsed), periodError)
		radius := new(big.Rat).Add(new(big.Rat).SetInt64(snap.boundNanos), drift)
		radius.Add(radius, estimate)
		center := new(big.Rat).Add(rationalTime(base), elapsed)
		earliest := new(big.Rat).Sub(new(big.Rat).Set(center), radius)
		latest := new(big.Rat).Add(new(big.Rat).Set(center), radius)
		if rationalTime(got.Earliest).Cmp(earliest) > 0 || rationalTime(got.Latest).Cmp(latest) < 0 {
			t.Fatalf("case %d: interval shrank: %+v vs [%s,%s]", i, got, earliest.RatString(), latest.RatString())
		}
		if got.ErrorBound < time.Duration(snap.boundNanos) {
			t.Fatalf("case %d: lost base precision", i)
		}
		excess := new(big.Int).Sub(big.NewInt(int64(got.ErrorBound)), ceilPositive(radius))
		if excess.Sign() < 0 || excess.Cmp(big.NewInt(4)) > 0 {
			t.Fatalf("case %d: error-radius precision budget exceeded: %s ns", i, excess)
		}
	}
}

func TestProjectionPrecisionOverflowAndCounterRegression(t *testing.T) {
	s := snapshotFromGolden(t)
	s.periodFrac = 1 << 63
	s.periodShift = 0
	s.periodErrFrac = 0
	s.maxDriftPPB = 0
	for _, bound := range []int64{0, 1, 1<<53 + 1, math.MaxInt64} {
		s.boundNanos = bound
		r, err := s.atTSC(s.asOfTSC)
		if err != nil || int64(r.ErrorBound) != bound {
			t.Fatalf("bound %d lost precision: %+v %v", bound, r, err)
		}
	}
	if _, err := s.atTSC(s.asOfTSC - 1); !errors.Is(err, ErrCounterBackwards) {
		t.Fatal(err)
	}
	s.maxDriftPPB = 1
	if _, err := s.atTSC(s.asOfTSC + 1); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("bound overflow: %v", err)
	}
	s.boundNanos = 0
	s.maxDriftPPB = 0
	if _, err := s.atTSC(math.MaxUint64); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("duration overflow: %v", err)
	}
	s.periodFrac = math.MaxUint64
	s.periodShift = 255
	r, err := s.atTSC(s.asOfTSC + 1)
	if err != nil || r.ErrorBound != time.Nanosecond {
		t.Fatalf("sub-ns projection: %+v %v", r, err)
	}
}

func TestStatusExpiryAndGrace(t *testing.T) {
	s := snapshotFromGolden(t)
	s.periodFrac = 1 << 63
	s.periodShift = 0
	s.periodErrFrac = 0
	s.maxDriftPPB = 0
	for _, tt := range []struct {
		delta uint64
		want  Status
	}{{0, StatusSynchronized}, {120, StatusSynchronized}, {121, StatusFreeRunning}, {240, StatusFreeRunning}, {241, StatusUnknown}} {
		r, err := s.atTSC(s.asOfTSC + tt.delta)
		if err != nil || r.Status != tt.want {
			t.Fatalf("delta %d: %+v %v want %v", tt.delta, r, err, tt.want)
		}
	}
	for _, status := range []Status{StatusUnknown, StatusDisrupted} {
		s.status = status
		r, err := s.atTSC(s.asOfTSC + 241)
		if err != nil || r.Status != status {
			t.Fatalf("overwrote status: %+v %v", r, err)
		}
	}
	// The fractional instant is already past expiry even if its floor equals it.
	s.status = StatusSynchronized
	s.periodShift = 60
	s.voidAfter = s.asOf
	r, err := s.atTSC(s.asOfTSC + 1)
	if err != nil || r.Status != StatusUnknown {
		t.Fatalf("fractional expiry still trusted: %+v %v", r, err)
	}
}
