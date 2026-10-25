package board_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/counters"
)

func TestWritesCheckOwnership(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := viewerFor(t, pool, carol)
	d := board.Draft{Title: "x", Source: "changed", IP: ip1}
	snapshot := counterSnapshot(t, pool)

	// Post 1 is alice's, in forum 2, which carol can read.
	wantIs(t, board.EditPost(ctx, pool, c, 1, d, "", t0), board.ErrForbidden, "EditPost of another member's post")
	wantIs(t, board.DeletePost(ctx, pool, c, 1, t0), board.ErrForbidden, "DeletePost of another member's post")
	wantIs(t, board.RestorePost(ctx, pool, c, 1, t0), board.ErrForbidden, "RestorePost")
	wantIs(t, board.ApprovePost(ctx, pool, c, 1, t0), board.ErrForbidden, "ApprovePost")
	wantIs(t, board.SetTopicLock(ctx, pool, c, 1, true, t0), board.ErrForbidden, "SetTopicLock by a member who does not own the topic")

	// A post in a forum carol cannot read (post 5, forum 3) is not there for her.
	wantIs(t, board.EditPost(ctx, pool, c, 5, d, "", t0), board.ErrNotFound, "EditPost in an unreadable forum")
	wantIs(t, board.DeletePost(ctx, pool, c, 5, t0), board.ErrNotFound, "DeletePost in an unreadable forum")
	wantIs(t, board.RestorePost(ctx, pool, c, 5, t0), board.ErrNotFound, "RestorePost in an unreadable forum")
	wantIs(t, board.ApprovePost(ctx, pool, c, 5, t0), board.ErrNotFound, "ApprovePost in an unreadable forum")
	wantIs(t, board.SetTopicLock(ctx, pool, c, 2, true, t0), board.ErrNotFound, "SetTopicLock in an unreadable forum")
	_, err := board.Reply(ctx, pool, c, 2, board.Draft{Source: "x", IP: ip1}, t0)
	wantIs(t, err, board.ErrNotFound, "Reply in an unreadable forum")

	// A missing post is the same.
	wantIs(t, board.EditPost(ctx, pool, c, 9999, d, "", t0), board.ErrNotFound, "EditPost of a missing post")
	wantIs(t, board.DeletePost(ctx, pool, c, 9999, t0), board.ErrNotFound, "DeletePost of a missing post")
	wantIs(t, board.SetTopicLock(ctx, pool, c, 9999, true, t0), board.ErrNotFound, "SetTopicLock of a missing topic")

	// A held post of another member is not there for a member who cannot approve (post 3 is carol's).
	a := viewerFor(t, pool, alice)
	wantIs(t, board.EditPost(ctx, pool, a, 3, d, "", t0), board.ErrNotFound, "EditPost of a held post")
	wantIs(t, board.DeletePost(ctx, pool, a, 3, t0), board.ErrNotFound, "DeletePost of a held post")
	wantIs(t, board.ApprovePost(ctx, pool, a, 3, t0), board.ErrNotFound, "ApprovePost of a held post")

	// A guest may not edit, delete or moderate, even where the guests group may post
	// and holds every moderator right.
	grantGuests := []string{"topic.reply", "topic.start", "post.edit_own", "post.delete_own", "post.no_approval",
		"mod.edit", "mod.delete", "mod.approve", "mod.lock", "topic.lock_own"}
	allowGuests(t, pool, 2, grantGuests...)
	for _, o := range []string{"mod.edit", "mod.delete", "mod.approve", "mod.lock"} {
		allowGuests(t, pool, 0, o)
	}
	g := asGuest(t, pool)
	wantIs(t, board.EditPost(ctx, pool, g, 4, d, "", t0), board.ErrForbidden, "guest EditPost of the guest post")
	wantIs(t, board.EditPost(ctx, pool, g, 1, d, "", t0), board.ErrForbidden, "guest EditPost")
	wantIs(t, board.DeletePost(ctx, pool, g, 4, t0), board.ErrForbidden, "guest DeletePost of the guest post")
	wantIs(t, board.DeletePost(ctx, pool, g, 1, t0), board.ErrForbidden, "guest DeletePost")
	wantIs(t, board.RestorePost(ctx, pool, g, 1, t0), board.ErrForbidden, "guest RestorePost")
	wantIs(t, board.ApprovePost(ctx, pool, g, 1, t0), board.ErrForbidden, "guest ApprovePost")
	wantIs(t, board.SetTopicLock(ctx, pool, g, 1, true, t0), board.ErrForbidden, "guest SetTopicLock")
	// A guest in a forum it cannot read gets the same "not there" as a member.
	wantIs(t, board.EditPost(ctx, pool, g, 5, d, "", t0), board.ErrNotFound, "guest EditPost in an unreadable forum")

	if got := counterSnapshot(t, pool); got != snapshot {
		t.Errorf("a refused write changed the counters\n%s\n%s", snapshot, got)
	}
	var changed int
	if err := pool.QueryRow(ctx, `select count(*) from posts where edit_count > 0 or visibility = 'deleted'`).Scan(&changed); err != nil || changed != 1 {
		t.Errorf("%d posts edited or deleted (%v), want only the fixture's post 1", changed, err)
	}
	if n := len(readLog(t, pool)); n != 0 {
		t.Errorf("%d mod_log rows after refused writes, want 0", n)
	}
}

