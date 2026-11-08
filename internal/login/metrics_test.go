package login_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/metrics"
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

// A limited try counts as limited, and gobb_signin_refusals_total counts it
// under its reason.
func TestCheckCountsLimited(t *testing.T) {
	_, pool := newService(t)
	svc := login.NewWith(pool, login.DefaultOptions())
	svc.Floor = time.Millisecond
	bad := netip.MustParseAddr("192.0.2.9")
	for i := range login.DefaultFreeFailures + 1 {
		if _, err := svc.Check(t.Context(), "nobody"+string(rune('a'+i)), "wrong", bad, now); !errors.Is(err, login.ErrWrong) {
			t.Fatalf("a wrong try = %v", err)
		}
	}
	limited := metrics.SigninAttempts.Value("limited")
	addr, name := metrics.SigninRefusals.Value(login.ReasonAddress), metrics.SigninRefusals.Value(login.ReasonName)
	var le *login.LimitedError
	if _, err := svc.Check(context.Background(), "alice", pw, bad, now); !errors.As(err, &le) {
		t.Fatalf("the try after the free failures = %v, want *LimitedError", err)
	}
	if got := metrics.SigninAttempts.Value("limited"); got != limited+1 {
		t.Errorf("limited = %d, want %d", got, limited+1)
	}
	if got := metrics.SigninRefusals.Value(login.ReasonAddress) - addr; got != 1 {
		t.Errorf("address refusals +%d, want +1", got)
	}
	if got := metrics.SigninRefusals.Value(login.ReasonName) - name; got != 0 {
		t.Errorf("name refusals +%d, want +0", got)
	}
}

// A name whose wait holds lets the member through from a trusted address. That
// is no refusal. A stranger's try at the locked name is one, under "name".
func TestKnownAddressIsNoRefusal(t *testing.T) {
	_, pool := newService(t)
	svc := login.NewWith(pool, login.Options{Schedule: login.NewSchedule(login.ScheduleConfig{FreeFailures: 2, LastWait: time.Hour})})
	svc.Floor = time.Millisecond
	home := netip.MustParseAddr("192.0.2.55")
	exec(t, pool, `insert into user_ips (user_id, ip, last_seen_at) values (1, $1, $2)`, home, now)
	for i := range 3 {
		_, _ = svc.Check(t.Context(), "alice", "wrong", netip.AddrFrom4([4]byte{198, 51, 100, byte(i + 1)}), now)
	}
	name := metrics.SigninRefusals.Value(login.ReasonName)
	var le *login.LimitedError
	if _, err := svc.Check(t.Context(), "alice", pw, netip.MustParseAddr("198.51.100.9"), now); !errors.As(err, &le) {
		t.Fatalf("a stranger at the locked name = %v, want *LimitedError", err)
	}
	if got := metrics.SigninRefusals.Value(login.ReasonName) - name; got != 1 {
		t.Errorf("after the stranger: name refusals +%d, want +1", got)
	}
	if id, err := svc.Check(t.Context(), "alice", pw, home, now); err != nil || id != 1 {
		t.Fatalf("alice from home = %d, %v", id, err)
	}
	if got := metrics.SigninRefusals.Value(login.ReasonName) - name; got != 1 {
		t.Errorf("after alice from home: name refusals +%d, want still +1", got)
	}
}
