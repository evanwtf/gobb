package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/htmlcheck"
	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/password"
	"github.com/evanwtf/gobb/internal/session"
)

const newPassword = "a fresh pass phrase"

// passwordForm is a good form for POST /account/password.
func passwordForm(current, next string) url.Values {
	return url.Values{"current": {current}, "next": {next}, "confirm": {next}}
}

// userCol reads one column of one user.
func userCol(t *testing.T, pool *pgxpool.Pool, id int, col string) string {
	t.Helper()
	var v string
	if err := pool.QueryRow(t.Context(), `select `+col+`::text from users where id = $1`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// sessionCookie returns the value of the session cookie in a jar, or "".
func sessionValue(cookies []*http.Cookie) string {
	for _, c := range cookies {
		if c.Name == "gobb_session" {
			return c.Value
		}
	}
	return ""
}

// passwordInputs returns the input elements of type password in a page: for each,
// whether it carries a value attribute.
func passwordInputs(page string) (n int, withValue []string) {
	z := xhtml.NewTokenizer(strings.NewReader(page))
	for {
		switch z.Next() {
		case xhtml.ErrorToken:
			return n, withValue
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			tn, hasAttr := z.TagName()
			if string(tn) != "input" {
				continue
			}
			attrs := map[string]string{}
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				attrs[string(k)] = string(v)
			}
			if attrs["type"] == "password" {
				n++
				if _, ok := attrs["value"]; ok {
					withValue = append(withValue, attrs["name"])
				}
			}
		}
	}
}

// noPasswordEcho fails when a page holds a password field with a value, or any of
// the secrets in plain text, or when the page has no password field at all (the
// form must still be there).
func noPasswordEcho(t *testing.T, where, page string, secrets ...string) {
	t.Helper()
	n, with := passwordInputs(page)
	if n < 3 {
		t.Errorf("%s: %d password fields, want the form", where, n)
	}
	if len(with) != 0 {
		t.Errorf("%s: password fields with a value: %v", where, with)
	}
	for _, s := range secrets {
		if s != "" && strings.Contains(page, s) {
			t.Errorf("%s: the page holds %q", where, s)
		}
	}
}

func TestAccountPage(t *testing.T) {
	h, _ := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", testPassword)
	rec := getAs(t, h, "/account", a)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /account = %d", rec.Code)
	}
	page := bodyOf(t, rec)
	for _, want := range []string{
		`action="/account/password"`, `action="/account/email"`, `action="/account/signature"`,
		`name="current"`, `name="next"`, `name="confirm"`, `name="email"`, `name="signature"`,
		`autocomplete="current-password"`, `autocomplete="new-password"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the account page lacks %s", want)
		}
	}
	if got, ok := inputValue(page, "email"); !ok || got != "alice@example.com" {
		t.Errorf("the email field holds %q", got)
	}
	if got, ok := textareaOf(t, page, "signature"); !ok || got != "Example signature" {
		t.Errorf("the signature box holds %q", got)
	}
	noPasswordEcho(t, "GET /account", page)
	if strings.Count(page, `name="csrf"`) < 4 { // three forms and the layout's own
		t.Errorf("%d csrf fields", strings.Count(page, `name="csrf"`))
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if strings.Contains(page, ` style=`) || strings.Contains(page, "<script>") {
		t.Error("the page holds inline style or script")
	}
	if !strings.Contains(page, `<meta name="robots" content="noindex">`) {
		t.Error("the account page is indexable")
	}

	// A guest still goes to sign in.
	g := get(t, h, "/account")
	if g.Code != http.StatusFound || g.Header().Get("Location") != "/login?next=%2Faccount" {
		t.Errorf("guest: %d %q", g.Code, g.Header().Get("Location"))
	}
}

// The done notice is one of three fixed texts. Any other value shows nothing and
// is never echoed.
func TestAccountDoneNotices(t *testing.T) {
	h, _ := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", testPassword)
	const marker = `class="notice-inline"`
	for code, want := range map[string]string{
		"password":  "Your password is changed.",
		"email":     "Your email address is changed.",
		"signature": "Your signature is saved.",
	} {
		page := bodyOf(t, getAs(t, h, "/account?done="+code, a))
		if !strings.Contains(page, want) || !strings.Contains(page, marker) {
			t.Errorf("done=%s: the page lacks %q", code, want)
		}
	}
	for _, code := range []string{"", "other", "Password", "password%20", "%3Cscript%3Ealert(1)%3C%2Fscript%3E", "password%26done%3Demail"} {
		page := bodyOf(t, getAs(t, h, "/account?done="+code, a))
		if strings.Contains(page, marker) || strings.Contains(page, "is changed.") || strings.Contains(page, "is saved.") {
			t.Errorf("done=%q shows a notice", code)
		}
		if strings.Contains(page, "<script>alert") || strings.Contains(page, "alert(1)") {
			t.Errorf("done=%q is echoed", code)
		}
	}
}

func TestChangePassword(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	old := signIn(t, h, "alice", testPassword)
	other := signIn(t, h, "alice", testPassword) // a second browser
	bob := signIn(t, h, "bob", testPassword)
	hashBefore := userCol(t, pool, 1, "password_hash")

	// A wrong current password answers 400, with the form and the message, and
	// changes nothing. No password field holds a value.
	rec := send(t, h, "/account/password", old, passwordForm("not the password", newPassword))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong current password = %d, want 400", rec.Code)
	}
	page := bodyOf(t, rec)
	if !strings.Contains(page, "That is not your current password.") {
		t.Error("the page lacks the message")
	}
	noPasswordEcho(t, "wrong current", page, "not the password", newPassword)
	if userCol(t, pool, 1, "password_hash") != hashBefore {
		t.Error("a wrong current password changed the hash")
	}
	if rec := getAs(t, h, "/account", old); rec.Code != http.StatusOK {
		t.Errorf("the session ended after a refused change: %d", rec.Code)
	}

	// The new password is refused when short, when it is the username's, when too
	// long, and when the two entries differ.
	for _, tc := range []struct {
		name string
		form url.Values
		msg  string
	}{
		{"9 characters", passwordForm(testPassword, "123456789"), "Use at least 10 characters."},
		{"the username", passwordForm(testPassword, "alice"), "Use at least 10 characters."},
		{"1,025 bytes", passwordForm(testPassword, strings.Repeat("a", 1025)), "Use at most 1,024 bytes."},
		{"mismatch", url.Values{"current": {testPassword}, "next": {newPassword}, "confirm": {newPassword + "x"}}, "The two new passwords differ."},
		{"no confirm", url.Values{"current": {testPassword}, "next": {newPassword}}, "The two new passwords differ."},
	} {
		rec := send(t, h, "/account/password", old, tc.form)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", tc.name, rec.Code)
			continue
		}
		page := bodyOf(t, rec)
		if !strings.Contains(page, tc.msg) {
			t.Errorf("%s: the page lacks %q", tc.name, tc.msg)
		}
		secrets := []string{testPassword}
		if tc.form.Get("next") != "alice" { // the name is on every page
			secrets = append(secrets, tc.form.Get("next"))
		}
		noPasswordEcho(t, tc.name, page, secrets...)
	}
	if userCol(t, pool, 1, "password_hash") != hashBefore {
		t.Error("a refused new password changed the hash")
	}

	// The good change: 303 to the notice, a new session cookie.
	rec, cookies := postAs(t, h, "/account/password", old, passwordForm(testPassword, newPassword))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/account?done=password" {
		t.Fatalf("change = %d %q\n%s", rec.Code, rec.Header().Get("Location"), bodyOf(t, rec))
	}
	fresh := sessionValue(cookies)
	if fresh == "" || fresh == sessionValue(old) {
		t.Fatalf("the answer sets no new session cookie (%q, old %q)", fresh, sessionValue(old))
	}
	var set *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "gobb_session" {
			set = c
		}
	}
	if set == nil || !set.HttpOnly || set.SameSite != http.SameSiteLaxMode || set.Path != "/" || set.MaxAge != 0 {
		t.Errorf("session cookie = %+v, want HttpOnly, Lax, path /, a browser-session cookie", set)
	}
	if userCol(t, pool, 1, "password_scheme") != "gobb" {
		t.Error("password_scheme is not gobb")
	}
	if ok, _ := password.Verify(newPassword, userCol(t, pool, 1, "password_hash"), "gobb"); !ok {
		t.Error("the stored hash does not verify the new password")
	}

	// Both old sessions are dead: a guest page, and the account page goes to sign
	// in. Bob's session lives. The new cookie is signed in.
	for name, c := range map[string][]*http.Cookie{"old": old, "other": other} {
		if rec := getAs(t, h, "/account", c); rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
			t.Errorf("%s session: GET /account = %d %q, want a guest's redirect", name, rec.Code, rec.Header().Get("Location"))
		}
	}
	if rec := getAs(t, h, "/account", bob); rec.Code != http.StatusOK {
		t.Errorf("bob's session: %d", rec.Code)
	}
	next := getAs(t, h, "/account?done=password", cookies)
	nextPage := bodyOf(t, next)
	if next.Code != http.StatusOK || !strings.Contains(nextPage, "Your password is changed.") {
		t.Errorf("the new session: %d", next.Code)
	}
	var sessions int
	if err := pool.QueryRow(t.Context(), `select count(*) from sessions where user_id = 1`).Scan(&sessions); err != nil || sessions != 1 {
		t.Errorf("alice has %d sessions, %v, want 1", sessions, err)
	}

	// The csrf token follows the session: the next page carries a new one, and
	// the old one no longer works.
	oldToken := session.CSRFToken(sessionValue(old))
	newToken := formToken(t, nextPage)
	if newToken == oldToken || newToken != session.CSRFToken(fresh) {
		t.Errorf("the page after the change carries the old token or a wrong one")
	}
	r := request(t, h, http.MethodPost, "/account/signature", cookies, url.Values{"csrf": {oldToken}, "signature": {"x"}})
	if r.Code != http.StatusForbidden {
		t.Errorf("old token with the new session = %d, want 403", r.Code)
	}

	// The old password fails at /login, and the new one works.
	if rec, _ := postAs(t, h, "/login", nil, url.Values{"name": {"alice"}, "password": {testPassword}}); rec.Code != http.StatusOK || !strings.Contains(bodyOf(t, rec), "The name or password is wrong.") {
		t.Errorf("old password at /login = %d", rec.Code)
	}
	if rec, _ := postAs(t, h, "/login", nil, url.Values{"name": {"alice"}, "password": {newPassword}}); rec.Code != http.StatusSeeOther {
		t.Errorf("new password at /login = %d", rec.Code)
	}
}

// The new session keeps the "remember me" choice of the old one.
func TestChangePasswordKeepsRemember(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	for _, tc := range []struct {
		name     string
		id       int
		remember bool
	}{{"bob", 2, true}, {"carol", 3, false}} {
		form := url.Values{"name": {tc.name}, "password": {testPassword}}
		if tc.remember {
			form.Set("remember", "1")
		}
		rec, cookies := postAs(t, h, "/login", nil, form)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("sign-in of %s = %d", tc.name, rec.Code)
		}
		rec, _ = postAs(t, h, "/account/password", cookies, passwordForm(testPassword, newPassword))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%s: change = %d\n%s", tc.name, rec.Code, bodyOf(t, rec))
		}
		var set *http.Cookie
		for _, c := range rec.Result().Cookies() {
			if c.Name == "gobb_session" {
				set = c
			}
		}
		if set == nil {
			t.Fatalf("%s: no session cookie", tc.name)
		}
		wantAge := 0
		if tc.remember {
			wantAge = int(session.RememberFor / time.Second)
		}
		if set.MaxAge != wantAge {
			t.Errorf("%s: Max-Age = %d, want %d", tc.name, set.MaxAge, wantAge)
		}
		var got bool
		var exp time.Time
		if err := pool.QueryRow(t.Context(), `select remember, expires_at from sessions where user_id = $1`, tc.id).Scan(&got, &exp); err != nil || got != tc.remember {
			t.Errorf("%s: stored remember = %v, %v", tc.name, got, err)
		}
		if long := exp.After(time.Now().Add(48 * time.Hour)); long != tc.remember {
			t.Errorf("%s: expires_at %v does not fit remember = %v", tc.name, exp, tc.remember)
		}
	}
}

// The new password may not be the username, however the username is written.
func TestChangePasswordNotTheUsername(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	exec(t, pool, `update users set username = 'Carol-The-Poster', username_clean = 'carol-the-poster' where id = 3`)
	c := signIn(t, h, "carol-the-poster", testPassword)
	hash := userCol(t, pool, 3, "password_hash")
	for _, pw := range []string{"Carol-The-Poster", "CAROL-THE-POSTER", "carol-the-poster"} {
		rec := send(t, h, "/account/password", c, passwordForm(testPassword, pw))
		if rec.Code != http.StatusBadRequest || !strings.Contains(bodyOf(t, rec), "Choose a password that differs from your username.") {
			t.Errorf("%q = %d", pw, rec.Code)
		}
	}
	if userCol(t, pool, 3, "password_hash") != hash {
		t.Error("the hash changed")
	}
}

// A wrong current password counts toward the sign-in limits, as a failed /login
// does, and the limit answers 429 with the form. The change is not made, even
// with the right password, while the limit holds. The account tries (5 an hour
// for each form) are lower than the sign-in limit (10 for an address), so the
// failures are spread over the two forms and /login to reach it.
func TestChangePasswordLimits(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", testPassword)
	hash := userCol(t, pool, 1, "password_hash")
	for i := range 4 {
		if rec := send(t, h, "/account/password", a, passwordForm("wrong "+strconv.Itoa(i), newPassword)); rec.Code != http.StatusBadRequest {
			t.Fatalf("password failure %d = %d, want 400", i+1, rec.Code)
		}
		if rec := send(t, h, "/account/email", a, url.Values{"email": {"a2@example.org"}, "current": {"wrong " + strconv.Itoa(i)}}); rec.Code != http.StatusBadRequest {
			t.Fatalf("email failure %d = %d, want 400", i+1, rec.Code)
		}
	}
	for i := range 2 {
		if rec, _ := postAs(t, h, "/login", nil, url.Values{"name": {"alice"}, "password": {"wrong"}}); rec.Code != http.StatusOK {
			t.Fatalf("login failure %d = %d", i+1, rec.Code)
		}
	}
	rec := send(t, h, "/account/password", a, passwordForm(testPassword, newPassword))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("11th failure's successor = %d, want 429", rec.Code)
	}
	page := bodyOf(t, rec)
	if !strings.Contains(page, "Too many sign-in attempts. Try again in 15 minutes.") {
		t.Errorf("the 429 page lacks the limit's message")
	}
	noPasswordEcho(t, "429", page, testPassword, newPassword, "wrong ")
	if secs, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || secs < 1 || secs > 900 {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	if userCol(t, pool, 1, "password_hash") != hash {
		t.Error("the hash changed while the limit held")
	}
	// The email form meets the same limit.
	rec = send(t, h, "/account/email", a, url.Values{"email": {"a2@example.org"}, "current": {testPassword}})
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(bodyOf(t, rec), "Too many sign-in attempts.") {
		t.Errorf("email form after the failures = %d", rec.Code)
	}
	// The limit is shared with /login: the sign-in page is limited too.
	if rec, _ := postAs(t, h, "/login", nil, url.Values{"name": {"alice"}, "password": {testPassword}}); rec.Code != http.StatusTooManyRequests {
		t.Errorf("/login after the failures = %d, want 429", rec.Code)
	}
}

// Each member gets 5 tries an hour on each of the email and the password form.
// A try is a POST that reaches the check of the current password: refusals count
// as much as successes. The 6th answers 429 with the form and the kept fields,
// even with the right password and a good change, and nothing is written. The
// forms count apart, members count apart, and the hour passes.
func TestAccountTriesPerHour(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	clock := now
	h, pool := siteWithPool(t, postingBoard(), Options{Now: func() time.Time { return clock }})
	c := signIn(t, h, "carol", testPassword)
	const tooMany = "Too many tries. Wait an hour and try again."

	// Email: two refusals, three successes (each a change), then the 6th.
	emails := []string{"c1@example.org", "c2@example.org", "c3@example.org"}
	for i := range 2 {
		if rec := send(t, h, "/account/email", c, url.Values{"email": {"x@example.org"}, "current": {"wrong" + strconv.Itoa(i)}}); rec.Code != http.StatusBadRequest {
			t.Fatalf("refusal %d = %d", i+1, rec.Code)
		}
	}
	for _, e := range emails {
		if rec := send(t, h, "/account/email", c, url.Values{"email": {e}, "current": {testPassword}}); rec.Code != http.StatusSeeOther {
			t.Fatalf("change to %s = %d\n%s", e, rec.Code, bodyOf(t, rec))
		}
	}
	rec := send(t, h, "/account/email", c, url.Values{"email": {"c4@example.org"}, "current": {testPassword}})
	page := bodyOf(t, rec)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(page, tooMany) {
		t.Fatalf("6th email try = %d\n%s", rec.Code, page)
	}
	if got, _ := inputValue(page, "email"); got != "c4@example.org" {
		t.Errorf("the email field holds %q, want the sent address", got)
	}
	noPasswordEcho(t, "email 429", page, testPassword)
	if secs, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || secs < 3000 || secs > 3600 {
		t.Errorf("Retry-After = %q, want about an hour", rec.Header().Get("Retry-After"))
	}
	if userCol(t, pool, 3, "email") != "c3@example.org" {
		t.Errorf("the 429 wrote %q", userCol(t, pool, 3, "email"))
	}

	// The password form has its own five. Two refusals and three changes.
	for i := range 2 {
		if rec := send(t, h, "/account/password", c, passwordForm("wrong"+strconv.Itoa(i), newPassword)); rec.Code != http.StatusBadRequest {
			t.Fatalf("password refusal %d = %d", i+1, rec.Code)
		}
	}
	current := testPassword
	for i := range 3 {
		next := newPassword + strconv.Itoa(i)
		rec, cookies := postAs(t, h, "/account/password", c, passwordForm(current, next))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("password change %d = %d\n%s", i+1, rec.Code, bodyOf(t, rec))
		}
		c, current = cookies, next
	}
	hash := userCol(t, pool, 3, "password_hash")
	rec = send(t, h, "/account/password", c, passwordForm(current, newPassword+"last"))
	page = bodyOf(t, rec)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(page, tooMany) {
		t.Fatalf("6th password try = %d\n%s", rec.Code, page)
	}
	noPasswordEcho(t, "password 429", page, current, newPassword)
	if userCol(t, pool, 3, "password_hash") != hash {
		t.Error("the 6th password try changed the hash")
	}
	if rec := getAs(t, h, "/account", c); rec.Code != http.StatusOK {
		t.Errorf("the session ended after a refused try: %d", rec.Code)
	}

	// Another member has fresh tries.
	b := signIn(t, h, "bob", testPassword)
	if rec := send(t, h, "/account/email", b, url.Values{"email": {"b1@example.org"}, "current": {testPassword}}); rec.Code != http.StatusSeeOther {
		t.Errorf("bob's first email try = %d", rec.Code)
	}
	// After the hour, the member has tries again.
	clock = now.Add(61 * time.Minute)
	if rec := send(t, h, "/account/email", c, url.Values{"email": {"c5@example.org"}, "current": {current}}); rec.Code != http.StatusSeeOther {
		t.Errorf("email try after the hour = %d", rec.Code)
	}
}

// A try that fails the cheap checks (a short new password, a taken form) never
// reaches the check of the current password, so it takes no try.
func TestAccountTriesCountOnlyTheCheck(t *testing.T) {
	h, _ := siteWithPool(t, postingBoard(), Options{})
	c := signIn(t, h, "carol", testPassword)
	for range 8 {
		if rec := send(t, h, "/account/password", c, passwordForm(testPassword, "short")); rec.Code != http.StatusBadRequest {
			t.Fatalf("short password = %d", rec.Code)
		}
		if rec := send(t, h, "/account/email", c, url.Values{"email": {"not an address"}, "current": {testPassword}}); rec.Code != http.StatusBadRequest {
			t.Fatalf("bad address = %d", rec.Code)
		}
	}
	if rec := send(t, h, "/account/email", c, url.Values{"email": {"fine@example.org"}, "current": {testPassword}}); rec.Code != http.StatusSeeOther {
		t.Errorf("a real try after 16 cheap refusals = %d", rec.Code)
	}
}

// A full gate answers 503 with Retry-After, with the form, and changes nothing.
func TestChangePasswordBusy(t *testing.T) {
	gate := login.NewGate(1024)
	h, pool := siteWithPool(t, postingBoard(), Options{LoginGate: gate, LoginGateWait: 20 * time.Millisecond})
	a := signIn(t, h, "alice", testPassword)
	hold, err := gate.Acquire(t.Context(), 1024, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	hash := userCol(t, pool, 1, "password_hash")
	rec := send(t, h, "/account/password", a, passwordForm(testPassword, newPassword))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" {
		t.Fatalf("busy = %d, Retry-After %q, want 503 and 5", rec.Code, rec.Header().Get("Retry-After"))
	}
	noPasswordEcho(t, "503", bodyOf(t, rec), testPassword, newPassword)
	if userCol(t, pool, 1, "password_hash") != hash {
		t.Error("the hash changed on a busy gate")
	}
}

// The hash of the new password takes the gate too, and a full gate at that point
// answers 503 and stores nothing. The gate is full only after the check of the
// current password has run, so the 503 can come from nowhere but the hold around
// the hash. Deleting that hold makes this test fail.
func TestChangePasswordHashHoldsTheGate(t *testing.T) {
	gate := login.NewGate(password.HashMemoryKiB)
	h, pool := siteWithPool(t, postingBoard(), Options{LoginGate: gate, LoginGateWait: 20 * time.Millisecond})
	a := signIn(t, h, "alice", testPassword)
	hash := userCol(t, pool, 1, "password_hash")

	var held func()
	beforeHold = func() {
		var err error
		if held, err = gate.Acquire(t.Context(), password.HashMemoryKiB, time.Second); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeHold = nil })
	rec := send(t, h, "/account/password", a, passwordForm(testPassword, newPassword))
	if held != nil {
		held()
	}
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" {
		t.Fatalf("change with the gate full at the hash = %d, Retry-After %q, want 503 and 5\n%s", rec.Code, rec.Header().Get("Retry-After"), bodyOf(t, rec))
	}
	noPasswordEcho(t, "503 at the hash", bodyOf(t, rec), testPassword, newPassword)
	if userCol(t, pool, 1, "password_hash") != hash {
		t.Error("the hash changed although the hold failed")
	}
	if rec := getAs(t, h, "/account", a); rec.Code != http.StatusOK {
		t.Errorf("the session ended: %d", rec.Code)
	}

	// With the gate free the same change goes through on a one-share gate: the
	// check of the current password gives its share back before the hash takes one.
	beforeHold = nil
	rec = send(t, h, "/account/password", a, passwordForm(testPassword, newPassword))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("change on a one-share gate = %d\n%s", rec.Code, bodyOf(t, rec))
	}
	if userCol(t, pool, 1, "password_hash") == hash {
		t.Error("the hash did not change")
	}
}

func TestPasswordRightIsChecked(t *testing.T) {
	b := postingBoard()
	h, pool := siteWithPool(t, b, Options{})
	c := signIn(t, h, "carol", testPassword)
	exec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (3, 0, 'account.password', 2)`)
	hash := userCol(t, pool, 3, "password_hash")
	rec := send(t, h, "/account/password", c, passwordForm(testPassword, newPassword))
	if rec.Code != http.StatusForbidden {
		t.Errorf("without account.password = %d, want 403", rec.Code)
	}
	if userCol(t, pool, 3, "password_hash") != hash {
		t.Error("the hash changed")
	}
	page := bodyOf(t, getAs(t, h, "/account", c))
	if strings.Contains(page, `action="/account/password"`) || !strings.Contains(page, `action="/account/email"`) {
		t.Error("the page shows the password form, or lacks the email form")
	}
}

