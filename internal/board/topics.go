package board

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evanwtf/gobb/internal/db"
)

// TopicRow is one line of a topic list.
type TopicRow struct {
	ID    int64
	Title string
	// Kind is "normal", "sticky", "announce" or "global".
	Kind string
	// Visibility is "approved", "unapproved" or "deleted". The viewer sees the
	// last two only with the right to approve or delete in the forum.
	Visibility string
	Locked     bool
	Author     Author
	// Replies and Views are the stored counters. Replies counts the approved
	// posts minus the first.
	Replies, Views int
	// Last is the newest approved post. Its ID is 0 for a topic with no
	// approved post, and for a moved-topic marker. A topic page does not fill it.
	Last PostRef
	// MovedTo is the ID of the target topic when the row is a moved-topic marker,
	// and 0 otherwise.
	MovedTo int64
	// ForumID is the forum that holds the topic.
	ForumID int64
	// New is true when the topic is unread for a member: its last post is newer
	// than the member's mark for the topic, for its forum and for the whole
	// board. A guest, a marker and a topic whose lists did not ask for it have
	// New false.
	New bool
}

// ForumPage is one page of a forum or a category.
type ForumPage struct {
	Forum *Forum
	// Crumbs are the ancestors of the forum that the viewer may list, from the top.
	Crumbs []*Forum
	// Subforums are the children of the forum that the viewer may list.
	Subforums []*Forum
	// Pinned holds the announcements of the forum and every global topic of a
	// forum that the viewer may read. It is the same on every page and does not
	// count toward the pages.
	Pinned []TopicRow
	// Topics holds the page: sticky first, then the newest activity.
	Topics      []TopicRow
	Page, Pages int
}

// LinkError is the error of [ForumView] for a link forum. The caller redirects
// to URL.
type LinkError struct{ URL string }

func (e LinkError) Error() string { return "board: link forum to " + e.URL }

// MovedError is the error of [TopicView] for a moved-topic marker whose target
// the viewer may see. The caller redirects to the topic To.
type MovedError struct{ To int64 }

func (e MovedError) Error() string { return fmt.Sprintf("board: topic moved to %d", e.To) }

// locateForum finds forum id for v. It returns the forum, whether v may list
// it, and the ancestors that v may list. A forum that v may list comes from the
// tree of [Index], with its listed children and its rolled-up numbers. One that
// v may not list but may read comes from its own row, with no children and no
// ancestors. Any other forum gives [ErrNotFound]; the caller applies the rule
// for the kind.
//
// withTree asks for the New flags and the last posts of the tree, which a
// forum page shows for its subforums. A topic page shows neither.
func locateForum(ctx context.Context, q db.Querier, v Viewer, id int64, withTree bool) (f *Forum, listed bool, crumbs []*Forum, err error) {
	tree, err := index(ctx, q, v, withTree, withTree)
	if err != nil {
		return nil, false, nil, err
	}
	byID := map[int64]*Forum{}
	stack := append([]*Forum(nil), tree...)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		byID[n.ID] = n
		stack = append(stack, n.Children...)
	}
	if f, ok := byID[id]; ok {
		for p, depth := byID[f.ParentID], 0; p != nil && depth < maxDepth; p, depth = byID[p.ParentID], depth+1 {
			crumbs = append([]*Forum{p}, crumbs...)
		}
		return f, true, crumbs, nil
	}

	// Not listed. The viewer may still read the forum.
	if !v.Perms.Has("forum.read", id) {
		return nil, false, nil, ErrNotFound
	}
	f = &Forum{ID: id}
	var parent *int64
	err = q.QueryRow(ctx, `
select parent_id, kind, name, description, rules, coalesce(link_url, ''), locked, topic_count, post_count
  from forums where id = $1`, id).
		Scan(&parent, &f.Kind, &f.Name, &f.Description, &f.Rules, &f.LinkURL, &f.Locked, &f.Topics, &f.Posts)
	if err == pgx.ErrNoRows {
		return nil, false, nil, ErrNotFound
	}
	if err != nil {
		return nil, false, nil, fmt.Errorf("board: read forum %d: %w", id, err)
	}
	if parent != nil {
		f.ParentID = *parent
	}
	f.Readable = f.Kind == "forum"
	return f, false, nil, nil
}

// ForumView returns page number page (from 1) of forum forumID as v sees it.
//
//   - A category needs forum.list. It shows its children and no topics.
//   - A link forum needs forum.list and gives a [LinkError].
//   - A forum needs forum.read. A forum that v may list but not read gives
//     [ErrForbidden]: v already sees its name on the index, so the refusal
//     tells nothing new, and the page can offer a sign-in. A forum that v may
//     neither list nor read gives [ErrNotFound], like a missing one (spec 14).
//   - A page after the last gives [ErrNotFound]. An empty forum has one page.
func ForumView(ctx context.Context, q db.Querier, v Viewer, forumID int64, page int) (ForumPage, error) {
	f, listed, crumbs, err := locateForum(ctx, q, v, forumID, true)
	if err != nil {
		return ForumPage{}, err
	}
	switch f.Kind {
	case "link":
		if !listed {
			return ForumPage{}, ErrNotFound
		}
		return ForumPage{}, LinkError{URL: f.LinkURL}
	case "category":
		if !listed || page != 1 {
			return ForumPage{}, ErrNotFound
		}
		return ForumPage{Forum: f, Crumbs: crumbs, Subforums: f.Children, Page: 1, Pages: 1}, nil
	}
	if !v.Perms.Has("forum.read", forumID) {
		// locateForum found the forum and v may not read it, so v may list it.
		return ForumPage{}, ErrForbidden
	}
	out := ForumPage{Forum: f, Crumbs: crumbs, Subforums: f.Children, Page: page}

	sc, err := newScope(ctx, q, v)
	if err != nil {
		return ForumPage{}, err
	}
	args := append(sc.args(), forumID)

	var n int
	err = q.QueryRow(ctx, `select count(*) from topics t where `+regularTopics, args...).Scan(&n)
	if err != nil {
		return ForumPage{}, fmt.Errorf("board: count topics: %w", err)
	}
	out.Pages = pageCount(n, topicsPerPage)
	if page < 1 || page > out.Pages {
		return ForumPage{}, ErrNotFound
	}

	out.Topics, err = topicRows(ctx, q, `where `+regularTopics, regularOrder, `limit $6 offset $7`,
		append(args, topicsPerPage, (page-1)*topicsPerPage)...)
	if err != nil {
		return ForumPage{}, err
	}
	// Two queries, so that each one reads its own partial index: global topics
	// come first, then the announcements of this forum.
	global, err := topicRows(ctx, q, `where `+globalTopics, globalOrder, ``, sc.args()...)
	if err != nil {
		return ForumPage{}, err
	}
	announce, err := topicRows(ctx, q, `where `+announceTopics, announceOrder, ``, args...)
	if err != nil {
		return ForumPage{}, err
	}
	out.Pinned = append(global, announce...)
	return out, nil
}

