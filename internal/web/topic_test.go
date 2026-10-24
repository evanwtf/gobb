package web

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// article returns the HTML of the article element of post id.
func article(t *testing.T, body string, id int) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)<article[^>]*id="p` + strconv.Itoa(id) + `".*?</article>`)
	s := re.FindString(body)
	if s == "" {
		t.Fatalf("no article for post %d", id)
	}
	return s
}

func TestTopicPage(t *testing.T) {
	// Guests may download here, so post 2 shows its attachment as an image.
	b := bundletest.Small()
	b.Grants = append(b.Grants, guestGrant(0, "account.download"), guestGrant(2, "attach.download"))
	h, _ := newTestSite(t, b, Options{})
	rec := get(t, h, "/t/1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := bodyOf(t, rec)
	for _, want := range []string{
		`<strong>example</strong>`,
		`id="p1"`, `id="p2"`, `id="p4"`,
		`Example signature`,
		`<time datetime="2019-05-02T09:00:00Z">May 2, 2019, 5:00 am</time>`,
		`Welcome to the example board`,
		`Newcomer`, // the rank
		`src="/files/ranks/newcomer.png"`,
		`src="/files/avatars/1.png"`,
		`Posts: <b>120</b>`, // the post count, labelled
		`May 1, 2019`,       // the join date of alice
		`Edited`, `typo`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
	if strings.Contains(body, `id="p3"`) || strings.Contains(body, "waits for a moderator") {
		t.Error("the guest sees the unapproved post")
	}
	if !strings.Contains(article(t, body, 2), `src="/a/1"`) {
		t.Error("post 2 does not show its inline image at /a/1")
	}
	p4 := article(t, body, 4)
	if !strings.Contains(p4, `<bdi>Visitor</bdi>`) || strings.Contains(p4, `/u/`) {
		t.Errorf("post 4 should show Visitor in bdi with no profile link:\n%s", p4)
	}
	if !strings.Contains(p4, "Guest") {
		t.Error("post 4 lacks the word Guest")
	}
	if !strings.Contains(article(t, body, 1), `href="/u/1"`) {
		t.Error("post 1 has no link to the profile of alice")
	}
	// The signature belongs to post 1 only: post 2 does not ask for it.
	if strings.Contains(article(t, body, 2), "Example signature") {
		t.Error("post 2 shows a signature")
	}
	// No IP for a guest.
	if strings.Contains(body, "10.0.0.5") {
		t.Error("the page shows an IP address")
	}
	// Posts hold no attachment list when the image is inline.
	if strings.Contains(article(t, body, 2), "diagram.png") {
		t.Error("post 2 lists an attachment that it shows inline")
	}
	if !strings.Contains(body, `<h1`) {
		t.Error("no h1")
	}
}

func TestTopicPageAttachmentList(t *testing.T) {
	b := bundletest.Small()
	for i := range b.Posts {
		if b.Posts[i].ID == 2 {
			b.Posts[i].Body.Source = "No picture here."
		}
	}
	h, _ := newTestSite(t, b, Options{})
	p2 := article(t, bodyOf(t, get(t, h, "/t/1")), 2)
	for _, want := range []string{`href="/a/1"`, "diagram.png", "A diagram"} {
		if !strings.Contains(p2, want) {
			t.Errorf("post 2 lacks %q:\n%s", want, p2)
		}
	}
}

func TestTopicSmiley(t *testing.T) {
	b := bundletest.Small()
	b.Grants = append(b.Grants, guestGrant(3, "forum.list"), guestGrant(3, "forum.read"))
	h, _ := newTestSite(t, b, Options{})
	rec := get(t, h, "/t/2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(bodyOf(t, rec), `<img class="smiley" src="/files/smilies/smile.gif"`) {
		t.Error("the smiley image is missing")
	}
}

func TestTopicPoll(t *testing.T) {
	b := bundletest.Small()
	b.Grants = append(b.Grants, guestGrant(3, "forum.list"), guestGrant(3, "forum.read"))
	h, _ := newTestSite(t, b, Options{})
	body := bodyOf(t, get(t, h, "/t/2"))
	for _, want := range []string{
		"Favorite color?", "Green", "Blue", "Red",
		`<progress`, `value="3"`, `max="5"`,
		"5 votes",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the poll lacks %q", want)
		}
	}
	// The poll shows once, above the posts.
	if strings.Count(body, `class="poll"`) != 1 {
		t.Errorf("the page has %d polls", strings.Count(body, `class="poll"`))
	}
}

// The parts of the markup each get the HTML that the style sheet depends on.
func TestTopicPageRichPost(t *testing.T) {
	h, _ := newTestSite(t, richBoard(), Options{})
	rec := get(t, h, "/t/5")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	p7 := article(t, bodyOf(t, rec), 7)
	for _, want := range []string{
		`<blockquote>`, `a quoted line`,
		`href="/p/1"`, `href="/t/1"`,
		`<img class="smiley" src="/files/smilies/smile.gif"`,
		`<img src="/a/3"`,
		`<iframe class="embed youtube" src="https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ"`,
		`<span class="spoiler"`,
		`<ul>`,
		`<pre class="code"><code class="language-go">`,
		`Example signature`,
	} {
		if !strings.Contains(p7, want) {
			t.Errorf("post 7 lacks %q:\n%s", want, p7)
		}
	}
}

func TestTopicNotFound(t *testing.T) {
	h, _ := smallSite(t)
	// Topic 2 is in a forum that the guest cannot read; it looks like a missing topic.
	hidden, missing := get(t, h, "/t/2"), get(t, h, "/t/99")
	if hidden.Code != http.StatusNotFound || missing.Code != http.StatusNotFound {
		t.Fatalf("status %d and %d, want 404", hidden.Code, missing.Code)
	}
	if shape(bodyOf(t, hidden), "/t/2") != shape(bodyOf(t, missing), "/t/99") {
		t.Error("a hidden topic differs from a missing topic")
	}
	for _, p := range []string{"/t/abc", "/t/-1", "/t/0", "/f/abc", "/p/abc", "/a/abc", "/t/99999999999999999999"} {
		if rec := get(t, h, p); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, rec.Code)
		}
	}
}

func TestRedirects(t *testing.T) {
	b := bundletest.Small()
	b.Grants = append(b.Grants, guestGrant(3, "forum.list"), guestGrant(3, "forum.read"))
	h, _ := newTestSite(t, b, Options{})
	for _, c := range []struct {
		from   string
		status int
		to     string
	}{
		{"/t/3", http.StatusMovedPermanently, "/t/1"},
		{"/p/4", http.StatusFound, "/t/1#p4"},
		{"/p/1", http.StatusFound, "/t/1#p1"},
		{"/p/5", http.StatusFound, "/t/2#p5"}, // forum 3 is readable in this board
	} {
		rec := get(t, h, c.from)
		if rec.Code != c.status || rec.Header().Get("Location") != c.to {
			t.Errorf("%s: %d %q, want %d %q", c.from, rec.Code, rec.Header().Get("Location"), c.status, c.to)
		}
	}
	for _, p := range []string{"/p/3", "/p/99", "/p/0"} {
		rec := get(t, h, p)
		if rec.Code != http.StatusNotFound || rec.Header().Get("Location") != "" {
			t.Errorf("%s: %d, Location %q; want 404 and none", p, rec.Code, rec.Header().Get("Location"))
		}
	}

	// With forum 3 hidden, post 5 is missing, and so is the marker in forum 3.
	h2, _ := smallSite(t)
	for _, p := range []string{"/p/5", "/t/3"} {
		rec := get(t, h2, p)
		if rec.Code != http.StatusNotFound || rec.Header().Get("Location") != "" {
			t.Errorf("%s with forum 3 hidden: %d, Location %q; want 404 and none", p, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestAttachmentRights(t *testing.T) {
	pool, dataDir := boardtest.Load(t, bundletest.Small())
	h, err := New(t.Context(), pool, Options{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(t, h, "/a/1"); rec.Code != http.StatusForbidden {
		t.Errorf("guest /a/1: %d, want 403", rec.Code)
	}
	if rec := get(t, h, "/a/2"); rec.Code != http.StatusNotFound {
		t.Errorf("guest /a/2: %d, want 404", rec.Code)
	}
	if rec := get(t, h, "/a/99"); rec.Code != http.StatusNotFound {
		t.Errorf("guest /a/99: %d, want 404", rec.Code)
	}
	// 403 only for a viewer who may see the post. A hidden post stays 404.
	if _, err := pool.Exec(t.Context(), `update posts set visibility = 'unapproved' where id = 2`); err != nil {
		t.Fatal(err)
	}
	if rec := get(t, h, "/a/1"); rec.Code != http.StatusNotFound {
		t.Errorf("guest /a/1 on an unapproved post: %d, want 404", rec.Code)
	}
	if _, err := pool.Exec(t.Context(), `update posts set visibility = 'approved' where id = 2`); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(t.Context(), `insert into acl_group_grants (group_id, forum_id, option, setting)
		values (1, 0, 'account.download', 3), (1, 2, 'attach.download', 3)`); err != nil {
		t.Fatal(err)
	}
	rec := get(t, h, "/a/1")
	if rec.Code != http.StatusOK {
		t.Fatalf("guest /a/1 with the grants: %d", rec.Code)
	}
	want, err := os.ReadFile(filepath.Join(dataDir, "attachments", "1.png"))
	if err != nil {
		t.Fatal(err)
	}
	if got := bodyOf(t, rec); got != string(want) {
		t.Errorf("body is %d bytes, want the %d bytes of the file", len(got), len(want))
	}
	hd := rec.Header()
	for k, v := range map[string]string{
		"Content-Type":           "image/png",
		"X-Content-Type-Options": "nosniff",
		"Cache-Control":          "private, no-cache",
		"Content-Disposition":    "inline; filename*=UTF-8''diagram.png",
	} {
		if got := hd.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	var count int
	if err := pool.QueryRow(t.Context(), `select download_count from attachments where id = 1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 7 {
		t.Errorf("download_count = %d, want 7", count)
	}
	// A guest still may not fetch the orphan.
	if rec := get(t, h, "/a/2"); rec.Code != http.StatusNotFound {
		t.Errorf("guest /a/2: %d, want 404", rec.Code)
	}
	// HEAD works and sends no body.
	if rec := do(t, h, http.MethodHead, "/a/1"); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD /a/1: %d, %d bytes", rec.Code, rec.Body.Len())
	}
}

