package board_test

import (
	"context"
	"errors"
	"testing"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// TestModeratorButtons: Restore and Approve follow mod.delete and mod.approve,
// and show on the posts they apply to. Delete and Restore never show together.
func TestModeratorButtons(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	m := moderator(t, pool, bob)
	c := viewerFor(t, pool, carol)

	buttons := func(v board.Viewer) []board.PostButtons {
		t.Helper()
		tp, err := board.TopicView(ctx, pool, v, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		return tp.Buttons
	}
	// Topic 1 holds posts 1, 2, 3 (held, carol's) and 4, in that order.
	b := buttons(m)
	for i, want := range []struct{ del, restore, approve bool }{
		{true, false, false}, {true, false, false}, {true, false, true}, {true, false, false},
	} {
		if b[i].Delete != want.del || b[i].Restore != want.restore || b[i].Approve != want.approve {
			t.Errorf("moderator, post %d: %+v, want %+v", i+1, b[i], want)
		}
	}
	// A plain member has neither button.
	for i, got := range buttons(c) {
		if got.Restore || got.Approve {
			t.Errorf("plain member, post %d: %+v", i+1, got)
		}
	}

	// A deleted post offers Restore and no Delete, to a holder of mod.delete only.
	if _, err := pool.Exec(ctx, `update posts set visibility = 'deleted', visibility_before_delete = 'approved' where id = 4`); err != nil {
		t.Fatal(err)
	}
	if got := buttons(m)[3]; !got.Restore || got.Delete || got.Approve || got.Edit {
		t.Errorf("deleted post, moderator: %+v", got)
	}
	grantOpts(t, pool, carol, 2, "mod.approve")
	for i, got := range buttons(viewerFor(t, pool, carol)) {
		if got.Restore {
			t.Errorf("mod.approve alone, post %d: Restore", i+1)
		}
	}

	// A held topic with an approved first post offers Approve on that post.
	if _, err := pool.Exec(ctx, `update topics set visibility = 'unapproved' where id = 1`); err != nil {
		t.Fatal(err)
	}
	if got := buttons(m)[0]; !got.Approve {
		t.Errorf("first post of a held topic: %+v", got)
	}
	if got := buttons(m)[1]; got.Approve {
		t.Errorf("second post of a held topic: %+v", got)
	}
}

// TestLockButtons: the topic offers Lock or Unlock by the rule of SetTopicLock.
func TestLockButtons(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	page := func(v board.Viewer) board.TopicPage {
		t.Helper()
		tp, err := board.TopicView(ctx, pool, v, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		return tp
	}
	want := func(who string, v board.Viewer, lock, unlock bool) {
		t.Helper()
		tp := page(v)
		if tp.CanLock != lock || tp.CanUnlock != unlock {
			t.Errorf("%s: CanLock %v CanUnlock %v, want %v %v", who, tp.CanLock, tp.CanUnlock, lock, unlock)
		}
	}
	setLocked := func(locked bool) {
		t.Helper()
		if _, err := pool.Exec(ctx, `update topics set locked = $1 where id = 1`, locked); err != nil {
			t.Fatal(err)
		}
	}
	m := moderator(t, pool, bob)
	c := viewerFor(t, pool, carol)
	want("moderator, open", m, true, false)
	want("plain member, open", c, false, false)
	want("guest, open", asGuest(t, pool), false, false)
	setLocked(true)
	want("moderator, locked", m, false, true)
	want("plain member, locked", c, false, false)

	// Alice wrote topic 1. With topic.lock_own she locks it, and unlocks only her own lock.
	grantOpts(t, pool, alice, 2, "topic.lock_own")
	a := viewerFor(t, pool, alice)
	want("author, locked with no log row", a, false, false)
	setLocked(false)
	want("author, open", a, true, false)
	if err := board.SetTopicLock(ctx, pool, a, 1, true, t0); err != nil {
		t.Fatal(err)
	}
	want("author, locked by the author", a, false, true)
	if err := board.SetTopicLock(ctx, pool, a, 1, false, t0.Add(1)); err != nil {
		t.Fatal(err)
	}
	if err := board.SetTopicLock(ctx, pool, m, 1, true, t0.Add(2)); err != nil {
		t.Fatal(err)
	}
	want("author, locked by a moderator", a, false, false)
	// Bob wrote none of topic 1's rows, and holds no lock_own: only mod.lock counts.
	grantOpts(t, pool, carol, 2, "topic.lock_own")
	want("lock_own, not the author", viewerFor(t, pool, carol), false, false)
}

// TestLockedErrors: a reply that a lock stops says which lock.
func TestLockedErrors(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := viewerFor(t, pool, carol)
	d := board.Draft{Source: "A reply.", IP: ip1}

	if _, err := pool.Exec(ctx, `update topics set locked = true where id = 1`); err != nil {
		t.Fatal(err)
	}
	wantIs(t, board.CanReply(ctx, pool, c, 1), board.ErrTopicLocked, "CanReply, locked topic")
	wantIs(t, board.CanReply(ctx, pool, c, 1), board.ErrForbidden, "a lock is a refusal")
	_, err := board.Reply(ctx, pool, c, 1, d, t0)
	wantIs(t, err, board.ErrTopicLocked, "Reply, locked topic")

	if _, err := pool.Exec(ctx, `update topics set locked = false where id = 1; update forums set locked = true where id = 2`); err != nil {
		t.Fatal(err)
	}
	wantIs(t, board.CanReply(ctx, pool, c, 1), board.ErrForbidden, "CanReply, locked forum")
	wantIs(t, board.CanReply(ctx, pool, c, 1), board.ErrForumLocked, "CanReply, locked forum")
	_, err = board.Reply(ctx, pool, c, 1, d, t0)
	wantIs(t, err, board.ErrForumLocked, "Reply, locked forum")

	// No topic.reply in an open topic is a plain refusal, not a lock.
	if _, err := pool.Exec(ctx, `update forums set locked = false where id = 2`); err != nil {
		t.Fatal(err)
	}
	err = board.CanReply(ctx, pool, asGuest(t, pool), 1)
	wantIs(t, err, board.ErrForbidden, "guest")
	if errIsLock(err) {
		t.Errorf("a guest with no topic.reply is told a lock stops them: %v", err)
	}
}

func errIsLock(err error) bool {
	return errors.Is(err, board.ErrTopicLocked) || errors.Is(err, board.ErrForumLocked)
}
