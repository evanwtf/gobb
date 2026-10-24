package board_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// allow grants option to group on forum (0 for the whole board) in b.
func allow(b *bundletest.Board, group, forum int64, option string) {
	s := bundle.Yes
	b.Grants = append(b.Grants, bundle.Grant{GroupID: &group, ForumID: forum, Option: &option, Setting: &s})
}

// searchBoard is the small board with the search rights that a case needs:
// guests (group 1) hold account.search and forum.search on forum 2; members
// (group 2) hold account.search and forum.search on forum 2; admins (group 4,
// alice) hold forum.search on forum 3. Guests cannot read forum 3.
func searchBoard() *bundletest.Board {
	b := bundletest.Small()
	allow(b, 1, 0, "account.search")
	allow(b, 1, 2, "forum.search")
	allow(b, 2, 0, "account.search")
	allow(b, 2, 2, "forum.search")
	allow(b, 4, 3, "forum.search")
	return b
}

// guestFor loads the guest viewer.
func guestFor(t *testing.T, pool *pgxpool.Pool) board.Viewer {
	t.Helper()
	ctx := context.Background()
	s, err := board.LoadSettings(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	v, err := board.Guest(ctx, pool, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// hitIDs lists the post IDs of the hits in order.
func hitIDs(p board.SearchPage) []int64 {
	out := make([]int64, len(p.Hits))
	for i, h := range p.Hits {
		out[i] = h.Post.ID
	}
	return out
}

// search runs a search and fails the test on an error.
func search(t *testing.T, pool *pgxpool.Pool, v board.Viewer, sq board.SearchQuery) board.SearchPage {
	t.Helper()
	p, err := board.Search(context.Background(), pool, v, sq)
	if err != nil {
		t.Fatalf("Search(%+v): %v", sq, err)
	}
	return p
}

func TestSearchScope(t *testing.T) {
	pool, _ := boardtest.Load(t, searchBoard())
	g := guestFor(t, pool)

	if got := hitIDs(search(t, pool, g, board.SearchQuery{Text: "example"})); !slices.Equal(got, []int64{1}) {
		t.Errorf("guest, example = %v, want [1]", got)
	}
	// Post 3 waits for approval: nobody finds it, whatever the right.
	if got := hitIDs(search(t, pool, g, board.SearchQuery{Text: "moderator"})); len(got) != 0 {
		t.Errorf("guest, moderator = %v, want none (post 3 is unapproved)", got)
	}
	bob := memberFor(t, pool, 2)
	if got := hitIDs(search(t, pool, bob, board.SearchQuery{Text: "moderator"})); len(got) != 0 {
		t.Errorf("bob, moderator = %v, want none (post 3 is unapproved)", got)
	}

	// forum.search without forum.read gives nothing.
	b := searchBoard()
	allow(b, 1, 3, "forum.search")
	pool2, _ := boardtest.Load(t, b)
	if got := hitIDs(search(t, pool2, guestFor(t, pool2), board.SearchQuery{Text: "color"})); len(got) != 0 {
		t.Errorf("guest with forum.search and no forum.read, color = %v, want none", got)
	}
	// forum.read without forum.search gives nothing either.
	if got := hitIDs(search(t, pool, memberFor(t, pool, 2), board.SearchQuery{Text: "welcome", ForumID: 3})); len(got) != 0 {
		t.Errorf("bob, forum 3 = %v, want none", got)
	}

	// Alice holds both on forum 3.
	alice := memberFor(t, pool, 1)
	if got := hitIDs(search(t, pool, alice, board.SearchQuery{Text: "color"})); !slices.Equal(got, []int64{6, 5}) {
		t.Errorf("alice, color = %v, want [6 5]", got)
	}
	// Without account.search the answer is ErrForbidden, whatever the forum rights.
	b = bundletest.Small()
	allow(b, 2, 2, "forum.search")
	pool3, _ := boardtest.Load(t, b)
	_, err := board.Search(context.Background(), pool3, memberFor(t, pool3, 2), board.SearchQuery{Text: "example"})
	if !errors.Is(err, board.ErrForbidden) {
		t.Errorf("Search without account.search = %v, want ErrForbidden", err)
	}
	// A forum filter for a forum that the viewer cannot search is empty, not an error.
	if got := hitIDs(search(t, pool, g, board.SearchQuery{Text: "color", ForumID: 3})); len(got) != 0 {
		t.Errorf("guest, forum 3 = %v, want none", got)
	}
}

func TestSearchTitle(t *testing.T) {
	b := searchBoard()
	b.Topics[0].Title = "Zebra crossing"
	pool, _ := boardtest.Load(t, b)
	g := guestFor(t, pool)

	// Post 1 is the first post of topic 1: its topic title matches. No other
	// post of the topic matches.
	if got := hitIDs(search(t, pool, g, board.SearchQuery{Text: "zebra"})); !slices.Equal(got, []int64{1}) {
		t.Errorf("zebra = %v, want [1]", got)
	}
	p := search(t, pool, g, board.SearchQuery{Text: "zebra"})
	if p.Total != 1 || p.Hits[0].Post.TopicTitle != "Zebra crossing" {
		t.Errorf("zebra: total %d, title %q", p.Total, p.Hits[0].Post.TopicTitle)
	}
	// A post that matches by its subject and by the title of its topic is one hit.
	b = searchBoard()
	pool2, _ := boardtest.Load(t, b)
	p = search(t, pool2, guestFor(t, pool2), board.SearchQuery{Text: "welcome"})
	if got := hitIDs(p); !slices.Equal(got, []int64{4, 2, 1}) && !slices.Equal(got, []int64{2, 1}) {
		t.Errorf("welcome = %v", got)
	}
	n := 0
	for _, id := range hitIDs(p) {
		if id == 1 {
			n++
		}
	}
	if n != 1 || p.Total != len(p.Hits) {
		t.Errorf("welcome lists post 1 %d times, total %d for %d hits", n, p.Total, len(p.Hits))
	}
	// Title hit with a text that is not in the first post: the sort "best" gives
	// the same post once.
	p = search(t, pool, g, board.SearchQuery{Text: "zebra", Sort: "best"})
	if got := hitIDs(p); !slices.Equal(got, []int64{1}) {
		t.Errorf("zebra, best = %v, want [1]", got)
	}
}

func TestSearchTitleOfHiddenTopics(t *testing.T) {
	b := searchBoard()
	// The marker (topic 3) gets a rare word in its title: a marker is never found.
	b.Topics[2].Title = "Quokka marker"
	// Topic 9, added here, is unapproved: nothing in it is found.
	b.Topics = append(b.Topics, bundle.Topic{
		ID: 9, ForumID: 2, Title: "Narwhal sightings", AuthorID: ptr(int64(1)), AuthorName: "alice",
		CreatedAt: utc(2021, 1, 1, 0, 0), Kind: "normal", Visibility: "unapproved",
	})
	b.Posts = append(b.Posts, bundle.Post{
		ID: 90, TopicID: 9, AuthorID: ptr(int64(1)), AuthorName: "alice", CreatedAt: utc(2021, 1, 1, 0, 0),
		Subject: "Narwhal", Body: bundle.Body{Source: "narwhal narwhal", Markdown: true}, Visibility: "approved",
	})
	pool, _ := boardtest.Load(t, b)
	g := guestFor(t, pool)
	for _, w := range []string{"quokka", "narwhal"} {
		if got := hitIDs(search(t, pool, g, board.SearchQuery{Text: w})); len(got) != 0 {
			t.Errorf("%s = %v, want none", w, got)
		}
	}
}

func TestSearchAuthor(t *testing.T) {
	pool, _ := boardtest.Load(t, searchBoard())
	alice := memberFor(t, pool, 1)
	p := search(t, pool, alice, board.SearchQuery{Author: "BOB"})
	if got := hitIDs(p); !slices.Equal(got, []int64{5, 2}) {
		t.Errorf("author BOB = %v, want [5 2]", got)
	}
	if p.Total != 2 || p.Pages != 1 || p.Page != 1 {
		t.Errorf("total %d, page %d of %d", p.Total, p.Page, p.Pages)
	}
	// The hit has the author, the forum and a snippet of the text.
	h := p.Hits[0]
	if h.Post.Author.ID != 2 || h.Post.Author.Name != "bob" || h.Forum == nil || h.Forum.ID != 3 || h.Forum.Name != "Members Lounge" {
		t.Errorf("hit = %+v, forum %+v", h.Post, h.Forum)
	}
	if !strings.Contains(string(h.Snippet), "Which color do you like best?") {
		t.Errorf("snippet = %q", h.Snippet)
	}
	// With a text, the author narrows the hits.
	if got := hitIDs(search(t, pool, alice, board.SearchQuery{Text: "color", Author: "bob"})); !slices.Equal(got, []int64{5}) {
		t.Errorf("color by bob = %v, want [5]", got)
	}
	// An unknown author, and a name that cleans to nothing, give no hits.
	for _, name := range []string{"nobody", "\u200b"} {
		if got := hitIDs(search(t, pool, alice, board.SearchQuery{Author: name})); len(got) != 0 {
			t.Errorf("author %q = %v, want none", name, got)
		}
	}
	// A guest post has no author ID: it is found by its text and never by author.
	if got := hitIDs(search(t, pool, alice, board.SearchQuery{Author: "Visitor"})); len(got) != 0 {
		t.Errorf("author Visitor = %v, want none", got)
	}
	// The author filter holds on the title arm too.
	b := searchBoard()
	b.Topics[0].Title = "Zebra crossing"
	pool2, _ := boardtest.Load(t, b)
	a2 := memberFor(t, pool2, 1)
	if got := hitIDs(search(t, pool2, a2, board.SearchQuery{Text: "zebra", Author: "bob"})); len(got) != 0 {
		t.Errorf("zebra by bob = %v, want none (alice wrote the first post)", got)
	}
	if got := hitIDs(search(t, pool2, a2, board.SearchQuery{Text: "zebra", Author: "alice"})); !slices.Equal(got, []int64{1}) {
		t.Errorf("zebra by alice = %v, want [1]", got)
	}
}

func TestSearchForumFilterAndSort(t *testing.T) {
	pool, _ := boardtest.Load(t, searchBoard())
	alice := memberFor(t, pool, 1)
	all := search(t, pool, alice, board.SearchQuery{Author: "alice"})
	if got := hitIDs(all); !slices.Equal(got, []int64{6, 1}) {
		t.Errorf("alice's posts = %v, want [6 1] (newest first)", got)
	}
	if got := hitIDs(search(t, pool, alice, board.SearchQuery{Author: "alice", ForumID: 2})); !slices.Equal(got, []int64{1}) {
		t.Errorf("forum 2 = %v, want [1]", got)
	}
	if got := hitIDs(search(t, pool, alice, board.SearchQuery{Author: "alice", ForumID: 3})); !slices.Equal(got, []int64{6}) {
		t.Errorf("forum 3 = %v, want [6]", got)
	}

	// "best" puts the post with more of the word first; "new" the newer one.
	b := searchBoard()
	b.Posts = append(b.Posts,
		bundle.Post{ID: 20, TopicID: 1, AuthorID: ptr(int64(2)), AuthorName: "bob", CreatedAt: utc(2021, 5, 1, 0, 0),
			Subject: "Re: x", Body: bundle.Body{Source: "pelican", Markdown: true}, Visibility: "approved"},
		bundle.Post{ID: 21, TopicID: 1, AuthorID: ptr(int64(2)), AuthorName: "bob", CreatedAt: utc(2021, 5, 2, 0, 0),
			Subject: "Re: x", Body: bundle.Body{Source: "one pelican among many other long words of no use", Markdown: true}, Visibility: "approved"},
		bundle.Post{ID: 22, TopicID: 1, AuthorID: ptr(int64(2)), AuthorName: "bob", CreatedAt: utc(2021, 5, 3, 0, 0),
			Subject: "Re: x", Body: bundle.Body{Source: "pelican pelican pelican pelican", Markdown: true}, Visibility: "approved"},
	)
	pool2, _ := boardtest.Load(t, b)
	g := guestFor(t, pool2)
	if got := hitIDs(search(t, pool2, g, board.SearchQuery{Text: "pelican"})); !slices.Equal(got, []int64{22, 21, 20}) {
		t.Errorf("pelican, new = %v, want [22 21 20]", got)
	}
	if got := hitIDs(search(t, pool2, g, board.SearchQuery{Text: "pelican", Sort: "new"})); !slices.Equal(got, []int64{22, 21, 20}) {
		t.Errorf("pelican, sort=new = %v", got)
	}
	best := hitIDs(search(t, pool2, g, board.SearchQuery{Text: "pelican", Sort: "best"}))
	if len(best) != 3 || best[0] != 22 {
		t.Errorf("pelican, best = %v, want 22 first", best)
	}
	// An unknown sort means the default.
	if got := hitIDs(search(t, pool2, g, board.SearchQuery{Text: "pelican", Sort: "oldest"})); !slices.Equal(got, []int64{22, 21, 20}) {
		t.Errorf("pelican, sort=oldest = %v, want the default order", got)
	}
}

// setBodyText sets the plain text of a post. The search column follows.
func setBodyText(t *testing.T, pool *pgxpool.Pool, id int64, text string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `update posts set body_text = $1 where id = $2`, text, id); err != nil {
		t.Fatal(err)
	}
}

func TestSearchSnippetEscapes(t *testing.T) {
	b := searchBoard()
	b.Posts[5].Body = bundle.Body{Source: "<script>alert(1)</script> needle", Markdown: true, Smilies: true, Autolink: true}
	pool, _ := boardtest.Load(t, b)
	// The markup parser drops raw tags from body_text, so the test writes the
	// text as an import from an older board could hold it.
	setBodyText(t, pool, 6, "<script>alert(1)</script> needle")
	alice := memberFor(t, pool, 1)
	p := search(t, pool, alice, board.SearchQuery{Text: "needle"})
	if len(p.Hits) != 1 || p.Hits[0].Post.ID != 6 {
		t.Fatalf("needle = %v", hitIDs(p))
	}
	s := string(p.Hits[0].Snippet)
	if !strings.Contains(s, "&lt;script&gt;") || !strings.Contains(s, "<mark>needle</mark>") || strings.Contains(s, "<script") {
		t.Errorf("snippet = %q", s)
	}
}

// The marker runes in the text of a post cannot make a mark or break the page.
func TestSearchSnippetMarkerRunes(t *testing.T) {
	b := searchBoard()
	b.Posts[5].Body = bundle.Body{Source: "a \ue000fake\ue001 needle <b>x</b> & \"q\"", Markdown: true}
	pool, _ := boardtest.Load(t, b)
	setBodyText(t, pool, 6, "a \ue000fake\ue001 needle <b>x</b> & \"q\"")
	p := search(t, pool, memberFor(t, pool, 1), board.SearchQuery{Text: "needle"})
	if len(p.Hits) != 1 {
		t.Fatalf("needle = %v", hitIDs(p))
	}
	s := string(p.Hits[0].Snippet)
	if strings.Contains(s, "<mark>fake") || strings.ContainsAny(s, "\ue000\ue001") || strings.Contains(s, "<b>") {
		t.Errorf("snippet = %q", s)
	}
	if strings.Count(s, "<mark>") != 1 || strings.Count(s, "</mark>") != 1 {
		t.Errorf("snippet = %q, want one mark", s)
	}
	// The same text found by author alone has no mark at all.
	p = search(t, pool, memberFor(t, pool, 1), board.SearchQuery{Author: "alice", ForumID: 3})
	s = string(p.Hits[0].Snippet)
	if strings.Contains(s, "<mark>") || strings.ContainsAny(s, "\ue000\ue001") || strings.Contains(s, "<b>") {
		t.Errorf("author-only snippet = %q", s)
	}
}

func TestSearchBounds(t *testing.T) {
	pool, _ := boardtest.Load(t, searchBoard())
	ctx := context.Background()
	g := guestFor(t, pool)

	long := strings.Repeat("a", 257)
	if _, err := board.Search(ctx, pool, g, board.SearchQuery{Text: long}); !errors.Is(err, board.ErrBadSearch) {
		t.Errorf("257 bytes = %v, want ErrBadSearch", err)
	}
	if _, err := board.Search(ctx, pool, g, board.SearchQuery{Text: strings.Repeat("a", 256)}); err != nil {
		t.Errorf("256 bytes = %v, want no error", err)
	}
	if _, err := board.Search(ctx, pool, g, board.SearchQuery{Author: long}); !errors.Is(err, board.ErrBadSearch) {
		t.Errorf("author of 257 bytes = %v, want ErrBadSearch", err)
	}
	// A multi-byte text counts bytes, not runes.
	if _, err := board.Search(ctx, pool, g, board.SearchQuery{Text: strings.Repeat("\u00e9", 129)}); !errors.Is(err, board.ErrBadSearch) {
		t.Errorf("129 two-byte runes = %v, want ErrBadSearch", err)
	}
	if _, err := board.Search(ctx, pool, g, board.SearchQuery{Text: "example", Page: 41}); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("page 41 = %v, want ErrNotFound", err)
	}
	if _, err := board.Search(ctx, pool, g, board.SearchQuery{Text: "example", Page: 2}); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("page 2 of 1 = %v, want ErrNotFound", err)
	}
	// An empty query shows the form alone: no hits, one page, no database work
	// for the hits.
	p := search(t, pool, g, board.SearchQuery{Text: "  "})
	if len(p.Hits) != 0 || p.Total != 0 || p.Page != 1 || p.Pages != 1 {
		t.Errorf("empty query = %+v", p)
	}
	// A query with no hits is page 1 of 1.
	p = search(t, pool, g, board.SearchQuery{Text: "zzzzzz"})
	if len(p.Hits) != 0 || p.Total != 0 || p.Pages != 1 {
		t.Errorf("no hits = %+v", p)
	}
	// Words that the parser drops, and an operator with nothing after it, are
	// no error.
	for _, text := range []string{"the", "!!!", "-", "\"", "or or", "a:*", "(((", "example -zebra"} {
		if _, err := board.Search(ctx, pool, g, board.SearchQuery{Text: text}); err != nil {
			t.Errorf("Search(%q) = %v", text, err)
		}
	}
	// A forum number that matches nothing is no error.
	if _, err := board.Search(ctx, pool, g, board.SearchQuery{Text: "example", ForumID: 999}); err != nil {
		t.Errorf("forum 999 = %v", err)
	}
}

