package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/login"
)

// wantBaseCSP is the policy of every response that has no embed. It is the exact
// text of the plan, so a change to the policy shows in a test.
const wantBaseCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' https:; connect-src 'self'; font-src 'self'; frame-src 'none'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'"

// embedSite is a site whose post 1 holds a bare YouTube link and whose post 2
// holds a bare X post link. Both sit alone on their line, so each is an embed.
func embedSite(t *testing.T, mutate func(b *bundletest.Board)) http.Handler {
	t.Helper()
	b := bundletest.Small()
	b.Posts[0].Body.Source = "Watch this:\n\nhttps://www.youtube.com/watch?v=dQw4w9WgXcQ\n"
	b.Posts[1].Body.Source = "And this:\n\nhttps://x.com/example/status/20\n"
	if mutate != nil {
		mutate(b)
	}
	h, _ := newTestSite(t, b, Options{})
	return h
}

// directive returns the value of the named directive of a policy, or false.
func directive(policy, name string) (string, bool) {
	for _, d := range strings.Split(policy, ";") {
		d = strings.TrimSpace(d)
		if v, ok := strings.CutPrefix(d, name+" "); ok {
			return v, true
		}
	}
	return "", false
}

func TestCSPBase(t *testing.T) {
	h, _ := smallSite(t)
	for _, p := range []string{"/", "/nope", "/files/avatars/1.png", "/login", "/healthz", "/static/gobb.css"} {
		rec := get(t, h, p)
		if got := rec.Header().Get("Content-Security-Policy"); got != wantBaseCSP {
			t.Errorf("%s (%d): Content-Security-Policy = %q\nwant %q", p, rec.Code, got, wantBaseCSP)
		}
	}
}

func TestCSPEmbeds(t *testing.T) {
	h := embedSite(t, nil)
	rec := get(t, h, "/t/1")
	if rec.Code != http.StatusOK {
		t.Fatalf("/t/1 = %d", rec.Code)
	}
	body := bodyOf(t, rec)
	csp := rec.Header().Get("Content-Security-Policy")
	for name, want := range map[string][]string{
		"frame-src":   {"https://www.youtube-nocookie.com", "https://platform.twitter.com"},
		"script-src":  {"'self'", "https://platform.twitter.com", "https://platform.x.com"},
		"connect-src": {"'self'", "https://syndication.twitter.com", "https://cdn.syndication.twimg.com"},
		"style-src":   {"'self'", "'unsafe-inline'"},
	} {
		got, _ := directive(csp, name)
		for _, w := range want {
			if !strings.Contains(" "+got+" ", " "+w+" ") {
				t.Errorf("%s = %q, want it to hold %s", name, got, w)
			}
		}
	}
	if !strings.Contains(body, `<script async src="https://platform.x.com/widgets.js"></script>`) {
		t.Errorf("the page lacks the X script tag")
	}
	if n := strings.Count(body, "<script async"); n != 1 {
		t.Errorf("%d async scripts, want 1", n)
	}
	for _, d := range []string{"default-src", "img-src", "font-src", "form-action", "frame-ancestors", "base-uri", "object-src"} {
		want, _ := directive(wantBaseCSP, d)
		if got, _ := directive(csp, d); got != want {
			t.Errorf("%s = %q, want %q", d, got, want)
		}
	}
	if strings.Contains(csp, "frame-src 'none'") {
		t.Errorf("frame-src 'none' stays in %q", csp)
	}

	// The index holds no post, so it keeps the base policy.
	if got := get(t, h, "/").Header().Get("Content-Security-Policy"); got != wantBaseCSP {
		t.Errorf("/ CSP = %q", got)
	}
}

// A page with only a YouTube embed gets its frame origin and nothing else.
func TestCSPEmbedsYouTubeOnly(t *testing.T) {
	h := embedSite(t, func(b *bundletest.Board) {
		b.Posts[1].Body.Source = "No embed here."
	})
	rec := get(t, h, "/t/1")
	csp := rec.Header().Get("Content-Security-Policy")
	if got, _ := directive(csp, "frame-src"); got != "https://www.youtube-nocookie.com" {
		t.Errorf("frame-src = %q", got)
	}
	if got, _ := directive(csp, "script-src"); got != "'self'" {
		t.Errorf("script-src = %q", got)
	}
	if got, _ := directive(csp, "connect-src"); got != "'self'" {
		t.Errorf("connect-src = %q", got)
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP holds unsafe-inline: %q", csp)
	}
	if strings.Contains(bodyOf(t, rec), "<script async") {
		t.Error("the page loads a provider script for a YouTube embed")
	}
}

