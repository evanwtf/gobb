package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
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

// A signature and a static page render trees too, so their embeds count.
func TestCSPEmbedInSignatureAndPage(t *testing.T) {
	h := embedSite(t, func(b *bundletest.Board) {
		b.Users[0].Signature.Source = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
		b.StaticPages[0].Body.Source = "https://x.com/example/status/20"
	})
	bob := signIn(t, h, "bob", testPassword)
	rec := getAs(t, h, "/u/1", bob)
	if rec.Code != http.StatusOK {
		t.Fatalf("profile = %d", rec.Code)
	}
	if got, _ := directive(rec.Header().Get("Content-Security-Policy"), "frame-src"); !strings.Contains(got, "youtube-nocookie") {
		t.Errorf("profile frame-src = %q", got)
	}
	rec = get(t, h, "/pages/"+firstPageSlug(t))
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
	// A method the route does not take, a POST that the CSRF check refuses, and
	// a body past the cap.
	for _, tc := range []struct {
		method, target, body string
	}{
		{"DELETE", "/", ""},
		{"POST", "/login", "name=x"},
		{"POST", "/healthz", ""},
		{"POST", "/login", strings.Repeat("a", 1<<20)},
	} {
		req, _ := http.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		checkFiveHeaders(t, tc.method+" "+tc.target+" ("+http.StatusText(rec.Code)+")", rec.Header())
	}
	// The sign-in limit answers 429.
	var last int
	for i := range 40 {
		rec, _ := postAs(t, h, "/login", nil, url.Values{"name": {"alice"}, "password": {"wrong" + strings.Repeat("x", i)}})
		last = rec.Code
		checkFiveHeaders(t, "failed sign-in", rec.Header())
		if last == http.StatusTooManyRequests {
			break
		}
	}
	if last != http.StatusTooManyRequests {
		t.Logf("no 429 after 40 failed sign-ins (last status %d)", last)
	}
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
	// Guests: private, no-cache on every HTML page, error pages included.
	for _, p := range []string{"/", "/f/2", "/t/1", "/nope", "/members", "/team", "/search", "/pages/" + firstPageSlug(t)} {
		rec := get(t, h, p)
		ct := rec.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: Content-Type %q", p, ct)
			continue
		}
		cc := rec.Header().Get("Cache-Control")
		if cc != "private, no-cache" && cc != "private, no-store" {
			t.Errorf("guest %s (%d): Cache-Control = %q", p, rec.Code, cc)
		}
	}
	if got := get(t, h, "/").Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Errorf("guest / Cache-Control = %q, want private, no-cache", got)
	}
	if got := get(t, h, "/nope").Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Errorf("guest 404 Cache-Control = %q, want private, no-cache", got)
	}
	// Members: private, no-store.
	cookies := signIn(t, h, "alice", testPassword)
	for _, p := range []string{"/", "/f/2", "/t/1", "/nope", "/members", "/u/1", "/login"} {
		rec := getAs(t, h, p, cookies)
		if rec.Code >= 300 && rec.Code < 400 {
			continue // a redirect has no page to cache
		}
		if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Errorf("member %s (%d): Cache-Control = %q, want private, no-store", p, rec.Code, got)
		}
	}
	// Assets and files keep their own.
	if got := get(t, h, "/static/gobb.css").Header().Get("Cache-Control"); !strings.HasPrefix(got, "public, max-age=") {
		t.Errorf("/static Cache-Control = %q", got)
	}
	if got := get(t, h, "/files/avatars/1.png").Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Errorf("/files Cache-Control = %q", got)
	}
}

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
