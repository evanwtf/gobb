package scripts

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// burstServer is a fake sign-in page. GET /login sets a cookie and holds a csrf
// field. POST /login answers status(n) for the n-th try (from 1), and records
// the name and the X-Forwarded-For address of each try. A try without the
// cookie or the token gets 403.
type burstServer struct {
	mu     sync.Mutex
	tries  int
	names  map[string]int
	addrs  map[string]bool
	status func(n int) int
}

func (b *burstServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/login" {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		http.SetCookie(w, &http.Cookie{Name: "gobb_csrf", Value: "secret"})
		fmt.Fprint(w, `<form><input type="hidden" name="csrf" value="tok"></form>`)
		return
	}
	c, err := r.Cookie("gobb_csrf")
	if err != nil || c.Value != "secret" || r.FormValue("csrf") != "tok" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	b.mu.Lock()
	b.tries++
	n := b.tries
	b.names[r.FormValue("name")]++
	b.addrs[r.Header.Get("X-Forwarded-For")] = true
	b.mu.Unlock()
	w.WriteHeader(b.status(n))
}

func newBurstServer(t *testing.T, status func(int) int) (*burstServer, string) {
	t.Helper()
	b := &burstServer{names: map[string]int{}, addrs: map[string]bool{}, status: status}
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	return b, srv.URL
}

// Every try answers 200: the script passes, sends each try from its own
// address, and gives each name its share.
func TestLoginBurstPasses(t *testing.T) {
	b, url := newBurstServer(t, func(int) int { return http.StatusOK })
	out, code := run(t, t.TempDir(), nil, "login-burst.sh", url, "7", "3", "bob", "carol")
	if code != 0 || !strings.Contains(out, "total: 7 tries: 200 x7") {
		t.Fatalf("exit %d, output:\n%s", code, out)
	}
	if b.tries != 7 || len(b.addrs) != 7 || b.names["bob"] != 4 || b.names["carol"] != 3 {
		t.Errorf("tries %d, addresses %d, names %v; want 7, 7, bob 4 and carol 3", b.tries, len(b.addrs), b.names)
	}
	for _, line := range []string{"tries 1-3: 200 x3", "tries 4-6: 200 x3", "tries 7-7: 200 x1"} {
		if !strings.Contains(out, line) {
			t.Errorf("output lacks %q:\n%s", line, out)
		}
	}
}

// One try that gets another status fails the burst: a 503 of a full gate, a
// 429 of a limit, or a dropped connection.
func TestLoginBurstFailsOnAnyOtherStatus(t *testing.T) {
	for _, bad := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusInternalServerError} {
		_, url := newBurstServer(t, func(n int) int {
			if n == 5 {
				return bad
			}
			return http.StatusOK
		})
		out, code := run(t, t.TempDir(), nil, "login-burst.sh", url, "6", "2", "bob")
		if code != 1 || !strings.Contains(out, fmt.Sprintf("%d x1", bad)) || !strings.Contains(out, "FAIL 1 of 6 tries") {
			t.Errorf("status %d: exit %d, output:\n%s", bad, code, out)
		}
	}
}

// A server that drops every sign-in fails every try, with status 000, and the
// burst fails. (A process that the kernel kills drops its connections so.)
func TestLoginBurstFailsWhenTheServerDrops(t *testing.T) {
	b := &burstServer{names: map[string]int{}, addrs: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			b.ServeHTTP(w, r)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("the test server cannot hijack")
			return
		}
		if conn, _, err := hj.Hijack(); err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(srv.Close)
	out, code := run(t, t.TempDir(), nil, "login-burst.sh", srv.URL, "4", "4", "bob")
	if code != 1 || !strings.Contains(out, "000 x4") {
		t.Errorf("exit %d, output:\n%s", code, out)
	}
}

// A usage error exits 2 and sends nothing.
func TestLoginBurstUsage(t *testing.T) {
	b, url := newBurstServer(t, func(int) int { return http.StatusOK })
	for _, args := range [][]string{
		{url, "4", "2"},
		{url, "four", "2", "bob"},
		{url, "4", "0", "bob"},
	} {
		if out, code := run(t, t.TempDir(), nil, "login-burst.sh", args...); code != 2 {
			t.Errorf("%q: exit %d, want 2:\n%s", args, code, out)
		}
	}
	if b.tries != 0 {
		t.Errorf("a usage error sent %d tries", b.tries)
	}
}
