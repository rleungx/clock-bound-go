package clockbound

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"testing"
	"time"
)

func golden(t testing.TB) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/upstream-v3.bin")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func snapshotFromGolden(t testing.TB) clockSnapshot {
	t.Helper()
	snap, err := readClockSnapshot(golden(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestOfficialRustWriterLayout(t *testing.T) {
	snap := snapshotFromGolden(t)
	if snap.asOfTSC != 1000 || snap.asOf.Unix() != 1700000000 || snap.asOf.Nanosecond() != 123456789 ||
		snap.voidAfter.Unix() != 1700000120 || snap.voidAfter.Nanosecond() != 123456789 ||
		snap.periodFrac != math.MaxUint64 || snap.periodErrFrac != math.MaxUint64 ||
		snap.boundNanos != 37 || snap.disruption != 42 || snap.maxDriftPPB != 100 ||
		snap.status != StatusSynchronized || snap.disruptionOn || snap.periodShift != 10 || snap.periodErrShift != 20 {
		t.Fatalf("upstream ABI mismatch: %+v", snap)
	}
	result, err := snap.atTSC(2024)
	if err != nil {
		t.Fatal(err)
	}
	center := snap.asOf.Add(time.Second - time.Nanosecond)
	if result.Earliest != center.Add(-1092*time.Nanosecond) || result.Latest != center.Add(1092*time.Nanosecond) {
		t.Fatalf("unexpected exact projection: %+v", result)
	}
}

func TestClockBoundRejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name   string
		offset int
		value  uint64
		width  int
		want   error
	}{
		{"magic", 0, 0, 8, ErrMalformedSegment},
		{"size-small", 8, 16, 4, ErrMalformedSegment},
		{"size-large", 8, 4096, 4, ErrMalformedSegment},
		{"unsupported-version", 12, 0x0404, 2, ErrUnsupportedVersion},
		{"old-version", 12, 2, 2, ErrUnsupportedVersion},
		{"reversed-range", 12, 0x0403, 2, ErrUnsupportedVersion},
		{"uninitialized-version", 12, 0, 2, ErrNotInitialized},
		{"uninitialized-generation", 14, 0, 2, ErrNotInitialized},
		{"writer-busy", 14, 3, 2, ErrNotInitialized},
		{"negative-bound", 72, math.MaxUint64, 8, ErrMalformedSegment},
		{"bad-drift", 88, 1000000000, 4, ErrMalformedSegment},
		{"bad-status", 92, 9, 4, ErrMalformedSegment},
		{"bad-boolean", 96, 2, 1, ErrMalformedSegment},
		{"bad-as-of-nsec", 32, 1000000000, 8, ErrMalformedSegment},
		{"bad-expiry-nsec", 48, math.MaxUint64, 8, ErrMalformedSegment},
		{"expiry-before-update", 40, 1699999999, 8, ErrMalformedSegment},
		{"zero-period", 56, 0, 8, ErrNotInitialized},
		{"timestamp-too-large", 24, math.MaxInt64, 8, ErrOutOfRange},
		{"timestamp-too-small", 24, 1 << 63, 8, ErrOutOfRange},
		{"expiry-too-large", 40, math.MaxInt64, 8, ErrOutOfRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := golden(t)
			switch tt.width {
			case 1:
				data[tt.offset] = byte(tt.value)
			case 2:
				binary.LittleEndian.PutUint16(data[tt.offset:], uint16(tt.value))
			case 4:
				binary.LittleEndian.PutUint32(data[tt.offset:], uint32(tt.value))
			case 8:
				binary.LittleEndian.PutUint64(data[tt.offset:], tt.value)
			}
			if _, err := readClockSnapshot(data, nil); !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
	for n := 0; n < 104; n++ {
		if _, err := readClockSnapshot(make([]byte, n), nil); !errors.Is(err, ErrMalformedSegment) {
			t.Fatalf("length %d: %v", n, err)
		}
	}
	if _, err := decodeClockSnapshot(nil); !errors.Is(err, ErrMalformedSegment) {
		t.Fatal(err)
	}
}

func TestGenerationReuseAndWriterRestart(t *testing.T) {
	data := golden(t)
	previous, err := readClockSnapshot(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A 16-bit sequence may wrap back to the same value while the client is idle.
	binary.LittleEndian.PutUint64(data[72:], 12345)
	snap, err := readClockSnapshot(data, &previous)
	if err != nil || snap.boundNanos != 12345 {
		t.Fatalf("reused old generation: %+v %v", snap, err)
	}
	for _, word := range []uint32{0, 0x00000303, 0x00030303} {
		binary.LittleEndian.PutUint32(data[12:], word)
		snap, err = readClockSnapshot(data, &previous)
		if err != nil || snap.boundNanos != previous.boundNanos {
			t.Fatalf("restart/update word %x: %+v %v", word, snap, err)
		}
		result, err := snap.atTSC(previous.asOfTSC + 123*1024)
		if err != nil || result.IsTrusted() || result.Status != StatusUnknown {
			t.Fatalf("stale calibration trusted: %+v %v", result, err)
		}
	}
	binary.LittleEndian.PutUint32(data[12:], 0x00040303)
	snap, err = readClockSnapshot(data, &previous)
	if err != nil || snap.boundNanos != 12345 {
		t.Fatalf("did not recover: %+v %v", snap, err)
	}
}

func TestVersionCompatibilityRange(t *testing.T) {
	for _, word := range []uint16{0x0303} {
		if err := validateClockBoundVersion(word); err != nil {
			t.Fatal(err)
		}
	}
	for _, word := range []uint16{0, 2, 0x0404, 0x0403, 0x0102, 0x0304, 0x0203} {
		if !errors.Is(validateClockBoundVersion(word), ErrUnsupportedVersion) {
			t.Fatalf("accepted %x", word)
		}
	}
}

func vmFixture(marker uint64) []byte {
	data := make([]byte, 104)
	binary.LittleEndian.PutUint32(data[0:], 0x4B4C4356)
	binary.LittleEndian.PutUint32(data[4:], 104)
	binary.LittleEndian.PutUint16(data[8:], 1)
	binary.LittleEndian.PutUint32(data[12:], 2)
	binary.LittleEndian.PutUint64(data[16:], marker)
	return data
}

func TestVMClockValidation(t *testing.T) {
	for _, marker := range []uint64{0, 42, math.MaxUint64} {
		got, err := readVMClockMarker(vmFixture(marker))
		if err != nil || got != marker {
			t.Fatalf("marker %d: %d %v", marker, got, err)
		}
	}
	for _, tt := range []struct {
		name   string
		mutate func([]byte)
		want   error
	}{
		{"magic", func(d []byte) { d[0] = 0 }, ErrMalformedSegment},
		{"size", func(d []byte) { binary.LittleEndian.PutUint32(d[4:], 4096) }, ErrMalformedSegment},
		{"version", func(d []byte) { d[8] = 2 }, ErrUnsupportedVersion},
		{"busy", func(d []byte) { d[12] = 3 }, ErrSnapshotBusy},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := vmFixture(42)
			tt.mutate(d)
			if _, err := readVMClockMarker(d); !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
	if _, err := readVMClockMarker(nil); !errors.Is(err, ErrMalformedSegment) {
		t.Fatal(err)
	}
}
