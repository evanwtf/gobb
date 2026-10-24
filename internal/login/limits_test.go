package login_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/ratelimit"
)

// fakeVerify counts the calls and the most that ran together. It matches the
// password "right".
type fakeVerify struct {
	calls   atomic.Int64
	running atomic.Int64
	peak    atomic.Int64
	sleep   time.Duration
}

func (f *fakeVerify) verify(raw, _, _ string) (bool, bool) {
	f.calls.Add(1)
	n := f.running.Add(1)
	defer f.running.Add(-1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(f.sleep)
	return raw == "right", false
}

// limitedService returns a service with a fake Verify, a floor of zero, and
// limits of ipLimit failures per address and nameLimit per name.
func limitedService(t *testing.T, ipLimit, nameLimit int, gate *login.Gate) (*login.Service, *fakeVerify, *pgxpool.Pool) {
	t.Helper()
	_, pool := newService(t)
	f := &fakeVerify{}
	svc := login.NewWith(pool, login.Options{
		IPLimit:   ratelimit.New(ipLimit, 15*time.Minute, 10000),
		NameLimit: ratelimit.New(nameLimit, time.Hour, 10000),
		Gate:      gate,
		Verify:    f.verify,
	})
	svc.Floor = 0
	return svc, f, pool
}

func addr(i int) netip.Addr {
	return netip.MustParseAddr(fmt.Sprintf("10.0.%d.%d", i/250, i%250+1))
}

// argonHash returns an argon2id hash string that asks for memory KiB. The fake
// Verify never runs it.
func argonHash(memory int) string {
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=1,p=1$%s$%s", memory,
		enc.EncodeToString([]byte("0123456789abcdef")), enc.EncodeToString(make([]byte, 32)))
}

