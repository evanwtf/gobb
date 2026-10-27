package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/metrics"
	"github.com/evanwtf/gobb/internal/password"
)

const (
	resetPublic   = "https://board.example.org/forums/"
	resetFloor    = 50 * time.Millisecond
	resetPassword = "NewPassword2026"
)

// testMailOptions returns site options with email on: the public address of
// RFC 2606, a floor of 1 ms and a Wake that does nothing.
func testMailOptions() Options {
	return Options{
		Mail:       &MailOptions{PublicURL: resetPublic, Wake: func() {}, PerHour: 100, PerDay: 100, Sender: &fakeSender{}},
		ResetFloor: time.Millisecond,
	}
}

// resetBoard is the small board with three more members: erin is inactive,
// frank is banned in resetEnv, and gina lacks account.password.
func resetBoard() *bundletest.Board {
	b := bundletest.Small()
	carol := b.Users[2]
	for _, n := range []struct {
		id    int64
		name  string
		state string
	}{{5, "erin", "inactive"}, {6, "frank", "active"}, {7, "gina", "active"}} {
		u := carol
		u.ID, u.Username, u.Email, u.State = n.id, n.name, n.name+"@example.org", n.state
		b.Users = append(b.Users, u)
		b.GroupMembers = append(b.GroupMembers, bundle.GroupMember{GroupID: 2, UserID: n.id})
	}
	return b
}

// resetEnv is a site with email on, its pool, the site value and the count of
// Wake calls.
type resetEnv struct {
	h     http.Handler
	pool  *pgxpool.Pool
	site  *site
	wakes *atomic.Int32
}

func newResetEnv(t *testing.T, opt Options) *resetEnv {
	t.Helper()
	return newResetEnvFor(t, opt, resetBoard())
}

// newResetEnvFor is newResetEnv for the board b, which must hold the users of
// resetBoard.
func newResetEnvFor(t *testing.T, opt Options, b *bundletest.Board) *resetEnv {
	t.Helper()
	e := &resetEnv{wakes: new(atomic.Int32)}
	// The limits of the board are high unless the test sets its own.
	perHour, perDay := 100, 100
	if opt.Mail != nil {
		perHour, perDay = opt.Mail.PerHour, opt.Mail.PerDay
	}
	opt.Mail = &MailOptions{PublicURL: resetPublic, Wake: func() { e.wakes.Add(1) }, PerHour: perHour, PerDay: perDay}
	if opt.ResetFloor == 0 {
		opt.ResetFloor = resetFloor
	}
	opt.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}
	onNew = func(s *site) { e.site = s }
	t.Cleanup(func() { onNew = nil })
	e.h, e.pool = siteWithPool(t, b, opt)
	onNew = nil
	ctx := context.Background()
	if err := board.BanUser(ctx, e.pool, 6, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	exec(t, e.pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (7, 0, 'account.password', 2)`)
	// alice is an administrator in the small board, and an administrator gets no
	// reset email, and an owner always has the right. The tests of the reset need
	// her as an ordinary member.
	exec(t, e.pool, `update users set is_owner = false where id = 1`)
	exec(t, e.pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (1, 0, 'admin', 2)`)
	return e
}

// client is a visitor who comes through the trusted proxy from addr.
type client struct {
	t       *testing.T
	e       *resetEnv
	addr    string
	cookies []*http.Cookie
	token   string
}

// guest returns a guest with a csrf cookie, who comes from addr. Each address
// of the tests is in a /64 of its own.
func (e *resetEnv) guest(t *testing.T, addr string) *client {
	t.Helper()
	c := &client{t: t, e: e, addr: addr}
	rec := c.send(http.MethodGet, "/reset", nil, nil)
	c.cookies = jar(nil, rec)
	m := csrfField.FindStringSubmatch(bodyOf(t, rec))
	if m == nil {
		t.Fatalf("GET /reset: no csrf field (status %d)", rec.Code)
	}
	c.token = m[1]
	return c
}

