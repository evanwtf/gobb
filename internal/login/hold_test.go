package login

import (
	"context"
	"errors"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
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

// The floor counts from the later of the start of the sign-in and the moment
// the gate lets it in. Another check holds the whole gate for longer than the
// floor. Then a wrong sign-in for an unknown name, a gobb account and a legacy
// account each answers no sooner than the grant plus the floor, and the three
// agree. If the floor counted from the start, each would answer when its own
// hash ends, and the hash time (here 0, 40 and 120 ms) would show.
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
	after := map[string]time.Duration{}
	for _, name := range []string{"nobody", "alice", "bob"} {
		release, err := gate.Acquire(t.Context(), gate.budget, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
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
	}
	lo, hi := after["nobody"], after["nobody"]
	for name, d := range after {
		if d < floor {
			t.Errorf("%s: answered %v after the grant, before the floor of %v", name, d, floor)
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
// collection that ran after the check before it.
func TestHoldEndsWithACollection(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	gate := NewGate(password.HashMemoryKiB)
	var during atomic.Uint32
	s := NewWith(pool, Options{Gate: gate, Verify: func(string, string, string) (bool, bool) {
		during.Store(forcedGCs())
		return false, false
	}})
	s.Floor = 0
	for _, c := range []struct{ name, pw string }{{"alice", "wrong"}, {"nobody", "wrong"}} {
		during.Store(0)
		if _, err := s.Check(t.Context(), c.name, c.pw, netip.MustParseAddr("10.1.2.3"), time.Now()); !errors.Is(err, ErrWrong) {
			t.Fatalf("%s: err = %v, want ErrWrong", c.name, err)
		}
		release, err := gate.Acquire(t.Context(), gate.budget, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		after := forcedGCs()
		release()
		if after <= during.Load() {
			t.Errorf("%s: forced collections %d while the check ran, %d when the gate was free again; want a collection before the release",
				c.name, during.Load(), after)
		}
	}
}
