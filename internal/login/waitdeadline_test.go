package login

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A wait deadline shortens the gate wait, and a passed one makes it fail at
// once. A gate with room still grants it after the deadline.
func TestWaitDeadlineShortensTheGateWait(t *testing.T) {
	opt := DefaultOptions()
	opt.Gate = NewGate(1024)
	s := NewWith(nil, opt)
	s.GateWait = 5 * time.Second
	past := WithWaitDeadline(context.Background(), time.Now().Add(-time.Second))
	release, err := s.Hold(past)
	if err != nil {
		t.Fatalf("a hold past the deadline with room: %v", err)
	}
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
	release()
}