// send sends one request as c. A nil form sends no body.
func (c *client) send(method, target string, form url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	c.t.Helper()
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, target, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	r.RemoteAddr = "10.0.0.1:4000"
	r.Header.Set("X-Forwarded-For", c.addr)
	for k, v := range hdr {
		if k == "Host" {
			r.Host = v
			continue
		}
		r.Header.Set(k, v)
	}
	for _, ck := range c.cookies {
		r.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	c.e.h.ServeHTTP(rec, r)
	return rec
}

// post sends form with the csrf token of c.
func (c *client) post(target string, form url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	c.t.Helper()
	f := cloneForm(form)
	f.Set("csrf", c.token)
	return c.send(http.MethodPost, target, f, hdr)
}

// ask posts a reset request for name and returns the answer.
func (c *client) ask(name string) *httptest.ResponseRecorder {
	c.t.Helper()
	return c.post("/reset", url.Values{"name": {name}}, nil)
}

// mailRow is one row of the email queue.
type mailRow struct {
	To, Subject, Body string
	UserID            int64
}

func (e *resetEnv) queue(t *testing.T) []mailRow {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `select to_addr, subject, body, coalesce(user_id, 0) from email_queue order by id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []mailRow
	for rows.Next() {
		var m mailRow
		if err := rows.Scan(&m.To, &m.Subject, &m.Body, &m.UserID); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

var linkRE = regexp.MustCompile(`https://board\.example\.org/forums/reset/([A-Za-z0-9_-]{43})`)

// tokenOf returns the token in the link of an email body.
func tokenOf(t *testing.T, body string) string {
	t.Helper()
	m := linkRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("the email body has no reset link:\n%s", body)
	}
	return m[1]
}

// requestLink asks for a reset of name and returns the token of the newest email.
func (c *client) requestLink(name string) string {
	c.t.Helper()
	before := len(c.e.queue(c.t))
	if rec := c.ask(name); rec.Code != http.StatusSeeOther {
		c.t.Fatalf("POST /reset for %s = %d", name, rec.Code)
	}
	q := c.e.queue(c.t)
	if len(q) != before+1 {
		c.t.Fatalf("the queue holds %d emails, want %d", len(q), before+1)
	}
	return tokenOf(c.t, q[len(q)-1].Body)
}

// locate returns the Location header of rec.
func locate(rec *httptest.ResponseRecorder) string { return rec.Header().Get("Location") }

func TestResetRoutesOffWithoutMail(t *testing.T) {
	h, _ := smallSite(t)
	for _, target := range []string{"/reset", "/reset/x", "/reset/" + strings.Repeat("a", 43)} {
		if rec := get(t, h, target); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
	}
	if body := bodyOf(t, get(t, h, "/login")); strings.Contains(body, "Forgot your password?") {
		t.Error("the sign-in page links to the reset form with email off")
	}
	if body := bodyOf(t, get(t, h, "/login?reset=1")); strings.Contains(body, "Your password is changed") {
		t.Error("the sign-in page shows the reset notice with email off")
	}
}

func TestResetSignInLink(t *testing.T) {
	for _, base := range []string{"/", "/forums/"} {
		h, _ := newTestSite(t, bundletest.Small(), func() Options { o := testMailOptions(); o.BasePath = base; return o }())
		body := bodyOf(t, get(t, h, base+"login"))
		want := `<p class="hint"><a href="` + base + `reset">Forgot your password?</a></p>`
		if !strings.Contains(body, want) {
			t.Errorf("base %s: the sign-in page lacks %s", base, want)
		}
		if strings.Contains(body, "Your password is changed") {
			t.Errorf("base %s: the notice shows without ?reset=1", base)
		}
		body = bodyOf(t, get(t, h, base+"login?reset=1"))
		notice := `<p class="notice-inline" role="status">Your password is changed. Sign in with the new password.</p>`
		if !strings.Contains(body, notice) {
			t.Errorf("base %s: /login?reset=1 lacks %s", base, notice)
		}
	}
}

func TestResetFormPages(t *testing.T) {
	e := newResetEnv(t, Options{})
	rec := e.guest(t, "2001:db8:1::1").send(http.MethodGet, "/reset", nil, nil)
	body := bodyOf(t, rec)
	for _, want := range []string{
		"Reset your password", "Username or email address", `name="name"`, `name="csrf"`,
		"We email a link to the address of the account. The link works for 1 hour.", "Email me a link",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /reset lacks %q", want)
		}
	}
	if rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(body, `<meta name="robots" content="noindex">`) {
		t.Errorf("GET /reset: Cache-Control %q, noindex %v", rec.Header().Get("Cache-Control"), strings.Contains(body, "noindex"))
	}
	sent := bodyOf(t, e.guest(t, "2001:db8:1::1").send(http.MethodGet, "/reset?sent=1", nil, nil))
	for _, want := range []string{
		"Check your email",
		"If an account matches, we sent a link to its email address. The link works for 1 hour. If no email comes, look in your spam folder.",
	} {
		if !strings.Contains(sent, want) {
			t.Errorf("GET /reset?sent=1 lacks %q", want)
		}
	}
	if strings.Contains(sent, `name="name"`) {
		t.Error("the sent notice still shows the form")
	}
}

func TestResetEmptyName(t *testing.T) {
	e := newResetEnv(t, Options{})
	c := e.guest(t, "2001:db8:2::1")
	for i := range 8 {
		rec := c.ask("   ")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("empty name %d = %d, want 400", i+1, rec.Code)
		}
		if body := bodyOf(t, rec); !strings.Contains(body, `<p class="field-error" id="reset-name-error" role="alert">Enter your username or email address.</p>`) {
			t.Fatalf("the 400 lacks the message beside the field:\n%s", body)
		}
	}
	// None of them counted against the address limit of 5 an hour.
	for i := range 5 {
		if rec := c.ask("nobody"); rec.Code != http.StatusSeeOther {
			t.Fatalf("request %d after the empty names = %d, want 303", i+1, rec.Code)
		}
	}
	if n := len(e.queue(t)); n != 0 {
		t.Errorf("the queue holds %d emails, want 0", n)
	}
}

