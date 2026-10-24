package board

import (
	"context"
	"fmt"
	"time"

	"github.com/evanwtf/gobb/internal/db"
)

// Author names the writer of a post. ID 0 means there is no profile to link: a
// guest, or a member who is gone.
type Author struct {
	ID   int64
	Name string
}

// PostRef points at one post, with what a list needs to show it.
type PostRef struct {
	ID         int64
	TopicID    int64
	TopicTitle string
	Author     Author
	At         time.Time
}

// Forum is one node of the forum tree, as one viewer sees it.
type Forum struct {
	ID, ParentID int64
	// Kind is "category", "forum" or "link".
	Kind        string
	Name        string
	Description string
	Rules       string
	LinkURL     string
	Locked      bool
	// Readable is true when the viewer may open the forum. It is false for a
	// forum that the viewer may list but not read: the page shows such a forum
	// with a lock and no numbers. A category and a link forum are always
	// readable, because they hold no topics.
	Readable bool
	// Topics and Posts add up the forum and the descendants that the viewer may
	// read. They count approved rows only.
	Topics, Posts int
	// Last is the newest approved post in the same set, or nil.
	Last *PostRef
	// New is true when the forum, or a descendant that the viewer may read,
	// holds an unread topic that the viewer may read. It is always false for a
	// guest.
	New      bool
	Children []*Forum
}

// forumNode is a forum row with the numbers that Index needs while it builds
// the tree.
type forumNode struct {
	Forum
	ownTopics, ownPosts int
	lastPostID          int64
	children            []*forumNode
}

// maxDepth stops the walk of a parent chain that loops. The schema has no
// check against a cycle.
const maxDepth = 32

// Index returns the top-level forums that v may list, each with its listed
// children, in position and then ID order. A forum is listed when it and every
// ancestor have forum.list for v.
//
// Topics, Posts and Last of a forum add up the forum and every listed
// descendant that v may read (forum.read), so a forum that v cannot read adds
// nothing, not a count and not a last post. Index reads the forums with one
// query and the last posts with one more.
//
// For a member, Index also sets New. One more query finds the forums that hold
// unread topics, however many topics there are.
func Index(ctx context.Context, q db.Querier, v Viewer) ([]*Forum, error) {
	return index(ctx, q, v, true)
}

// index is [Index]. The withNew flag skips the query for the New flags.
func index(ctx context.Context, q db.Querier, v Viewer, withNew bool) ([]*Forum, error) {
	rows, err := q.Query(ctx, `
select id, parent_id, kind, name, description, rules, coalesce(link_url, ''),
       locked, topic_count, post_count, coalesce(last_post_id, 0)
  from forums
 order by position, id`)
	if err != nil {
		return nil, fmt.Errorf("board: list forums: %w", err)
	}
	defer rows.Close()
	byID := map[int64]*forumNode{}
	var all []*forumNode
	for rows.Next() {
		n := &forumNode{}
		var parent *int64
		if err := rows.Scan(&n.ID, &parent, &n.Kind, &n.Name, &n.Description, &n.Rules, &n.LinkURL,
			&n.Locked, &n.ownTopics, &n.ownPosts, &n.lastPostID); err != nil {
			return nil, fmt.Errorf("board: list forums: %w", err)
		}
		if parent != nil {
			n.ParentID = *parent
		}
		byID[n.ID] = n
		all = append(all, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("board: list forums: %w", err)
	}

	// Link the listed forums into a tree. The query order puts siblings in
	// position order.
	listed := map[int64]bool{}
	var isListed func(n *forumNode, depth int) bool
	isListed = func(n *forumNode, depth int) bool {
		if ok, seen := listed[n.ID]; seen {
			return ok
		}
		ok := depth < maxDepth && v.Perms.Has("forum.list", n.ID)
		if ok && n.ParentID != 0 {
			p, found := byID[n.ParentID]
			ok = found && isListed(p, depth+1)
		}
		listed[n.ID] = ok
		return ok
	}
	var roots []*forumNode
	for _, n := range all {
		if !isListed(n, 0) {
			continue
		}
		if n.ParentID == 0 {
			roots = append(roots, n)
		} else {
			p := byID[n.ParentID]
			p.children = append(p.children, n)
		}
	}

	// The last posts of the forums that count toward a listed forum.
	var postIDs []int64
	for _, n := range all {
		if listed[n.ID] && n.lastPostID != 0 && n.Kind != "link" && v.Perms.Has("forum.read", n.ID) {
			postIDs = append(postIDs, n.lastPostID)
		}
	}
	posts, err := lastPosts(ctx, q, postIDs)
	if err != nil {
		return nil, err
	}

	var unread map[int64]bool
	if withNew && v.UserID != 0 {
		if unread, err = forumsWithUnread(ctx, q, v); err != nil {
			return nil, err
		}
	}

	var build func(n *forumNode) *Forum
	build = func(n *forumNode) *Forum {
		f := n.Forum
		f.New = n.Kind == "forum" && unread[n.ID]
		f.Readable = n.Kind != "forum" || v.Perms.Has("forum.read", n.ID)
		if n.Kind != "link" && v.Perms.Has("forum.read", n.ID) {
			f.Topics, f.Posts = n.ownTopics, n.ownPosts
			f.Last = posts[n.lastPostID]
		}
		for _, c := range n.children {
			cf := build(c)
			f.Children = append(f.Children, cf)
			f.New = f.New || cf.New
			f.Topics += cf.Topics
			f.Posts += cf.Posts
			if cf.Last != nil && (f.Last == nil || newer(cf.Last, f.Last)) {
				f.Last = cf.Last
			}
		}
		return &f
	}
	out := make([]*Forum, len(roots))
	for i, r := range roots {
		out[i] = build(r)
	}
	return out, nil
}

// newer reports whether a is a later post than b: by time, then by ID.
func newer(a, b *PostRef) bool {
	if !a.At.Equal(b.At) {
		return a.At.After(b.At)
	}
	return a.ID > b.ID
}

// lastPosts reads the posts with the given IDs in one query and returns them by
// ID. A member's current name replaces the name stored on the post.
func lastPosts(ctx context.Context, q db.Querier, ids []int64) (map[int64]*PostRef, error) {
	out := map[int64]*PostRef{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
select p.id, p.topic_id, t.title, coalesce(p.author_id, 0),
       coalesce(u.username, p.author_name), p.created_at
  from posts p
  join topics t on t.id = p.topic_id
  left join users u on u.id = p.author_id
 where p.id = any($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("board: read last posts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		p := &PostRef{}
		if err := rows.Scan(&p.ID, &p.TopicID, &p.TopicTitle, &p.Author.ID, &p.Author.Name, &p.At); err != nil {
			return nil, fmt.Errorf("board: read last posts: %w", err)
		}
		out[p.ID] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("board: read last posts: %w", err)
	}
	return out, nil
}
