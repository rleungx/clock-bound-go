// Package clockbound reads bounded timestamps from the AWS ClockBound daemon.
// It requires the daemon's v3 shared-memory segment on the same Linux host.
package clockbound

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
)

const (
	DefaultClockBoundPath = "/var/run/clockbound/shm1"
	DefaultVMClockPath    = "/dev/vmclock0"
)

var (
	ErrClosed              = errors.New("clockbound client is closed or uninitialized")
	ErrUnsupportedPlatform = errors.New("clockbound requires Linux amd64 or arm64")
	ErrMalformedSegment    = errors.New("malformed clockbound shared-memory segment")
	ErrUnsupportedVersion  = errors.New("unsupported shared-memory version")
	ErrNotInitialized      = errors.New("shared-memory segment is not initialized")
	ErrSnapshotBusy        = errors.New("could not read a consistent shared-memory snapshot")
	ErrCounterBackwards    = errors.New("hardware counter precedes the clock snapshot")
	ErrOutOfRange          = errors.New("clockbound time or error bound is outside the supported range")
)

// Status describes whether the daemon can bound its clock's error.
type Status int32

const (
	StatusUnknown Status = iota
	StatusSynchronized
	StatusFreeRunning
	StatusDisrupted
)

func (s Status) String() string {
	switch s {
	case StatusUnknown:
		return "UNKNOWN"
	case StatusSynchronized:
		return "SYNCHRONIZED"
	case StatusFreeRunning:
		return "FREE_RUNNING"
	case StatusDisrupted:
		return "DISRUPTED"
	default:
		return fmt.Sprintf("STATUS(%d)", s)
	}
}

// Result bounds true time at the hardware-counter sample inside Now, provided
// IsTrusted returns true. It does not describe the later time when Now returns.
// Times are in UTC and carry no Go process monotonic-clock readings.
type Result struct {
	Earliest time.Time
	Latest   time.Time
	Status   Status
	// ErrorBound is half the interval width, rounded outward to nanoseconds.
	ErrorBound time.Duration
}

// IsTrusted reports whether this result has a valid, usable time interval.
func (r Result) IsTrusted() bool {
	return (r.Status == StatusSynchronized || r.Status == StatusFreeRunning) &&
		!r.Earliest.After(r.Latest) && r.ErrorBound >= 0
}

// DefinitelyBefore requires both intervals to be trusted and strictly disjoint.
// Overlapping intervals cannot establish a strict real-time order.
func DefinitelyBefore(a, b Result) bool {
	return a.IsTrusted() && b.IsTrusted() && a.Latest.Before(b.Earliest)
}

// Client is safe for concurrent use. Do not copy a Client. Its zero value is
// closed. Call New or NewWithPaths to create one, and Close to release mappings.
type Client struct {
	mu          sync.Mutex
	clockBound  *mappedSegment
	vmClock     *mappedSegment
	vmClockPath string
	snapshot    clockSnapshot
	closed      bool
}

func New() (*Client, error) {
	return NewWithPaths(DefaultClockBoundPath, DefaultVMClockPath)
}

func NewWithPath(clockBoundPath string) (*Client, error) {
	return NewWithPaths(clockBoundPath, DefaultVMClockPath)
}

// NewWithPaths opens a ClockBound v3 segment and, when requested by the daemon,
// a VMClock v1 device. It never disables a requested disruption check silently.
func NewWithPaths(clockBoundPath, vmClockPath string) (*Client, error) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return nil, ErrUnsupportedPlatform
	}
	return openClient(clockBoundPath, vmClockPath)
}

func openClient(clockBoundPath, vmClockPath string) (*Client, error) {
	cb, err := mapSegment(clockBoundPath, clockBoundSegment)
	if err != nil {
		return nil, fmt.Errorf("open ClockBound: %w", err)
	}
	snap, err := readClockSnapshot(cb.data, nil)
	if err != nil {
		_ = cb.close()
		return nil, err
	}
	c := &Client{clockBound: cb, vmClockPath: vmClockPath, snapshot: snap}
	if snap.disruptionOn {
		if err := c.ensureVMClock(); err != nil {
			_ = c.Close()
			return nil, err
		}
		if _, err := readVMClockMarker(c.vmClock.data); err != nil {
			_ = c.Close()
			return nil, err
		}
	}
	return c, nil
}

// Now samples the hardware counter, projects it using daemon calibration, and
// checks VMClock for disruptions. Call IsTrusted even when err is nil.
func (c *Client) Now() (Result, error) { return c.now(readTSC) }

func (c *Client) now(counter func() (uint64, error)) (Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.clockBound == nil {
		return Result{}, ErrClosed
	}
	snap, err := readClockSnapshot(c.clockBound.data, &c.snapshot)
	if err != nil {
		return Result{}, err
	}
	c.snapshot = snap
	tsc, err := counter()
	if err != nil {
		return Result{}, err
	}
	result, err := snap.atTSC(tsc)
	if err != nil {
		return Result{}, err
	}
	if snap.disruptionOn || c.vmClock != nil {
		if err := c.ensureVMClock(); err != nil {
			return Result{}, err
		}
		marker, err := readVMClockMarker(c.vmClock.data)
		if err != nil {
			return Result{}, err
		}
		if marker != snap.disruption {
			result.Status = StatusDisrupted
		}
	}
	return result, nil
}

func (c *Client) ensureVMClock() error {
	if c.vmClock != nil {
		return nil
	}
	vm, err := mapSegment(c.vmClockPath, vmClockSegment)
	if err != nil {
		return fmt.Errorf("open required VMClock: %w", err)
	}
	c.vmClock = vm
	return nil
}

// Close releases both mappings. Concurrent Now calls complete before unmapping.
// Closing a Client more than once, or closing its zero value, is harmless.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	var errs []error
	if c.vmClock != nil {
		errs = append(errs, c.vmClock.close())
	}
	if c.clockBound != nil {
		errs = append(errs, c.clockBound.close())
	}
	return errors.Join(errs...)
}
