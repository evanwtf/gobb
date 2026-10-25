package board

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/markup"
)

// The rights of the posting functions are written once, here, and used by both
// the write and the check that a form page runs. A page that shows a form calls
// the check first, so a viewer who cannot post never sees a form, and the write
// still checks again.

// mayStart reports whether v may start a topic in a forum of the given kind and
// lock state: topic.start, a forum that holds topics, and an open forum or
// mod.lock.
func mayStart(v Viewer, forumID int64, kind string, locked bool) bool {
	if kind != "forum" || !v.Perms.Has("topic.start", forumID) {
		return false
	}
	return !locked || v.Perms.Has("mod.lock", forumID)
}

// mayReplyTo reports whether v holds topic.reply in the forum of a topic that
// has the given visibility. A moved-topic marker is no reply target at all: the
// callers answer [ErrNotFound] for it before they ask. The locks come next, in
// [lockStops].
func mayReplyTo(v Viewer, forumID int64, visibility string) bool {
	return v.Perms.Has("topic.reply", forumID) && visibility == "approved"
}

// lockStops reports whether a lock on the topic or its forum stops v: v then
// needs mod.lock in the forum.
func lockStops(v Viewer, forumID int64, topicLocked, forumLocked bool) bool {
	return lockStopError(v, forumID, topicLocked, forumLocked) != nil
}

// lockStopError is [lockStops] with the reason: [ErrTopicLocked] or
// [ErrForumLocked], both an [ErrForbidden], or nil when no lock stops v. A lock
// on the topic is named first.
func lockStopError(v Viewer, forumID int64, topicLocked, forumLocked bool) error {
	if v.Perms.Has("mod.lock", forumID) {
		return nil
	}
	switch {
	case topicLocked:
		return ErrTopicLocked
	case forumLocked:
		return ErrForumLocked
	}
	return nil
}

// mayEdit decides whether v may edit a post of authorID in a topic. ok is the
// answer; own is true when the author's right alone allows it (post.edit_own in
// an unlocked topic) and false when mod.edit does. A guest never edits.
func mayEdit(v Viewer, authorID, forumID int64, topicLocked bool) (own, ok bool) {
	if v.UserID == 0 {
		return false, false
	}
	own = authorID == v.UserID && v.Perms.Has("post.edit_own", forumID) && !topicLocked
	return own, own || v.Perms.Has("mod.edit", forumID)
}

// editRights is [mayEdit] for a post and topic that were read: it gives
// [ErrForbidden] for no.
func editRights(v Viewer, p postRef, t topicRef) (own bool, err error) {
	own, ok := mayEdit(v, p.authorID, p.forumID, t.locked)
	if !ok {
		return false, ErrForbidden
	}
	return own, nil
}

// mayDelete decides whether v may delete a post of authorID. ok is the answer;
// own is true when the author's right alone allows it. The first post of a
// topic that holds other approved posts is not the author's to delete: isFirst
// and others are asked only when the author's right is the one that decides. A
// guest never deletes.
func mayDelete(v Viewer, authorID, forumID int64, isFirst func() (bool, error), others func() (int, error)) (own, ok bool, err error) {
	if v.UserID == 0 {
		return false, false, nil
	}
	own = authorID == v.UserID && v.Perms.Has("post.delete_own", forumID)
	if own {
		first, err := isFirst()
		if err != nil {
			return false, false, err
		}
		if first {
			n, err := others()
			if err != nil {
				return false, false, err
			}
			own = n == 0
		}
	}
	return own, own || v.Perms.Has("mod.delete", forumID), nil
}

// approvedOthers counts the approved posts of a topic other than postID.
func approvedOthers(ctx context.Context, q db.Querier, topicID, postID int64) (int, error) {
	var n int
	err := q.QueryRow(ctx, `select count(*) from posts where topic_id = $1 and id <> $2 and visibility = 'approved'`,
		topicID, postID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("board: count posts of topic %d: %w", topicID, err)
	}
	return n, nil
}

// deleteRights is [mayDelete] for a post that was read: it gives [ErrForbidden]
// for no.
func deleteRights(ctx context.Context, q db.Querier, v Viewer, p postRef) (own bool, err error) {
	own, ok, err := mayDelete(v, p.authorID, p.forumID,
		func() (bool, error) { return p.first, nil },
		func() (int, error) { return approvedOthers(ctx, q, p.topicID, p.id) })
	if err != nil {
		return false, err
	}
	if !ok {
		return false, ErrForbidden
	}
	return own, nil
}

// MayDownload reports whether v may load the attachments of the posts of a
// forum: account.download, and attach.download in the forum.
func MayDownload(v Viewer, forumID int64) bool {
	return v.Perms.Has("account.download", 0) && v.Perms.Has("attach.download", forumID)
}