func TestGuestCannotUseTheAccountRoutes(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	before := dumpData(t, pool)
	for _, tc := range []struct {
		target string
		form   url.Values
	}{
		{"/account/password", passwordForm(testPassword, newPassword)},
		{"/account/email", url.Values{"email": {"x@example.org"}, "current": {testPassword}}},
		{"/account/signature", url.Values{"signature": {"x"}}},
	} {
		rec := send(t, h, tc.target, nil, tc.form)
		if rec.Code != http.StatusForbidden {
			t.Errorf("guest POST %s = %d, want 403", tc.target, rec.Code)
		}
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("a guest changed %v", diff)
	}
}

func TestChangeEmail(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	c := signIn(t, h, "carol", testPassword)
	exec(t, pool, `update users set email_verified = true where id = 3`)
	form := func(email, current string) url.Values { return url.Values{"email": {email}, "current": {current}} }

	// A wrong current password: 400, the email field keeps what was typed.
	rec := send(t, h, "/account/email", c, form("new@example.org", "nope"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong password = %d", rec.Code)
	}
	page := bodyOf(t, rec)
	if !strings.Contains(page, "That is not your current password.") {
		t.Error("the page lacks the message")
	}
	if got, _ := inputValue(page, "email"); got != "new@example.org" {
		t.Errorf("the email field holds %q after a wrong password", got)
	}
	noPasswordEcho(t, "email, wrong password", page, "nope")

	// A taken address: 400 with the message, and the typed address stays.
	for _, e := range []string{"alice@example.com", " ALICE@example.com"} {
		rec = send(t, h, "/account/email", c, form(e, testPassword))
		page = bodyOf(t, rec)
		if rec.Code != http.StatusBadRequest || !strings.Contains(page, "That address belongs to another account.") {
			t.Errorf("taken %q = %d", e, rec.Code)
		}
		if got, _ := inputValue(page, "email"); got != e {
			t.Errorf("the email field holds %q, want %q", got, e)
		}
		noPasswordEcho(t, "email, taken", page, testPassword)
	}
	// A bad shape.
	rec = send(t, h, "/account/email", c, form("not an address", testPassword))
	if rec.Code != http.StatusBadRequest || !strings.Contains(bodyOf(t, rec), "Enter a valid email address.") {
		t.Errorf("bad shape = %d", rec.Code)
	}
	if userCol(t, pool, 3, "email") != "carol@example.com" || userCol(t, pool, 3, "email_verified") != "true" {
		t.Fatal("a refused change touched the row")
	}

	// The own address, unchanged, is a no-op: 303, and email_verified stays.
	rec = send(t, h, "/account/email", c, form("Carol@Example.com", testPassword))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/account" {
		t.Errorf("own address = %d %q, want 303 to /account with no notice", rec.Code, rec.Header().Get("Location"))
	}
	if userCol(t, pool, 3, "email") != "carol@example.com" || userCol(t, pool, 3, "email_verified") != "true" {
		t.Error("the no-op changed the row")
	}

	// A free address is saved, and email_verified is false.
	rec = send(t, h, "/account/email", c, form("carol.new@example.org", testPassword))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/account?done=email" {
		t.Fatalf("free address = %d %q\n%s", rec.Code, rec.Header().Get("Location"), bodyOf(t, rec))
	}
	if userCol(t, pool, 3, "email") != "carol.new@example.org" || userCol(t, pool, 3, "email_verified") != "false" {
		t.Errorf("stored %q, verified %s", userCol(t, pool, 3, "email"), userCol(t, pool, 3, "email_verified"))
	}
	// The session is not touched by an email change.
	if rec := getAs(t, h, "/account", c); rec.Code != http.StatusOK {
		t.Errorf("the session after an email change = %d", rec.Code)
	}
	// The address shows on the own account page and on no profile, for anyone.
	if got, _ := inputValue(bodyOf(t, getAs(t, h, "/account", c)), "email"); got != "carol.new@example.org" {
		t.Errorf("the account page shows %q", got)
	}
	a := signIn(t, h, "alice", testPassword) // alice is an admin
	for who, cookies := range map[string][]*http.Cookie{"guest": nil, "carol": c, "alice": a} {
		for _, p := range []string{"/u/3", "/members", "/team", "/t/1", "/search?author=carol"} {
			if strings.Contains(bodyOf(t, getAs(t, h, p, cookies)), "carol.new@example.org") {
				t.Errorf("%s sees the address on %s", who, p)
			}
		}
	}
}

