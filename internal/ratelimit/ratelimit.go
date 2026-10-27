// Package ratelimit counts events per key inside a sliding window.
//
// gobb uses it to limit failed sign-ins, per client address and per name.
package ratelimit

import (
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/evanwtf/gobb/internal/metrics"
)

// Limiter counts events per key. A key is over the limit when it has limit
// events inside the window. The time comes from the caller, so a test needs no
// clock. A Limiter is safe for concurrent use.
type Limiter struct {
	limit   int
	window  time.Duration
	maxKeys int
	// name labels the metrics of the limiter (gobb_rate_limited_total and the
	// gobb_rate_limit_ family). A Limiter with no name records nothing.
	name string

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

// Named sets the name that labels the metrics of l, and returns l. The name is
// fixed text, such as "signin_address", never a key. The counters are
// gobb_rate_limited_total (refusals), gobb_rate_limit_allowed_total,
// gobb_rate_limit_fill_ratio, gobb_rate_limit_refunds_total and
// gobb_rate_limit_evictions_total. At each scrape, the gauges of the gobb_rate_limit_
// family read the keys and the settings of l. A second Limiter under one name
// replaces the first in those gauges.
func (l *Limiter) Named(name string) *Limiter {
	regMu.Lock()
	defer regMu.Unlock()
	if l.name != "" && named[l.name] == l {
		delete(named, l.name)
	}
	l.name = name
	if name == "" {
		return l
	}
	named[name] = l
	metrics.RateLimited.Init(name)
	metrics.RateLimitAllowed.Init(name)
	metrics.RateLimitFill.Init(name)
	metrics.RateLimitRefunds.Init(name)
	metrics.RateLimitEvictions.Init(name)
	return l
}

// Refused counts one refusal of a named limiter in gobb_rate_limited_total.
// [Limiter.Take] counts its own refusals. A caller of [Limiter.TryTake] calls
// Refused when it refuses the request.
func (l *Limiter) Refused() {
	if l.name != "" {
		metrics.RateLimited.Inc(l.name)
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
// [Limiter.Refund] when the work does not count. A false Take counts as a refusal
// of a named limiter (gobb_rate_limited_total).
func (l *Limiter) Take(key string, now time.Time) (ok bool, wait time.Duration) {
	ok, wait = l.TryTake(key, now)
	if !ok {
		l.Refused()
	}
	return ok, wait
}

// TryTake is [Limiter.Take] with no count: for a caller that may let the request
// through when the key is full. That caller calls [Limiter.Refused] when it
// refuses.
func (l *Limiter) TryTake(key string, now time.Time) (ok bool, wait time.Duration) {
	ok, wait, n, dropped := l.tryTake(key, now)
	l.evicted(dropped)
	if ok {
		l.recorded(n)
	}
	return ok, wait
}

// tryTake is the work of TryTake under the lock. It returns the events of key
// after the take, and the keys that it dropped to make room.
func (l *Limiter) tryTake(key string, now time.Time) (ok bool, wait time.Duration, n, dropped int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	events := l.live(key, now)
	if len(events) >= l.limit {
		return false, events[0].Add(l.window).Sub(now), 0, 0
	}
	if events == nil && len(l.keys) >= l.maxKeys {
		dropped = l.makeRoom(now)
	}
	events = append(events, now)
	l.keys[key] = events
	return true, 0, len(events), dropped
}

// Refund removes the most recent event of key that was recorded at at, as
// [Limiter.Take] or [Limiter.Add] did. It does nothing when there is none. A named
// limiter counts each event that Refund removes (gobb_rate_limit_refunds_total).
func (l *Limiter) Refund(key string, at time.Time) {
	if l.refund(key, at) && l.name != "" {
		metrics.RateLimitRefunds.Inc(l.name)
	}
}

func (l *Limiter) refund(key string, at time.Time) bool {
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
			return true
		}
	}
	return false
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
// then, if it is still full, a batch of about 1% of the keys: the ones with the
// fewest events (closest to free), the oldest last event first among equals. A key
// at its limit is the last to go, so a flood of new keys does not free a locked one.
func (l *Limiter) Add(key string, now time.Time) {
	n, dropped := l.add(key, now)
	l.evicted(dropped)
	l.recorded(n)
}

// add is the work of Add under the lock. It returns the events of key after the
// add, and the keys that it dropped to make room.
func (l *Limiter) add(key string, now time.Time) (n, dropped int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.keys[key]; !ok && len(l.keys) >= l.maxKeys {
		dropped = l.makeRoom(now)
	}
	events := append(l.live(key, now), now)
	if len(events) > l.limit {
		events = events[len(events)-l.limit:]
	}
	l.keys[key] = events
	return len(events), dropped
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

// makeRoom drops the keys with no event inside the window. If the Limiter is
// still full, it drops a batch of about 1% of maxKeys (at least one key) in one
// scan, so that a flood of new keys does not pay for a full scan each. The batch
// is the keys with the fewest events, and among equals the keys with the oldest
// last event. It returns the number of keys of that batch: keys with events that
// were still inside the window, which start again at zero. The keys with no live
// event are no loss and are not counted. The caller holds the lock.
func (l *Limiter) makeRoom(now time.Time) int {
	cut := now.Add(-l.window)
	type entry struct {
		key  string
		n    int
		last time.Time
	}
	live := make([]entry, 0, len(l.keys))
	for k, events := range l.keys {
		last := events[len(events)-1]
		if !last.After(cut) {
			delete(l.keys, k)
			continue
		}
		live = append(live, entry{k, len(events), last})
	}
	if len(l.keys) < l.maxKeys {
		return 0
	}
	batch := min(max(1, l.maxKeys/100), len(live))
	// Pick the batch with a selection pass: the batch is small, so keep a short
	// list of the worst candidates and insert into it.
	less := func(a, b entry) bool {
		if a.n != b.n {
			return a.n < b.n
		}
		return a.last.Before(b.last)
	}
	picked := make([]entry, 0, batch)
	for _, e := range live {
		if len(picked) == batch && !less(e, picked[batch-1]) {
			continue
		}
		pos := len(picked)
		if pos == batch {
			pos--
		} else {
			picked = append(picked, e)
		}
		for pos > 0 && less(e, picked[pos-1]) {
			picked[pos] = picked[pos-1]
			pos--
		}
		picked[pos] = e
	}
	for _, e := range picked {
		delete(l.keys, e.key)
	}
	return len(picked)
}

// recorded counts one event that a named limiter recorded: gobb_rate_limit_allowed_total,
// and in gobb_rate_limit_fill_ratio the events of the key after it, over the limit.
// n is that number of events. The caller does not hold the lock.
func (l *Limiter) recorded(n int) {
	if l.name == "" {
		return
	}
	metrics.RateLimitAllowed.Inc(l.name)
	metrics.RateLimitFill.Observe(float64(n)/float64(l.limit), l.name)
}

// evicted counts n keys that a named limiter dropped to make room
// (gobb_rate_limit_evictions_total). The caller does not hold the lock.
func (l *Limiter) evicted(n int) {
	if l.name != "" && n > 0 {
		metrics.RateLimitEvictions.Add(uint64(n), l.name)
	}
}

// fill is what one scan of the keys of a Limiter finds.
type fill struct {
	keys    int     // keys held, live or not
	full    int     // keys with limit events inside the window
	fullest float64 // the most live events of any key, over the limit
}

// scan reads the keys and changes nothing. Its cost grows with the number of keys
// and is bounded by maxKeys. It holds the lock for the whole scan.
func (l *Limiter) scan(now time.Time) fill {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := now.Add(-l.window)
	f := fill{keys: len(l.keys)}
	most := 0
	for _, events := range l.keys {
		// The events are oldest first: count back from the newest to the cut.
		n := 0
		for i := len(events) - 1; i >= 0 && events[i].After(cut); i-- {
			n++
		}
		if n >= l.limit {
			f.full++
		}
		most = max(most, n)
	}
	f.fullest = float64(most) / float64(l.limit)
	return f
}

var (
	regMu sync.Mutex
	// named holds the Limiter of each name. The gauges read it at each scrape.
	named = map[string]*Limiter{}
)

// snapshot returns the named limiters, sorted by name.
func snapshot() []*Limiter {
	regMu.Lock()
	defer regMu.Unlock()
	all := make([]*Limiter, 0, len(named))
	for _, l := range named {
		all = append(all, l)
	}
	slices.SortFunc(all, func(a, b *Limiter) int { return strings.Compare(a.name, b.name) })
	return all
}

func init() {
	// Each gauge reads its own scan. The two that need one are the only ones
	// that take a limiter's lock for longer than a map length.
	gauge := func(name, help string, f func(l *Limiter, now time.Time) float64) {
		metrics.Default.GaugeSamples(name, help, []string{"limit"}, func() []metrics.Sample {
			now := time.Now()
			all := snapshot()
			out := make([]metrics.Sample, len(all))
			for i, l := range all {
				out[i] = metrics.Sample{Values: []string{l.name}, Value: f(l, now)}
			}
			return out
		})
	}
	gauge("gobb_rate_limit_keys", "Keys that a rate limit holds, live or not.",
		func(l *Limiter, _ time.Time) float64 { return float64(l.Len()) })
	gauge("gobb_rate_limit_keys_full", "Keys at the limit now, by rate limit.",
		func(l *Limiter, now time.Time) float64 { return float64(l.scan(now).full) })
	gauge("gobb_rate_limit_fullest_ratio", "The highest fill of any key now (events in the window over the limit), by rate limit.",
		func(l *Limiter, now time.Time) float64 { return l.scan(now).fullest })
	gauge("gobb_rate_limit_max_events", "Events that a key may have in the window, by rate limit.",
		func(l *Limiter, _ time.Time) float64 { return float64(l.limit) })
	gauge("gobb_rate_limit_window_seconds", "Length of the window of a rate limit, in seconds.",
		func(l *Limiter, _ time.Time) float64 { return l.window.Seconds() })
	gauge("gobb_rate_limit_max_keys", "Keys that a rate limit may hold before it drops some.",
		func(l *Limiter, _ time.Time) float64 { return float64(l.maxKeys) })
}
