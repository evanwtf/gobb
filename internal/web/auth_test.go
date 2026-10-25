package web

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/password"
	"github.com/evanwtf/gobb/internal/session"
)

const testPassword = "example-password"

// siteAndPool is newTestSite with the pool, for a test that reads the tables.
func siteAndPool(t *testing.T, b *bundletest.Board, opt Options) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	pool, dataDir := boardtest.Load(t, b)
	opt.DataDir = dataDir
	if opt.LoginFloor == 0 {
		opt.LoginFloor = time.Millisecond
	}
	h, err := New(t.Context(), pool, opt)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	return h, pool
}

// cookieNamed returns the cookie that rec sets under name, or nil.
func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// rawSetCookie returns the Set-Cookie header lines of rec that start with name=.
func rawSetCookie(rec *httptest.ResponseRecorder, name string) string {
	for _, line := range rec.Header().Values("Set-Cookie") {
		if strings.HasPrefix(line, name+"=") {
			return line
		}
	}
	return ""
}

func countRows(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSignInFlow(t *testing.T) {
	h, pool := siteAndPool(t, bundletest.Small(), Options{CookieSecure: true})

	// The sign-in page sets the cookie of the guest's token and holds the form.
	page := get(t, h, "/login")
	if page.Code != http.StatusOK {
		t.Fatalf("GET /login = %d", page.Code)
	}
	raw := rawSetCookie(page, "gobb_csrf")
	for _, want := range []string{"HttpOnly", "SameSite=Lax", "Secure", "Path=/"} {
		if !strings.Contains(raw, want) {
			t.Errorf("gobb_csrf cookie %q lacks %s", raw, want)
		}
	}
	if strings.Contains(raw, "Max-Age") {
		t.Errorf("gobb_csrf cookie %q has a lifetime", raw)
	}
	body := bodyOf(t, page)
	for _, want := range []string{`action="/login"`, `name="name"`, `name="password"`, `name="remember"`, `name="next"`, `name="csrf"`, `type="password"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the sign-in page lacks %s", want)
		}
	}
	if page.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q on the sign-in page", page.Header().Get("Cache-Control"))
	}

	// Sign in without "Remember me": a session cookie.
	rec, cookies := postAs(t, h, "/login", nil, url.Values{"name": {"alice"}, "password": {testPassword}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("POST /login = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	raw = rawSetCookie(rec, "gobb_session")
	for _, want := range []string{"HttpOnly", "SameSite=Lax", "Secure", "Path=/"} {
		if !strings.Contains(raw, want) {
			t.Errorf("gobb_session cookie %q lacks %s", raw, want)
		}
	}
	if strings.Contains(raw, "Max-Age") || strings.Contains(raw, "Expires") {
		t.Errorf("gobb_session cookie %q has a lifetime without Remember me", raw)
	}
	if n := countRows(t, pool, `select count(*) from sessions where user_id = 1 and not remember`); n != 1 {
		t.Errorf("%d sessions for alice, want 1", n)
	}

	home := bodyOf(t, getAs(t, h, "/", cookies))
	if !strings.Contains(home, "Signed in as") || !strings.Contains(home, "<bdi>alice</bdi>") {
		t.Errorf("the index for alice lacks the signed-in marker:\n%s", home)
	}
	if strings.Contains(home, `href="/login`) {
		t.Error("the index for a member still offers to sign in")
	}

	// "Remember me" adds a year.
	rec, _ = postAs(t, h, "/login", nil, url.Values{"name": {"bob"}, "password": {testPassword}, "remember": {"1"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("remember sign-in = %d", rec.Code)
	}
	if raw := rawSetCookie(rec, "gobb_session"); !strings.Contains(raw, "Max-Age=31536000") {
		t.Errorf("gobb_session cookie %q lacks Max-Age=31536000", raw)
	}
	if n := countRows(t, pool, `select count(*) from sessions where user_id = 2 and remember`); n != 1 {
		t.Errorf("%d remembered sessions for bob, want 1", n)
	}
	if n := countRows(t, pool, `select count(*) from user_ips where user_id in (1, 2)`); n != 2 {
		t.Errorf("%d user_ips rows, want 2", n)
	}

	// Sign out deletes the row and expires the cookie.
	rec, after := postAs(t, h, "/logout", cookies, url.Values{})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("POST /logout = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if c := cookieNamed(rec, "gobb_session"); c == nil || c.MaxAge >= 0 || c.Value != "" {
		t.Errorf("logout cookie = %+v, want an expired cookie", c)
	}
	if n := countRows(t, pool, `select count(*) from sessions where user_id = 1`); n != 0 {
		t.Errorf("%d sessions for alice after sign-out, want 0", n)
	}
	if strings.Contains(bodyOf(t, getAs(t, h, "/", after)), "Signed in as") {
		t.Error("the index still shows alice after sign-out")
	}
	// The old cookie is worth nothing.
	if strings.Contains(bodyOf(t, getAs(t, h, "/", cookies)), "Signed in as") {
		t.Error("the old session cookie still works")
	}
}

// headerParts splits the header of a page into the part above the nav (the
// brand and the member's links) and the nav. It fails the test when the markup
// has no nav.
func headerParts(t *testing.T, page string) (top, nav string) {
	t.Helper()
	start := strings.Index(page, `<header class="site-header">`)
	i := strings.Index(page, `<nav class="site-nav"`)
	end := strings.Index(page, `</header>`)
	if start < 0 || i < start || end < i {
		t.Fatalf("the page has no header with a nav:\n%s", page)
	}
	return page[start:i], page[i:end]
}

// The member's links sit in the header row, at the right of the board name,
// and not in the nav.
func TestHeaderUser(t *testing.T) {
	h, _ := siteAndPool(t, bundletest.Small(), Options{})
	cookies := signIn(t, h, "alice", testPassword)

	// A member: the name links to the account page, and Sign out is there.
	top, nav := headerParts(t, bodyOf(t, getAs(t, h, "/", cookies)))
	user := regexp.MustCompile(`(?s)<div class="header-user">.*?</div>`).FindString(top)
	if user == "" {
		t.Fatalf("the header row of a member has no .header-user:\n%s", top)
	}
	if !strings.Contains(user, `Signed in as <a href="/account"><bdi>alice</bdi></a>`) {
		t.Errorf("the name of a member is no link to the account page:\n%s", user)
	}
	if strings.Contains(user, "aria-current") {
		t.Errorf("the link to the account page is current on the index:\n%s", user)
	}
	if !strings.Contains(user, `action="/logout"`) || !strings.Contains(user, `name="csrf"`) || !strings.Contains(user, ">Sign out</button>") {
		t.Errorf("the Sign out form is not in .header-user:\n%s", user)
	}
	for _, gone := range []string{"Signed in as", "Sign out", `action="/logout"`, "nav-who", "nav-end", "nav-out"} {
		if strings.Contains(nav, gone) {
			t.Errorf("the nav of a member still holds %q", gone)
		}
	}

	// On the account page the name link is the current page.
	top, _ = headerParts(t, bodyOf(t, getAs(t, h, "/account", cookies)))
	if !strings.Contains(top, `<a href="/account" aria-current="page"><bdi>alice</bdi></a>`) {
		t.Errorf("the name link lacks aria-current on /account:\n%s", top)
	}

	// A guest: the Sign in link is in .header-user, and the nav holds none.
	top, nav = headerParts(t, bodyOf(t, get(t, h, "/")))
	user = regexp.MustCompile(`(?s)<div class="header-user">.*?</div>`).FindString(top)
	if !strings.Contains(user, `>Sign in</a>`) {
		t.Errorf("the header row of a guest has no Sign in link:\n%s", top)
	}
	if strings.Contains(user, "Signed in as") || strings.Contains(user, "Sign out") {
		t.Errorf("the header row of a guest shows a member's links:\n%s", user)
	}
	for _, gone := range []string{"Sign in", "Signed in as", "nav-end"} {
		if strings.Contains(nav, gone) {
			t.Errorf("the nav of a guest still holds %q", gone)
		}
	}

	// The sign-in page itself offers no link to itself.
	top, _ = headerParts(t, bodyOf(t, get(t, h, "/login")))
	if strings.Contains(top, "Sign in</a>") {
		t.Errorf("the sign-in page links to itself:\n%s", top)
	}
}

// Without the Secure flag the cookie works on http://127.0.0.1, which is how
// the owner checks a private copy. This test uses a real server, a cookie jar
// and the headers a browser sends with a same-origin form post.
func TestSignInOverPlainHTTP(t *testing.T) {
	h, _ := siteAndPool(t, bundletest.Small(), Options{CookieSecure: false})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil || base.Hostname() != "127.0.0.1" {
		t.Fatalf("server URL %q: %v", srv.URL, err)
	}

	for _, mode := range []string{"fetch-metadata", "origin-only"} {
		t.Run(mode, func(t *testing.T) {
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Jar: jar}
			resp, err := client.Get(srv.URL + "/login")
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range resp.Header.Values("Set-Cookie") {
				if strings.Contains(line, "Secure") {
					t.Errorf("Set-Cookie %q is Secure on plain HTTP", line)
				}
			}
			page, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			m := csrfField.FindStringSubmatch(string(page))
			if m == nil {
				t.Fatal("no csrf field")
			}

			form := url.Values{"name": {"alice"}, "password": {testPassword}, "csrf": {m[1]}, "next": {"/t/1"}}
			req, err := http.NewRequest(http.MethodPost, srv.URL+"/login", strings.NewReader(form.Encode()))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", srv.URL)
			if mode == "fetch-metadata" {
				req.Header.Set("Sec-Fetch-Site", "same-origin")
			}
			resp, err = client.Do(req) // follows the 303 to /t/1
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/t/1" {
				t.Fatalf("after sign-in: %d at %s", resp.StatusCode, resp.Request.URL)
			}
			if !strings.Contains(string(got), "<bdi>alice</bdi>") || !strings.Contains(string(got), "Signed in as") {
				t.Error("the page after sign-in does not show alice")
			}
			var haveSession bool
			for _, c := range jar.Cookies(base) {
				haveSession = haveSession || c.Name == "gobb_session"
			}
			if !haveSession {
				t.Error("the cookie jar did not keep the session cookie")
			}
		})
	}
}

func TestPostNeedsCSRF(t *testing.T) {
	h, pool := siteAndPool(t, bundletest.Small(), Options{})
	form := func() url.Values { return url.Values{"name": {"alice"}, "password": {testPassword}} }

	// A guest's cookies and token to build the cases from.
	page := get(t, h, "/login")
	guest := jar(nil, page)
	token := csrfField.FindStringSubmatch(bodyOf(t, page))[1]
	// The token of another visitor.
	other := csrfField.FindStringSubmatch(bodyOf(t, get(t, h, "/login")))[1]

	post := func(name string, cookies []*http.Cookie, f url.Values, header map[string]string, want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(f.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range header {
			r.Header.Set(k, v)
		}
		for _, c := range cookies {
			r.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != want {
			t.Errorf("%s: status %d, want %d", name, rec.Code, want)
		}
		if want == http.StatusForbidden && cookieNamed(rec, "gobb_session") != nil {
			t.Errorf("%s: the refused post set a session cookie", name)
		}
	}
	valid := form()
	valid.Set("csrf", token)
	post("no csrf field", guest, form(), nil, http.StatusForbidden)
	wrong := form()
	wrong.Set("csrf", "AAAA")
	post("wrong csrf", guest, wrong, nil, http.StatusForbidden)
	stolen := form()
	stolen.Set("csrf", other)
	post("another visitor's csrf", guest, stolen, nil, http.StatusForbidden)
	post("no cookie", nil, valid, nil, http.StatusForbidden)
	post("cross-site fetch metadata", guest, valid, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, http.StatusForbidden)
	post("cross-site origin, no fetch metadata", guest, valid, map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden)
	post("same-site is not same-origin", guest, valid, map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden)
	post("a good post", guest, valid, map[string]string{"Origin": "http://example.com", "Sec-Fetch-Site": "same-origin"}, http.StatusSeeOther)

	if n := countRows(t, pool, `select count(*) from sessions`); n != 1 {
		t.Errorf("%d sessions after the cases, want 1 (only the good post)", n)
	}

	// The other POST routes need the token too. Sign-out needs it from a member
	// with a live session; for anyone else it has nothing to protect
	// (TestFormsAfterTheSessionEnded).
	member := signIn(t, h, "bob", testPassword)
	for target, who := range map[string][]*http.Cookie{"/logout": member, "/scheme": guest} {
		for name, f := range map[string]url.Values{"none": {"scheme": {"slate-light"}}, "wrong": {"scheme": {"slate-light"}, "csrf": {"AAAA"}}} {
			rec := request(t, h, http.MethodPost, target, who, f)
			if rec.Code != http.StatusForbidden {
				t.Errorf("POST %s with %s csrf = %d, want 403", target, name, rec.Code)
			}
		}
	}
}

func TestSignInErrors(t *testing.T) {
	b := bundletest.Small()
	h, pool := siteAndPool(t, b, Options{})
	// carol shares bob's address. dave gets a password and an active state.
	if _, err := pool.Exec(t.Context(), `update users set email = 'bob@example.com', email_clean = 'bob@example.com' where id = 3`); err != nil {
		t.Fatal(err)
	}

	try := func(name, pass string) (*httptest.ResponseRecorder, string) {
		t.Helper()
		rec, _ := postAs(t, h, "/login", nil, url.Values{"name": {name}, "password": {pass}, "next": {"/t/1"}})
		if cookieNamed(rec, "gobb_session") != nil {
			t.Errorf("%q: a failed sign-in set a session cookie", name)
		}
		return rec, bodyOf(t, rec)
	}

	rec, body := try("alice", "wrong <b>")
	if rec.Code != http.StatusOK || !strings.Contains(body, "The name or password is wrong.") {
		t.Errorf("wrong password: %d\n%s", rec.Code, body)
	}
	if !strings.Contains(body, `value="alice"`) {
		t.Error("the form lost the name")
	}
	if strings.Contains(body, "wrong &lt;b&gt;") || strings.Contains(body, "wrong <b>") {
		t.Error("the page echoes the password")
	}
	if !strings.Contains(body, `value="/t/1"`) {
		t.Error("the form lost next")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}

	rec, body = try("nobody", testPassword)
	if rec.Code != http.StatusOK || !strings.Contains(body, "The name or password is wrong.") {
		t.Errorf("unknown name: %d", rec.Code)
	}
	rec, body = try("bob@example.com", testPassword)
	if rec.Code != http.StatusOK || !strings.Contains(body, "This email address belongs to more than one account. Sign in with your username.") {
		t.Errorf("shared email: %d\n%s", rec.Code, body)
	}
	rec, body = try("bob@example.com", "wrong")
	if !strings.Contains(body, "The name or password is wrong.") {
		t.Errorf("shared email, wrong password: %d", rec.Code)
	}

	// An inactive account with the right password.
	hash := hashFor(t, "dave-password")
	if _, err := pool.Exec(t.Context(), `update users set password_hash = $1, password_scheme = 'gobb' where id = 4`, hash); err != nil {
		t.Fatal(err)
	}
	rec, body = try("dave", "dave-password")
	if rec.Code != http.StatusForbidden || !strings.Contains(body, "This account is not active.") {
		t.Errorf("inactive: %d\n%s", rec.Code, body)
	}
	// A banned account.
	if _, err := pool.Exec(t.Context(), `update users set state = 'active' where id = 4`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `update bans set ends_at = null where kind = 'user'`); err != nil {
		t.Fatal(err)
	}
	rec, body = try("dave", "dave-password")
	if rec.Code != http.StatusForbidden || !strings.Contains(body, "banned") || !strings.Contains(body, "Rule 1") {
		t.Errorf("banned: %d\n%s", rec.Code, body)
	}
}

func TestSignInNext(t *testing.T) {
	for _, c := range []struct{ base, next, want string }{
		{"/", "/t/1", "/t/1"},
		{"/", "/t/1?page=2#p9", "/t/1?page=2#p9"},
		{"/", "", "/"},
		{"/", "//evil.example/x", "/"},
		{"/", "/\\evil.example", "/"},
		{"/", "https://evil.example/", "/"},
		{"/", "javascript:alert(1)", "/"},
		{"/", "t/1", "/"},
		{"/", "/t/1\r\nX: y", "/"},
		{"/", "/login", "/"},
		{"/", "/login?next=/login", "/"},
		{"/forum/", "/forum/t/1", "/forum/t/1"},
		{"/forum/", "/t/1", "/forum/"},
		{"/forum/", "/forumx/t/1", "/forum/"},
		{"/forum/", "/forum/login", "/forum/"},
		// Dot segments cannot take the visitor out of the base path.
		{"/forum/", "/forum/../x", "/forum/"},
		{"/forum/", "/forum/..//evil.example", "/forum/"},
		{"/forum/", "/forum/%2e%2e/x", "/forum/"},
		{"/forum/", "/forum/%2E%2E/%2e%2e/x", "/forum/"},
		{"/forum/", "/forum/t/../../x", "/forum/"},
		{"/forum/", "/forum/%5Cevil.example", "/forum/"},
		{"/forum/", "/forum/./t/1", "/forum/t/1"},
		{"/forum/", "/forum/t/../t/1?x=1#a", "/forum/t/1?x=1#a"},
		{"/forum/", "/forum/./", "/forum/"},
		{"/", "/a/../t/1", "/t/1"},
		{"/", "/../../t/1", "/t/1"},
	} {
		h, _ := siteAndPool(t, bundletest.Small(), Options{BasePath: c.base})
		login := strings.TrimSuffix(c.base, "/") + "/login"
		rec, _ := postAsAt(t, h, c.base, login, nil, url.Values{"name": {"alice"}, "password": {testPassword}, "next": {c.next}})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != c.want {
			t.Errorf("base %s next %q: %d to %q, want %q", c.base, c.next, rec.Code, rec.Header().Get("Location"), c.want)
		}
	}

	// The form carries a safe next, and only that.
	h, _ := smallSite(t)
	body := bodyOf(t, get(t, h, "/login?next=/t/1"))
	if !strings.Contains(body, `name="next" value="/t/1"`) {
		t.Error("the form lost next")
	}
	body = bodyOf(t, get(t, h, "/login?next=//evil.example"))
	if strings.Contains(body, "evil.example") {
		t.Error("the form carries an unsafe next")
	}
	// A member who opens the sign-in page goes on to next at once.
	cookies := signIn(t, h, "alice", testPassword)
	rec := getAs(t, h, "/login?next=/t/1", cookies)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/t/1" {
		t.Errorf("GET /login as a member = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = getAs(t, h, "/login?next=/login", cookies)
	if rec.Header().Get("Location") != "/" {
		t.Errorf("GET /login?next=/login as a member goes to %q, want /", rec.Header().Get("Location"))
	}
}

// The sign-in link of a guest carries the page the guest came from.
func TestGuestNav(t *testing.T) {
	h, _ := smallSite(t)
	body := bodyOf(t, get(t, h, "/t/1?page=1&scheme=slate-light"))
	if !strings.Contains(body, `href="/login?next=%2Ft%2F1%3Fpage%3D1"`) {
		t.Errorf("the sign-in link lacks the return path:\n%s", body)
	}
	if strings.Contains(body, "Signed in as") || strings.Contains(body, `action="/logout"`) {
		t.Error("a guest sees the member's controls")
	}
	// The sign-in page does not link to itself with itself as next.
	body = bodyOf(t, get(t, h, "/login"))
	if strings.Contains(body, "next=%2Flogin") {
		t.Error("the sign-in page links to itself as next")
	}
}

// A guest gets the csrf cookie once, and the token in the page matches it.
func TestGuestCSRFCookie(t *testing.T) {
	h, _ := smallSite(t)
	first := get(t, h, "/")
	c := cookieNamed(first, "gobb_csrf")
	if c == nil || len(c.Value) != 43 {
		t.Fatalf("gobb_csrf = %+v, want 32 bytes in base64url", c)
	}
	want := session.CSRFToken(c.Value)
	if got := csrfField.FindStringSubmatch(bodyOf(t, first)); got == nil || got[1] != want {
		t.Errorf("the token in the page does not match the cookie")
	}
	second := getAs(t, h, "/", []*http.Cookie{{Name: "gobb_csrf", Value: c.Value}})
	if cookieNamed(second, "gobb_csrf") != nil {
		t.Error("the cookie is set again though the guest has it")
	}
	if got := csrfField.FindStringSubmatch(bodyOf(t, second)); got == nil || got[1] != want {
		t.Error("the token changed between pages")
	}
	// Two guests with no cookie get different secrets.
	other := cookieNamed(get(t, h, "/"), "gobb_csrf")
	if other == nil || other.Value == c.Value {
		t.Error("two guests share a csrf secret")
	}
	// Static files and the health check set no cookie.
	for _, p := range []string{"/static/gobb.css", "/healthz"} {
		if rec := get(t, h, p); len(rec.Header().Values("Set-Cookie")) != 0 {
			t.Errorf("%s sets a cookie", p)
		}
	}
	// A member's token comes from the session.
	cookies := signIn(t, h, "alice", testPassword)
	var token string
	for _, c := range cookies {
		if c.Name == "gobb_session" {
			token = c.Value
		}
	}
	body := bodyOf(t, getAs(t, h, "/", cookies))
	if got := csrfField.FindStringSubmatch(body); got == nil || got[1] != session.CSRFToken(token) {
		t.Error("a member's token is not derived from the session token")
	}
}

// A session that has expired, or whose user is gone or inactive, is a guest.
func TestSessionEnds(t *testing.T) {
	var offset atomic.Int64
	start := time.Now()
	h, pool := siteAndPool(t, bundletest.Small(), Options{Now: func() time.Time { return start.Add(time.Duration(offset.Load())) }})
	cookies := signIn(t, h, "alice", testPassword)
	signedIn := func(c []*http.Cookie) bool {
		return strings.Contains(bodyOf(t, getAs(t, h, "/", c)), "Signed in as")
	}
	if !signedIn(cookies) {
		t.Fatal("not signed in")
	}
	// An inactive user.
	if _, err := pool.Exec(t.Context(), `update users set state = 'inactive' where id = 1`); err != nil {
		t.Fatal(err)
	}
	if signedIn(cookies) {
		t.Error("an inactive user is still signed in")
	}
	// The page deleted the dead session, so a later change of state does not bring it back.
	if _, err := pool.Exec(t.Context(), `update users set state = 'active' where id = 1`); err != nil {
		t.Fatal(err)
	}
	if signedIn(cookies) {
		t.Error("a session that was dead came back to life")
	}
	cookies = signIn(t, h, "alice", testPassword)
	if !signedIn(cookies) {
		t.Fatal("not signed in again")
	}
	// A day passes without a request: the session ends. The page also clears the cookie.
	offset.Store(int64(25 * time.Hour))
	rec := getAs(t, h, "/", cookies)
	if strings.Contains(bodyOf(t, rec), "Signed in as") {
		t.Error("a session of 25 hours is still signed in")
	}
	if c := cookieNamed(rec, "gobb_session"); c == nil || c.MaxAge >= 0 {
		t.Errorf("the dead session cookie was not cleared: %+v", c)
	}
}

// A request touches the session at most every five minutes. A remembered
// session also gets its cookie again, so that the year starts over.
func TestSessionSlides(t *testing.T) {
	var offset atomic.Int64
	start := time.Now()
	h, pool := siteAndPool(t, bundletest.Small(), Options{Now: func() time.Time { return start.Add(time.Duration(offset.Load())) }})
	plain := signIn(t, h, "alice", testPassword)
	rec, remembered := postAs(t, h, "/login", nil, url.Values{"name": {"bob"}, "password": {testPassword}, "remember": {"1"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("sign-in = %d", rec.Code)
	}

	offset.Store(int64(time.Minute))
	if rec := getAs(t, h, "/", remembered); cookieNamed(rec, "gobb_session") != nil {
		t.Error("the cookie was set again after a minute")
	}
	lastSeen := func(user int64) time.Time {
		var ts time.Time
		if err := pool.QueryRow(t.Context(), `select last_seen_at from sessions where user_id = $1`, user).Scan(&ts); err != nil {
			t.Fatal(err)
		}
		return ts
	}
	before := lastSeen(1)

	offset.Store(int64(time.Hour))
	if strings.Contains(bodyOf(t, getAs(t, h, "/", plain)), "Signed in as") == false {
		t.Error("alice is signed out after an hour")
	}
	if !lastSeen(1).After(before) {
		t.Error("last_seen_at did not move after an hour")
	}
	rec = getAs(t, h, "/", remembered)
	if c := cookieNamed(rec, "gobb_session"); c == nil || c.MaxAge != 365*24*3600 {
		t.Errorf("remembered cookie after an hour = %+v, want Max-Age 365 days", c)
	}
	rec = getAs(t, h, "/", plain)
	if cookieNamed(rec, "gobb_session") != nil {
		t.Error("a session cookie without Remember me was set again")
	}
}

// board.Member is what the web layer builds a member's viewer from.
func TestBoardMemberUsed(t *testing.T) {
	h, pool := siteAndPool(t, bundletest.Small(), Options{})
	cookies := signIn(t, h, "bob", testPassword)
	settings, err := board.LoadSettings(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	v, err := board.Member(t.Context(), pool, 2, settings)
	if err != nil || v.UserID != 2 || v.Name != "bob" || v.Scheme != "slate-deep" || v.Loc.String() != "Europe/London" {
		t.Errorf("Member = %+v, %v", v, err)
	}
	if !strings.Contains(bodyOf(t, getAs(t, h, "/", cookies)), `data-scheme="slate-deep"`) {
		t.Error("bob's page is not in his scheme")
	}
}

// hashFor returns a gobb hash of pw.
func hashFor(t *testing.T, pw string) string {
	t.Helper()
	hash, err := password.Hash(pw)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

// A member whose account turns inactive after sign-in is a guest at once, on
// every path: the page, and each POST with the guest's own token. The dead
// cookie is cleared and its row is deleted. Before the fix, the page showed a
// guest but the POSTs still used the session token as the secret, so every one
// of them answered 403 for as long as the cookie lived.
func TestInactiveMemberIsAGuestEverywhere(t *testing.T) {
	h, pool := siteAndPool(t, bundletest.Small(), Options{})
	ctx := t.Context()

	// deadBob signs bob in, then makes his account inactive. The page has not
	// seen the change: the row is still there, and so are the old cookies.
	deadBob := func(t *testing.T) (cookies []*http.Cookie, guestToken string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `update users set state = 'active' where id = 2`); err != nil {
			t.Fatal(err)
		}
		cookies = signIn(t, h, "bob", testPassword)
		if n := countRows(t, pool, `select count(*) from sessions where user_id = 2`); n != 1 {
			t.Fatalf("%d sessions for bob, want 1", n)
		}
		if _, err := pool.Exec(ctx, `update users set state = 'inactive' where id = 2`); err != nil {
			t.Fatal(err)
		}
		for _, c := range cookies {
			if c.Name == "gobb_csrf" {
				guestToken = session.CSRFToken(c.Value)
			}
		}
		if guestToken == "" {
			t.Fatal("no guest csrf cookie")
		}
		return cookies, guestToken
	}
	// sessionCookies returns the gobb_session lines that rec sets, in order.
	sessionCookies := func(rec *httptest.ResponseRecorder) (out []*http.Cookie) {
		for _, c := range rec.Result().Cookies() {
			if c.Name == "gobb_session" {
				out = append(out, c)
			}
		}
		return out
	}

	t.Run("page", func(t *testing.T) {
		cookies, _ := deadBob(t)
		rec := getAs(t, h, "/", cookies)
		if strings.Contains(bodyOf(t, rec), "Signed in as") {
			t.Error("an inactive member still sees the member page")
		}
		if cs := sessionCookies(rec); len(cs) != 1 || cs[0].MaxAge >= 0 {
			t.Errorf("the dead cookie was not cleared: %+v", cs)
		}
		if n := countRows(t, pool, `select count(*) from sessions where user_id = 2`); n != 0 {
			t.Errorf("%d sessions for bob after the page, want 0", n)
		}
	})

	t.Run("sign in as another user", func(t *testing.T) {
		cookies, token := deadBob(t)
		form := url.Values{"name": {"alice"}, "password": {testPassword}, "csrf": {token}}
		rec := request(t, h, http.MethodPost, "/login", cookies, form)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("POST /login = %d, want 303", rec.Code)
		}
		cs := sessionCookies(rec)
		if len(cs) != 1 || cs[0].Value == "" || cs[0].MaxAge < 0 {
			t.Errorf("want one new session cookie and no clear: %+v", cs)
		}
		if n := countRows(t, pool, `select count(*) from sessions where user_id = 2`); n != 0 {
			t.Errorf("%d sessions for bob after the sign-in, want 0", n)
		}
		if n := countRows(t, pool, `select count(*) from sessions where user_id = 1`); n != 1 {
			t.Errorf("%d sessions for alice, want 1", n)
		}
	})

	t.Run("sign out", func(t *testing.T) {
		cookies, token := deadBob(t)
		rec := request(t, h, http.MethodPost, "/logout", cookies, url.Values{"csrf": {token}})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("POST /logout = %d, want 303", rec.Code)
		}
		if cs := sessionCookies(rec); len(cs) != 1 || cs[0].MaxAge >= 0 {
			t.Errorf("the dead cookie was not cleared: %+v", cs)
		}
		if n := countRows(t, pool, `select count(*) from sessions where user_id = 2`); n != 0 {
			t.Errorf("%d sessions for bob after sign-out, want 0", n)
		}
	})

	t.Run("scheme", func(t *testing.T) {
		cookies, token := deadBob(t)
		rec := request(t, h, http.MethodPost, "/scheme", cookies, url.Values{"csrf": {token}, "scheme": {"slate-light"}})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("POST /scheme = %d, want 303", rec.Code)
		}
		if cs := sessionCookies(rec); len(cs) != 1 || cs[0].MaxAge >= 0 {
			t.Errorf("the dead cookie was not cleared: %+v", cs)
		}
		// He is a guest now: the choice is a cookie, and his row is not touched.
		if c := cookieNamed(rec, "gobb_scheme"); c == nil || c.Value != "slate-light" {
			t.Errorf("gobb_scheme = %+v", c)
		}
		var scheme string
		if err := pool.QueryRow(ctx, `select color_scheme from users where id = 2`).Scan(&scheme); err != nil || scheme != "slate-deep" {
			t.Errorf("bob's scheme = %q (%v), want slate-deep", scheme, err)
		}
	})

	t.Run("the session token is not the secret", func(t *testing.T) {
		cookies, _ := deadBob(t)
		var token string
		for _, c := range cookies {
			if c.Name == "gobb_session" {
				token = session.CSRFToken(c.Value)
			}
		}
		rec := request(t, h, http.MethodPost, "/scheme", cookies, url.Values{"csrf": {token}, "scheme": {"slate-light"}})
		if rec.Code != http.StatusForbidden {
			t.Errorf("a token made from the dead session = %d, want 403", rec.Code)
		}
		var scheme string
		if err := pool.QueryRow(ctx, `select color_scheme from users where id = 2`).Scan(&scheme); err != nil || scheme != "slate-deep" {
			t.Errorf("bob's scheme = %q (%v), want slate-deep", scheme, err)
		}
		// Sign-out has nothing to protect once the session is dead: 303.
		rec = request(t, h, http.MethodPost, "/logout", cookies, url.Values{"csrf": {token}})
		if rec.Code != http.StatusSeeOther {
			t.Errorf("sign-out with a dead session = %d, want 303", rec.Code)
		}
	})
}

// A form of a member's page sent after the session ended. "Sign out" has
// nothing left to protect: it answers 303 to the index and clears the cookie.
// Any other form answers 403 and says that the session ended, not "reload and
// try again". A live member's form with a wrong token, and a guest's, still get
// the plain refusal.
func TestFormsAfterTheSessionEnded(t *testing.T) {
	h, pool := siteAndPool(t, bundletest.Small(), Options{})
	ctx := t.Context()
	// ended signs alice in, reads the token of her page, then ends the session.
	ended := func(t *testing.T) ([]*http.Cookie, string) {
		t.Helper()
		cookies := signIn(t, h, "alice", testPassword)
		m := csrfField.FindStringSubmatch(bodyOf(t, getAs(t, h, "/", cookies)))
		if m == nil {
			t.Fatal("the member page has no csrf field")
		}
		if _, err := pool.Exec(ctx, `update sessions set expires_at = now() - interval '1 minute' where user_id = 1`); err != nil {
			t.Fatal(err)
		}
		return cookies, m[1]
	}
	const endedText = "Your session ended. You are signed out."

	cookies, token := ended(t)
	rec := request(t, h, http.MethodPost, "/logout", cookies, url.Values{"csrf": {token}})
	if rec.Code != http.StatusSeeOther || location(rec) != "/" {
		t.Errorf("POST /logout after the end: %d %q, want 303 to /", rec.Code, location(rec))
	}
	if c := cookieNamed(rec, "gobb_session"); c == nil || c.MaxAge >= 0 {
		t.Errorf("the session cookie was not cleared: %+v", c)
	}

	for _, c := range []struct {
		target string
		form   url.Values
	}{
		{"/scheme", url.Values{"scheme": {"slate-light"}}},
		{"/read", url.Values{}},
		{"/f/2/read", url.Values{}},
	} {
		cookies, token := ended(t)
		c.form.Set("csrf", token)
		rec := request(t, h, http.MethodPost, c.target, cookies, c.form)
		if body := bodyOf(t, rec); rec.Code != http.StatusForbidden || !strings.Contains(body, endedText) {
			t.Errorf("POST %s after the end: %d, want 403 with %q:\n%s", c.target, rec.Code, endedText, body)
		}
	}
	var scheme string
	if err := pool.QueryRow(ctx, `select color_scheme from users where id = 1`).Scan(&scheme); err != nil || scheme != "slate" {
		t.Errorf("alice's scheme = %q (%v), want slate: a dead session changed it", scheme, err)
	}

	// A live member with a wrong token: the plain refusal, and still signed in.
	live := signIn(t, h, "bob", testPassword)
	for _, target := range []string{"/logout", "/scheme"} {
		rec := request(t, h, http.MethodPost, target, live, url.Values{"csrf": {"wrong"}, "scheme": {"slate-light"}})
		if body := bodyOf(t, rec); rec.Code != http.StatusForbidden || strings.Contains(body, endedText) || !strings.Contains(body, "reload the page") {
			t.Errorf("POST %s by a live member with a wrong token: %d, want the plain 403:\n%s", target, rec.Code, body)
		}
	}
	if !strings.Contains(bodyOf(t, getAs(t, h, "/", live)), "Signed in as") {
		t.Error("a refused sign-out signed bob out")
	}
	// A guest with a wrong token: the plain refusal.
	rec = request(t, h, http.MethodPost, "/scheme", nil, url.Values{"csrf": {"wrong"}, "scheme": {"slate-light"}})
	if body := bodyOf(t, rec); rec.Code != http.StatusForbidden || strings.Contains(body, endedText) {
		t.Errorf("POST /scheme by a guest with a wrong token: %d, want the plain 403", rec.Code)
	}
}
