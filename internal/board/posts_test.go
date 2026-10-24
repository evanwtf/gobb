package board_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/markup"
)

func postIDs(posts []board.Post) []int64 {
	out := make([]int64, len(posts))
	for i, p := range posts {
		out[i] = p.ID
	}
	return out
}

func TestTopicViewGuest(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	v := guestOn(t, pool)

	tp, err := board.TopicView(ctx, pool, v, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := postIDs(tp.Posts); !equalIDs(got, []int64{1, 2, 4}) {
		t.Fatalf("posts = %v, want [1 2 4]", got)
	}
	if tp.Topic.ID != 1 || tp.Topic.Title != "Welcome to the example board" || tp.Topic.Kind != "sticky" {
		t.Errorf("Topic = %+v", tp.Topic)
	}
	if tp.Forum == nil || tp.Forum.ID != 2 || len(tp.Crumbs) != 1 || tp.Crumbs[0].ID != 1 {
		t.Errorf("Forum = %+v, Crumbs = %+v", tp.Forum, tp.Crumbs)
	}
	if tp.Page != 1 || tp.Pages != 1 || tp.Poll != nil {
		t.Errorf("page %d of %d, poll %+v", tp.Page, tp.Pages, tp.Poll)
	}

	p1 := tp.Posts[0]
	if p1.Card == nil || p1.Card.RankTitle != "Newcomer" || p1.Card.PostCount != 120 {
		t.Fatalf("post 1 card = %+v, want rank Newcomer and 120 posts", p1.Card)
	}
	if p1.Card.RankImage != "ranks/newcomer.png" || p1.Card.AvatarPath != "avatars/1.png" || p1.Card.Name != "alice" {
		t.Errorf("post 1 card = %+v", p1.Card)
	}
	if p1.Card.Joined.IsZero() {
		t.Error("post 1 card has no join date")
	}
	if p1.Signature == nil {
		t.Error("post 1 has no signature")
	}
	if p1.EditCount != 1 || p1.EditReason != "typo" || p1.EditedAt == nil || p1.EditedBy == nil || p1.EditedBy.Name != "alice" {
		t.Errorf("post 1 edit = %d %q %v %v", p1.EditCount, p1.EditReason, p1.EditedAt, p1.EditedBy)
	}
	if p1.Tree == nil || p1.Tree.Kind != markup.Root {
		t.Errorf("post 1 tree = %+v", p1.Tree)
	}
	if p1.Subject != "Welcome" || p1.Author.ID != 1 || p1.Author.Name != "alice" {
		t.Errorf("post 1 = %+v", p1)
	}

	p2 := tp.Posts[1]
	if len(p2.Attachments) != 0 {
		t.Errorf("post 2 Attachments = %+v, want none: attachment 1 shows inline", p2.Attachments)
	}
	if p2.Signature != nil {
		t.Error("post 2 shows a signature though enable_sig is off")
	}

	p4 := tp.Posts[2]
	if p4.Author.ID != 0 || p4.Author.Name != "Visitor" || p4.Card != nil {
		t.Errorf("post 4 author = %+v, card %+v; want a guest named Visitor", p4.Author, p4.Card)
	}
	for _, p := range tp.Posts {
		if p.IP != "" {
			t.Errorf("post %d shows IP %q to a guest", p.ID, p.IP)
		}
	}
}

// A non-inline attachment shows in the list under its post.
func TestTopicViewAttachmentList(t *testing.T) {
	ctx := context.Background()
	b := bundletest.Small()
	for i := range b.Posts {
		if b.Posts[i].ID == 2 {
			b.Posts[i].Body.Source = "No image here."
		}
	}
	pool, _ := boardtest.Load(t, b)
	tp, err := board.TopicView(ctx, pool, guestOn(t, pool), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := tp.Posts[1].Attachments
	if len(got) != 1 || got[0].ID != 1 || got[0].Name != "diagram.png" || got[0].Mime != "image/png" ||
		got[0].Comment != "A diagram" || got[0].Downloads != 7 || got[0].Size == 0 || got[0].Inline {
		t.Errorf("post 2 Attachments = %+v", got)
	}
}

func TestTopicViewRankAuto(t *testing.T) {
	ctx := context.Background()
	b := bundletest.Small()
	b.Ranks = append(b.Ranks,
		bundle.Rank{ID: 2, Title: "Veteran", MinPosts: 100},
		bundle.Rank{ID: 3, Title: "Staff", MinPosts: 0, Special: true},
		bundle.Rank{ID: 4, Title: "Legend", MinPosts: 1000},
	)
	b.Users[0].RankID = nil // alice: 120 posts -> Veteran; Staff is special, Legend is out of reach
	pool, _ := boardtest.Load(t, b)
	tp, err := board.TopicView(ctx, pool, guestOn(t, pool), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := tp.Posts[0].Card.RankTitle; got != "Veteran" {
		t.Errorf("rank = %q, want Veteran", got)
	}
	if got := tp.Posts[0].Card.RankImage; got != "" {
		t.Errorf("rank image = %q, want none", got)
	}
	// Bob has rank 1 set by hand, whatever his count.
	if got := tp.Posts[1].Card.RankTitle; got != "Newcomer" {
		t.Errorf("bob's rank = %q, want Newcomer", got)
	}
}

func TestTopicViewPoll(t *testing.T) {
	ctx := context.Background()
	b := bundletest.Small()
	b.Grants = append(b.Grants, listGrant(3), readGrant(3))
	pool, _ := boardtest.Load(t, b)
	tp, err := board.TopicView(ctx, pool, guestOn(t, pool), 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	p := tp.Poll
	if p == nil || p.Question != "Favorite color?" || p.MaxOptions != 2 || p.Total != 5 || p.EndsAt != nil {
		t.Fatalf("Poll = %+v", p)
	}
	if len(p.Options) != 3 || p.Options[0].Text != "Green" || p.Options[0].Votes != 3 ||
		p.Options[1].Text != "Blue" || p.Options[1].Votes != 2 ||
		p.Options[2].Text != "Red" || p.Options[2].Votes != 0 {
		t.Errorf("Options = %+v", p.Options)
	}
	if got := postIDs(tp.Posts); !equalIDs(got, []int64{5, 6}) {
		t.Errorf("posts = %v", got)
	}
	// The first post holds a smiley element.
	var found bool
	var walk func(n *markup.Node)
	walk = func(n *markup.Node) {
		if n.Kind == markup.Element && n.Name == "smiley" {
			found = true
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(tp.Posts[0].Tree)
	if !found {
		t.Error("post 5 has no smiley element")
	}
}

// The guest may not read forum 3, so topics 2 and 3 are missing.
func TestTopicViewHiddenAndMarker(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	v := guestOn(t, pool)
	for _, id := range []int64{2, 3, 99} {
		if _, err := board.TopicView(ctx, pool, v, id, 1); !errors.Is(err, board.ErrNotFound) {
			t.Errorf("topic %d: err = %v, want ErrNotFound", id, err)
		}
	}
}

func TestTopicViewMarker(t *testing.T) {
	ctx := context.Background()
	b := bundletest.Small()
	b.Grants = append(b.Grants, listGrant(3), readGrant(3))
	pool, _ := boardtest.Load(t, b)
	_, err := board.TopicView(ctx, pool, guestOn(t, pool), 3, 1)
	var me board.MovedError
	if !errors.As(err, &me) || me.To != 1 {
		t.Fatalf("err = %v, want MovedError to 1", err)
	}
	// A marker whose target the guest cannot read is a missing topic.
	if _, err := pool.Exec(ctx, `update topics set moved_to_topic_id = 2 where id = 3`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `delete from acl_group_grants where forum_id = 3`); err != nil {
		t.Fatal(err)
	}
	if _, err := board.TopicView(ctx, pool, guestOn(t, pool), 3, 1); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("hidden target: err = %v, want ErrNotFound", err)
	}
}

// A moderator sees the unapproved post and its IP when the grant allows.
func TestTopicViewModerator(t *testing.T) {
	ctx := context.Background()
	b := bundletest.Small()
	group, yes := int64(3), bundle.Yes
	opt := "mod.view_ip"
	b.Grants = append(b.Grants, bundle.Grant{GroupID: &group, ForumID: 0, Option: &opt, Setting: &yes})
	pool, _ := boardtest.Load(t, b)

	bob := memberOn(t, pool, 2, "bob") // a moderator: mod.approve, mod.view_ip
	tp, err := board.TopicView(ctx, pool, bob, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := postIDs(tp.Posts); !equalIDs(got, []int64{1, 2, 3, 4}) {
		t.Fatalf("moderator sees %v, want 1 2 3 4", got)
	}
	if tp.Posts[2].Visibility != "unapproved" || tp.Posts[0].Visibility != "approved" {
		t.Errorf("visibility = %q, %q", tp.Posts[0].Visibility, tp.Posts[2].Visibility)
	}
	if tp.Posts[0].IP != "10.0.0.5" || tp.Posts[3].IP != "2001:db8::7" {
		t.Errorf("IPs = %q, %q", tp.Posts[0].IP, tp.Posts[3].IP)
	}

	// Carol sees her own unapproved post, and no IP.
	carol := memberOn(t, pool, 3, "carol")
	tp, err = board.TopicView(ctx, pool, carol, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := postIDs(tp.Posts); !equalIDs(got, []int64{1, 2, 3, 4}) {
		t.Errorf("carol sees %v", got)
	}
	if tp.Posts[0].IP != "" {
		t.Errorf("carol sees IP %q", tp.Posts[0].IP)
	}
	// A deleted post shows to a viewer with mod.delete only.
	if _, err := pool.Exec(ctx, `update posts set visibility = 'deleted' where id = 2`); err != nil {
		t.Fatal(err)
	}
	tp, err = board.TopicView(ctx, pool, bob, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := postIDs(tp.Posts); !equalIDs(got, []int64{1, 3, 4}) {
		t.Errorf("without mod.delete, bob sees %v, want 1 3 4", got)
	}
}

func TestTopicViewPages(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Large(3, 60))
	v := guestOn(t, pool)
	for _, c := range []struct {
		page, posts int
		first       int64
	}{{1, 25, 1}, {2, 25, 26}, {3, 10, 51}} {
		tp, err := board.TopicView(ctx, pool, v, 1, c.page)
		if err != nil {
			t.Fatalf("page %d: %v", c.page, err)
		}
		if tp.Pages != 3 || tp.Page != c.page || len(tp.Posts) != c.posts || tp.Posts[0].ID != c.first {
			t.Errorf("page %d: %d of %d, %d posts, first %d", c.page, tp.Page, tp.Pages, len(tp.Posts), tp.Posts[0].ID)
		}
	}
	if _, err := board.TopicView(ctx, pool, v, 1, 4); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("page 4: err = %v, want ErrNotFound", err)
	}
	// Topic 2 is in forum 3, hidden from the guest: its first post is missing.
	if _, _, err := board.PostLocation(ctx, pool, v, 61); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("PostLocation(61): err = %v, want ErrNotFound", err)
	}
	loc := func(post int64) (int64, int) {
		topic, page, err := board.PostLocation(ctx, pool, v, post)
		if err != nil {
			t.Fatalf("PostLocation(%d): %v", post, err)
		}
		return topic, page
	}
	for _, c := range []struct {
		post  int64
		topic int64
		page  int
	}{{1, 1, 1}, {25, 1, 1}, {26, 1, 2}, {30, 1, 2}, {60, 1, 3}} {
		if topic, page := loc(c.post); topic != c.topic || page != c.page {
			t.Errorf("PostLocation(%d) = topic %d page %d, want %d and %d", c.post, topic, page, c.topic, c.page)
		}
	}
}

func TestPostLocation(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	v := guestOn(t, pool)
	topic, page, err := board.PostLocation(ctx, pool, v, 4)
	if err != nil || topic != 1 || page != 1 {
		t.Errorf("PostLocation(4) = %d, %d, %v", topic, page, err)
	}
	for _, id := range []int64{3, 5, 99} {
		if _, _, err := board.PostLocation(ctx, pool, v, id); !errors.Is(err, board.ErrNotFound) {
			t.Errorf("PostLocation(%d): err = %v, want ErrNotFound", id, err)
		}
	}
	// The unapproved post shows to its author, and the page counts the posts
	// that this viewer sees: carol sees post 3 in third place.
	carol := memberOn(t, pool, 3, "carol")
	if topic, page, err := board.PostLocation(ctx, pool, carol, 3); err != nil || topic != 1 || page != 1 {
		t.Errorf("PostLocation(3) for carol = %d, %d, %v", topic, page, err)
	}
}

func TestAttachmentFile(t *testing.T) {
	ctx := context.Background()
	b := bundletest.Small()
	pool, _ := boardtest.Load(t, b)

	guest := guestOn(t, pool)
	// The guest lacks account.download: forbidden, not missing.
	if _, err := board.AttachmentFile(ctx, pool, guest, 1); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("guest, attachment 1: err = %v, want ErrForbidden", err)
	}
	// An attachment with no post: missing for everyone but its uploader.
	if _, err := board.AttachmentFile(ctx, pool, guest, 2); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("guest, attachment 2: err = %v, want ErrNotFound", err)
	}
	if _, err := board.AttachmentFile(ctx, pool, guest, 99); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("guest, attachment 99: err = %v, want ErrNotFound", err)
	}

	// Bob (member): account.download is not granted by the fixture. Grant it.
	if _, err := pool.Exec(ctx, `insert into acl_group_grants (group_id, forum_id, option, setting) values (1, 0, 'account.download', 3), (1, 2, 'attach.download', 3)`); err != nil {
		t.Fatal(err)
	}
	guest = guestOn(t, pool)
	m, err := board.AttachmentFile(ctx, pool, guest, 1)
	if err != nil {
		t.Fatal(err)
	}
	if m.Path != "attachments/1.png" || m.Mime != "image/png" || m.Name != "diagram.png" || m.Size == 0 {
		t.Errorf("meta = %+v", m)
	}
	// The orphan stays missing for a guest, and shows to its uploader.
	if _, err := board.AttachmentFile(ctx, pool, guest, 2); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("guest, orphan: err = %v, want ErrNotFound", err)
	}
	if _, err := pool.Exec(ctx, `insert into acl_group_grants (group_id, forum_id, option, setting) values (2, 0, 'account.download', 3)`); err != nil {
		t.Fatal(err)
	}
	alice := memberOn(t, pool, 1, "alice")
	if m, err := board.AttachmentFile(ctx, pool, alice, 2); err != nil || m.Mime != "image/gif" {
		t.Errorf("uploader, orphan: %+v, %v", m, err)
	}
	bob := memberOn(t, pool, 2, "bob")
	if _, err := board.AttachmentFile(ctx, pool, bob, 2); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("other member, orphan: err = %v, want ErrNotFound", err)
	}
	// An attachment on an unapproved post is missing for a guest.
	if _, err := pool.Exec(ctx, `update attachments set post_id = 3 where id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := board.AttachmentFile(ctx, pool, guestOn(t, pool), 1); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("unapproved post: err = %v, want ErrNotFound", err)
	}
}

func TestSmileyLookup(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	look, err := board.SmileyLookup(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := look(":)")
	if !ok || s.Image != "/files/smilies/smile.gif" || s.Alt != "smile" || s.Code != ":)" {
		t.Errorf(":) = %+v, %v", s, ok)
	}
	if _, ok := look(":nope:"); ok {
		t.Error("an unknown code was found")
	}
	if strings.Contains(s.Image, "..") {
		t.Error("bad image path")
	}
}

// A marker that points at itself is a missing topic.
func TestTopicViewMarkerSelf(t *testing.T) {
	ctx := context.Background()
	b := bundletest.Small()
	b.Grants = append(b.Grants, listGrant(3), readGrant(3))
	pool, _ := boardtest.Load(t, b)
	if _, err := pool.Exec(ctx, `update topics set moved_to_topic_id = 3 where id = 3`); err != nil {
		t.Fatal(err)
	}
	if _, err := board.TopicView(ctx, pool, guestOn(t, pool), 3, 1); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// CanDownload says whether the viewer may load the attachments of the topic.
func TestTopicViewCanDownload(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	tp, err := board.TopicView(ctx, pool, guestOn(t, pool), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if tp.CanDownload {
		t.Error("a guest without the rights can download")
	}
	if _, err := pool.Exec(ctx, `insert into acl_group_grants (group_id, forum_id, option, setting) values (1, 0, 'account.download', 3)`); err != nil {
		t.Fatal(err)
	}
	if tp, _ = board.TopicView(ctx, pool, guestOn(t, pool), 1, 1); tp.CanDownload {
		t.Error("account.download alone is enough")
	}
	if _, err := pool.Exec(ctx, `insert into acl_group_grants (group_id, forum_id, option, setting) values (1, 2, 'attach.download', 3)`); err != nil {
		t.Fatal(err)
	}
	if tp, _ = board.TopicView(ctx, pool, guestOn(t, pool), 1, 1); !tp.CanDownload {
		t.Error("a guest with both rights cannot download")
	}
}

// A topic page shows no last-post column, so it does not read the last posts of
// the forum tree (the forum page does, for its subforums).
func TestTopicViewReadsNoLastPosts(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	alice := memberOn(t, pool, 1, "alice")
	lastPosts := func(run func(q *lastPostSpy)) int {
		spy := &lastPostSpy{counting: counting{pool: pool}}
		run(spy)
		return spy.lastPosts
	}
	// The control: the forum page of forum 2 reads the last post of forum 3.
	if n := lastPosts(func(q *lastPostSpy) {
		if _, err := board.ForumView(ctx, q, alice, 2, 1); err != nil {
			t.Fatal(err)
		}
	}); n != 1 {
		t.Fatalf("the forum page ran the last-post query %d times, want 1", n)
	}
	for _, id := range []int64{1, 2} {
		if n := lastPosts(func(q *lastPostSpy) {
			if _, err := board.TopicView(ctx, q, alice, id, 1); err != nil {
				t.Fatal(err)
			}
		}); n != 0 {
			t.Errorf("topic %d ran the last-post query %d times, want 0", id, n)
		}
	}
}

// lastPostSpy counts the statements, and the runs of the last-post query of
// the forum tree.
type lastPostSpy struct {
	counting
	lastPosts int
}

func (s *lastPostSpy) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "where p.id = any($1)") {
		s.lastPosts++
	}
	return s.counting.Query(ctx, sql, args...)
}
