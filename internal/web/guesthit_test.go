package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/evanwtf/gobb/internal/batch"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// guestHitSite is a site with the page cache, the batched writes and a main
// in-flight limit of 2, and its *site.
func guestHitSite(t *testing.T) (http.Handler, *site, *GuestCache) {
	t.Helper()
	var s *site
	onNew = func(got *site) { s = got }
	t.Cleanup(func() { onNew = nil })
	h, _, g := cachedSite(t, bundletest.Small(), Options{Writes: batch.New(), MaxInFlight: 2})
	onNew = nil
	return h, s, g
}

// fill takes every main slot, as requests under way would, and gives them
// back at the end of the test.
func fill(t *testing.T, s *site) {
	t.Helper()
	for range cap(s.inFlight.all) {
		s.inFlight.all <- struct{}{}
	}
	t.Cleanup(func() {
		for range cap(s.inFlight.all) {
			<-s.inFlight.all
		}
	})
}

// sendWith sends method target with the cookies and headers, and no others.
func sendWith(h http.Handler, method, target string, cookies []*http.Cookie, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// With every main slot held, a guest page in the cache still answers 200 from
// memory, with no render; a guest miss and a member page get 503.
func TestGuestHitSkipsTheInFlightLimit(t *testing.T) {
	h, s, g := guestHitSite(t)
	member := signIn(t, h, "alice", testPassword)
	if rec := get(t, h, "/t/1"); rec.Code != http.StatusOK { // stores the page
		t.Fatalf("/t/1 = %d", rec.Code)
	}
	slotted := sendWith(h, http.MethodGet, "/t/1", nil, nil)
	fill(t, s)

	hits, builds := g.Pages.Stats().Hits, layoutBuilds.Load()
	rec := get(t, h, "/t/1")
	if rec.Code != http.StatusOK {
		t.Fatalf("a cached guest page with the limit full = %d, want 200", rec.Code)
	}
	if n := layoutBuilds.Load() - builds; n != 0 {
		t.Errorf("the hit rendered %d times, want 0", n)
	}
	if n := g.Pages.Stats().Hits - hits; n != 1 {
		t.Errorf("gobb_page_cache_hits_total rose by %d, want 1", n)
	}
	if sameShape(bodyOf(t, rec)) != sameShape(bodyOf(t, slotted)) {
		t.Error("the page before the limit differs from the page behind it")
	}
	for _, name := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options", "Cache-Control", "Content-Type"} {
		if got, want := rec.Header().Get(name), slotted.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q as on the slotted path", name, got, want)
		}
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("the hit has no Content-Security-Policy")
	}

	// The ETag answers as now: the same token gives the same tag and a 304.
	c := &http.Cookie{Name: csrfCookie, Value: cookieValue(rec, csrfCookie)}
	first := sendWith(h, http.MethodGet, "/t/1", []*http.Cookie{c}, nil)
	tag := first.Header().Get("ETag")
	if first.Code != http.StatusOK || tag == "" {
		t.Fatalf("a guest with a csrf cookie = %d, ETag %q", first.Code, tag)
	}
	if rec := sendWith(h, http.MethodGet, "/t/1", []*http.Cookie{c}, map[string]string{"If-None-Match": tag}); rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match with the tag = %d, want 304", rec.Code)
	}

	for _, target := range []string{"/t/1?page=2", "/t/999", "/f/999", "/search?q=x"} {
		if rec := get(t, h, target); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("guest %s (not in the cache) with the limit full = %d, want 503", target, rec.Code)
		}
	}
	if rec := getAs(t, h, "/t/1", member); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("a member page with the limit full = %d, want 503", rec.Code)
	}
	if rec := sendWith(h, http.MethodPost, "/t/1", nil, nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("a POST with the limit full = %d, want 503", rec.Code)
	}
}

// A forged or dead session cookie needs a database lookup, so it takes a slot.
func TestGuestHitNotForASessionCookie(t *testing.T) {
	h, s, _ := guestHitSite(t)
	member := signIn(t, h, "alice", testPassword)
	var sess *http.Cookie
	for _, c := range member {
		if c.Name == sessionCookie {
			sess = c
		}
	}
	if rec := getAs(t, h, "/", member); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	// Sign out: the cookie is now dead.
	dead := &http.Cookie{Name: sessionCookie, Value: sess.Value}
	if _, err := s.pool.Exec(t.Context(), `delete from sessions`); err != nil {
		t.Fatal(err)
	}
	if rec := get(t, h, "/t/1"); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	fill(t, s)
	for name, c := range map[string]*http.Cookie{
		"forged": {Name: sessionCookie, Value: "made-up-token"},
		"dead":   dead,
	} {
		if rec := sendWith(h, http.MethodGet, "/t/1", []*http.Cookie{c}, nil); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("a %s session cookie with the limit full = %d, want 503 (no bypass)", name, rec.Code)
		}
	}
	if rec := get(t, h, "/t/1"); rec.Code != http.StatusOK {
		t.Errorf("a guest with no cookie = %d, want 200", rec.Code)
	}
}

// A GET or HEAD with a query string that the page does not take maps to the
// same key, and is served from the cache with the limit full.
func TestGuestHitUnknownQuery(t *testing.T) {
	h, s, g := guestHitSite(t)
	if rec := get(t, h, "/t/1"); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	fill(t, s)
	hits := g.Pages.Stats().Hits
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := sendWith(h, method, "/t/1?utm_source=x&ref=y", nil, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("%s with an unknown query, limit full = %d, want 200", method, rec.Code)
		}
		if method == http.MethodHead && rec.Body.Len() != 0 {
			t.Errorf("HEAD wrote a body of %d bytes", rec.Body.Len())
		}
	}
	if n := g.Pages.Stats().Hits - hits; n != 2 {
		t.Errorf("hits rose by %d, want 2", n)
	}
}