// Twenty sign-ins at once on a 128 MiB gate, against a hash that needs 64 MiB,
// run two at a time at most, and every one returns.
func TestLoginMemoryBudget(t *testing.T) {
	_, pool := newService(t)
	exec(t, pool, `update users set password_hash = $1, password_scheme = 'gobb' where id = 1`, argonHash(65536))
	f := &fakeVerify{sleep: 20 * time.Millisecond}
	svc := login.NewWith(pool, login.Options{
		IPLimit:   ratelimit.New(10, 15*time.Minute, 10000),
		NameLimit: ratelimit.New(20, time.Hour, 10000),
		Gate:      login.NewGate(131072),
		Verify:    f.verify,
	})
	svc.Floor = 0

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := svc.Check(t.Context(), "alice", "right", addr(i), now)
			if err != nil || id != 1 {
				errs <- fmt.Errorf("Check %d = %d, %v", i, id, err)
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("some calls did not return")
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := f.calls.Load(); got != 20 {
		t.Errorf("Verify ran %d times, want 20", got)
	}
	if peak := f.peak.Load(); peak > 2 || peak < 1 {
		t.Errorf("peak of Verify at once = %d, want 1 or 2 (128 MiB / 64 MiB)", peak)
	}
}

func TestCheckLimits(t *testing.T) {
	svc, f, _ := limitedService(t, 10, 20, nil)
	ctx := t.Context()
	bad := netip.MustParseAddr("10.0.0.9")

	for i := range 10 {
		if _, err := svc.Check(ctx, "alice", "wrong", bad, now); !errors.Is(err, login.ErrWrong) {
			t.Fatalf("try %d: %v, want ErrWrong", i+1, err)
		}
	}
	calls := f.calls.Load()
	_, err := svc.Check(ctx, "alice", "right", bad, now)
	var limited *login.LimitedError
	if !errors.As(err, &limited) {
		t.Fatalf("11th try = %v, want *LimitedError", err)
	}
	if limited.RetryAfter <= 0 || limited.RetryAfter > 15*time.Minute {
		t.Errorf("RetryAfter = %v, want in (0, 15m]", limited.RetryAfter)
	}
	if f.calls.Load() != calls {
		t.Error("Verify ran for a limited try")
	}
	// A limited try does not extend the wait.
	_, err = svc.Check(ctx, "alice", "right", bad, now.Add(time.Minute))
	var again *login.LimitedError
	if !errors.As(err, &again) || again.RetryAfter >= limited.RetryAfter {
		t.Errorf("a later limited try: %v (first wait %v)", err, limited.RetryAfter)
	}

	// The same name from another address still works: 10 of its 20 failures.
	if id, err := svc.Check(ctx, "alice", "right", addr(1), now); err != nil || id != 1 {
		t.Errorf("alice from another address = %d, %v", id, err)
	}
	// After the window the first address works too.
	if id, err := svc.Check(ctx, "alice", "right", bad, now.Add(16*time.Minute)); err != nil || id != 1 {
		t.Errorf("alice after the wait = %d, %v", id, err)
	}
}

// The name limit holds across addresses, and a success clears the name.
func TestCheckNameLimit(t *testing.T) {
	svc, _, _ := limitedService(t, 10, 20, nil)
	ctx := t.Context()
	for i := range 20 {
		if _, err := svc.Check(ctx, "Alice", "wrong", addr(i), now); !errors.Is(err, login.ErrWrong) {
			t.Fatalf("try %d: %v", i+1, err)
		}
	}
	// "alice" cleans to the same key as "Alice".
	_, err := svc.Check(ctx, " alice ", "right", addr(50), now)
	var limited *login.LimitedError
	if !errors.As(err, &limited) {
		t.Fatalf("21st failure's successor = %v, want *LimitedError", err)
	}
	if limited.RetryAfter <= 30*time.Minute || limited.RetryAfter > time.Hour {
		t.Errorf("RetryAfter = %v, want the hour window's wait", limited.RetryAfter)
	}
	// Another name is not affected.
	if id, err := svc.Check(ctx, "bob", "right", addr(50), now); err != nil || id != 2 {
		t.Errorf("bob = %d, %v", id, err)
	}
	// After the window the name works.
	if id, err := svc.Check(ctx, "alice", "right", addr(50), now.Add(61*time.Minute)); err != nil || id != 1 {
		t.Errorf("alice after the window = %d, %v", id, err)
	}
}

func TestCheckSuccessResetsName(t *testing.T) {
	svc, _, _ := limitedService(t, 100, 3, nil)
	ctx := t.Context()
	for round := range 3 {
		for i := range 2 {
			if _, err := svc.Check(ctx, "alice", "wrong", addr(round*10+i), now); !errors.Is(err, login.ErrWrong) {
				t.Fatalf("round %d try %d: %v", round, i, err)
			}
		}
		if id, err := svc.Check(ctx, "alice", "right", addr(round*10+9), now); err != nil || id != 1 {
			t.Fatalf("round %d: right password = %d, %v (the success should clear the name)", round, id, err)
		}
	}
}

// A name with no account is limited like one with an account, and it costs one
// check.
func TestCheckUnknownName(t *testing.T) {
	svc, f, _ := limitedService(t, 100, 3, nil)
	ctx := t.Context()
	if _, err := svc.Check(ctx, "nobody", "wrong", ip, now); !errors.Is(err, login.ErrWrong) {
		t.Fatal(err)
	}
	if got := f.calls.Load(); got != 1 {
		t.Errorf("Verify ran %d times for an unknown name, want 1", got)
	}
	for i := 1; i < 3; i++ {
		_, _ = svc.Check(ctx, "NOBODY", "wrong", addr(i), now)
	}
	_, err := svc.Check(ctx, "nobody", "wrong", addr(9), now)
	var limited *login.LimitedError
	if !errors.As(err, &limited) {
		t.Errorf("unknown name after 3 failures = %v, want *LimitedError", err)
	}
	// The limit answers before any lookup or hash.
	calls := f.calls.Load()
	_, _ = svc.Check(ctx, "alice", "wrong", addr(9), now)
	_, _ = svc.Check(ctx, "nobody", "wrong", addr(10), now)
	if got := f.calls.Load(); got != calls+1 {
		t.Errorf("Verify ran %d more times, want 1 (alice only)", got-calls)
	}
}

// A name that is not valid, or too long, also costs one check, and counts for the
// address.
func TestCheckInvalidNameCountsForAddress(t *testing.T) {
	svc, f, _ := limitedService(t, 2, 100, nil)
	ctx := t.Context()
	for _, name := range []string{"\xff", string(make([]byte, 600))} {
		if _, err := svc.Check(ctx, name, "wrong", ip, now); !errors.Is(err, login.ErrWrong) {
			t.Fatalf("name %q: %v", name, err)
		}
	}
	if got := f.calls.Load(); got != 2 {
		t.Errorf("Verify ran %d times for two bad names, want 2", got)
	}
	var limited *login.LimitedError
	if _, err := svc.Check(ctx, "alice", "right", ip, now); !errors.As(err, &limited) {
		t.Errorf("after two failures from one address: %v, want *LimitedError", err)
	}
}

// A wrong password waits for the floor after it lets go of the gate, so one slow
// failure does not hold the next sign-in.
func TestFloorDoesNotHoldGate(t *testing.T) {
	_, pool := newService(t)
	f := &fakeVerify{}
	svc := login.NewWith(pool, login.Options{
		IPLimit:   ratelimit.New(10, 15*time.Minute, 100),
		NameLimit: ratelimit.New(20, time.Hour, 100),
		// One check of a new gobb hash at a time.
		Gate:   login.NewGate(1024),
		Verify: f.verify,
	})
	svc.Floor = 300 * time.Millisecond
	start := time.Now()
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = svc.Check(t.Context(), "nobody", "wrong", addr(i), now)
		}()
	}
	wg.Wait()
	// Held through the floor, the two would take 600 ms.
	if took := time.Since(start); took >= 550*time.Millisecond {
		t.Errorf("two failures took %v: the floor held the gate", took)
	}
}

// A limited try answers at once. It does not wait for the floor.
func TestLimitedDoesNotWait(t *testing.T) {
	svc, _, _ := limitedService(t, 1, 100, nil)
	svc.Floor = time.Minute
	ctx, cancelFirst := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancelFirst()
	_, _ = svc.Check(ctx, "alice", "wrong", ip, now) // stops waiting when ctx ends
	start := time.Now()
	var limited *login.LimitedError
	if _, err := svc.Check(t.Context(), "alice", "right", ip, now); !errors.As(err, &limited) {
		t.Fatalf("err = %v, want *LimitedError", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("a limited try took %v", took)
	}
}

// A full gate answers ErrBusy without the floor, and without a count against the
// address.
func TestCheckBusy(t *testing.T) {
	gate := login.NewGate(1024)
	svc, f, _ := limitedService(t, 1, 100, gate)
	svc.GateWait = 50 * time.Millisecond
	svc.Floor = time.Minute
	hold, err := gate.Acquire(t.Context(), 1024, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := svc.Check(t.Context(), "alice", "right", ip, now); !errors.Is(err, login.ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("ErrBusy took %v", took)
	}
	if f.calls.Load() != 0 {
		t.Error("Verify ran on a full gate")
	}
	hold()
	// The busy try did not count: this address has a limit of 1 failure.
	if id, err := svc.Check(t.Context(), "alice", "right", ip, now); err != nil || id != 1 {
		t.Errorf("after the gate freed = %d, %v", id, err)
	}
}
