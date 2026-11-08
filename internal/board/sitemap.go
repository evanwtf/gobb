package board

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/markup"
)

// SitemapPage is one page of the sitemap: the index, a page of a forum or a
// category, or a page of a topic.
type SitemapPage struct {
	// Kind is "index", "forum" or "topic".
	Kind string
	// ID is the forum or topic ID. It is 0 for the index.
	ID int64
	// Page is the page number, from 1.
	Page int
	// Mod is the time of the newest post that the page shows, or zero when the
	// page shows none.
	Mod time.Time
}

// Sitemap returns every page that viewer v can open on the index, the forums and
// the topics, in that order: the forums and the topics by ID, then page. The
// caller passes the guest viewer ([Guest]), so the list holds what a crawler
// can read and nothing more.
//
//   - A category is in the list when v may list it ([Index]). It has one page.
//   - A forum is in the list when v may read it, as [ForumView] decides. It has
//     the pages that [ForumView] serves: the topics that it lists, moved-topic
//     markers included, 50 to a page.
//   - A topic is in the list when v may see it, as [TopicView] decides, and it is
//     not a moved-topic marker. It has the pages of the posts that v may see.
//
// The visibility rules are the clauses that the pages use ([visible] and
// markerVisible), so a page in the list is a page that v can open.
func Sitemap(ctx context.Context, q db.Querier, v Viewer) ([]SitemapPage, error) {
	sc, err := newScope(ctx, q, v)
	if err != nil {
		return nil, err
	}
	tree, err := index(ctx, q, v, false, true)
	if err != nil {
		return nil, err
	}
	var forums []SitemapPage
	stack := slices.Clone(tree)
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		stack = append(stack, f.Children...)
		if f.Kind == "category" {
			sp := SitemapPage{Kind: "forum", ID: f.ID, Page: 1}
			if f.Last != nil {
				sp.Mod = f.Last.At
			}
			forums = append(forums, sp)
		}
	}

	// The forums that v may read, with page 1 each, and then the pages that the
	// listed topics fill.
	pages := map[[2]int64]time.Time{}
	rows, err := q.Query(ctx, `select id from forums where kind = 'forum' and id = any($1::bigint[])`, sc.read)
	if err != nil {
		return nil, fmt.Errorf("board: sitemap forums: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("board: sitemap forums: %w", err)
	}
	for _, id := range ids {
		pages[[2]int64{id, 1}] = time.Time{}
	}
	rows, err = q.Query(ctx, `
select forum_id, (rn - 1) / $5 + 1, max(last_post_at)
  from (select t.forum_id, t.last_post_at,
               row_number() over (partition by t.forum_id`+regularOrder+`) as rn
          from topics t where `+listedTopics+`) x
 group by 1, 2`, append(sc.args(), topicsPerPage)...)
	if err != nil {
		return nil, fmt.Errorf("board: sitemap forum pages: %w", err)
	}
	if err := collectPages(rows, pages); err != nil {
		return nil, fmt.Errorf("board: sitemap forum pages: %w", err)
	}
	forums = append(forums, sortedPages("forum", pages)...)
	slices.SortFunc(forums, func(a, b SitemapPage) int {
		if a.ID != b.ID {
			return cmpInt(a.ID, b.ID)
		}
		return a.Page - b.Page
	})

	// The topics that v may see, with page 1 each, and then the pages that their
	// posts fill.
	pages = map[[2]int64]time.Time{}
	rows, err = q.Query(ctx, `select t.id, t.last_post_at from topics t
 where t.moved_to_topic_id is null and `+visible("t", 1), sc.args()...)
	if err != nil {
		return nil, fmt.Errorf("board: sitemap topics: %w", err)
	}
	if err := collectPages(rows, pages); err != nil {
		return nil, fmt.Errorf("board: sitemap topics: %w", err)
	}
	rows, err = q.Query(ctx, `
select topic_id, (rn - 1) / $5 + 1, max(greatest(created_at, coalesce(edited_at, created_at)))
  from (select p.topic_id, p.created_at, p.edited_at,
               row_number() over (partition by p.topic_id`+postOrder+`) as rn
          from posts p
          join topics t on t.id = p.topic_id
         where t.moved_to_topic_id is null and `+visible("t", 1)+` and `+visible("p", 1)+`) x
 group by 1, 2`, append(sc.args(), postsPerPage)...)
	if err != nil {
		return nil, fmt.Errorf("board: sitemap topic pages: %w", err)
	}
	// The page 1 of a topic shows its posts: the newest of them dates the page,
	// not the time of the topic row.
	posted := map[[2]int64]time.Time{}
	if err := collectPages(rows, posted); err != nil {
		return nil, fmt.Errorf("board: sitemap topic pages: %w", err)
	}
	for k, at := range posted {
		pages[k] = at
	}
	topics := sortedPages("topic", pages)

	out := []SitemapPage{{Kind: "index", Page: 1}}
	for _, p := range append(slices.Clone(forums), topics...) {
		if p.Mod.After(out[0].Mod) {
			out[0].Mod = p.Mod
		}
	}
	out = append(out, forums...)
	return append(out, topics...), nil
}

// collectPages reads rows of (ID, page, time) into pages. A row with no page
// column holds (ID, time) and stands for page 1.
func collectPages(rows pgx.Rows, pages map[[2]int64]time.Time) error {
	defer rows.Close()
	cols := len(rows.FieldDescriptions())
	for rows.Next() {
		var (
			id, page int64 = 0, 1
			at       *time.Time
		)
		var err error
		if cols == 2 {
			err = rows.Scan(&id, &at)
		} else {
			err = rows.Scan(&id, &page, &at)
		}
		if err != nil {
			return err
		}
		var t time.Time
		if at != nil {
			t = *at
		}
		pages[[2]int64{id, page}] = t
	}
	return rows.Err()
}

// sortedPages returns the pages of kind, by ID and then page.
func sortedPages(kind string, pages map[[2]int64]time.Time) []SitemapPage {
	out := make([]SitemapPage, 0, len(pages))
	for k, at := range pages {
		out = append(out, SitemapPage{Kind: kind, ID: k[0], Page: int(k[1]), Mod: at})
	}
	slices.SortFunc(out, func(a, b SitemapPage) int {
		if a.ID != b.ID {
			return cmpInt(a.ID, b.ID)
		}
		return a.Page - b.Page
	})
	return out
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// FirstPost returns the tree of the first post of topic topicID that v may
// see, for the description of a page of the topic after the first. It gives
// [ErrNotFound] when v sees no post of the topic.
func FirstPost(ctx context.Context, q db.Querier, v Viewer, topicID int64) (*markup.Node, error) {
	sc, err := newScope(ctx, q, v)
	if err != nil {
		return nil, err
	}
	var (
		id  int64
		raw []byte
	)
	err = q.QueryRow(ctx, `
select p.id, p.body_tree from posts p
  join topics t on t.id = p.topic_id
 where p.topic_id = $5 and `+visible("t", 1)+` and `+visible("p", 1)+postOrder+`
 limit 1`, append(sc.args(), topicID)...).Scan(&id, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("board: first post of %d: %w", topicID, err)
	}
	return decodeTree(raw, "post", id), nil
}