func TestSetSignature(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", testPassword)

	// Post 1 is alice's and shows her signature.
	rec := send(t, h, "/account/signature", a, url.Values{"signature": {"Yours, **x**"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/account?done=signature" {
		t.Fatalf("set = %d %q\n%s", rec.Code, rec.Header().Get("Location"), bodyOf(t, rec))
	}
	sig, tail := signatureDivs(bodyOf(t, getAs(t, h, "/t/1", a)))
	if len(tail) != 0 {
		t.Errorf("markup after the signature: %q", tail)
	}
	if len(sig) == 0 || !strings.Contains(sig[0], "<strong>x</strong>") {
		t.Errorf("signature on her post = %q", sig)
	}
	if got, _ := textareaOf(t, bodyOf(t, getAs(t, h, "/account", a)), "signature"); got != "Yours, **x**" {
		t.Errorf("the account page shows %q", got)
	}

	// 2,001 bytes: 400, the message, the text stays in the box, nothing changes.
	long := strings.Repeat("é", 900) + "ab\r\n&<>\"'"
	long += strings.Repeat("a", 2001-len(long))
	rec = send(t, h, "/account/signature", a, url.Values{"signature": {long}})
	page := bodyOf(t, rec)
	if rec.Code != http.StatusBadRequest || !strings.Contains(page, "The text is too long. The limit is 2,000 bytes.") {
		t.Fatalf("2,001 bytes = %d", rec.Code)
	}
	if got, _ := textareaOf(t, page, "signature"); got != long {
		t.Errorf("the box holds %d bytes, want the %d sent", len(got), len(long))
	}
	if got := userCol(t, pool, 1, "signature_source"); got != "Yours, **x**" {
		t.Errorf("stored signature = %q", got)
	}
	// 2,000 bytes pass.
	if rec := send(t, h, "/account/signature", a, url.Values{"signature": {long[:2000]}}); rec.Code != http.StatusSeeOther {
		t.Errorf("2,000 bytes = %d", rec.Code)
	}
	// Empty clears it, and the post shows none.
	if rec := send(t, h, "/account/signature", a, url.Values{"signature": {""}}); rec.Code != http.StatusSeeOther {
		t.Errorf("empty = %d", rec.Code)
	}
	if sig, _ := signatureDivs(bodyOf(t, getAs(t, h, "/t/1", a))); len(sig) != 0 {
		t.Errorf("a cleared signature still shows: %q", sig)
	}
	// The email and password fields of the same page are unaffected by a signature error.
	rec = send(t, h, "/account/signature", a, url.Values{"signature": {strings.Repeat("a", 2001)}})
	noPasswordEcho(t, "signature error", bodyOf(t, rec))
}

// signatureDivs returns the raw inner HTML of every element with the class
// signature, cut between its start tag and its matching end tag by depth. The
// bytes are the ones the server wrote. Like postBodies for a body, it also checks
// what follows the end tag: only white space, then the address line or the
// buttons of the post, or the end of the post (the </div> of the post and the
// </article>). Anything else is a problem, so a signature that closed its div
// early cannot hide the bytes after the early close.
func signatureDivs(page string) (out, problems []string) {
	z := xhtml.NewTokenizer(strings.NewReader(page))
	// after is 0 outside the tail. It is 1 right after a signature ends, and 2
	// after the end of the post-main element, when </article> must follow.
	pos, start, depth, after := 0, -1, 0, 0
	for {
		tt := z.Next()
		raw := z.Raw()
		before := pos
		pos += len(raw)
		if tt == xhtml.ErrorToken {
			if start >= 0 {
				problems = append(problems, "a signature element is not closed")
			}
			return out, problems
		}
		if after > 0 && start < 0 && !(tt == xhtml.TextToken && strings.TrimSpace(string(raw)) == "") {
			ok := false
			switch tt {
			case xhtml.StartTagToken:
				tn, hasAttr := z.TagName()
				class := ""
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					if string(k) == "class" {
						class = string(v)
					}
				}
				ok = after == 1 && (string(tn) == "p" && class == "post-ip" || string(tn) == "div" && class == "post-actions")
				after = 0
			case xhtml.EndTagToken:
				tn, _ := z.TagName()
				switch {
				case after == 1 && string(tn) == "div":
					ok, after = true, 2
				case after == 2 && string(tn) == "article":
					ok, after = true, 0
				default:
					after = 0
				}
			default:
				after = 0
			}
			if !ok {
				problems = append(problems, fmt.Sprintf("unexpected markup after a signature: %q", raw))
			}
		}
		switch tt {
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			tn, hasAttr := z.TagName()
			if string(tn) != "div" || tt == xhtml.SelfClosingTagToken {
				continue
			}
			if start >= 0 {
				depth++
				continue
			}
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				if string(k) == "class" && slicesContains(strings.Fields(string(v)), "signature") {
					start, depth = pos, 1
				}
			}
		case xhtml.EndTagToken:
			if tn, _ := z.TagName(); start >= 0 && string(tn) == "div" {
				if depth--; depth == 0 {
					out = append(out, page[start:before])
					start, after = -1, 1
				}
			}
		}
	}
}

