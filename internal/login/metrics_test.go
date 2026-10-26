package login_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/metrics"
	"github.com/evanwtf/gobb/internal/ratelimit"
)

// Each check adds one to gobb_signin_attempts_total under its result.
func TestCheckCountsResults(t *testing.T) {
	svc, _ := newService(t)
	for _, c := range []struct {
		name, pass string
		from       netip.Addr
		result     string
	}{
		{"alice", pw, ip, "ok"},
		{"alice", "wrong", ip, "wrong"},
		{"nobody", pw, ip, "wrong"},
		{"alice", pw, netip.MustParseAddr("10.99.1.2"), "banned"}, // an address ban of the small board
	} {
		before := metrics.SigninAttempts.Value(c.result)
		_, _ = svc.Check(t.Context(), c.name, c.pass, c.from, now)
		if got := metrics.SigninAttempts.Value(c.result); got != before+1 {
			t.Errorf("%s/%s: %s = %d, want %d", c.name, c.pass, c.result, got, before+1)
		}
	}
}

// A limited try counts as limited, and the limit of the address counts its
// refusal under its name.
func TestCheckCountsLimited(t *testing.T) {
	_, pool := newService(t)
	svc := login.NewWith(pool, login.DefaultOptions())
	svc.Floor = time.Millisecond
	bad := netip.MustParseAddr("10.0.0.9")
	for range login.DefaultIPFailures {
		if _, err := svc.Check(t.Context(), "alice", "wrong", bad, now); !errors.Is(err, login.ErrWrong) {
			t.Fatalf("a wrong try = %v", err)
		}
	}
	limited := metrics.SigninAttempts.Value("limited")
	addr, name := metrics.RateLimited.Value("signin_address"), metrics.RateLimited.Value("signin_name")
	var le *login.LimitedError
	if _, err := svc.Check(context.Background(), "alice", pw, bad, now); !errors.As(err, &le) {
		t.Fatalf("the try after the limit = %v, want *LimitedError", err)
	}
	if got := metrics.SigninAttempts.Value("limited"); got != limited+1 {
		t.Errorf("limited = %d, want %d", got, limited+1)
	}
	// The address limit refused. The name's wait was only read, so the name limit
	// counts nothing.
	if got := metrics.RateLimited.Value("signin_address") - addr; got != 1 {
		t.Errorf("signin_address refusals +%d, want +1", got)
	}
	if got := metrics.RateLimited.Value("signin_name") - name; got != 0 {
		t.Errorf("signin_name refusals +%d, want +0", got)
	}
}

// A full name limit lets the member through from an address that the account has
// used. That is no refusal, and signin_name does not count it. A stranger's try
// at the locked name is one.
func TestKnownAddressIsNoRefusal(t *testing.T) {
	_, pool := newService(t)
	opt := login.DefaultOptions()
	opt.NameLimit = ratelimit.New(3, time.Hour, 100).Named("signin_name")
	svc := login.NewWith(pool, opt)
	svc.Floor = time.Millisecond
	home := netip.MustParseAddr("10.5.5.5")
	exec(t, pool, `insert into user_ips (user_id, ip, last_seen_at) values (1, $1, $2)`, home, now)
	for i := range 3 {
		_, _ = svc.Check(t.Context(), "alice", "wrong", netip.AddrFrom4([4]byte{10, 7, 0, byte(i + 1)}), now)
	}
	name := metrics.RateLimited.Value("signin_name")
	var le *login.LimitedError
	if _, err := svc.Check(t.Context(), "alice", pw, netip.MustParseAddr("10.7.0.9"), now); !errors.As(err, &le) {
		t.Fatalf("a stranger at the locked name = %v, want *LimitedError", err)
	}
	if got := metrics.RateLimited.Value("signin_name") - name; got != 1 {
		t.Errorf("after the stranger: signin_name +%d, want +1", got)
	}
	if id, err := svc.Check(t.Context(), "alice", pw, home, now); err != nil || id != 1 {
		t.Fatalf("alice from home = %d, %v", id, err)
	}
	if got := metrics.RateLimited.Value("signin_name") - name; got != 1 {
		t.Errorf("after alice from home: signin_name +%d, want still +1", got)
	}
}
