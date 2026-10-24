package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// allowGroup grants option to group on forum (0 for the whole board) in b.
func allowGroup(b *bundletest.Board, group, forum int64, option string) {
	s := bundle.Yes
	b.Grants = append(b.Grants, bundle.Grant{GroupID: &group, ForumID: forum, Option: &option, Setting: &s})
}

// searchSiteBoard is the small board where guests and members may search
// forum 2, and admins (alice) may search forum 3 too.
func searchSiteBoard() *bundletest.Board {
	b := bundletest.Small()
	allowGroup(b, 1, 0, "account.search")
	allowGroup(b, 1, 2, "forum.search")
	allowGroup(b, 2, 0, "account.search")
	allowGroup(b, 2, 2, "forum.search")
	allowGroup(b, 4, 3, "forum.search")
	return b
}

func TestSearchPage(t *testing.T) {
	// Without account.search, a guest gets 403.
	h, _ := smallSite(t)
	rec := get(t, h, "/search?q=example")
	if rec.Code != http.StatusForbidden {
		t.Errorf("guest without the right: status %d, want 403", rec.Code)
	}
	if strings.Contains(bodyOf(t, rec), "Welcome") {
		t.Error("the 403 page shows a topic title")
	}
	// The 403 comes before the checks of the query.
	for _, target := range []string{"/search?q=" + strings.Repeat("a", 257), "/search?q=x&page=41"} {
		if rec := get(t, h, target); rec.Code != http.StatusForbidden {
			t.Errorf("guest without the right, %.40s: status %d, want 403", target, rec.Code)
		}
	}
	// And the nav holds no search box.
	if strings.Contains(bodyOf(t, get(t, h, "/")), "/search") {
		t.Error("a viewer without account.search sees a search link")
	}

	h, _ = newTestSite(t, searchSiteBoard(), Options{})
	rec = get(t, h, "/search?q=example")
	body := bodyOf(t, rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(body, `href="/p/1"`) || !strings.Contains(body, "Welcome to the example board") {
		t.Errorf("the hit for post 1 is missing:\n%s", body)
	}
	if !strings.Contains(body, "<mark>example</mark>") {
		t.Error("the snippet does not mark the word")
	}
	if strings.Contains(body, `href="/p/3"`) {
		t.Error("the unapproved post 3 is listed")
	}
	// The author of the hit has a profile link, in <bdi>.
	if !strings.Contains(body, `<a href="/u/1"><bdi>alice</bdi></a>`) {
		t.Error("the author has no profile link")
	}
	// The forum of the hit links to the forum.
	if !strings.Contains(body, `href="/f/2"`) || !strings.Contains(body, "Main Discussion") {
		t.Error("the hit lacks its forum")
	}
	if !strings.Contains(body, "<time datetime=") {
		t.Error("the hit lacks a time")
	}
	// The nav has the search box, and the page holds the form.
	for _, want := range []string{`role="search"`, `action="/search"`, `name="q"`, `name="author"`, `name="forum"`, `name="sort"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	// The form keeps the query.
	if !regexp.MustCompile(`name="q"[^>]*value="example"`).MatchString(body) {
		t.Error("the form lost the query")
	}

	for _, c := range []struct {
		target string
		code   int
	}{
		{"/search?q=" + strings.Repeat("a", 257), http.StatusBadRequest},
		{"/search?q=" + strings.Repeat("a", 256), http.StatusOK},
		{"/search?q=example&page=41", http.StatusNotFound},
		{"/search?q=example&page=2", http.StatusNotFound},
		{"/search?q=example&page=0", http.StatusBadRequest},
		{"/search?q=example&page=abc", http.StatusBadRequest},
		{"/search?q=example&page=" + strings.Repeat("9", 30), http.StatusBadRequest},
		{"/search?q=example&forum=abc", http.StatusBadRequest},
		{"/search?q=example&forum=-1", http.StatusBadRequest},
		{"/search?q=example&forum=3", http.StatusOK},
		{"/search?q=example&sort=oldest", http.StatusOK},
		{"/search", http.StatusOK},
		{"/search?q=", http.StatusOK},
	} {
		if rec := get(t, h, c.target); rec.Code != c.code {
			t.Errorf("GET %.50s: status %d, want %d", c.target, rec.Code, c.code)
		}
	}
	// The form alone says nothing about results.
	body = bodyOf(t, get(t, h, "/search"))
	if strings.Contains(body, "No posts") || strings.Contains(body, "search-hit") {
		t.Error("the empty form shows results")
	}
	// A query with no hits says so.
	if body := bodyOf(t, get(t, h, "/search?q=zzzzzz")); !strings.Contains(body, "No posts match") {
		t.Error("no hits: no message")
	}
}

func TestSearchPageForumSelect(t *testing.T) {
	h, _ := newTestSite(t, searchSiteBoard(), Options{})
	sel := func(body string) string {
		i := strings.Index(body, `<select id="s-forum"`)
		if i < 0 {
			t.Fatalf("no forum select in:\n%s", body)
		}
		j := strings.Index(body[i:], "</select>")
		return body[i : i+j]
	}
	guest := sel(bodyOf(t, get(t, h, "/search")))
	if !strings.Contains(guest, `value="2"`) || strings.Contains(guest, `value="3"`) || strings.Contains(guest, "Members Lounge") {
		t.Errorf("guest select:\n%s", guest)
	}
	if !strings.Contains(guest, `value="">All forums`) && !strings.Contains(guest, `value="" selected>All forums`) {
		t.Errorf("no All forums option:\n%s", guest)
	}
	alice := signIn(t, h, "alice", "example-password")
	got := sel(bodyOf(t, getAs(t, h, "/search", alice)))
	if !strings.Contains(got, `value="2"`) || !strings.Contains(got, `value="3"`) || !strings.Contains(got, "Members Lounge") {
		t.Errorf("alice select:\n%s", got)
	}
	// The chosen forum is selected.
	got = sel(bodyOf(t, getAs(t, h, "/search?forum=3", alice)))
	if !regexp.MustCompile(`value="3"\s+selected`).MatchString(got) {
		t.Errorf("forum 3 is not selected:\n%s", got)
	}
}

func TestSearchPageHidden(t *testing.T) {
	h, _ := newTestSite(t, searchSiteBoard(), Options{})
	// A guest cannot read forum 3: no hit, no title, whatever the filter.
	for _, target := range []string{"/search?q=color", "/search?q=color&forum=3", "/search?author=bob", "/search?q=favorite"} {
		rec := get(t, h, target)
		body := bodyOf(t, rec)
		if rec.Code != http.StatusOK || strings.Contains(body, "Favorite color") || strings.Contains(body, `href="/p/5"`) || strings.Contains(body, `href="/p/6"`) {
			t.Errorf("GET %s: status %d, leaks forum 3", target, rec.Code)
		}
	}
	// Alice finds them.
	alice := signIn(t, h, "alice", "example-password")
	body := bodyOf(t, getAs(t, h, "/search?q=color", alice))
	if !strings.Contains(body, `href="/p/5"`) || !strings.Contains(body, `href="/p/6"`) {
		t.Error("alice does not find the posts of forum 3")
	}
}

func TestSearchAuthorLinks(t *testing.T) {
	h, _ := newTestSite(t, searchSiteBoard(), Options{})
	// The profile page links to a search by author, with no text.
	alice := signIn(t, h, "alice", "example-password")
	profile := bodyOf(t, getAs(t, h, "/u/2", alice))
	m := regexp.MustCompile(`href="(/search\?[^"]*)"`).FindStringSubmatch(profile)
	if m == nil {
		t.Fatalf("the profile has no search link:\n%s", profile)
	}
	link := strings.ReplaceAll(m[1], "&amp;", "&")
	rec := getAs(t, h, link, alice)
	body := bodyOf(t, rec)
	if rec.Code != http.StatusOK || !strings.Contains(body, `href="/p/2"`) || !strings.Contains(body, `href="/p/5"`) {
		t.Errorf("GET %s: status %d, body lacks bob's posts", link, rec.Code)
	}
	if strings.Contains(body, `href="/p/1"`) || strings.Contains(body, `href="/p/6"`) {
		t.Error("the author search lists the posts of another member")
	}
	// A guest post and a gone author show the name and no profile link.
	body = bodyOf(t, get(t, h, "/search?q=guest"))
	if !strings.Contains(body, `href="/p/4"`) || !strings.Contains(body, "<bdi>Visitor</bdi>") {
		t.Fatalf("the guest post is not listed:\n%s", body)
	}
	i := strings.Index(body, "<bdi>Visitor</bdi>")
	if j := strings.LastIndex(body[:i], "<a "); j >= 0 && strings.Contains(body[j:i], `href="/u/`) && !strings.Contains(body[j:i], "</a>") {
		t.Error("the guest author has a profile link")
	}
}

