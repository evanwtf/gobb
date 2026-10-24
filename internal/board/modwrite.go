package board

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/counters"
)

// visibilityChange names the three writes that move a post between the states
// approved, unapproved and deleted.
type visibilityChange int

const (
	changeDelete visibilityChange = iota
	changeRestore
	changeApprove
)

// DeletePost deletes post postID. The row stays, with visibility deleted and the
// state it had kept in visibility_before_delete, so a moderator can restore it.
//
//   - A post that v cannot see is [ErrNotFound].
//   - An author with post.delete_own may delete the own post, unless it is the
//     first post of a topic that holds other approved posts. A holder of
//     mod.delete in the forum may delete any post. A guest, and anyone else, gets
//     [ErrForbidden].
//   - Deleting the first post of a topic deletes the topic: its visibility
//     becomes deleted, and its posts keep their own.
//   - The counters of the topic and the forum are set again. The post count of an
//     author who holds post.counts there drops by one for an approved post.
//   - A delete by mod.delete writes a mod_log row. An author's own delete writes
//     none. A post that is already deleted is left alone.
//
// now is the time of the delete.
func DeletePost(ctx context.Context, pool *pgxpool.Pool, v Viewer, postID int64, now time.Time) error {
	return changePostVisibility(ctx, pool, v, postID, now, changeDelete)
}

// RestorePost brings back a deleted post in the state it had before the delete,
// approved or held. It needs mod.delete in the forum. Restoring the first post of
// a deleted topic restores the topic to the state it had before its delete. A row
// with no kept state (deleted before gobb kept it, or imported as deleted) comes
// back approved when the restorer holds mod.approve in the forum, and held
// otherwise. A post that is not deleted is left alone.
func RestorePost(ctx context.Context, pool *pgxpool.Pool, v Viewer, postID int64, now time.Time) error {
	return changePostVisibility(ctx, pool, v, postID, now, changeRestore)
}

// ApprovePost approves a held post. It needs mod.approve in the forum. Approving
// the first post of a held topic approves the topic, also when the post is
// approved already. A post and a topic that are approved are left alone. A deleted
// post gives [ErrForbidden]: restore it first.
func ApprovePost(ctx context.Context, pool *pgxpool.Pool, v Viewer, postID int64, now time.Time) error {
	return changePostVisibility(ctx, pool, v, postID, now, changeApprove)
}