// A static page renders a tree too, so its embeds count. A signature does not
// embed: see TestSignatureEmbedIsAPlainLink.
func TestCSPEmbedInStaticPage(t *testing.T) {
	h := embedSite(t, func(b *bundletest.Board) {
		b.StaticPages[0].Body.Source = "https://x.com/example/status/20"
	})
	rec := get(t, h, "/pages/"+firstPageSlug(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("static page = %d", rec.Code)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if got, _ := directive(csp, "script-src"); !strings.Contains(got, "https://platform.x.com") {
		t.Errorf("static page script-src = %q", got)
	}
	if !strings.Contains(bodyOf(t, rec), `<script async src="https://platform.x.com/widgets.js">`) {
		t.Error("the static page lacks the X script tag")
	}
}

// A signature renders an embed link as a plain link, with no frame and no
// provider script, so a signature cannot load third-party script on every page
// that shows it (A-D2). A post keeps its embeds.
func TestSignatureEmbedIsAPlainLink(t *testing.T) {
	const link = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	h := embedSite(t, func(b *bundletest.Board) {
		b.Users[0].Signature.Source = link
		// Neither post of the topic holds an embed, so only the signature could
		// bring one.
		b.Posts[0].Body.Source = "No embed here."
		b.Posts[1].Body.Source = "Nor here."
	})
	bob := signIn(t, h, "bob", testPassword)
	for _, page := range []string{"/t/1", "/u/1"} {
		rec := getAs(t, h, page, bob)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", page, rec.Code)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != wantBaseCSP {
			t.Errorf("%s: CSP = %q, want the base policy %q", page, got, wantBaseCSP)
		}
		body := bodyOf(t, rec)
		if !strings.Contains(body, `<a href="`+link+`" rel="nofollow ugc noopener">`) {
			t.Errorf("%s: the signature holds no plain link", page)
		}
		if strings.Contains(body, "<iframe") || strings.Contains(body, "<script async") {
			t.Errorf("%s: the signature brought a frame or a script", page)
		}
	}
	// The same link in a post is an embed.
	h = embedSite(t, func(b *bundletest.Board) { b.Users[0].Signature.Source = link })
	rec := getAs(t, h, "/t/1", signIn(t, h, "bob", testPassword))
	if got, _ := directive(rec.Header().Get("Content-Security-Policy"), "frame-src"); !strings.Contains(got, "youtube-nocookie") {
		t.Errorf("a post lost its embed: frame-src = %q", got)
	}
}

func firstPageSlug(t *testing.T) string {
	t.Helper()
	return bundletest.Small().StaticPages[0].Slug
}

var fiveHeaders = map[string]string{
	"X-Content-Type-Options":     "nosniff",
	"Referrer-Policy":            "strict-origin-when-cross-origin",
	"X-Frame-Options":            "DENY",
	"Cross-Origin-Opener-Policy": "same-origin",
	"Permissions-Policy":         "camera=(), microphone=(), geolocation=()",
}

func checkFiveHeaders(t *testing.T, what string, h http.Header) {
	t.Helper()
	for k, want := range fiveHeaders {
		if got := h.Get(k); got != want {
			t.Errorf("%s: %s = %q, want %q", what, k, got, want)
		}
	}
	if h.Get("Content-Security-Policy") == "" {
		t.Errorf("%s: no Content-Security-Policy", what)
	}
}

func TestSecurityHeaders(t *testing.T) {
	pool, dataDir := boardtest.Load(t, bundletest.Small())
	h, err := New(t.Context(), pool, Options{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(),
		`insert into acl_group_grants (group_id, forum_id, option, setting) values (1, 0, 'account.download', 3), (1, 2, 'attach.download', 3)`); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/", "/t/1", "/nope", "/a/1", "/healthz", "/static/gobb.js", "/files/avatars/1.png",
		"/login", "/u/1", "/p/1", "/t/99/new", "/f/2?page=zzz", "/t/1?page=0", "/new", "/a/999", "/../x"} {
		rec := get(t, h, p)
		checkFiveHeaders(t, p+" ("+http.StatusText(rec.Code)+")", rec.Header())
	}
	// A method the route does not take, and a POST that the CSRF check refuses.
	for _, tc := range []struct {
		method, target, body string
		want                 int
	}{
		{"DELETE", "/healthz", "", http.StatusMethodNotAllowed},
		{"POST", "/login", "name=x", http.StatusForbidden},
		{"POST", "/healthz", "", http.StatusMethodNotAllowed},
	} {
		req, _ := http.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.target, rec.Code, tc.want)
		}
		checkFiveHeaders(t, tc.method+" "+tc.target, rec.Header())
	}
}