// PostButtons are the buttons that a post shows to a viewer. They are the rules
// of [CanReply], [LoadEdit] and [LoadDelete] applied to the post, with no row
// lock; a button is a hint, and the write checks again.
type PostButtons struct {
	Quote, Edit, Delete bool
	// Restore shows on a deleted post (mod.delete). Approve shows on a held post
	// (mod.approve), and on the first post of a held topic.
	Restore, Approve bool
}

// pageButtons decides the buttons of each post of the page. canReply is the
// answer of the reply rule for the topic.
func pageButtons(ctx context.Context, q db.Querier, v Viewer, out TopicPage, canReply bool) ([]PostButtons, error) {
	buttons := make([]PostButtons, len(out.Posts))
	forumID := out.Forum.ID
	firstID, loaded := int64(0), false
	isFirst := func(postID int64) (bool, error) {
		if !loaded {
			loaded = true
			err := q.QueryRow(ctx, `select id from posts where topic_id = $1 order by created_at, id limit 1`, out.Topic.ID).Scan(&firstID)
			if err != nil {
				return false, fmt.Errorf("board: read first post of topic %d: %w", out.Topic.ID, err)
			}
		}
		return firstID == postID, nil
	}
	for i, p := range out.Posts {
		b := &buttons[i]
		b.Quote = canReply && p.Visibility == "approved" && out.Topic.Visibility == "approved"
		if v.UserID == 0 {
			continue
		}
		if p.Visibility == "deleted" {
			// The rule of RestorePost. A deleted post offers nothing else.
			b.Restore = v.Perms.Has("mod.delete", forumID)
			continue
		}
		_, b.Edit = mayEdit(v, p.Author.ID, forumID, out.Topic.Locked)
		_, ok, err := mayDelete(v, p.Author.ID, forumID,
			func() (bool, error) { return isFirst(p.ID) },
			func() (int, error) { return approvedOthers(ctx, q, out.Topic.ID, p.ID) })
		if err != nil {
			return nil, err
		}
		b.Delete = ok
		// The rule of ApprovePost: a held post, and the first post of a held topic.
		if v.Perms.Has("mod.approve", forumID) {
			switch {
			case p.Visibility == "unapproved":
				b.Approve = true
			case out.Topic.Visibility == "unapproved":
				if b.Approve, err = isFirst(p.ID); err != nil {
					return nil, err
				}
			}
		}
	}
	return buttons, nil
}

// lockRights decides whether v may set the lock of topic t to locked. It is the
// rule of [SetTopicLock], and a topic page asks it to decide whether to offer
// Lock or Unlock. It gives [ErrForbidden] for no. It reads the log only for an
// author who holds topic.lock_own and not mod.lock, and who unlocks.
//
// An author who holds topic.lock_own and not mod.lock may unlock only when the
// latest topic.lock row of the topic names the author as actor. A topic that a
// moderator locked, or that was imported locked and has no row, stays locked for
// the author.
func lockRights(ctx context.Context, q db.Querier, v Viewer, t topicRef, locked bool) error {
	if v.UserID == 0 {
		return ErrForbidden
	}
	own := t.authorID == v.UserID && v.Perms.Has("topic.lock_own", t.forumID)
	mod := v.Perms.Has("mod.lock", t.forumID)
	if !own && !mod {
		return ErrForbidden
	}
	if t.moved {
		return ErrForbidden
	}
	if !t.locked || locked || mod {
		return nil
	}
	actor, err := latestLockActor(ctx, q, t.id)
	if err != nil {
		return err
	}
	if actor != v.UserID {
		return ErrForbidden
	}
	return nil
}

// latestLockActor returns the actor of the latest topic.lock row of the topic: 0
// when that row has no actor (a command-line action) or when there is no row (a
// topic that was imported locked).
func latestLockActor(ctx context.Context, q db.Querier, topicID int64) (int64, error) {
	var actor *int64
	err := q.QueryRow(ctx, `
select actor_id from mod_log where target_type = 'topic' and target_id = $1 and action = 'topic.lock'
 order by id desc limit 1`, topicID).Scan(&actor)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && actor == nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("board: read the lock of topic %d: %w", topicID, err)
	}
	return *actor, nil
}

