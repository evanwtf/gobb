package login_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/password"
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

// Twenty sign-ins at once on a 128 MiB gate, against a hash that asks for
// 64 MiB, run two checks at a time at most, and every one returns. The test
// counts the calls of a fake Verify that allocates nothing, so it shows the
// concurrency that the gate allows, not the memory that the checks use. The CI
// burst step under the 256 MiB cap tests the memory (.github/workflows/ci.yml).
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
	_, _ = svc.Check(t.Context(), "alice", "wrong", ip, now) // no floor yet
	svc.Floor = time.Minute
	start := time.Now()
	var limited *login.LimitedError
	if _, err := svc.Check(t.Context(), "alice", "right", ip, now); !errors.As(err, &limited) {
		t.Fatalf("err = %v, want *LimitedError", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("a limited try took %v", took)
	}
}

// A full gate answers ErrBusy after the floor, and without a count against the
// address.
func TestCheckBusy(t *testing.T) {
	gate := login.NewGate(1024)
	svc, f, _ := limitedService(t, 1, 100, gate)
	svc.GateWait = 50 * time.Millisecond
	svc.Floor = 200 * time.Millisecond
	hold, err := gate.Acquire(t.Context(), 1024, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := svc.Check(t.Context(), "alice", "right", ip, now); !errors.Is(err, login.ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if took := time.Since(start); took < svc.Floor || took > 5*time.Second {
		t.Errorf("ErrBusy took %v, want the floor %v", took, svc.Floor)
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

// flood sends n wrong guesses at once and returns how many were limited.
func flood(t *testing.T, svc *login.Service, n int, name string, from func(i int) netip.Addr) (limited int64) {
	t.Helper()
	var wg sync.WaitGroup
	var lim atomic.Int64
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Check(t.Context(), name, "wrong", from(i), now)
			var l *login.LimitedError
			if errors.As(err, &l) {
				lim.Add(1)
			} else if !errors.Is(err, login.ErrWrong) {
				t.Errorf("Check %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	return lim.Load()
}

// Parallel guesses cannot pass a limit that the first of them has not reached:
// the limit is taken under one lock before the hash.
func TestParallelGuessesFromOneIP(t *testing.T) {
	svc, f, _ := limitedService(t, 10, 1000, nil)
	f.sleep = 20 * time.Millisecond
	limited := flood(t, svc, 50, "alice", func(int) netip.Addr { return ip })
	if got := f.calls.Load(); got > 10 {
		t.Errorf("Verify ran %d times for 50 parallel guesses, want at most 10", got)
	}
	if limited < 40 {
		t.Errorf("%d tries were limited, want at least 40", limited)
	}
}

func TestParallelGuessesAtOneName(t *testing.T) {
	svc, f, _ := limitedService(t, 1000, 20, nil)
	f.sleep = 20 * time.Millisecond
	limited := flood(t, svc, 50, "alice", func(i int) netip.Addr { return addr(i) })
	if got := f.calls.Load(); got > 20 {
		t.Errorf("Verify ran %d times for 50 parallel guesses at one name, want at most 20", got)
	}
	if limited < 30 {
		t.Errorf("%d tries were limited, want at least 30", limited)
	}
}

// An IPv6 client holds a /64, so its addresses share one budget. An IPv4-mapped
// address is the IPv4 address.
func TestIPv6KeyedBy64(t *testing.T) {
	svc, _, _ := limitedService(t, 3, 1000, nil)
	ctx := t.Context()
	for i := range 3 {
		a := netip.MustParseAddr(fmt.Sprintf("2001:db8:1:2::%x", i+1))
		if _, err := svc.Check(ctx, "alice", "wrong", a, now); !errors.Is(err, login.ErrWrong) {
			t.Fatalf("try %d: %v", i+1, err)
		}
	}
	var limited *login.LimitedError
	same := netip.MustParseAddr("2001:db8:1:2:ffff:ffff:ffff:ffff")
	if _, err := svc.Check(ctx, "alice", "right", same, now); !errors.As(err, &limited) {
		t.Errorf("another address of the same /64: %v, want *LimitedError", err)
	}
	other := netip.MustParseAddr("2001:db8:1:3::1")
	if id, err := svc.Check(ctx, "alice", "right", other, now); err != nil || id != 1 {
		t.Errorf("an address of another /64 = %d, %v", id, err)
	}
	// IPv4 stays per address; a mapped address is the IPv4 address.
	for range 3 {
		_, _ = svc.Check(ctx, "alice", "wrong", netip.MustParseAddr("::ffff:10.7.7.7"), now)
	}
	if _, err := svc.Check(ctx, "alice", "right", netip.MustParseAddr("10.7.7.7"), now); !errors.As(err, &limited) {
		t.Errorf("plain form of a mapped address: %v, want *LimitedError", err)
	}
	if id, err := svc.Check(ctx, "alice", "right", netip.MustParseAddr("10.7.7.8"), now); err != nil || id != 1 {
		t.Errorf("the next IPv4 address = %d, %v", id, err)
	}
}

// When both limits are over, the longer wait is the answer.
func TestLongerWaitWins(t *testing.T) {
	svc, _, _ := limitedService(t, 2, 2, nil)
	ctx := t.Context()
	// The name has 2 failures at now, from other addresses; the address has 2
	// failures at now+10m, so its window ends at now+25m and the name's at now+60m.
	for i := range 2 {
		_, _ = svc.Check(ctx, "alice", "wrong", addr(i), now)
	}
	later := now.Add(10 * time.Minute)
	for range 2 {
		_, _ = svc.Check(ctx, "nobody", "wrong", ip, later)
	}
	var limited *login.LimitedError
	_, err := svc.Check(ctx, "alice", "right", ip, later)
	if !errors.As(err, &limited) {
		t.Fatalf("err = %v", err)
	}
	if want := 50 * time.Minute; limited.RetryAfter != want {
		t.Errorf("RetryAfter = %v, want %v (the name's wait, not the address's 15m)", limited.RetryAfter, want)
	}
	// With the name free again, only the address's wait remains.
	_, err = svc.Check(ctx, "bob", "right", ip, later)
	if !errors.As(err, &limited) || limited.RetryAfter != 15*time.Minute {
		t.Errorf("address only: %v, want 15m", err)
	}
}

// A try that did not fail the password gives its reservation back.
func TestRefunds(t *testing.T) {
	ctx := t.Context()
	t.Run("inactive", func(t *testing.T) {
		svc, _, pool := limitedService(t, 1, 1, nil)
		exec(t, pool, `update users set password_hash = (select password_hash from users where id = 1), password_scheme = 'gobb' where id = 4`)
		for range 3 {
			if _, err := svc.Check(ctx, "dave", "right", ip, now); !errors.Is(err, login.ErrInactive) {
				t.Fatalf("err = %v, want ErrInactive each time", err)
			}
		}
	})
	t.Run("banned", func(t *testing.T) {
		svc, _, _ := limitedService(t, 1, 1, nil)
		var banned *login.BannedError
		for range 3 {
			if _, err := svc.Check(ctx, "alice", "right", netip.MustParseAddr("10.99.1.2"), now); !errors.As(err, &banned) {
				t.Fatalf("err = %v, want a ban each time", err)
			}
		}
	})
	t.Run("database error", func(t *testing.T) {
		svc, _, _ := limitedService(t, 1, 1, nil)
		dead, cancel := context.WithCancel(ctx)
		cancel()
		for range 3 {
			if _, err := svc.Check(dead, "alice", "right", ip, now); err == nil {
				t.Fatal("no error with a dead context")
			}
		}
		if id, err := svc.Check(ctx, "alice", "right", ip, now); err != nil || id != 1 {
			t.Errorf("after the errors = %d, %v", id, err)
		}
	})
	t.Run("success", func(t *testing.T) {
		svc, _, _ := limitedService(t, 1, 1, nil)
		for range 3 {
			if id, err := svc.Check(ctx, "alice", "right", ip, now); err != nil || id != 1 {
				t.Fatalf("good sign-in = %d, %v", id, err)
			}
		}
	})
}

// A name that the limit holds can still be used from an address that the account
// has used before. The address limit still applies there. A stranger gets the same
// 429 whether or not the account exists.
func TestKnownAddressSkipsNameLimit(t *testing.T) {
	svc, f, pool := limitedService(t, 5, 3, nil)
	ctx := t.Context()
	home := netip.MustParseAddr("10.5.5.5")
	homeV6 := netip.MustParseAddr("2001:db8:aa:bb::1")
	exec(t, pool, `insert into user_ips (user_id, ip, last_seen_at) values (1, $1, $3), (1, $2, $3)`, home, homeV6, now)

	for i := range 3 {
		if _, err := svc.Check(ctx, "alice", "wrong", addr(i), now); !errors.Is(err, login.ErrWrong) {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	var limited *login.LimitedError
	if _, err := svc.Check(ctx, "alice", "right", addr(20), now); !errors.As(err, &limited) {
		t.Fatalf("a stranger's address: %v, want *LimitedError", err)
	}
	// The name is locked for a stranger. A wrong password at home is still wrong,
	// and counts against the address.
	if _, err := svc.Check(ctx, "alice", "wrong", home, now); !errors.Is(err, login.ErrWrong) {
		t.Errorf("wrong password from home: %v, want ErrWrong", err)
	}
	// The right password at home signs in, and clears the name.
	if id, err := svc.Check(ctx, "alice", "right", home, now); err != nil || id != 1 {
		t.Fatalf("home with the right password = %d, %v", id, err)
	}
	if id, err := svc.Check(ctx, "alice", "right", addr(21), now); err != nil || id != 1 {
		t.Errorf("a stranger after the name was cleared = %d, %v", id, err)
	}

	// Lock the name again. Another address of the home /64 counts as home.
	for i := range 3 {
		_, _ = svc.Check(ctx, "alice", "wrong", addr(30+i), now)
	}
	if _, err := svc.Check(ctx, "alice", "right", addr(40), now); !errors.As(err, &limited) {
		t.Fatalf("locked again: %v", err)
	}
	near := netip.MustParseAddr("2001:db8:aa:bb::77")
	if id, err := svc.Check(ctx, "alice", "right", near, now); err != nil || id != 1 {
		t.Errorf("an address in the home /64 = %d, %v", id, err)
	}

	// An unknown name is locked the same way for everyone, and a known address of
	// someone else does not help.
	for i := range 3 {
		_, _ = svc.Check(ctx, "nobody", "wrong", addr(50+i), now)
	}
	before := f.calls.Load()
	if _, err := svc.Check(ctx, "nobody", "right", home, now); !errors.As(err, &limited) {
		t.Errorf("unknown name from alice's home: %v, want *LimitedError", err)
	}
	if _, err := svc.Check(ctx, "nobody", "right", addr(60), now); !errors.As(err, &limited) {
		t.Errorf("unknown name from a stranger: %v, want *LimitedError", err)
	}
	if f.calls.Load() != before {
		t.Error("Verify ran for a limited unknown name")
	}
}

// The address limit holds at a known address too.
func TestKnownAddressStillHasIPLimit(t *testing.T) {
	svc, _, pool := limitedService(t, 2, 1, nil)
	ctx := t.Context()
	home := netip.MustParseAddr("10.5.5.5")
	exec(t, pool, `insert into user_ips (user_id, ip, last_seen_at) values (1, $1, $2)`, home, now)
	_, _ = svc.Check(ctx, "alice", "wrong", addr(1), now) // the name limit is 1
	for range 2 {
		if _, err := svc.Check(ctx, "alice", "wrong", home, now); !errors.Is(err, login.ErrWrong) {
			t.Fatalf("from home: %v", err)
		}
	}
	var limited *login.LimitedError
	if _, err := svc.Check(ctx, "alice", "right", home, now); !errors.As(err, &limited) {
		t.Errorf("third try from home: %v, want *LimitedError", err)
	}
}

// The first sign-in after the cutover rehashes a legacy hash. The hash runs
// inside the gate. When the gate stays full, the sign-in still succeeds, the old
// hash stays, and the next sign-in tries again.
func TestRehashUsesGate(t *testing.T) {
	_, pool := newService(t)
	gate := login.NewGate(1024)
	var hold atomic.Pointer[func()]
	verify := func(raw, stored, scheme string) (bool, bool) {
		ok, rehash := password.Verify(raw, stored, scheme)
		if ok && hold.Load() == nil {
			// While this check holds the gate, queue a holder for all of it. It
			// runs first when this check lets go.
			go func() {
				rel, err := gate.Acquire(context.Background(), 1024, 10*time.Second)
				if err == nil {
					hold.Store(&rel)
				}
			}()
			time.Sleep(100 * time.Millisecond)
		}
		return ok, rehash
	}
	svc := login.NewWith(pool, login.Options{Gate: gate, Verify: verify})
	svc.Floor = 0
	svc.GateWait = 100 * time.Millisecond

	id, err := svc.Check(t.Context(), "bob", pw, ip, now)
	if err != nil || id != 2 {
		t.Fatalf("Check = %d, %v; want 2, nil (a busy gate does not fail the sign-in)", id, err)
	}
	var scheme string
	if err := pool.QueryRow(t.Context(), `select password_scheme from users where id = 2`).Scan(&scheme); err != nil || scheme != "legacy" {
		t.Errorf("scheme = %q (%v): the rehash ran outside the gate", scheme, err)
	}
	rel := hold.Load()
	if rel == nil {
		t.Fatal("the test holder never got the gate")
	}
	(*rel)()
	svc.GateWait = 5 * time.Second
	if id, err := svc.Check(t.Context(), "bob", pw, ip, now); err != nil || id != 2 {
		t.Fatalf("second Check = %d, %v", id, err)
	}
	if err := pool.QueryRow(t.Context(), `select password_scheme from users where id = 2`).Scan(&scheme); err != nil || scheme != "gobb" {
		t.Errorf("scheme = %q (%v) after the gate freed, want gobb", scheme, err)
	}
}

// A try that the name limit refuses keeps its address event: one address cannot
// send unlimited lookups at a locked name.
func TestNameRefusalSpendsAddressBudget(t *testing.T) {
	svc, f, _ := limitedService(t, 10, 3, nil)
	ctx := t.Context()
	for i := range 3 {
		_, _ = svc.Check(ctx, "alice", "wrong", addr(i), now)
	}
	var limited *login.LimitedError
	spender := netip.MustParseAddr("10.8.8.8")
	for i := range 10 {
		if _, err := svc.Check(ctx, "alice", "right", spender, now); !errors.As(err, &limited) {
			t.Fatalf("try %d at the locked name: %v, want *LimitedError", i+1, err)
		}
	}
	// The 11th try is over the limit of the address, for any name.
	if _, err := svc.Check(ctx, "alice", "right", spender, now); !errors.As(err, &limited) {
		t.Fatalf("11th try: %v", err)
	}
	calls := f.calls.Load()
	if _, err := svc.Check(ctx, "bob", "right", spender, now); !errors.As(err, &limited) {
		t.Errorf("another name from the same address: %v, want *LimitedError (the address is spent)", err)
	}
	if f.calls.Load() != calls {
		t.Error("Verify ran for an address over its limit")
	}
	// Another address is not affected.
	if id, err := svc.Check(ctx, "bob", "right", addr(100), now); err != nil || id != 2 {
		t.Errorf("bob from another address = %d, %v", id, err)
	}
}

// A try that the address limit refuses takes nothing from the name limit, not
// even for a moment. A name event that was taken and given back would still have
// pushed another key out of a full limiter.
func TestAddressRefusalTouchesNoName(t *testing.T) {
	_, pool := newService(t)
	f := &fakeVerify{}
	names := ratelimit.New(3, time.Hour, 3)
	svc := login.NewWith(pool, login.Options{
		IPLimit:   ratelimit.New(1, 15*time.Minute, 100),
		NameLimit: names,
		Verify:    f.verify,
	})
	svc.Floor = 0
	ctx := t.Context()
	for i := range 3 {
		names.Add("alice", now.Add(time.Duration(i)*time.Second))
	}
	names.Add("bob", now)
	spent := netip.MustParseAddr("10.8.8.8")
	if _, err := svc.Check(ctx, "nobody", "wrong", spent, now); !errors.Is(err, login.ErrWrong) {
		t.Fatal(err)
	}
	// "carol" is a new key in a full limiter. A refused try must not add it.
	var limited *login.LimitedError
	if _, err := svc.Check(ctx, "carol", "right", spent, now); !errors.As(err, &limited) {
		t.Fatalf("err = %v, want *LimitedError", err)
	}
	// The limiter is full (alice, bob, nobody). A refused try that took an event
	// for "carol" would have pushed one of them out.
	if !names.Has("alice") || !names.Has("bob") || !names.Has("nobody") || names.Has("carol") {
		t.Errorf("name keys after a refused try: alice=%t bob=%t nobody=%t carol=%t; want true, true, true, false",
			names.Has("alice"), names.Has("bob"), names.Has("nobody"), names.Has("carol"))
	}
	// The wait of a full name still counts: alice is over, from the same address.
	_, err := svc.Check(ctx, "alice", "right", spent, now.Add(time.Minute))
	if !errors.As(err, &limited) || limited.RetryAfter < 30*time.Minute {
		t.Errorf("both over: %v, want the name's wait of about an hour", err)
	}
}

// An address that cannot be read is never a known address, and a sign-in from it
// is not stored in user_ips.
func TestUnspecifiedAddressIsNeverKnown(t *testing.T) {
	svc, _, pool := limitedService(t, 100, 2, nil)
	ctx := t.Context()
	exec(t, pool, `insert into user_ips (user_id, ip, last_seen_at) values (1, '0.0.0.0', $1), (1, '::', $1)`, now)
	for i := range 2 {
		_, _ = svc.Check(ctx, "alice", "wrong", addr(i), now)
	}
	var limited *login.LimitedError
	for _, a := range []netip.Addr{netip.IPv4Unspecified(), netip.IPv6Unspecified(), netip.MustParseAddr("::ffff:0.0.0.0")} {
		if _, err := svc.Check(ctx, "alice", "right", a, now); !errors.As(err, &limited) {
			t.Errorf("from %v: %v, want *LimitedError", a, err)
		}
	}
}

// querySpy counts the queries that mention user_ips.
type querySpy struct {
	db.Querier
	userIPs atomic.Int64
}

func (q *querySpy) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.Contains(sql, "user_ips") {
		q.userIPs.Add(1)
	}
	return q.Querier.QueryRow(ctx, sql, args...)
}

// The user_ips query runs only when the name limit is over.
func TestKnownAddressQueryOnlyWhenNameLimited(t *testing.T) {
	_, pool := newService(t)
	spy := &querySpy{Querier: pool}
	f := &fakeVerify{}
	svc := login.NewWith(spy, login.Options{
		IPLimit:   ratelimit.New(100, 15*time.Minute, 100),
		NameLimit: ratelimit.New(2, time.Hour, 100),
		Verify:    f.verify,
	})
	svc.Floor = 0
	ctx := t.Context()
	_, _ = svc.Check(ctx, "alice", "wrong", addr(1), now)
	if id, err := svc.Check(ctx, "alice", "right", addr(1), now); err != nil || id != 1 {
		t.Fatal(id, err)
	}
	_, _ = svc.Check(ctx, "nobody", "wrong", addr(1), now)
	_, _ = svc.Check(ctx, "\xff", "wrong", addr(1), now)
	if n := spy.userIPs.Load(); n != 0 {
		t.Errorf("the user_ips query ran %d times with no name over its limit", n)
	}
	for i := range 2 {
		_, _ = svc.Check(ctx, "alice", "wrong", addr(10+i), now)
	}
	_, _ = svc.Check(ctx, "alice", "right", addr(20), now)
	if n := spy.userIPs.Load(); n != 1 {
		t.Errorf("the user_ips query ran %d times for one name-limited try, want 1", n)
	}
}

// gateHeld returns a service on the small board where carol holds an imported
// argon2id hash at m=65,536, with the charge set as the start-up step sets it.
// The gate is in the state of the round-3 re-review: the default budget of
// 131,072 KiB with four holders of 19,456 KiB, so 53,248 KiB is free. A check
// that charged 19 MiB would fit; a check that charges 64 MiB does not. The
// service has a floor of 300 ms and a gate wait of 60 ms.
func gateHeld(t *testing.T) (*login.Service, *fakeVerify) {
	t.Helper()
	_, pool := newService(t)
	exec(t, pool, `update users set password_hash = $1, password_scheme = 'legacy' where id = 3`, argonHash(65536))
	// bob, carol and dave share one email address; alice keeps her own.
	exec(t, pool, `update users set email = 'shared@example.com', email_clean = 'shared@example.com' where id in (2, 3, 4)`)
	gate := login.NewGate(login.DefaultBudgetKiB)
	for range 4 {
		hold, err := gate.Acquire(t.Context(), password.HashMemoryKiB, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(hold)
	}
	// The state: 19 MiB fits, 64 MiB does not.
	probe, err := gate.Acquire(t.Context(), password.HashMemoryKiB, time.Millisecond)
	if err != nil {
		t.Fatalf("19 MiB does not fit in the held gate: %v", err)
	}
	probe()
	if _, err := gate.Acquire(t.Context(), 65536, time.Millisecond); !errors.Is(err, login.ErrBusy) {
		t.Fatalf("64 MiB in the held gate: err = %v, want ErrBusy", err)
	}
	f := &fakeVerify{}
	svc := login.NewWith(pool, login.Options{Gate: gate, Verify: f.verify})
	if err := svc.LoadCharge(t.Context()); err != nil {
		t.Fatal(err)
	}
	svc.Floor = 300 * time.Millisecond
	svc.GateWait = 60 * time.Millisecond
	return svc, f
}

// The kinds of name that a stranger can try: a legacy bcrypt hash (1,024 KiB by
// its own cost), a gobb hash (19,456 KiB), an imported argon2id hash (65,536
// KiB), no account at all, an email shared by three accounts and a unique email.
var hashKinds = []struct{ kind, name string }{
	{"legacy hash", "bob"},
	{"gobb hash", "alice"},
	{"legacy argon2id at 64 MiB", "carol"},
	{"unknown name", "nobody"},
	{"shared email", "shared@example.com"},
	{"unique email", "alice@example.com"},
}

// With the gate full for a hash of 64 MiB and not for 19 MiB, every account, a
// shared or unique email and an unknown name wait for the gate: they all charge the start-up charge of
// 64 MiB. The wait ends in ErrBusy (503) for each, no sooner than the floor. A
// name whose check fitted would get ErrWrong (200) instead, and the status would
// tell a stranger that the account exists.
func TestCheckFloorHoldsOnBusy(t *testing.T) {
	for _, k := range hashKinds {
		t.Run(k.kind, func(t *testing.T) {
			svc, f := gateHeld(t)
			start := time.Now()
			_, err := svc.Check(t.Context(), k.name, "wrong", ip, now)
			took := time.Since(start)
			if !errors.Is(err, login.ErrBusy) {
				t.Errorf("err = %v, want ErrBusy", err)
			}
			if took < svc.Floor {
				t.Errorf("answered after %v, before the floor %v", took, svc.Floor)
			}
			if f.calls.Load() != 0 {
				t.Error("Verify ran with no room in the gate")
			}
		})
	}
}

// A context that ends during the gate wait (the client half-closed) gives the
// same error for every kind of name, and no answer before the floor.
func TestCheckFloorHoldsOnGateCancel(t *testing.T) {
	for _, k := range hashKinds {
		t.Run(k.kind, func(t *testing.T) {
			svc, f := gateHeld(t)
			svc.GateWait = 10 * time.Second
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			time.AfterFunc(50*time.Millisecond, cancel)
			start := time.Now()
			_, err := svc.Check(ctx, k.name, "wrong", ip, now)
			took := time.Since(start)
			if !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v, want context.Canceled", err)
			}
			if took < svc.Floor {
				t.Errorf("answered after %v, before the floor %v", took, svc.Floor)
			}
			if f.calls.Load() != 0 {
				t.Error("Verify ran with no room in the gate")
			}
		})
	}
}

// Every failure that follows a reservation takes the floor: an inactive account, a
// ban, and a lookup that fails. A good password does not.
func TestCheckFloorHoldsOnEveryFailure(t *testing.T) {
	svc, _, pool := limitedService(t, 100, 100, nil)
	svc.Floor = 300 * time.Millisecond
	exec(t, pool, `update users set password_hash = (select password_hash from users where id = 1), password_scheme = 'gobb' where id = 4`)
	dead, cancel := context.WithCancel(t.Context())
	cancel()
	for _, c := range []struct {
		name string
		ctx  context.Context
		user string
		from netip.Addr
		want func(error) bool
	}{
		{"inactive", t.Context(), "dave", ip, func(err error) bool { return errors.Is(err, login.ErrInactive) }},
		{"banned", t.Context(), "alice", netip.MustParseAddr("10.99.1.2"), func(err error) bool {
			var b *login.BannedError
			return errors.As(err, &b)
		}},
		{"lookup cancelled", dead, "alice", ip, func(err error) bool { return errors.Is(err, context.Canceled) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			start := time.Now()
			_, err := svc.Check(c.ctx, c.user, "right", c.from, now)
			if !c.want(err) {
				t.Fatalf("err = %v", err)
			}
			if took := time.Since(start); took < svc.Floor {
				t.Errorf("answered after %v, before the floor %v", took, svc.Floor)
			}
		})
	}
	start := time.Now()
	if id, err := svc.Check(t.Context(), "alice", "right", ip, now); err != nil || id != 1 {
		t.Fatalf("good sign-in = %d, %v", id, err)
	}
	if took := time.Since(start); took >= svc.Floor {
		t.Errorf("a good password took %v, want no wait", took)
	}
}