// The 413, 429 and 503 answers of the sign-in carry the headers too.
func TestSecurityHeadersOnLoginLimits(t *testing.T) {
	h, _ := siteAndPool(t, bundletest.Small(), proxyOptions())
	cookies, token := csrfFor(t, h)
	big := proxied(t, h, "/login", "2001:db8::5", cookies, url.Values{"name": {"alice"}, "password": {strings.Repeat("x", 1<<20)}})
	if big.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("1 MiB POST /login = %d, want 413", big.Code)
	}
	checkFiveHeaders(t, "413", big.Header())
	form := url.Values{"name": {"alice"}, "password": {"wrong"}, "csrf": {token}}
	for i := range 10 {
		if rec := proxied(t, h, "/login", "2001:db8::5", cookies, form); rec.Code != http.StatusOK {
			t.Fatalf("failure %d = %d, want 200", i+1, rec.Code)
		}
	}
	limited := proxied(t, h, "/login", "2001:db8::5", cookies, form)
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("11th try = %d, want 429", limited.Code)
	}
	checkFiveHeaders(t, "429", limited.Header())

	gate := login.NewGate(1024)
	hold, err := gate.Acquire(t.Context(), 1024, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	busy, _ := siteAndPool(t, bundletest.Small(), Options{LoginGate: gate, LoginGateWait: 20 * time.Millisecond})
	rec, _ := postAs(t, busy, "/login", nil, url.Values{"name": {"alice"}, "password": {testPassword}})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("busy POST /login = %d, want 503", rec.Code)
	}
	checkFiveHeaders(t, "503", rec.Header())
}

// A non-image attachment keeps its sandbox. The page policy comes along, so
// the download is as strict as a page.
func TestAttachmentSandboxKept(t *testing.T) {
	pool, dataDir := boardtest.Load(t, bundletest.Small())
	h, err := New(t.Context(), pool, Options{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`insert into acl_group_grants (group_id, forum_id, option, setting) values (1, 0, 'account.download', 3), (1, 2, 'attach.download', 3)`,
		`update attachments set mime = 'text/html', real_name = 'page.html' where id = 1`,
	} {
		if _, err := pool.Exec(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	rec := get(t, h, "/a/1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !regexp.MustCompile(`(^|; )sandbox($|;)`).MatchString(csp) {
		t.Errorf("no sandbox directive in %q", csp)
	}
	if !strings.HasPrefix(csp, "default-src 'none'") {
		t.Errorf("the page policy is lost: %q", csp)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") {
		t.Errorf("Content-Disposition = %q", got)
	}
	checkFiveHeaders(t, "/a/1", rec.Header())

	// An image attachment is not sandboxed.
	if _, err := pool.Exec(t.Context(), `update attachments set mime = 'image/png' where id = 1`); err != nil {
		t.Fatal(err)
	}
	rec = get(t, h, "/a/1")
	if got := rec.Header().Get("Content-Security-Policy"); got != wantBaseCSP {
		t.Errorf("image CSP = %q", got)
	}
}

func TestHTMLCaching(t *testing.T) {
	h, _ := smallSite(t)
	// A guest gets private, no-cache on a page and on an error page, and the
	// pages that hold a form or a changing list get no-store.
	for _, tc := range []struct{ path, want string }{
		{"/", "private, no-cache"}, {"/f/2", "private, no-cache"}, {"/t/1", "private, no-cache"},
		{"/nope", "private, no-cache"}, {"/members", "private, no-cache"}, {"/team", "private, no-cache"},
		{"/search", "private, no-cache"}, {"/pages/" + firstPageSlug(t), "private, no-cache"},
		{"/login", "no-store"},
	} {
		rec := get(t, h, tc.path)
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: Content-Type %q", tc.path, ct)
		}
		if got := rec.Header().Get("Cache-Control"); got != tc.want {
			t.Errorf("guest %s (%d): Cache-Control = %q, want %q", tc.path, rec.Code, got, tc.want)
		}
	}
	// A member gets private, no-store on every page.
	cookies := signIn(t, h, "alice", testPassword)
	for _, tc := range []struct{ path, want string }{
		{"/", "private, no-store"}, {"/f/2", "private, no-store"}, {"/t/1", "private, no-store"},
		{"/nope", "private, no-store"}, {"/members", "private, no-store"}, {"/u/1", "private, no-store"},
		{"/new", "private, no-store"}, {"/login?next=/", "private, no-store"},
	} {
		rec := getAs(t, h, tc.path, cookies)
		if rec.Code >= 300 && rec.Code < 400 {
			if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Errorf("member %s redirect: Cache-Control = %q", tc.path, got)
			}
			continue
		}
		if got := rec.Header().Get("Cache-Control"); got != tc.want {
			t.Errorf("member %s (%d): Cache-Control = %q, want %q", tc.path, rec.Code, got, tc.want)
		}
	}
	if got := getAs(t, h, "/new", cookies).Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("member /new: Cache-Control = %q", got)
	}
	// Assets and files keep their own.
	if got := get(t, h, "/static/gobb.css").Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Errorf("/static Cache-Control = %q", got)
	}
	if got := getAs(t, h, "/static/gobb.css", cookies).Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Errorf("member /static Cache-Control = %q", got)
	}
	if got := get(t, h, "/files/avatars/1.png").Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Errorf("/files Cache-Control = %q", got)
	}
}

