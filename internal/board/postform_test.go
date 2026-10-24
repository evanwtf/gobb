package board_test

import (
	"context"
	"testing"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// TestCanReply: the read-only check agrees with Reply on each case.
func TestCanReply(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := viewerFor(t, pool, carol)

	if err := board.CanReply(ctx, pool, c, 1); err != nil {
		t.Errorf("plain member, open topic: %v", err)
	}
	wantIs(t, board.CanReply(ctx, pool, c, 9999), board.ErrNotFound, "missing topic")
	wantIs(t, board.CanReply(ctx, pool, c, 2), board.ErrNotFound, "topic in an unreadable forum")
	wantIs(t, board.CanReply(ctx, pool, asGuest(t, pool), 1), board.ErrForbidden, "guest without topic.reply")
	wantIs(t, board.CanReply(ctx, pool, viewerFor(t, pool, alice), 3), board.ErrNotFound, "moved-topic marker")

	if _, err := pool.Exec(ctx, `update topics set locked = true where id = 1`); err != nil {
		t.Fatal(err)
	}
	wantIs(t, board.CanReply(ctx, pool, c, 1), board.ErrForbidden, "locked topic")
	grantOpts(t, pool, carol, 2, "mod.lock")
	if err := board.CanReply(ctx, pool, viewerFor(t, pool, carol), 1); err != nil {
		t.Errorf("locked topic, mod.lock: %v", err)
	}
	if _, err := pool.Exec(ctx, `update topics set locked = false where id = 1; update forums set locked = true where id = 2`); err != nil {
		t.Fatal(err)
	}
	if err := board.CanReply(ctx, pool, viewerFor(t, pool, carol), 1); err != nil {
		t.Errorf("locked forum, mod.lock: %v", err)
	}
	wantIs(t, board.CanReply(ctx, pool, viewerFor(t, pool, alice), 1), board.ErrForbidden, "locked forum")

	allowGuests(t, pool, 2, "topic.reply")
	if err := board.CanReply(ctx, pool, asGuest(t, pool), 1); err == nil {
		t.Error("guest, locked forum: allowed")
	}
}

func TestCanStartTopic(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := viewerFor(t, pool, carol)

	if err := board.CanStartTopic(ctx, pool, c, 2); err != nil {
		t.Errorf("plain member: %v", err)
	}
	wantIs(t, board.CanStartTopic(ctx, pool, c, 9999), board.ErrNotFound, "missing forum")
	wantIs(t, board.CanStartTopic(ctx, pool, c, 3), board.ErrNotFound, "unreadable forum")
	wantIs(t, board.CanStartTopic(ctx, pool, asGuest(t, pool), 2), board.ErrForbidden, "guest without topic.start")
	grantOpts(t, pool, alice, 1, "forum.read")
	wantIs(t, board.CanStartTopic(ctx, pool, viewerFor(t, pool, alice), 1), board.ErrForbidden, "category")

	if _, err := pool.Exec(ctx, `update forums set locked = true where id = 2`); err != nil {
		t.Fatal(err)
	}
	wantIs(t, board.CanStartTopic(ctx, pool, c, 2), board.ErrForbidden, "locked forum")
	grantOpts(t, pool, carol, 2, "mod.lock")
	if err := board.CanStartTopic(ctx, pool, viewerFor(t, pool, carol), 2); err != nil {
		t.Errorf("locked forum, mod.lock: %v", err)
	}
	allowGuests(t, pool, 2, "topic.start")
	if _, err := pool.Exec(ctx, `update forums set locked = false where id = 2`); err != nil {
		t.Fatal(err)
	}
	if err := board.CanStartTopic(ctx, pool, asGuest(t, pool), 2); err != nil {
		t.Errorf("guest with topic.start: %v", err)
	}
}

// TestLoadEdit: the form loader applies the rights of EditPost and returns what
// the form needs.
func TestLoadEdit(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	a := viewerFor(t, pool, alice)

	// Post 1 is alice's first post of topic 1.
	e, err := board.LoadEdit(ctx, pool, a, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !e.First || e.Title != "Welcome to the example board" || e.Source != "Welcome to the **example** board." ||
		e.Moderated || e.TopicID != 1 || e.ForumID != 2 || !e.Flags.Markdown || e.Stored == nil {
		t.Errorf("LoadEdit(1) = %+v", e)
	}
	// Post 2 is bob's: not alice's to edit.
	_, err = board.LoadEdit(ctx, pool, a, 2)
	wantIs(t, err, board.ErrForbidden, "another member's post")
	// A post that alice cannot see is a missing one.
	_, err = board.LoadEdit(ctx, pool, viewerFor(t, pool, carol), 5)
	wantIs(t, err, board.ErrNotFound, "post in an unreadable forum")
	_, err = board.LoadEdit(ctx, pool, a, 9999)
	wantIs(t, err, board.ErrNotFound, "missing post")
	_, err = board.LoadEdit(ctx, pool, asGuest(t, pool), 4)
	wantIs(t, err, board.ErrForbidden, "guest")

	// A moderator edits alice's post and gives a reason.
	m := moderator(t, pool, bob)
	e, err = board.LoadEdit(ctx, pool, m, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !e.Moderated {
		t.Error("a moderator's edit of another member's post is not Moderated")
	}
	e, err = board.LoadEdit(ctx, pool, m, 2)
	if err != nil {
		t.Fatal(err)
	}
	if e.First || e.Moderated {
		t.Errorf("bob's own reply: %+v", e)
	}

	// A locked topic stops an author, not a moderator.
	if _, err := pool.Exec(ctx, `update topics set locked = true where id = 1`); err != nil {
		t.Fatal(err)
	}
	_, err = board.LoadEdit(ctx, pool, a, 1)
	wantIs(t, err, board.ErrForbidden, "own post in a locked topic")
	if _, err := board.LoadEdit(ctx, pool, m, 1); err != nil {
		t.Errorf("moderator, locked topic: %v", err)
	}
}

func TestLoadDelete(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := viewerFor(t, pool, carol)

	// Post 3 is carol's own held reply; she lacks post.delete_own.
	_, err := board.LoadDelete(ctx, pool, c, 3)
	wantIs(t, err, board.ErrForbidden, "no post.delete_own")
	grantOpts(t, pool, carol, 2, "post.delete_own")
	c = viewerFor(t, pool, carol)
	d, err := board.LoadDelete(ctx, pool, c, 3)
	if err != nil {
		t.Fatal(err)
	}
	if d.TopicID != 1 || d.ForumID != 2 || d.First {
		t.Errorf("LoadDelete(3) = %+v", d)
	}
	_, err = board.LoadDelete(ctx, pool, c, 2)
	wantIs(t, err, board.ErrForbidden, "another member's post")
	_, err = board.LoadDelete(ctx, pool, c, 5)
	wantIs(t, err, board.ErrNotFound, "unreadable")
	_, err = board.LoadDelete(ctx, pool, asGuest(t, pool), 4)
	wantIs(t, err, board.ErrForbidden, "guest")

	// The first post of a topic with replies is not the author's to delete.
	grantOpts(t, pool, alice, 2, "post.delete_own")
	a := viewerFor(t, pool, alice)
	_, err = board.LoadDelete(ctx, pool, a, 1)
	wantIs(t, err, board.ErrForbidden, "first post with replies")
	m := moderator(t, pool, bob)
	d, err = board.LoadDelete(ctx, pool, m, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !d.First {
		t.Error("post 1 is the first post")
	}
}

func TestCard(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c, err := board.Card(ctx, pool, alice)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != alice || c.Name != "alice" || c.PostCount != 120 || c.RankTitle != "Newcomer" {
		t.Errorf("Card = %+v", c)
	}
	_, err = board.Card(ctx, pool, 9999)
	wantIs(t, err, board.ErrNotFound, "missing user")
}

func TestHeads(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := viewerFor(t, pool, carol)
	f, crumbs, err := board.ForumHead(ctx, pool, c, 2)
	if err != nil || f.Name != "Main Discussion" || len(crumbs) != 1 || crumbs[0].ID != 1 {
		t.Errorf("ForumHead = %+v %v %v", f, crumbs, err)
	}
	_, _, err = board.ForumHead(ctx, pool, c, 3)
	wantIs(t, err, board.ErrNotFound, "unreadable forum")
	title, f, _, err := board.TopicHead(ctx, pool, c, 1)
	if err != nil || title != "Welcome to the example board" || f.ID != 2 {
		t.Errorf("TopicHead = %q %+v %v", title, f, err)
	}
	_, _, _, err = board.TopicHead(ctx, pool, c, 2)
	wantIs(t, err, board.ErrNotFound, "topic in an unreadable forum")
}
