package board_test

import (
	"context"
	"errors"
	"strings"
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
	// A moderator may lock over the lock of the author: Lock and Unlock both show.
	want("moderator, locked by the author", m, true, true)
	if err := board.SetTopicLock(ctx, pool, a, 1, false, t0.Add(1)); err != nil {
		t.Fatal(err)
	}
	if err := board.SetTopicLock(ctx, pool, m, 1, true, t0.Add(2)); err != nil {
		t.Fatal(err)
	}
	want("author, locked by a moderator", a, false, false)
	want("moderator, locked by a moderator", m, false, true)
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

// TestModeratorEditOfOwnPost: a moderator who edits the own post by mod.edit alone
// (the topic is locked, so post.edit_own stops) is not asked for a reason. A reason
// that arrives anyway is neither checked, nor stored, nor logged. The edit still
// writes its log row. Another member's post still asks.
func TestModeratorEditOfOwnPost(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	m := moderator(t, pool, bob)
	if _, err := pool.Exec(ctx, `update topics set locked = true where id = 1`); err != nil {
		t.Fatal(err)
	}

	// Post 2 is bob's.
	e, err := board.LoadEdit(ctx, pool, m, 2)
	if err != nil {
		t.Fatal(err)
	}
	if e.Moderated {
		t.Error("the form asks bob for a reason on his own post")
	}
	if e, err = board.LoadEdit(ctx, pool, m, 1); err != nil || !e.Moderated {
		t.Errorf("alice's post: Moderated %v, %v; want the form to ask", e.Moderated, err)
	}

	forged := strings.Repeat("r", 300) // past the limit: not checked, since not asked
	d := board.Draft{Source: "Bob edits his own post.", IP: ip1}
	if err := board.EditPost(ctx, pool, m, 2, d, forged, t0); err != nil {
		t.Fatalf("EditPost of the own post with a reason past the limit: %v", err)
	}
	var stored, details string
	if err := pool.QueryRow(ctx, `select edit_reason from posts where id = 2`).Scan(&stored); err != nil || stored != "" {
		t.Errorf("stored reason = %q (%v)", stored, err)
	}
	if err := pool.QueryRow(ctx, `select details->>'reason' from mod_log where action = 'post.edit' and target_id = 2`).Scan(&details); err != nil {
		t.Fatalf("the edit wrote no log row: %v", err)
	}
	if details != "" {
		t.Errorf("logged reason = %q, want none", details)
	}

	// Another member's post: the reason is checked, stored and logged.
	d = board.Draft{Title: "Welcome again", Source: "Bob edits alice's post.", IP: ip1}
	_, err = board.LoadEdit(ctx, pool, m, 1)
	if err != nil {
		t.Fatal(err)
	}
	wantInput(t, board.EditPost(ctx, pool, m, 1, d, forged, t0.Add(1)), "reason", "")
	if err := board.EditPost(ctx, pool, m, 1, d, "typo", t0.Add(2)); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select edit_reason from posts where id = 1`).Scan(&stored); err != nil || stored != "typo" {
		t.Errorf("stored reason = %q (%v)", stored, err)
	}
}
