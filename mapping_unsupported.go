//go:build (!linux && !darwin) || (!amd64 && !arm64)

package clockbound

import (
	"errors"
	"os"
)

type segmentKind int

const (
	clockBoundSegment segmentKind = iota
	vmClockSegment
)

type mappedSegment struct {
	file *os.File
	data []byte
}

func mapSegment(string, segmentKind) (*mappedSegment, error) {
	return nil, ErrUnsupportedPlatform
}

func (m *mappedSegment) close() error {
	return errors.New("shared memory is unavailable on this platform")
}
