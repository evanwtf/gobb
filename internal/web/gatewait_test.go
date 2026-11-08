package web

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/login"
)

// A sign-in that finds the gate full answers 503 after the gate wait, not
// later: the refusal is quick.
func TestSignInBusyGateAnswersWithinTheWait(t *testing.T) {
	gate := login.NewGate(1024)
	const wait = 300 * time.Millisecond
	h, _ := siteAndPool(t, bundletest.Small(), Options{LoginGate: gate, LoginGateWait: wait})
	hold, err := gate.Acquire(t.Context(), 1024, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	start := time.Now()
	rec, _ := postAs(t, h, "/login", nil, url.Values{"name": {"alice"}, "password": {"wrong"}})
	took := time.Since(start)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" {
		t.Fatalf("busy gate = %d, Retry-After %q, want 503 and 5", rec.Code, rec.Header().Get("Retry-After"))
	}
	if took < wait || took > wait+700*time.Millisecond {
		t.Errorf("the 503 took %s, want the gate wait of %s and little more", took, wait)
	}
}

// With no wait set, a sign-in waits the default, which is 1 s.
func TestDefaultGateWaitIsOneSecond(t *testing.T) {
	if login.DefaultGateWait != time.Second {
		t.Errorf("login.DefaultGateWait = %s, want 1s", login.DefaultGateWait)
	}
}
