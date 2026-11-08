package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/session"
)

// cachedSite is siteWithPool with a guest page cache of 4 MiB and no start-up
// render.
func cachedSite(t *testing.T, b *bundletest.Board, opt Options) (http.Handler, *pgxpool.Pool, *GuestCache) {
	t.Helper()
	g := NewGuestCache(4<<20, false)
	opt.GuestCache = g
	h, pool := siteWithPool(t, b, opt)
	return h, pool, g
}

// renderedIn finds the time of the build line, which differs on every request.
var renderedIn = regexp.MustCompile(`Rendered in [0-9.]+ s`)

// sameShape masks the csrf tokens and the render time of a page.
func sameShape(body string) string {
	return renderedIn.ReplaceAllString(withoutCSRF(body), "Rendered in T s")
}

// pageToken returns the csrf token of a page.
func pageToken(t *testing.T, body string) string {
	t.Helper()
	m := csrfField.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("the page has no csrf field")
	}
	return m[1]
}

// cookieValue returns the value of the cookie name that rec sets, or "".
func cookieValue(rec *httptest.ResponseRecorder, name string) string {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

func TestGuestCacheSameBodyFreshToken(t *testing.T) {
	h, pool, _ := cachedSite(t, richBoard(), Options{})
	live, err := New(t.Context(), pool, Options{LoginFloor: time.Millisecond, Signin: testSchedule()})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/f/2", "/t/5", "/t/6", "/pages/about"} {
		builds := layoutBuilds.Load()
		a, b := get(t, h, path), get(t, h, path)
		if n := layoutBuilds.Load() - builds; n != 1 {
			t.Errorf("%s: two guests built the page %d times, want 1", path, n)
		}
		if a.Code != http.StatusOK || b.Code != http.StatusOK {
			t.Fatalf("%s: %d and %d", path, a.Code, b.Code)
		}
		ab, bb := bodyOf(t, a), bodyOf(t, b)
		if sameShape(ab) != sameShape(bb) {
			t.Errorf("%s: two guests got different pages", path)
		}
		// Each guest with no cookie gets a new secret, and the token of it.
		sa, sb := cookieValue(a, csrfCookie), cookieValue(b, csrfCookie)
		ta, tb := pageToken(t, ab), pageToken(t, bb)
		if sa == "" || sb == "" || sa == sb || ta == tb {
			t.Errorf("%s: the guests share a secret or a token", path)
		}
		if ta != session.CSRFToken(sa) || tb != session.CSRFToken(sb) {
			t.Errorf("%s: a token is not the token of its secret", path)
		}
		if strings.Contains(ab, "gobbslot") {
			t.Errorf("%s: a slot was not filled in", path)
		}
		if !renderedIn.MatchString(ab) {
			t.Errorf("%s: the build line has no render time", path)
		}
		// The cached page is the live page, less the parts of the request.
		lrec := get(t, live, path)
		if sameShape(bodyOf(t, lrec)) != sameShape(ab) {
			t.Errorf("%s: the cached page differs from the live one", path)
		}
		for _, name := range []string{"Content-Type", "Content-Security-Policy", "Cache-Control"} {
			if a.Header().Get(name) != lrec.Header().Get(name) {
				t.Errorf("%s: %s = %q, live %q", path, name, a.Header().Get(name), lrec.Header().Get(name))
			}
		}
		// A guest with a secret keeps it, and gets its token.
		rec := getAs(t, h, path, []*http.Cookie{{Name: csrfCookie, Value: "my-secret"}})
		if got := pageToken(t, bodyOf(t, rec)); got != session.CSRFToken("my-secret") {
			t.Errorf("%s: a guest with a secret got another token", path)
		}
		if cookieValue(rec, csrfCookie) != "" {
			t.Errorf("%s: a guest with a secret got a new one", path)
		}
	}
}

