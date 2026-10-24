package board_test

import (
	"context"
	"testing"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// A new post carries the signature of a member who holds post.signatures in the
// forum, and only then. A guest's post never does, whatever the guests group
// holds. An edit keeps the stored flag, whatever the editor holds.
func TestNewPostsAttachTheSignature(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())

	// Without the grant: no signature, for a topic and for a reply.
	plain := viewerFor(t, pool, carol)
	pid, err := board.NewTopic(ctx, pool, plain, 2, board.Draft{Title: "No sig", Source: "text", IP: ip1}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if readPost(t, pool, pid).ShowSg {
		t.Error("a topic by a member without post.signatures carries the signature")
	}
	rid, err := board.Reply(ctx, pool, plain, 1, board.Draft{Source: "reply", IP: ip1}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if readPost(t, pool, rid).ShowSg {
		t.Error("a reply by a member without post.signatures carries the signature")
	}

	// With the grant in forum 2: both carry it.
	grantOpts(t, pool, carol, 2, "post.signatures")
	signed := viewerFor(t, pool, carol)
	pid, err = board.NewTopic(ctx, pool, signed, 2, board.Draft{Title: "With sig", Source: "text two", IP: ip1}, t0)
	if err != nil {
		t.Fatal(err)
	}
	rid, err = board.Reply(ctx, pool, signed, 1, board.Draft{Source: "reply two", IP: ip1}, t0)
	if err != nil {
		t.Fatal(err)
	}
	for what, id := range map[string]int64{"topic": pid, "reply": rid} {
		if !readPost(t, pool, id).ShowSg {
			t.Errorf("a %s by a member with post.signatures lacks the signature", what)
		}
	}
	// The right is per forum: a grant in forum 2 does not reach forum 3.
	if _, err := pool.Exec(ctx, `update forums set kind = 'forum' where id = 3`); err != nil {
		t.Fatal(err)
	}
	grantOpts(t, pool, carol, 3, "topic.start", "forum.read", "forum.list")
	pid3, err := board.NewTopic(ctx, pool, viewerFor(t, pool, carol), 3, board.Draft{Title: "Elsewhere", Source: "text three", IP: ip1}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if readPost(t, pool, pid3).ShowSg {
		t.Error("post.signatures in forum 2 reached a post in forum 3")
	}

	// An edit keeps the stored flag: on stays on without the grant, off stays off with it.
	if err := board.EditPost(ctx, pool, signed, rid, board.Draft{Source: "reply edited", IP: ip1}, "", t0); err != nil {
		t.Fatal(err)
	}
	if !readPost(t, pool, rid).ShowSg {
		t.Error("an edit cleared the signature flag")
	}
	if _, err := pool.Exec(ctx, `update posts set enable_sig = false where id = $1`, rid); err != nil {
		t.Fatal(err)
	}
	if err := board.EditPost(ctx, pool, signed, rid, board.Draft{Source: "reply edited again", IP: ip1}, "", t0); err != nil {
		t.Fatal(err)
	}
	if readPost(t, pool, rid).ShowSg {
		t.Error("an edit set the signature flag on a post that had none")
	}
	if _, err := pool.Exec(ctx, `update posts set enable_sig = true where id = $1`, rid); err != nil {
		t.Fatal(err)
	}
	denyOpts(t, pool, carol, 2, "post.signatures")
	if err := board.EditPost(ctx, pool, viewerFor(t, pool, carol), rid, board.Draft{Source: "reply edited thrice", IP: ip1}, "", t0); err != nil {
		t.Fatal(err)
	}
	if !readPost(t, pool, rid).ShowSg {
		t.Error("an edit by a member who lost the right cleared the flag")
	}
}

func TestGuestPostNeverCarriesASignature(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	allowGuests(t, pool, 2, "topic.start", "topic.reply", "post.no_approval", "post.signatures")
	g := asGuest(t, pool)
	pid, err := board.NewTopic(ctx, pool, g, 2, board.Draft{Title: "Locked out", Source: "help", GuestName: "Visitor 9", IP: ip1}, t0)
	if err != nil {
		t.Fatal(err)
	}
	rid, err := board.Reply(ctx, pool, g, 1, board.Draft{Source: "me too", GuestName: "Visitor 9", IP: ip1}, t0)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{pid, rid} {
		if p := readPost(t, pool, id); p.ShowSg || p.AuthorID != 0 {
			t.Errorf("guest post %d: ShowSg = %v, author %d", id, p.ShowSg, p.AuthorID)
		}
	}
}