// lockOffers sets CanLock and CanUnlock of a topic page by [lockRights].
func lockOffers(ctx context.Context, q db.Querier, v Viewer, out *TopicPage) error {
	t := topicRef{id: out.Topic.ID, forumID: out.Forum.ID, authorID: out.Topic.Author.ID, locked: out.Topic.Locked}
	err := lockRights(ctx, q, v, t, !t.locked)
	switch {
	case err == nil:
		out.CanLock, out.CanUnlock = !t.locked, t.locked
		// A moderator may lock over the lock of the topic's author: the author can
		// undo that lock, and the moderator's lock cannot be undone by the author.
		// [SetTopicLock] writes the log row that makes it so.
		if t.locked && v.Perms.Has("mod.lock", t.forumID) && t.authorID != 0 && t.authorID != v.UserID {
			actor, err := latestLockActor(ctx, q, t.id)
			if err != nil {
				return err
			}
			out.CanLock = actor == t.authorID
		}
	case errors.Is(err, ErrForbidden):
	default:
		return err
	}
	return nil
}

// CanReply reports whether v may reply to topic topicID, with the rights of
// [Reply] and none of its effects. A topic that v cannot see, a moved-topic
// marker, or a missing topic gives [ErrNotFound]; a viewer who may see the topic
// and not reply gives [ErrForbidden]. A page calls it before it shows the reply
// form.
func CanReply(ctx context.Context, q db.Querier, v Viewer, topicID int64) error {
	sc, err := newScope(ctx, q, v)
	if err != nil {
		return err
	}
	t, err := readTopic(ctx, q, sc, topicID, false)
	if err != nil {
		return err
	}
	if t.moved {
		return ErrNotFound
	}
	if !mayReplyTo(v, t.forumID, t.visibility) {
		return ErrForbidden
	}
	_, forumLocked, err := forumRow(ctx, q, t.forumID, false)
	if err != nil {
		return err
	}
	return lockStopError(v, t.forumID, t.locked, forumLocked)
}

// CanStartTopic reports whether v may start a topic in forum forumID, with the
// rights of [NewTopic] and none of its effects. A forum that v cannot read, or
// that is missing, gives [ErrNotFound]; a viewer who may read it and not start a
// topic there gives [ErrForbidden].
func CanStartTopic(ctx context.Context, q db.Querier, v Viewer, forumID int64) error {
	kind, locked, err := readForum(ctx, q, v, forumID, false)
	if err != nil {
		return err
	}
	if !mayStart(v, forumID, kind, locked) {
		return ErrForbidden
	}
	return nil
}

// DraftChecks says which checks of a save [CheckDraft] runs.
type DraftChecks struct {
	// Author checks the writer of a new post: an active member, or a guest with a
	// valid name and an address that no ban holds.
	Author bool
	// Title checks the title (a new topic, and the edit of a first post).
	Title bool
	// Reason checks the edit reason (the edit of another member's post).
	Reason bool
}

// CheckDraft runs the checks of a save that do not need the body, in the order of
// the save, so that a preview refuses what the save would refuse. The errors are
// those of the save: an [*InputError], or [ErrForbidden] for a banned address. It
// writes nothing.
func CheckDraft(ctx context.Context, q db.Querier, v Viewer, d Draft, reason string, c DraftChecks, now time.Time) error {
	if c.Reason {
		if _, err := cleanReason(reason); err != nil {
			return err
		}
	}
	if c.Author {
		if _, err := resolveAuthor(ctx, q, v, d, now); err != nil {
			return err
		}
	}
	if c.Title {
		if _, err := cleanTitle(d.Title); err != nil {
			return err
		}
	}
	return nil
}

// EditForm is what the edit form needs of a post: where it stands, the text the
// author wrote, and the state that a preview must parse with, so that a preview
// agrees with the save.
type EditForm struct {
	PostID, TopicID, ForumID int64
	// First is true for the first post of its topic. Its form holds the title.
	First bool
	// Title is the title of the topic.
	Title string
	// Source is the Markdown of the post as stored.
	Source string
	// Flags are the stored flags of the post. Stored is its stored tree. Both
	// go to [ParseBodyKeeping], as [EditPost] passes them.
	Flags  markup.Options
	Stored *markup.Node
	// Moderated is true when mod.edit alone lets v edit the post of another
	// member: the form then asks for a reason. A moderator who edits the own post
	// by mod.edit alone is not asked, since the reason would not show.
	Moderated bool
}

// LoadEdit returns the data of the edit form of post postID, after the checks of
// [EditPost]: a post that v cannot see is [ErrNotFound]; a guest, or a member
// who may not edit the post, gets [ErrForbidden]. It writes nothing and takes no
// lock.
func LoadEdit(ctx context.Context, q db.Querier, v Viewer, postID int64) (EditForm, error) {
	p, t, err := readPost(ctx, q, v, postID, false)
	if err != nil {
		return EditForm{}, err
	}
	own, err := editRights(v, p, t)
	if err != nil {
		return EditForm{}, err
	}
	out := EditForm{PostID: postID, TopicID: t.id, ForumID: p.forumID, First: p.first, Flags: p.flags, Moderated: !own && p.authorID != v.UserID}
	var tree []byte
	err = q.QueryRow(ctx, `
select t.title, p.body_source, p.body_tree from posts p join topics t on t.id = p.topic_id where p.id = $1`, postID).
		Scan(&out.Title, &out.Source, &tree)
	if err != nil {
		return EditForm{}, fmt.Errorf("board: read post %d: %w", postID, err)
	}
	if out.Stored, err = markup.Decode(tree); err != nil {
		return EditForm{}, fmt.Errorf("board: read post %d: %w", postID, err)
	}
	return out, nil
}

