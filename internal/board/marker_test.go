package board_test

import (
	"context"
	"net/netip"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// A moved-topic marker is never a reply target, whatever its target. When the
// viewer cannot see the target, the marker does not exist for the viewer at all
// (spec 14): every answer is the one for a missing topic.
func TestMarkerIsNeverAReplyTarget(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	a := viewerFor(t, pool, alice)

	// Marker 3 stands in forum 3 and points at topic 1, which alice sees.
	wantIs(t, board.CanReply(ctx, pool, a, 3), board.ErrNotFound, "CanReply, visible target")
	_, _, _, err := board.TopicHead(ctx, pool, a, 3)
	wantIs(t, err, board.ErrNotFound, "TopicHead, visible target")
	_, err = board.Reply(ctx, pool, a, 3, board.Draft{Source: "x", IP: ip1}, t0)
	wantIs(t, err, board.ErrNotFound, "Reply, visible target")

	// Now the marker stands in forum 2, which carol reads, and points at topic 2 in
	// forum 3, which she does not.
	if _, err := pool.Exec(ctx, `update topics set forum_id = 2, moved_to_topic_id = 2 where id = 3`); err != nil {
		t.Fatal(err)
	}
	c := viewerFor(t, pool, carol)
	wantIs(t, board.CanReply(ctx, pool, c, 3), board.ErrNotFound, "CanReply, hidden target")
	_, _, _, err = board.TopicHead(ctx, pool, c, 3)
	wantIs(t, err, board.ErrNotFound, "TopicHead, hidden target")
	_, err = board.Reply(ctx, pool, c, 3, board.Draft{Source: "x", IP: ip1}, t0)
	wantIs(t, err, board.ErrNotFound, "Reply, hidden target")
	// The same for the other writes that read a topic: the answer is a missing one's.
	grantOpts(t, pool, carol, 2, "mod.lock")
	wantIs(t, board.SetTopicLock(ctx, pool, viewerFor(t, pool, carol), 3, true, t0), board.ErrNotFound, "SetTopicLock, hidden target")
	// A post in the marker topic cannot be quoted, edited or deleted by a viewer who
	// cannot see the target.
	if _, err := pool.Exec(ctx, `insert into posts (topic_id, forum_id, author_id, author_name, created_at, subject, body_source, body_tree, body_text, visibility)
select 3, 2, 3, 'carol', created_at, subject, 'Stray text', body_tree, 'Stray text', 'approved' from posts where id = 1`); err != nil {
		t.Fatal(err)
	}
	var strayID int64
	if err := pool.QueryRow(ctx, `select max(id) from posts`).Scan(&strayID); err != nil {
		t.Fatal(err)
	}
	_, err = board.QuoteSource(ctx, pool, viewerFor(t, pool, carol), strayID)
	wantIs(t, err, board.ErrNotFound, "QuoteSource of a post in a marker topic")
	_, err = board.LoadEdit(ctx, pool, viewerFor(t, pool, carol), strayID)
	wantIs(t, err, board.ErrNotFound, "LoadEdit of a post in a hidden marker")
	_, err = board.LoadDelete(ctx, pool, viewerFor(t, pool, carol), strayID)
	wantIs(t, err, board.ErrNotFound, "LoadDelete of a post in a hidden marker")
	// A missing topic gives the same answers.
	wantIs(t, board.CanReply(ctx, pool, c, 9999), board.ErrNotFound, "missing topic")
}

// The buttons of a topic page are the rules of the checks: for every viewer and
// every post, Quote, Edit and Delete show exactly when CanReply, LoadEdit and
// LoadDelete pass. TopicPage.CanReply agrees with CanReply, ForumPage.CanStart
// with CanStartTopic.
func TestButtonsAgreeWithTheChecks(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	grantOpts(t, pool, carol, 2, "post.delete_own")
	grantOpts(t, pool, alice, 2, "post.delete_own")
	moderator(t, pool, bob)
	allowGuests(t, pool, 2, "topic.reply", "topic.start")
	// A reply by carol that is approved, so that she owns an approved post.
	if _, err := pool.Exec(ctx, `update posts set visibility = 'approved' where id = 3`); err != nil {
		t.Fatal(err)
	}
	check := func(label string) {
		t.Helper()
		for name, v := range map[string]board.Viewer{
			"guest": asGuest(t, pool), "alice": viewerFor(t, pool, alice), "bob": viewerFor(t, pool, bob), "carol": viewerFor(t, pool, carol),
		} {
			tp, err := board.TopicView(ctx, pool, v, 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			canReply := board.CanReply(ctx, pool, v, 1) == nil
			if tp.CanReply != canReply {
				t.Errorf("%s, %s: CanReply = %v, the check says %v", label, name, tp.CanReply, canReply)
			}
			if len(tp.Buttons) != len(tp.Posts) {
				t.Fatalf("%s, %s: %d buttons for %d posts", label, name, len(tp.Buttons), len(tp.Posts))
			}
			for i, p := range tp.Posts {
				b := tp.Buttons[i]
				wantQuote := canReply && p.Visibility == "approved"
				_, errEdit := board.LoadEdit(ctx, pool, v, p.ID)
				_, errDel := board.LoadDelete(ctx, pool, v, p.ID)
				wantEdit, wantDel := errEdit == nil && p.Visibility != "deleted", errDel == nil && p.Visibility != "deleted"
				if b.Quote != wantQuote || b.Edit != wantEdit || b.Delete != wantDel {
					t.Errorf("%s, %s, post %d: buttons %+v, the checks say quote %v edit %v delete %v", label, name, p.ID, b, wantQuote, wantEdit, wantDel)
				}
			}
			fp, err := board.ForumView(ctx, pool, v, 2, 1)
			if err != nil {
				t.Fatal(err)
			}
			if fp.CanStart != (board.CanStartTopic(ctx, pool, v, 2) == nil) {
				t.Errorf("%s, %s: ForumPage.CanStart = %v", label, name, fp.CanStart)
			}
		}
	}
	check("open")
	if _, err := pool.Exec(ctx, `update topics set locked = true where id = 1`); err != nil {
		t.Fatal(err)
	}
	check("topic locked")
	if _, err := pool.Exec(ctx, `update topics set locked = false where id = 1; update forums set locked = true where id = 2`); err != nil {
		t.Fatal(err)
	}
	check("forum locked")
	if _, err := pool.Exec(ctx, `update forums set locked = false where id = 2; update posts set visibility = 'deleted' where id = 2`); err != nil {
		t.Fatal(err)
	}
	check("a deleted post")
	// The first post with replies is not the author's to delete; alone, it is.
	if _, err := pool.Exec(ctx, `delete from attachments where post_id in (2, 3, 4); delete from posts where id in (2, 3, 4)`); err != nil {
		t.Fatal(err)
	}
	check("the first post alone")
}

func TestMayDownload(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	a := viewerFor(t, pool, alice)
	for _, v := range []board.Viewer{asGuest(t, pool), a, viewerFor(t, pool, carol)} {
		tp, err := board.TopicView(ctx, pool, v, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got := board.MayDownload(v, 2); got != tp.CanDownload {
			t.Errorf("MayDownload = %v, TopicView.CanDownload = %v", got, tp.CanDownload)
		}
	}
}

// CheckDraft refuses what the save refuses.
func TestCheckDraft(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	allowGuests(t, pool, 2, "topic.start", "topic.reply")
	a, g := viewerFor(t, pool, alice), asGuest(t, pool)
	long := strings.Repeat("t", 121)
	all := board.DraftChecks{Author: true, Title: true, Reason: true}
	wantInput(t, board.CheckDraft(ctx, pool, a, board.Draft{Title: long, IP: ip1}, "", all, t0), "title", "The title is longer than 120 characters.")
	wantInput(t, board.CheckDraft(ctx, pool, a, board.Draft{Title: "", IP: ip1}, "", all, t0), "title", "Enter a title.")
	wantInput(t, board.CheckDraft(ctx, pool, a, board.Draft{Title: "ok", IP: ip1}, strings.Repeat("r", 256), all, t0), "reason", "")
	wantInput(t, board.CheckDraft(ctx, pool, g, board.Draft{Title: "ok", GuestName: "Alice", IP: ip1}, "", all, t0), "guest_name", "That name belongs to a member. Sign in, or choose another name.")
	wantInput(t, board.CheckDraft(ctx, pool, g, board.Draft{Title: "ok", GuestName: "", IP: ip1}, "", all, t0), "guest_name", "")
	if err := board.CheckDraft(ctx, pool, g, board.Draft{Title: "ok", GuestName: "Visitor", IP: ip1}, "", all, t0); err != nil {
		t.Errorf("good draft: %v", err)
	}
	// A check that is off does not run: a reply ignores the title.
	if err := board.CheckDraft(ctx, pool, a, board.Draft{Title: long, IP: ip1}, "", board.DraftChecks{Author: true}, t0); err != nil {
		t.Errorf("reply ignores the title: %v", err)
	}
	// A banned address is refused.
	if _, err := pool.Exec(ctx, `insert into bans (kind, ip, created_at) values ('ip', '10.9.9.9', now())`); err != nil {
		t.Fatal(err)
	}
	wantIs(t, board.CheckDraft(ctx, pool, g, board.Draft{GuestName: "Visitor", IP: netip.MustParseAddr("10.9.9.9")}, "", board.DraftChecks{Author: true}, t0), board.ErrForbidden, "banned address")
}