// Review Focus 4: every kind of name gets the same answer, headers and floor.
func TestResetRequestSameAnswer(t *testing.T) {
	e := newResetEnv(t, Options{})
	// Alice has used two of her three emails of the day; the next one is the last.
	for range 2 {
		e.site.resets.account.Add("1", time.Now())
	}
	names := []string{"alice", "nobody", "erin", "frank", "gina", "alice"}
	var first http.Header
	for i, name := range names {
		c := e.guest(t, "2001:db8:"+strconv.Itoa(0x10+i)+"::1")
		start := time.Now()
		rec := c.ask(name)
		took := time.Since(start)
		if rec.Code != http.StatusSeeOther || locate(rec) != "/reset?sent=1" {
			t.Errorf("request %d (%s) = %d to %q, want 303 to /reset?sent=1", i+1, name, rec.Code, locate(rec))
		}
		if took < resetFloor {
			t.Errorf("request %d (%s) took %v, under the floor of %v", i+1, name, took, resetFloor)
		}
		if i == 0 {
			first = rec.Header().Clone()
			continue
		}
		if !maps.EqualFunc(first, rec.Header(), func(a, b []string) bool { return strings.Join(a, "|") == strings.Join(b, "|") }) {
			t.Errorf("request %d (%s) has other headers:\n%v\nwant\n%v", i+1, name, rec.Header(), first)
		}
	}
	q := e.queue(t)
	if len(q) != 1 || q[0].To != "alice@example.com" || q[0].UserID != 1 {
		t.Errorf("the queue holds %+v, want one email for alice", q)
	}
	if n := e.wakes.Load(); n != 1 {
		t.Errorf("Wake ran %d times, want 1", n)
	}
}

func TestResetEmailText(t *testing.T) {
	e := newResetEnv(t, Options{})
	token := e.guest(t, "2001:db8:3::1").requestLink("alice@example.com")
	m := e.queue(t)[0]
	if m.Subject != "Reset your password on Example Board" {
		t.Errorf("subject = %q", m.Subject)
	}
	want := "Someone asked to reset the password of the account alice on Example Board.\n\n" +
		"To choose a new password, open this link within 1 hour:\n\n" +
		resetPublic + "reset/" + token + "\n\n" +
		"The link works once. If you did not ask for this, ignore this email.\n" +
		"Your password stays the same.\n"
	if m.Body != want {
		t.Errorf("body =\n%q\nwant\n%q", m.Body, want)
	}
}

func TestResetMailFoldsControlCharacters(t *testing.T) {
	subject, body := resetMailFor("Odd\r\nBcc: x\x00\x7f\u0085 Board", "https://board.example.org/forums/")("alice", strings.Repeat("a", 43))
	if want := "Reset your password on Odd  Bcc: x    Board"; subject != want {
		t.Errorf("subject = %q, want %q", subject, want)
	}
	for _, s := range []string{subject, body} {
		for _, r := range strings.ReplaceAll(s, "\n", "") {
			if r < 0x20 || (r >= 0x7f && r < 0xa0) {
				t.Errorf("control character %U in %q", r, s)
			}
		}
	}
	if !strings.Contains(body, "https://board.example.org/forums/reset/"+strings.Repeat("a", 43)+"\n") {
		t.Errorf("body lacks the link:\n%s", body)
	}
	// A public address without the trailing slash still gives one slash.
	_, body = resetMailFor("B", "https://board.example.org/forums")("alice", strings.Repeat("a", 43))
	if !strings.Contains(body, "https://board.example.org/forums/reset/") {
		t.Errorf("body lacks the link:\n%s", body)
	}
}

// A board name with a line break must not make the queue refuse the email of an
// existing account: that would show which names exist.
func TestResetBoardNameWithLineBreak(t *testing.T) {
	e := newResetEnv(t, Options{})
	exec(t, e.pool, `update settings set value = to_jsonb(E'Odd\r\nBoard\x01'::text) where key = 'board_name'`)
	e.guest(t, "2001:db8:4::1").requestLink("alice")
	if s := e.queue(t)[0].Subject; s != "Reset your password on Odd  Board " {
		t.Errorf("subject = %q", s)
	}
	// An unknown name gets the same answer.
	c := e.guest(t, "2001:db8:4::2")
	if rec := c.ask("nobody"); rec.Code != http.StatusSeeOther || locate(rec) != "/reset?sent=1" {
		t.Errorf("unknown name = %d to %q", rec.Code, locate(rec))
	}
}