// postsBoard is the small board with n more posts about "gizmo" in forum 2,
// one a minute from 2022-01-01, in topic 1. Post IDs start at 1000.
func postsBoard(n int) *bundletest.Board {
	b := searchBoard()
	for i := range n {
		b.Posts = append(b.Posts, bundle.Post{
			ID: int64(1000 + i), TopicID: 1, AuthorID: ptr(int64(2)), AuthorName: "bob",
			CreatedAt: utc(2022, 1, 1, 0, 0).Add(time.Duration(i) * time.Minute),
			Subject:   "Re: Welcome", Body: bundle.Body{Source: fmt.Sprintf("gizmo number %d", i), Markdown: true},
			Visibility: "approved",
		})
	}
	return b
}

func TestSearchPaging(t *testing.T) {
	pool, _ := boardtest.Load(t, postsBoard(60))
	g := guestFor(t, pool)
	p1 := search(t, pool, g, board.SearchQuery{Text: "gizmo"})
	if p1.Total != 60 || p1.Pages != 3 || p1.Page != 1 || len(p1.Hits) != 25 {
		t.Fatalf("page 1: total %d, %d of %d, %d hits", p1.Total, p1.Page, p1.Pages, len(p1.Hits))
	}
	if p1.Hits[0].Post.ID != 1059 {
		t.Errorf("first hit = %d, want 1059 (the newest)", p1.Hits[0].Post.ID)
	}
	p3 := search(t, pool, g, board.SearchQuery{Text: "gizmo", Page: 3})
	if len(p3.Hits) != 10 || p3.Hits[9].Post.ID != 1000 {
		t.Errorf("page 3: %d hits, last %v", len(p3.Hits), hitIDs(p3))
	}
	// Pages do not overlap.
	seen := map[int64]bool{}
	for page := 1; page <= 3; page++ {
		for _, id := range hitIDs(search(t, pool, g, board.SearchQuery{Text: "gizmo", Page: page})) {
			if seen[id] {
				t.Errorf("post %d is on two pages", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != 60 {
		t.Errorf("the pages hold %d posts, want 60", len(seen))
	}
	if _, err := board.Search(context.Background(), pool, g, board.SearchQuery{Text: "gizmo", Page: 4}); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("page 4 = %v, want ErrNotFound", err)
	}
	// Both sorts page the same way.
	b := search(t, pool, g, board.SearchQuery{Text: "gizmo", Sort: "best", Page: 3})
	if len(b.Hits) != 10 {
		t.Errorf("best, page 3: %d hits", len(b.Hits))
	}
}

// The total stops at 1,000: a query never counts past page 40.
func TestSearchTotalCap(t *testing.T) {
	pool, _ := boardtest.Load(t, postsBoard(1100))
	g := guestFor(t, pool)
	p := search(t, pool, g, board.SearchQuery{Text: "gizmo", Page: 40})
	if p.Total != 1000 || p.Pages != 40 || p.Page != 40 || len(p.Hits) != 25 {
		t.Errorf("page 40: total %d, %d of %d, %d hits", p.Total, p.Page, p.Pages, len(p.Hits))
	}
	if _, err := board.Search(context.Background(), pool, g, board.SearchQuery{Text: "gizmo", Page: 41}); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("page 41 = %v, want ErrNotFound", err)
	}
	// The author-only query is capped the same way.
	p = search(t, pool, g, board.SearchQuery{Author: "bob"})
	if p.Total != 1000 || p.Pages != 40 {
		t.Errorf("author bob: total %d, %d pages", p.Total, p.Pages)
	}
}

// A search runs a fixed number of statements.
func TestSearchStatementCount(t *testing.T) {
	pool, _ := boardtest.Load(t, postsBoard(60))
	g := guestFor(t, pool)
	for _, c := range []struct {
		name string
		sq   board.SearchQuery
		want int
	}{
		// The statements: the query (parsed once), the forums, the author when
		// there is one, the total and the page.
		{"text", board.SearchQuery{Text: "gizmo"}, 4},
		{"text, best", board.SearchQuery{Text: "gizmo", Sort: "best", Page: 2}, 4},
		{"author", board.SearchQuery{Author: "bob"}, 4},
		{"text and author", board.SearchQuery{Text: "gizmo", Author: "bob"}, 5},
		{"no hits", board.SearchQuery{Text: "zzzzzz"}, 3},
		{"only a negation", board.SearchQuery{Text: "-gizmo"}, 1},
		{"empty", board.SearchQuery{}, 0},
	} {
		cnt := counting{pool: pool}
		if _, err := board.Search(context.Background(), &cnt, g, c.sq); err != nil && !errors.Is(err, board.ErrSearchNoWord) {
			t.Fatal(err)
		}
		if cnt.n != c.want {
			t.Errorf("%s: %d statements, want %d", c.name, cnt.n, c.want)
		}
	}
}

func TestSearchForums(t *testing.T) {
	pool, _ := boardtest.Load(t, searchBoard())
	ctx := context.Background()
	got, err := board.SearchForums(ctx, pool, memberFor(t, pool, 1))
	if err != nil {
		t.Fatal(err)
	}
	// Alice may search forums 2 and 3. Forum 3 is a child of forum 2.
	if len(got) != 2 || got[0].ID != 2 || got[1].ID != 3 || got[0].Name != "Main Discussion" || got[1].Name != "Members Lounge" {
		t.Fatalf("alice: %+v", got)
	}
	if got[0].Depth != 0 || got[1].Depth != 1 {
		t.Errorf("alice: depths %d and %d, want 0 and 1 (the category is not listed)", got[0].Depth, got[1].Depth)
	}
	got, err = board.SearchForums(ctx, pool, guestFor(t, pool))
	if err != nil || len(got) != 1 || got[0].ID != 2 {
		t.Errorf("guest: %+v, %v", got, err)
	}
	// forum.search without forum.read lists nothing.
	b := searchBoard()
	allow(b, 1, 3, "forum.search")
	pool2, _ := boardtest.Load(t, b)
	got, err = board.SearchForums(ctx, pool2, guestFor(t, pool2))
	if err != nil || len(got) != 1 || got[0].ID != 2 {
		t.Errorf("guest with forum.search on forum 3: %+v, %v", got, err)
	}
	// Bob may read forum 2 and search it; he has no forum.search on forum 3.
	got, err = board.SearchForums(ctx, pool, memberFor(t, pool, 2))
	if err != nil || len(got) != 1 || got[0].ID != 2 {
		t.Errorf("bob: %+v, %v", got, err)
	}
}

func TestSearchBadText(t *testing.T) {
	pool, _ := boardtest.Load(t, searchBoard())
	g := guestFor(t, pool)
	for _, sq := range []board.SearchQuery{
		{Text: "\xff"},
		{Text: "a\x00b"},
		{Author: "a\x00b"},
		{Author: "\xc3"},
		{Text: "example", Author: "\xff"},
		{Text: "ok\xe2\x82"},
	} {
		cnt := counting{pool: pool}
		_, err := board.Search(context.Background(), &cnt, g, sq)
		if !errors.Is(err, board.ErrBadSearch) {
			t.Errorf("Search(%q, %q) = %v, want ErrBadSearch", sq.Text, sq.Author, err)
		}
		if cnt.n != 0 {
			t.Errorf("Search(%q, %q) ran %d statements, want 0", sq.Text, sq.Author, cnt.n)
		}
	}
	// A viewer without the right still gets ErrForbidden first.
	b := bundletest.Small()
	pool2, _ := boardtest.Load(t, b)
	if _, err := board.Search(context.Background(), pool2, guestFor(t, pool2), board.SearchQuery{Text: "\xff"}); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("no right, bad text = %v, want ErrForbidden", err)
	}
}

// A query of more than 16 terms is refused: its cost grows with the terms.
func TestSearchTooManyTerms(t *testing.T) {
	pool, _ := boardtest.Load(t, searchBoard())
	g := guestFor(t, pool)
	words := strings.Fields("apple banana cherry damson elder fig grape hazel iris jasmine kale lemon mango nectar olive peach quince")
	if _, err := board.Search(context.Background(), pool, g, board.SearchQuery{Text: strings.Join(words[:16], " or ")}); err != nil {
		t.Errorf("16 terms = %v, want no error", err)
	}
	_, err := board.Search(context.Background(), pool, g, board.SearchQuery{Text: strings.Join(words, " or ")})
	if !errors.Is(err, board.ErrSearchTooManyTerms) || !errors.Is(err, board.ErrBadSearch) {
		t.Errorf("17 terms = %v, want ErrSearchTooManyTerms (an ErrBadSearch)", err)
	}
	// A stop word is no term.
	if _, err := board.Search(context.Background(), pool, g, board.SearchQuery{Text: "the and of " + strings.Join(words[:16], " ")}); err != nil {
		t.Errorf("16 terms and stop words = %v, want no error", err)
	}
}

// A query with no positive term would list every post without the word.
func TestSearchNoPositiveTerm(t *testing.T) {
	pool, _ := boardtest.Load(t, searchBoard())
	g := guestFor(t, pool)
	for _, sq := range []board.SearchQuery{
		{Text: "-example"},
		{Text: "-example -welcome"},
		{Text: "example or -welcome"},
		{Text: "-example", Author: "alice"},
		{Text: "\"-example\" or -zebra"},
	} {
		cnt := counting{pool: pool}
		_, err := board.Search(context.Background(), &cnt, g, sq)
		if !errors.Is(err, board.ErrSearchNoWord) {
			t.Errorf("Search(%q) = %v, want ErrSearchNoWord", sq.Text, err)
		}
		if cnt.n > 1 {
			t.Errorf("Search(%q) ran %d statements, want 1 (the parse)", sq.Text, cnt.n)
		}
	}
	// A positive term next to a negation is a search, and an author alone is too.
	if got := hitIDs(search(t, pool, g, board.SearchQuery{Text: "welcome -diagram"})); len(got) == 0 {
		t.Error("welcome -diagram finds nothing")
	}
	if got := hitIDs(search(t, pool, g, board.SearchQuery{Text: "example -zebra"})); !slices.Equal(got, []int64{1}) {
		t.Errorf("example -zebra = %v, want [1]", got)
	}
	if got := hitIDs(search(t, pool, g, board.SearchQuery{Author: "alice"})); !slices.Equal(got, []int64{1}) {
		t.Errorf("author alice = %v, want [1]", got)
	}
	// A query that is all stop words is an empty search, not an error.
	p := search(t, pool, g, board.SearchQuery{Text: "the of"})
	if len(p.Hits) != 0 || p.Pages != 1 {
		t.Errorf("stop words = %+v", p)
	}
}

// The forum of the post and the forum of its topic must both be readable. A post
// whose forum_id is out of step with its topic's never shows a hidden topic.
func TestSearchForumOutOfStep(t *testing.T) {
	pool, _ := boardtest.Load(t, searchBoard())
	ctx := context.Background()
	g := guestFor(t, pool)
	// Post 5 is in topic 2, which sits in forum 3 (hidden from guests).
	if _, err := pool.Exec(ctx, `update posts set forum_id = 2 where id in (5, 6)`); err != nil {
		t.Fatal(err)
	}
	for _, sq := range []board.SearchQuery{
		{Text: "color"}, {Text: "favorite"}, {Author: "bob"}, {Author: "alice"}, {Text: "color", Author: "bob"},
		{Text: "color", Sort: "best"}, {Text: "color", ForumID: 2},
	} {
		for _, id := range hitIDs(search(t, pool, g, sq)) {
			if id == 5 || id == 6 {
				t.Errorf("guest, %+v: finds post %d of a hidden topic", sq, id)
			}
		}
	}
	// A topic title in a hidden forum is no leak through the title arm either.
	if _, err := pool.Exec(ctx, `update topics set title = 'Quokka lounge' where id = 2`); err != nil {
		t.Fatal(err)
	}
	if got := hitIDs(search(t, pool, g, board.SearchQuery{Text: "quokka"})); len(got) != 0 {
		t.Errorf("quokka = %v, want none", got)
	}
	// The other way round: the post is in the hidden forum, its topic is readable.
	if _, err := pool.Exec(ctx, `update posts set forum_id = 3 where id = 2`); err != nil {
		t.Fatal(err)
	}
	if got := hitIDs(search(t, pool, g, board.SearchQuery{Text: "diagram"})); len(got) != 0 {
		t.Errorf("diagram = %v, want none (the post is in a hidden forum)", got)
	}
}

func TestSearchDeleted(t *testing.T) {
	b := searchBoard()
	b.Posts[1].Visibility = "deleted"  // post 2, "diagram"
	b.Topics[1].Visibility = "deleted" // topic 2: posts 5 and 6, "color"
	pool, _ := boardtest.Load(t, b)
	alice := memberFor(t, pool, 1)
	g := guestFor(t, pool)
	for _, v := range []board.Viewer{g, alice} {
		for _, sq := range []board.SearchQuery{{Text: "diagram"}, {Text: "color"}, {Text: "favorite"}, {Author: "bob"}} {
			if got := hitIDs(search(t, pool, v, sq)); len(got) != 0 {
				t.Errorf("viewer %d, %+v = %v, want none", v.UserID, sq, got)
			}
		}
	}
	// The rest of the topic still shows.
	if got := hitIDs(search(t, pool, g, board.SearchQuery{Text: "welcome"})); len(got) == 0 {
		t.Error("welcome finds nothing")
	}
}

// A generic plan has no value for the parameters, so it cannot fold the query.
// The statements give the same hits as under a custom plan.
func TestSearchGenericPlan(t *testing.T) {
	pool, _ := boardtest.Load(t, postsBoard(60))
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	g := guestFor(t, pool)
	var want [][]int64
	cases := []board.SearchQuery{
		{Text: "gizmo"}, {Text: "gizmo", Sort: "best", Page: 2}, {Text: "welcome"}, {Text: "gizmo or zebra"},
		{Text: "gizmo", Author: "bob"}, {Author: "bob"},
	}
	for _, sq := range cases {
		want = append(want, hitIDs(search(t, pool, g, sq)))
	}
	if _, err := conn.Exec(ctx, `set plan_cache_mode = force_generic_plan`); err != nil {
		t.Fatal(err)
	}
	for run := range 8 {
		for i, sq := range cases {
			got, err := board.Search(ctx, conn, g, sq)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(hitIDs(got), want[i]) {
				t.Errorf("run %d, %+v: generic plan gives %v, want %v", run, sq, hitIDs(got), want[i])
			}
		}
	}
}
