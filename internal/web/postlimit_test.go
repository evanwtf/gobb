package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testClock is a settable time for the limits.
type testClock struct{ ns atomic.Int64 }

func newClock(t time.Time) *testClock { c := &testClock{}; c.ns.Store(t.UnixNano()); return c }
func (c *testClock) now() time.Time   { return time.Unix(0, c.ns.Load()).UTC() }
func (c *testClock) advance(d time.Duration) {
	c.ns.Add(int64(d))
}

// postFrom sends a form as the visitor with the cookies, from the address addr.
func postFrom(t *testing.T, h http.Handler, addr, target string, cookies []*http.Cookie, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = addr + ":4000"
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// tokenFrom reads the csrf token of the visitor with the cookies, from address addr.
func tokenFrom(t *testing.T, h http.Handler, addr string, cookies []*http.Cookie) (string, []*http.Cookie) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = addr + ":4000"
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return formToken(t, bodyOf(t, rec)), jar(cookies, rec)
}

const tooFast = "You are posting too fast. Wait a few minutes and try again."

// TestPostingIsRateLimitedPerMember covers A-I1, ruling 3: a POST to a posting
// form, a save or a preview, counts against 60 in 10 minutes for each member.
// Over the limit the answer is 429 with the form, the exact text kept, and the
// message. The count is for the member: another member is not held up. The
// window slides.
func TestPostingIsRateLimitedPerMember(t *testing.T) {
	clk := newClock(time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))
	h, pool := siteWithPool(t, postingBoard(), Options{Now: clk.now})
	a := signIn(t, h, "alice", "example-password")
	b := signIn(t, h, "bob", "example-password")
	token, _ := tokenFrom(t, h, "192.0.2.1", a)
	btoken, _ := tokenFrom(t, h, "192.0.2.1", b)
	body := "Line one\r\n\r\n  <b>two</b> & \"three\"\r\n"
	for i := 1; i <= 60; i++ {
		kind := "preview"
		if i%2 == 0 {
			kind = "save"
		}
		form := url.Values{"csrf": {token}, "body": {body}}
		if kind == "preview" {
			form.Set("preview", "1")
		} else {
			// A save of the same text inside a minute is the same post: no new row, still a 303.
			form.Set("body", body+strings.Repeat("x", i))
		}
		rec := postFrom(t, h, "192.0.2.1", "/t/1/reply", a, form)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("POST %d answered 429, want it to pass up to 60", i)
		}
	}
	rec := postFrom(t, h, "192.0.2.1", "/t/1/reply", a, url.Values{"csrf": {token}, "body": {body}, "preview": {"1"}})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the 61st POST answered %d, want 429", rec.Code)
	}
	page := bodyOf(t, rec)
	if !strings.Contains(page, tooFast) {
		t.Errorf("the page lacks the message:\n%s", page)
	}
	if got, ok := textareaOf(t, page, "body"); !ok || got != body {
		t.Errorf("the form holds %q (found %v), want the text that was sent %q", got, ok, body)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After header")
	}
	// A save is refused the same way and saves nothing.
	count := func() (n int) {
		if err := pool.QueryRow(t.Context(), `select count(*) from posts`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	save := postFrom(t, h, "192.0.2.1", "/t/1/reply", a, url.Values{"csrf": {token}, "body": {"one more"}})
	if save.Code != http.StatusTooManyRequests {
		t.Errorf("a save over the limit answered %d, want 429", save.Code)
	}
	if count() != before {
		t.Error("a save over the limit saved a post")
	}
	// Bob is not held up by alice.
	if rec := postFrom(t, h, "192.0.2.1", "/t/1/reply", b, url.Values{"csrf": {btoken}, "body": {"hi"}, "preview": {"1"}}); rec.Code != http.StatusOK {
		t.Errorf("another member's POST answered %d, want 200", rec.Code)
	}
	// The window slides: ten minutes later alice may post again.
	clk.advance(10*time.Minute + time.Second)
	if rec := postFrom(t, h, "192.0.2.1", "/t/1/reply", a, url.Values{"csrf": {token}, "body": {"hi"}, "preview": {"1"}}); rec.Code != http.StatusOK {
		t.Errorf("a POST after the window answered %d, want 200", rec.Code)
	}
}

// TestPostingIsRateLimitedPerGuestAddress: a guest address gets 20 POSTs in 10
// minutes. A refusal (here a forum where guests may not post) counts too, and the
// 21st is 429 with the text kept. Another address is not held up.
func TestPostingIsRateLimitedPerGuestAddress(t *testing.T) {
	clk := newClock(time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))
	h, _ := siteWithPool(t, guestBoard(false), Options{Now: clk.now})
	token, cookies := tokenFrom(t, h, "192.0.2.7", nil)
	otherToken, otherCookies := tokenFrom(t, h, "192.0.2.8", nil)
	form := func(tok string) url.Values {
		return url.Values{"csrf": {tok}, "title": {"T"}, "body": {"B <i>text</i>"}, "guest_name": {"Visitor 3"}}
	}
	for i := 1; i <= 20; i++ {
		rec := postFrom(t, h, "192.0.2.7", "/f/3/post", cookies, form(token))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("POST %d answered %d, want 403 (guests may not start a topic in forum 3)", i, rec.Code)
		}
	}
	rec := postFrom(t, h, "192.0.2.7", "/f/3/post", cookies, form(token))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the 21st POST answered %d, want 429", rec.Code)
	}
	page := bodyOf(t, rec)
	if !strings.Contains(page, tooFast) {
		t.Errorf("the page lacks the message:\n%s", page)
	}
	if got, _ := textareaOf(t, page, "body"); got != "B <i>text</i>" {
		t.Errorf("the form holds %q", got)
	}
	if got, _ := inputValue(page, "title"); got != "T" {
		t.Errorf("the title field holds %q", got)
	}
	if got, _ := inputValue(page, "guest_name"); got != "Visitor 3" {
		t.Errorf("the name field holds %q", got)
	}
	if rec := postFrom(t, h, "192.0.2.8", "/f/3/post", otherCookies, form(otherToken)); rec.Code != http.StatusForbidden {
		t.Errorf("another address answered %d, want the usual 403", rec.Code)
	}
	// The IPv6 /64 is one client.
	tok6, c6 := tokenFrom(t, h, "[2001:db8::1]", nil)
	for i := 1; i <= 20; i++ {
		postFrom(t, h, "[2001:db8::1]", "/f/3/post", c6, form(tok6))
	}
	if rec := postFrom(t, h, "[2001:db8::2]", "/f/3/post", c6, form(tok6)); rec.Code != http.StatusTooManyRequests {
		t.Errorf("a neighbor in the same /64 answered %d, want 429", rec.Code)
	}
}

