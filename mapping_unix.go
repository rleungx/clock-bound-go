//go:build (linux || darwin) && (amd64 || arm64)

package clockbound

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"syscall"
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

func mapSegment(path string, kind segmentKind) (*mappedSegment, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	closeOnError := func(err error) (*mappedSegment, error) {
		_ = f.Close()
		return nil, err
	}

	var header [16]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return closeOnError(fmt.Errorf("read shared-memory header: %w", err))
	}

	var segmentSize uint32
	switch kind {
	case clockBoundSegment:
		if binary.LittleEndian.Uint32(header[0:4]) != 0x414D5A4E || binary.LittleEndian.Uint32(header[4:8]) != 0x43420200 {
			return closeOnError(fmt.Errorf("%w: %s is not a ClockBound segment", ErrMalformedSegment, path))
		}
		segmentSize = binary.LittleEndian.Uint32(header[8:12])
	case vmClockSegment:
		if binary.LittleEndian.Uint32(header[0:4]) != 0x4B4C4356 {
			return closeOnError(fmt.Errorf("%w: %s is not a VMClock segment", ErrMalformedSegment, path))
		}
		segmentSize = binary.LittleEndian.Uint32(header[4:8])
	default:
		return closeOnError(fmt.Errorf("unknown shared-memory segment kind %d", kind))
	}
	if segmentSize < 104 || segmentSize > 1<<20 {
		return closeOnError(fmt.Errorf("%w: invalid shared-memory size %d", ErrMalformedSegment, segmentSize))
	}

	info, err := f.Stat()
	if err != nil {
		return closeOnError(err)
	}
	if info.Mode().IsRegular() && info.Size() < int64(segmentSize) {
		return closeOnError(fmt.Errorf("%w: file length %d is smaller than advertised size %d", ErrMalformedSegment, info.Size(), segmentSize))
	}

	data, err := syscall.Mmap(int(f.Fd()), 0, int(segmentSize), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return closeOnError(fmt.Errorf("mmap %s: %w", path, err))
	}
	return &mappedSegment{file: f, data: data}, nil
}

func (m *mappedSegment) close() error {
	if m == nil {
		return nil
	}
	var err error
	if m.data != nil {
		err = syscall.Munmap(m.data)
		m.data = nil
	}
	if m.file != nil {
		if closeErr := m.file.Close(); err == nil {
			err = closeErr
		}
		m.file = nil
	}
	return err
}
