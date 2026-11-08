package login

import (
	"crypto/sha256"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/evanwtf/gobb/internal/metrics"
)

// The defaults of the sign-in schedule (spec 10, "Load protection"). (owner)
const (
	// DefaultFreeFailures is how many failures leave no wait after them.
	DefaultFreeFailures = 2
	// DefaultLastWait is the wait after every failure past the end of
	// DefaultWaits.
	DefaultLastWait = 900 * time.Second
	// DefaultResetAfter is the time with no failure that clears a count.
	DefaultResetAfter = 24 * time.Hour
	// DefaultMaxKeys is the most counts that each table of the schedule holds:
	// one table for the addresses, one for the names. An entry costs about 145
	// bytes with its share of the map, so a full table holds about 7 MiB and
	// the two tables about 14 MiB.
	DefaultMaxKeys = 50000
)

// DefaultWaits returns the waits after the failures that follow the free
// ones: 15 s, 15 s, 60 s, 60 s and 60 s. (owner)
func DefaultWaits() []time.Duration {
	return []time.Duration{15 * time.Second, 15 * time.Second, 60 * time.Second, 60 * time.Second, 60 * time.Second}
}

// ScheduleConfig sets the waits of the sign-in schedule. A zero field takes
// its default, except FreeFailures and Waits, which a caller sets on purpose:
// [DefaultSchedule] gives the defaults.
type ScheduleConfig struct {
	// FreeFailures is how many failures of a count leave no wait after them.
	FreeFailures int
	// Waits are the waits after the failures that follow the free ones, in
	// order. Waits[0] follows failure FreeFailures+1.
	Waits []time.Duration
	// LastWait follows every failure past the end of Waits.
	LastWait time.Duration
	// ResetAfter is the time with no failure that clears a count.
	ResetAfter time.Duration
	// MaxKeys is the most counts in each table.
	MaxKeys int
}

// DefaultSchedule returns the schedule of the owner: two free failures, then
// 15 s, 15 s, 60 s, 60 s, 60 s, then 900 s; a count clears after 24 hours with
// no failure.
func DefaultSchedule() ScheduleConfig {
	return ScheduleConfig{
		FreeFailures: DefaultFreeFailures,
		Waits:        DefaultWaits(),
		LastWait:     DefaultLastWait,
		ResetAfter:   DefaultResetAfter,
		MaxKeys:      DefaultMaxKeys,
	}
}

// WaitAfter returns the wait before the next try after n failures, when free
// failures are free: 0 for n ≤ free, then Waits in order, then LastWait.
func (c ScheduleConfig) WaitAfter(n, free int) time.Duration {
	k := n - free
	switch {
	case k <= 0:
		return 0
	case k <= len(c.Waits):
		return c.Waits[k-1]
	}
	return c.LastWait
}

// entry is the count of one address or one name.
type entry struct {
	// fails is the number of failures since the count last cleared.
	fails int
	// lastFail is the time of the latest failure.
	lastFail time.Time
	// pending is the number of tries that passed the schedule and have not
	// ended. Each one counts as a failure at pendingAt until it ends, so
	// parallel tries cannot pass a wait that the first of them would start.
	pending   int
	pendingAt time.Time
	// lastTry is the time of the latest try that passed the schedule. Defensive
	// Mode spaces the tries of an untrusted address from it.
	lastTry time.Time
}

// count returns the failures that the schedule sees, and the time that the
// wait counts from.
func (e *entry) count() (int, time.Time) {
	n, from := e.fails+e.pending, e.lastFail
	if e.pending > 0 && e.pendingAt.After(from) {
		from = e.pendingAt
	}
	return n, from
}

// active returns the latest time that the entry changed.
func (e *entry) active() time.Time {
	t := e.lastFail
	for _, u := range []time.Time{e.pendingAt, e.lastTry} {
		if u.After(t) {
			t = u
		}
	}
	return t
}

// table holds the counts of one kind of key.
type table[K comparable] struct {
	name string // "address" or "name", for the eviction metric
	m    map[K]*entry
}

