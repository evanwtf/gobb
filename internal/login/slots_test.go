package login

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/metrics"
)

// A check that finds every slot taken gets ErrBusy at once and counts; the
// slot is free again after the release, and after a failed gate wait.
func TestSlotsBoundTheChecks(t *testing.T) {
	opt := DefaultOptions()
	opt.Gate = NewGate(1024)
	s := NewWith(nil, opt)
	s.GateWait = 50 * time.Millisecond
	s.Slots = make(chan struct{}, 1)

	release, err := s.Hold(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Slots) != 1 {
		t.Fatalf("a hold took %d slots, want 1", len(s.Slots))
	}
	before := metrics.InFlightRefusals.Value("signin")
	start := time.Now()
	if _, err := s.Hold(t.Context()); !errors.Is(err, ErrBusy) {
		t.Fatalf("a hold with no slot: %v, want ErrBusy", err)
	}
	if took := time.Since(start); took > 20*time.Millisecond {
		t.Errorf("the refusal waited %s; it must not wait", took)
	}
	if got := metrics.InFlightRefusals.Value("signin") - before; got != 1 {
		t.Errorf("signin refusals rose by %d, want 1", got)
	}
	release()
	release() // a second call does nothing
	if len(s.Slots) != 0 {
		t.Fatalf("%d slots held after the release", len(s.Slots))
	}

	// The gate is full and the slot free: the wait fails, and the slot comes back.
	hold, err := opt.Gate.Acquire(t.Context(), 1024, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Hold(t.Context()); !errors.Is(err, ErrBusy) {
		t.Fatalf("a hold with the gate full: %v, want ErrBusy", err)
	}
	hold()
	if len(s.Slots) != 0 {
		t.Errorf("%d slots held after a failed gate wait", len(s.Slots))
	}
}

// A wait deadline shortens the gate wait, and a passed one makes it fail at once.
func TestWaitDeadlineShortensTheGateWait(t *testing.T) {
	opt := DefaultOptions()
	opt.Gate = NewGate(1024)
	s := NewWith(nil, opt)
	s.GateWait = 5 * time.Second
	hold, err := opt.Gate.Acquire(t.Context(), 1024, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	for _, left := range []time.Duration{100 * time.Millisecond, -time.Second} {
		ctx := WithWaitDeadline(context.Background(), time.Now().Add(left))
		start := time.Now()
		if _, err := s.Hold(ctx); !errors.Is(err, ErrBusy) {
			t.Fatalf("left %s: %v, want ErrBusy", left, err)
		}
		if took := time.Since(start); took > max(left, 0)+300*time.Millisecond {
			t.Errorf("left %s: the wait took %s", left, took)
		}
	}
}

// A check that panics gives its slot back.
func TestSlotFreedAfterAPanickingCheck(t *testing.T) {
	opt := DefaultOptions()
	opt.Gate = NewGate(1024)
	opt.Schedule = nil
	opt.Verify = func(string, string, string) (bool, bool) { panic("boom") }
	s := NewWith(nil, opt)
	s.Floor = 0
	s.Slots = make(chan struct{}, 1)
	func() {
		defer func() {
			if p := recover(); p != "boom" {
				t.Fatalf("recovered %v, want the panic of the check", p)
			}
		}()
		// An invalid name runs the dummy check and touches no database.
		_, _ = s.Check(t.Context(), "\xff", "pw", netip.Addr{}, time.Now())
	}()
	if n := len(s.Slots); n != 0 {
		t.Errorf("%d slots held after a panic, want 0", n)
	}
}

// A sign-in holds one slot over its whole check, and a sign-in that finds no
// slot gets ErrBusy without a check.
func TestSigninHoldsOneSlotOverTheCheck(t *testing.T) {
	opt := DefaultOptions()
	opt.Gate = NewGate(1024)
	opt.Schedule = nil
	s := NewWith(nil, opt)
	s.Floor = 0
	s.Slots = make(chan struct{}, 1)
	var during int
	s.opt.Verify = func(string, string, string) (bool, bool) { during = len(s.Slots); return false, false }
	if _, err := s.Check(t.Context(), "\xff", "pw", netip.Addr{}, time.Now()); !errors.Is(err, ErrWrong) {
		t.Fatalf("a check with a free slot: %v, want ErrWrong", err)
	}
	if during != 1 || len(s.Slots) != 0 {
		t.Errorf("slots during the check %d and after %d, want 1 and 0", during, len(s.Slots))
	}
	s.Slots <- struct{}{}
	during = -1
	if _, err := s.Check(t.Context(), "\xff", "pw", netip.Addr{}, time.Now()); !errors.Is(err, ErrBusy) {
		t.Fatalf("a check with no slot: %v, want ErrBusy", err)
	}
	if during != -1 {
		t.Error("the check ran without a slot")
	}
}