func TestGuestCacheMemberGetsALivePage(t *testing.T) {
	h, _, g := cachedSite(t, richBoard(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	if body := bodyOf(t, get(t, h, "/t/5")); strings.Contains(body, "Account menu for") {
		t.Fatal("a guest page shows an account menu")
	}
	before := g.Pages.Stats()
	builds := layoutBuilds.Load()
	for range 2 {
		body := bodyOf(t, getAs(t, h, "/t/5", alice))
		if !strings.Contains(body, "Account menu for") {
			t.Error("the member got the guest page")
		}
	}
	if n := layoutBuilds.Load() - builds; n != 2 {
		t.Errorf("two member requests built %d pages, want 2", n)
	}
	after := g.Pages.Stats()
	if after.Hits != before.Hits || after.Misses != before.Misses || after.Entries != before.Entries {
		t.Errorf("a member request touched the cache: %+v then %+v", before, after)
	}
	// The guest page after the member's is still the guest's.
	if body := bodyOf(t, get(t, h, "/t/5")); strings.Contains(body, "Account menu for") || strings.Contains(body, "alice</bdi></summary>") {
		t.Error("a guest got the member's page")
	}
}

func TestGuestCacheDeadSessionGetsTheGuestPage(t *testing.T) {
	h, _, g := cachedSite(t, richBoard(), Options{})
	get(t, h, "/t/5")
	hits := g.Pages.Stats().Hits
	rec := getAs(t, h, "/t/5", []*http.Cookie{{Name: sessionCookie, Value: "made-up"}})
	if rec.Code != http.StatusOK || strings.Contains(bodyOf(t, rec), "Account menu for") {
		t.Fatalf("a dead session got %d", rec.Code)
	}
	if g.Pages.Stats().Hits != hits+1 {
		t.Error("a dead session cookie went past the cache")
	}
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("the dead session cookie was not cleared")
	}
}

func TestGuestCacheQueryVariantsShareAKey(t *testing.T) {
	h, _, g := cachedSite(t, richBoard(), Options{})
	builds := layoutBuilds.Load()
	var first string
	for _, target := range []string{"/t/5", "/t/5?page=1", "/t/5?utm_source=x&page=1", "/t/5?foo=bar", "/t/5?q=" + strings.Repeat("z", 100)} {
		rec := get(t, h, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", target, rec.Code)
		}
		body := sameShape(bodyOf(t, rec))
		if first == "" {
			first = body
		} else if body != first {
			t.Errorf("%s: the page differs from the canonical one", target)
		}
		if strings.Contains(body, "utm_source") || strings.Contains(body, "foo=bar") {
			t.Errorf("%s: a query parameter reached the page", target)
		}
	}
	if n := layoutBuilds.Load() - builds; n != 1 {
		t.Errorf("the variants built %d pages, want 1", n)
	}
	if st := g.Pages.Stats(); st.Entries != 1 {
		t.Errorf("%d entries, want 1", st.Entries)
	}
	// The scheme and the note of a held post are part of the key.
	if body := bodyOf(t, get(t, h, "/t/5?scheme=slate-deep")); !strings.Contains(body, `data-scheme="slate-deep"`) {
		t.Error("the scheme preview is lost")
	}
	if body := bodyOf(t, getAs(t, h, "/t/5", []*http.Cookie{{Name: schemeCookie, Value: "slate-light"}})); !strings.Contains(body, `data-scheme="slate-light"`) {
		t.Error("the scheme cookie is lost")
	}
	get(t, h, "/t/5?held=1")
	if st := g.Pages.Stats(); st.Entries != 4 {
		t.Errorf("%d entries after the scheme and held variants, want 4", st.Entries)
	}
	// A bad page number is not a key: the live handler answers it.
	if rec := get(t, h, "/t/5?page=x"); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad page = %d", rec.Code)
	}
}

func TestGuestCacheForgedHostNeverLeaks(t *testing.T) {
	// No public URL: the site would take a host from the request if anything did.
	h, _, _ := cachedSite(t, richBoard(), Options{})
	for _, target := range []string{"/", "/f/2", "/t/5", "/pages/about"} {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r.Host = "forged.example"
		r.Header.Set("X-Forwarded-Host", "forged.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if strings.Contains(bodyOf(t, rec), "forged.example") {
			t.Errorf("%s: the forged host is on the page of its own request", target)
		}
		if strings.Contains(bodyOf(t, get(t, h, target)), "forged.example") {
			t.Errorf("%s: the forged host reached another viewer", target)
		}
	}
}

