package board_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// The Small board has forum 1 (a category), forum 2 under it, and forum 3 under 2.
// Forums 2 and 3 hold topics.

// forumFacts is the part of a forum row that the tests compare.
type forumFacts struct {
	Parent   int64
	Kind     string
	Name     string
	Link     string
	Locked   bool
	Position int
}

// factsOf reads every forum row, by ID.
func factsOf(t *testing.T, pool *pgxpool.Pool) map[int64]forumFacts {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`select id, coalesce(parent_id, 0), kind, name, coalesce(link_url, '<null>'), locked, position from forums`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]forumFacts{}
	for rows.Next() {
		var (
			id int64
			f  forumFacts
		)
		if err := rows.Scan(&id, &f.Parent, &f.Kind, &f.Name, &f.Link, &f.Locked, &f.Position); err != nil {
			t.Fatal(err)
		}
		out[id] = f
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// adminLogRow is the newest mod_log row.
type adminLogRow struct {
	Actor      int64
	Action     string
	TargetType string
	TargetID   int64
	Details    string
}

func lastAdminLog(t *testing.T, pool *pgxpool.Pool) adminLogRow {
	t.Helper()
	var r adminLogRow
	if err := pool.QueryRow(context.Background(),
		`select coalesce(actor_id, 0), action, target_type, target_id, details::text from mod_log order by id desc limit 1`).
		Scan(&r.Actor, &r.Action, &r.TargetType, &r.TargetID, &r.Details); err != nil {
		t.Fatal(err)
	}
	return r
}

// inputField returns the field of an *InputError, or "" when err is not one.
func inputField(err error) string {
	var in *board.InputError
	if errors.As(err, &in) {
		return in.Field
	}
	return ""
}

// forumIn is a valid input for a forum.
func forumIn(name string) board.ForumInput {
	return board.ForumInput{Kind: "forum", Name: name, ParentID: 1}
}

func TestAdminForumsReader(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	for _, v := range []board.Viewer{viewerFor(t, pool, bob), asGuest(t, pool)} {
		if _, err := board.AdminForums(ctx, pool, v); !errors.Is(err, board.ErrForbidden) {
			t.Errorf("user %d: %v, want ErrForbidden", v.UserID, err)
		}
	}
	// A new forum has no grant, so no member sees it, but the admin list shows it.
	id, err := board.CreateForum(ctx, pool, viewerFor(t, pool, alice), forumIn("Hidden"), t0)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := board.AdminForums(ctx, pool, viewerFor(t, pool, alice))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range board.Flatten(tree) {
		got = append(got, fmt.Sprintf("%d:%d:%t", f.ID, f.Depth, f.HasTopics))
	}
	want := []string{"1:0:false", "2:1:true", "3:2:true", fmt.Sprintf("%d:1:false", id)}
	if !slices.Equal(got, want) {
		t.Errorf("tree = %v, want %v", got, want)
	}
}

// TestCreateForumCopiesGrants: a member of a group that the source forum grants
// sees the new forum in the index, and a guest too when the guests group has it.
func TestCreateForumCopiesGrants(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	alice := viewerFor(t, pool, alice)
	// A user grant on the source: dave may never read forum 2. It copies too.
	var groupSrc, userSrc int
	for _, q := range []struct {
		sql string
		n   *int
	}{
		{`select count(*) from acl_group_grants where forum_id = 2`, &groupSrc},
		{`select count(*) from acl_user_grants where forum_id = 2`, &userSrc},
	} {
		if err := pool.QueryRow(ctx, q.sql).Scan(q.n); err != nil {
			t.Fatal(err)
		}
	}
	if groupSrc == 0 || userSrc == 0 {
		t.Fatalf("the fixture has %d group grants and %d user grants on forum 2", groupSrc, userSrc)
	}

	in := forumIn("News")
	in.Description = "What is new"
	in.GrantsFrom = 2
	id, err := board.CreateForum(ctx, pool, alice, in, t0)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		sql  string
		want int
	}{
		{`select count(*) from acl_group_grants where forum_id = $1`, groupSrc},
		{`select count(*) from acl_user_grants where forum_id = $1`, userSrc},
	} {
		var n int
		if err := pool.QueryRow(ctx, c.sql, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != c.want {
			t.Errorf("%s: %d rows, want %d", c.sql, n, c.want)
		}
	}
	// The copies match the source row for row.
	var differ int
	if err := pool.QueryRow(ctx, `
select count(*) from (
  (select group_id, role_id, option, setting from acl_group_grants where forum_id = 2
   except select group_id, role_id, option, setting from acl_group_grants where forum_id = $1)
  union all
  (select group_id, role_id, option, setting from acl_group_grants where forum_id = $1
   except select group_id, role_id, option, setting from acl_group_grants where forum_id = 2)) d`, id).Scan(&differ); err != nil || differ != 0 {
		t.Errorf("group grants differ from the source: %d rows (%v)", differ, err)
	}

	// carol is a member, and a guest has the guests group: both see the forum.
	for _, v := range []board.Viewer{viewerFor(t, pool, carol), asGuest(t, pool)} {
		tree, err := board.Index(ctx, pool, v)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, c := range tree[0].Children {
			names = append(names, c.Name)
		}
		if !slices.Equal(names, []string{"Main Discussion", "News"}) {
			t.Errorf("user %d sees %v under General", v.UserID, names)
		}
	}

	// The row, the position at the end, and the log row.
	f := factsOf(t, pool)[id]
	if f.Parent != 1 || f.Kind != "forum" || f.Name != "News" || f.Link != "<null>" || f.Locked || f.Position < 2 {
		t.Errorf("row = %+v", f)
	}
	var desc, rules string
	if err := pool.QueryRow(ctx, `select description, rules from forums where id = $1`, id).Scan(&desc, &rules); err != nil || desc != "What is new" {
		t.Errorf("description %q (%v)", desc, err)
	}
	l := lastAdminLog(t, pool)
	if l.Actor != 1 || l.Action != "forum.create" || l.TargetType != "forum" || l.TargetID != id ||
		!strings.Contains(l.Details, `"via": "web"`) || !strings.Contains(l.Details, `"grants_from": 2`) {
		t.Errorf("log = %+v", l)
	}

	// With no source, nobody sees the new forum.
	in = forumIn("Quiet")
	quiet, err := board.CreateForum(ctx, pool, alice, in, t0)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `select (select count(*) from acl_group_grants where forum_id = $1) + (select count(*) from acl_user_grants where forum_id = $1)`, quiet).Scan(&n); err != nil || n != 0 {
		t.Errorf("a forum with no source has %d grants (%v)", n, err)
	}
	tree, err := board.Index(ctx, pool, viewerFor(t, pool, carol))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range tree[0].Children {
		if c.Name == "Quiet" {
			t.Error("a member sees a forum that has no grant")
		}
	}
}