// markerVisible is the SQL condition on a topic t: it is not a moved-topic
// marker, or the viewer may see its target. The four scope arguments are $1 to
// $4.
var markerVisible = `(t.moved_to_topic_id is null or exists (
  select 1 from topics m where m.id = t.moved_to_topic_id and ` + visible("m", 1) + `))`

// regularTopics selects the topics of forum $5 that the page lists in pages. An
// announcement or a global topic is pinned instead.
var regularTopics = `t.forum_id = $5 and t.kind in ('normal', 'sticky') and ` + visible("t", 1) + ` and ` + markerVisible

// globalTopics selects the global topics of every forum that the viewer may
// read. It has no forum argument. The partial index topics_global_idx serves it.
var globalTopics = `t.kind = 'global' and ` + visible("t", 1) + ` and ` + markerVisible

// globalOrder is the order of the global topics: the order of topics_global_idx.
const globalOrder = ` order by t.last_post_at desc, t.id desc`

// announceTopics selects the announcements of forum $5. The partial index
// topics_announce_idx serves it.
var announceTopics = `t.kind = 'announce' and t.forum_id = $5 and ` + visible("t", 1) + ` and ` + markerVisible

// announceOrder is the order of the announcements: the order of
// topics_announce_idx.
const announceOrder = ` order by t.last_post_at desc, t.id desc`

// regularOrder is the order of a page of topics: sticky first, then the newest
// activity.
const regularOrder = ` order by (t.kind = 'sticky') desc, t.last_post_at desc, t.id desc`

// topicSQL returns the topic list query for the where, order and limit clauses.
// The inner select picks the page from topics alone, so the order and the limit
// use the indexes of topics. The outer select then joins the authors, the last
// post and the viewer's row and marks (the viewer being $4) to those rows only,
// each by primary key (the lateral join with a limit keeps the planner from
// reading all the marks of a member who has tens of thousands): the New flag of every row comes from the same statement,
// and a member with many marks costs no more than one with none.
func topicSQL(where, order, limit string) string {
	return `
select t.id, t.title, t.kind, t.visibility, t.locked,
       coalesce(t.author_id, 0), coalesce(au.username, t.author_name),
       t.reply_count, t.view_count, coalesce(t.moved_to_topic_id, 0), t.forum_id,
       coalesce(t.moved_to_topic_id is null and t.last_post_at > ` + unreadAfter + `
              and ` + seesPostAfter(unreadAfter) + `, false),
       lp.id, lp.created_at, coalesce(lp.author_id, 0), coalesce(lu.username, lp.author_name)
  from (select t.* from topics t ` + where + order + ` ` + limit + `) t
  left join users me on me.id = $4::bigint
  left join forum_marks fm on fm.user_id = $4::bigint and fm.forum_id = t.forum_id
  left join lateral (select marked_at from topic_marks
                      where user_id = $4::bigint and topic_id = t.id limit 1) tm on true
  left join users au on au.id = t.author_id
  left join posts lp on lp.id = t.last_post_id and lp.visibility = 'approved'
  left join users lu on lu.id = lp.author_id` + order
}

// topicRows runs the topic list query with the given where, order and limit
// clauses and returns the rows. The arguments start with the four of
// [scope.args].
func topicRows(ctx context.Context, q db.Querier, where, order, limit string, args ...any) ([]TopicRow, error) {
	rows, err := q.Query(ctx, topicSQL(where, order, limit), args...)
	if err != nil {
		return nil, fmt.Errorf("board: list topics: %w", err)
	}
	defer rows.Close()
	var out []TopicRow
	for rows.Next() {
		var (
			r      TopicRow
			lastID *int64
			lastAt *time.Time
			lastBy int64
			lastNm *string
		)
		if err := rows.Scan(&r.ID, &r.Title, &r.Kind, &r.Visibility, &r.Locked,
			&r.Author.ID, &r.Author.Name, &r.Replies, &r.Views, &r.MovedTo, &r.ForumID, &r.New,
			&lastID, &lastAt, &lastBy, &lastNm); err != nil {
			return nil, fmt.Errorf("board: list topics: %w", err)
		}
		if lastID != nil && lastAt != nil && lastNm != nil {
			r.Last = PostRef{ID: *lastID, TopicID: r.ID, TopicTitle: r.Title,
				Author: Author{ID: lastBy, Name: *lastNm}, At: *lastAt}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("board: list topics: %w", err)
	}
	return out, nil
}