// TestWriteRights has one case for each row of "Who may do what", allowed and refused.
func TestWriteRights(t *testing.T) {
	ctx := context.Background()

	t.Run("new topic needs topic.start", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		d := board.Draft{Title: "t", Source: "s", IP: ip1}
		if _, err := board.NewTopic(ctx, pool, viewerFor(t, pool, carol), 2, d, t0); err != nil {
			t.Errorf("allowed: %v", err)
		}
		denyOpts(t, pool, carol, 2, "topic.start")
		_, err := board.NewTopic(ctx, pool, viewerFor(t, pool, carol), 2, board.Draft{Title: "u", Source: "s2", IP: ip1}, t0)
		wantIs(t, err, board.ErrForbidden, "refused")
	})

	t.Run("new topic needs a forum of kind forum", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		// Forum 1 is a category. Give alice every right there that would matter.
		grantOpts(t, pool, alice, 1, "forum.read", "topic.start", "topic.reply")
		a := viewerFor(t, pool, alice)
		d := board.Draft{Title: "t", Source: "s", IP: ip1}
		_, err := board.NewTopic(ctx, pool, a, 1, d, t0)
		wantIs(t, err, board.ErrForbidden, "category")
		if _, err := board.NewTopic(ctx, pool, a, 2, d, t0); err != nil {
			t.Errorf("forum: %v", err)
		}
		_, err = board.NewTopic(ctx, pool, a, 9999, d, t0)
		wantIs(t, err, board.ErrNotFound, "missing forum")
		// Link forums are not forums either.
		if _, err := pool.Exec(ctx, `update forums set kind = 'link', link_url = 'https://example.com/' where id = 2`); err != nil {
			t.Fatal(err)
		}
		_, err = board.NewTopic(ctx, pool, a, 2, board.Draft{Title: "u", Source: "s2", IP: ip1}, t0)
		wantIs(t, err, board.ErrForbidden, "link forum")
	})

	t.Run("new topic in a forum the viewer cannot read", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		// carol would hold topic.start in forum 3, but cannot read it.
		grantOpts(t, pool, carol, 3, "topic.start")
		_, err := board.NewTopic(ctx, pool, viewerFor(t, pool, carol), 3, board.Draft{Title: "t", Source: "s", IP: ip1}, t0)
		wantIs(t, err, board.ErrNotFound, "unreadable forum")
		grantOpts(t, pool, carol, 3, "forum.read")
		if _, err := board.NewTopic(ctx, pool, viewerFor(t, pool, carol), 3, board.Draft{Title: "t", Source: "s", IP: ip1}, t0); err != nil {
			t.Errorf("readable: %v", err)
		}
	})

	t.Run("new topic needs an unlocked forum, or mod.lock", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		if _, err := pool.Exec(ctx, `update forums set locked = true where id = 2`); err != nil {
			t.Fatal(err)
		}
		d := board.Draft{Title: "t", Source: "s", IP: ip1}
		_, err := board.NewTopic(ctx, pool, viewerFor(t, pool, carol), 2, d, t0)
		wantIs(t, err, board.ErrForbidden, "locked forum")
		grantOpts(t, pool, carol, 2, "mod.lock")
		if _, err := board.NewTopic(ctx, pool, viewerFor(t, pool, carol), 2, d, t0); err != nil {
			t.Errorf("with mod.lock: %v", err)
		}
		// mod.lock in another forum does not help.
		grantOpts(t, pool, alice, 3, "mod.lock")
		_, err = board.NewTopic(ctx, pool, viewerFor(t, pool, alice), 2, board.Draft{Title: "u", Source: "s2", IP: ip1}, t0)
		wantIs(t, err, board.ErrForbidden, "mod.lock in another forum")
	})

	t.Run("reply needs topic.reply", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		d := board.Draft{Source: "reply", IP: ip1}
		if _, err := board.Reply(ctx, pool, viewerFor(t, pool, carol), 1, d, t0); err != nil {
			t.Errorf("allowed: %v", err)
		}
		denyOpts(t, pool, carol, 2, "topic.reply")
		_, err := board.Reply(ctx, pool, viewerFor(t, pool, carol), 1, board.Draft{Source: "reply 2", IP: ip1}, t0)
		wantIs(t, err, board.ErrForbidden, "refused")
	})

	t.Run("reply needs a topic the viewer can read", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		d := board.Draft{Source: "reply", IP: ip1}
		grantOpts(t, pool, carol, 3, "topic.reply")
		_, err := board.Reply(ctx, pool, viewerFor(t, pool, carol), 2, d, t0)
		wantIs(t, err, board.ErrNotFound, "unreadable")
		grantOpts(t, pool, carol, 3, "forum.read")
		if _, err := board.Reply(ctx, pool, viewerFor(t, pool, carol), 2, d, t0); err != nil {
			t.Errorf("readable: %v", err)
		}
		_, err = board.Reply(ctx, pool, viewerFor(t, pool, carol), 9999, d, t0)
		wantIs(t, err, board.ErrNotFound, "missing")
	})

	t.Run("reply needs an approved topic", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		c := viewerFor(t, pool, carol)
		pid, err := board.NewTopic(ctx, pool, c, 2, board.Draft{Title: "held", Source: "held text", IP: ip1}, t0)
		if err != nil {
			t.Fatal(err)
		}
		topic := readPost(t, pool, pid).TopicID
		d := board.Draft{Source: "to a held topic", IP: ip1}
		// The author sees the held topic and still may not reply to it.
		_, err = board.Reply(ctx, pool, c, topic, d, t0)
		wantIs(t, err, board.ErrForbidden, "author of a held topic")
		// Neither may a moderator who sees it.
		m := moderator(t, pool, alice)
		_, err = board.Reply(ctx, pool, m, topic, d, t0)
		wantIs(t, err, board.ErrForbidden, "moderator, held topic")
		// bob has board-wide mod.approve, so he sees the held topic and is refused.
		_, err = board.Reply(ctx, pool, viewerFor(t, pool, bob), topic, d, t0)
		wantIs(t, err, board.ErrForbidden, "bob, held topic")
		// After approval it works.
		if err := board.ApprovePost(ctx, pool, m, pid, t0); err != nil {
			t.Fatal(err)
		}
		if _, err := board.Reply(ctx, pool, c, topic, d, t0); err != nil {
			t.Errorf("after approval: %v", err)
		}
		// A deleted topic is refused too.
		if err := board.DeletePost(ctx, pool, m, pid, t0); err != nil {
			t.Fatal(err)
		}
		_, err = board.Reply(ctx, pool, m, topic, board.Draft{Source: "to a deleted topic", IP: ip1}, t0)
		wantIs(t, err, board.ErrForbidden, "deleted topic, viewed by a moderator")
		_, err = board.Reply(ctx, pool, c, topic, board.Draft{Source: "to a deleted topic", IP: ip1}, t0)
		wantIs(t, err, board.ErrNotFound, "deleted topic, viewed by a member")
	})

	t.Run("reply needs an unlocked topic and forum, or mod.lock", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		if _, err := pool.Exec(ctx, `update topics set locked = true where id = 1`); err != nil {
			t.Fatal(err)
		}
		d := board.Draft{Source: "reply", IP: ip1}
		_, err := board.Reply(ctx, pool, viewerFor(t, pool, carol), 1, d, t0)
		wantIs(t, err, board.ErrForbidden, "locked topic")
		grantOpts(t, pool, carol, 2, "mod.lock")
		if _, err := board.Reply(ctx, pool, viewerFor(t, pool, carol), 1, d, t0); err != nil {
			t.Errorf("locked topic, with mod.lock: %v", err)
		}
		if _, err := pool.Exec(ctx, `update topics set locked = false where id = 1; update forums set locked = true where id = 2`); err != nil {
			t.Fatal(err)
		}
		_, err = board.Reply(ctx, pool, viewerFor(t, pool, alice), 1, board.Draft{Source: "reply 2", IP: ip1}, t0)
		wantIs(t, err, board.ErrForbidden, "locked forum")
		if _, err := board.Reply(ctx, pool, viewerFor(t, pool, carol), 1, board.Draft{Source: "reply 3", IP: ip1}, t0); err != nil {
			t.Errorf("locked forum, with mod.lock: %v", err)
		}
	})

	t.Run("reply to a moved-topic marker", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		grantOpts(t, pool, alice, 3, "topic.reply")
		_, err := board.Reply(ctx, pool, viewerFor(t, pool, alice), 3, board.Draft{Source: "reply", IP: ip1}, t0)
		wantIs(t, err, board.ErrNotFound, "marker")
	})

	t.Run("edit own post needs post.edit_own, the author and an unlocked topic", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		// post 1 is alice's first post; post 2 is bob's reply.
		d := board.Draft{Title: "A new title", Source: "edited", IP: ip1}
		a := viewerFor(t, pool, alice)
		if err := board.EditPost(ctx, pool, a, 1, d, "", t0); err != nil {
			t.Errorf("allowed: %v", err)
		}
		wantIs(t, board.EditPost(ctx, pool, a, 2, d, "", t0), board.ErrForbidden, "another member's post")
		if _, err := pool.Exec(ctx, `update topics set locked = true where id = 1`); err != nil {
			t.Fatal(err)
		}
		wantIs(t, board.EditPost(ctx, pool, a, 1, d, "", t0), board.ErrForbidden, "locked topic")
		if _, err := pool.Exec(ctx, `update topics set locked = false where id = 1`); err != nil {
			t.Fatal(err)
		}
		denyOpts(t, pool, alice, 2, "post.edit_own")
		wantIs(t, board.EditPost(ctx, pool, viewerFor(t, pool, alice), 1, d, "", t0), board.ErrForbidden, "without post.edit_own")
	})

	t.Run("edit any post needs mod.edit in the forum", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		d := board.Draft{Source: "moderated text", IP: ip1}
		// mod.edit in forum 3 only: not enough in forum 2.
		grantOpts(t, pool, carol, 3, "mod.edit")
		wantIs(t, board.EditPost(ctx, pool, viewerFor(t, pool, carol), 2, d, "why", t0), board.ErrForbidden, "mod.edit in another forum")
		grantOpts(t, pool, carol, 2, "mod.edit")
		if err := board.EditPost(ctx, pool, viewerFor(t, pool, carol), 2, d, "why", t0); err != nil {
			t.Errorf("allowed: %v", err)
		}
		// A moderator edits through a lock.
		if _, err := pool.Exec(ctx, `update topics set locked = true where id = 1`); err != nil {
			t.Fatal(err)
		}
		if err := board.EditPost(ctx, pool, viewerFor(t, pool, carol), 2, board.Draft{Source: "moderated again", IP: ip1}, "again", t0); err != nil {
			t.Errorf("locked topic, moderator: %v", err)
		}
	})

	t.Run("delete own post", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		grantOpts(t, pool, bob, 2, "post.delete_own")
		grantOpts(t, pool, alice, 2, "post.delete_own")
		// bob's post 2 is a reply: the author may delete it. (bob is a moderator, but not with mod.delete.)
		b := viewerFor(t, pool, bob)
		wantIs(t, board.DeletePost(ctx, pool, b, 1, t0), board.ErrForbidden, "another member's post")
		if err := board.DeletePost(ctx, pool, b, 2, t0); err != nil {
			t.Errorf("own reply: %v", err)
		}
		// alice's post 1 is the first post of a topic with other approved posts (4 is approved).
		a := viewerFor(t, pool, alice)
		wantIs(t, board.DeletePost(ctx, pool, a, 1, t0), board.ErrForbidden, "first post with replies")
		// With mod.delete she may.
		grantOpts(t, pool, alice, 2, "mod.delete")
		if err := board.DeletePost(ctx, pool, viewerFor(t, pool, alice), 1, t0); err != nil {
			t.Errorf("first post with replies, mod.delete: %v", err)
		}
	})

	t.Run("delete own first post without other approved posts", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		grantOpts(t, pool, carol, 2, "post.delete_own")
		c := viewerFor(t, pool, carol)
		pid, err := board.NewTopic(ctx, pool, c, 2, board.Draft{Title: "mine", Source: "only post", IP: ip1}, t0)
		if err != nil {
			t.Fatal(err)
		}
		topic := readPost(t, pool, pid).TopicID
		// A held reply by someone else is not an approved post.
		if _, err := board.Reply(ctx, pool, viewerFor(t, pool, bob), topic, board.Draft{Source: "x", IP: ip1}, t0); err == nil {
			t.Fatal("bob replied to a held topic")
		}
		if err := board.DeletePost(ctx, pool, c, pid, t0); err != nil {
			t.Errorf("own first post: %v", err)
		}
		if got := readTopic(t, pool, topic).Vis; got != "deleted" {
			t.Errorf("topic = %q, want deleted", got)
		}
	})

	t.Run("delete needs post.delete_own", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		// carol has the standard role, which has no post.delete_own.
		c := viewerFor(t, pool, carol)
		rid, err := board.Reply(ctx, pool, c, 1, board.Draft{Source: "my reply", IP: ip1}, t0)
		if err != nil {
			t.Fatal(err)
		}
		wantIs(t, board.DeletePost(ctx, pool, c, rid, t0), board.ErrForbidden, "own post without post.delete_own")
		grantOpts(t, pool, carol, 2, "post.delete_own")
		if err := board.DeletePost(ctx, pool, viewerFor(t, pool, carol), rid, t0); err != nil {
			t.Errorf("with post.delete_own: %v", err)
		}
	})

	t.Run("delete any, restore and approve", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		// Each right alone, in forum 2 only.
		grantOpts(t, pool, carol, 3, "mod.delete", "mod.approve")
		c := viewerFor(t, pool, carol)
		wantIs(t, board.DeletePost(ctx, pool, c, 2, t0), board.ErrForbidden, "mod.delete in another forum")
		wantIs(t, board.RestorePost(ctx, pool, c, 2, t0), board.ErrForbidden, "restore without mod.delete here")
		wantIs(t, board.ApprovePost(ctx, pool, c, 3, t0), board.ErrForbidden, "mod.approve in another forum")

		// carol's own held post 3: she sees it, and she may not approve it.
		grantOpts(t, pool, carol, 2, "mod.delete")
		d := viewerFor(t, pool, carol)
		wantIs(t, board.ApprovePost(ctx, pool, d, 3, t0), board.ErrForbidden, "own held post without mod.approve")
		if err := board.DeletePost(ctx, pool, d, 2, t0); err != nil {
			t.Errorf("mod.delete: %v", err)
		}
		if err := board.RestorePost(ctx, pool, d, 2, t0); err != nil {
			t.Errorf("restore with mod.delete: %v", err)
		}
		grantOpts(t, pool, carol, 2, "mod.approve")
		if err := board.ApprovePost(ctx, pool, viewerFor(t, pool, carol), 3, t0); err != nil {
			t.Errorf("approve with mod.approve: %v", err)
		}
		// A viewer who can read a deleted post only through mod.delete: others do not see it.
		if err := board.DeletePost(ctx, pool, d, 2, t0); err != nil {
			t.Fatal(err)
		}
		wantIs(t, board.RestorePost(ctx, pool, viewerFor(t, pool, alice), 2, t0), board.ErrNotFound, "restore of a deleted post the viewer cannot see")
	})

	t.Run("lock needs mod.lock, or topic.lock_own for the author", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		// Topic 1 is alice's. carol holds neither right.
		wantIs(t, board.SetTopicLock(ctx, pool, viewerFor(t, pool, carol), 1, true, t0), board.ErrForbidden, "no right")
		// topic.lock_own alone: only for the author's own topic.
		grantOpts(t, pool, carol, 2, "topic.lock_own")
		wantIs(t, board.SetTopicLock(ctx, pool, viewerFor(t, pool, carol), 1, true, t0), board.ErrForbidden, "lock_own on another member's topic")
		grantOpts(t, pool, alice, 2, "topic.lock_own")
		if err := board.SetTopicLock(ctx, pool, viewerFor(t, pool, alice), 1, true, t0); err != nil {
			t.Errorf("lock_own on the author's topic: %v", err)
		}
		if !readTopic(t, pool, 1).Locked {
			t.Error("topic 1 is not locked")
		}
		// mod.lock in another forum does not help.
		grantOpts(t, pool, carol, 3, "mod.lock")
		wantIs(t, board.SetTopicLock(ctx, pool, viewerFor(t, pool, carol), 1, false, t0), board.ErrForbidden, "mod.lock in another forum")
		grantOpts(t, pool, carol, 2, "mod.lock")
		if err := board.SetTopicLock(ctx, pool, viewerFor(t, pool, carol), 1, false, t0); err != nil {
			t.Errorf("mod.lock: %v", err)
		}
		if readTopic(t, pool, 1).Locked {
			t.Error("topic 1 is still locked")
		}
		// A marker is not a topic to lock.
		grantOpts(t, pool, alice, 3, "mod.lock")
		wantIs(t, board.SetTopicLock(ctx, pool, viewerFor(t, pool, alice), 3, true, t0), board.ErrForbidden, "marker")
	})
}