// Every line of the script-injection corpus, set as the signature of alice,
// gives a .signature element on her topic page that passes the strict HTML
// check. A refusal keeps the text and changes nothing.
func TestSignaturePayloads(t *testing.T) {
	payloads := loadPayloads(t)
	if len(payloads) < 150 {
		t.Fatalf("%d payloads", len(payloads))
	}
	h, _ := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", testPassword)
	token := formToken(t, bodyOf(t, getAs(t, h, "/account", a)))
	saved, refused, checked, blank := 0, 0, 0, 0
	for _, p := range payloads {
		rec := request(t, h, http.MethodPost, "/account/signature", a, url.Values{"csrf": {token}, "signature": {p.text}})
		valid := utf8.ValidString(p.text) && !strings.ContainsRune(p.text, 0) && len(p.text) <= 2000
		switch {
		case rec.Code == http.StatusSeeOther:
			saved++
		case rec.Code == http.StatusBadRequest && !valid:
			refused++
			continue
		case rec.Code == http.StatusBadRequest && strings.Contains(bodyOf(t, rec), "You may not use images in a signature."):
			t.Errorf("payloads.txt:%d: images refused although alice may post them", p.line)
			continue
		default:
			t.Errorf("payloads.txt:%d: status %d for %q", p.line, rec.Code, p.text)
			continue
		}
		if rec.Code != http.StatusSeeOther {
			continue
		}
		topic := getAs(t, h, "/t/1", a)
		body := bodyOf(t, topic)
		divs, tail := signatureDivs(body)
		for _, pr := range tail {
			t.Errorf("payloads.txt:%d: %s", p.line, pr)
		}
		if strings.TrimSpace(p.text) == "" {
			blank++
			if len(divs) != 0 {
				t.Errorf("payloads.txt:%d: a blank signature shows", p.line)
			}
			continue
		}
		if len(divs) == 0 {
			// A text that renders to nothing (a comment, say) has no element.
			continue
		}
		for _, d := range divs {
			htmlcheck.Check(t, d)
			checked++
		}
		bs, problems := postBodies(body)
		for _, pr := range problems {
			t.Errorf("payloads.txt:%d: %s", p.line, pr)
		}
		for _, b := range bs {
			htmlcheck.Check(t, b)
		}
		csp := topic.Header().Get("Content-Security-Policy")
		checkScripts(t, "signature payload "+strconv.Itoa(p.line), body, csp)
	}
	if saved < 100 || checked < 100 {
		t.Fatalf("saved %d, checked %d signatures", saved, checked)
	}
	t.Logf("payloads: %d; saved: %d; refused: %d; blank: %d; .signature elements checked: %d", len(payloads), saved, refused, blank, checked)
}

