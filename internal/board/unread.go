package board

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evanwtf/gobb/internal/db"
)

// unreadAfter is the SQL expression for the time that a topic t must pass to be
// unread for the member me: the newest of the member's mark-all time (or the
// registration time when there is none), the mark of the forum fm, and the mark
// of the topic tm. The viewer's rows are joined as me, fm and tm. A guest joins
// no rows, so the expression is null and the comparison is false.
const unreadAfter = `greatest(coalesce(me.last_mark_at, me.registered_at), fm.marked_at, tm.marked_at)`

// unreadVisible is the visibility clause of [unreadForumsSQL].
var unreadVisible = visible("t", 1)

// RecordVisit notes that member userID made a request at now.
//
// last_active_at moves to now when it is empty or more than five minutes old, so
// a member who clicks fast costs one write in five minutes.
// When the old value is more than 30 minutes old, a new visit starts: last_visit_at
// first takes the old last_active_at, which is the end of the last visit.
// A member with no last_active_at keeps the imported last_visit_at.
// Otherwise nothing is written, so most page views cost no write.
func RecordVisit(ctx context.Context, q db.Querier, userID int64, now time.Time) error {
	_, err := q.Exec(ctx, `
update users set
       last_visit_at = case when last_active_at is not null
                             and $2::timestamptz - last_active_at > interval '30 minutes'
                            then last_active_at else last_visit_at end,
       last_active_at = $2::timestamptz
 where id = $1
   and (last_active_at is null or $2::timestamptz - last_active_at > interval '5 minutes')`,
		userID, now)
	if err != nil {
		return fmt.Errorf("board: record visit of user %d: %w", userID, err)
	}
	return nil
}

// StartVisit starts a visit at once, as a sign-in does: last_visit_at takes the
// old last_active_at (it keeps its value when that is empty) and last_active_at
// takes now.
func StartVisit(ctx context.Context, q db.Querier, userID int64, now time.Time) error {
	_, err := q.Exec(ctx, `
update users set last_visit_at = coalesce(last_active_at, last_visit_at), last_active_at = $2
 where id = $1`, userID, now)
	if err != nil {
		return fmt.Errorf("board: start visit of user %d: %w", userID, err)
	}
	return nil
}

// MarkTopic records that the member read topicID up to the time upTo. The
// stored mark is the later of the old one and upTo, so a mark never moves back.
func MarkTopic(ctx context.Context, q db.Querier, userID, topicID int64, upTo time.Time) error {
	_, err := q.Exec(ctx, `
insert into topic_marks (user_id, topic_id, marked_at) values ($1, $2, $3)
on conflict (user_id, topic_id) do update
   set marked_at = greatest(topic_marks.marked_at, excluded.marked_at)`, userID, topicID, upTo)
	if err != nil {
		return fmt.Errorf("board: mark topic %d: %w", topicID, err)
	}
	return nil
}