// Review Focus 1: the link comes from the setting, not from the request.
func TestResetLinkIgnoresHost(t *testing.T) {
	e := newResetEnv(t, Options{})
	c := e.guest(t, "2001:db8:5::1")
	rec := c.post("/reset", url.Values{"name": {"alice"}}, map[string]string{
		"Host": "evil.example", "X-Forwarded-Host": "evil.example", "X-Forwarded-Proto": "http",
		"Forwarded": "host=evil.example",
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /reset = %d, want 303", rec.Code)
	}
	q := e.queue(t)
	if len(q) != 1 {
		t.Fatalf("the queue holds %d emails, want 1", len(q))
	}
	if !strings.Contains(q[0].Body, resetPublic+"reset/") || strings.Contains(q[0].Body, "evil") {
		t.Errorf("the body does not use the public address only:\n%s", q[0].Body)
	}
}

// Review Focus 2: a mail scanner that opens the link does not use it up.
func TestResetGetDoesNotConsume(t *testing.T) {
	e := newResetEnv(t, Options{})
	c := e.guest(t, "2001:db8:6::1")
	token := c.requestLink("alice")
	target := "/reset/" + token
	for i := range 3 {
		if rec := c.send(http.MethodGet, target, nil, nil); rec.Code != http.StatusOK {
			t.Fatalf("GET %d = %d, want 200", i+1, rec.Code)
		}
	}
	// A recorder keeps the body of a HEAD answer; a server drops it.
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	resp, err := http.Head(srv.URL + target)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > 0 {
		t.Errorf("HEAD = %d with %d bytes, want 200 and none", resp.StatusCode, resp.ContentLength)
	}
	var links int
	if err := e.pool.QueryRow(context.Background(), `select count(*) from password_resets`).Scan(&links); err != nil || links != 1 {
		t.Errorf("%d links after the GETs (%v), want 1", links, err)
	}
	good := url.Values{"password": {resetPassword}, "again": {resetPassword}}
	if rec := c.post(target, good, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST = %d, want 303\n%s", rec.Code, bodyOf(t, rec))
	}
	rec := c.post(target, good, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("second POST = %d, want 404", rec.Code)
	}
	if rec := c.send(http.MethodGet, target, nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("GET after the reset = %d, want 404", rec.Code)
	}
}

var absoluteURL = regexp.MustCompile(`https?://[^"'\s<>]*`)

func TestResetLinkPage(t *testing.T) {
	e := newResetEnv(t, Options{})
	c := e.guest(t, "2001:db8:7::1")
	token := c.requestLink("alice")
	rec := c.send(http.MethodGet, "/reset/"+token, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d", rec.Code)
	}
	h := rec.Header()
	if h.Get("Referrer-Policy") != "no-referrer" || h.Get("Cache-Control") != "no-store" {
		t.Errorf("Referrer-Policy %q, Cache-Control %q", h.Get("Referrer-Policy"), h.Get("Cache-Control"))
	}
	body := bodyOf(t, rec)
	for _, want := range []string{
		`<meta name="robots" content="noindex">`, "Choose a new password",
		"For the account <bdi>alice</bdi>.",
		`<input type="hidden" name="username" value="alice" autocomplete="username">`,
		"New password", `name="password"`, `autocomplete="new-password"`, "Type it again", `name="again"`,
		"Save the password", `action="/reset/` + token + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	for _, u := range absoluteURL.FindAllString(body, -1) {
		if !strings.HasPrefix(u, "http://example.com") {
			t.Errorf("the page holds the address %q", u)
		}
	}
	for _, bad := range []string{"<style", "style=", "<script>"} {
		if strings.Contains(body, bad) {
			t.Errorf("the page holds %q", bad)
		}
	}
	// The scheme picker and the sign-in link do not carry the token.
	if strings.Count(body, token) != 1 {
		t.Errorf("the token is on the page %d times, want 1 (the form action)", strings.Count(body, token))
	}
}

func TestResetDeadLink(t *testing.T) {
	e := newResetEnv(t, Options{})
	c := e.guest(t, "2001:db8:8::1")
	dead := strings.Repeat("A", 43)
	for _, tc := range []struct {
		method string
		form   url.Values
	}{{http.MethodGet, nil}, {http.MethodPost, url.Values{"password": {resetPassword}, "again": {resetPassword}}}} {
		var rec *httptest.ResponseRecorder
		if tc.method == http.MethodGet {
			rec = c.send(tc.method, "/reset/"+dead, nil, nil)
		} else {
			rec = c.post("/reset/"+dead, tc.form, nil)
		}
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s a dead link = %d, want 404", tc.method, rec.Code)
		}
		body := bodyOf(t, rec)
		for _, want := range []string{"Link not valid", "This link has expired or has been used.", `href="/reset"`, "Ask for a new link"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: the dead-link page lacks %q", tc.method, want)
			}
		}
		if strings.Contains(body, dead) {
			t.Errorf("%s: the dead-link page echoes the token", tc.method)
		}
		if rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: headers %v", tc.method, rec.Header())
		}
	}
	// A token of the wrong shape gets the same page.
	if rec := c.send(http.MethodGet, "/reset/short", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("GET /reset/short = %d, want 404", rec.Code)
	}
}

// signedIn signs name in and returns the cookies.
func (e *resetEnv) signedIn(t *testing.T, name, password string) []*http.Cookie {
	t.Helper()
	return signIn(t, e.h, name, password)
}

