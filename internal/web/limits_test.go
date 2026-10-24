package web

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/password"
)

// proxied sends form to target as a client at addr behind a trusted proxy: the
// peer is 10.0.0.1 and X-Forwarded-For names the client.
func proxied(t *testing.T, h http.Handler, target, addr string, cookies []*http.Cookie, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "10.0.0.1:4000"
	r.Header.Set("X-Forwarded-For", addr)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func proxyOptions() Options {
	return Options{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}}
}

// csrfFor returns the guest cookies and the csrf token that the sign-in page gives.
func csrfFor(t *testing.T, h http.Handler) ([]*http.Cookie, string) {
	t.Helper()
	page := getAs(t, h, "/login", nil)
	m := csrfField.FindStringSubmatch(bodyOf(t, page))
	if m == nil {
		t.Fatal("the sign-in page has no csrf field")
	}
	return jar(nil, page), m[1]
}

func TestLoginHTTPLimits(t *testing.T) {
	h, _ := siteAndPool(t, bundletest.Small(), proxyOptions())
	cookies, token := csrfFor(t, h)

	// A 1 MiB body answers 413, in the layout, before the csrf check.
	big := proxied(t, h, "/login", "2001:db8::5", cookies, url.Values{"name": {"alice"}, "password": {strings.Repeat("x", 1<<20)}})
	if big.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("1 MiB POST /login = %d, want 413", big.Code)
	}
	if body := bodyOf(t, big); !strings.Contains(body, "Request too large") || !strings.Contains(body, "<html") {
		t.Errorf("the 413 page is not the layout:\n%s", body)
	}

	form := func(pw string) url.Values {
		return url.Values{"name": {"alice"}, "password": {pw}, "csrf": {token}}
	}
	for i := range 10 {
		rec := proxied(t, h, "/login", "2001:db8::5", cookies, form("wrong"))
		if rec.Code != http.StatusOK {
			t.Fatalf("failure %d = %d, want 200", i+1, rec.Code)
		}
	}
	limited := proxied(t, h, "/login", "2001:db8::5", cookies, form(testPassword))
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("11th try = %d, want 429", limited.Code)
	}
	secs, err := strconv.Atoi(limited.Header().Get("Retry-After"))
	if err != nil || secs < 1 || secs > 15*60 {
		t.Errorf("Retry-After = %q (%v), want 1 to 900 seconds", limited.Header().Get("Retry-After"), err)
	}
	body := bodyOf(t, limited)
	// The same layout as /login, the message, and the form still there.
	for _, want := range []string{
		"Try again in 15 minutes.", "Too many sign-in attempts.",
		`action="/login"`, `name="name"`, `name="password"`, `name="csrf"`, `value="alice"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the 429 page lacks %q", want)
		}
	}
	if strings.Contains(body, testPassword) || strings.Contains(body, "wrong") {
		t.Error("the 429 page echoes a password")
	}
	if limited.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", limited.Header().Get("Cache-Control"))
	}
	// Another address signs in at once.
	if ok := proxied(t, h, "/login", "2001:db8:0:6::1", cookies, form(testPassword)); ok.Code != http.StatusSeeOther {
		t.Errorf("another address = %d, want 303", ok.Code)
	}
}

// With no proxy, the peer address is the client (the owner's local browser).
func TestLoginLimitsOnPeerAddress(t *testing.T) {
	h, _ := siteAndPool(t, bundletest.Small(), Options{})
	for i := range 10 {
		rec, _ := postAs(t, h, "/login", nil, url.Values{"name": {"alice"}, "password": {"wrong"}})
		if rec.Code != http.StatusOK {
			t.Fatalf("failure %d = %d", i+1, rec.Code)
		}
	}
	rec, cookies := postAs(t, h, "/login", nil, url.Values{"name": {"alice"}, "password": {testPassword}})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("11th try = %d, want 429", rec.Code)
	}
	if body := bodyOf(t, rec); !strings.Contains(body, `name="password"`) {
		t.Error("the form is gone from the 429 page")
	}
	_ = cookies
}

// The limit is the name's, and a success clears it: here the name limit is
// reached from many addresses, and the right password works after the wait.
func TestLoginNameLimitHTTP(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	clock := now
	opt := proxyOptions()
	opt.Now = func() time.Time { return clock }
	h, _ := siteAndPool(t, bundletest.Small(), opt)
	cookies, token := csrfFor(t, h)
	form := func(pw string) url.Values {
		return url.Values{"name": {"bob"}, "password": {pw}, "csrf": {token}}
	}
	for i := range 20 {
		addr := "2001:db8:" + strconv.Itoa(0x100+i) + "::1" // one /64 each
		if rec := proxied(t, h, "/login", addr, cookies, form("wrong")); rec.Code != http.StatusOK {
			t.Fatalf("failure %d = %d", i+1, rec.Code)
		}
	}
	rec := proxied(t, h, "/login", "2001:db8:900::1", cookies, form(testPassword))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("21st = %d, want 429", rec.Code)
	}
	if body := bodyOf(t, rec); !strings.Contains(body, "Try again in 60 minutes.") {
		t.Errorf("the page does not state the wait:\n%s", body)
	}
	// After the wait, the right password works.
	clock = clock.Add(61 * time.Minute)
	if ok := proxied(t, h, "/login", "2001:db8:900::1", cookies, form(testPassword)); ok.Code != http.StatusSeeOther {
		t.Errorf("after the wait = %d, want 303", ok.Code)
	}
}

func TestLimitedMinutes(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "1 minute"},
		{time.Millisecond, "1 minute"},
		{time.Second, "1 minute"},
		{time.Minute, "1 minute"},
		{time.Minute + time.Nanosecond, "2 minutes"},
		{90 * time.Second, "2 minutes"},
		{14*time.Minute + 59*time.Second, "15 minutes"},
		{15 * time.Minute, "15 minutes"},
		{time.Hour, "60 minutes"},
	} {
		if got := waitText(tc.d); got != tc.want {
			t.Errorf("waitText(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
	if secs := retrySeconds(0); secs != 1 {
		t.Errorf("retrySeconds(0) = %d, want 1", secs)
	}
	if secs := retrySeconds(1500 * time.Millisecond); secs != 2 {
		t.Errorf("retrySeconds(1.5s) = %d, want 2", secs)
	}
}

// A busy gate answers 503 with Retry-After 5, in the sign-in layout.
func TestLoginBusy(t *testing.T) {
	gate := login.NewGate(1024)
	hold, err := gate.Acquire(t.Context(), 1024, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	h, _ := siteAndPool(t, bundletest.Small(), Options{LoginGate: gate, LoginGateWait: 20 * time.Millisecond})
	rec, _ := postAs(t, h, "/login", nil, url.Values{"name": {"alice"}, "password": {testPassword}})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("busy POST /login = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") != "5" {
		t.Errorf("Retry-After = %q, want 5", rec.Header().Get("Retry-After"))
	}
	if body := bodyOf(t, rec); !strings.Contains(body, `name="password"`) {
		t.Error("the form is gone from the 503 page")
	}
}

// The site sets the charge of every password check from the stored hashes when
// it starts. On a board with an imported argon2id hash at 64 MiB, and a gate
// with room for 19 MiB and not for 64 MiB, every name answers 503: the unknown
// name, the gobb account, the bcrypt account and the 64 MiB account alike. A
// name whose check fitted would answer 200, and the status would tell a stranger
// that the 64 MiB account exists.
func TestLoginChargeFollowsStoredHashes(t *testing.T) {
	b := bundletest.Small()
	enc := base64.RawStdEncoding
	heavy := "$argon2id$v=19$m=65536,t=4,p=2$" + enc.EncodeToString([]byte("0123456789abcdef")) + "$" + enc.EncodeToString(make([]byte, 32))
	for i := range b.Users {
		if b.Users[i].Username == "carol" {
			b.Users[i].Password = bundle.Password{Scheme: "legacy", Hash: heavy}
		}
	}
	gate := login.NewGate(login.DefaultBudgetKiB)
	for range 4 {
		hold, err := gate.Acquire(t.Context(), password.HashMemoryKiB, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer hold()
	}
	h, _ := siteAndPool(t, b, Options{LoginGate: gate, LoginGateWait: 20 * time.Millisecond})
	for _, name := range []string{"nobody", "alice", "bob", "carol"} {
		rec, _ := postAs(t, h, "/login", nil, url.Values{"name": {name}, "password": {"wrong"}})
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("POST /login as %s = %d, want 503", name, rec.Code)
		}
	}
}

// web.New times the password checks at start and sets the sign-in floor, with
// one log line. A test that sets LoginFloor skips the timing.
func TestNewTimesTheFloor(t *testing.T) {
	pool, dataDir := boardtest.Load(t, bundletest.Small())
	floorLines := func(opt Options) []string {
		logs := captureLogs(t)
		opt.DataDir = dataDir
		if _, err := New(t.Context(), pool, opt); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, l := range logs.lines() {
			if strings.Contains(l, "login: sign-in floor") {
				out = append(out, l)
			}
		}
		return out
	}
	lines := floorLines(Options{})
	if len(lines) != 1 || !strings.Contains(lines[0], "level=INFO") {
		t.Fatalf("start-up floor lines = %q, want one INFO line", lines)
	}
	// The small board shares no email address, so K is 1. The floor is at least
	// the default and a whole number of 50 ms; T depends on this host.
	m := regexp.MustCompile(`floor_ms=(\d+) t_ms=\d+ k=1\b`).FindStringSubmatch(lines[0])
	if m == nil {
		t.Fatalf("the floor line %q has no floor_ms, t_ms and k=1", lines[0])
	}
	if ms, _ := strconv.Atoi(m[1]); ms < 400 || ms%50 != 0 {
		t.Errorf("floor_ms = %d, want at least 400 and a multiple of 50", ms)
	}
	if lines := floorLines(Options{LoginFloor: time.Millisecond}); len(lines) != 0 {
		t.Errorf("a set LoginFloor still timed the checks: %q", lines)
	}
}

// bodyCaps lists every POST route: its pattern in site.go, an address that
// matches it, and the cap of its body.
var bodyCaps = []struct {
	pattern string
	route   string
	limit   int
}{
	{"/login", "/login", 16 << 10},
	{"/logout", "/logout", 4 << 10},
	{"/scheme", "/scheme", 4 << 10},
	{"/read", "/read", 4 << 10},
	{"/f/{id}/read", "/f/1/read", 4 << 10},
	{"/f/{id}/post", "/f/2/post", maxPostingBody},
	{"/t/{id}/reply", "/t/1/reply", maxPostingBody},
	{"/p/{id}/edit", "/p/1/edit", maxPostingBody},
	{"/p/{id}/delete", "/p/1/delete", 4 << 10},
	{"/p/{id}/restore", "/p/1/restore", 4 << 10},
	{"/p/{id}/approve", "/p/1/approve", 4 << 10},
	{"/t/{id}/lock", "/t/1/lock", 4 << 10},
	{"/t/{id}/unlock", "/t/1/unlock", 4 << 10},
}

// Every POST route has a cap on its body. A body over it answers 413 in the
// layout, even without a csrf token. A body just under it reaches the csrf check.
func TestPostBodyCaps(t *testing.T) {
	h, _ := smallSite(t)
	for _, tc := range bodyCaps {
		t.Run(tc.route, func(t *testing.T) {
			over := url.Values{"junk": {strings.Repeat("x", tc.limit)}}
			rec := request(t, h, http.MethodPost, tc.route, nil, over)
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("body of %d bytes = %d, want 413", len(over.Encode()), rec.Code)
			}
			if body := bodyOf(t, rec); !strings.Contains(body, "Request too large") || !strings.Contains(body, "<html") {
				t.Errorf("the 413 is not the layout: %.200s", body)
			}
			under := url.Values{"junk": {strings.Repeat("x", tc.limit-100)}}
			rec = request(t, h, http.MethodPost, tc.route, nil, under)
			// Sign-out with no live session has nothing to protect: past the
			// cap, it answers 303 to the index instead of the 403.
			want := http.StatusForbidden
			if tc.route == "/logout" {
				want = http.StatusSeeOther
			}
			if rec.Code != want {
				t.Errorf("body of %d bytes = %d, want %d after the csrf check", len(under.Encode()), rec.Code, want)
			}
		})
	}
}

// The caps cover every POST route that exists: the test fails when a route is
// added with no row in bodyCaps, and when a row names a route that is gone.
func TestPostRoutesHaveCaps(t *testing.T) {
	src, err := os.ReadFile("site.go")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, m := range regexp.MustCompile(`Handle(?:Func)?\("POST ([^"]+)"`).FindAllStringSubmatch(string(src), -1) {
		found[m[1]] = true
	}
	listed := map[string]bool{}
	for _, c := range bodyCaps {
		listed[c.pattern] = true
	}
	if len(found) == 0 {
		t.Fatal("found no POST route in site.go")
	}
	for p := range found {
		if !listed[p] {
			t.Errorf("POST %s has no row in bodyCaps", p)
		}
	}
	for p := range listed {
		if !found[p] {
			t.Errorf("bodyCaps lists POST %s, which site.go does not route", p)
		}
	}
}