func TestCreateForumRefusals(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	admin := viewerFor(t, pool, alice)
	// A link is no parent.
	link, err := board.CreateForum(ctx, pool, admin, board.ForumInput{Kind: "link", Name: "Docs", LinkURL: "https://example.com/docs"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	before := factsOf(t, pool)
	logs := logCount(t, pool)

	tooLong := strings.Repeat("x", 101)
	for _, c := range []struct {
		name  string
		in    board.ForumInput
		field string
	}{
		{"no kind", board.ForumInput{Name: "A"}, "kind"},
		{"unknown kind", board.ForumInput{Kind: "board", Name: "A"}, "kind"},
		{"empty name", board.ForumInput{Kind: "forum", Name: "  "}, "name"},
		{"long name", board.ForumInput{Kind: "forum", Name: tooLong}, "name"},
		{"name with a line break", board.ForumInput{Kind: "forum", Name: "A\nB"}, "name"},
		{"name with a hidden character", board.ForumInput{Kind: "forum", Name: "A\u200bB"}, "name"},
		{"description with a line break", board.ForumInput{Kind: "forum", Name: "A", Description: "x\ny"}, "description"},
		{"long description", board.ForumInput{Kind: "forum", Name: "A", Description: strings.Repeat("d", 501)}, "description"},
		{"rules with a control character", board.ForumInput{Kind: "forum", Name: "A", Rules: "x\x07y"}, "rules"},
		{"long rules", board.ForumInput{Kind: "forum", Name: "A", Rules: strings.Repeat("r", 5001)}, "rules"},
		{"a link with no address", board.ForumInput{Kind: "link", Name: "A"}, "link_url"},
		{"a link to a script", board.ForumInput{Kind: "link", Name: "A", LinkURL: "javascript:alert(1)"}, "link_url"},
		{"a link by mail", board.ForumInput{Kind: "link", Name: "A", LinkURL: "mailto:a@example.com"}, "link_url"},
		{"a link to a site path", board.ForumInput{Kind: "link", Name: "A", LinkURL: "/f/1"}, "link_url"},
		{"a link to a host with no scheme", board.ForumInput{Kind: "link", Name: "A", LinkURL: "example.com"}, "link_url"},
		{"a link with a space", board.ForumInput{Kind: "link", Name: "A", LinkURL: "https://example.com/a b"}, "link_url"},
		{"a missing parent", board.ForumInput{Kind: "forum", Name: "A", ParentID: 999}, "parent"},
		{"a link as parent", board.ForumInput{Kind: "forum", Name: "A", ParentID: link}, "parent"},
		{"a missing source of grants", board.ForumInput{Kind: "forum", Name: "A", GrantsFrom: 999}, "grants_from"},
	} {
		if _, err := board.CreateForum(ctx, pool, admin, c.in, t0); inputField(err) != c.field {
			t.Errorf("%s: %v, want an input error on %q", c.name, err, c.field)
		}
	}
	// bob is a moderator and a guest is nobody: neither writes.
	for _, v := range []board.Viewer{viewerFor(t, pool, bob), asGuest(t, pool)} {
		if _, err := board.CreateForum(ctx, pool, v, forumIn("A"), t0); !errors.Is(err, board.ErrForbidden) {
			t.Errorf("user %d: %v, want ErrForbidden", v.UserID, err)
		}
	}
	if !equalFacts(before, factsOf(t, pool)) || logCount(t, pool) != logs {
		t.Error("a refused create changed something")
	}
	// A valid link, and rules saved in the form that the import stores.
	id, err := board.CreateForum(ctx, pool, admin, board.ForumInput{Kind: "link", Name: "Wiki", LinkURL: " HTTPS://example.com/wiki ",
		Rules: "ignored for a link? no: stored\r\nsecond line", ParentID: 1}, t0)
	if err != nil {
		t.Fatal(err)
	}
	var rules string
	if err := pool.QueryRow(ctx, `select rules from forums where id = $1`, id).Scan(&rules); err != nil || rules != "ignored for a link? no: stored\nsecond line" {
		t.Errorf("rules = %q (%v)", rules, err)
	}
	if f := factsOf(t, pool)[id]; f.Link != "HTTPS://example.com/wiki" || f.Kind != "link" {
		t.Errorf("link row = %+v", f)
	}
}

func equalFacts(a, b map[int64]forumFacts) bool {
	if len(a) != len(b) {
		return false
	}
	for id, f := range a {
		if b[id] != f {
			return false
		}
	}
	return true
}

func TestUpdateForum(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	admin := viewerFor(t, pool, alice)

	// A change of text: one row that names the fields.
	in := board.ForumInput{Kind: "forum", Name: "Main Talk", Description: "Chat", Rules: "One.\r\nTwo.\r\n"}
	if err := board.UpdateForum(ctx, pool, admin, 2, in, t0); err != nil {
		t.Fatal(err)
	}
	var name, desc, rules string
	if err := pool.QueryRow(ctx, `select name, description, rules from forums where id = 2`).Scan(&name, &desc, &rules); err != nil {
		t.Fatal(err)
	}
	if name != "Main Talk" || desc != "Chat" || rules != "One.\nTwo." {
		t.Errorf("row = %q %q %q", name, desc, rules)
	}
	l := lastAdminLog(t, pool)
	if l.Actor != 1 || l.Action != "forum.update" || l.TargetID != 2 || !strings.Contains(l.Details, `"via": "web"`) ||
		!strings.Contains(l.Details, `"fields": ["name", "description", "rules"]`) || !strings.Contains(l.Details, `"name_from": "Main Discussion"`) {
		t.Errorf("log = %+v", l)
	}
	// The same input again changes nothing and writes no row.
	logs := logCount(t, pool)
	if err := board.UpdateForum(ctx, pool, admin, 2, in, t0); !errors.Is(err, board.ErrNoChange) || logCount(t, pool) != logs {
		t.Errorf("same input: %v, rows %d, want ErrNoChange and %d", err, logCount(t, pool), logs)
	}
	if err := board.UpdateForum(ctx, pool, admin, 999, in, t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("missing forum: %v", err)
	}

	before := factsOf(t, pool)
	logs = logCount(t, pool)
	for _, c := range []struct {
		name  string
		id    int64
		in    board.ForumInput
		field string
	}{
		{"a forum with topics becomes a category", 2, board.ForumInput{Kind: "category", Name: "X"}, "kind"},
		{"a forum with topics becomes a link", 3, board.ForumInput{Kind: "link", Name: "X", LinkURL: "https://example.com/"}, "kind"},
		{"a forum with subforums becomes a link", 1, board.ForumInput{Kind: "link", Name: "X", LinkURL: "https://example.com/"}, "kind"},
		{"a bad name", 2, board.ForumInput{Kind: "forum", Name: ""}, "name"},
		{"a bad kind", 2, board.ForumInput{Kind: "x", Name: "X"}, "kind"},
	} {
		if err := board.UpdateForum(ctx, pool, admin, c.id, c.in, t0); inputField(err) != c.field {
			t.Errorf("%s: %v, want an input error on %q", c.name, err, c.field)
		}
	}
	for _, v := range []board.Viewer{viewerFor(t, pool, bob), asGuest(t, pool)} {
		if err := board.UpdateForum(ctx, pool, v, 2, board.ForumInput{Kind: "forum", Name: "Taken"}, t0); !errors.Is(err, board.ErrForbidden) {
			t.Errorf("user %d: %v", v.UserID, err)
		}
	}
	if !equalFacts(before, factsOf(t, pool)) || logCount(t, pool) != logs {
		t.Error("a refused update changed something")
	}

	// A forum with no topic changes kind in every direction, and the link address
	// follows the kind.
	id, err := board.CreateForum(ctx, pool, admin, forumIn("Empty"), t0)
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		in   board.ForumInput
		kind string
		link string
	}{
		{board.ForumInput{Kind: "category", Name: "Empty", LinkURL: "https://example.com/ignored"}, "category", "<null>"},
		{board.ForumInput{Kind: "link", Name: "Empty", LinkURL: "https://example.com/x"}, "link", "https://example.com/x"},
		{board.ForumInput{Kind: "forum", Name: "Empty"}, "forum", "<null>"},
	}
	for _, s := range steps {
		if err := board.UpdateForum(ctx, pool, admin, id, s.in, t0); err != nil {
			t.Fatal(err)
		}
		if f := factsOf(t, pool)[id]; f.Kind != s.kind || f.Link != s.link {
			t.Errorf("after %s: %+v", s.kind, f)
		}
	}
}

// TestUpdateKeepsAnImportedLink: an address that the import allows and the form
// does not (a site path) stays when the admin edits another field, and only the
// stored value passes.
func TestUpdateKeepsAnImportedLink(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	admin := viewerFor(t, pool, alice)
	if _, err := pool.Exec(ctx, `insert into forums (id, parent_id, kind, name, link_url, position) values (50, 1, 'link', 'Old', '/p/1', 7)`); err != nil {
		t.Fatal(err)
	}
	if err := board.UpdateForum(ctx, pool, admin, 50, board.ForumInput{Kind: "link", Name: "Renamed", LinkURL: "/p/1"}, t0); err != nil {
		t.Fatal(err)
	}
	if f := factsOf(t, pool)[50]; f.Name != "Renamed" || f.Link != "/p/1" {
		t.Errorf("row = %+v", f)
	}
	if err := board.UpdateForum(ctx, pool, admin, 50, board.ForumInput{Kind: "link", Name: "Renamed", LinkURL: "/p/2"}, t0); inputField(err) != "link_url" {
		t.Errorf("a new site path: %v", err)
	}
}

func TestMoveForum(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	admin := viewerFor(t, pool, alice)
	link, err := board.CreateForum(ctx, pool, admin, board.ForumInput{Kind: "link", Name: "Docs", LinkURL: "https://example.com/"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	before := factsOf(t, pool)
	logs := logCount(t, pool)

	// A cycle is refused, and nothing changes: under itself, under a child, under
	// a grandchild, and under a link.
	for _, c := range []struct {
		name       string
		id, parent int64
	}{
		{"1 under itself", 1, 1},
		{"1 under its child", 1, 2},
		{"1 under its grandchild", 1, 3},
		{"2 under its child", 2, 3},
		{"3 under a link", 3, link},
		{"3 under a missing forum", 3, 999},
	} {
		if err := board.MoveForum(ctx, pool, admin, c.id, c.parent, t0); inputField(err) != "parent" {
			t.Errorf("%s: %v, want an input error on parent", c.name, err)
		}
	}
	if err := board.MoveForum(ctx, pool, admin, 999, 1, t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("missing forum: %v", err)
	}
	if err := board.MoveForum(ctx, pool, admin, 3, 2, t0); !errors.Is(err, board.ErrNoChange) {
		t.Errorf("same parent: %v", err)
	}
	for _, v := range []board.Viewer{viewerFor(t, pool, bob), asGuest(t, pool)} {
		if err := board.MoveForum(ctx, pool, v, 3, 1, t0); !errors.Is(err, board.ErrForbidden) {
			t.Errorf("user %d: %v", v.UserID, err)
		}
	}
	if !equalFacts(before, factsOf(t, pool)) || logCount(t, pool) != logs {
		t.Fatal("a refused move changed something")
	}

	// A valid move: 3 goes to the top level, at the end; 2 keeps its place.
	if err := board.MoveForum(ctx, pool, admin, 3, 0, t0); err != nil {
		t.Fatal(err)
	}
	got := factsOf(t, pool)
	if got[3].Parent != 0 || got[3].Position <= got[link].Position {
		t.Errorf("3 = %+v, link = %+v", got[3], got[link])
	}
	l := lastAdminLog(t, pool)
	if l.Action != "forum.move" || l.TargetID != 3 || !strings.Contains(l.Details, `"from": 2`) || !strings.Contains(l.Details, `"to": 0`) || !strings.Contains(l.Details, `"via": "web"`) {
		t.Errorf("log = %+v", l)
	}
	// A subtree moves with its root: 2 (with the topics) goes under 3.
	if err := board.MoveForum(ctx, pool, admin, 2, 3, t0); err != nil {
		t.Fatal(err)
	}
	if got := factsOf(t, pool); got[2].Parent != 3 {
		t.Errorf("2 = %+v", got[2])
	}
}

// TestDepthLimit: the index lists a forum only when its chain of parents is short
// enough, so a create or a move that goes deeper is refused.
func TestDepthLimit(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	admin := viewerFor(t, pool, alice)
	// Forum 3 is at depth 2. A chain of 29 more reaches depth 31, the deepest the
	// index lists.
	parent := int64(3)
	var chain []int64
	for i := range 29 {
		id, err := board.CreateForum(ctx, pool, admin, board.ForumInput{Kind: "forum", Name: fmt.Sprintf("Level %d", i), ParentID: parent}, t0)
		if err != nil {
			t.Fatalf("level %d: %v", i, err)
		}
		chain = append(chain, id)
		parent = id
	}
	if _, err := board.CreateForum(ctx, pool, admin, board.ForumInput{Kind: "forum", Name: "Too deep", ParentID: parent}, t0); inputField(err) != "parent" {
		t.Errorf("create at depth 32: %v", err)
	}
	// Moving the whole chain under forum 3's depth is refused too: forum 1 with
	// its subtree under the end of a second chain would not fit.
	top, err := board.CreateForum(ctx, pool, admin, board.ForumInput{Kind: "forum", Name: "Other root"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := board.MoveForum(ctx, pool, admin, top, chain[len(chain)-1], t0); inputField(err) != "parent" {
		t.Errorf("move below the deepest: %v", err)
	}
	if err := board.MoveForum(ctx, pool, admin, top, chain[len(chain)-2], t0); err != nil {
		t.Errorf("move one level up from the deepest: %v", err)
	}
	// The subtree counts: a forum with a child fits under a parent at depth 29
	// (the child then sits at 31), and does not fit under a parent at depth 30.
	h, err := board.CreateForum(ctx, pool, admin, board.ForumInput{Kind: "forum", Name: "Root with a child"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := board.CreateForum(ctx, pool, admin, board.ForumInput{Kind: "forum", Name: "Child", ParentID: h}, t0); err != nil {
		t.Fatal(err)
	}
	if err := board.MoveForum(ctx, pool, admin, h, chain[len(chain)-2], t0); inputField(err) != "parent" {
		t.Errorf("move a subtree of height 1 under depth 30: %v", err)
	}
	if err := board.MoveForum(ctx, pool, admin, h, chain[len(chain)-3], t0); err != nil {
		t.Errorf("move a subtree of height 1 under depth 29: %v", err)
	}
}

// TestMovesAtOnceMakeNoCycle: two admins move two forums under each other at the
// same time. One move wins and the other is refused; the tree has no cycle.
func TestMovesAtOnceMakeNoCycle(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	admin := viewerFor(t, pool, alice)
	for round := range 20 {
		a, err := board.CreateForum(ctx, pool, admin, board.ForumInput{Kind: "forum", Name: fmt.Sprintf("A%d", round)}, t0)
		if err != nil {
			t.Fatal(err)
		}
		b, err := board.CreateForum(ctx, pool, admin, board.ForumInput{Kind: "forum", Name: fmt.Sprintf("B%d", round)}, t0)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			errs[0] = board.MoveForum(ctx, pool, admin, a, b, t0)
		}()
		go func() {
			defer wg.Done()
			<-start
			errs[1] = board.MoveForum(ctx, pool, admin, b, a, t0)
		}()
		close(start)
		wg.Wait()
		ok := 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case inputField(err) != "parent":
				t.Fatalf("round %d: %v", round, err)
			}
		}
		if ok != 1 {
			t.Fatalf("round %d: %d moves succeeded (%v), want 1", round, ok, errs)
		}
	}
	// No forum is its own ancestor.
	facts := factsOf(t, pool)
	for id := range facts {
		seen := 0
		for cur := id; cur != 0; cur = facts[cur].Parent {
			if seen++; seen > len(facts) {
				t.Fatalf("forum %d is in a cycle", id)
			}
		}
	}
	tree, err := board.AdminForums(ctx, pool, admin)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(board.Flatten(tree)); got != len(facts) {
		t.Errorf("the tree reaches %d of %d forums", got, len(facts))
	}
}

func TestReorderForum(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	admin := viewerFor(t, pool, alice)
	// Four siblings under forum 1, with the same position as the import may leave
	// them: 2 (from the fixture) and three more at position 5.
	var ids []int64
	for _, n := range []string{"B", "C", "D"} {
		id, err := board.CreateForum(ctx, pool, admin, forumIn(n), t0)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := pool.Exec(ctx, `update forums set position = 5 where parent_id = 1`); err != nil {
		t.Fatal(err)
	}
	order := func(unique bool) []int64 {
		rows, err := pool.Query(ctx, `select id, position from forums where parent_id = 1 order by position, id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []int64
		seen := map[int]bool{}
		for rows.Next() {
			var id int64
			var pos int
			if err := rows.Scan(&id, &pos); err != nil {
				t.Fatal(err)
			}
			if unique && seen[pos] {
				t.Errorf("two forums share position %d", pos)
			}
			seen[pos] = true
			out = append(out, id)
		}
		return out
	}
	// The ties break by ID.
	if got, want := order(false), []int64{2, ids[0], ids[1], ids[2]}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	// The first up and the last down are no change.
	logs := logCount(t, pool)
	if err := board.ReorderForum(ctx, pool, admin, 2, "up", t0); !errors.Is(err, board.ErrNoChange) {
		t.Errorf("first up: %v", err)
	}
	if err := board.ReorderForum(ctx, pool, admin, ids[2], "down", t0); !errors.Is(err, board.ErrNoChange) {
		t.Errorf("last down: %v", err)
	}
	if err := board.ReorderForum(ctx, pool, admin, 2, "sideways", t0); inputField(err) != "dir" {
		t.Errorf("sideways: %v", err)
	}
	if err := board.ReorderForum(ctx, pool, admin, 999, "up", t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	for _, v := range []board.Viewer{viewerFor(t, pool, bob), asGuest(t, pool)} {
		if err := board.ReorderForum(ctx, pool, v, 2, "down", t0); !errors.Is(err, board.ErrForbidden) {
			t.Errorf("user %d: %v", v.UserID, err)
		}
	}
	if logCount(t, pool) != logs {
		t.Error("a refused reorder wrote a row")
	}
	// Move 2 down twice, then the last one up once.
	for range 2 {
		if err := board.ReorderForum(ctx, pool, admin, 2, "down", t0); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := order(true), []int64{ids[0], ids[1], 2, ids[2]}; !slices.Equal(got, want) {
		t.Errorf("after two downs: %v, want %v", got, want)
	}
	if err := board.ReorderForum(ctx, pool, admin, ids[2], "up", t0); err != nil {
		t.Fatal(err)
	}
	if got, want := order(true), []int64{ids[0], ids[1], ids[2], 2}; !slices.Equal(got, want) {
		t.Errorf("after the up: %v, want %v", got, want)
	}
	l := lastAdminLog(t, pool)
	if l.Action != "forum.reorder" || l.TargetID != ids[2] || !strings.Contains(l.Details, `"dir": "up"`) || !strings.Contains(l.Details, `"via": "web"`) {
		t.Errorf("log = %+v", l)
	}
	// The positions are 1 to 4 now. Other parents are untouched.
	var min, max int
	if err := pool.QueryRow(ctx, `select min(position), max(position) from forums where parent_id = 1`).Scan(&min, &max); err != nil || min != 1 || max != 4 {
		t.Errorf("positions %d to %d (%v)", min, max, err)
	}
}

func TestSetForumLock(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	admin := viewerFor(t, pool, alice)
	locked := func(id int64) bool { return factsOf(t, pool)[id].Locked }

	for _, v := range []board.Viewer{viewerFor(t, pool, bob), asGuest(t, pool)} {
		if err := board.SetForumLock(ctx, pool, v, 2, true, t0); !errors.Is(err, board.ErrForbidden) {
			t.Errorf("user %d: %v", v.UserID, err)
		}
	}
	if locked(2) {
		t.Fatal("a refused lock locked the forum")
	}
	if err := board.SetForumLock(ctx, pool, admin, 2, true, t0); err != nil {
		t.Fatal(err)
	}
	if l := lastAdminLog(t, pool); !locked(2) || l.Action != "forum.lock" || l.TargetID != 2 || l.TargetType != "forum" || !strings.Contains(l.Details, `"via": "web"`) {
		t.Errorf("locked %v, log %+v", locked(2), l)
	}
	logs := logCount(t, pool)
	if err := board.SetForumLock(ctx, pool, admin, 2, true, t0); !errors.Is(err, board.ErrNoChange) || logCount(t, pool) != logs {
		t.Errorf("lock twice: %v", err)
	}
	// The lock is the one that stops a reply from a member with no mod.lock.
	if err := board.CanReply(ctx, pool, viewerFor(t, pool, carol), 1); !errors.Is(err, board.ErrForumLocked) {
		t.Errorf("CanReply into the locked forum: %v", err)
	}
	if err := board.SetForumLock(ctx, pool, admin, 2, false, t0); err != nil {
		t.Fatal(err)
	}
	if l := lastAdminLog(t, pool); locked(2) || l.Action != "forum.unlock" {
		t.Errorf("locked %v, log %+v", locked(2), l)
	}
	if err := board.SetForumLock(ctx, pool, admin, 2, false, t0); !errors.Is(err, board.ErrNoChange) {
		t.Errorf("unlock twice: %v", err)
	}
	// A category holds no topics: it cannot be locked. The import may have locked
	// one, and the admin can unlock it.
	if err := board.SetForumLock(ctx, pool, admin, 1, true, t0); inputField(err) != "locked" {
		t.Errorf("lock a category: %v", err)
	}
	if _, err := pool.Exec(ctx, `update forums set locked = true where id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := board.SetForumLock(ctx, pool, admin, 1, false, t0); err != nil || locked(1) {
		t.Errorf("unlock a category: %v", err)
	}
	if err := board.SetForumLock(ctx, pool, admin, 999, true, t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
}
