package ratelimit_test

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/metrics"
	"github.com/evanwtf/gobb/internal/ratelimit"
)

var t0 = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func TestLimiter(t *testing.T) {
	l := ratelimit.New(3, time.Minute, 10)
	for i := range 2 {
		l.Add("k", t0.Add(time.Duration(i)*10*time.Second))
	}
	if over, _ := l.Over("k", t0.Add(20*time.Second)); over {
		t.Fatal("Over = true after 2 events with limit 3")
	}
	l.Add("k", t0.Add(20*time.Second))
	over, wait := l.Over("k", t0.Add(30*time.Second))
	if !over {
		t.Fatal("Over = false after 3 events with limit 3")
	}
	// The oldest event is at t0 and expires at t0+1m; now is t0+30s.
	if wait != 30*time.Second {
		t.Errorf("wait = %v, want 30s", wait)
	}
	if over, _ := l.Over("other", t0.Add(30*time.Second)); over {
		t.Error("another key is over the limit")
	}
	// One event expires: the key has room again.
	if over, _ := l.Over("k", t0.Add(61*time.Second)); over {
		t.Error("Over = true after the oldest event left the window")
	}
	if over, _ := l.Over("k", t0.Add(2*time.Minute)); over {
		t.Error("Over = true after the whole window")
	}
}

func TestLimiterReset(t *testing.T) {
	l := ratelimit.New(2, time.Minute, 10)
	l.Add("k", t0)
	l.Add("k", t0)
	if over, _ := l.Over("k", t0); !over {
		t.Fatal("Over = false at the limit")
	}
	l.Reset("k")
	if over, _ := l.Over("k", t0); over {
		t.Error("Over = true after Reset")
	}
	l.Reset("never added") // no panic
}

func TestLimiterWindowSlides(t *testing.T) {
	l := ratelimit.New(2, time.Minute, 10)
	l.Add("k", t0)
	l.Add("k", t0.Add(50*time.Second))
	// At t0+61s the first event is out; one more fills the window again.
	l.Add("k", t0.Add(61*time.Second))
	over, wait := l.Over("k", t0.Add(62*time.Second))
	if !over {
		t.Fatal("Over = false with 2 events in the window")
	}
	if wait != 48*time.Second {
		t.Errorf("wait = %v, want 48s (the event at 50s expires at 110s)", wait)
	}
}

func TestLimiterMaxKeys(t *testing.T) {
	l := ratelimit.New(1, time.Hour, 2)
	l.Add("old", t0)
	l.Add("newer", t0.Add(time.Minute))
	l.Add("newest", t0.Add(2*time.Minute)) // drops "old"
	now := t0.Add(3 * time.Minute)
	if over, _ := l.Over("old", now); over {
		t.Error("the oldest key was kept")
	}
	for _, k := range []string{"newer", "newest"} {
		if over, _ := l.Over(k, now); !over {
			t.Errorf("key %q was dropped", k)
		}
	}
	if n := l.Len(); n != 2 {
		t.Errorf("Len = %d, want 2", n)
	}
}

func TestLimiterMaxKeysDropsExpiredFirst(t *testing.T) {
	l := ratelimit.New(1, time.Minute, 2)
	l.Add("a", t0)                     // expires at t0+1m
	l.Add("b", t0.Add(30*time.Second)) // still live later
	// "a" is expired at t0+70s, "b" is not. A third key drops "a" (expired), not "b".
	l.Add("c", t0.Add(70*time.Second))
	now := t0.Add(80 * time.Second)
	if over, _ := l.Over("b", now); !over {
		t.Error("a live key was dropped while an expired key stood")
	}
	if over, _ := l.Over("c", now); !over {
		t.Error("the new key was not kept")
	}
	if n := l.Len(); n != 2 {
		t.Errorf("Len = %d, want 2", n)
	}
}

func TestLimiterAddKnownKeyKeepsSize(t *testing.T) {
	l := ratelimit.New(5, time.Hour, 2)
	l.Add("a", t0)
	l.Add("b", t0)
	for i := range 10 {
		l.Add("a", t0.Add(time.Duration(i)*time.Second))
	}
	if n := l.Len(); n != 2 {
		t.Errorf("Len = %d, want 2", n)
	}
}

