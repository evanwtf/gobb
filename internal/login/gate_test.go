package login_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/login"
)

// acquireAsync starts Acquire in a goroutine. The channel gets the release
// function (or the error) when the call returns.
type grant struct {
	release func()
	err     error
}

func acquireAsync(g *login.Gate, kib uint64, wait time.Duration) <-chan grant {
	out := make(chan grant, 1)
	go func() {
		rel, err := g.Acquire(context.Background(), kib, wait)
		out <- grant{rel, err}
	}()
	return out
}

func TestGate(t *testing.T) {
	g := login.NewGate(100)
	first, err := g.Acquire(t.Context(), 60, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	second := acquireAsync(g, 60, 10*time.Second)
	select {
	case <-second:
		t.Fatal("two holders of 60 ran together on a budget of 100")
	case <-time.After(100 * time.Millisecond):
	}
	first()
	select {
	case got := <-second:
		if got.err != nil {
			t.Fatalf("second holder: %v", got.err)
		}
		got.release()
	case <-time.After(5 * time.Second):
		t.Fatal("the second holder did not run after the first released")
	}

	// Two holders of 40 fit together.
	a, err := g.Acquire(t.Context(), 40, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b, err := g.Acquire(t.Context(), 40, time.Second)
	if err != nil {
		t.Fatalf("two holders of 40 on a budget of 100: %v", err)
	}
	a()
	b()

	// A cost over the budget counts as the whole budget, and runs alone.
	big, err := g.Acquire(t.Context(), 500, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Acquire(t.Context(), 1, 50*time.Millisecond); !errors.Is(err, login.ErrBusy) {
		t.Errorf("a holder next to a holder of 500: %v, want ErrBusy", err)
	}
	big()
	small, err := g.Acquire(t.Context(), 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Acquire(t.Context(), 500, 50*time.Millisecond); !errors.Is(err, login.ErrBusy) {
		t.Errorf("a holder of 500 next to a holder of 1: %v, want ErrBusy", err)
	}
	small()
}

func TestGateBusy(t *testing.T) {
	g := login.NewGate(100)
	full, err := g.Acquire(t.Context(), 100, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := g.Acquire(t.Context(), 10, 50*time.Millisecond); !errors.Is(err, login.ErrBusy) {
		t.Fatalf("full gate: %v, want ErrBusy", err)
	}
	if took := time.Since(start); took < 50*time.Millisecond || took > 5*time.Second {
		t.Errorf("Acquire waited %v, want about 50ms", took)
	}
	// A waiter that gave up leaves no trace: the gate works after the release.
	full()
	again, err := g.Acquire(t.Context(), 100, time.Second)
	if err != nil {
		t.Fatalf("after a timed-out waiter: %v", err)
	}
	again()
}

func TestGateContext(t *testing.T) {
	g := login.NewGate(10)
	full, err := g.Acquire(t.Context(), 10, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer full()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := g.Acquire(ctx, 5, time.Minute)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Acquire ignored its context")
	}
}

// A release can run twice and frees the cost once.
func TestGateReleaseOnce(t *testing.T) {
	g := login.NewGate(100)
	rel, err := g.Acquire(t.Context(), 100, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	rel()
	a, err := g.Acquire(t.Context(), 60, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer a()
	if _, err := g.Acquire(t.Context(), 60, 50*time.Millisecond); !errors.Is(err, login.ErrBusy) {
		t.Errorf("a second release gave back memory it did not hold: %v", err)
	}
}

// Waiters run in arrival order: a stream of small holders does not starve a
// large one.
func TestGateFairness(t *testing.T) {
	g := login.NewGate(100)
	hold, err := g.Acquire(t.Context(), 60, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	large := acquireAsync(g, 60, 10*time.Second)
	time.Sleep(50 * time.Millisecond)
	small := acquireAsync(g, 10, 10*time.Second)
	select {
	case <-small:
		t.Fatal("a late small holder passed a waiting large one")
	case <-time.After(100 * time.Millisecond):
	}
	hold()
	var wg sync.WaitGroup
	for _, ch := range []<-chan grant{large, small} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case got := <-ch:
				if got.err != nil {
					t.Errorf("waiter: %v", got.err)
					return
				}
				got.release()
			case <-time.After(5 * time.Second):
				t.Error("a waiter did not run")
			}
		}()
	}
	wg.Wait()
}

// Budget returns the budget the gate was made with, in KiB, whatever is held.
func TestGateBudget(t *testing.T) {
	g := login.NewGate(100)
	if got := g.Budget(); got != 100 {
		t.Errorf("Budget() = %d, want 100", got)
	}
	rel, err := g.Acquire(t.Context(), 60, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	if got := g.Budget(); got != 100 {
		t.Errorf("Budget() with 60 held = %d, want 100", got)
	}
	if got := login.NewGate(login.DefaultBudgetKiB).Budget(); got != login.DefaultBudgetKiB {
		t.Errorf("Budget() = %d, want %d", got, login.DefaultBudgetKiB)
	}
}
