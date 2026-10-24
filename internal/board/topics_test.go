package board_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/acl"
	"github.com/evanwtf/gobb/internal/acldb"
	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// guestOn returns the guest of the board in pool.
func guestOn(t *testing.T, pool *pgxpool.Pool) board.Viewer {
	t.Helper()
	s, err := board.LoadSettings(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	v, err := board.Guest(context.Background(), pool, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// memberOn returns the viewer for user id, with the permissions of that user.
func memberOn(t *testing.T, pool *pgxpool.Pool, id int64, name string) board.Viewer {
	t.Helper()
	subject, err := acldb.Load(context.Background(), pool, id)
	if err != nil {
		t.Fatal(err)
	}
	return board.Viewer{UserID: id, Name: name, Perms: acl.Resolve(subject), Loc: time.UTC, Scheme: "slate"}
}

func topicIDs(rows []board.TopicRow) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestForumViewGuest(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	v := guestOn(t, pool)

	fp, err := board.ForumView(ctx, pool, v, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if fp.Forum == nil || fp.Forum.ID != 2 || fp.Forum.Rules != "Be kind." {
		t.Fatalf("Forum = %+v", fp.Forum)
	}
	if fp.Page != 1 || fp.Pages != 1 {
		t.Errorf("page %d of %d, want 1 of 1", fp.Page, fp.Pages)
	}
	if len(fp.Topics) != 1 {
		t.Fatalf("Topics = %+v, want only topic 1", fp.Topics)
	}
	row := fp.Topics[0]
	if row.ID != 1 || row.Kind != "sticky" || row.Replies != 2 || row.Views != 42 {
		t.Errorf("row = %+v, want topic 1, sticky, 2 replies, 42 views", row)
	}
	if row.Author.ID != 1 || row.Author.Name != "alice" {
		t.Errorf("row author = %+v", row.Author)
	}
	if row.Last.ID != 4 || row.Last.Author.Name != "Visitor" || row.Last.Author.ID != 0 {
		t.Errorf("row last = %+v, want post 4 by Visitor", row.Last)
	}
	if len(fp.Crumbs) != 1 || fp.Crumbs[0].ID != 1 {
		t.Errorf("Crumbs = %+v, want the category", fp.Crumbs)
	}
	if len(fp.Pinned) != 0 {
		t.Errorf("Pinned = %+v", fp.Pinned)
	}

	// The guest may not list forum 3: it looks like a missing forum.
	if _, err := board.ForumView(ctx, pool, v, 3, 1); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("forum 3: err = %v, want ErrNotFound", err)
	}
	if _, err := board.ForumView(ctx, pool, v, 99, 1); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("forum 99: err = %v, want ErrNotFound", err)
	}

	// The category shows forum 2 among its subforums.
	cat, err := board.ForumView(ctx, pool, v, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Subforums) != 1 || cat.Subforums[0].ID != 2 {
		t.Errorf("category Subforums = %+v, want forum 2", cat.Subforums)
	}
	if len(cat.Topics) != 0 {
		t.Errorf("a category lists topics: %+v", cat.Topics)
	}
}

func TestForumViewMarkerAndGlobal(t *testing.T) {
	ctx := context.Background()
	b := bundletest.Small()
	b.Grants = append(b.Grants, listGrant(3), readGrant(3))
	pool, _ := boardtest.Load(t, b)
	v := guestOn(t, pool)

	fp, err := board.ForumView(ctx, pool, v, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	var marker *board.TopicRow
	for i := range fp.Topics {
		if fp.Topics[i].ID == 3 {
			marker = &fp.Topics[i]
		}
	}
	if marker == nil || marker.MovedTo != 1 {
		t.Fatalf("Topics = %+v, want the marker (3) with MovedTo 1", fp.Topics)
	}
	if marker.Last.ID != 0 {
		t.Errorf("a marker has a last post: %+v", marker.Last)
	}
	// Forum 2's subforum list now includes forum 3 for the guest.
	f2, err := board.ForumView(ctx, pool, v, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(f2.Subforums) != 1 || f2.Subforums[0].ID != 3 {
		t.Errorf("forum 2 Subforums = %+v", f2.Subforums)
	}
	if len(f2.Pinned) != 0 {
		t.Errorf("Pinned before the change = %+v", f2.Pinned)
	}

	// Topic 2 (forum 3) becomes global: forum 2 shows it as pinned.
	if _, err := pool.Exec(ctx, `update topics set kind = 'global' where id = 2`); err != nil {
		t.Fatal(err)
	}
	f2, err = board.ForumView(ctx, pool, v, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := topicIDs(f2.Pinned); !equalIDs(got, []int64{2}) {
		t.Errorf("forum 2 Pinned = %v, want [2]", got)
	}
	// It does not count as a regular topic of forum 2 or forum 3.
	if got := topicIDs(f2.Topics); !equalIDs(got, []int64{1}) {
		t.Errorf("forum 2 Topics = %v, want [1]", got)
	}
}

// A global topic of a forum that the viewer may not read stays out of sight.
func TestForumViewGlobalHidden(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	if _, err := pool.Exec(ctx, `update topics set kind = 'global' where id = 2`); err != nil {
		t.Fatal(err)
	}
	v := guestOn(t, pool)
	fp, err := board.ForumView(ctx, pool, v, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(fp.Pinned) != 0 {
		t.Errorf("Pinned = %+v, want none: forum 3 is hidden from guests", fp.Pinned)
	}
}

// A forum that the viewer may list but not read is listed with a lock, and
// opening it looks the same as a missing forum.
func TestForumViewListedNotReadable(t *testing.T) {
	ctx := context.Background()
	b := bundletest.Small()
	b.Grants = append(b.Grants, listGrant(3))
	pool, _ := boardtest.Load(t, b)
	v := guestOn(t, pool)

	if _, err := board.ForumView(ctx, pool, v, 3, 1); !errors.Is(err, board.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	// Forum 2 lists forum 3 as a subforum that the guest cannot read.
	f2, err := board.ForumView(ctx, pool, v, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(f2.Subforums) != 1 || f2.Subforums[0].ID != 3 {
		t.Fatalf("Subforums = %+v", f2.Subforums)
	}
	sub := f2.Subforums[0]
	if sub.Readable || sub.Topics != 0 || sub.Posts != 0 || sub.Last != nil {
		t.Errorf("subforum = %+v, want not readable, no counts, no last post", sub)
	}
	if !f2.Forum.Readable {
		t.Error("forum 2 is not marked readable")
	}
}

func TestForumViewLink(t *testing.T) {
	ctx := context.Background()
	b := bundletest.Small()
	b.Forums = append(b.Forums, bundleForum(4, 0, "link", "Elsewhere", 5))
	b.Forums[3].LinkURL = ptr("https://example.com/out")
	b.Grants = append(b.Grants, listGrant(4))
	pool, _ := boardtest.Load(t, b)
	v := guestOn(t, pool)
	_, err := board.ForumView(ctx, pool, v, 4, 1)
	var le board.LinkError
	if !errors.As(err, &le) || le.URL != "https://example.com/out" {
		t.Fatalf("err = %v, want LinkError to the URL", err)
	}
	// A link forum that the guest may not list is a missing forum.
	b2 := bundletest.Small()
	b2.Forums = append(b2.Forums, bundleForum(4, 0, "link", "Elsewhere", 5))
	b2.Forums[3].LinkURL = ptr("https://example.com/out")
	pool2, _ := boardtest.Load(t, b2)
	if _, err := board.ForumView(ctx, pool2, guestOn(t, pool2), 4, 1); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("unlisted link: err = %v, want ErrNotFound", err)
	}
}

// Sticky topics come first, then the newest activity, then the higher ID. An
// announcement shows on every page and does not count toward the pages.
func TestForumViewOrderAndPages(t *testing.T) {
	ctx := context.Background()
	b := bundletest.Large(110, 1) // 55 topics in forum 2, 55 in forum 3
	// Topic 1 is sticky and old; topic 3 is an announcement.
	b.Topics[0].Kind = "sticky"
	b.Topics[2].Kind = "announce"
	pool, _ := boardtest.Load(t, b)
	v := guestOn(t, pool)

	p1, err := board.ForumView(ctx, pool, v, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	// 55 topics, minus the announcement: 54 regular rows, so 2 pages.
	if p1.Pages != 2 || p1.Page != 1 {
		t.Fatalf("page %d of %d, want 1 of 2", p1.Page, p1.Pages)
	}
	if len(p1.Topics) != 50 {
		t.Fatalf("page 1 has %d topics, want 50", len(p1.Topics))
	}
	if got := topicIDs(p1.Pinned); !equalIDs(got, []int64{3}) {
		t.Errorf("Pinned = %v, want [3]", got)
	}
	if p1.Topics[0].ID != 1 || p1.Topics[0].Kind != "sticky" {
		t.Errorf("first row = %+v, want the sticky topic 1", p1.Topics[0])
	}
	// After the sticky: newest activity first. Topic t is created t hours after
	// the start, so the highest odd ID is the newest in forum 2: 109.
	if p1.Topics[1].ID != 109 || p1.Topics[2].ID != 107 {
		t.Errorf("rows 2 and 3 = %d, %d, want 109, 107", p1.Topics[1].ID, p1.Topics[2].ID)
	}
	p2, err := board.ForumView(ctx, pool, v, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Topics) != 4 {
		t.Errorf("page 2 has %d topics, want 4", len(p2.Topics))
	}
	if got := topicIDs(p2.Pinned); !equalIDs(got, []int64{3}) {
		t.Errorf("page 2 Pinned = %v, want [3] again", got)
	}
	if _, err := board.ForumView(ctx, pool, v, 2, 3); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("page 3: err = %v, want ErrNotFound", err)
	}
}

func TestForumViewEmptyForum(t *testing.T) {
	ctx := context.Background()
	b := bundletest.Small()
	b.Forums = append(b.Forums, bundleForum(4, 1, "forum", "Empty", 9))
	b.Grants = append(b.Grants, listGrant(4), readGrant(4))
	pool, _ := boardtest.Load(t, b)
	v := guestOn(t, pool)
	fp, err := board.ForumView(ctx, pool, v, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	if fp.Pages != 1 || len(fp.Topics) != 0 {
		t.Errorf("empty forum: %d pages, %d topics, want 1 and 0", fp.Pages, len(fp.Topics))
	}
	if _, err := board.ForumView(ctx, pool, v, 4, 2); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("page 2 of an empty forum: err = %v, want ErrNotFound", err)
	}
}

// A moderator sees the unapproved topic; a guest does not. The author sees
// their own.
func TestForumViewWhoSeesWhich(t *testing.T) {
	ctx := context.Background()
	b := bundletest.Small()
	b.Topics = append(b.Topics, bundletest.Small().Topics[0])
	b.Topics[3].ID = 4
	b.Topics[3].Title = "Waiting"
	b.Topics[3].Kind = "normal"
	b.Topics[3].Visibility = "unapproved"
	b.Topics[3].AuthorID = ptr(int64(3))
	b.Topics[3].AuthorName = "carol"
	b.Posts = append(b.Posts, b.Posts[0])
	b.Posts[6].ID = 7
	b.Posts[6].TopicID = 4
	b.Posts[6].AuthorID = ptr(int64(3))
	b.Posts[6].AuthorName = "carol"
	b.Posts[6].Visibility = "unapproved"
	b.Posts[6].EditCount, b.Posts[6].EditedAt, b.Posts[6].EditedBy, b.Posts[6].EditReason = 0, nil, nil, ""
	pool, _ := boardtest.Load(t, b)

	guest, err := board.ForumView(ctx, pool, guestOn(t, pool), 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := topicIDs(guest.Topics); !equalIDs(got, []int64{1}) {
		t.Errorf("guest sees %v, want [1]", got)
	}
	for _, c := range []struct {
		id   int64
		name string
		want int
	}{{2, "bob", 2}, {3, "carol", 2}, {1, "alice", 1}} {
		fp, err := board.ForumView(ctx, pool, memberOn(t, pool, c.id, c.name), 2, 1)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(fp.Topics) != c.want {
			t.Errorf("%s sees %v, want %d topics", c.name, topicIDs(fp.Topics), c.want)
		}
	}
}

func TestParsePage(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
		bad  bool
	}{
		{"", 1, false}, {"1", 1, false}, {"7", 7, false}, {"1000000", 1000000, false},
		{"0", 0, true}, {"-1", 0, true}, {"abc", 0, true}, {"+3", 0, true}, {" 3", 0, true},
		{"1000001", 0, true}, {"999999999999999999999999999999", 0, true}, {"1.5", 0, true},
		{"٣", 0, true},
	} {
		got, err := board.ParsePage(c.in)
		if (err != nil) != c.bad || (!c.bad && got != c.want) {
			t.Errorf("ParsePage(%q) = %d, %v; want %d, bad=%v", c.in, got, err, c.want, c.bad)
		}
	}
}