// resetFlow runs the steps of a reset of alice and checks them.
func resetFlow(t *testing.T, e *resetEnv) {
	t.Helper()
	sessionA := e.signedIn(t, "alice", testPassword)
	c := e.guest(t, "2001:db8:9::1")
	token := c.requestLink("alice")
	// Session A opens the link and sets the password.
	c.cookies = sessionA
	page := c.send(http.MethodGet, "/reset/"+token, nil, nil)
	if page.Code != http.StatusOK {
		t.Fatalf("GET link = %d", page.Code)
	}
	c.cookies = jar(sessionA, page)
	c.token = formToken(t, bodyOf(t, page))
	rec := c.post("/reset/"+token, url.Values{"password": {resetPassword}, "again": {resetPassword}}, nil)
	if rec.Code != http.StatusSeeOther || locate(rec) != "/login?reset=1" {
		t.Fatalf("POST = %d to %q, want 303 to /login?reset=1\n%s", rec.Code, locate(rec), bodyOf(t, rec))
	}
	if !clearsSession(rec) {
		t.Error("the answer does not clear the session cookie of the visitor")
	}
	// Session A is dead.
	if acct := getAs(t, e.h, "/account", sessionA); acct.Code != http.StatusFound || !strings.HasPrefix(locate(acct), "/login") {
		t.Errorf("GET /account with session A = %d to %q, want a redirect to sign in", acct.Code, locate(acct))
	}
	// The old password fails; the new one works.
	if rec, _ := postAs(t, e.h, "/login", nil, url.Values{"name": {"alice"}, "password": {testPassword}}); rec.Code == http.StatusSeeOther {
		t.Error("the old password still signs in")
	}
	e.signedIn(t, "alice", resetPassword)
	if body := bodyOf(t, get(t, e.h, "/login?reset=1")); !strings.Contains(body, "Your password is changed.") {
		t.Error("the sign-in page lacks the notice")
	}
}

func TestResetFlow(t *testing.T) {
	resetFlow(t, newResetEnv(t, Options{}))
}

// clearsSession reports whether rec tells the browser to drop the session cookie.
func clearsSession(rec *httptest.ResponseRecorder) bool {
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "gobb_session" && ck.MaxAge < 0 {
			return true
		}
	}
	return false
}

func TestResetClearsOnlyOwnSession(t *testing.T) {
	t.Run("another member's session stays", func(t *testing.T) {
		e := newResetEnv(t, Options{})
		sessionB := e.signedIn(t, "bob", testPassword)
		c := e.guest(t, "2001:db8:a1::1")
		token := c.requestLink("alice")
		c.cookies = sessionB
		page := c.send(http.MethodGet, "/reset/"+token, nil, nil)
		if page.Code != http.StatusOK {
			t.Fatalf("GET link = %d", page.Code)
		}
		c.cookies = jar(sessionB, page)
		c.token = formToken(t, bodyOf(t, page))
		rec := c.post("/reset/"+token, url.Values{"password": {resetPassword}, "again": {resetPassword}}, nil)
		if rec.Code != http.StatusSeeOther || locate(rec) != "/login?reset=1" {
			t.Fatalf("POST = %d to %q, want 303 to /login?reset=1", rec.Code, locate(rec))
		}
		if clearsSession(rec) {
			t.Error("the answer clears the session cookie of bob, who is not alice")
		}
		if acct := getAs(t, e.h, "/account", sessionB); acct.Code != http.StatusOK {
			t.Errorf("GET /account as bob = %d, want 200: bob is still signed in", acct.Code)
		}
		e.signedIn(t, "alice", resetPassword)
	})
	t.Run("the member's own session goes", func(t *testing.T) {
		e := newResetEnv(t, Options{})
		resetFlow(t, e) // signs in alice, completes her link, and checks that the cookie is cleared
	})
}

func TestResetPasswordsDiffer(t *testing.T) {
	e := newResetEnv(t, Options{})
	c := e.guest(t, "2001:db8:a::1")
	token := c.requestLink("alice")
	holds := 0
	beforeHold = func() { holds++ }
	t.Cleanup(func() { beforeHold = nil })
	rec := c.post("/reset/"+token, url.Values{"password": {resetPassword}, "again": {resetPassword + "x"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST = %d, want 400", rec.Code)
	}
	body := bodyOf(t, rec)
	if !strings.Contains(body, `role="alert">The two passwords differ.</p>`) || strings.Contains(body, resetPassword) {
		t.Errorf("the 400 lacks the message, or echoes the password:\n%s", body)
	}
	if holds != 0 {
		t.Errorf("the hash gate was taken %d times, want 0", holds)
	}
	if rec := c.send(http.MethodGet, "/reset/"+token, nil, nil); rec.Code != http.StatusOK {
		t.Errorf("the link after a refusal: GET = %d, want 200", rec.Code)
	}
}

func TestResetWeakPassword(t *testing.T) {
	e := newResetEnv(t, Options{})
	c := e.guest(t, "2001:db8:b::1")
	token := c.requestLink("alice")
	holds := 0
	beforeHold = func() { holds++ }
	t.Cleanup(func() { beforeHold = nil })
	for _, tc := range []struct{ pw, msg string }{
		{"short", "Use at least 10 characters."},
		{strings.Repeat("x", 1025), "Use at most 1,024 bytes."},
	} {
		rec := c.post("/reset/"+token, url.Values{"password": {tc.pw}, "again": {tc.pw}}, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST of %d bytes = %d, want 400", len(tc.pw), rec.Code)
		}
		body := bodyOf(t, rec)
		if !strings.Contains(body, `role="alert">`+tc.msg+`</p>`) || !strings.Contains(body, `id="reset-password-error"`) {
			t.Errorf("the 400 for %d bytes lacks %q beside the field:\n%s", len(tc.pw), tc.msg, body)
		}
	}
	if holds != 0 {
		t.Errorf("the hash gate was taken %d times, want 0", holds)
	}
	if rec := c.send(http.MethodGet, "/reset/"+token, nil, nil); rec.Code != http.StatusOK {
		t.Errorf("the link after a refusal: GET = %d, want 200", rec.Code)
	}
	// The same link still works.
	if rec := c.post("/reset/"+token, url.Values{"password": {resetPassword}, "again": {resetPassword}}, nil); rec.Code != http.StatusSeeOther {
		t.Errorf("POST of a good password after refusals = %d, want 303", rec.Code)
	}
}

func TestResetFullGate(t *testing.T) {
	gate := login.NewGate(password.HashMemoryKiB)
	e := newResetEnv(t, Options{LoginGate: gate, LoginGateWait: 20 * time.Millisecond})
	c := e.guest(t, "2001:db8:c::1")
	token := c.requestLink("alice")
	var held func()
	beforeHold = func() {
		var err error
		if held, err = gate.Acquire(t.Context(), password.HashMemoryKiB, time.Second); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeHold = nil })
	rec := c.post("/reset/"+token, url.Values{"password": {resetPassword}, "again": {resetPassword}}, nil)
	if held != nil {
		held()
	}
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" {
		t.Fatalf("POST = %d, Retry-After %q; want 503 and 5\n%s", rec.Code, rec.Header().Get("Retry-After"), bodyOf(t, rec))
	}
	if body := bodyOf(t, rec); strings.Contains(body, resetPassword) {
		t.Error("the 503 echoes the password")
	}
	beforeHold = nil
	if rec := c.send(http.MethodGet, "/reset/"+token, nil, nil); rec.Code != http.StatusOK {
		t.Errorf("the link after a busy answer: GET = %d, want 200", rec.Code)
	}
	if rec := c.post("/reset/"+token, url.Values{"password": {resetPassword}, "again": {resetPassword}}, nil); rec.Code != http.StatusSeeOther {
		t.Errorf("POST with the gate free = %d, want 303", rec.Code)
	}
}