func TestSearchNoXSS(t *testing.T) {
	b := searchSiteBoard()
	b.Posts[5].Body = bundle.Body{Source: "<script>alert(1)</script> needle", Markdown: true}
	h, _ := newTestSite(t, b, Options{})
	alice := signIn(t, h, "alice", "example-password")
	body := bodyOf(t, getAs(t, h, "/search?q=needle", alice))
	if strings.Contains(body, "<script>alert") || !strings.Contains(body, "&lt;script&gt;") {
		t.Error("the snippet is not escaped")
	}
	// The query comes back escaped in the form and in the page.
	q := `"><script>alert(2)</script>`
	body = bodyOf(t, getAs(t, h, "/search?"+url.Values{"q": {q}, "author": {q}}.Encode(), alice))
	if strings.Contains(body, "<script>alert(2)") {
		t.Error("the query is not escaped")
	}
}

func TestSearchPager(t *testing.T) {
	b := searchSiteBoard()
	for i := range 60 {
		b.Posts = append(b.Posts, bundlePost(int64(1000+i), 1, 2, "bob", at(2022, 1, 1, i/60, i%60), "gizmo here"))
	}
	h, _ := newTestSite(t, b, Options{})
	body := bodyOf(t, get(t, h, "/search?q=gizmo&sort=best"))
	if !strings.Contains(body, `class="pager"`) {
		t.Fatal("no pager")
	}
	// The links keep the whole query and add the page. Page 1 has no page.
	if !strings.Contains(body, `href="/search?q=gizmo&amp;sort=best&amp;page=2"`) {
		t.Errorf("no link to page 2 with the query:\n%s", body)
	}
	if strings.Contains(body, "page=1") {
		t.Error("a link to page 1 holds page=1")
	}
	rec := get(t, h, "/search?q=gizmo&sort=best&page=3")
	if rec.Code != http.StatusOK || !strings.Contains(bodyOf(t, rec), `aria-current="page">3<`) {
		t.Errorf("page 3: status %d", rec.Code)
	}
	if rec := get(t, h, "/search?q=gizmo&page=4"); rec.Code != http.StatusNotFound {
		t.Errorf("page 4: status %d, want 404", rec.Code)
	}
}

