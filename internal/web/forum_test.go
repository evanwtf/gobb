package web

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// richBoard is bundletest.Small with a forum 4 that the guest reads and a
// topic 5 in forum 2 whose first post uses every part of the markup. Topic 6
// holds a poll and sits in forum 2 too, so a guest can see it.
func richBoard() *bundletest.Board {
	b := bundletest.Small()
	b.Grants = append(b.Grants, guestGrant(3, "forum.list"), guestGrant(3, "forum.read"),
		// Guests may download, so the images of the posts load.
		guestGrant(0, "account.download"), guestGrant(2, "attach.download"), guestGrant(3, "attach.download"))
	b.Topics = append(b.Topics,
		bundleTopic(5, 2, "Rich post", "normal", at(2021, 3, 4, 15, 0)),
		bundleTopic(6, 2, "Another poll", "normal", at(2021, 3, 5, 15, 0)),
	)
	b.Posts = append(b.Posts, bundlePost(7, 5, 1, "alice", at(2021, 3, 4, 15, 0), richBody),
		bundlePost(8, 5, 2, "bob", at(2021, 3, 4, 16, 0), "A reply with a smiley :) and code `x := 1`."),
		bundlePost(9, 6, 2, "bob", at(2021, 3, 5, 15, 0), "Vote please."))
	b.Posts[len(b.Posts)-3].ShowSignature = true
	b.Polls = append(b.Polls, bundlePoll(6))
	// An inline image of the post: attachment 3 shows in the body at /a/3.
	b.Files["attachments/3.png"] = b.Files["attachments/1.png"]
	b.Attachments = append(b.Attachments, bundle.Attachment{
		ID: 3, PostID: ptr(int64(7)), UploaderID: ptr(int64(1)), Name: "picture.png",
		File: bundletest.Ref("attachments/3.png", b.Files["attachments/3.png"]), Mime: "image/png",
		CreatedAt: at(2021, 3, 4, 15, 0),
	})
	return b
}

// richBody holds a quote, a YouTube embed, an image of the site, a link to a
// topic, a smiley, a spoiler, a list and a code block.
const richBody = "> alice wrote in [post 1](/p/1):\n> a quoted line\n\n" +
	"Look at [the first topic](/t/1) :)\n\n" +
	"![inline picture](/a/3)\n\n" +
	"https://www.youtube.com/watch?v=dQw4w9WgXcQ\n\n" +
	"||a secret||\n\n" +
	"- one\n- two\n\n" +
	"```go\nfmt.Println(\"hello\")\n```\n"