// A redirect depends on the viewer, so no shared cache may keep one. It carries
// the same Cache-Control as a page. A moved topic's 301 and a legacy 301 would
// otherwise let a cache serve a member's redirect to a guest.
func TestRedirectsCacheControl(t *testing.T) {
	h, _ := smallSite(t)
	cookies := signIn(t, h, "alice", testPassword)
	for _, tc := range []struct {
		path   string
		member int
		guest  int // 0 when a guest gets no redirect
	}{
		{"/t/3", http.StatusMovedPermanently, 0}, // a moved-topic marker in a forum guests cannot read
		{"/old/about.cgi", http.StatusMovedPermanently, http.StatusMovedPermanently},
		{"/p/1", http.StatusFound, http.StatusFound},
		{"/t/1/new", http.StatusFound, 0},
		{"/account", http.StatusOK, http.StatusFound},
	} {
		guest := get(t, h, tc.path)
		if tc.guest != 0 {
			if guest.Code != tc.guest {
				t.Errorf("guest %s = %d, want %d", tc.path, guest.Code, tc.guest)
			}
			if got := guest.Header().Get("Cache-Control"); got != "private, no-cache" {
				t.Errorf("guest %s (%d): Cache-Control = %q, want private, no-cache", tc.path, guest.Code, got)
			}
		}
		member := getAs(t, h, tc.path, cookies)
		if member.Code != tc.member {
			t.Errorf("member %s = %d, want %d", tc.path, member.Code, tc.member)
		}
		if got := member.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Errorf("member %s (%d): Cache-Control = %q, want private, no-store", tc.path, member.Code, got)
		}
	}
}

