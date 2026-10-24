package web

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// A guest who may not download shows an inline attachment image as a note, not
// as a broken image.
func TestInlineImageNote(t *testing.T) {
	h, _ := smallSite(t)
	p2 := article(t, bodyOf(t, get(t, h, "/t/1")), 2)
	if strings.Contains(p2, `<img src="/a/1"`) {
		t.Errorf("a guest without account.download gets an image that cannot load:\n%s", p2)
	}
	if !strings.Contains(p2, `<span class="img-note">Image: diagram (sign in to view)</span>`) {
		t.Errorf("post 2 lacks the note:\n%s", p2)
	}

	// With the rights, the image shows.
	pool, dataDir := boardtest.Load(t, bundletest.Small())
	if _, err := pool.Exec(t.Context(), `insert into acl_group_grants (group_id, forum_id, option, setting)
		values (1, 0, 'account.download', 3), (1, 2, 'attach.download', 3)`); err != nil {
		t.Fatal(err)
	}
	h2, err := New(t.Context(), pool, Options{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	p2 = article(t, bodyOf(t, get(t, h2, "/t/1")), 2)
	if !strings.Contains(p2, `<img src="/a/1"`) || strings.Contains(p2, "img-note") {
		t.Errorf("a guest with the rights should get the image:\n%s", p2)
	}
	// Only the board-wide right is not enough: the forum right is needed too.
	if _, err := pool.Exec(t.Context(), `delete from acl_group_grants where option = 'attach.download'`); err != nil {
		t.Fatal(err)
	}
	p2 = article(t, bodyOf(t, get(t, h2, "/t/1")), 2)
	if strings.Contains(p2, `<img src="/a/1"`) || !strings.Contains(p2, "img-note") {
		t.Errorf("a guest without attach.download should get the note:\n%s", p2)
	}
}

// A marker that points at itself is a missing topic, not a redirect loop.
func TestMarkerPointsAtItself(t *testing.T) {
	pool, dataDir := boardtest.Load(t, richBoard())
	h, err := New(t.Context(), pool, Options{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(t, h, "/t/3"); rec.Code != http.StatusMovedPermanently {
		t.Fatalf("/t/3 before the change: %d, want 301", rec.Code)
	}
	if _, err := pool.Exec(t.Context(), `update topics set moved_to_topic_id = 3 where id = 3`); err != nil {
		t.Fatal(err)
	}
	rec := get(t, h, "/t/3")
	if rec.Code != http.StatusNotFound || rec.Header().Get("Location") != "" {
		t.Errorf("/t/3 pointing at itself: %d, Location %q; want 404", rec.Code, rec.Header().Get("Location"))
	}
}

// An ID that is not in its canonical form is not a page. The page of the
// canonical form is never reached by a second spelling.
func TestNonCanonicalIDs(t *testing.T) {
	h, _ := smallSite(t)
	for _, p := range []string{"/t/+1", "/t/01", "/t/001", "/f/+2", "/f/02", "/p/+4", "/p/04", "/a/+1", "/a/01", "/t/1_0", "/t/0x1"} {
		if rec := get(t, h, p); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, rec.Code)
		}
	}
	for _, p := range []string{"/t/1", "/f/2", "/p/4"} {
		if rec := get(t, h, p); rec.Code != http.StatusOK && rec.Code != http.StatusFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
}

// A topic, a post and an attachment in a forum that the viewer may list but not
// read look the same as the ones that do not exist: the same status, the same
// bytes, no 403, no redirect.
func TestListedNotReadableLooksMissing(t *testing.T) {
	b := bundletest.Small()
	b.Grants = append(b.Grants, guestGrant(3, "forum.list"))
	pool, dataDir := boardtest.Load(t, b)
	// Attachment 1 moves to post 5, in forum 3. The guest also lacks the download
	// right, so a 403 would show that the attachment exists.
	if _, err := pool.Exec(t.Context(), `update attachments set post_id = 5 where id = 1`); err != nil {
		t.Fatal(err)
	}
	h, err := New(t.Context(), pool, Options{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ hidden, missing string }{
		{"/t/2", "/t/99"},
		{"/t/3", "/t/99"}, // the marker in the locked forum
		{"/p/5", "/p/99"},
		{"/p/6", "/p/99"},
		{"/a/1", "/a/99"},
		{"/f/3", "/f/99"},
	} {
		hid, mis := get(t, h, c.hidden), get(t, h, c.missing)
		if hid.Code != http.StatusNotFound || mis.Code != http.StatusNotFound {
			t.Errorf("%s and %s: status %d and %d, want 404 and 404", c.hidden, c.missing, hid.Code, mis.Code)
			continue
		}
		if hid.Header().Get("Location") != "" {
			t.Errorf("%s: Location %q", c.hidden, hid.Header().Get("Location"))
		}
		// The headers that tell pages apart must match too.
		for _, k := range []string{"Content-Type", "Cache-Control", "Content-Length"} {
			if hid.Header().Get(k) != mis.Header().Get(k) {
				t.Errorf("%s: header %s = %q, missing page has %q", c.hidden, k, hid.Header().Get(k), mis.Header().Get(k))
			}
		}
		if shape(bodyOf(t, hid), c.hidden) != shape(bodyOf(t, mis), c.missing) {
			t.Errorf("%s: the page differs from the page of %s", c.hidden, c.missing)
		}
	}
}

// headingLevels returns the levels of the headings in body, in order.
func headingLevels(body string) []int {
	var out []int
	for _, m := range regexp.MustCompile(`<h([1-6])[\s>]`).FindAllStringSubmatch(body, -1) {
		n, _ := strconv.Atoi(m[1])
		out = append(out, n)
	}
	return out
}

// The pages have no landmark that repeats a name, pagers with their own names,
// a heading for each post, and no jump in the heading order.
func TestPageStructure(t *testing.T) {
	b := bundletest.Large(130, 60)
	b.Posts[1].Body.Source = "# Level one in a post\n\ntext" // the second post of topic 1
	h, _ := newTestSite(t, b, Options{})
	for _, p := range []string{"/", "/f/2", "/f/2?page=2", "/t/1", "/t/1?page=2"} {
		body := bodyOf(t, get(t, h, p))
		prev := 0
		for _, l := range headingLevels(body) {
			if l > prev+1 {
				t.Errorf("%s: a level %d heading follows level %d", p, l, prev)
			}
			prev = l
		}
		if n := strings.Count(body, "<h1"); n != 1 {
			t.Errorf("%s: %d h1 elements, want 1", p, n)
		}
		if strings.Contains(body, "<aside") {
			t.Errorf("%s: the page has an aside landmark", p)
		}
		labels := regexp.MustCompile(`<nav class="pager" aria-label="([^"]*)"`).FindAllStringSubmatch(body, -1)
		if p != "/" && len(labels) != 2 {
			t.Errorf("%s: %d pagers, want 2", p, len(labels))
		}
		seen := map[string]bool{}
		for _, m := range labels {
			if seen[m[1]] {
				t.Errorf("%s: two pagers are both named %q", p, m[1])
			}
			seen[m[1]] = true
		}
	}
	body := bodyOf(t, get(t, h, "/t/1"))
	arts := strings.Split(body, "<article ")[1:]
	if len(arts) != 25 {
		t.Fatalf("%d articles", len(arts))
	}
	for i, a := range arts {
		a, _, _ = strings.Cut(a, "</article>")
		if !strings.Contains(a, "<h2") {
			t.Errorf("article %d has no h2 heading", i+1)
		}
	}
	// The headings that repeat the topic title are hidden from the eye only.
	if !strings.Contains(body, `<h2 class="visually-hidden">Post by <bdi>`) {
		t.Error("no hidden post heading")
	}
	// A level 1 heading in a post sits under the post heading.
	if !strings.Contains(article(t, body, 2), "Level one in a post") {
		t.Error("the heading of post 2 is missing")
	}
}

// A request that is not a navigation (an image load, a prefetch) has no
// bookkeeping to write, so the page is built once. A navigation builds it twice:
// once to see that it builds, once after the read marks, for the nav count.
func TestNonNavigationBuildsTheTopicOnce(t *testing.T) {
	h, _ := siteWithPool(t, pagedBoard(), Options{})
	carol := signIn(t, h, "carol", "example-password")
	count := func(hdr map[string]string) int64 {
		before := layoutBuilds.Load()
		if rec := getWith(t, h, "/t/1", carol, hdr); rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		return layoutBuilds.Load() - before
	}
	if n := count(asImage); n != 1 {
		t.Errorf("an image load built the page %d times, want 1", n)
	}
	if n := count(map[string]string{"Sec-Fetch-Dest": "document", "Sec-Purpose": "prefetch"}); n != 1 {
		t.Errorf("a prefetch built the page %d times, want 1", n)
	}
	if n := count(asPage); n != 2 {
		t.Errorf("a navigation built the page %d times, want 2", n)
	}
}
