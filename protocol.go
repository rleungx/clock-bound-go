package clockbound

import (
	"encoding/binary"
	"fmt"
	"sync/atomic"
	"time"
	"unsafe"
)

const (
	clockBoundLayoutV3   = 3
	vmClockLayoutV1      = 1
	clockBoundSegmentLen = 104
	vmClockSegmentLen    = 104
	snapshotAttempts     = 1000
	freeRunningGrace     = 60 * time.Second
	maxDriftPPB          = 1_000_000_000
	minTimestampSeconds  = -62135596800 // 0001-01-01 UTC
	maxTimestampSeconds  = 253402300799 // 9999-12-31 UTC
)

type clockSnapshot struct {
	asOfTSC        uint64
	asOf           time.Time
	voidAfter      time.Time
	periodFrac     uint64
	periodErrFrac  uint64
	boundNanos     int64
	disruption     uint64
	maxDriftPPB    uint32
	status         Status
	disruptionOn   bool
	periodShift    uint8
	periodErrShift uint8
}

// readClockSnapshot never caches solely by the 16-bit generation: it wraps.
// During an update or restart a previous calibration remains usable until its
// own void-after time. Without a previous calibration the read must succeed.
func readClockSnapshot(data []byte, previous *clockSnapshot) (clockSnapshot, error) {
	if len(data) < clockBoundSegmentLen {
		return clockSnapshot{}, fmt.Errorf("%w: ClockBound length %d < %d", ErrMalformedSegment, len(data), clockBoundSegmentLen)
	}
	for attempt := 0; attempt < snapshotAttempts; attempt++ {
		header := load32(data, 12)
		version, generation := uint16(header), uint16(header>>16)
		if version == 0 || generation == 0 || generation&1 != 0 {
			if previous != nil {
				return *previous, nil
			}
			continue
		}
		if err := validateClockBoundVersion(version); err != nil {
			return clockSnapshot{}, err
		}
		var body [clockBoundSegmentLen - 16]byte
		// Every payload load is acquire ordered, including the final word, so on
		// ARM64 none of these loads can move after the trailing sequence read.
		// The producer is an external process; ordinary copy has no such barrier.
		for offset := 0; offset < len(body); offset += 8 {
			binary.LittleEndian.PutUint64(body[offset:offset+8], load64(data, 16+offset))
		}
		magic := load64(data, 0)
		size := load32(data, 8)
		if header != load32(data, 12) {
			continue
		}
		if magic != 0x43420200414D5A4E || size < clockBoundSegmentLen || uint64(size) > uint64(len(data)) {
			return clockSnapshot{}, fmt.Errorf("%w: ClockBound magic or length", ErrMalformedSegment)
		}
		return decodeClockSnapshot(body[:])
	}
	if previous != nil {
		return *previous, nil
	}
	return clockSnapshot{}, fmt.Errorf("%w: ClockBound has no stable initial calibration", ErrNotInitialized)
}

func load32(data []byte, offset int) uint32 {
	return atomic.LoadUint32((*uint32)(unsafe.Pointer(&data[offset])))
}

func load64(data []byte, offset int) uint64 {
	return atomic.LoadUint64((*uint64)(unsafe.Pointer(&data[offset])))
}

func validateClockBoundVersion(word uint16) error {
	// The Rust ABI stores current version in the low byte and minimum in the
	// high byte. This is relevant for asymmetric future compatibility ranges.
	minimum, current := uint8(word>>8), uint8(word)
	if minimum != clockBoundLayoutV3 || current != clockBoundLayoutV3 {
		return fmt.Errorf("%w: unverified ClockBound version range %d..%d (need 3..3)", ErrUnsupportedVersion, minimum, current)
	}
	return nil
}

func decodeClockSnapshot(body []byte) (clockSnapshot, error) {
	if len(body) < clockBoundSegmentLen-16 {
		return clockSnapshot{}, fmt.Errorf("%w: truncated v3 body", ErrMalformedSegment)
	}
	u64 := binary.LittleEndian.Uint64
	status := Status(int32(binary.LittleEndian.Uint32(body[76:80])))
	drift := binary.LittleEndian.Uint32(body[72:76])
	bound := int64(u64(body[56:64]))
	if status < StatusUnknown || status > StatusDisrupted || drift >= maxDriftPPB || bound < 0 || body[80] > 1 {
		return clockSnapshot{}, fmt.Errorf("%w: status, drift, bound or disruption flag", ErrMalformedSegment)
	}
	asOfSec, asOfNS := int64(u64(body[8:16])), int64(u64(body[16:24]))
	voidSec, voidNS := int64(u64(body[24:32])), int64(u64(body[32:40]))
	if asOfSec < minTimestampSeconds || asOfSec > maxTimestampSeconds || voidSec < minTimestampSeconds || voidSec > maxTimestampSeconds {
		return clockSnapshot{}, fmt.Errorf("%w: calibration timestamp outside years 0001..9999", ErrOutOfRange)
	}
	if asOfNS < 0 || asOfNS >= int64(time.Second) || voidNS < 0 || voidNS >= int64(time.Second) {
		return clockSnapshot{}, fmt.Errorf("%w: invalid timespec nanoseconds", ErrMalformedSegment)
	}
	period := u64(body[40:48])
	if period == 0 {
		return clockSnapshot{}, fmt.Errorf("%w: zero counter period", ErrNotInitialized)
	}
	snap := clockSnapshot{
		asOfTSC:        u64(body[0:8]),
		asOf:           time.Unix(asOfSec, asOfNS).UTC(),
		voidAfter:      time.Unix(voidSec, voidNS).UTC(),
		periodFrac:     period,
		periodErrFrac:  u64(body[48:56]),
		boundNanos:     bound,
		disruption:     u64(body[64:72]),
		maxDriftPPB:    drift,
		status:         status,
		disruptionOn:   body[80] == 1,
		periodShift:    body[81],
		periodErrShift: body[82],
	}
	if snap.voidAfter.Before(snap.asOf) {
		return clockSnapshot{}, fmt.Errorf("%w: void-after precedes as-of", ErrMalformedSegment)
	}
	return snap, nil
}

func readVMClockMarker(data []byte) (uint64, error) {
	if len(data) < vmClockSegmentLen {
		return 0, fmt.Errorf("%w: truncated VMClock", ErrMalformedSegment)
	}
	for attempt := 0; attempt < snapshotAttempts; attempt++ {
		first := load32(data, 12)
		if first&1 != 0 {
			continue
		}
		magic, size := load32(data, 0), load32(data, 4)
		version := load32(data, 8) & 0xffff
		marker := load64(data, 16)
		if first != load32(data, 12) {
			continue
		}
		if magic != 0x4B4C4356 || size < vmClockSegmentLen || uint64(size) > uint64(len(data)) {
			return 0, fmt.Errorf("%w: VMClock magic or length", ErrMalformedSegment)
		}
		if version != vmClockLayoutV1 {
			return 0, fmt.Errorf("%w: VMClock v%d", ErrUnsupportedVersion, version)
		}
		return marker, nil
	}
	return 0, fmt.Errorf("%w: VMClock", ErrSnapshotBusy)
}