// An attachment type that a browser would run on our origin is sent as a
// download, in a sandbox.
func TestAttachmentUnsafeType(t *testing.T) {
	pool, dataDir := boardtest.Load(t, bundletest.Small())
	h, err := New(t.Context(), pool, Options{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for _, q := range []string{
		`insert into acl_group_grants (group_id, forum_id, option, setting) values (1, 0, 'account.download', 3), (1, 2, 'attach.download', 3)`,
		`update attachments set mime = 'text/html', real_name = 'né"w.html' where id = 1`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	rec := get(t, h, "/a/1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment; filename*=UTF-8''n%C3%A9%22w.html") {
		t.Errorf("Content-Disposition = %q", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "sandbox") {
		t.Errorf("Content-Security-Policy = %q", got)
	}
}

func TestNamesInBdi(t *testing.T) {
	pool, dataDir := boardtest.Load(t, richBoard())
	name := "al\u202eice"
	if _, err := pool.Exec(t.Context(), `update users set username = $1 where id = 1`, name); err != nil {
		t.Fatal(err)
	}
	h, err := New(t.Context(), pool, Options{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/", "/f/2", "/t/1", "/t/5"} {
		body := bodyOf(t, get(t, h, p))
		all := strings.Count(body, name)
		inBdi := strings.Count(body, "<bdi>"+name+"</bdi>")
		if all != inBdi {
			t.Errorf("%s: the name appears %d times, %d of them in <bdi>", p, all, inBdi)
		}
		if p != "/" && all == 0 {
			t.Errorf("%s: the name does not appear", p)
		}
	}
}

func TestPaginationBounds(t *testing.T) {
	h, _ := newTestSite(t, bundletest.Large(3, 60), Options{})
	count := func(body string) int { return strings.Count(body, "<article ") }

	rec := get(t, h, "/t/1?page=3")
	if rec.Code != http.StatusOK {
		t.Fatalf("page 3: %d", rec.Code)
	}
	if n := count(bodyOf(t, rec)); n != 10 {
		t.Errorf("page 3 has %d articles, want 10", n)
	}
	if n := count(bodyOf(t, get(t, h, "/t/1"))); n != 25 {
		t.Errorf("page 1 has %d articles, want 25", n)
	}
	if rec := get(t, h, "/t/1?page=4"); rec.Code != http.StatusNotFound {
		t.Errorf("page 4: %d, want 404", rec.Code)
	}
	for _, q := range []string{"0", "-1", "abc", "", "99999999999999999999999999999a", strings.Repeat("9", 30), "1.5", "+2", "%20"} {
		want := http.StatusBadRequest
		if q == "" {
			want = http.StatusOK // an empty value is page 1
		}
		for _, p := range []string{"/t/1?page=", "/f/2?page="} {
			if rec := get(t, h, p+q); rec.Code != want {
				t.Errorf("%s%s: %d, want %d", p, q, rec.Code, want)
			}
		}
	}
	// The "new since" list has the same bounds. A guest goes to sign in first, so
	// a member asks.
	alice := signIn(t, h, "alice", "example-password")
	for _, q := range []string{"0", "-1", "abc", strings.Repeat("9", 30), "+2", "%20"} {
		if rec := getAs(t, h, "/new?page="+q, alice); rec.Code != http.StatusBadRequest {
			t.Errorf("/new?page=%s: %d, want 400", q, rec.Code)
		}
	}
	if rec := getAs(t, h, "/new?page=", alice); rec.Code != http.StatusOK {
		t.Errorf("/new?page=: %d, want 200", rec.Code)
	}
	if rec := getAs(t, h, "/new?page=2", alice); rec.Code != http.StatusNotFound {
		t.Errorf("/new?page=2: %d, want 404", rec.Code)
	}
	// The forum list has pages too: 3 topics are on one page; page 2 is missing.
	if rec := get(t, h, "/f/2?page=2"); rec.Code != http.StatusNotFound {
		t.Errorf("/f/2?page=2: %d, want 404", rec.Code)
	}
	rec = get(t, h, "/p/30")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/t/1?page=2#p30" {
		t.Errorf("/p/30: %d %q, want 302 to /t/1?page=2#p30", rec.Code, rec.Header().Get("Location"))
	}
	// The pager links page 2 of the topic, and page 1 has no ?page=.
	body := bodyOf(t, get(t, h, "/t/1?page=2"))
	for _, want := range []string{`href="/t/1?page=3"`, `href="/t/1"`, `aria-current="page"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page 2 lacks %q", want)
		}
	}
	if strings.Contains(body, "page=1") {
		t.Error("a link names page=1")
	}
}

// The forum list pages at 50 topics.
func TestForumPagination(t *testing.T) {
	h, _ := newTestSite(t, bundletest.Large(130, 1), Options{})
	// Forum 2 holds the 65 odd topics.
	rec := get(t, h, "/f/2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := bodyOf(t, rec)
	if n := strings.Count(body, `class="topic-row`); n != 50 {
		t.Errorf("page 1 has %d topic rows, want 50", n)
	}
	if !strings.Contains(body, `href="/f/2?page=2"`) {
		t.Error("no link to page 2")
	}
	body = bodyOf(t, get(t, h, "/f/2?page=2"))
	if n := strings.Count(body, `class="topic-row`); n != 15 {
		t.Errorf("page 2 has %d topic rows, want 15", n)
	}
	if rec := get(t, h, "/f/2?page=3"); rec.Code != http.StatusNotFound {
		t.Errorf("page 3: %d, want 404", rec.Code)
	}
}
