package ratelimit_test

import (
	"testing"
	"time"

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