// MarkForum marks forum forumID, and every descendant of it that v may read,
// read at now. A forum that v may neither list nor read, and a forum that does
// not exist, give [ErrNotFound].
func MarkForum(ctx context.Context, q db.Querier, v Viewer, forumID int64, now time.Time) error {
	rows, err := q.Query(ctx, `select id, parent_id from forums`)
	if err != nil {
		return fmt.Errorf("board: mark forum %d: %w", forumID, err)
	}
	defer rows.Close()
	exists := map[int64]bool{}
	kids := map[int64][]int64{}
	for rows.Next() {
		var id int64
		var parent *int64
		if err := rows.Scan(&id, &parent); err != nil {
			return fmt.Errorf("board: mark forum %d: %w", forumID, err)
		}
		exists[id] = true
		if parent != nil {
			kids[*parent] = append(kids[*parent], id)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("board: mark forum %d: %w", forumID, err)
	}
	rows.Close()
	if !exists[forumID] || !(v.Perms.Has("forum.list", forumID) || v.Perms.Has("forum.read", forumID)) {
		return ErrNotFound
	}

	ids := []int64{}
	seen := map[int64]bool{}
	for stack := []int64{forumID}; len(stack) > 0; {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[id] {
			continue
		}
		seen[id] = true
		if v.Perms.Has("forum.read", id) {
			ids = append(ids, id)
		}
		stack = append(stack, kids[id]...)
	}
	if len(ids) == 0 {
		return nil
	}
	_, err = q.Exec(ctx, `
insert into forum_marks (user_id, forum_id, marked_at)
select $1, f, $3 from unnest($2::bigint[]) f
on conflict (user_id, forum_id) do update
   set marked_at = greatest(forum_marks.marked_at, excluded.marked_at)`, v.UserID, ids, now)
	if err != nil {
		return fmt.Errorf("board: mark forum %d: %w", forumID, err)
	}
	return nil
}

// MarkAll sets the member's mark-all time to now. It never moves the time back.
func MarkAll(ctx context.Context, q db.Querier, userID int64, now time.Time) error {
	_, err := q.Exec(ctx, `
update users set last_mark_at = greatest(last_mark_at, $2) where id = $1`, userID, now)
	if err != nil {
		return fmt.Errorf("board: mark all read for user %d: %w", userID, err)
	}
	return nil
}

// CountView adds one to the view count of topic topicID. The caller has already
// checked that the viewer may see the topic.
func CountView(ctx context.Context, q db.Querier, topicID int64) error {
	_, err := q.Exec(ctx, `update topics set view_count = view_count + 1 where id = $1`, topicID)
	if err != nil {
		return fmt.Errorf("board: count view of topic %d: %w", topicID, err)
	}
	return nil
}

// unreadForumsSQL finds the forums that hold an unread topic for the member $4
// and that the scope $1 to $3 lets the member read.
var unreadForumsSQL = `
select distinct t.forum_id
  from topics t
  join users me on me.id = $4::bigint
  left join forum_marks fm on fm.user_id = me.id and fm.forum_id = t.forum_id
  left join topic_marks tm on tm.user_id = me.id and tm.topic_id = t.id
 where t.last_post_at > (select coalesce(last_mark_at, registered_at) from users where id = $4::bigint)
   and t.last_post_at > ` + unreadAfter + `
   and t.moved_to_topic_id is null and ` + unreadVisible

// forumsWithUnread returns the IDs of the forums that hold a topic which is
// unread for member v and which v may read. One query serves the whole index.
// The first time test lets the index topics_last_post_idx bound the scan to the
// topics newer than the mark-all time; the second applies the marks of the
// forum and of the topic, which are looked up by primary key.
func forumsWithUnread(ctx context.Context, q db.Querier, v Viewer) (map[int64]bool, error) {
	sc, err := newScope(ctx, q, v)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, unreadForumsSQL, sc.args()...)
	if err != nil {
		return nil, fmt.Errorf("board: find unread forums: %w", err)
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("board: find unread forums: %w", err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("board: find unread forums: %w", err)
	}
	return out, nil
}

// NewRow is one line of the "new since your last visit" list.
type NewRow struct {
	TopicRow
	// Forum is the forum of the topic. Only ID and Name are set.
	Forum *Forum
}

// NewPage is one page of the "new since your last visit" list.
type NewPage struct {
	Topics      []NewRow
	Page, Pages int
	// Total is the number of topics on all pages.
	Total int
}

// newSinceWhere selects the topics of the list: the last post is newer than the
// member's last visit, the topic is not a moved-topic marker, and the viewer may
// see it. The time is a scalar subquery that PostgreSQL evaluates once, so
// topics_last_post_idx can bound the scan. The scope arguments are $1 to $4.
var newSinceWhere = `t.last_post_at > (select coalesce(last_visit_at, registered_at) from users where id = $4::bigint)
   and t.moved_to_topic_id is null and ` + visible("t", 1)

// newSinceOrder is the order of the list: newest activity first, the order of
// topics_last_post_idx.
const newSinceOrder = ` order by t.last_post_at desc, t.id desc`

// CountNew returns how many topics [NewSince] lists for v, over all pages. A
// guest has none.
func CountNew(ctx context.Context, q db.Querier, v Viewer) (int, error) {
	if v.UserID == 0 {
		return 0, nil
	}
	sc, err := newScope(ctx, q, v)
	if err != nil {
		return 0, err
	}
	var n int
	err = q.QueryRow(ctx, `
select count(*) from topics t where `+newSinceWhere, sc.args()...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("board: count new topics: %w", err)
	}
	return n, nil
}

// NewSince returns page number page (from 1) of the topics that have a post
// since the member's last visit, newest first. It lists the topics that v may
// read and see, and no moved-topic marker. New marks the ones the member has
// not read. The member's visit and marks are read from the database, so the
// list agrees with the badges of the other pages.
//
// A guest has no list: page 1 is empty, and any other page is not found. A
// page after the last gives [ErrNotFound]. An empty list has one page.
func NewSince(ctx context.Context, q db.Querier, v Viewer, page int) (NewPage, error) {
	if v.UserID == 0 {
		if page != 1 {
			return NewPage{}, ErrNotFound
		}
		return NewPage{Page: 1, Pages: 1}, nil
	}
	total, err := CountNew(ctx, q, v)
	if err != nil {
		return NewPage{}, err
	}
	out := NewPage{Page: page, Pages: pageCount(total, topicsPerPage), Total: total}
	if page < 1 || page > out.Pages {
		return NewPage{}, ErrNotFound
	}
	sc, err := newScope(ctx, q, v)
	if err != nil {
		return NewPage{}, err
	}
	rows, err := topicRows(ctx, q, `where `+newSinceWhere, newSinceOrder, `limit $5 offset $6`,
		append(sc.args(), topicsPerPage, (page-1)*topicsPerPage)...)
	if err != nil {
		return NewPage{}, err
	}

	ids := []int64{}
	for _, r := range rows {
		ids = append(ids, r.ForumID)
	}
	names := map[int64]*Forum{}
	if len(ids) > 0 {
		fr, err := q.Query(ctx, `select id, name from forums where id = any($1)`, ids)
		if err != nil {
			return NewPage{}, fmt.Errorf("board: read forum names: %w", err)
		}
		defer fr.Close()
		for fr.Next() {
			f := &Forum{}
			if err := fr.Scan(&f.ID, &f.Name); err != nil {
				return NewPage{}, fmt.Errorf("board: read forum names: %w", err)
			}
			names[f.ID] = f
		}
		if err := fr.Err(); err != nil {
			return NewPage{}, fmt.Errorf("board: read forum names: %w", err)
		}
	}
	for _, r := range rows {
		out.Topics = append(out.Topics, NewRow{TopicRow: r, Forum: names[r.ForumID]})
	}
	return out, nil
}

// FirstUnread returns the first post of topic topicID that v sees and has not
// read: the first one after the newest of the member's mark-all time, forum
// mark and topic mark. When no post is newer, it returns the last post that v
// sees. A guest has no marks, so the first post comes back.
//
// A topic that v may not see, a missing topic, a moved-topic marker and a topic
// with no post that v sees give [ErrNotFound].
func FirstUnread(ctx context.Context, q db.Querier, v Viewer, topicID int64) (int64, error) {
	sc, err := newScope(ctx, q, v)
	if err != nil {
		return 0, err
	}
	var postID *int64
	err = q.QueryRow(ctx, `
select coalesce(
  (select p.id from posts p
    where p.topic_id = t.id and `+visible("p", 1)+`
      and p.created_at > coalesce(`+unreadAfter+`, '-infinity'::timestamptz)
    order by p.created_at, p.id limit 1),
  (select p.id from posts p
    where p.topic_id = t.id and `+visible("p", 1)+`
    order by p.created_at desc, p.id desc limit 1))
  from topics t
  left join users me on me.id = $4::bigint
  left join forum_marks fm on fm.user_id = $4::bigint and fm.forum_id = t.forum_id
  left join topic_marks tm on tm.user_id = $4::bigint and tm.topic_id = t.id
 where t.id = $5 and t.moved_to_topic_id is null and `+visible("t", 1), append(sc.args(), topicID)...).Scan(&postID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && postID == nil) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("board: first unread post of topic %d: %w", topicID, err)
	}
	return *postID, nil
}