// A guest's token does not let a guest edit a post, whoever wrote it: the
// answer is 403 or 404 and nothing changes.
func TestGuestEditWithGuestTokenChangesNothing(t *testing.T) {
	h, pool := siteWithPool(t, guestBoard(false), Options{})
	// A guest post, and a member's post.
	rec := postGuest(t, h, "/f/2/post", nil, url.Values{"title": {"Help"}, "body": {"Locked out."}, "guest_name": {"Visitor 9"}})
	guestPost := idOf(t, rec)
	before := dumpData(t, pool)
	for _, id := range []int{1, guestPost, 5, 9999} {
		rec, _ := postAs(t, h, "/p/"+strconv.Itoa(id)+"/edit", nil, url.Values{"body": {"hijack"}, "title": {"hijack"}})
		page := bodyOf(t, rec)
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
			t.Errorf("guest POST /p/%d/edit = %d, want 403 or 404", id, rec.Code)
		}
		// The token is good, so the refusal must be the rights', and not the page
		// of a form that could not be checked.
		if strings.Contains(page, expiredHeading) || strings.Contains(page, "Form not accepted") || strings.Contains(page, "The form could not be checked") {
			t.Errorf("guest POST /p/%d/edit was refused as a bad form, not on the rights:\n%s", id, page)
		}
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("a guest's edit changed %v", diff)
	}
}