func TestResetLimits(t *testing.T) {
	t.Run("address", func(t *testing.T) {
		e := newResetEnv(t, Options{})
		c := e.guest(t, "2001:db8:d::1")
		for i := range 5 {
			if rec := c.ask("nobody"); rec.Code != http.StatusSeeOther {
				t.Fatalf("request %d = %d, want 303", i+1, rec.Code)
			}
		}
		rec := c.ask("nobody")
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("6th request = %d, want 429", rec.Code)
		}
		secs, err := strconv.Atoi(rec.Header().Get("Retry-After"))
		if err != nil || secs < 1 || secs > 3600 {
			t.Errorf("Retry-After = %q (%v)", rec.Header().Get("Retry-After"), err)
		}
		if body := bodyOf(t, rec); !regexp.MustCompile(`Too many requests from your address\. Try again in \d+ minutes\.`).MatchString(body) {
			t.Errorf("the 429 lacks the message:\n%s", body)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
		}
	})
	t.Run("board", func(t *testing.T) {
		// The board allows 5 emails an hour and 8 a day. Four members can get
		// 3 emails each, which is more than either limit.
		b := resetBoard()
		for i := range 4 {
			u := b.Users[2]
			u.ID, u.Username, u.Email = int64(100+i), "member"+strconv.Itoa(i), "member"+strconv.Itoa(i)+"@example.org"
			b.Users = append(b.Users, u)
			b.GroupMembers = append(b.GroupMembers, bundle.GroupMember{GroupID: 2, UserID: u.ID})
		}
		var clock atomic.Int64
		clock.Store(time.Now().UnixNano())
		now := func() time.Time { return time.Unix(0, clock.Load()) }
		e := newResetEnvFor(t, Options{Now: now, Mail: &MailOptions{PerHour: 5, PerDay: 8}}, b)
		logs := captureAllLogs(t)
		hour0, day0 := metrics.ResetSkipped.Value("board_hour"), metrics.ResetSkipped.Value("board_day")
		addr := 0
		ask := func(name string) {
			t.Helper()
			addr++
			c := e.guest(t, "2001:db8:e"+strconv.Itoa(addr)+"::1")
			if rec := c.ask(name); rec.Code != http.StatusSeeOther || locate(rec) != "/reset?sent=1" {
				t.Fatalf("request for %s = %d to %q, want 303 to /reset?sent=1", name, rec.Code, locate(rec))
			}
		}
		want := func(queued, perHour, perDay int, when string) {
			t.Helper()
			if n := len(e.queue(t)); n != queued {
				t.Errorf("%s: the queue holds %d emails, want %d", when, n, queued)
			}
			if n := strings.Count(logs.String(), "reset email skipped: board limit"); n != perHour+perDay {
				t.Errorf("%s: %d log lines for the board limit, want %d:\n%s", when, n, perHour+perDay, logs.String())
			}
			if h, d := strings.Count(logs.String(), "per=hour"), strings.Count(logs.String(), "per=day"); h != perHour || d != perDay {
				t.Errorf("%s: per=hour %d times and per=day %d times, want %d and %d", when, h, d, perHour, perDay)
			}
			if h, d := metrics.ResetSkipped.Value("board_hour")-hour0, metrics.ResetSkipped.Value("board_day")-day0; h != uint64(perHour) || d != uint64(perDay) {
				t.Errorf("%s: the skip metric counted board_hour %d and board_day %d, want %d and %d", when, h, d, perHour, perDay)
			}
		}
		for _, name := range []string{"member0", "member0", "member0", "member1", "member1"} {
			ask(name)
		}
		want(5, 0, 0, "5 emails in the first hour")
		// The 6th email in the hour is a quiet skip: the same 303, no row, no Wake.
		wakes := e.wakes.Load()
		ask("member1")
		want(5, 1, 0, "the 6th email in the hour")
		if e.wakes.Load() != wakes {
			t.Error("Wake ran for a request that queued nothing")
		}
		// An hour later the hour has room, and the day takes 3 more.
		clock.Add(int64(61 * time.Minute))
		for _, name := range []string{"member1", "member2", "member2"} {
			ask(name)
		}
		want(8, 1, 0, "8 emails in the day")
		// The 9th email in the day is a quiet skip, though the hour has room, and
		// it is still a skip two hours later.
		wakes = e.wakes.Load()
		ask("member3")
		// The day's refusal gave back the slot of the hour: this hour holds 3
		// emails, so 2 more fit and a 3rd does not.
		for i := range 3 {
			if ok, _ := e.site.resets.hour.Take(resetBoardKey, now()); ok != (i < 2) {
				t.Errorf("slot %d of the hour after a refusal of the day: ok = %v, want %v", i+4, ok, i < 2)
			}
		}
		for range 2 {
			e.site.resets.hour.Refund(resetBoardKey, now())
		}
		clock.Add(int64(2 * time.Hour))
		ask("member3")
		want(8, 1, 2, "the 9th email in the day")
		if e.wakes.Load() != wakes {
			t.Error("Wake ran for a request that the day refused")
		}
		for _, secret := range []string{"member3", "member3@example.org", "2001:db8:e"} {
			if strings.Contains(logs.String(), secret) {
				t.Errorf("the log holds %q", secret)
			}
		}
		// The skipped requests keep no slot of the account.
		for i := range 3 {
			if ok, _ := e.site.resets.account.Take("103", now()); !ok {
				t.Errorf("the account of the skipped requests has no slot %d", i+1)
			}
		}
		for range 3 {
			e.site.resets.account.Refund("103", now())
		}
		// A day after the first emails, the board has room again.
		clock.Add(int64(21 * time.Hour))
		ask("member3")
		want(9, 1, 2, "a day later")
	})
	t.Run("unknown names do not count against the board", func(t *testing.T) {
		e := newResetEnv(t, Options{})
		for a := range 7 {
			c := e.guest(t, "2001:db8:d"+strconv.Itoa(a)+"::1")
			for range 5 {
				if rec := c.ask("nobody"); rec.Code != http.StatusSeeOther {
					t.Fatalf("address %d = %d, want 303", a, rec.Code)
				}
			}
		}
		// 35 requests for an unknown name have passed. A real account still gets
		// its email.
		c := e.guest(t, "2001:db8:df::1")
		c.requestLink("carol")
	})
	t.Run("account", func(t *testing.T) {
		e := newResetEnv(t, Options{})
		skipped := metrics.ResetSkipped.Value("account_day")
		limited := metrics.RateLimited.Value("reset_account")
		for i := range 4 {
			c := e.guest(t, "2001:db8:f"+strconv.Itoa(i)+"::1")
			if rec := c.ask("alice"); rec.Code != http.StatusSeeOther || locate(rec) != "/reset?sent=1" {
				t.Fatalf("request %d for alice = %d to %q", i+1, rec.Code, locate(rec))
			}
		}
		if n := len(e.queue(t)); n != 3 {
			t.Errorf("the queue holds %d emails after 4 requests, want 3", n)
		}
		if n := e.wakes.Load(); n != 3 {
			t.Errorf("Wake ran %d times, want 3", n)
		}
		if n := metrics.ResetSkipped.Value("account_day") - skipped; n != 1 {
			t.Errorf("the skip metric counted account_day %d times, want 1", n)
		}
		if n := metrics.RateLimited.Value("reset_account") - limited; n != 1 {
			t.Errorf("the rate-limit metric counted reset_account %d times, want 1", n)
		}
	})
	t.Run("dead link", func(t *testing.T) {
		e := newResetEnv(t, Options{})
		c := e.guest(t, "2001:db8:10::1")
		dead := "/reset/" + strings.Repeat("B", 43)
		for i := range 20 {
			if rec := c.send(http.MethodGet, dead, nil, nil); rec.Code != http.StatusNotFound {
				t.Fatalf("dead-link hit %d = %d, want 404", i+1, rec.Code)
			}
		}
		rec := c.send(http.MethodGet, dead, nil, nil)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("21st dead-link hit = %d, want 429", rec.Code)
		}
		if secs, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || secs < 1 {
			t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
		}
		if strings.Contains(bodyOf(t, rec), strings.Repeat("B", 43)) {
			t.Error("the 429 echoes the token")
		}
		// Another address is not limited.
		other := e.guest(t, "2001:db8:11::1")
		if rec := other.send(http.MethodGet, dead, nil, nil); rec.Code != http.StatusNotFound {
			t.Errorf("another address = %d, want 404", rec.Code)
		}
	})
}

