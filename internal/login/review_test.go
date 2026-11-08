package login_test

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/login"
)

// roundTrips counts every statement that a service sends.
type roundTrips struct {
	db.Querier
	n atomic.Int64
}

func (q *roundTrips) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	q.n.Add(1)
	return q.Querier.Exec(ctx, sql, args...)
}

func (q *roundTrips) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	q.n.Add(1)
	return q.Querier.Query(ctx, sql, args...)
}

func (q *roundTrips) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	q.n.Add(1)
	return q.Querier.QueryRow(ctx, sql, args...)
}

// A refused try at a locked name costs the same database work for an existing
// username, an email address, a shared email address and a name with no
// account: one statement for the first refusal from an address, none for the
// next ones in the same wait. So the time of a refusal tells a stranger nothing,
// and a stranger cannot sample it for free.
func TestRefusedTryCostsTheSameForEveryName(t *testing.T) {
	_, pool := newService(t)
	spy := &roundTrips{Querier: pool}
	f := &fakeVerify{}
	svc := login.NewWith(spy, login.Options{Schedule: lockAfter(1), Verify: f.verify})
	svc.Floor = 0
	ctx := t.Context()
	names := []string{"alice", "nobody", "alice@example.com", "bob@example.com", "nobody@example.com"}
	first := map[string]int64{}
	for i, name := range names {
		// One failure locks the name.
		if _, err := svc.Check(ctx, name, "wrong", addr(i), now); !errors.Is(err, login.ErrWrong) {
			t.Fatalf("%s: %v", name, err)
		}
		from := addr(100 + i)
		for try := range 3 {
			spy.n.Store(0)
			_, err := svc.Check(ctx, name, "right", from, now.Add(time.Second))
			if l, ok := isLimited(err); !ok || l.Reason != login.ReasonName {
				t.Fatalf("%s try %d: %v, want the name's wait", name, try, err)
			}
			got := spy.n.Load()
			if try == 0 {
				first[name] = got
			} else if got != 0 {
				t.Errorf("%s: refused try %d sent %d statements, want 0", name, try+1, got)
			}
		}
	}
	for _, name := range names {
		if first[name] != 1 {
			t.Errorf("%s: the first refused try sent %d statements, want 1", name, first[name])
		}
	}
	t.Logf("statements on the first refused try: %v", first)
	if f.calls.Load() != int64(len(names)) {
		t.Errorf("Verify ran %d times, want %d (the locking failures only)", f.calls.Load(), len(names))
	}
}

// A spray of new names with one failure each cannot flush a count that holds a
// wait: a full table evicts the counts not in a wait, fewest failures first.
func TestEvictionKeepsACountInAWait(t *testing.T) {
	_, pool := newService(t)
	f := &fakeVerify{}
	sched := login.NewSchedule(login.ScheduleConfig{FreeFailures: 2, Waits: login.DefaultWaits(), LastWait: 900 * time.Second, MaxKeys: 64})
	svc := login.NewWith(pool, login.Options{Schedule: sched, Verify: f.verify})
	svc.Floor = 0
	ctx := t.Context()
	// The victim: eight failures, the last at +210 s, so a wait of 900 s.
	for i, at := range []time.Duration{0, 0, 0, 15, 30, 90, 150, 210} {
		if _, err := svc.Check(ctx, "alice", "wrong", addr(i), now.Add(at*time.Second)); !errors.Is(err, login.ErrWrong) {
			t.Fatalf("victim failure %d: %v", i+1, err)
		}
	}
	// 1,000 new names, one failure each, each from its own IPv6 /64.
	for i := range 1000 {
		from := netip.MustParseAddr(fmt.Sprintf("2001:db8:%x::1", i+1))
		_, _ = svc.Check(ctx, fmt.Sprintf("spray%d", i), "wrong", from, now.Add(300*time.Second))
	}
	if a, n := sched.Keys(); a > 64 || n > 64 {
		t.Errorf("tables hold %d addresses and %d names, want at most 64 each", a, n)
	}
	_, err := svc.Check(ctx, "alice", "right", addr(200), now.Add(301*time.Second))
	if l, ok := isLimited(err); !ok || l.Reason != login.ReasonName || l.RetryAfter != 809*time.Second {
		t.Errorf("the victim after the spray = %v, want the name's wait of 809 s", err)
	}
}

// When both counts hold a wait, the answer gives the larger one.
func TestRefusalReportsTheLargerWait(t *testing.T) {
	svc, _ := scheduled(t, nil)
	ctx := t.Context()
	from := netip.MustParseAddr("192.0.2.40")
	// The name: five failures from other addresses (wait 60 s from +30 s).
	for i, at := range []time.Duration{0, 0, 0, 15, 30} {
		_, _ = svc.Check(ctx, "bob", "wrong", addr(i), now.Add(at*time.Second))
	}
	// The address: three failures at other names (wait 15 s from +31 s).
	for i := range 3 {
		_, _ = svc.Check(ctx, fmt.Sprintf("x%d", i), "wrong", from, now.Add(31*time.Second))
	}
	_, err := svc.Check(ctx, "bob", "right", from, now.Add(32*time.Second))
	if l, ok := isLimited(err); !ok || l.Reason != login.ReasonAddress || l.RetryAfter != 58*time.Second {
		t.Errorf("both held = %v, want the address's refusal with the name's 58 s", err)
	}
	// The address alone: its own wait.
	_, err = svc.Check(ctx, "carol", "right", from, now.Add(32*time.Second))
	if l, ok := isLimited(err); !ok || l.RetryAfter != 14*time.Second {
		t.Errorf("address only = %v, want 14 s", err)
	}
}

// A panic in the password check gives the pending try back, so it cannot hold
// a count in a wait for ever.
func TestPanicGivesThePendingTryBack(t *testing.T) {
	_, pool := newService(t)
	boom := true
	verify := func(raw, _, _ string) (bool, bool) {
		if boom {
			panic("boom")
		}
		return raw == "right", false
	}
	svc := login.NewWith(pool, login.Options{Schedule: lockAfter(1), Verify: verify})
	svc.Floor = 0
	func() {
		defer func() { _ = recover() }()
		_, _ = svc.Check(t.Context(), "alice", "right", ip, now)
	}()
	boom = false
	if id, err := svc.Check(t.Context(), "alice", "right", ip, now); err != nil || id != 1 {
		t.Errorf("after the panic = %d, %v; want a sign-in (no pending try left)", id, err)
	}
}

// ClearName clears the count of a name: a member held in a wait can sign in
// at once, while the count of the address stays.
func TestClearName(t *testing.T) {
	svc, _ := scheduled(t, nil)
	ctx := t.Context()
	for i := range 8 {
		_, _ = svc.Check(ctx, "alice", "wrong", addr(i), now.Add(time.Duration(i)*time.Hour))
	}
	at := now.Add(7*time.Hour + time.Second)
	if _, ok := isLimited(func() error { _, err := svc.Check(ctx, "alice", "right", addr(50), at); return err }()); !ok {
		t.Fatal("alice is not held")
	}
	svc.ClearName(" Alice ")
	if id, err := svc.Check(ctx, "alice", "right", addr(50), at); err != nil || id != 1 {
		t.Errorf("after ClearName = %d, %v", id, err)
	}
}
