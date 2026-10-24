package board

import (
	"context"
	"errors"
	"fmt"

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

// mayReplyTo reports whether v holds topic.reply in the forum of t, and t is an
// approved topic that is no moved-topic marker. The locks come next, in
// [lockStops].
func mayReplyTo(v Viewer, t topicRef) bool {
	return v.Perms.Has("topic.reply", t.forumID) && t.visibility == "approved" && !t.moved
}

// lockStops reports whether a lock on the topic or its forum stops v: v then
// needs mod.lock in the forum.
func lockStops(v Viewer, forumID int64, topicLocked, forumLocked bool) bool {
	return (topicLocked || forumLocked) && !v.Perms.Has("mod.lock", forumID)
}

// editRights decides whether v may edit post p of topic t. It returns own true
// when the author's right alone allows it (post.edit_own in an unlocked topic),
// and false when mod.edit does. A guest, and anyone else, gets [ErrForbidden].
func editRights(v Viewer, p postRef, t topicRef) (own bool, err error) {
	if v.UserID == 0 {
		return false, ErrForbidden
	}
	own = p.authorID == v.UserID && v.Perms.Has("post.edit_own", p.forumID) && !t.locked
	if !own && !v.Perms.Has("mod.edit", p.forumID) {
		return false, ErrForbidden
	}
	return own, nil
}

// deleteRights decides whether v may delete post p. It returns own true when
// the author's right alone allows it. The first post of a topic that holds other
// approved posts is not the author's to delete. A guest, and anyone else, gets
// [ErrForbidden].
func deleteRights(ctx context.Context, q db.Querier, v Viewer, p postRef) (own bool, err error) {
	if v.UserID == 0 {
		return false, ErrForbidden
	}
	own = p.authorID == v.UserID && v.Perms.Has("post.delete_own", p.forumID)
	if own && p.first {
		var others int
		err := q.QueryRow(ctx, `select count(*) from posts where topic_id = $1 and id <> $2 and visibility = 'approved'`,
			p.topicID, p.id).Scan(&others)
		if err != nil {
			return false, fmt.Errorf("board: count posts of topic %d: %w", p.topicID, err)
		}
		own = others == 0
	}
	if !own && !v.Perms.Has("mod.delete", p.forumID) {
		return false, ErrForbidden
	}
	return own, nil
}

// CanReply reports whether v may reply to topic topicID, with the rights of
// [Reply] and none of its effects. A topic that v cannot see, or that is missing,
// gives [ErrNotFound]; a viewer who may see it and not reply gives
// [ErrForbidden]. A page calls it before it shows the reply form.
func CanReply(ctx context.Context, q db.Querier, v Viewer, topicID int64) error {
	sc, err := newScope(ctx, q, v)
	if err != nil {
		return err
	}
	t, err := readTopic(ctx, q, sc, topicID, false)
	if err != nil {
		return err
	}
	if !mayReplyTo(v, t) {
		return ErrForbidden
	}
	var forumLocked bool
	if err := q.QueryRow(ctx, `select locked from forums where id = $1`, t.forumID).Scan(&forumLocked); err != nil {
		return fmt.Errorf("board: read forum %d: %w", t.forumID, err)
	}
	if lockStops(v, t.forumID, t.locked, forumLocked) {
		return ErrForbidden
	}
	return nil
}

// CanStartTopic reports whether v may start a topic in forum forumID, with the
// rights of [NewTopic] and none of its effects. A forum that v cannot read, or
// that is missing, gives [ErrNotFound]; a viewer who may read it and not start a
// topic there gives [ErrForbidden].
func CanStartTopic(ctx context.Context, q db.Querier, v Viewer, forumID int64) error {
	var (
		kind   string
		locked bool
	)
	err := q.QueryRow(ctx, `select kind, locked from forums where id = $1`, forumID).Scan(&kind, &locked)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !v.Perms.Has("forum.read", forumID)) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("board: read forum %d: %w", forumID, err)
	}
	if !mayStart(v, forumID, kind, locked) {
		return ErrForbidden
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
	// Moderated is true when mod.edit alone lets v edit the post: the form then
	// asks for a reason.
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
	out := EditForm{PostID: postID, TopicID: t.id, ForumID: p.forumID, First: p.first, Flags: p.flags, Moderated: !own}
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
// see, or that is missing, gives [ErrNotFound]. It reads no posts.
func TopicHead(ctx context.Context, q db.Querier, v Viewer, topicID int64) (string, *Forum, []*Forum, error) {
	sc, err := newScope(ctx, q, v)
	if err != nil {
		return "", nil, nil, err
	}
	t, err := readTopic(ctx, q, sc, topicID, false)
	if err != nil {
		return "", nil, nil, err
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