// get returns the entry of k, or nil. It clears a count that has had no
// failure for resetAfter, and drops an entry that holds nothing more.
func (t *table[K]) get(k K, now time.Time, resetAfter, gap time.Duration) *entry {
	e := t.m[k]
	if e == nil {
		return nil
	}
	if e.pending == 0 && e.fails > 0 && now.Sub(e.lastFail) >= resetAfter {
		e.fails, e.lastFail = 0, time.Time{}
	}
	if e.pending == 0 && e.fails == 0 && now.Sub(e.lastTry) >= gap {
		delete(t.m, k)
		return nil
	}
	return e
}

// put returns the entry of k, and adds one when there is none. A full table
// first drops the entries that hold nothing more. When that frees less than an
// eighth of the table, it evicts entries until an eighth is free, so the next
// new keys do not sweep the table again. It evicts the entries with no try in
// progress in this order: first those not in a wait, then those in a wait;
// within each, the fewest failures first, then the least recently changed. An
// attacker who sprays new keys with one failure each therefore evicts his own
// keys before a count that holds a wait.
func (t *table[K]) put(k K, now time.Time, c ScheduleConfig, gap time.Duration) *entry {
	if e := t.m[k]; e != nil {
		return e
	}
	limit := c.MaxKeys
	if len(t.m) >= limit {
		for key := range t.m {
			t.get(key, now, c.ResetAfter, gap)
		}
	}
	if keep := limit - max(1, limit/8); len(t.m) > keep {
		t.evict(len(t.m)-keep, now, c)
	}
	e := &entry{}
	t.m[k] = e
	return e
}

// evict drops n entries with no try in progress, in the order of put.
func (t *table[K]) evict(n int, now time.Time, c ScheduleConfig) {
	type rank struct {
		k      K
		waits  bool
		fails  int
		active time.Time
	}
	all := make([]rank, 0, len(t.m))
	for key, e := range t.m {
		if e.pending > 0 {
			continue
		}
		f, from := e.count()
		all = append(all, rank{key, from.Add(c.WaitAfter(f, c.FreeFailures)).After(now), e.fails, e.active()})
	}
	slices.SortFunc(all, func(a, b rank) int {
		switch {
		case a.waits != b.waits:
			if a.waits {
				return 1
			}
			return -1
		case a.fails != b.fails:
			return a.fails - b.fails
		}
		return a.active.Compare(b.active)
	})
	n = min(n, len(all))
	for _, r := range all[:n] {
		delete(t.m, r.k)
	}
	metrics.SigninScheduleEvictions.Add(uint64(n), t.name)
}

// nameKey is the key of a cleaned name: its SHA-256, cut to 16 bytes, so that
// a long name costs no more memory than a short one.
type nameKey [16]byte

func keyOfName(clean string) nameKey {
	sum := sha256.Sum256([]byte(clean))
	var k nameKey
	copy(k[:], sum[:16])
	return k
}

// Schedule holds the counts of failed sign-ins per address and per name, in
// memory. It is safe for concurrent use. The time comes from the caller.
type Schedule struct {
	cfg ScheduleConfig

	mu    sync.Mutex
	addrs table[netip.Prefix]
	names table[nameKey]
	// trust caches the answers about trusted addresses that refused tries
	// asked ([Schedule.remember]).
	trust map[trustKey]trustAnswer
}

// NewSchedule returns an empty Schedule with the waits of cfg. A zero LastWait,
// ResetAfter or MaxKeys takes its default.
func NewSchedule(cfg ScheduleConfig) *Schedule {
	if cfg.LastWait <= 0 {
		cfg.LastWait = DefaultLastWait
	}
	if cfg.ResetAfter <= 0 {
		cfg.ResetAfter = DefaultResetAfter
	}
	if cfg.MaxKeys <= 0 {
		cfg.MaxKeys = DefaultMaxKeys
	}
	cfg.FreeFailures = max(cfg.FreeFailures, 0)
	cfg.Waits = slices.Clone(cfg.Waits)
	return &Schedule{
		cfg:   cfg,
		addrs: table[netip.Prefix]{name: "address", m: map[netip.Prefix]*entry{}},
		names: table[nameKey]{name: "name", m: map[nameKey]*entry{}},
		trust: map[trustKey]trustAnswer{},
	}
}

