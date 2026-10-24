package login

import (
	"context"
	"errors"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/password"
)

// sharedEmail is the email address that bob, carol and dave share on the
// boards of these tests. alice keeps alice@example.com, a unique address.
const sharedEmail = "shared@example.com"

// sharedBoard returns the small board with one email address shared by three
// accounts: bob (bcrypt), carol (argon2id at 64 MiB) and dave (no hash, so the
// dummy check). alice (gobb) keeps a unique address.
func sharedBoard(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, _ := boardtest.Load(t, bundletest.Small())
	setHashes(t, pool, [4][2]string{{"gobb", argonAt(19456)}, {"legacy", bcryptHash}, {"legacy", argonAt(65536)}, {"legacy", ""}})
	if _, err := pool.Exec(t.Context(), `update users set email = $1, email_clean = $1 where id in (2, 3, 4)`, sharedEmail); err != nil {
		t.Fatal(err)
	}
	return pool
}

// A sign-in holds the gate once for all of its hash work, whatever the number
// of accounts that the name finds. The hold is the largest charge among them.
func TestOneHoldPerSignIn(t *testing.T) {
	pool := sharedBoard(t)
	var calls atomic.Int64
	s := NewWith(pool, Options{Gate: NewGate(DefaultBudgetKiB), Verify: func(string, string, string) (bool, bool) {
		calls.Add(1)
		return false, false
	}})
	if err := s.LoadCharge(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.Floor = 0
	var holds []uint64
	s.onAcquire = func(kib uint64) { holds = append(holds, kib) }
	for _, c := range []struct {
		name   string
		verify int64
	}{
		{sharedEmail, 3}, // bob, carol and the dummy check for dave
		{"alice@example.com", 1},
		{"alice", 1},
		{"nobody", 1},
		{"\xff", 1},
	} {
		holds = nil
		calls.Store(0)
		if _, err := s.Check(t.Context(), c.name, "wrong", netip.MustParseAddr("10.1.2.3"), time.Now()); !errors.Is(err, ErrWrong) {
			t.Errorf("%q: err = %v, want ErrWrong", c.name, err)
		}
		if len(holds) != 1 || holds[0] != 65536 {
			t.Errorf("%q: gate holds = %v, want one of 65536 KiB", c.name, holds)
		}
		if n := calls.Load(); n != c.verify {
			t.Errorf("%q: Verify ran %d times, want %d", c.name, n, c.verify)
		}
	}
}

// contended runs one sign-in for name with a wrong password on a gate that has
// room for exactly one check. When Verify runs for the at-th time, a competitor
// queues for the whole gate, and gets it as soon as the check lets go, for
// 400 ms. With cancel, ctx ends at that moment too. It returns the time that
// Check took and its error.
func contended(t *testing.T, pool *pgxpool.Pool, name string, at int64, cancel bool) (time.Duration, error) {
	t.Helper()
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	var (
		calls atomic.Int64
		wg    sync.WaitGroup
		gate  *Gate
	)
	s := NewWith(pool, Options{Verify: func(string, string, string) (bool, bool) {
		if calls.Add(1) != at {
			return false, false
		}
		if cancel {
			stop()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := gate.Acquire(context.Background(), gate.budget, 5*time.Second)
			if err != nil {
				t.Error("the competitor got no room:", err)
				return
			}
			time.Sleep(400 * time.Millisecond)
			release()
		}()
		// Wait until the competitor is in the queue.
		for {
			gate.mu.Lock()
			n := gate.waiters.Len()
			gate.mu.Unlock()
			if n > 0 {
				return false, false
			}
			time.Sleep(time.Millisecond)
		}
	}})
	if err := s.LoadCharge(t.Context()); err != nil {
		t.Fatal(err)
	}
	gate = NewGate(s.ChargeKiB())
	s.opt.Gate = gate
	s.Floor = 300 * time.Millisecond
	s.GateWait = 60 * time.Millisecond
	start := time.Now()
	_, err := s.Check(ctx, name, "wrong", netip.MustParseAddr("10.1.2.3"), time.Now())
	took := time.Since(start)
	wg.Wait()
	return took, err
}

// Another sign-in that takes the gate between two candidates of a shared email
// must not change the answer: a shared email, like a unique one, gets ErrWrong
// at the floor. With one gate trip per candidate, the shared email waited for
// the gate again and got ErrBusy (503) where a unique email got 200.
func TestSharedEmailBusyLooksUnique(t *testing.T) {
	pool := sharedBoard(t)
	for _, name := range []string{sharedEmail, "alice@example.com"} {
		took, err := contended(t, pool, name, 1, false)
		if !errors.Is(err, ErrWrong) {
			t.Errorf("%s: err = %v, want ErrWrong, as for a unique email", name, err)
		}
		if took < 300*time.Millisecond {
			t.Errorf("%s: answered after %v, before the floor", name, took)
		}
	}
}

// A context that ends during the second candidate's Verify, while another
// sign-in waits for the gate, does not change the answer either: ErrWrong at the
// floor, as for a unique email whose context ends during its Verify. With one
// gate trip per candidate, the third candidate waited for the gate, saw the
// cancel and answered 500.
func TestSharedEmailCancelLooksUnique(t *testing.T) {
	pool := sharedBoard(t)
	for _, c := range []struct {
		name string
		at   int64
	}{
		{sharedEmail, 2},
		{"alice@example.com", 1},
	} {
		took, err := contended(t, pool, c.name, c.at, true)
		if !errors.Is(err, ErrWrong) {
			t.Errorf("%s: err = %v, want ErrWrong, as for a unique email", c.name, err)
		}
		if took < 300*time.Millisecond {
			t.Errorf("%s: answered after %v, before the floor", c.name, took)
		}
	}
}

// The floor counts from the start of the sign-in, moved forward by the time
// that it waits for the gate. Another check holds the whole gate for longer
// than the floor. Then a wrong sign-in for an unknown name, a gobb account and a
// legacy account each answers no sooner than the grant plus the floor (less the
// time from the start to the call for the gate, which the test measures: the
// reservation and the lookup), and the three agree. If the floor counted
// from the start alone, each would answer when its own hash ends, and the hash
// time (here 0, 40 and 120 ms) would show.
func TestFloorCountsFromTheGrant(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	setHashes(t, pool, [4][2]string{{"gobb", argonAt(19456)}, {"legacy", bcryptHash}, {"legacy", chainHash}, {"legacy", ""}})
	const (
		floor = 300 * time.Millisecond
		hold  = 500 * time.Millisecond
		slack = 60 * time.Millisecond
	)
	gate := NewGate(password.HashMemoryKiB)
	s := NewWith(pool, Options{Gate: gate, Verify: func(_, stored, scheme string) (bool, bool) {
		switch {
		case stored == dummyHash():
		case scheme == "gobb":
			time.Sleep(40 * time.Millisecond)
		default:
			time.Sleep(120 * time.Millisecond)
		}
		return false, false
	}})
	if err := s.LoadCharge(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.Floor = floor
	s.GateWait = 5 * time.Second
	// The server makes the dummy hash once, before it listens. Make it here too:
	// the first call costs about 150 ms, and it would run in the lookup phase of
	// the first sign-in, outside the wait.
	dummyHash()
	// asked is when the sign-in calls for the gate. The time from the start to
	// then (the reservation and the lookup) is outside the wait, so the floor
	// may end that much before grant + floor.
	var asked time.Time
	s.onAcquire = func(uint64) { asked = time.Now() }
	after := map[string]time.Duration{}
	before := map[string]time.Duration{}
	for _, name := range []string{"nobody", "alice", "bob"} {
		release, err := gate.Acquire(t.Context(), gate.budget, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		start := time.Now()
		go func() {
			_, err := s.Check(t.Context(), name, "wrong", netip.MustParseAddr("10.1.2.3"), time.Now())
			done <- err
		}()
		// Wait until the sign-in is in the queue, then hold the gate on.
		for {
			gate.mu.Lock()
			n := gate.waiters.Len()
			gate.mu.Unlock()
			if n > 0 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		time.Sleep(hold)
		granted := time.Now()
		release()
		if err := <-done; !errors.Is(err, ErrWrong) {
			t.Fatalf("%s: err = %v, want ErrWrong", name, err)
		}
		after[name] = time.Since(granted)
		before[name] = asked.Sub(start)
	}
	lo, hi := after["nobody"], after["nobody"]
	for name, d := range after {
		if d < floor-before[name] {
			t.Errorf("%s: answered %v after the grant, before the floor of %v (less %v before the call for the gate)",
				name, d, floor, before[name])
		}
		lo, hi = min(lo, d), max(hi, d)
	}
	if hi-lo > slack {
		t.Errorf("times after the grant %v differ by %v, want within %v", after, hi-lo, slack)
	}
}

// forcedGCs returns how many collections runtime.GC has run in this process.
func forcedGCs() uint32 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.NumForcedGC
}

// A check gives its share of the gate back only after a garbage collection.
// The memory of a check is garbage once the check ends, and the runtime
// collects it later, after the next allocation. Without the collection, the
// next holder's memory comes on top of that garbage, and the heap holds one
// check more than the gate allows: under a 256 MiB cap, three 64 MiB blocks and
// sometimes four. So a check that waits for the gate must see a forced
// collection that ran after the check before it, at the moment the gate lets it
// in. A waiter records the count at that moment. A collection that runs after
// the release is too late: the waiter has already run by then. The loop gives
// that race many chances to show.
func TestHoldEndsWithACollection(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	gate := NewGate(password.HashMemoryKiB)
	var (
		during  atomic.Uint32 // forced collections when Verify ran
		atEntry atomic.Uint32 // forced collections when the waiter got in
		wg      sync.WaitGroup
	)
	s := NewWith(pool, Options{Gate: gate, Verify: func(string, string, string) (bool, bool) {
		during.Store(forcedGCs())
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := gate.Acquire(context.Background(), gate.budget, 5*time.Second)
			if err != nil {
				t.Error("the waiter got no room:", err)
				return
			}
			atEntry.Store(forcedGCs())
			release()
		}()
		// Wait until the waiter is in the queue. A waiter that never queues
		// (it got room, or its Acquire failed) fails the test at the deadline.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			gate.mu.Lock()
			n := gate.waiters.Len()
			gate.mu.Unlock()
			if n > 0 {
				return false, false
			}
			time.Sleep(time.Millisecond)
		}
		t.Error("the waiter did not queue for the gate within 5 s")
		return false, false
	}})
	s.Floor = 0
	for _, name := range []string{"alice", "nobody"} {
		for range 20 {
			if _, err := s.Check(t.Context(), name, "wrong", netip.MustParseAddr("10.1.2.3"), time.Now()); !errors.Is(err, ErrWrong) {
				t.Fatalf("%s: err = %v, want ErrWrong", name, err)
			}
			wg.Wait()
			if atEntry.Load() <= during.Load() {
				t.Fatalf("%s: forced collections %d while the check ran, %d when the next holder got in; want a collection before the release",
					name, during.Load(), atEntry.Load())
			}
		}
	}
}

// A sign-in that holds the gate for several checks (a shared email address)
// collects between them. The checks run one after the other inside one hold;
// without a collection between two of them, the memory of the first is still
// garbage when the second allocates its own, and the heap holds both. Verify
// records the count of forced collections at each call: each call after the
// first must see a collection that ran after the call before it.
func TestHoldCollectsBetweenChecks(t *testing.T) {
	pool := sharedBoard(t)
	var seen []uint32
	s := NewWith(pool, Options{Gate: NewGate(DefaultBudgetKiB), Verify: func(string, string, string) (bool, bool) {
		seen = append(seen, forcedGCs())
		return false, false
	}})
	if err := s.LoadCharge(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.Floor = 0
	if _, err := s.Check(t.Context(), sharedEmail, "wrong", netip.MustParseAddr("10.1.2.3"), time.Now()); !errors.Is(err, ErrWrong) {
		t.Fatalf("err = %v, want ErrWrong", err)
	}
	// bob, carol and the dummy check for dave: three checks, two gaps.
	if len(seen) != 3 {
		t.Fatalf("Verify ran %d times, want 3", len(seen))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] <= seen[i-1] {
			t.Errorf("check %d saw %d forced collections, check %d saw %d; want a collection between them (%v)",
				i, seen[i-1], i+1, seen[i], seen)
		}
	}
}

// A querier that makes the lookup of a missing name slower, as a big table or
// a cold cache would.
type slowLookup struct {
	db.Querier
	extra time.Duration
	known string
}

func (q slowLookup) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "username_clean = $1") && len(args) == 1 && args[0] != q.known {
		time.Sleep(q.extra)
	}
	return q.Querier.Query(ctx, sql, args...)
}