func TestSearchNavBox(t *testing.T) {
	h, _ := newTestSite(t, searchSiteBoard(), Options{})
	body := bodyOf(t, get(t, h, "/"))
	i := strings.Index(body, `role="search"`)
	if i < 0 {
		t.Fatal("no search box in the nav of the index")
	}
	if !strings.Contains(body[:i], `class="site-nav"`) {
		t.Error("the search box is not in the nav")
	}
	// The box has a label for assistive technology.
	if !regexp.MustCompile(`<label[^>]*for="nav-q"[^>]*>`).MatchString(body) {
		t.Error("the nav search field has no label")
	}
}

// A query that search refuses answers 400 with the form and a message, and never
// a 500. The server checks the bytes; maxlength counts UTF-16 units.
func TestSearchRefusals(t *testing.T) {
	h, _ := newTestSite(t, searchSiteBoard(), Options{})
	words := strings.Join(strings.Fields("apple banana cherry damson elder fig grape hazel iris jasmine kale lemon mango nectar olive peach quince"), "+or+")
	for _, c := range []struct {
		target, message string
	}{
		{"/search?q=%FF", "cannot use"},
		{"/search?q=a%00b", "cannot use"},
		{"/search?author=a%00b", "cannot use"},
		{"/search?q=example&author=%C3", "cannot use"},
		{"/search?q=" + strings.Repeat("a", 257), "256 bytes"},
		{"/search?author=" + strings.Repeat("a", 257), "256 bytes"},
		{"/search?q=" + strings.Repeat("%C3%A9", 129), "256 bytes"},
		{"/search?q=" + words, "at most 16"},
	} {
		rec := get(t, h, c.target)
		body := bodyOf(t, rec)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %.60s: status %d, want 400", c.target, rec.Code)
			continue
		}
		if !strings.Contains(body, c.message) || !strings.Contains(body, `name="q"`) || !strings.Contains(body, `id="s-forum"`) {
			t.Errorf("GET %.60s: the page lacks the message %q or the form", c.target, c.message)
		}
		if strings.Contains(body, "\x00") || strings.Contains(body, "\xff") {
			t.Errorf("GET %.60s: the page echoes the bad bytes", c.target)
		}
	}
	// The page and forum values hold ASCII digits only.
	for _, target := range []string{"/search?q=x&forum=%2B2", "/search?q=x&forum=2%20", "/search?q=x&forum=0x2", "/search?q=x&forum=" + strings.Repeat("9", 30)} {
		if rec := get(t, h, target); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s: status %d, want 400", target, rec.Code)
		}
	}
	// A query with no positive term is no error: the form and a hint.
	for _, target := range []string{"/search?q=-example", "/search?q=-example&author=alice", "/search?q=example+or+-welcome"} {
		rec := get(t, h, target)
		body := bodyOf(t, rec)
		if rec.Code != http.StatusOK || !strings.Contains(body, "Add a word to find.") || strings.Contains(body, "search-hit") || strings.Contains(body, "No posts match") {
			t.Errorf("GET %s: status %d, body lacks the hint or lists hits", target, rec.Code)
		}
	}
	// The same admins who follow the profile link still find the author's posts.
	if rec := get(t, h, "/search?author=alice"); rec.Code != http.StatusOK || !strings.Contains(bodyOf(t, rec), `href="/p/1"`) {
		t.Errorf("author alone: status %d", rec.Code)
	}
}
