package board

import (
	"context"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/evanwtf/gobb/internal/db"
)

// The approval queue: the held posts in the forums where the viewer may
// approve (design section 11). Moderators use it as well as admins.

// queuePerPage is the count of posts on one page of the queue, and
// queueExcerpt the length in characters of the text that each one shows.
const (
	queuePerPage = 50
	queueExcerpt = 300
)

// HeldPost is one post of the approval queue.
type HeldPost struct {
	ID         int64
	TopicID    int64
	TopicTitle string
	ForumID    int64
	ForumName  string
	Author     Author
	At         time.Time
	Subject    string
	// Excerpt is the start of the plain text of the post, at most 300
	// characters. It is text, not HTML.
	Excerpt string
	// First is true when the post is the first of its topic: approving it
	// approves the topic too.
	First bool
	// CanDelete is true when the viewer holds mod.delete in the forum.
	CanDelete bool
}

// QueuePage is one page of the approval queue, oldest first.
type QueuePage struct {
	Posts       []HeldPost
	Total       int
	Page, Pages int
}

// MayApproveSomewhere reports whether v may approve posts in any forum that v
// can read. Only such a viewer has an approval queue.
func MayApproveSomewhere(ctx context.Context, q db.Querier, v Viewer) (bool, error) {
	if v.UserID == 0 {
		return false, nil
	}
	sc, err := newScope(ctx, q, v)
	if err != nil {
		return false, err
	}
	return len(sc.approve) > 0, nil
}

// HeldPosts returns page page of the approval queue of v: the held posts in
// the forums where v holds mod.approve and forum.read, oldest first, 50 a
// page. Each post says whether v may delete it. A post in a deleted topic is not in the queue. A viewer with no such
// forum gets [ErrForbidden]. A page past the last is [ErrNotFound]; page 1 of
// an empty queue is not.
func HeldPosts(ctx context.Context, q db.Querier, v Viewer, page int) (QueuePage, error) {
	if v.UserID == 0 {
		return QueuePage{}, ErrForbidden
	}
	sc, err := newScope(ctx, q, v)
	if err != nil {
		return QueuePage{}, err
	}
	if len(sc.approve) == 0 {
		return QueuePage{}, ErrForbidden
	}
	const where = `p.visibility = 'unapproved' and t.visibility <> 'deleted' and p.forum_id = any($1::bigint[])`
	var out QueuePage
	if err := q.QueryRow(ctx, `select count(*) from posts p join topics t on t.id = p.topic_id where `+where,
		sc.approve).Scan(&out.Total); err != nil {
		return QueuePage{}, fmt.Errorf("board: count the queue: %w", err)
	}
	out.Page, out.Pages = page, pageCount(out.Total, queuePerPage)
	if page < 1 || page > out.Pages {
		return QueuePage{}, ErrNotFound
	}
	rows, err := q.Query(ctx, `
select p.id, p.topic_id, t.title, p.forum_id, f.name, coalesce(p.author_id, 0), p.author_name, p.created_at,
       p.subject, left(p.body_text, $2), p.id = (select fp.id from posts fp where fp.topic_id = p.topic_id order by fp.created_at, fp.id limit 1)
  from posts p
  join topics t on t.id = p.topic_id
  join forums f on f.id = p.forum_id
 where `+where+`
 order by p.created_at, p.id
 limit $3 offset $4`, sc.approve, queueExcerpt+1, queuePerPage, (page-1)*queuePerPage)
	if err != nil {
		return QueuePage{}, fmt.Errorf("board: read the queue: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var h HeldPost
		if err := rows.Scan(&h.ID, &h.TopicID, &h.TopicTitle, &h.ForumID, &h.ForumName, &h.Author.ID, &h.Author.Name,
			&h.At, &h.Subject, &h.Excerpt, &h.First); err != nil {
			return QueuePage{}, fmt.Errorf("board: read the queue: %w", err)
		}
		h.CanDelete = slices.Contains(sc.del, h.ForumID)
		if utf8.RuneCountInString(h.Excerpt) > queueExcerpt {
			h.Excerpt = string([]rune(h.Excerpt)[:queueExcerpt]) + "…"
		}
		out.Posts = append(out.Posts, h)
	}
	if err := rows.Err(); err != nil {
		return QueuePage{}, fmt.Errorf("board: read the queue: %w", err)
	}
	return out, nil
}