// The floor covers the lookup. A name with an account and a name without one
// answer at the same time even when the lookup of the missing name takes 5 ms
// longer: the floor counts from the start of the sign-in, moved only by the
// time spent waiting for the gate.
func TestFloorCoversTheLookup(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	s := NewWith(slowLookup{pool, 5 * time.Millisecond, "alice"}, Options{
		Gate:   NewGate(DefaultBudgetKiB),
		Verify: func(string, string, string) (bool, bool) { return false, false },
	})
	s.Floor = 60 * time.Millisecond
	// The least of several answers drops the noise of the scheduler.
	least := map[string]time.Duration{}
	for range 8 {
		for _, name := range []string{"alice", "nobody"} {
			start := time.Now()
			if _, err := s.Check(t.Context(), name, "wrong", netip.MustParseAddr("10.1.2.3"), time.Now()); !errors.Is(err, ErrWrong) {
				t.Fatalf("%s: err = %v, want ErrWrong", name, err)
			}
			took := time.Since(start)
			if old, ok := least[name]; !ok || took < old {
				least[name] = took
			}
		}
	}
	for name, d := range least {
		if d < s.Floor {
			t.Errorf("%s: answered after %v, before the floor of %v", name, d, s.Floor)
		}
	}
	if diff := (least["nobody"] - least["alice"]).Abs(); diff > time.Millisecond {
		t.Errorf("answer times differ by %v (alice %v, nobody %v), want within 1 ms", diff, least["alice"], least["nobody"])
	}
}