func TestResetCSRF(t *testing.T) {
	e := newResetEnv(t, Options{})
	c := e.guest(t, "2001:db8:12::1")
	// A request with no token.
	for i := range 7 {
		if rec := c.send(http.MethodPost, "/reset", url.Values{"name": {"alice"}}, nil); rec.Code != http.StatusForbidden {
			t.Fatalf("POST with no token %d = %d, want 403", i+1, rec.Code)
		}
	}
	// A request from another site, with a good token.
	for i := range 7 {
		rec := c.post("/reset", url.Values{"name": {"alice"}}, map[string]string{"Sec-Fetch-Site": "cross-site"})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("cross-origin POST %d = %d, want 403", i+1, rec.Code)
		}
		rec = c.post("/reset", url.Values{"name": {"alice"}}, map[string]string{"Origin": "https://attacker.example"})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("POST with a foreign Origin %d = %d, want 403", i+1, rec.Code)
		}
	}
	if n := len(e.queue(t)); n != 0 {
		t.Errorf("the queue holds %d emails after refused requests", n)
	}
	// None counted: the address and alice have all their room.
	for i := range 3 {
		if rec := c.ask("alice"); rec.Code != http.StatusSeeOther {
			t.Fatalf("request %d after the refusals = %d, want 303", i+1, rec.Code)
		}
	}
	if n := len(e.queue(t)); n != 3 {
		t.Errorf("the queue holds %d emails, want 3", n)
	}
	// The link form refuses a missing token too.
	token := tokenOf(t, e.queue(t)[0].Body)
	if rec := c.send(http.MethodPost, "/reset/"+token, url.Values{"password": {resetPassword}, "again": {resetPassword}}, nil); rec.Code != http.StatusForbidden {
		t.Errorf("POST /reset/token with no csrf = %d, want 403", rec.Code)
	}
}

