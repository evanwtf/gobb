package board

import (
	"context"
	"errors"
	"fmt"
	"html"
	"html/template"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/evanwtf/gobb/internal/account"
	"github.com/evanwtf/gobb/internal/db"
)

// Search limits.
const (
	// searchPerPage is the number of hits on a page.
	searchPerPage = 25
	// maxSearchPages is the last page that a search serves.
	maxSearchPages = 40
	// maxSearchHits is the most hits that a search counts: 40 pages of 25. A
	// query never reads past it, so a word that most posts hold costs no more
	// than a rare one.
	maxSearchHits = searchPerPage * maxSearchPages
	// MaxSearchBytes is the longest text or author that [Search] accepts.
	MaxSearchBytes = 256
)

// The two runes that mark a matched word in the output of ts_headline. They are
// in a Unicode private-use block; [Search] removes them from the text first, so
// no post can place one.
const (
	markStart = ""
	markStop  = ""
)

// ts_headline drops the text of an HTML-like tag from its output. A post that
// holds "<b>" would lose it, so the query swaps "<" and ">" for these two
// private-use runes before the headline and [snippetHTML] swaps them back.
const (
	angleOpen  = "\ue002"
	angleClose = "\ue003"
)

// headlineOptions are the options of ts_headline: the two markers and the size
// of the snippet.
const headlineOptions = "StartSel=" + markStart + ", StopSel=" + markStop +
	", MaxWords=35, MinWords=15, ShortWord=2, MaxFragments=0"

// ErrBadSearch means the search text or the author name is too long.
var ErrBadSearch = errors.New("board: bad search")

// SearchQuery is one search. Text is the words to find. ForumID is the forum to
// search, or 0 for every forum. Author is a member name. Sort is "new" (the
// default) or "best". Page counts from 1.
type SearchQuery struct {
	Text    string
	ForumID int64
	Author  string
	Sort    string
	Page    int
}

// Hit is one post that a search found.
type Hit struct {
	Post  PostRef
	Forum *Forum
	// Snippet is a piece of the post, safe for HTML. The words that matched are
	// in <mark> elements.
	Snippet template.HTML
}

// SearchPage is one page of hits. Total stops at 1,000, so Pages is at most 40.
type SearchPage struct {
	Hits        []Hit
	Total       int
	Page, Pages int
}

// SearchForum is a forum in which a viewer may search, for the forum select.
type SearchForum struct {
	ID   int64
	Name string
	// Depth is the number of listed ancestors, to indent a child under its
	// parent.
	Depth int
}