// The floor covers the lookup on the busy path too. The gate stays full until
// the wait for it ends, so every sign-in answers [ErrBusy]. A name with an
// account and a name without one (whose lookup takes 5 ms longer) answer at the
// same time, and no sooner than the wait plus the floor: the floor counts from the
// start, moved forward by the wait, whether or not the gate grants room.
func TestFloorCoversTheLookupWhenBusy(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	gate := NewGate(password.HashMemoryKiB)
	s := NewWith(slowLookup{pool, 5 * time.Millisecond, "alice"}, Options{
		Gate:   gate,
		Verify: func(string, string, string) (bool, bool) { return false, false },
	})
	const gateWait = 80 * time.Millisecond
	s.Floor = 60 * time.Millisecond
	s.GateWait = gateWait
	release, err := gate.Acquire(t.Context(), gate.budget, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	least := map[string]time.Duration{}
	for range 6 {
		for _, name := range []string{"alice", "nobody"} {
			start := time.Now()
			if _, err := s.Check(t.Context(), name, "wrong", netip.MustParseAddr("10.1.2.3"), time.Now()); !errors.Is(err, ErrBusy) {
				t.Fatalf("%s: err = %v, want ErrBusy", name, err)
			}
			took := time.Since(start)
			if old, ok := least[name]; !ok || took < old {
				least[name] = took
			}
		}
	}
	for name, d := range least {
		if d < gateWait+s.Floor {
			t.Errorf("%s: answered after %v, before the wait plus the floor of %v", name, d, gateWait+s.Floor)
		}
	}
	if diff := (least["nobody"] - least["alice"]).Abs(); diff > time.Millisecond {
		t.Errorf("busy answer times differ by %v (alice %v, nobody %v), want within 1 ms", diff, least["alice"], least["nobody"])
	}
}