func TestGuestCacheConcurrentMissesRenderOnce(t *testing.T) {
	h, _, g := cachedSite(t, richBoard(), Options{})
	const n = 20
	builds := layoutBuilds.Load()
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := range n {
		wg.Go(func() {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/t/5", nil))
			codes[i] = rec.Code
		})
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("request %d = %d", i, c)
		}
	}
	if got := layoutBuilds.Load() - builds; got != 1 {
		t.Errorf("%d concurrent misses built %d pages, want 1", n, got)
	}
	if st := g.Pages.Stats(); st.Hits+st.Misses != n {
		t.Errorf("hits %d + misses %d, want %d", st.Hits, st.Misses, n)
	}
}

func TestGuestCacheETag(t *testing.T) {
	h, _, _ := cachedSite(t, richBoard(), Options{})
	secret := []*http.Cookie{{Name: csrfCookie, Value: "etag-secret"}}
	rec := getAs(t, h, "/t/5", secret)
	etag := rec.Header().Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) {
		t.Fatalf("ETag = %q", etag)
	}
	r := httptest.NewRequest(http.MethodGet, "/t/5", nil)
	r.AddCookie(secret[0])
	r.Header.Set("If-None-Match", `"other", `+etag)
	again := httptest.NewRecorder()
	h.ServeHTTP(again, r)
	if again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Errorf("a matching If-None-Match = %d with %d bytes", again.Code, again.Body.Len())
	}
	// Another guest has another token on the page, so another tag.
	if other := getAs(t, h, "/t/5", []*http.Cookie{{Name: csrfCookie, Value: "another"}}); other.Header().Get("ETag") == etag {
		t.Error("two guests share a tag for pages with different tokens")
	}
	// A guest with no secret gets a new one, and never a 304.
	r = httptest.NewRequest(http.MethodGet, "/t/5", nil)
	r.Header.Set("If-None-Match", etag)
	fresh := httptest.NewRecorder()
	h.ServeHTTP(fresh, r)
	if fresh.Code != http.StatusOK {
		t.Errorf("a guest with no secret = %d", fresh.Code)
	}
	// HEAD has the headers and no body.
	r = httptest.NewRequest(http.MethodHead, "/t/5", nil)
	head := httptest.NewRecorder()
	h.ServeHTTP(head, r)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("ETag") == "" {
		t.Errorf("HEAD = %d, %d bytes, ETag %q", head.Code, head.Body.Len(), head.Header().Get("ETag"))
	}
}

func TestGuestCacheErrorPagesAreNotKept(t *testing.T) {
	h, _, g := cachedSite(t, richBoard(), Options{})
	for _, target := range []string{"/t/999", "/t/3", "/f/999", "/pages/nope"} {
		want := get(t, h, target).Code
		if want == http.StatusOK {
			t.Fatalf("%s = 200", target)
		}
		if got := get(t, h, target).Code; got != want {
			t.Errorf("%s: %d then %d", target, want, got)
		}
	}
	if st := g.Pages.Stats(); st.Entries != 0 {
		t.Errorf("%d error pages or redirects were kept", st.Entries)
	}
	// The redirect of a moved topic keeps its target.
	if rec := get(t, h, "/t/3"); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/t/1" {
		t.Errorf("/t/3 = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestGuestCacheIndexEndsAtMidnight(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 14, 23, 30, 0, 0, loc)
	h, _, g := cachedSite(t, richBoard(), Options{Now: func() time.Time { return now }})
	get(t, h, "/")
	e, ok := g.Pages.Peek(cacheKey{kind: cacheIndex}.String())
	if !ok {
		t.Fatal("the index is not cached")
	}
	if want := time.Date(2026, 3, 15, 0, 0, 0, 0, loc); !e.Expires.Equal(want) {
		t.Errorf("the index ends at %s, want %s", e.Expires, want)
	}
	if e, _ := g.Pages.Peek(cacheKey{kind: cacheTopic, id: 5, page: 1}.String()); !e.Expires.IsZero() {
		t.Error("a topic page has an end")
	}
}
