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
	limited, refused := metrics.SigninAttempts.Value("limited"), metrics.RateLimited.Value("signin_address")
	var le *login.LimitedError
	if _, err := svc.Check(context.Background(), "alice", pw, bad, now); !errors.As(err, &le) {
		t.Fatalf("the try after the limit = %v, want *LimitedError", err)
	}
	if got := metrics.SigninAttempts.Value("limited"); got != limited+1 {
		t.Errorf("limited = %d, want %d", got, limited+1)
	}
	if got := metrics.RateLimited.Value("signin_address"); got <= refused {
		t.Errorf("signin_address refusals = %d, want more than %d", got, refused)
	}
}