// A member's signature shows under a reply made just now where the member holds
// post.signatures, and does not show where the member lacks it. A guest's post
// shows none.
func TestSignatureShowsUnderANewReply(t *testing.T) {
	b := guestBoard(false)
	h, pool := siteWithPool(t, b, Options{})
	c := signIn(t, h, "carol", testPassword)
	if rec := send(t, h, "/account/signature", c, url.Values{"signature": {"Carol's **mark**"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("set signature = %d", rec.Code)
	}
	reply := func(cookies []*http.Cookie, form url.Values) string {
		t.Helper()
		rec := send(t, h, "/t/1/reply", cookies, form)
		id := idOf(t, rec)
		return article(t, topicOf(t, h, id, cookies), id)
	}

	// No grant yet: the new reply shows no signature.
	if art := reply(c, url.Values{"body": {"First reply."}}); strings.Contains(art, `class="signature"`) {
		t.Errorf("a reply without post.signatures shows a signature:\n%s", art)
	}
	// The grant in forum 2: the next reply shows it.
	exec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (3, 2, 'post.signatures', 3)`)
	art := reply(c, url.Values{"body": {"Second reply."}})
	if sigs, _ := signatureDivs(art); len(sigs) != 1 || !strings.Contains(sigs[0], "<strong>mark</strong>") {
		t.Errorf("a reply with post.signatures shows %q", sigs)
	}
	// The grant goes: the old reply keeps its signature, a new one has none.
	exec(t, pool, `update acl_user_grants set setting = 2 where user_id = 3 and option = 'post.signatures'`)
	if art := reply(c, url.Values{"body": {"Third reply."}}); strings.Contains(art, `class="signature"`) {
		t.Errorf("a reply after the right was withdrawn shows a signature:\n%s", art)
	}
	// A guest never shows one, even where the guests group holds the right.
	exec(t, pool, `insert into acl_group_grants (group_id, forum_id, option, setting) select id, 2, 'post.signatures', 3 from groups where builtin = 'guests'`)
	if art := reply(nil, url.Values{"body": {"Guest reply."}, "guest_name": {"Visitor 9"}}); strings.Contains(art, `class="signature"`) {
		t.Errorf("a guest post shows a signature:\n%s", art)
	}
}

// holdSignIn starts a sign-in as name in the background and holds it after the
// password is verified and before the session is inserted. It returns the point
// to wait on, a function that lets the sign-in go on, and a function that waits
// for its answer.
func holdSignIn(t *testing.T, h http.Handler, name string, remember bool) (reached <-chan struct{}, release func(), answer func() (*httptest.ResponseRecorder, []*http.Cookie)) {
	t.Helper()
	cookies, token := csrfFor(t, h)
	form := url.Values{"csrf": {token}, "name": {name}, "password": {testPassword}}
	if remember {
		form.Set("remember", "1")
	}
	hit := make(chan struct{})
	gate := make(chan struct{})
	var once sync.Once
	beforeSession = func() {
		once.Do(func() { close(hit) })
		<-gate
	}
	t.Cleanup(func() { beforeSession = nil })
	type result struct {
		rec *httptest.ResponseRecorder
		jar []*http.Cookie
	}
	done := make(chan result, 1)
	go func() {
		rec := request(t, h, http.MethodPost, "/login", cookies, form)
		done <- result{rec, jar(cookies, rec)}
	}()
	var releaseOnce sync.Once
	release = func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return hit, release, func() (*httptest.ResponseRecorder, []*http.Cookie) {
		select {
		case r := <-done:
			return r.rec, r.jar
		case <-time.After(20 * time.Second):
			t.Fatal("the sign-in never answered")
			return nil, nil
		}
	}
}

func waitFor(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(20 * time.Second):
		t.Fatal("the sign-in never reached the hold")
	}
}

func sessionsOf(t *testing.T, pool *pgxpool.Pool, id int) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), `select count(*) from sessions where user_id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A sign-in that verified the old password and is held before it inserts its
// session must not leave a session behind when the password changes in that
// gap. The member ends with exactly one session: the one from the change.
func TestSignInInFlightDoesNotSurviveAPasswordChange(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})

	// The control: held and released with no change, the sign-in succeeds, so the
	// hold itself alters nothing.
	reached, release, answer := holdSignIn(t, h, "alice", true)
	waitFor(t, reached)
	release()
	if rec, cookies := answer(); rec.Code != http.StatusSeeOther || sessionValue(cookies) == "" {
		t.Fatalf("control sign-in = %d", rec.Code)
	}
	if n := sessionsOf(t, pool, 1); n != 1 {
		t.Fatalf("control: %d sessions", n)
	}
	beforeSession = nil

	// The race: alice is signed in here; a second sign-in (the old password,
	// "remember me") is held; the password changes; the sign-in goes on.
	a := signIn(t, h, "alice", testPassword)
	reached, release, answer = holdSignIn(t, h, "alice", true)
	waitFor(t, reached)
	rec, fresh := postAs(t, h, "/account/password", a, passwordForm(testPassword, newPassword))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("change = %d\n%s", rec.Code, bodyOf(t, rec))
	}
	release()
	late, lateCookies := answer()
	if late.Code != http.StatusOK || !strings.Contains(bodyOf(t, late), "The name or password is wrong.") {
		t.Errorf("the late sign-in = %d, want the wrong-password answer", late.Code)
	}
	if sessionValue(lateCookies) != "" {
		t.Error("the late sign-in set a session cookie")
	}
	if n := sessionsOf(t, pool, 1); n != 1 {
		t.Errorf("alice has %d sessions, want exactly the one from the change", n)
	}
	if rec := getAs(t, h, "/account", fresh); rec.Code != http.StatusOK {
		t.Errorf("the session from the change does not work: %d", rec.Code)
	}
}