func TestDeleteFirstPostDeletesTopic(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	m := moderator(t, pool, carol)

	before := readForum(t, pool, 2)
	topic0 := readTopic(t, pool, 1)
	if before.Topics != 1 || before.Posts != 3 || topic0.Replies != 2 {
		t.Fatalf("fixture: forum 2 = %+v, topic 1 = %+v", before, topic0)
	}

	if err := board.DeletePost(ctx, pool, m, 1, t0); err != nil {
		t.Fatal(err)
	}
	if got := readTopic(t, pool, 1); got.Vis != "deleted" || got.Replies != 1 || got.Last != 4 {
		t.Errorf("topic after delete = %+v, want deleted, 1 reply, last post 4", got)
	}
	// The posts keep their own visibility.
	for id, want := range map[int64]string{1: "deleted", 2: "approved", 3: "unapproved", 4: "approved"} {
		if got := readPost(t, pool, id).Vis; got != want {
			t.Errorf("post %d = %q, want %q", id, got, want)
		}
	}
	if got := readForum(t, pool, 2); got.Topics != 0 || got.Posts != 0 || got.Last != 0 {
		t.Errorf("forum after delete = %+v, want no topics, no posts and no last post", got)
	}
	wantCountersExact(t, pool, "deleting a first post")

	// The topic is gone for a member, and there for a moderator.
	if _, err := board.TopicView(ctx, pool, viewerFor(t, pool, alice), 1, 1); err == nil {
		t.Error("a member sees the deleted topic")
	}
	if _, err := board.TopicView(ctx, pool, m, 1, 1); err != nil {
		t.Errorf("the moderator does not see the deleted topic: %v", err)
	}

	if err := board.RestorePost(ctx, pool, m, 1, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := readTopic(t, pool, 1); got != topic0 {
		t.Errorf("topic after restore = %+v, want %+v", got, topic0)
	}
	if got := readForum(t, pool, 2); got != before {
		t.Errorf("forum after restore = %+v, want %+v", got, before)
	}
	if got := readPost(t, pool, 1).Vis; got != "approved" {
		t.Errorf("post 1 = %q after restore", got)
	}
	wantCountersExact(t, pool, "restoring a first post")

	// Delete and restore of a reply leave the topic alone.
	if err := board.DeletePost(ctx, pool, m, 2, t0); err != nil {
		t.Fatal(err)
	}
	if got := readTopic(t, pool, 1); got.Vis != "approved" || got.Replies != 1 {
		t.Errorf("topic after deleting a reply = %+v", got)
	}
	if got := readForum(t, pool, 2); got.Posts != 2 || got.Topics != 1 {
		t.Errorf("forum after deleting a reply = %+v", got)
	}
}

func TestCountersMatchRecompute(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	grantOpts(t, pool, alice, 2, "post.no_approval", "post.counts", "post.delete_own")
	grantOpts(t, pool, carol, 2, "post.counts")
	a := viewerFor(t, pool, alice)
	m := moderator(t, pool, bob)
	allowGuests(t, pool, 2, "topic.reply")
	g := asGuest(t, pool)
	c := viewerFor(t, pool, carol)

	pid, err := board.NewTopic(ctx, pool, a, 2, board.Draft{Title: "Counters", Source: "first", IP: ip1}, t0)
	if err != nil {
		t.Fatal(err)
	}
	wantCountersExact(t, pool, "NewTopic")
	topic := readPost(t, pool, pid).TopicID

	r1, err := board.Reply(ctx, pool, a, topic, board.Draft{Source: "reply one", IP: ip1}, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	wantCountersExact(t, pool, "Reply")
	held, err := board.Reply(ctx, pool, c, topic, board.Draft{Source: "held reply", IP: ip2}, t0.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	wantCountersExact(t, pool, "a held Reply")
	gh, err := board.Reply(ctx, pool, g, topic, board.Draft{Source: "guest reply", GuestName: "Visitor", IP: ip2}, t0.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	wantCountersExact(t, pool, "a guest Reply")

	if err := board.EditPost(ctx, pool, a, r1, board.Draft{Source: "reply one, edited", IP: ip1}, "", t0.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantCountersExact(t, pool, "EditPost")
	if err := board.EditPost(ctx, pool, a, pid, board.Draft{Title: "Counters, renamed", Source: "first, edited", IP: ip1}, "", t0.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantCountersExact(t, pool, "EditPost of the first post")

	if err := board.DeletePost(ctx, pool, a, r1, t0.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantCountersExact(t, pool, "DeletePost of a reply")
	if err := board.RestorePost(ctx, pool, m, r1, t0.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantCountersExact(t, pool, "RestorePost of a reply")
	if err := board.ApprovePost(ctx, pool, m, held, t0.Add(8*time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantCountersExact(t, pool, "ApprovePost")
	if err := board.ApprovePost(ctx, pool, m, gh, t0.Add(9*time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantCountersExact(t, pool, "ApprovePost of a guest post")
	if err := board.DeletePost(ctx, pool, m, pid, t0.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantCountersExact(t, pool, "DeletePost of a first post")
	if err := board.RestorePost(ctx, pool, m, pid, t0.Add(11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantCountersExact(t, pool, "RestorePost of a first post")
	if err := board.SetTopicLock(ctx, pool, m, topic, true, t0.Add(12*time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantCountersExact(t, pool, "SetTopicLock")
	if err := board.SetTopicLock(ctx, pool, m, topic, false, t0.Add(13*time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantCountersExact(t, pool, "SetTopicLock off")

	// The newest post is the last post, by the time of the post and not the time of the write.
	if got := readTopic(t, pool, topic); got.Last != gh || got.Replies != 3 {
		t.Errorf("topic = %+v, want last post %d and 3 replies", got, gh)
	}
}

func TestModLog(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	grantOpts(t, pool, alice, 2, "post.delete_own")
	a := viewerFor(t, pool, alice)
	m := moderator(t, pool, carol)

	var want []logRow
	step := func(err error, action, targetType string, target int64, details string, at time.Time) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		want = append(want, logRow{Actor: carol, Action: action, TargetType: targetType, Target: target, Details: details, At: at})
		got := readLog(t, pool)
		if len(got) != len(want) {
			t.Fatalf("after %s: %d mod_log rows, want %d: %+v", action, len(got), len(want), got)
		}
		g, w := got[len(got)-1], want[len(want)-1]
		if g.Actor != w.Actor || g.Action != w.Action || g.TargetType != w.TargetType || g.Target != w.Target || !g.At.Equal(w.At) {
			t.Errorf("after %s: last row = %+v, want %+v", action, g, w)
		}
		var same bool
		if err := pool.QueryRow(ctx, `select $1::jsonb = $2::jsonb`, g.Details, w.Details).Scan(&same); err != nil || !same {
			t.Errorf("after %s: details = %s, want %s", action, g.Details, w.Details)
		}
	}
	at := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Minute) }

	step(board.EditPost(ctx, pool, m, 2, board.Draft{Source: "moderated", IP: ip1}, "removed a link", at(1)),
		"post.edit", "post", 2, `{"reason":"removed a link"}`, at(1))
	step(board.DeletePost(ctx, pool, m, 2, at(2)), "post.delete", "post", 2, `{}`, at(2))
	step(board.RestorePost(ctx, pool, m, 2, at(3)), "post.restore", "post", 2, `{}`, at(3))
	step(board.ApprovePost(ctx, pool, m, 3, at(4)), "post.approve", "post", 3, `{}`, at(4))
	step(board.SetTopicLock(ctx, pool, m, 1, true, at(5)), "topic.lock", "topic", 1, `{}`, at(5))
	step(board.SetTopicLock(ctx, pool, m, 1, false, at(6)), "topic.unlock", "topic", 1, `{}`, at(6))

	// Nothing that changes nothing writes a row: a repeat, or a refusal.
	if err := board.SetTopicLock(ctx, pool, m, 1, false, at(7)); err != nil {
		t.Errorf("unlock of an unlocked topic: %v", err)
	}
	if err := board.RestorePost(ctx, pool, m, 2, at(7)); err != nil {
		t.Errorf("restore of a live post: %v", err)
	}
	if err := board.ApprovePost(ctx, pool, m, 3, at(7)); err != nil {
		t.Errorf("approve of an approved post: %v", err)
	}
	wantIs(t, board.EditPost(ctx, pool, viewerFor(t, pool, carol), 99999, board.Draft{Source: "x"}, "", at(7)), board.ErrNotFound, "edit of a missing post")
	wantIs(t, board.DeletePost(ctx, pool, viewerFor(t, pool, bob), 1, at(7)), board.ErrForbidden, "delete without mod.delete")
	if n := len(readLog(t, pool)); n != len(want) {
		t.Fatalf("%d rows after repeats and refusals, want %d", n, len(want))
	}
	if err := board.DeletePost(ctx, pool, m, 2, at(8)); err != nil {
		t.Fatal(err)
	}
	if err := board.DeletePost(ctx, pool, m, 2, at(9)); err != nil {
		t.Fatalf("a second delete: %v", err)
	}
	want = append(want, logRow{})
	if n := len(readLog(t, pool)); n != len(want) {
		t.Fatalf("%d rows after a repeated delete, want %d: it is one action", n, len(want))
	}

	// An author's own edit and delete write nothing.
	n := len(readLog(t, pool))
	if err := board.EditPost(ctx, pool, a, 1, board.Draft{Title: "t", Source: "own edit", IP: ip1}, "a reason that is ignored", at(10)); err != nil {
		t.Fatal(err)
	}
	rid, err := board.Reply(ctx, pool, a, 1, board.Draft{Source: "to delete", IP: ip1}, at(11))
	if err != nil {
		t.Fatal(err)
	}
	if err := board.DeletePost(ctx, pool, a, rid, at(12)); err != nil {
		t.Fatal(err)
	}
	if got := len(readLog(t, pool)); got != n {
		t.Errorf("%d mod_log rows after the author's own edit and delete, want %d", got, n)
	}
}

func TestEditPost(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	a := viewerFor(t, pool, alice)

	// Post 1 is the first post of topic 1 and was edited once, with the reason "typo".
	// That reason belongs to the old edit.
	if err := board.EditPost(ctx, pool, a, 1, board.Draft{Title: "  Renamed topic ", Source: "Edited **text**", IP: ip2}, "ignored", t0); err != nil {
		t.Fatal(err)
	}
	p := readPost(t, pool, 1)
	if p.EditCount != 2 || p.EditedAt == nil || !p.EditedAt.Equal(t0) || p.EditedBy != alice {
		t.Errorf("edit fields = %d %v %d", p.EditCount, p.EditedAt, p.EditedBy)
	}
	if p.EditReason != "" {
		t.Errorf("edit_reason = %q: an author's own edit clears the old reason", p.EditReason)
	}
	if p.Source != "Edited **text**" || p.Text != "Edited text" || p.Subject != "Renamed topic" {
		t.Errorf("post = %+v", p)
	}
	if !p.Created.Equal(time.Date(2019, 5, 2, 9, 0, 0, 0, time.UTC)) || p.IP != "10.0.0.5" {
		t.Errorf("created_at or author_ip changed: %v %q", p.Created, p.IP)
	}
	if got := readTopic(t, pool, 1).Title; got != "Renamed topic" {
		t.Errorf("topic title = %q", got)
	}
	var bold bool
	if err := pool.QueryRow(ctx, `select jsonb_path_exists(body_tree, '$.** ? (@.n == "b")') from posts where id = 1`).Scan(&bold); err != nil || !bold {
		t.Errorf("body_tree has no bold element (%v)", err)
	}

	// A first post needs a title.
	wantInput(t, board.EditPost(ctx, pool, a, 1, board.Draft{Title: " ", Source: "x"}, "", t0), "title", "")
	wantInput(t, board.EditPost(ctx, pool, a, 1, board.Draft{Title: "ok", Source: ""}, "", t0), "body", "")

	// A reply: the title is ignored and the topic keeps its title.
	grantOpts(t, pool, bob, 2, "post.edit_own")
	b := viewerFor(t, pool, bob)
	if err := board.EditPost(ctx, pool, b, 2, board.Draft{Title: "ignored title", Source: "bob edits"}, "", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := readTopic(t, pool, 1).Title; got != "Renamed topic" {
		t.Errorf("topic title = %q after an edit of a reply", got)
	}
	if got := readPost(t, pool, 2); got.Subject != "Re: Welcome" || got.EditCount != 1 || got.EditReason != "" {
		t.Errorf("reply = %+v", got)
	}

	// The stored flags stay: a post stored as plain text stays plain text.
	if _, err := pool.Exec(ctx, `update posts set enable_markdown = false, enable_smilies = false where id = 2`); err != nil {
		t.Fatal(err)
	}
	if err := board.EditPost(ctx, pool, b, 2, board.Draft{Source: "**still** :) plain"}, "", t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := readPost(t, pool, 2); got.Markdown || got.Smilies || !got.Autolink || got.Text != "**still** :) plain" {
		t.Errorf("flags or text after an edit = %+v", got)
	}

	// A moderator's edit of another member's post stores the reason.
	m := moderator(t, pool, carol)
	if err := board.EditPost(ctx, pool, m, 2, board.Draft{Source: "cleaned"}, "off topic", t0.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got := readPost(t, pool, 2)
	if got.EditReason != "off topic" || got.EditedBy != carol || got.EditCount != 3 {
		t.Errorf("after the moderator's edit = %+v", got)
	}
	// The reason is at most 255 characters.
	long := make([]rune, 256)
	for i := range long {
		long[i] = 'é'
	}
	wantInput(t, board.EditPost(ctx, pool, m, 2, board.Draft{Source: "cleaned 2"}, string(long), t0), "reason", "")
	if err := board.EditPost(ctx, pool, m, 2, board.Draft{Source: "cleaned 3"}, string(long[:255]), t0.Add(4*time.Minute)); err != nil {
		t.Errorf("a reason of 255 characters: %v", err)
	}
	wantInput(t, board.EditPost(ctx, pool, m, 2, board.Draft{Source: "cleaned 4"}, "a\nb", t0), "reason", "")

	// A moderator who lacks post.images cannot add an image to a post.
	wantInput(t, board.EditPost(ctx, pool, m, 4, board.Draft{Source: "![x](https://example.com/a.png)"}, "", t0), "body", msgImages)
	wantCountersExact(t, pool, "edits")
}

// TestPostCountFollowsApproval pins users.post_count: it moves with the approved
// posts of an author who holds post.counts in the forum.
func TestPostCountFollowsApproval(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	grantOpts(t, pool, carol, 2, "post.counts")
	m := moderator(t, pool, bob)
	c := viewerFor(t, pool, carol)
	base := userPostCount(t, pool, carol)

	hid, err := board.Reply(ctx, pool, c, 1, board.Draft{Source: "held", IP: ip1}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if got := userPostCount(t, pool, carol); got != base {
		t.Errorf("held post: post_count = %d, want %d", got, base)
	}
	if err := board.ApprovePost(ctx, pool, m, hid, t0); err != nil {
		t.Fatal(err)
	}
	if got := userPostCount(t, pool, carol); got != base+1 {
		t.Errorf("after approve: post_count = %d, want %d", got, base+1)
	}
	if err := board.DeletePost(ctx, pool, m, hid, t0); err != nil {
		t.Fatal(err)
	}
	if got := userPostCount(t, pool, carol); got != base {
		t.Errorf("after delete: post_count = %d, want %d", got, base)
	}
	if err := board.RestorePost(ctx, pool, m, hid, t0); err != nil {
		t.Fatal(err)
	}
	if got := userPostCount(t, pool, carol); got != base+1 {
		t.Errorf("after restore: post_count = %d, want %d", got, base+1)
	}
	// Delete of a held post changes no count.
	hid2, err := board.Reply(ctx, pool, c, 1, board.Draft{Source: "held 2", IP: ip1}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := board.DeletePost(ctx, pool, m, hid2, t0); err != nil {
		t.Fatal(err)
	}
	if got := userPostCount(t, pool, carol); got != base+1 {
		t.Errorf("after deleting a held post: post_count = %d, want %d", got, base+1)
	}
	// The right is the author's, not the moderator's: bob has no post.counts and
	// carol has, and the count still moved above. In forum 3, carol has no right.
	denyOpts(t, pool, carol, 2, "post.counts")
	if err := board.DeletePost(ctx, pool, m, hid, t0); err != nil {
		t.Fatal(err)
	}
	if got := userPostCount(t, pool, carol); got != base+1 {
		t.Errorf("after the author lost post.counts: post_count = %d, want %d", got, base+1)
	}
	// A count never goes below zero.
	if _, err := pool.Exec(ctx, `update users set post_count = 0 where id = $1`, carol); err != nil {
		t.Fatal(err)
	}
	grantOpts(t, pool, alice, 2, "post.counts", "post.no_approval")
	if _, err := pool.Exec(ctx, `update users set post_count = 0 where id = $1`, alice); err != nil {
		t.Fatal(err)
	}
	if err := board.DeletePost(ctx, pool, m, 1, t0); err != nil {
		t.Fatal(err)
	}
	if got := userPostCount(t, pool, alice); got != 0 {
		t.Errorf("post_count = %d, want 0", got)
	}
}

func TestRepeatedModeratorWrites(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	m := moderator(t, pool, carol)

	// Approve of an approved post, restore of a live post and a second lock change nothing.
	snap := counterSnapshot(t, pool)
	if err := board.ApprovePost(ctx, pool, m, 1, t0); err != nil {
		t.Errorf("approve of an approved post: %v", err)
	}
	if err := board.RestorePost(ctx, pool, m, 1, t0); err != nil {
		t.Errorf("restore of a live post: %v", err)
	}
	if err := board.SetTopicLock(ctx, pool, m, 1, false, t0); err != nil {
		t.Errorf("unlock of an unlocked topic: %v", err)
	}
	if counterSnapshot(t, pool) != snap || len(readLog(t, pool)) != 0 {
		t.Error("a write that changes nothing changed a row or wrote a log row")
	}
	// An approve of a deleted post is refused: restore it first.
	if err := board.DeletePost(ctx, pool, m, 3, t0); err != nil {
		t.Fatal(err)
	}
	wantIs(t, board.ApprovePost(ctx, pool, m, 3, t0), board.ErrForbidden, "approve of a deleted post")
	// Approving the first post of a held topic approves the topic.
	c := viewerFor(t, pool, carol)
	pid, err := board.NewTopic(ctx, pool, c, 2, board.Draft{Title: "held topic", Source: "text", IP: ip1}, t0)
	if err != nil {
		t.Fatal(err)
	}
	topic := readPost(t, pool, pid).TopicID
	if err := board.ApprovePost(ctx, pool, m, pid, t0); err != nil {
		t.Fatal(err)
	}
	if got := readTopic(t, pool, topic).Vis; got != "approved" {
		t.Errorf("topic = %q after the approval of its first post", got)
	}
	if got := readForum(t, pool, 2); got.Topics != 2 {
		t.Errorf("forum topic_count = %d, want 2", got.Topics)
	}
	wantCountersExact(t, pool, "approving a held topic")
}

// TestConcurrentWritesKeepCountersExact runs many writes at once on the same
// topics and forum. Each write computes a counter from the rows it can see, so
// only the row locks keep a late writer from storing a number that an earlier,
// concurrent writer has already made stale.
func TestConcurrentWritesKeepCountersExact(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	for _, u := range []int64{alice, bob, carol} {
		grantOpts(t, pool, u, 2, "post.no_approval", "post.counts", "post.delete_own")
	}
	m := moderator(t, pool, carol)
	users := []board.Viewer{viewerFor(t, pool, alice), viewerFor(t, pool, bob), m}

	// Three topics to write to, and the posts that the deleters take turns on.
	var topics []int64
	for i := range 3 {
		pid, err := board.NewTopic(ctx, pool, users[0], 2, board.Draft{Title: "t" + string(rune('a'+i)), Source: "first " + string(rune('a'+i)), IP: ip1}, t0)
		if err != nil {
			t.Fatal(err)
		}
		topics = append(topics, readPost(t, pool, pid).TopicID)
	}
	for round := range 4 {
		// The posts to delete are chosen before the writers start. Chosen by each
		// goroutine, as a count from the front, they depended on how many of the
		// other deletes had committed, and a late goroutine found no row.
		var targets []int64
		rows, err := pool.Query(ctx, `select id from posts where forum_id = 2 and visibility = 'approved' order by id limit 6`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			targets = append(targets, id)
		}
		if err := rows.Err(); err != nil || len(targets) != 6 {
			t.Fatalf("round %d: %d posts to delete (%v), want 6", round, len(targets), err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 64)
		start := make(chan struct{})
		for i := range 18 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				u := users[i%3]
				at := t0.Add(time.Duration(round*100+i) * time.Second)
				text := "round " + string(rune('a'+round)) + " writer " + string(rune('a'+i))
				switch i % 6 {
				case 5:
					_, err := board.NewTopic(ctx, pool, u, 2, board.Draft{Title: text, Source: text, IP: ip1}, at)
					errs <- err
				default:
					_, err := board.Reply(ctx, pool, u, topics[i%3], board.Draft{Source: text, IP: ip1}, at)
					errs <- err
				}
			}()
		}
		// At the same time, delete and restore the first posts and some replies.
		for i := range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				id := targets[i]
				at := t0.Add(time.Duration(round*100+50+i) * time.Second)
				if err := board.DeletePost(ctx, pool, m, id, at); err != nil {
					errs <- err
					return
				}
				if i%2 == 0 {
					errs <- board.RestorePost(ctx, pool, m, id, at.Add(time.Second))
					return
				}
				errs <- nil
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			// A reply to a topic that a concurrent delete has just hidden is refused.
			if err != nil && !errors.Is(err, board.ErrNotFound) && !errors.Is(err, board.ErrForbidden) {
				t.Fatalf("round %d: %v", round, err)
			}
		}
		wantCountersExact(t, pool, "a round of concurrent writes")
	}
}

// TestRestoreReturnsToTheStateBeforeTheDelete: a delete keeps the state of the
// row, and a restore puts it back. A row with no kept state (deleted before the
// migration, or imported) comes back approved only for a restorer with
// mod.approve.
func TestRestoreReturnsToTheStateBeforeTheDelete(t *testing.T) {
	ctx := context.Background()

	t.Run("a held post comes back held", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		grantOpts(t, pool, carol, 2, "post.delete_own", "post.counts")
		// alice can restore, and has no mod.approve.
		grantOpts(t, pool, alice, 2, "mod.delete")
		base := userPostCount(t, pool, carol)
		// Post 3 is carol's held reply.
		if err := board.DeletePost(ctx, pool, viewerFor(t, pool, carol), 3, t0); err != nil {
			t.Fatal(err)
		}
		if err := board.RestorePost(ctx, pool, viewerFor(t, pool, alice), 3, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if got := readPost(t, pool, 3).Vis; got != "unapproved" {
			t.Errorf("post 3 = %q after delete and restore, want unapproved", got)
		}
		if got := userPostCount(t, pool, carol); got != base {
			t.Errorf("carol post_count = %d, want %d: a held post does not count", got, base)
		}
		if got := readForum(t, pool, 2).Posts; got != 3 {
			t.Errorf("forum posts = %d, want 3", got)
		}
		wantCountersExact(t, pool, "restore of a held post")
		// Another delete and restore, now by a moderator who holds both rights.
		m := moderator(t, pool, bob)
		if err := board.DeletePost(ctx, pool, m, 3, t0.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if err := board.RestorePost(ctx, pool, m, 3, t0.Add(3*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if got := readPost(t, pool, 3).Vis; got != "unapproved" {
			t.Errorf("post 3 = %q after the second round, want unapproved", got)
		}
	})

	t.Run("an approved post comes back approved", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		grantOpts(t, pool, alice, 2, "mod.delete") // no mod.approve
		a := viewerFor(t, pool, alice)
		if err := board.DeletePost(ctx, pool, a, 2, t0); err != nil {
			t.Fatal(err)
		}
		if err := board.RestorePost(ctx, pool, a, 2, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if got := readPost(t, pool, 2).Vis; got != "approved" {
			t.Errorf("post 2 = %q, want approved", got)
		}
		wantCountersExact(t, pool, "restore of an approved post")
	})

	t.Run("a held topic comes back held", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		grantOpts(t, pool, carol, 2, "post.delete_own")
		grantOpts(t, pool, alice, 2, "mod.delete")
		c := viewerFor(t, pool, carol)
		forum0 := readForum(t, pool, 2)
		pid, err := board.NewTopic(ctx, pool, c, 2, board.Draft{Title: "held", Source: "held text", IP: ip1}, t0)
		if err != nil {
			t.Fatal(err)
		}
		topic := readPost(t, pool, pid).TopicID
		if err := board.DeletePost(ctx, pool, c, pid, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if got := readTopic(t, pool, topic).Vis; got != "deleted" {
			t.Fatalf("topic = %q after the delete", got)
		}
		if err := board.RestorePost(ctx, pool, viewerFor(t, pool, alice), pid, t0.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if got := readTopic(t, pool, topic).Vis; got != "unapproved" {
			t.Errorf("topic = %q, want unapproved", got)
		}
		if got := readPost(t, pool, pid).Vis; got != "unapproved" {
			t.Errorf("post = %q, want unapproved", got)
		}
		if got := readForum(t, pool, 2); got != forum0 {
			t.Errorf("forum = %+v, want %+v: a held topic does not count", got, forum0)
		}
		wantCountersExact(t, pool, "restore of a held topic")
	})

	t.Run("a deleted row with no kept state", func(t *testing.T) {
		for _, withApprove := range []bool{false, true} {
			pool, _ := boardtest.Load(t, bundletest.Small())
			// Posts 1 and 2 and topic 1 are deleted as an import would leave them.
			if _, err := pool.Exec(ctx, `update posts set visibility = 'deleted' where id in (1, 2); update topics set visibility = 'deleted' where id = 1`); err != nil {
				t.Fatal(err)
			}
			grantOpts(t, pool, alice, 2, "mod.delete")
			want := "unapproved"
			if withApprove {
				grantOpts(t, pool, alice, 2, "mod.approve")
				want = "approved"
			}
			a := viewerFor(t, pool, alice)
			if err := board.RestorePost(ctx, pool, a, 2, t0); err != nil {
				t.Fatal(err)
			}
			if got := readPost(t, pool, 2).Vis; got != want {
				t.Errorf("approve=%v: reply = %q, want %q", withApprove, got, want)
			}
			if err := board.RestorePost(ctx, pool, a, 1, t0); err != nil {
				t.Fatal(err)
			}
			if got := readPost(t, pool, 1).Vis; got != want {
				t.Errorf("approve=%v: first post = %q, want %q", withApprove, got, want)
			}
			if got := readTopic(t, pool, 1).Vis; got != want {
				t.Errorf("approve=%v: topic = %q, want %q", withApprove, got, want)
			}
			wantCountersExact(t, pool, "restore of imported deleted rows")
		}
	})
}

// TestApproveHeldTopicWithApprovedFirstPost: imported data can hold a held
// topic whose first post is approved. Approving that post approves the topic.
func TestApproveHeldTopicWithApprovedFirstPost(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	m := moderator(t, pool, carol)
	if _, err := pool.Exec(ctx, `update topics set visibility = 'unapproved' where id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := counters.Forum(ctx, pool, 2); err != nil {
		t.Fatal(err)
	}
	if got := readForum(t, pool, 2); got.Topics != 0 || got.Posts != 0 {
		t.Fatalf("forum = %+v with a held topic", got)
	}
	// A reply of the held topic changes nothing.
	if err := board.ApprovePost(ctx, pool, m, 2, t0); err != nil {
		t.Fatal(err)
	}
	if got := readTopic(t, pool, 1).Vis; got != "unapproved" {
		t.Errorf("topic = %q after approving a reply that was approved", got)
	}
	if n := len(readLog(t, pool)); n != 0 {
		t.Errorf("%d log rows after a no-op", n)
	}
	if err := board.ApprovePost(ctx, pool, m, 1, t0); err != nil {
		t.Fatal(err)
	}
	if got := readTopic(t, pool, 1).Vis; got != "approved" {
		t.Errorf("topic = %q, want approved", got)
	}
	if got := readForum(t, pool, 2); got.Topics != 1 || got.Posts != 3 {
		t.Errorf("forum = %+v, want 1 topic and 3 posts", got)
	}
	if l := readLog(t, pool); len(l) != 1 || l[0].Action != "post.approve" || l[0].Target != 1 {
		t.Errorf("log = %+v, want one post.approve of post 1", l)
	}
	wantCountersExact(t, pool, "approving a held topic")
	// A second approval changes nothing.
	if err := board.ApprovePost(ctx, pool, m, 1, t0); err != nil {
		t.Fatal(err)
	}
	if n := len(readLog(t, pool)); n != 1 {
		t.Errorf("%d log rows after the second approval, want 1", n)
	}
}

// TestOwnPostLog: a row records every action on another member's post, and an
// action on one's own post that only a moderator right allows. An action that the
// own right alone allows writes none, even when the author holds the moderator
// right too.
func TestOwnPostLog(t *testing.T) {
	ctx := context.Background()

	t.Run("edit", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		grantOpts(t, pool, alice, 2, "mod.edit")
		a := viewerFor(t, pool, alice)
		// Own post, unlocked topic: post.edit_own allows it alone.
		if err := board.EditPost(ctx, pool, a, 1, board.Draft{Title: "t", Source: "own 1", IP: ip1}, "r", t0); err != nil {
			t.Fatal(err)
		}
		if n := len(readLog(t, pool)); n != 0 {
			t.Fatalf("%d rows after an own edit by an author with mod.edit, want 0", n)
		}
		// Own post, locked topic: only mod.edit allows it.
		if _, err := pool.Exec(ctx, `update topics set locked = true where id = 1`); err != nil {
			t.Fatal(err)
		}
		if err := board.EditPost(ctx, pool, a, 1, board.Draft{Title: "t", Source: "own 2", IP: ip1}, "through the lock", t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		l := readLog(t, pool)
		if len(l) != 1 || l[0].Action != "post.edit" || l[0].Target != 1 || l[0].Actor != alice {
			t.Fatalf("log = %+v, want one post.edit of post 1", l)
		}
		if got := readPost(t, pool, 1).EditReason; got != "" {
			t.Errorf("edit_reason = %q: the reason belongs to another member's post only, and the last edit gave none", got)
		}
		// Another member's post: always a row.
		if err := board.EditPost(ctx, pool, a, 2, board.Draft{Source: "bob's", IP: ip1}, "r", t0.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if n := len(readLog(t, pool)); n != 2 {
			t.Errorf("%d rows, want 2", n)
		}
	})

	t.Run("delete", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		grantOpts(t, pool, alice, 2, "post.delete_own", "mod.delete")
		a := viewerFor(t, pool, alice)
		rid, err := board.Reply(ctx, pool, a, 1, board.Draft{Source: "my reply", IP: ip1}, t0)
		if err != nil {
			t.Fatal(err)
		}
		// Own reply: post.delete_own allows it alone.
		if err := board.DeletePost(ctx, pool, a, rid, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if n := len(readLog(t, pool)); n != 0 {
			t.Fatalf("%d rows after an own delete by an author with mod.delete, want 0", n)
		}
		// Own first post with replies: only mod.delete allows it.
		if err := board.DeletePost(ctx, pool, a, 1, t0.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
		l := readLog(t, pool)
		if len(l) != 1 || l[0].Action != "post.delete" || l[0].Target != 1 {
			t.Fatalf("log = %+v, want one post.delete of post 1", l)
		}
	})

	t.Run("delete without post.delete_own", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		grantOpts(t, pool, alice, 2, "mod.delete")
		a := viewerFor(t, pool, alice)
		rid, err := board.Reply(ctx, pool, a, 1, board.Draft{Source: "my reply", IP: ip1}, t0)
		if err != nil {
			t.Fatal(err)
		}
		if err := board.DeletePost(ctx, pool, a, rid, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if l := readLog(t, pool); len(l) != 1 || l[0].Action != "post.delete" {
			t.Errorf("log = %+v, want one post.delete: only mod.delete allowed it", l)
		}
	})
}

// TestLockOwnUnlock: an author with only topic.lock_own may unlock the topic
// only when the latest topic.lock row of the topic names the author as actor.
func TestLockOwnUnlock(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	// Topic 1 is alice's.
	grantOpts(t, pool, alice, 2, "topic.lock_own")
	grantOpts(t, pool, carol, 2, "mod.lock")
	a := viewerFor(t, pool, alice)
	m := viewerFor(t, pool, carol)
	at := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Minute) }

	// The author locks and unlocks.
	if err := board.SetTopicLock(ctx, pool, a, 1, true, at(1)); err != nil {
		t.Fatal(err)
	}
	if err := board.SetTopicLock(ctx, pool, a, 1, false, at(2)); err != nil {
		t.Errorf("the author unlocks the topic that the author locked: %v", err)
	}
	if readTopic(t, pool, 1).Locked {
		t.Error("topic 1 is still locked")
	}

	// A moderator locks: the author may not unlock.
	if err := board.SetTopicLock(ctx, pool, m, 1, true, at(3)); err != nil {
		t.Fatal(err)
	}
	wantIs(t, board.SetTopicLock(ctx, pool, a, 1, false, at(4)), board.ErrForbidden, "unlock of a moderator's lock")
	if !readTopic(t, pool, 1).Locked {
		t.Error("the author unlocked a moderator's lock")
	}
	// The moderator unlocks, the author locks again, and unlocks again.
	if err := board.SetTopicLock(ctx, pool, m, 1, false, at(5)); err != nil {
		t.Fatal(err)
	}
	if err := board.SetTopicLock(ctx, pool, a, 1, true, at(6)); err != nil {
		t.Fatal(err)
	}
	// The moderator unlocks and locks again: the latest lock row is the moderator's.
	if err := board.SetTopicLock(ctx, pool, m, 1, false, at(7)); err != nil {
		t.Fatal(err)
	}
	if err := board.SetTopicLock(ctx, pool, m, 1, true, at(8)); err != nil {
		t.Fatal(err)
	}
	wantIs(t, board.SetTopicLock(ctx, pool, a, 1, false, at(9)), board.ErrForbidden, "unlock after the moderator locked again")
	if err := board.SetTopicLock(ctx, pool, m, 1, false, at(10)); err != nil {
		t.Errorf("a moderator unlocks: %v", err)
	}

	// A topic that was imported locked, with no log row.
	if _, err := pool.Exec(ctx, `delete from mod_log; update topics set locked = true where id = 1`); err != nil {
		t.Fatal(err)
	}
	wantIs(t, board.SetTopicLock(ctx, pool, a, 1, false, at(11)), board.ErrForbidden, "unlock of an imported lock")
	if !readTopic(t, pool, 1).Locked {
		t.Error("the author unlocked an imported lock")
	}
	// Another author's lock_own does not help on this topic either.
	grantOpts(t, pool, bob, 2, "topic.lock_own")
	wantIs(t, board.SetTopicLock(ctx, pool, viewerFor(t, pool, bob), 1, false, at(12)), board.ErrForbidden, "other member")
}