func TestForumPage(t *testing.T) {
	h, _ := newTestSite(t, richBoard(), Options{})
	rec := get(t, h, "/f/2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := bodyOf(t, rec)
	for _, want := range []string{
		`<h1`, `Main Discussion`,
		`Be kind.`, // the rules
		`href="/t/1"`, `Welcome to the example board`,
		`Sticky`,
		`<bdi>alice</bdi>`,
		`<time datetime="2020-02-01T11:00:00Z">Feb 1, 2020, 6:00 am</time>`,
		`href="/p/4"`, `<bdi>Visitor</bdi>`,
		`Rich post`, `href="/t/5"`,
		`Subforums`, // forum 3 is readable in this board
		`Members Lounge`,
		`aria-current="page"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
	// The sticky topic comes first.
	if strings.Index(body, `href="/t/1"`) > strings.Index(body, `href="/t/5"`) {
		t.Error("the sticky topic is not first")
	}
	// The row shows the replies (2) and the views (42).
	row := regexp.MustCompile(`(?s)<tr class="topic-row[^"]*">.*?href="/t/1".*?</tr>`).FindString(body)
	if row == "" {
		t.Fatal("no row for topic 1")
	}
	for _, want := range []string{`>2<`, `>42<`} {
		if !strings.Contains(row, want) {
			t.Errorf("the row of topic 1 lacks %q:\n%s", want, row)
		}
	}
	// A crumb leads back to the index and the category.
	for _, want := range []string{`href="/"`, `href="/f/1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks the crumb %q", want)
		}
	}
}

func TestForumPageMarkerAndPinned(t *testing.T) {
	b := richBoard()
	b.Topics[3].Kind = "announce" // topic 5 becomes an announcement of forum 2
	h, _ := newTestSite(t, b, Options{})
	body := bodyOf(t, get(t, h, "/f/3"))
	if !strings.Contains(body, "Moved:") || !strings.Contains(body, `href="/t/1"`) {
		t.Errorf("forum 3 does not show the moved marker with its target:\n%s", body)
	}
	body = bodyOf(t, get(t, h, "/f/2"))
	if !strings.Contains(body, "Announcement") {
		t.Error("the announcement has no marker")
	}
	if strings.Index(body, `href="/t/5"`) > strings.Index(body, `href="/t/1"`) {
		t.Error("the pinned announcement is not above the other topics")
	}
}

func TestForumCategoryPage(t *testing.T) {
	h, _ := smallSite(t)
	rec := get(t, h, "/f/1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := bodyOf(t, rec)
	if !strings.Contains(body, "Main Discussion") || strings.Contains(body, "Members Lounge") {
		t.Error("the category page does not list the forum the guest may list, and only it")
	}
}

func TestForumPageHead(t *testing.T) {
	h, _ := smallSite(t)
	for _, p := range []string{"/f/2", "/t/1"} {
		if rec := do(t, h, http.MethodHead, p); rec.Code != http.StatusOK {
			t.Errorf("HEAD %s = %d", p, rec.Code)
		}
	}
}

// A forum that the guest may list but not read is on the index with a lock and
// no numbers. Opening it answers 403 with a way to sign in that returns to the
// forum: the index already shows its name, so the 403 tells nothing new. A
// forum that the guest may not list, and every topic, post and attachment in a
// forum that the guest may not read, still answer the 404 of a missing one
// (spec 14).
func TestListedNotReadable(t *testing.T) {
	b := bundletest.Small()
	b.Grants = append(b.Grants, guestGrant(3, "forum.list"))
	h, _ := newTestSite(t, b, Options{})

	index := bodyOf(t, get(t, h, "/f/2"))
	row := rowOf(index, "forum-row", "Members Lounge")
	if row == "" {
		t.Fatalf("forum 3 is not listed on the page of forum 2:\n%s", index)
	}
	if !strings.Contains(row, "Members only") {
		t.Errorf("the row has no members-only marker:\n%s", row)
	}
	if strings.Contains(row, `href="/f/3"`) || strings.Contains(row, "No posts yet") || strings.Contains(row, "forum-count") {
		t.Errorf("the row links the forum, or shows counts or a last post:\n%s", row)
	}
	if strings.Contains(row, "A quiet room") {
		t.Errorf("the row shows the description of a forum the guest cannot read:\n%s", row)
	}

	// The same holds on the board index for a top-level forum.
	b2 := bundletest.Small()
	b2.Forums = append(b2.Forums, bundleForum(4, 0, "forum", "Staff Room", 5))
	b2.Grants = append(b2.Grants, guestGrant(4, "forum.list"))
	// Attachment 1 moves to a post of topic 2, in forum 3, which the guest may
	// neither list nor read on this board.
	b2.Attachments[0].PostID = ptr(int64(5))
	b2.LegacyRoutes = append(b2.LegacyRoutes, legacyRoute("old/forum", "forum", ptr("id"), nil, 0))
	h2, _ := newTestSite(t, b2, Options{})
	idx := bodyOf(t, get(t, h2, "/"))
	row = rowOf(idx, "forum-row", "Staff Room")
	if !strings.Contains(row, "Members only") || strings.Contains(row, "No posts yet") || strings.Contains(row, `href="/f/4"`) {
		t.Errorf("the index row of the locked forum is wrong:\n%s", row)
	}

	// Opening it, by its page or by an old link: 403 and a sign-in link that
	// comes back.
	for _, c := range []struct{ target, next string }{
		{"/f/4", "%2Ff%2F4"},
		{"/f/4?page=2", "%2Ff%2F4%3Fpage%3D2"},
		{"/old/forum?id=4", "%2Fold%2Fforum%3Fid%3D4"},
	} {
		rec := get(t, h2, c.target)
		body := bodyOf(t, rec)
		if rec.Code != http.StatusForbidden || !strings.Contains(body, `href="/login?next=`+c.next+`"`) || !strings.Contains(body, "Sign in to see this forum.") {
			t.Errorf("%s: status %d, want 403 with a sign-in link to %s:\n%s", c.target, rec.Code, c.next, body)
		}
	}
	// A member who may list it and not read it gets 403 with no sign-in link.
	group, list, yes := int64(2), "forum.list", bundle.Yes
	b3 := bundletest.Small()
	b3.Forums = append(b3.Forums, bundleForum(4, 0, "forum", "Staff Room", 5))
	b3.Grants = append(b3.Grants, bundle.Grant{GroupID: &group, ForumID: 4, Option: &list, Setting: &yes})
	h3, _ := newTestSite(t, b3, Options{})
	rec := getAs(t, h3, "/f/4", signIn(t, h3, "bob", testPassword))
	if body := bodyOf(t, rec); rec.Code != http.StatusForbidden || !strings.Contains(body, "You do not have the right to see this forum.") || strings.Contains(body, "/login?next=") {
		t.Errorf("member /f/4: status %d, want 403 with no sign-in link:\n%s", rec.Code, body)
	}

	// Not listed, or inside a forum the guest may not read: the 404 of a
	// missing one, byte for byte.
	for _, c := range []struct{ hidden, missing string }{
		{"/f/3", "/f/99"},
		{"/t/2", "/t/99"},
		{"/p/5", "/p/99"},
		{"/a/1", "/a/99"},
		{"/old/forum?id=3", "/old/forum?id=99"},
	} {
		hid, mis := get(t, h2, c.hidden), get(t, h2, c.missing)
		if hid.Code != http.StatusNotFound || mis.Code != http.StatusNotFound {
			t.Errorf("%s and %s: status %d and %d, want 404 and 404", c.hidden, c.missing, hid.Code, mis.Code)
			continue
		}
		if shape(bodyOf(t, hid), c.hidden) != shape(bodyOf(t, mis), c.missing) {
			t.Errorf("the page of %s differs from the page of the missing %s", c.hidden, c.missing)
		}
	}
}

func TestForumPageLinkForum(t *testing.T) {
	b := bundletest.Small()
	b.Forums = append(b.Forums, bundleForum(4, 0, "link", "Elsewhere", 5))
	b.Forums[3].LinkURL = ptr("https://example.com/out")
	b.Grants = append(b.Grants, guestGrant(4, "forum.list"))
	h, _ := newTestSite(t, b, Options{})
	rec := get(t, h, "/f/4")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://example.com/out" {
		t.Errorf("link forum: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// The pages carry no inline script, no style attribute and no style element.
func TestForumPagesAreCSPClean(t *testing.T) {
	h, _ := newTestSite(t, richBoard(), Options{})
	for _, target := range []string{"/f/1", "/f/2", "/f/3", "/t/1", "/t/5", "/t/6"} {
		body := strings.ToLower(bodyOf(t, get(t, h, target)))
		if strings.Contains(body, "<style") || regexp.MustCompile(`\sstyle\s*=`).MatchString(body) {
			t.Errorf("%s: inline style", target)
		}
		for _, m := range regexp.MustCompile(`<script[^>]*>`).FindAllString(body, -1) {
			if !strings.Contains(m, "src=") {
				t.Errorf("%s: inline script %s", target, m)
			}
		}
		if regexp.MustCompile(`\son[a-z]+\s*=`).MatchString(body) {
			t.Errorf("%s: inline event handler", target)
		}
	}
}