// The 303 after a sign-in and the 303 after a sign-out carry the header of the
// request that made them: a guest signs in, a member signs out.
func TestLoginLogoutRedirectsCacheControl(t *testing.T) {
	h, _ := smallSite(t)
	rec, cookies := postAs(t, h, "/login", nil, url.Values{"name": {"alice"}, "password": {testPassword}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("sign-in = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Errorf("sign-in 303: Cache-Control = %q, want private, no-cache", got)
	}
	rec, _ = postAs(t, h, "/logout", cookies, url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("sign-out = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("sign-out 303: Cache-Control = %q, want private, no-store", got)
	}
}

// An attachment keeps its own Cache-Control, for a guest and for a member.
func TestAttachmentKeepsCacheControl(t *testing.T) {
	pool, dataDir := boardtest.Load(t, bundletest.Small())
	h, err := New(t.Context(), pool, Options{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(),
		`insert into acl_group_grants (group_id, forum_id, option, setting) values (1, 0, 'account.download', 3), (1, 2, 'attach.download', 3)`); err != nil {
		t.Fatal(err)
	}
	rec := get(t, h, "/a/1")
	if rec.Code != http.StatusOK {
		t.Fatalf("/a/1 = %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Errorf("/a/1 Cache-Control = %q, want private, no-cache", got)
	}
	// A download depends on the rights of the viewer, so the browser revalidates,
	// and ServeContent answers 304 when the file is as old as the browser's copy.
	lm := rec.Header().Get("Last-Modified")
	if lm == "" {
		t.Fatal("/a/1 has no Last-Modified")
	}
	req := httptest.NewRequest(http.MethodGet, "/a/1", nil)
	req.Header.Set("If-Modified-Since", lm)
	again := httptest.NewRecorder()
	h.ServeHTTP(again, req)
	if again.Code != http.StatusNotModified {
		t.Errorf("revalidation = %d, want 304", again.Code)
	}
	if got := again.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Errorf("304 Cache-Control = %q", got)
	}
}

// Two posts that need the same provider load its script once. A signature adds
// no script.
func TestOneScriptTagPerProvider(t *testing.T) {
	h := embedSite(t, func(b *bundletest.Board) {
		b.Posts[0].Body.Source = "https://x.com/example/status/20"
		b.Posts[1].Body.Source = "https://x.com/example/status/21"
		b.Posts[2].Body.Source = "https://x.com/example/status/22"
		b.Posts[3].Body.Source = "plain"
		b.Users[0].Signature.Source = "https://x.com/example/status/23"
		b.Users[1].Signature.Source = "https://x.com/example/status/24"
	})
	body := bodyOf(t, get(t, h, "/t/1"))
	if n := strings.Count(body, `<script async src="https://platform.x.com/widgets.js"></script>`); n != 1 {
		t.Errorf("%d X script tags, want 1", n)
	}
	if n := strings.Count(body, "<script async"); n != 1 {
		t.Errorf("%d async scripts, want 1", n)
	}
	// Post 1 and post 2 hold one tweet each. The signature of post 1 holds a third
	// tweet link, which is a plain link and not an embed (A-D2). (Post 3 is
	// unapproved, post 2 shows no signature.) The script is loaded once.
	if n := strings.Count(body, `class="twitter-tweet"`); n != 2 {
		t.Errorf("%d tweets on the page, want 2 (the 2 posts; the signature is a plain link)", n)
	}
	if !strings.Contains(body, `<a href="https://x.com/i/status/23" rel="nofollow ugc noopener">`) {
		t.Error("the signature holds no plain link to its tweet")
	}
}

// A response that is not a page and that answers an error tells search engines
// to skip it, as the robots meta does for a page. Only /healthz answers so: the
// other errors are pages (spec 13).
func TestNoindexHeaderOnPlainErrors(t *testing.T) {
	h, _ := smallSite(t)
	// The mux answers 405 in plain text.
	rec := get(t, h, "/healthz")
	if rec.Header().Get("X-Robots-Tag") != "" {
		t.Errorf("a 200 has X-Robots-Tag %q", rec.Header().Get("X-Robots-Tag"))
	}
	rec = do(t, h, http.MethodDelete, "/healthz")
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("X-Robots-Tag") != "noindex" {
		t.Errorf("healthz 405: %d, X-Robots-Tag %q", rec.Code, rec.Header().Get("X-Robots-Tag"))
	}
	// A failing database gives the 503 of /healthz.
	rec = httptest.NewRecorder()
	secure(Health(failingPinger{})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("X-Robots-Tag") != "noindex" {
		t.Errorf("healthz 503: %d, X-Robots-Tag %q", rec.Code, rec.Header().Get("X-Robots-Tag"))
	}
	// A request that cannot read the settings gets the error page of a guest,
	// from the settings in memory: a page, with the robots meta and no header.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/t/1", nil).WithContext(ctx))
	if rec.Code != http.StatusInternalServerError || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(rec.Body.String(), `<meta name="robots" content="noindex">`) || rec.Header().Get("X-Robots-Tag") != "" {
		t.Errorf("a request that cannot read the settings: %d %q, X-Robots-Tag %q, want a 500 page with the robots meta",
			rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("X-Robots-Tag"))
	}
	// A page answer carries the meta element and no header.
	if got := get(t, h, "/nope").Header().Get("X-Robots-Tag"); got != "" {
		t.Errorf("an HTML 404 has X-Robots-Tag %q", got)
	}
}

type failingPinger struct{}

func (failingPinger) Ping(context.Context) error { return errors.New("down") }

func TestNoindex(t *testing.T) {
	h, _ := smallSite(t)
	const meta = `<meta name="robots" content="noindex">`
	for _, p := range []string{"/login", "/search", "/nope", "/search?q=alice", "/t/99"} {
		if body := bodyOf(t, get(t, h, p)); !strings.Contains(body, meta) {
			t.Errorf("%s lacks the robots meta", p)
		}
	}
	cookies := signIn(t, h, "alice", testPassword)
	if body := bodyOf(t, getAs(t, h, "/new", cookies)); !strings.Contains(body, meta) {
		t.Error("/new lacks the robots meta")
	}
	for _, p := range []string{"/t/1", "/", "/f/2", "/members", "/team", "/u/1"} {
		if body := bodyOf(t, getAs(t, h, p, cookies)); strings.Contains(body, meta) {
			t.Errorf("%s has the robots meta", p)
		}
	}
}