// logCapture sends the default slog logger to a buffer until the test ends.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logCapture) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func captureAllLogs(t *testing.T) *logCapture {
	t.Helper()
	l := &logCapture{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return l
}

func TestResetLogsNoSecret(t *testing.T) {
	e := newResetEnv(t, Options{})
	logs := captureAllLogs(t)
	resetFlow(t, e)
	// Failures log too: a dead link, a refused password and a closed database.
	c := e.guest(t, "2001:db8:13::1")
	token := c.requestLink("alice")
	// Every token that the queue holds is a secret, the token of resetFlow too.
	secrets := []string{token, "alice@example.com", resetPassword, resetPassword + "2", strings.Repeat("C", 43)}
	for _, m := range e.queue(t) {
		secrets = append(secrets, tokenOf(t, m.Body))
	}
	c.send(http.MethodGet, "/reset/"+strings.Repeat("C", 43), nil, nil)
	c.post("/reset/"+token, url.Values{"password": {"short"}, "again": {"short"}}, nil)
	e.pool.Close()
	c.send(http.MethodGet, "/reset/"+token, nil, nil)
	c.post("/reset/"+token, url.Values{"password": {resetPassword + "2"}, "again": {resetPassword + "2"}}, nil)
	c.ask("alice")
	out := logs.String()
	for _, secret := range secrets {
		if strings.Contains(out, secret) {
			t.Errorf("a log line holds %q:\n%s", secret, out)
		}
	}
}

func TestLogPath(t *testing.T) {
	tok := strings.Repeat("T", 43)
	cases := []struct{ path, want string }{
		{"/reset/" + tok, "/reset/{token}"},
		{"/reset/", "/reset/{token}"},
		{"/reset", "/reset"},
		{"/resets/" + tok, "/resets/" + tok},
		{"/topic/" + tok, "/topic/" + tok},
		{"/", "/"},
		{"/login", "/login"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, c.path, nil)
			if got := logPath(r); got != c.want {
				t.Errorf("logPath(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
	t.Run("under a base path", func(t *testing.T) {
		// logPath sees the path after stripBase removed the base path.
		s := &site{base: "/forums/"}
		var seen string
		h := s.stripBase(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = logPath(r) }))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/forums/reset/"+tok, nil))
		if seen != "/reset/{token}" {
			t.Errorf("logPath behind the base path = %q, want /reset/{token}", seen)
		}
	})
}

// The tests of the routes need a token whose hash a test can insert.
var testResetToken = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))

func testResetTokenRow() string {
	sum := sha256.Sum256([]byte(testResetToken))
	return `insert into password_resets (id, user_id, epoch, created_at, expires_at)
 select '` + hex.EncodeToString(sum[:]) + `', id, password_epoch, now(), now() + interval '1 hour' from users where username = 'carol'`
}

func TestNewRefusesMailWithoutLimits(t *testing.T) {
	for _, m := range []MailOptions{
		{PublicURL: resetPublic, PerDay: 20},
		{PublicURL: resetPublic, PerHour: 10},
	} {
		if _, err := New(context.Background(), nil, Options{Mail: &m}); err == nil || !strings.Contains(err.Error(), "PerHour") {
			t.Errorf("New with %+v: err = %v, want an error that names PerHour and PerDay", m, err)
		}
	}
}
