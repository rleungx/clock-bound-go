package clockbound

import (
	"fmt"
	"testing"
	"time"
)

func TestResultOrderRequiresTrustAndStrictSeparation(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := Result{Earliest: base, Latest: base.Add(time.Second), Status: StatusSynchronized}
	b := Result{Earliest: base.Add(2 * time.Second), Latest: base.Add(3 * time.Second), Status: StatusFreeRunning}
	if !DefinitelyBefore(a, b) || DefinitelyBefore(b, a) {
		t.Fatal("strict order failed")
	}
	b.Earliest = a.Latest
	if DefinitelyBefore(a, b) {
		t.Fatal("touching intervals ordered")
	}
	b.Earliest = base.Add(2 * time.Second)
	for _, status := range []Status{StatusUnknown, StatusDisrupted, Status(99)} {
		b.Status = status
		if DefinitelyBefore(a, b) || b.IsTrusted() {
			t.Fatal("untrusted interval ordered")
		}
	}
	b.Status = StatusSynchronized
	b.Earliest = b.Latest.Add(time.Second)
	if b.IsTrusted() {
		t.Fatal("invalid interval trusted")
	}
	for _, s := range []Status{StatusUnknown, StatusSynchronized, StatusFreeRunning, StatusDisrupted, Status(99)} {
		if s.String() == "" {
			t.Fatal("missing status string")
		}
	}
}

func ExampleDefinitelyBefore() {
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := Result{Earliest: t, Latest: t.Add(time.Millisecond), Status: StatusSynchronized}
	b := Result{Earliest: t.Add(2 * time.Millisecond), Latest: t.Add(3 * time.Millisecond), Status: StatusFreeRunning}
	fmt.Println(DefinitelyBefore(a, b))
	// Output: true
}