// Config returns the waits of the schedule.
func (s *Schedule) Config() ScheduleConfig {
	c := s.cfg
	c.Waits = slices.Clone(c.Waits)
	return c
}

// trust is what a try knows about the trust of its address: not yet asked, yes
// or no.
type trust int8

const (
	unknown trust = iota
	trusted
	untrusted
)

// rules are the rules of Defensive Mode for one try. The zero value is the
// normal schedule.
type rules struct {
	// on is true while Defensive Mode is on.
	on bool
	// gap is the least time between two tries of an untrusted address.
	gap time.Duration
}

// tryState is what [Schedule.try] knows and decides for one sign-in.
type tryState struct {
	addr    netip.Prefix
	name    nameKey
	hasName bool
	// addrTrust is the trust of the address for any member; nameTrust for the
	// account that the name finds.
	addrTrust, nameTrust trust

	// The decision.
	need    trust // unknown: decided; trusted: ask addrTrust; untrusted: ask nameTrust
	refused bool
	reason  string
	wait    time.Duration
	// heldAddr and heldName say which counts hold a pending try.
	heldAddr, heldName bool
}

// The values of tryState.need.
const (
	needNothing   = unknown
	needAddrTrust = trusted
	needNameTrust = untrusted
)

// The reasons of a refusal, the labels of gobb_signin_refusals_total.
const (
	// ReasonAddress: the wait of the address has not ended.
	ReasonAddress = "address"
	// ReasonName: the wait of the name has not ended, and the address is not one
	// of the member's trusted addresses.
	ReasonName = "name"
	// ReasonDefensive: the normal schedule allows the try, and the rules of
	// Defensive Mode for an untrusted address refuse it.
	ReasonDefensive = "defensive"
)

// try decides one sign-in under the lock. It refuses it, asks for the trust of
// the address or of the name (st.need), or reserves a pending try on each
// count that applies. A refusal changes no count.
func (s *Schedule) try(st *tryState, now time.Time, r rules) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st.need, st.refused, st.reason, st.wait = needNothing, false, "", 0
	c := s.cfg
	gap := r.gap

	// The wait of the name, and whether it holds this address. A trust answer
	// that a refused try already asked stays good while the wait lasts.
	nameWait := time.Duration(0)
	if st.hasName && st.nameTrust != trusted {
		if e := s.names.get(st.name, now, c.ResetAfter, 0); e != nil {
			n, from := e.count()
			nameWait = max(0, from.Add(c.WaitAfter(n, c.FreeFailures)).Sub(now))
		}
		if nameWait > 0 && st.nameTrust == unknown {
			if t, ok := s.trust[trustKey{st.name, st.addr}]; ok && now.Before(t.until) {
				st.nameTrust = t.answer
			}
		}
		if st.nameTrust == trusted {
			nameWait = 0
		}
	}
	// refuse refuses the try with the larger of wait and the wait of the name,
	// so that the answer names the moment that a try can pass.
	refuse := func(reason string, wait time.Duration) {
		st.refused, st.reason, st.wait = true, reason, max(wait, nameWait)
	}

	// The address. In Defensive Mode an untrusted address has no free failure,
	// and waits at least gap after its last try.
	if a := s.addrs.get(st.addr, now, c.ResetAfter, gap); a != nil {
		n, from := a.count()
		normal := from.Add(c.WaitAfter(n, c.FreeFailures)).Sub(now)
		if normal > 0 {
			refuse(ReasonAddress, normal)
			return
		}
		if r.on && st.addrTrust != trusted {
			strict := max(from.Add(c.WaitAfter(n, 0)).Sub(now), a.lastTry.Add(gap).Sub(now))
			if strict > 0 {
				if st.addrTrust == unknown {
					st.need = needAddrTrust
					return
				}
				refuse(ReasonDefensive, strict)
				return
			}
		}
	}

	// The name. Its wait does not hold an address that is one of the member's
	// trusted addresses.
	if nameWait > 0 {
		if st.nameTrust == unknown {
			st.need = needNameTrust
			return
		}
		refuse(ReasonName, nameWait)
		return
	}

	a := s.addrs.put(st.addr, now, c, gap)
	a.pending++
	a.pendingAt, a.lastTry = now, now
	st.heldAddr = true
	if st.hasName && st.nameTrust != trusted {
		e := s.names.put(st.name, now, c, 0)
		e.pending++
		e.pendingAt = now
		st.heldName = true
	}
}