func TestTake(t *testing.T) {
	l := ratelimit.New(3, time.Minute, 10)
	for i := range 3 {
		ok, _ := l.Take("k", t0.Add(time.Duration(i)*10*time.Second))
		if !ok {
			t.Fatalf("Take %d refused under the limit", i+1)
		}
	}
	ok, wait := l.Take("k", t0.Add(30*time.Second))
	if ok {
		t.Fatal("4th Take allowed with limit 3")
	}
	if wait != 30*time.Second {
		t.Errorf("wait = %v, want 30s", wait)
	}
	// A refused Take records nothing: the oldest event still leaves at t0+1m.
	if ok, _ := l.Take("k", t0.Add(61*time.Second)); !ok {
		t.Error("Take refused after the oldest event left")
	}
	if ok, _ := l.Take("other", t0); !ok {
		t.Error("another key refused")
	}
}

// Take is one step under the lock: parallel callers get exactly limit grants.
func TestTakeParallel(t *testing.T) {
	l := ratelimit.New(10, time.Hour, 10)
	var granted atomic.Int64
	var wg sync.WaitGroup
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := l.Take("k", t0); ok {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := granted.Load(); got != 10 {
		t.Errorf("granted %d, want 10", got)
	}
}

func TestRefund(t *testing.T) {
	l := ratelimit.New(2, time.Minute, 10)
	l.Take("k", t0)
	l.Take("k", t0.Add(time.Second))
	if over, _ := l.Over("k", t0.Add(2*time.Second)); !over {
		t.Fatal("not over at the limit")
	}
	l.Refund("k", t0.Add(time.Second))
	if over, _ := l.Over("k", t0.Add(2*time.Second)); over {
		t.Error("still over after a refund")
	}
	// The event at t0 is the one that stays: it leaves at t0+1m.
	l.Take("k", t0.Add(3*time.Second))
	if over, wait := l.Over("k", t0.Add(4*time.Second)); !over || wait != 56*time.Second {
		t.Errorf("Over = %v, %v; want true, 56s", over, wait)
	}
	l.Refund("never", t0) // no panic
	l.Refund("k", t0.Add(time.Hour))
	if over, _ := l.Over("k", t0.Add(4*time.Second)); !over {
		t.Error("a refund of an event that does not exist removed another")
	}
	// Refunding the last event drops the key.
	one := ratelimit.New(1, time.Minute, 10)
	one.Take("a", t0)
	one.Refund("a", t0)
	if n := one.Len(); n != 0 {
		t.Errorf("Len = %d after the only event was refunded, want 0", n)
	}
}

// A key that is over its limit survives a flood of keys with one event each.
func TestLimiterEvictionKeepsLockedKey(t *testing.T) {
	l := ratelimit.New(20, time.Hour, 100)
	for i := range 20 {
		l.Add("locked", t0.Add(time.Duration(i)*time.Second))
	}
	if over, _ := l.Over("locked", t0.Add(time.Minute)); !over {
		t.Fatal("precondition: locked is over its limit")
	}
	for i := range 10000 {
		l.Add(fmt.Sprintf("junk-%d", i), t0.Add(time.Minute+time.Duration(i)*time.Millisecond))
	}
	if over, _ := l.Over("locked", t0.Add(2*time.Minute)); !over {
		t.Error("a flood of one-event keys freed a locked key")
	}
	if n := l.Len(); n > 100 {
		t.Errorf("Len = %d, want at most 100", n)
	}
}

// A key with fewer events goes before a key with more; with equal counts, the
// key with the oldest last event goes first.
func TestLimiterEvictionOrder(t *testing.T) {
	l := ratelimit.New(5, time.Hour, 3)
	l.Add("two", t0)
	l.Add("two", t0.Add(time.Second))
	l.Add("old", t0.Add(time.Minute))
	l.Add("new", t0.Add(2*time.Minute))
	l.Add("x", t0.Add(3*time.Minute)) // full: drops "old", the oldest of the one-event keys
	if l.Has("old") || !l.Has("two") || !l.Has("new") || !l.Has("x") {
		t.Fatalf("after x: old=%t two=%t new=%t x=%t", l.Has("old"), l.Has("two"), l.Has("new"), l.Has("x"))
	}
	l.Add("y", t0.Add(4*time.Minute)) // drops "new", not "two" with its two events
	if l.Has("new") || !l.Has("two") || !l.Has("x") || !l.Has("y") {
		t.Errorf("after y: new=%t two=%t x=%t y=%t", l.Has("new"), l.Has("two"), l.Has("x"), l.Has("y"))
	}
}

// Expired keys go first, even when a live key has fewer events: here the expired
// key has three events, and each live key has one. Only the expired-first rule
// picks it.
func TestLimiterEvictionExpiredFirst(t *testing.T) {
	l := ratelimit.New(5, time.Hour, 3)
	now := t0.Add(4 * time.Hour)
	for i := range 3 {
		l.Add("expired", t0.Add(time.Duration(i)*time.Minute))
	}
	l.Add("live1", now.Add(-30*time.Minute))
	l.Add("live2", now.Add(-20*time.Minute))
	if l.Len() != 3 {
		t.Fatalf("precondition: Len = %d", l.Len())
	}
	l.Add("new", now)
	if l.Has("expired") || !l.Has("live1") || !l.Has("live2") || !l.Has("new") {
		t.Errorf("expired=%t live1=%t live2=%t new=%t; want false, true, true, true",
			l.Has("expired"), l.Has("live1"), l.Has("live2"), l.Has("new"))
	}
}

// A full limiter makes room for a batch of keys at once, so the scan does not
// run for every new key. The order is the same, and Len stays within maxKeys.
func TestLimiterEvictionBatch(t *testing.T) {
	l := ratelimit.New(5, time.Hour, 1000)
	for i := range 1000 {
		// Keys 0 to 9 have two events; the rest have one. Older keys have lower numbers.
		k := fmt.Sprintf("k%04d", i)
		l.Add(k, t0.Add(time.Duration(i)*time.Second))
		if i < 10 {
			l.Add(k, t0.Add(time.Hour/2+time.Duration(i)*time.Second))
		}
	}
	now := t0.Add(40 * time.Minute)
	l.Add("fresh", now)
	if l.Len() > 1000 {
		t.Fatalf("Len = %d, want at most 1000", l.Len())
	}
	if l.Len() > 991 {
		t.Errorf("Len = %d after the first eviction, want a batch of about 1%% gone", l.Len())
	}
	for i := range 10 {
		if k := fmt.Sprintf("k%04d", i); !l.Has(k) {
			t.Errorf("%s (two events) was evicted before keys with one", k)
		}
	}
	if l.Has("k0010") {
		t.Error("the oldest one-event key stayed")
	}
	if !l.Has("fresh") {
		t.Error("the new key is gone")
	}
}

// BenchmarkAddNewKeyFull measures Add of a new key into a limiter that holds
// 10,000 live keys, the cost of a flood of new names.
func BenchmarkAddNewKeyFull(b *testing.B) {
	l := ratelimit.New(20, time.Hour, 10000)
	for i := range 10000 {
		key := fmt.Sprintf("seed-%d", i)
		for j := range 1 + i%5 {
			l.Add(key, t0.Add(time.Duration(i*10+j)*time.Millisecond))
		}
	}
	now := t0.Add(time.Minute)
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		l.Add(fmt.Sprintf("flood-%d", i), now)
	}
}

// A named limiter counts each check that finds the key full, by Take and by
// Over, under its name. Its series exists at 0 from the start.
func TestNamedCountsRefusals(t *testing.T) {
	l := ratelimit.New(1, time.Minute, 10).Named("test_named")
	if got := metrics.RateLimited.Value("test_named"); got != 0 {
		t.Fatalf("a new name starts at %d, want 0", got)
	}
	if ok, _ := l.Take("k", t0); !ok {
		t.Fatal("the first Take was refused")
	}
	if over, _ := l.Over("other", t0); over {
		t.Fatal("another key is full")
	}
	if ok, _ := l.Take("k", t0); ok {
		t.Fatal("the second Take passed a limit of 1")
	}
	if over, _ := l.Over("k", t0); !over {
		t.Fatal("Over = false at the limit")
	}
	if got := metrics.RateLimited.Value("test_named"); got != 2 {
		t.Errorf("refusals = %d, want 2 (one Take, one Over)", got)
	}
}