// TestGuestNameChecksAreLimited covers B-I2: every guest-name check, from a
// preview or a save, counts against 10 in 10 minutes for each address. The 11th
// is 429 with the form and the text kept, and the answer does not say whether the
// name belongs to a member.
func TestGuestNameChecksAreLimited(t *testing.T) {
	clk := newClock(time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))
	// Raise the posting limit, so that the name limit is the one that answers.
	h, pool := siteWithPool(t, guestBoard(false), Options{Now: clk.now, GuestPosts: 1000})
	token, cookies := tokenFrom(t, h, "192.0.2.7", nil)
	send := func(name string, preview bool) *httptest.ResponseRecorder {
		form := url.Values{"csrf": {token}, "title": {"Help"}, "body": {"Locked out."}, "guest_name": {name}}
		if preview {
			form.Set("preview", "1")
		}
		return postFrom(t, h, "192.0.2.7", "/f/2/post", cookies, form)
	}
	// Nine previews, then a save: ten checks. A member's name answers 400 and a free
	// name 200 for as long as the limit is not reached.
	for i := 0; i < 9; i++ {
		want, name := http.StatusOK, "free-name-"+string(rune('a'+i))
		if i%2 == 0 {
			want, name = http.StatusBadRequest, "Alice"
		}
		if rec := send(name, true); rec.Code != want {
			t.Fatalf("check %d (%q) answered %d, want %d", i+1, name, rec.Code, want)
		}
	}
	if rec := send("Visitor 77", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("the 10th check, a save, answered %d, want 303", rec.Code)
	}
	// The 11th check, for a name that exists, and a 12th, for one that does not.
	taken, free := send("Alice", true), send("nobody-by-that-name", true)
	for name, rec := range map[string]*httptest.ResponseRecorder{"member": taken, "free": free} {
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("a check of a %s name over the limit answered %d, want 429", name, rec.Code)
		}
		if rec.Header().Get("Retry-After") == "" {
			t.Errorf("%s: no Retry-After header", name)
		}
	}
	tp, fp := bodyOf(t, taken), bodyOf(t, free)
	// M3: the guest reads the text of the posting limit, not a text about names.
	for name, body := range map[string]string{"member": tp, "free": fp} {
		if !strings.Contains(body, tooFast) {
			t.Errorf("%s: the answer over the name-check limit does not say %q", name, tooFast)
		}
		if strings.Contains(body, "checking names") {
			t.Errorf("%s: the answer over the name-check limit mentions names being checked", name)
		}
	}
	if strings.Contains(tp, "belongs to a member") || strings.Contains(fp, "belongs to a member") {
		t.Error("an answer over the limit says whether the name belongs to a member")
	}
	if got, _ := textareaOf(t, tp, "body"); got != "Locked out." {
		t.Errorf("the form holds %q", got)
	}
	if got, _ := inputValue(tp, "guest_name"); got != "Alice" {
		t.Errorf("the name field holds %q", got)
	}
	// The two answers differ only in the name they echo.
	if strings.ReplaceAll(withoutCSRF(tp), "Alice", "N") != strings.ReplaceAll(withoutCSRF(fp), "nobody-by-that-name", "N") {
		t.Error("the answers for a member's name and a free name differ")
	}
	// A save over the limit saves nothing.
	var posts int
	if err := pool.QueryRow(t.Context(), `select count(*) from posts where author_name like 'Visitor%'`).Scan(&posts); err != nil {
		t.Fatal(err)
	}
	if rec := send("Visitor 78", false); rec.Code != http.StatusTooManyRequests {
		t.Errorf("a save over the limit answered %d, want 429", rec.Code)
	}
	var after int
	if err := pool.QueryRow(t.Context(), `select count(*) from posts where author_name like 'Visitor%'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != posts {
		t.Errorf("a save over the limit saved a post")
	}
	// Another address has its own count; the window slides.
	tok2, c2 := tokenFrom(t, h, "192.0.2.9", nil)
	if rec := postFrom(t, h, "192.0.2.9", "/f/2/post", c2, url.Values{"csrf": {tok2}, "title": {"x"}, "body": {"y"}, "guest_name": {"Alice"}, "preview": {"1"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("another address answered %d, want 400", rec.Code)
	}
	clk.advance(10*time.Minute + time.Second)
	if rec := send("Alice", true); rec.Code != http.StatusBadRequest {
		t.Errorf("a check after the window answered %d, want 400", rec.Code)
	}
}
