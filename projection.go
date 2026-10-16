package clockbound

import (
	"math"
	"math/bits"
	"time"
)

func (s clockSnapshot) atTSC(tsc uint64) (Result, error) {
	if tsc < s.asOfTSC {
		return Result{}, ErrCounterBackwards
	}
	// Compute cycles * fractional-period * 1e9 / 2^(64+shift) exactly.
	// A 192-bit intermediate avoids both float precision loss and uint64 wrap.
	hi, lo := bits.Mul64(tsc-s.asOfTSC, s.periodFrac)
	carry, low := bits.Mul64(lo, uint64(time.Second))
	high, middle := bits.Mul64(hi, uint64(time.Second))
	middle, carryOut := bits.Add64(middle, carry, 0)
	high += carryOut
	elapsed, fractional, err := shift192([3]uint64{low, middle, high}, 64+int(s.periodShift))
	if err != nil || elapsed > math.MaxInt64 {
		return Result{}, ErrOutOfRange
	}
	center := s.asOf.Add(time.Duration(elapsed))
	elapsedUpper := elapsed
	if fractional {
		if elapsed == math.MaxInt64 {
			return Result{}, ErrOutOfRange
		}
		elapsedUpper++
	}

	// Grow the error using an upper bound on elapsed time. Each division rounds
	// outward, and one more nanosecond covers a fractional center projection.
	hi, lo = bits.Mul64(elapsedUpper, uint64(s.maxDriftPPB))
	drift, remainder := bits.Div64(hi, lo, uint64(time.Second))
	if remainder != 0 {
		drift++
	}
	hi, lo = bits.Mul64(elapsedUpper, s.periodErrFrac)
	estimate, estimateFractional, err := shift192([3]uint64{lo, hi, 0}, 64+int(s.periodErrShift))
	if err != nil {
		return Result{}, err
	}
	if estimateFractional {
		estimate++
	}
	bound := uint64(s.boundNanos)
	for _, extra := range [...]uint64{drift, estimate, elapsedUpper - elapsed} {
		if extra > math.MaxInt64 || bound > math.MaxInt64-extra {
			return Result{}, ErrOutOfRange
		}
		bound += extra
	}
	status := s.status
	if status == StatusSynchronized || status == StatusFreeRunning {
		switch {
		case time.Duration(elapsedUpper) > s.voidAfter.Sub(s.asOf):
			status = StatusUnknown
		case time.Duration(elapsedUpper) > freeRunningGrace:
			status = StatusFreeRunning
		}
	}
	errorBound := time.Duration(bound)
	return Result{Earliest: center.Add(-errorBound), Latest: center.Add(errorBound), Status: status, ErrorBound: errorBound}, nil
}

// shift192 divides a little-endian 192-bit integer by 2^shift. It reports
// discarded fractional bits and rejects a quotient wider than uint64.
func shift192(words [3]uint64, shift int) (uint64, bool, error) {
	switch {
	case shift >= 192:
		return 0, words[0]|words[1]|words[2] != 0, nil
	case shift >= 128:
		n := uint(shift - 128)
		fractional := words[0]|words[1]|(words[2]&((uint64(1)<<n)-1)) != 0
		return words[2] >> n, fractional, nil
	case shift >= 64:
		n := uint(shift - 64)
		if words[2]>>n != 0 {
			return 0, false, ErrOutOfRange
		}
		quotient := words[1]>>n | words[2]<<(64-n)
		fractional := words[0]|(words[1]&((uint64(1)<<n)-1)) != 0
		return quotient, fractional, nil
	default:
		n := uint(shift)
		if words[2] != 0 || words[1]>>n != 0 {
			return 0, false, ErrOutOfRange
		}
		quotient := words[0]>>n | words[1]<<(64-n)
		return quotient, words[0]&((uint64(1)<<n)-1) != 0, nil
	}
}