// DeleteForm says where a post that v may delete stands.
type DeleteForm struct {
	PostID, TopicID, ForumID int64
	// First is true for the first post of its topic. Deleting it deletes the
	// topic.
	First bool
}

// LoadDelete returns where post postID stands, after the checks of [DeletePost]:
// a post that v cannot see is [ErrNotFound]; a viewer who may not delete it gets
// [ErrForbidden]. It writes nothing and takes no lock.
func LoadDelete(ctx context.Context, q db.Querier, v Viewer, postID int64) (DeleteForm, error) {
	p, t, err := readPost(ctx, q, v, postID, false)
	if err != nil {
		return DeleteForm{}, err
	}
	if _, err := deleteRights(ctx, q, v, p); err != nil {
		return DeleteForm{}, err
	}
	return DeleteForm{PostID: postID, TopicID: t.id, ForumID: p.forumID, First: p.first}, nil
}

// DeletedOwnPost says where post postID stood when v wrote it and it is deleted.
// A second click on a confirmed delete meets such a post: [LoadDelete] cannot see
// it any more. The author already knows that the post exists (spec 14), so the
// handler answers as for a success. The second result is false for every other
// case: a guest, a post of another author, a post that is not deleted, and a post
// that does not exist. The caller then keeps the answer that it had. It writes
// nothing and takes no lock.
func DeletedOwnPost(ctx context.Context, q db.Querier, v Viewer, postID int64) (DeleteForm, bool, error) {
	if v.UserID == 0 {
		return DeleteForm{}, false, nil
	}
	d := DeleteForm{PostID: postID}
	err := q.QueryRow(ctx, `
select topic_id, forum_id from posts
 where id = $1 and author_id = $2 and visibility = 'deleted'`, postID, v.UserID).Scan(&d.TopicID, &d.ForumID)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeleteForm{}, false, nil
	}
	if err != nil {
		return DeleteForm{}, false, fmt.Errorf("board: read deleted post %d: %w", postID, err)
	}
	return d, true, nil
}

// Card returns the author card of member userID, as a post shows it. A user who
// does not exist gives [ErrNotFound]. A preview uses it for the writer.
func Card(ctx context.Context, q db.Querier, userID int64) (UserCard, error) {
	var c UserCard
	err := scanCard(q.QueryRow(ctx, `select `+cardColumns+` from users u`+cardJoins+` where u.id = $1`, userID), &c)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserCard{}, ErrNotFound
	}
	if err != nil {
		return UserCard{}, fmt.Errorf("board: read card of %d: %w", userID, err)
	}
	return c, nil
}

// ForumHead returns forum forumID and the ancestors that v may list, for the
// heading of a form. A forum that v cannot read, or that is missing, gives
// [ErrNotFound]. It reads no topics.
func ForumHead(ctx context.Context, q db.Querier, v Viewer, forumID int64) (*Forum, []*Forum, error) {
	if !v.Perms.Has("forum.read", forumID) {
		return nil, nil, ErrNotFound
	}
	f, _, crumbs, err := locateForum(ctx, q, v, forumID, false)
	if err != nil {
		return nil, nil, err
	}
	return f, crumbs, nil
}

// TopicHead returns the title of topic topicID, its forum and the ancestors of
// the forum that v may list, for the heading of a form. A topic that v cannot
// see, a moved-topic marker, or a missing topic gives [ErrNotFound]: a marker
// carries the title of a topic that v may not see. It reads no posts.
func TopicHead(ctx context.Context, q db.Querier, v Viewer, topicID int64) (string, *Forum, []*Forum, error) {
	sc, err := newScope(ctx, q, v)
	if err != nil {
		return "", nil, nil, err
	}
	t, err := readTopic(ctx, q, sc, topicID, false)
	if err != nil {
		return "", nil, nil, err
	}
	if t.moved {
		return "", nil, nil, ErrNotFound
	}
	var title string
	if err := q.QueryRow(ctx, `select title from topics where id = $1`, topicID).Scan(&title); err != nil {
		return "", nil, nil, fmt.Errorf("board: read topic %d: %w", topicID, err)
	}
	f, crumbs, err := ForumHead(ctx, q, v, t.forumID)
	if err != nil {
		return "", nil, nil, err
	}
	return title, f, crumbs, nil
}
