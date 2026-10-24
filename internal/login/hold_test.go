package login

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
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