func changePostVisibility(ctx context.Context, pool *pgxpool.Pool, v Viewer, postID int64, now time.Time, change visibilityChange) error {
	return inTx(ctx, pool, func(tx pgx.Tx) error {
		p, t, err := lockPost(ctx, tx, v, postID)
		if err != nil {
			return err
		}
		if v.UserID == 0 {
			return ErrForbidden
		}
		var (
			action    string
			postTo    = p.visibility // the new state of the post
			topicTo   = t.visibility // the new state of the topic, when the post is its first
			own       bool           // the own right alone allows the action: no log row
			changePst bool
			changeTop bool
		)
		switch change {
		case changeDelete:
			if own, err = deleteRights(ctx, tx, v, p); err != nil {
				return err
			}
			if p.visibility == "deleted" {
				return nil
			}
			action, postTo, changePst = "post.delete", "deleted", true
			if p.first {
				topicTo, changeTop = "deleted", t.visibility != "deleted"
			}
		case changeRestore:
			if !v.Perms.Has("mod.delete", p.forumID) {
				return ErrForbidden
			}
			if p.visibility != "deleted" {
				return nil
			}
			action, changePst = "post.restore", true
			postTo = restoredState(v, p.forumID, p.before)
			if p.first && t.visibility == "deleted" {
				topicTo, changeTop = restoredState(v, p.forumID, t.before), true
			}
		case changeApprove:
			if !v.Perms.Has("mod.approve", p.forumID) {
				return ErrForbidden
			}
			if p.visibility == "deleted" {
				return ErrForbidden
			}
			action = "post.approve"
			if p.visibility == "unapproved" {
				postTo, changePst = "approved", true
			}
			// A held topic follows its first post, also when that post is approved
			// already (imported data can hold such a topic).
			if p.first && t.visibility == "unapproved" {
				topicTo, changeTop = "approved", true
			}
			if !changePst && !changeTop {
				return nil
			}
		}

		// The rights are checked. The counters of the forum change next, so lock it.
		if _, _, err := lockForum(ctx, tx, p.forumID); err != nil {
			return err
		}
		if changePst {
			// A delete keeps the state of the row; every other change clears it.
			var before any
			if change == changeDelete {
				before = p.visibility
			}
			if _, err := tx.Exec(ctx, `update posts set visibility = $2, visibility_before_delete = $3 where id = $1`,
				p.id, postTo, before); err != nil {
				return fmt.Errorf("board: change post %d: %w", p.id, err)
			}
		}
		if changeTop {
			var before any
			if change == changeDelete {
				before = t.visibility
			}
			if _, err := tx.Exec(ctx, `update topics set visibility = $2, visibility_before_delete = $3 where id = $1`,
				t.id, topicTo, before); err != nil {
				return fmt.Errorf("board: change topic %d: %w", t.id, err)
			}
		}
		if err := counters.Topic(ctx, tx, t.id); err != nil {
			return err
		}
		if err := counters.Forum(ctx, tx, p.forumID); err != nil {
			return err
		}
		// The post count follows the approved state of the post.
		if delta := countOf(postTo) - countOf(p.visibility); delta != 0 {
			counts, err := authorCounts(ctx, tx, v, p.authorID, p.forumID)
			if err != nil {
				return err
			}
			if counts {
				if err := addPostCount(ctx, tx, p.authorID, delta); err != nil {
					return err
				}
			}
		}
		if own {
			return nil
		}
		return logMod(ctx, tx, v, action, "post", p.id, "{}", now)
	})
}

// restoredState is the state that a restore gives to a deleted row. A row that
// kept its state before the delete gets that state back. A row with no kept state
// (deleted before the state was kept, or imported as deleted) comes back approved
// only when the restorer may approve in the forum, and held otherwise: a
// restore must not approve what no moderator with the right approved.
func restoredState(v Viewer, forumID int64, before string) string {
	switch before {
	case "approved", "unapproved":
		return before
	}
	if v.Perms.Has("mod.approve", forumID) {
		return "approved"
	}
	return "unapproved"
}

// countOf is 1 for the visibility that a post count includes.
func countOf(visibility string) int {
	if visibility == "approved" {
		return 1
	}
	return 0
}

// SetTopicLock locks or unlocks topic topicID. It needs mod.lock in the forum, or
// topic.lock_own for the author of the topic. A topic that v cannot see is
// [ErrNotFound]; a guest, and anyone else, gets [ErrForbidden], and so does a
// moved-topic marker. A topic that is already in the state asked for is left
// alone. A change writes one mod_log row, whoever makes it.
//
// An author who holds topic.lock_own and not mod.lock may unlock the topic only
// when the latest topic.lock row of the topic names the author as actor. A topic
// that a moderator locked, or that was imported locked and has no row, stays
// locked for the author: [ErrForbidden].
func SetTopicLock(ctx context.Context, pool *pgxpool.Pool, v Viewer, topicID int64, locked bool, now time.Time) error {
	return inTx(ctx, pool, func(tx pgx.Tx) error {
		sc, err := newScope(ctx, tx, v)
		if err != nil {
			return err
		}
		t, err := lockTopic(ctx, tx, sc, topicID)
		if err != nil {
			return err
		}
		if err := lockRights(ctx, tx, v, t, locked); err != nil {
			return err
		}
		if t.locked == locked {
			return nil
		}
		if _, err := tx.Exec(ctx, `update topics set locked = $2 where id = $1`, t.id, locked); err != nil {
			return fmt.Errorf("board: lock topic %d: %w", t.id, err)
		}
		action := "topic.unlock"
		if locked {
			action = "topic.lock"
		}
		return logMod(ctx, tx, v, action, "topic", t.id, "{}", now)
	})
}
