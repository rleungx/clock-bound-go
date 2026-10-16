//go:build (linux || darwin) && (amd64 || arm64)

package clockbound

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func fixtureFile(t testing.TB, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func publish(t testing.TB, path string, data []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, part := range []struct {
		off   int64
		value []byte
	}{{14, []byte{3, 0}}, {16, data[16:]}, {14, []byte{4, 0}}} {
		if _, err := f.WriteAt(part.value, part.off); err != nil {
			t.Fatal(err)
		}
	}
}

func fixedCounter() (uint64, error) { return 2024, nil }

func TestMappedClientLifecycleAndConcurrentClose(t *testing.T) {
	path := fixtureFile(t, "clockbound", golden(t))
	client, err := openClient(path, "/missing-vmclock-unused")
	if err != nil {
		t.Fatal(err)
	}
	r, err := client.now(fixedCounter)
	if err != nil || !r.IsTrusted() {
		t.Fatalf("initial read: %+v %v", r, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				got, err := client.now(fixedCounter)
				if err != nil && !errors.Is(err, ErrClosed) {
					t.Errorf("concurrent read: %v", err)
					return
				}
				if err == nil && !got.IsTrusted() {
					t.Errorf("unexpected untrusted result: %+v", got)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.now(fixedCounter); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if client.clockBound.data != nil || client.clockBound.file != nil {
		t.Fatal("mapping or descriptor not released")
	}
	var zero Client
	if _, err := zero.Now(); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := zero.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDisruptionDetectedAfterCounterSample(t *testing.T) {
	data := golden(t)
	data[96] = 1
	cbPath := fixtureFile(t, "clockbound", data)
	vmPath := fixtureFile(t, "vmclock", vmFixture(42))
	c, err := openClient(cbPath, vmPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r, err := c.now(fixedCounter)
	if err != nil || !r.IsTrusted() {
		t.Fatalf("matching marker: %+v %v", r, err)
	}
	r, err = c.now(func() (uint64, error) {
		f, err := os.OpenFile(vmPath, os.O_WRONLY, 0)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		changed := vmFixture(43)
		_, err = f.WriteAt(changed[12:24], 12)
		return 2024, err
	})
	if err != nil || r.Status != StatusDisrupted || r.IsTrusted() {
		t.Fatalf("missed disruption: %+v %v", r, err)
	}
}

func TestDisruptionSupportCanBeEnabledAfterRestart(t *testing.T) {
	data := golden(t)
	cbPath := fixtureFile(t, "clockbound", data)
	vmPath := filepath.Join(t.TempDir(), "vmclock")
	c, err := openClient(cbPath, vmPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	data[96] = 1
	publish(t, cbPath, data)
	for i := 0; i < 2; i++ {
		if _, err := c.now(fixedCounter); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing required VMClock accepted: %v", err)
		}
	}
	if err := os.WriteFile(vmPath, vmFixture(42), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := c.now(fixedCounter)
	if err != nil || !r.IsTrusted() {
		t.Fatalf("did not recover after VMClock appeared: %+v %v", r, err)
	}
	data[96] = 0
	publish(t, cbPath, data)
	r, err = c.now(fixedCounter)
	if err != nil || !r.IsTrusted() {
		t.Fatalf("disabling support: %+v %v", r, err)
	}
}

func TestRequiredVMClockAndCounterFailures(t *testing.T) {
	data := golden(t)
	data[96] = 1
	cbPath := fixtureFile(t, "clockbound", data)
	if _, err := openClient(cbPath, "/missing-vmclock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	invalid := vmFixture(42)
	invalid[8] = 2
	vmPath := fixtureFile(t, "vmclock", invalid)
	if _, err := openClient(cbPath, vmPath); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatal(err)
	}
	data[96] = 0
	if err := os.WriteFile(cbPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := openClient(cbPath, vmPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	injected := errors.New("counter unavailable")
	if _, err := c.now(func() (uint64, error) { return 0, injected }); !errors.Is(err, injected) {
		t.Fatal(err)
	}
}

func TestMappingRejectsTruncationAndBadHeaders(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{"empty", nil}, {"short", make([]byte, 15)},
		{"badmagic", make([]byte, 104)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := fixtureFile(t, tt.name, tt.data)
			if m, err := mapSegment(path, clockBoundSegment); err == nil {
				m.close()
				t.Fatal("accepted corrupt file")
			}
		})
	}
	data := golden(t)
	binary.LittleEndian.PutUint32(data[8:], 8192)
	path := fixtureFile(t, "advertised-longer-than-file", data)
	if _, err := mapSegment(path, clockBoundSegment); !errors.Is(err, ErrMalformedSegment) {
		t.Fatalf("truncated mapping: %v", err)
	}
	binary.LittleEndian.PutUint32(data[8:], 1<<30)
	path = fixtureFile(t, "huge", data)
	if _, err := mapSegment(path, clockBoundSegment); err == nil {
		t.Fatal("accepted huge mapping")
	}
	if _, err := openClient("/missing-clockbound", "/unused"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if runtime.GOOS != "linux" {
		if _, err := NewWithPath(path); !errors.Is(err, ErrUnsupportedPlatform) {
			t.Fatal(err)
		}
	}
}

func TestConcurrentExternalPublication(t *testing.T) {
	initial := golden(t)
	binary.LittleEndian.PutUint64(initial[72:], 42) // bound equals disruption marker
	path := fixtureFile(t, "clockbound", initial)
	mapped, err := mapSegment(path, clockBoundSegment)
	if err != nil {
		t.Fatal(err)
	}
	defer mapped.close()
	writer, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		data := append([]byte(nil), initial...)
		for i := uint16(1); i <= 600; i++ {
			generation := i*2 + 2
			binary.LittleEndian.PutUint16(data[14:], generation-1)
			if _, err := writer.WriteAt(data[14:16], 14); err != nil {
				t.Error(err)
				return
			}
			binary.LittleEndian.PutUint64(data[72:], uint64(i))
			binary.LittleEndian.PutUint64(data[80:], uint64(i))
			if _, err := writer.WriteAt(data[16:], 16); err != nil {
				t.Error(err)
				return
			}
			runtime.Gosched()
			binary.LittleEndian.PutUint16(data[14:], generation)
			if _, err := writer.WriteAt(data[14:16], 14); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 800; i++ {
				snap, err := readClockSnapshot(mapped.data, nil)
				if errors.Is(err, ErrNotInitialized) {
					continue
				}
				if err != nil {
					t.Error(err)
					return
				}
				if uint64(snap.boundNanos) != snap.disruption {
					t.Errorf("torn snapshot: bound=%d marker=%d", snap.boundNanos, snap.disruption)
					return
				}
			}
		}()
	}
	wg.Wait()
	final, err := readClockSnapshot(mapped.data, nil)
	if err != nil || final.boundNanos != 600 || final.disruption != 600 {
		t.Fatalf("final publication not visible: %+v %v", final, err)
	}
}

// The pinned Rust client keeps checking a VMClock once it has opened one,
// including after a daemon restart disables the flag. Do not drop that check.
func TestVMClockMonitoringStaysActiveAfterFlagDisabled(t *testing.T) {
	data := golden(t)
	data[96] = 1
	cbPath := fixtureFile(t, "clockbound", data)
	vmPath := fixtureFile(t, "vmclock", vmFixture(42))
	c, err := openClient(cbPath, vmPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	data[96] = 0
	publish(t, cbPath, data)
	f, err := os.OpenFile(vmPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteAt(vmFixture(43)[16:24], 16)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.now(fixedCounter)
	if err != nil || r.Status != StatusDisrupted || r.IsTrusted() {
		t.Fatalf("stopped checking live VMClock: %+v %v", r, err)
	}
}

// These traces exercise the actual pinned Rust SDK's constructor, mappings,
// snapshot cache and VMClock handling. Only the hardware-counter input was
// injected in the isolated Rust test copy; algorithms and reader code are real.
func TestReaderTracesAgainstActualRustClient(t *testing.T) {
	raw, err := os.ReadFile("testdata/rust-reader-traces.json")
	if err != nil {
		t.Fatal(err)
	}
	var traces []struct {
		Name  string `json:"name"`
		Steps []struct {
			Snapshot      string          `json:"snapshot"`
			VMClock       string          `json:"vmclock"`
			Counter       uint64          `json:"counter"`
			Policy        string          `json:"policy"`
			ExpectedBound int64           `json:"expected_bound"`
			Rust          *rustProjection `json:"rust"`
			RustError     string          `json:"rust_error"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(raw, &traces); err != nil {
		t.Fatal(err)
	}
	steps := 0
	for _, trace := range traces {
		t.Run(trace.Name, func(t *testing.T) {
			directory := t.TempDir()
			cbPath, vmPath := filepath.Join(directory, "clockbound"), filepath.Join(directory, "vmclock")
			var client *Client
			defer func() {
				if client != nil {
					client.Close()
				}
			}()
			for index, step := range trace.Steps {
				steps++
				for _, file := range []struct{ path, hex string }{{cbPath, step.Snapshot}, {vmPath, step.VMClock}} {
					bytes, err := hex.DecodeString(file.hex)
					if err != nil {
						t.Fatal(err)
					}
					f, err := os.OpenFile(file.path, os.O_CREATE|os.O_WRONLY, 0600)
					if err != nil {
						t.Fatal(err)
					}
					_, err = f.WriteAt(bytes, 0)
					f.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
				if client == nil {
					client, err = openClient(cbPath, vmPath)
					if err != nil {
						t.Fatal(err)
					}
				}
				got, err := client.now(func() (uint64, error) { return step.Counter, nil })
				if step.RustError != "" {
					if err == nil {
						t.Fatalf("step %d: Rust error %s but Go accepted %+v", index, step.RustError, got)
					}
					continue
				}
				if err != nil || step.Rust == nil {
					t.Fatalf("step %d: Go=%v Rust=%+v", index, err, step.Rust)
				}
				if got.Status != step.Rust.Status {
					t.Fatalf("step %d: Rust status=%v Go=%v", index, step.Rust.Status, got.Status)
				}
				if step.Policy == "fresh_generation_reuse" {
					if client.snapshot.boundNanos != step.ExpectedBound {
						t.Fatal("reused stale calibration after sequence wrap")
					}
					continue
				}
				expectedEarliest := time.Unix(step.Rust.EarliestSec, step.Rust.EarliestNS).UTC()
				expectedLatest := time.Unix(step.Rust.LatestSec, step.Rust.LatestNS).UTC()
				legacyBound := expectedLatest.Sub(expectedEarliest) / 2
				centerShift := got.Earliest.Add(got.ErrorBound).Sub(expectedEarliest.Add(legacyBound))
				if centerShift < -time.Nanosecond || centerShift > time.Nanosecond {
					t.Fatalf("step %d: centers differ by %v", index, centerShift)
				}
				if delta := got.ErrorBound - legacyBound; delta < 0 || delta > 4*time.Nanosecond {
					t.Fatalf("step %d: unexpected bound difference %v", index, delta)
				}
			}
		})
	}
	if len(traces) != 6 || steps != 21 {
		t.Fatalf("incomplete trace corpus: %d traces, %d steps", len(traces), steps)
	}
}
