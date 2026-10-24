package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakePinger func(ctx context.Context) error

func (f fakePinger) Ping(ctx context.Context) error { return f(ctx) }

func serve(t *testing.T, p Pinger, method string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	Health(p).ServeHTTP(rec, httptest.NewRequest(method, "/healthz", nil))
	return rec
}

func TestHealthOK(t *testing.T) {
	rec := serve(t, fakePinger(func(context.Context) error { return nil }), http.MethodGet)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "ok" {
		t.Fatalf("body = %q, want ok", got)
	}
}

func TestHealthDBDown(t *testing.T) {
	rec := serve(t, fakePinger(func(context.Context) error { return errors.New("down") }), http.MethodGet)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "db unavailable" {
		t.Fatalf("body = %q, want db unavailable", got)
	}
}

func TestHealthSlowDB(t *testing.T) {
	// The pinger ignores its context, as a hung driver could.
	release := make(chan struct{})
	defer close(release)
	slow := fakePinger(func(context.Context) error {
		<-release
		return nil
	})
	start := time.Now()
	rec := serve(t, slow, http.MethodGet)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("took %v, want under 3s", elapsed)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHealthMethod(t *testing.T) {
	called := false
	p := fakePinger(func(context.Context) error { called = true; return nil })
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := serve(t, p, m)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", m, rec.Code)
		}
		if rec.Header().Get("Allow") == "" {
			t.Errorf("%s: missing Allow header", m)
		}
	}
	if called {
		t.Error("the pinger ran for a non-GET request")
	}
}

func TestHealthURL(t *testing.T) {
	cases := []struct {
		listen, want string
	}{
		{"127.0.0.1:8080", "http://127.0.0.1:8080/healthz"},
		{"0.0.0.0:8080", "http://127.0.0.1:8080/healthz"},
		{"[::]:9000", "http://127.0.0.1:9000/healthz"},
		{":8081", "http://127.0.0.1:8081/healthz"},
		{"localhost:7000", "http://localhost:7000/healthz"},
		{"[::1]:7000", "http://[::1]:7000/healthz"},
		{"10.231.0.5:8080", "http://10.231.0.5:8080/healthz"},
	}
	for _, c := range cases {
		got, err := HealthURL(c.listen)
		if err != nil {
			t.Errorf("HealthURL(%q): %v", c.listen, err)
			continue
		}
		if got != c.want {
			t.Errorf("HealthURL(%q) = %q, want %q", c.listen, got, c.want)
		}
	}
	for _, bad := range []string{"", "8080", "host:", "a:b:c"} {
		if _, err := HealthURL(bad); err == nil {
			t.Errorf("HealthURL(%q): want an error", bad)
		}
	}
}