// searchForumIDs returns the forums in which v may read and search, narrowed to
// forumID when that is not 0.
func searchForumIDs(ctx context.Context, q db.Querier, v Viewer, forumID int64) ([]int64, error) {
	sc, err := newScope(ctx, q, v)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for _, id := range sc.read {
		if forumID != 0 && id != forumID {
			continue
		}
		if v.Perms.Has("forum.search", id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// Search finds posts for v. It covers the forums where v has forum.read and
// forum.search, and only approved posts in approved topics that are not
// markers. A viewer without account.search gets [ErrForbidden]. A text or an
// author over [MaxSearchBytes] gives [ErrBadSearch]. A page past 40, or past
// the last page of the hits, gives [ErrNotFound]. An empty text with no author
// is no search: the result has no hits.
//
// The text goes through websearch_to_tsquery. A post matches on its search
// column. The first post of a topic also matches on the title of the topic.
// The author matches the cleaned user name, so an unknown author gives no
// hits and a post without an author ID is never found by author.
//
// A search runs one statement for the forums, one for the author when there is
// one, one for the total and one for the page of hits. The total counts at most
// 1,000 rows, and ts_headline runs on the hits of the page only.
func Search(ctx context.Context, q db.Querier, v Viewer, sq SearchQuery) (SearchPage, error) {
	if !v.Perms.Has("account.search", 0) {
		return SearchPage{}, ErrForbidden
	}
	text, author := strings.TrimSpace(sq.Text), strings.TrimSpace(sq.Author)
	if len(sq.Text) > MaxSearchBytes || len(sq.Author) > MaxSearchBytes {
		return SearchPage{}, ErrBadSearch
	}
	page := max(sq.Page, 1)
	if page > maxSearchPages {
		return SearchPage{}, ErrNotFound
	}
	empty := SearchPage{Page: 1, Pages: 1}
	if text == "" && author == "" {
		if page > 1 {
			return SearchPage{}, ErrNotFound
		}
		return empty, nil
	}

	forums, err := searchForumIDs(ctx, q, v, sq.ForumID)
	if err != nil {
		return SearchPage{}, err
	}
	// The arguments of the statements: the forums first, then the text, then
	// the author.
	args := []any{forums}
	var authorID int64
	if author != "" {
		err := q.QueryRow(ctx, `select id from users where username_clean = $1`, account.CleanUsername(author)).Scan(&authorID)
		if errors.Is(err, pgx.ErrNoRows) {
			return finishEmpty(page)
		}
		if err != nil {
			return SearchPage{}, fmt.Errorf("board: search author: %w", err)
		}
	}
	if len(forums) == 0 {
		return finishEmpty(page)
	}
	sb := searchBuilder{args: args, byText: text != "", byAuthor: author != "", best: sq.Sort == "best"}
	if sb.byText {
		sb.text = sb.arg(text)
	}
	if sb.byAuthor {
		sb.author = sb.arg(authorID)
	}

	var total int
	if err := q.QueryRow(ctx, sb.countSQL(), sb.args...).Scan(&total); err != nil {
		return SearchPage{}, fmt.Errorf("board: search count: %w", err)
	}
	pages := pageCount(total, searchPerPage)
	if page > pages {
		return SearchPage{}, ErrNotFound
	}
	out := SearchPage{Total: total, Page: page, Pages: pages}
	if total == 0 {
		return out, nil
	}
	hits, err := sb.page(ctx, q, page)
	if err != nil {
		return SearchPage{}, err
	}
	out.Hits = hits
	return out, nil
}

// finishEmpty is the result of a search that cannot match: no hits, one page.
func finishEmpty(page int) (SearchPage, error) {
	if page > 1 {
		return SearchPage{}, ErrNotFound
	}
	return SearchPage{Page: 1, Pages: 1}, nil
}

// searchBuilder writes the SQL of one search. The forums are always $1.
type searchBuilder struct {
	args             []any
	byText, byAuthor bool
	best             bool
	// text and author are the placeholders of the text and the author ID.
	text, author string
}

// arg adds a query argument and returns its placeholder.
func (b *searchBuilder) arg(v any) string {
	b.args = append(b.args, v)
	return fmt.Sprintf("$%d", len(b.args))
}

// hits returns the SQL of the candidate rows: id, created_at and rank. It is one
// arm for the posts, and with a text a second arm for the first posts whose
// topic title matches while their own text does not. The two arms never hold the
// same post, so a union all counts each post once and the total needs no
// grouping. withRank asks for ts_rank; the total does not.
func (b *searchBuilder) hits(withRank bool) string {
	rank := func(expr string) string {
		if withRank && b.best {
			return expr
		}
		return "0::real"
	}
	authorA, authorB := "", ""
	if b.byAuthor {
		authorA = " and p.author_id = " + b.author
		authorB = " and fp.author_id = " + b.author
	}
	if !b.byText {
		return `
select p.id, p.created_at, 0::real as rank
  from posts p join topics t on t.id = p.topic_id
 where p.visibility = 'approved' and p.forum_id = any($1::bigint[])
   and t.visibility = 'approved' and t.moved_to_topic_id is null` + authorA
	}
	tsq := "websearch_to_tsquery('english'::regconfig, " + b.text + ")"
	return `
select p.id, p.created_at, ` + rank("ts_rank(p.search, "+tsq+")") + ` as rank
  from posts p join topics t on t.id = p.topic_id
 where p.search @@ ` + tsq + ` and p.visibility = 'approved' and p.forum_id = any($1::bigint[])
   and t.visibility = 'approved' and t.moved_to_topic_id is null` + authorA + `
union all
select fp.id, fp.created_at, ` + rank("ts_rank(to_tsvector('english'::regconfig, t.title), "+tsq+")") + `
  from topics t
  cross join lateral (select p2.id, p2.created_at, p2.author_id, p2.search from posts p2
                       where p2.topic_id = t.id and p2.visibility = 'approved'
                       order by p2.created_at, p2.id limit 1) fp
 where to_tsvector('english'::regconfig, t.title) @@ ` + tsq + `
   and t.visibility = 'approved' and t.moved_to_topic_id is null and t.forum_id = any($1::bigint[])
   and not (fp.search @@ ` + tsq + `)` + authorB
}

// countSQL returns the statement of the total. It stops at [maxSearchHits] rows.
func (b *searchBuilder) countSQL() string {
	return "with hits as (" + b.hits(false) + ") select count(*) from (select 1 from hits limit " +
		strconv.Itoa(maxSearchHits) + ") c"
}

// pageSQL returns the statement of one page of hits and its arguments.
// ts_headline runs only on the rows of the page.
func (b *searchBuilder) pageSQL(page int) (string, []any) {
	order := "h.created_at desc, h.id desc"
	if b.best && b.byText {
		order = "h.rank desc, " + order
	}
	args := append([]any(nil), b.args...)
	snippet := "left(p.body_text, 300)"
	if b.byText {
		opts := fmt.Sprintf("$%d", len(args)+1)
		args = append(args, headlineOptions)
		snippet = "ts_headline('english'::regconfig, translate(p.subject || ' ' || p.body_text, E'<>\\uE000\\uE001\\uE002\\uE003', E'\\uE002\\uE003'), " +
			"websearch_to_tsquery('english'::regconfig, " + b.text + "), " + opts + ")"
	}
	args = append(args, searchPerPage, (page-1)*searchPerPage)
	limit := fmt.Sprintf("limit $%d offset $%d", len(args)-1, len(args))
	return `
with hits as (` + b.hits(true) + `)
select p.id, p.topic_id, t.title, coalesce(p.author_id, 0), coalesce(u.username, p.author_name), p.created_at,
       f.id, f.name, f.kind, ` + snippet + `
  from (select * from hits h order by ` + order + ` ` + limit + `) h
  join posts p on p.id = h.id
  join topics t on t.id = p.topic_id
  join forums f on f.id = p.forum_id
  left join users u on u.id = p.author_id
 order by ` + order, args
}

// page reads the hits of one page.
func (b *searchBuilder) page(ctx context.Context, q db.Querier, page int) ([]Hit, error) {
	sql, args := b.pageSQL(page)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("board: search: %w", err)
	}
	defer rows.Close()
	byForum := map[int64]*Forum{}
	var out []Hit
	for rows.Next() {
		var (
			h   Hit
			raw string
			f   Forum
		)
		if err := rows.Scan(&h.Post.ID, &h.Post.TopicID, &h.Post.TopicTitle, &h.Post.Author.ID, &h.Post.Author.Name,
			&h.Post.At, &f.ID, &f.Name, &f.Kind, &raw); err != nil {
			return nil, fmt.Errorf("board: search: %w", err)
		}
		if known, ok := byForum[f.ID]; ok {
			h.Forum = known
		} else {
			f.Readable = true
			h.Forum = &f
			byForum[f.ID] = &f
		}
		h.Snippet = snippetHTML(raw, b.byText)
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("board: search: %w", err)
	}
	return out, nil
}

// snippetHTML turns the text of a snippet into HTML. It escapes the text, then
// turns the two marker runes into <mark> and </mark>. Text that did not go
// through ts_headline carries no marks, and a marker rune in it is dropped.
func snippetHTML(raw string, marked bool) template.HTML {
	if !marked {
		raw = strings.NewReplacer(markStart, "", markStop, "", angleOpen, "", angleClose, "").Replace(raw)
		return template.HTML(html.EscapeString(raw)) //nolint:gosec // escaped on this line
	}
	esc := html.EscapeString(strings.NewReplacer(angleOpen, "<", angleClose, ">").Replace(raw))
	esc = strings.NewReplacer(markStart, "<mark>", markStop, "</mark>").Replace(esc)
	return template.HTML(esc) //nolint:gosec // escaped above; only the two markers became tags
}

// SearchForums lists the forums in which v may search, in tree order, for the
// forum select of the search page. A category and a link forum hold no posts, so
// they are left out. Depth counts the listed ancestors of a forum.
func SearchForums(ctx context.Context, q db.Querier, v Viewer) ([]SearchForum, error) {
	rows, err := q.Query(ctx, `select id, parent_id, kind, name from forums order by position, id`)
	if err != nil {
		return nil, fmt.Errorf("board: list search forums: %w", err)
	}
	defer rows.Close()
	type node struct {
		id       int64
		kind     string
		name     string
		children []*node
	}
	var roots []*node
	byID := map[int64]*node{}
	parents := map[int64]*int64{}
	var all []*node
	for rows.Next() {
		n := &node{}
		var parent *int64
		if err := rows.Scan(&n.id, &parent, &n.kind, &n.name); err != nil {
			return nil, fmt.Errorf("board: list search forums: %w", err)
		}
		byID[n.id], parents[n.id] = n, parent
		all = append(all, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("board: list search forums: %w", err)
	}
	for _, n := range all {
		if p := parents[n.id]; p != nil && byID[*p] != nil {
			byID[*p].children = append(byID[*p].children, n)
		} else {
			roots = append(roots, n)
		}
	}
	var out []SearchForum
	var walk func(n *node, depth, guard int)
	walk = func(n *node, depth, guard int) {
		if guard > maxDepth {
			return
		}
		listed := n.kind == "forum" && v.Perms.Has("forum.read", n.id) && v.Perms.Has("forum.search", n.id)
		if listed {
			out = append(out, SearchForum{ID: n.id, Name: n.name, Depth: depth})
			depth++
		}
		for _, c := range n.children {
			walk(c, depth, guard+1)
		}
	}
	for _, n := range roots {
		walk(n, 0, 0)
	}
	return out, nil
}
