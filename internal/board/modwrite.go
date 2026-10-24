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

// DeletePost deletes post postID. The row stays, with visibility deleted, so a
// moderator can restore it.
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

// RestorePost brings back a deleted post, with visibility approved. It needs
// mod.delete in the forum. Restoring the first post of a deleted topic restores
// the topic. A post that is not deleted is left alone.
//
// A post that was held when it was deleted comes back approved, and the same
// goes for a held topic: gobb does not store the state before the delete.
func RestorePost(ctx context.Context, pool *pgxpool.Pool, v Viewer, postID int64, now time.Time) error {
	return changePostVisibility(ctx, pool, v, postID, now, changeRestore)
}

// ApprovePost approves a held post. It needs mod.approve in the forum. Approving
// the first post of a held topic approves the topic. A post that is already
// approved is left alone. A deleted post gives [ErrForbidden]: restore it first.
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
			target, action string
			own            bool
		)
		switch change {
		case changeDelete:
			own = p.authorID == v.UserID && v.Perms.Has("post.delete_own", p.forumID)
			if own && p.first {
				// The first post of a topic with replies is not the author's to delete.
				var others int
				err := tx.QueryRow(ctx, `select count(*) from posts where topic_id = $1 and id <> $2 and visibility = 'approved'`,
					p.topicID, p.id).Scan(&others)
				if err != nil {
					return fmt.Errorf("board: count posts of topic %d: %w", p.topicID, err)
				}
				own = others == 0
			}
			if !own && !v.Perms.Has("mod.delete", p.forumID) {
				return ErrForbidden
			}
			if p.visibility == "deleted" {
				return nil
			}
			target, action = "deleted", "post.delete"
		case changeRestore:
			if !v.Perms.Has("mod.delete", p.forumID) {
				return ErrForbidden
			}
			if p.visibility != "deleted" {
				return nil
			}
			target, action = "approved", "post.restore"
		case changeApprove:
			if !v.Perms.Has("mod.approve", p.forumID) {
				return ErrForbidden
			}
			switch p.visibility {
			case "approved":
				return nil
			case "deleted":
				return ErrForbidden
			}
			target, action = "approved", "post.approve"
		}

		// The rights are checked. The counters of the forum change next, so lock it.
		if _, _, err := lockForum(ctx, tx, p.forumID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update posts set visibility = $2 where id = $1`, p.id, target); err != nil {
			return fmt.Errorf("board: change post %d: %w", p.id, err)
		}
		if p.first {
			// The topic follows its first post. A restore or an approval changes only
			// a topic that waits for it.
			var from string
			switch change {
			case changeRestore:
				from = "deleted"
			case changeApprove:
				from = "unapproved"
			}
			if _, err := tx.Exec(ctx, `update topics set visibility = $2 where id = $1 and ($3 = '' or visibility = $3)`,
				t.id, target, from); err != nil {
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
		if delta := countOf(target) - countOf(p.visibility); delta != 0 {
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
		if v.UserID == 0 {
			return ErrForbidden
		}
		own := t.authorID == v.UserID && v.Perms.Has("topic.lock_own", t.forumID)
		if !own && !v.Perms.Has("mod.lock", t.forumID) {
			return ErrForbidden
		}
		if t.moved {
			return ErrForbidden
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