// The same gap with a ban written straight to the bans table. The sign-in is
// answered like a wrong password and creates no session.
func TestSignInInFlightDoesNotSurviveABan(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	reached, release, answer := holdSignIn(t, h, "carol", true)
	waitFor(t, reached)
	exec(t, pool, `insert into bans (kind, user_id, reason_shown, created_at) values ('user', 3, 'testing', now())`)
	release()
	rec, cookies := answer()
	page := bodyOf(t, rec)
	if rec.Code != http.StatusOK || !strings.Contains(page, "The name or password is wrong.") {
		t.Errorf("the sign-in of a member banned in the gap = %d", rec.Code)
	}
	if strings.Contains(page, "banned") || strings.Contains(page, "testing") {
		t.Error("the answer names the ban")
	}
	if sessionValue(cookies) != "" || sessionsOf(t, pool, 3) != 0 {
		t.Errorf("a banned member got a session: cookie %q, %d rows", sessionValue(cookies), sessionsOf(t, pool, 3))
	}
}

// After a password change, a ban that lands between the commit and the insert of
// the new session leaves the member signed out: the insert is conditional like a
// sign-in, and the answer sends the member to sign in.
func TestPasswordChangeNewSessionChecksTheBan(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	c := signIn(t, h, "carol", testPassword)
	beforeNewSession = func() {
		exec(t, pool, `insert into bans (kind, user_id, created_at) values ('user', 3, now())`)
	}
	t.Cleanup(func() { beforeNewSession = nil })
	rec := send(t, h, "/account/password", c, passwordForm(testPassword, newPassword))
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Fatalf("change with a ban in the gap = %d %q, want 303 to /login", rec.Code, rec.Header().Get("Location"))
	}
	if v := sessionValue(rec.Result().Cookies()); v != "" {
		t.Errorf("a session cookie %q was set", v)
	}
	if n := sessionsOf(t, pool, 3); n != 0 {
		t.Errorf("carol has %d sessions after the ban, want 0", n)
	}
}

// A member who is gone when the signature form runs gets the bare form with the
// text, 404, and no ERROR line in the log.
func TestSignatureOfAVanishedMember(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	c := signIn(t, h, "carol", testPassword)
	logs := captureLogs(t)
	afterBegin = func() { exec(t, pool, `update users set state = 'inactive' where id = 3`) }
	t.Cleanup(func() { afterBegin = nil })
	text := "My **last** words & <b>more</b>"
	rec := send(t, h, "/account/signature", c, url.Values{"signature": {text}})
	page := bodyOf(t, rec)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404\n%s", rec.Code, page)
	}
	if got, ok := textareaOf(t, page, "body"); !ok || got != text {
		t.Errorf("the bare form holds %q", got)
	}
	if !strings.Contains(page, "no longer available") {
		t.Error("the bare form lacks its notice")
	}
	for _, line := range logs.lines() {
		if strings.Contains(line, "level=ERROR") {
			t.Errorf("an ERROR line: %s", line)
		}
	}
}