// trustKey names one answer of the trust cache: a name and a client range.
type trustKey struct {
	name nameKey
	addr netip.Prefix
}

// trustAnswer is one answer of the trust cache, good until until.
type trustAnswer struct {
	answer trust
	until  time.Time
}

// remember keeps the answer "not trusted" that the database gave about the
// address of st and its name, while the wait of the name lasts. Later tries in that wait then decide with no database work, so the
// refused tries at a locked name cost the same for every name: one query for
// the first, none after it. A full cache drops its expired answers, and all of
// them when none has expired.
func (s *Schedule) remember(st *tryState, now time.Time) {
	// Only a "no" is kept. A "yes" lets the try through to the password check,
	// so keeping it saves nothing, and a ban in that wait must end the trust at
	// once.
	if st.nameTrust != untrusted || !st.hasName {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.names.m[st.name]
	if e == nil {
		return
	}
	n, from := e.count()
	until := from.Add(s.cfg.WaitAfter(n, s.cfg.FreeFailures))
	if !until.After(now) {
		return
	}
	if len(s.trust) >= s.cfg.MaxKeys {
		for k, t := range s.trust {
			if !now.Before(t.until) {
				delete(s.trust, k)
			}
		}
		if len(s.trust) >= s.cfg.MaxKeys {
			clear(s.trust)
		}
	}
	s.trust[trustKey{st.name, st.addr}] = trustAnswer{st.nameTrust, until}
}

// ClearName clears the count of the cleaned name clean, and the trust answers
// of the name. A completed password reset calls it, so that a member whom a
// stranger holds in a wait can sign in again at once.
func (s *Schedule) ClearName(clean string) {
	k := keyOfName(clean)
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.names.m[k]; e != nil {
		e.fails, e.lastFail = 0, time.Time{}
		if e.pending == 0 {
			delete(s.names.m, k)
		}
	}
	for tk := range s.trust {
		if tk.name == k {
			delete(s.trust, tk)
		}
	}
}

// end ends the pending tries of st. failed says that the try was a failure;
// a failure counts against the address, and against the name unless
// nameCounts is false. clearName clears the count of the name (a good
// sign-in).
func (s *Schedule) end(st *tryState, now time.Time, failed, nameCounts, clearName bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.heldAddr {
		if a := s.addrs.m[st.addr]; a != nil {
			a.pending = max(a.pending-1, 0)
			if failed {
				a.fails++
				if now.After(a.lastFail) {
					a.lastFail = now
				}
			}
		}
	}
	if !st.hasName {
		return
	}
	e := s.names.m[st.name]
	if e == nil {
		return
	}
	if st.heldName {
		e.pending = max(e.pending-1, 0)
	}
	switch {
	case clearName:
		e.fails, e.lastFail = 0, time.Time{}
	case failed && nameCounts && st.heldName:
		e.fails++
		if now.After(e.lastFail) {
			e.lastFail = now
		}
	}
	if e.pending == 0 && e.fails == 0 {
		delete(s.names.m, st.name)
	}
}

// Keys returns the number of counts in the table of the addresses and in the
// table of the names.
func (s *Schedule) Keys() (addrs, names int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.addrs.m), len(s.names.m)
}
