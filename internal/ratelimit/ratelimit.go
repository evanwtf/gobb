// Package ratelimit counts events per key inside a sliding window.
//
// gobb uses it to limit failed sign-ins, per client address and per name.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter counts events per key. A key is over the limit when it has limit
// events inside the window. The time comes from the caller, so a test needs no
// clock. A Limiter is safe for concurrent use.
type Limiter struct {
	limit   int
	window  time.Duration
	maxKeys int

	mu   sync.Mutex
	keys map[string][]time.Time // events of a key, oldest first; at most limit
}

// New returns a Limiter that allows limit events per key in window, and holds at
// most maxKeys keys. Both limit and maxKeys must be at least 1.
func New(limit int, window time.Duration, maxKeys int) *Limiter {
	return &Limiter{
		limit: max(limit, 1), window: window, maxKeys: max(maxKeys, 1),
		keys: make(map[string][]time.Time),
	}
}

// Over reports whether key has limit events inside the window ending at now. If
// it does, wait is the time until the oldest of them leaves the window, which is
// when the key has room again.
func (l *Limiter) Over(key string, now time.Time) (over bool, wait time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	events := l.live(key, now)
	if len(events) < l.limit {
		return false, 0
	}
	return true, events[0].Add(l.window).Sub(now)
}

// Take is Over and Add in one step under the lock. When key has fewer than limit
// events inside the window, Take records an event at now and returns true. When
// it has limit events, Take records nothing and returns false with the wait until
// the oldest leaves the window. A caller that must not let parallel requests pass
// a limit takes the event first, before the work, and gives it back with
// [Limiter.Refund] when the work does not count.
func (l *Limiter) Take(key string, now time.Time) (ok bool, wait time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	events := l.live(key, now)
	if len(events) >= l.limit {
		return false, events[0].Add(l.window).Sub(now)
	}
	if events == nil && len(l.keys) >= l.maxKeys {
		l.makeRoom(now)
	}
	l.keys[key] = append(events, now)
	return true, 0
}

// Refund removes the most recent event of key that was recorded at at, as
// [Limiter.Take] or [Limiter.Add] did. It does nothing when there is none.
func (l *Limiter) Refund(key string, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	events := l.keys[key]
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Equal(at) {
			events = append(events[:i:i], events[i+1:]...)
			if len(events) == 0 {
				delete(l.keys, key)
			} else {
				l.keys[key] = events
			}
			return
		}
	}
}

// Has reports whether the Limiter holds key.
func (l *Limiter) Has(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.keys[key]
	return ok
}

// Add records one event for key at now. When the Limiter holds maxKeys keys and
// key is new, Add first drops the keys that have no event inside the window, and
// then, if it is still full, the key with the fewest events (the one that is
// closest to free), the oldest last event first among equals. A key at its limit
// is the last to go, so a flood of new keys does not free a locked one.
func (l *Limiter) Add(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.keys[key]; !ok && len(l.keys) >= l.maxKeys {
		l.makeRoom(now)
	}
	events := append(l.live(key, now), now)
	if len(events) > l.limit {
		events = events[len(events)-l.limit:]
	}
	l.keys[key] = events
}

// Reset forgets the events of key.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.keys, key)
}

// Len returns the number of keys that the Limiter holds.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.keys)
}

// live returns the events of key that are inside the window ending at now, and
// stores the trimmed list. The caller holds the lock.
func (l *Limiter) live(key string, now time.Time) []time.Time {
	events, ok := l.keys[key]
	if !ok {
		return nil
	}
	cut := now.Add(-l.window)
	i := 0
	for i < len(events) && !events[i].After(cut) {
		i++
	}
	if i == len(events) {
		delete(l.keys, key)
		return nil
	}
	events = events[i:]
	l.keys[key] = events
	return events
}

// makeRoom drops the keys with no event inside the window; if none went, it drops
// the key with the fewest events, and among equals the one with the oldest last
// event. The caller holds the lock.
func (l *Limiter) makeRoom(now time.Time) {
	cut := now.Add(-l.window)
	for k, events := range l.keys {
		if !events[len(events)-1].After(cut) {
			delete(l.keys, k)
		}
	}
	if len(l.keys) < l.maxKeys {
		return
	}
	var victim string
	var victimN int
	var victimAt time.Time
	first := true
	for k, events := range l.keys {
		n, last := len(events), events[len(events)-1]
		if first || n < victimN || (n == victimN && last.Before(victimAt)) {
			victim, victimN, victimAt, first = k, n, last, false
		}
	}
	delete(l.keys, victim)
}