// The tail check itself must see what it claims to see: a signature element that
// a stray end tag closes early leaves markup behind it that is not the template's.
func TestSignatureDivsSeesAnEarlyClose(t *testing.T) {
	const ok = `<article><div class="post-main"><div class="signature"><p>fine</p></div>
<p class="post-ip">IP address: <code>10.0.0.1</code></p>
<div class="post-actions"></div></div></article>`
	if sigs, problems := signatureDivs(ok); len(sigs) != 1 || sigs[0] != "<p>fine</p>" || len(problems) != 0 {
		t.Errorf("a well-formed tail: %q, %q", sigs, problems)
	}
	const bare = `<article><div class="post-main"><div class="signature">x</div></div></article>`
	if _, problems := signatureDivs(bare); len(problems) != 0 {
		t.Errorf("a signature at the end of the post: %q", problems)
	}
	const early = `<article><div class="post-main"><div class="signature">x</div><script>alert(1)</script></div></div></article>`
	if _, problems := signatureDivs(early); len(problems) == 0 {
		t.Error("a script after the end of the signature went unseen")
	}
	const stray = `<article><div class="post-main"><div class="signature">x</div></p><b>y</b></div></article>`
	if _, problems := signatureDivs(stray); len(problems) == 0 {
		t.Error("a stray end tag after the signature went unseen")
	}
}

const secondPassword = "the second pass phrase"

// A password change whose check of the current password read an older epoch is
// refused: it answers as a wrong current password and changes nothing. Here a
// second browser of the same member changes the password in the gap between the
// check and the write. The late change must not overwrite it.
func TestPasswordChangeWithAnOlderEpochIsRefused(t *testing.T) {
	h, _ := siteWithPool(t, postingBoard(), Options{})
	first := signIn(t, h, "alice", testPassword)
	second := signIn(t, h, "alice", testPassword)
	beforeHold = func() {
		beforeHold = nil
		if rec := send(t, h, "/account/password", second, passwordForm(testPassword, secondPassword)); rec.Code != http.StatusSeeOther {
			t.Errorf("the change in the gap = %d", rec.Code)
		}
	}
	t.Cleanup(func() { beforeHold = nil })
	rec := send(t, h, "/account/password", first, passwordForm(testPassword, newPassword))
	if rec.Code != http.StatusBadRequest || !strings.Contains(bodyOf(t, rec), "That is not your current password.") {
		t.Fatalf("the late change = %d, want 400 as a wrong current password", rec.Code)
	}
	// The password of the change in the gap stands, and the late one never took.
	if sessionValue(signIn(t, h, "alice", secondPassword)) == "" {
		t.Error("the password of the first change no longer works")
	}
	if rec, _ := postAs(t, h, "/login", nil, url.Values{"name": {"alice"}, "password": {newPassword}}); rec.Code == http.StatusSeeOther {
		t.Error("the late change took the password")
	}
}

// The same rule for the email change.
func TestEmailChangeWithAnOlderEpochIsRefused(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	first := signIn(t, h, "alice", testPassword)
	second := signIn(t, h, "alice", testPassword)
	before := userCol(t, pool, 1, "email")
	beforeEmailChange = func() {
		beforeEmailChange = nil
		if rec := send(t, h, "/account/password", second, passwordForm(testPassword, secondPassword)); rec.Code != http.StatusSeeOther {
			t.Errorf("the change in the gap = %d", rec.Code)
		}
	}
	t.Cleanup(func() { beforeEmailChange = nil })
	rec := send(t, h, "/account/email", first, url.Values{"email": {"late@example.org"}, "current": {testPassword}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(bodyOf(t, rec), "That is not your current password.") {
		t.Fatalf("the late email change = %d, want 400 as a wrong current password", rec.Code)
	}
	if got := userCol(t, pool, 1, "email"); got != before {
		t.Errorf("the email is %q, want it unchanged (%q)", got, before)
	}
}

// A sign-in in flight on a legacy hash (bob's), held after the rehash, does not
// survive a password change either: the rehash leaves the epoch, and the change
// bumps it.
func TestLegacySignInInFlightDoesNotSurviveAPasswordChange(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	b := signIn(t, h, "bob", testPassword)
	reached, release, answer := holdSignIn(t, h, "bob", false)
	waitFor(t, reached)
	if rec := send(t, h, "/account/password", b, passwordForm(testPassword, newPassword)); rec.Code != http.StatusSeeOther {
		t.Fatalf("change = %d", rec.Code)
	}
	release()
	late, cookies := answer()
	if late.Code != http.StatusOK || !strings.Contains(bodyOf(t, late), "The name or password is wrong.") || sessionValue(cookies) != "" {
		t.Errorf("the late sign-in = %d", late.Code)
	}
	if n := sessionsOf(t, pool, 2); n != 1 {
		t.Errorf("bob has %d sessions, want exactly the one from the change", n)
	}
}

// A sign-in that verified the password and is held before it inserts its session
// gets no session when an administrator resets the password in that gap. The
// reset adds one to the epoch, so the held sign-in is refused like a wrong
// password. Alice ends with no session at all.
func TestSignInInFlightDoesNotSurviveAReset(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   int
	}{{"gobb hash", 1}, {"legacy hash", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			h, pool := siteWithPool(t, postingBoard(), Options{})
			reached, release, answer := holdSignIn(t, h, map[int]string{1: "alice", 2: "bob"}[tc.id], true)
			waitFor(t, reached)
			if _, err := board.ResetPassword(t.Context(), pool, int64(tc.id), time.Now()); err != nil {
				t.Fatal(err)
			}
			release()
			rec, cookies := answer()
			if rec.Code != http.StatusOK || !strings.Contains(bodyOf(t, rec), "The name or password is wrong.") {
				t.Errorf("the sign-in held across a reset = %d, want the wrong-password answer", rec.Code)
			}
			if sessionValue(cookies) != "" || sessionsOf(t, pool, tc.id) != 0 {
				t.Errorf("a session survived the reset: cookie %q, %d rows", sessionValue(cookies), sessionsOf(t, pool, tc.id))
			}
		})
	}
}

// The same gap with a ban from the command-line write. The sign-in answers like a
// wrong password, and no session remains.
func TestSignInInFlightDoesNotSurviveABanCommand(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	reached, release, answer := holdSignIn(t, h, "carol", true)
	waitFor(t, reached)
	if err := board.BanUser(t.Context(), pool, 3, "testing", time.Now()); err != nil {
		t.Fatal(err)
	}
	release()
	rec, cookies := answer()
	page := bodyOf(t, rec)
	if rec.Code != http.StatusOK || !strings.Contains(page, "The name or password is wrong.") || strings.Contains(page, "banned") {
		t.Errorf("the sign-in held across a ban = %d", rec.Code)
	}
	if sessionValue(cookies) != "" || sessionsOf(t, pool, 3) != 0 {
		t.Errorf("a session survived the ban: cookie %q, %d rows", sessionValue(cookies), sessionsOf(t, pool, 3))
	}
}
