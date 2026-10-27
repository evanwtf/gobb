package board_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/markup"
)

// lastContentLog returns the newest mod_log row and its details as a map. It fails the
// test when the log is empty.
func lastContentLog(t *testing.T, pool *pgxpool.Pool) (modRow, map[string]any) {
	t.Helper()
	rows := modRows(t, pool)
	if len(rows) == 0 {
		t.Fatal("the log is empty")
	}
	last := rows[len(rows)-1]
	details := map[string]any{}
	if err := json.Unmarshal([]byte(last.Details), &details); err != nil {
		t.Fatal(err)
	}
	return last, details
}

// wantLog checks the newest log row: the actor is alice, and the details hold
// via "web" and every pair of want.
func wantLog(t *testing.T, pool *pgxpool.Pool, action, targetType string, target int64, want map[string]any) {
	t.Helper()
	row, details := lastContentLog(t, pool)
	if row.Actor == nil || *row.Actor != alice || row.Action != action || row.TargetType != targetType || row.Target != target {
		t.Errorf("log row = %+v, want %s on %s %d by alice", row, action, targetType, target)
	}
	if details["via"] != "web" {
		t.Errorf("details = %v, want via web", details)
	}
	for k, v := range want {
		got, _ := json.Marshal(details[k])
		wantJSON, _ := json.Marshal(v)
		if string(got) != string(wantJSON) {
			t.Errorf("details[%q] = %s, want %s", k, got, wantJSON)
		}
	}
}

// count reads one number from the database.
func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The three sources that pages in the tests use, with each flag set.
func pageSources() []bundle.StaticPage {
	rich := "# Rules\n\nBe **kind** and *calm* :) Visit https://example.com/rules today.\n\n" +
		"1. First\n2. Second\n\n> A quote\n\n```\ncode :D\n```\n\n[A link](https://example.com/a)\n\n" +
		"![An image](https://example.com/a.png)\n"
	page := func(slug string, md, sm, auto bool, src string) bundle.StaticPage {
		return bundle.StaticPage{Slug: slug, Title: strings.ToUpper(slug[:1]) + slug[1:],
			Body: bundle.Body{Source: src, Markdown: md, Smilies: sm, Autolink: auto}, UpdatedAt: t0}
	}
	return []bundle.StaticPage{
		page("all-on", true, true, true, rich),
		page("all-off", false, false, false, rich),
		page("plain-links", false, true, true, "Plain :) text with https://example.com/x and **stars**.\n\nTwo paragraphs :D\n"),
		page("md-only", true, false, false, rich+"\n"+strings.Repeat("A line with *emphasis* and a :) code.\n", 20)),
	}
}

// A page saved in the editor has the tree, the source and the flags that the
// import gives the same text with the same flags.
func TestAdminPageMatchesImport(t *testing.T) {
	ctx := context.Background()
	b := bundletest.Small()
	pages := pageSources()
	b.StaticPages = append(b.StaticPages, pages...)
	pool, _ := boardtest.Load(t, b)
	admin := viewerFor(t, pool, alice)
	for _, imported := range pages {
		copySlug := "copy-" + imported.Slug
		in := board.PageInput{Slug: copySlug, Title: imported.Title, Source: imported.Body.Source,
			Markdown: imported.Body.Markdown, Smilies: imported.Body.Smilies, Autolink: imported.Body.Autolink}
		if err := board.AdminCreatePage(ctx, pool, admin, in, t0); err != nil {
			t.Fatalf("%s: %v", imported.Slug, err)
		}
		var same bool
		err := pool.QueryRow(ctx, `
select a.body_tree = b.body_tree and a.body_source = b.body_source and a.title = b.title
   and a.enable_markdown = b.enable_markdown and a.enable_smilies = b.enable_smilies and a.enable_autolink = b.enable_autolink
  from static_pages a, static_pages b where a.slug = $1 and b.slug = $2`, imported.Slug, copySlug).Scan(&same)
		if err != nil || !same {
			t.Errorf("%s: the saved page differs from the imported page (%v)", imported.Slug, err)
		}
		// The same text, saved again as it is, changes nothing.
		if err := board.AdminUpdatePage(ctx, pool, admin, copySlug, in, t0); !errors.Is(err, board.ErrNoChange) {
			t.Errorf("%s: a save with no change = %v, want ErrNoChange", imported.Slug, err)
		}
	}
	// The trees are not empty: the smiley and the image are in the tree of "all-on".
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from static_pages where jsonb_path_exists(body_tree, '$.** ? (@.n == "smiley")')`).Scan(&n); err != nil || n < 2 {
		t.Errorf("%d pages hold a smiley element, want at least 2 (%v)", n, err)
	}
}

func TestAdminPageWrites(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	admin := viewerFor(t, pool, alice)
	in := board.PageInput{Slug: "faq", Title: "  Questions  ", Source: "Line one\r\nLine two :)", Markdown: true, Smilies: true, Autolink: true}

	if err := board.AdminCreatePage(ctx, pool, admin, in, t0); err != nil {
		t.Fatal(err)
	}
	f, err := board.AdminPage(ctx, pool, "faq")
	if err != nil {
		t.Fatal(err)
	}
	if f.Title != "Questions" || f.Source != "Line one\nLine two :)" || !f.Markdown || !f.Smilies || !f.Autolink || !f.UpdatedAt.Equal(t0) {
		t.Errorf("page = %+v", f)
	}
	wantLog(t, pool, "page.create", "page", 0, map[string]any{"slug": "faq", "title": "Questions"})

	// The page shows in the list for the footer.
	links, err := board.PageLinks(ctx, pool)
	if err != nil || len(links) != 3 {
		t.Errorf("links = %v, %v", links, err)
	}

	// A second page with the slug is refused, and so is every bad value. Nothing
	// changes: no page, no log row.
	pages, logs := count(t, pool, `select count(*) from static_pages`), count(t, pool, `select count(*) from mod_log`)
	long := func(n int) string { return strings.Repeat("a", n) }
	bad := []struct {
		name  string
		in    board.PageInput
		field string
	}{
		{"slug in use", board.PageInput{Slug: "faq", Title: "T", Source: "x"}, "slug"},
		{"slug in use, from the import", board.PageInput{Slug: "about", Title: "T", Source: "x"}, "slug"},
		{"slug empty", board.PageInput{Slug: "", Title: "T", Source: "x"}, "slug"},
		{"slug capitals", board.PageInput{Slug: "Rules", Title: "T", Source: "x"}, "slug"},
		{"slug underscore", board.PageInput{Slug: "a_b", Title: "T", Source: "x"}, "slug"},
		{"slug leading hyphen", board.PageInput{Slug: "-a", Title: "T", Source: "x"}, "slug"},
		{"slug slash", board.PageInput{Slug: "a/b", Title: "T", Source: "x"}, "slug"},
		{"slug space", board.PageInput{Slug: "a b", Title: "T", Source: "x"}, "slug"},
		{"slug 65 characters", board.PageInput{Slug: long(65), Title: "T", Source: "x"}, "slug"},
		{"slug reserved", board.PageInput{Slug: "new", Title: "T", Source: "x"}, "slug"},
		{"title empty", board.PageInput{Slug: "ok", Title: "  ", Source: "x"}, "title"},
		{"title control", board.PageInput{Slug: "ok", Title: "A\x01B", Source: "x"}, "title"},
		{"title newline", board.PageInput{Slug: "ok", Title: "A\nB", Source: "x"}, "title"},
		{"title 256 bytes", board.PageInput{Slug: "ok", Title: long(256), Source: "x"}, "title"},
		{"title not UTF-8", board.PageInput{Slug: "ok", Title: "A\xffB", Source: "x"}, "title"},
		{"body empty", board.PageInput{Slug: "ok", Title: "T", Source: " \n "}, "body"},
		{"body NUL", board.PageInput{Slug: "ok", Title: "T", Source: "a\x00b"}, "body"},
		{"body not UTF-8", board.PageInput{Slug: "ok", Title: "T", Source: "a\xffb"}, "body"},
		{"body over the limit", board.PageInput{Slug: "ok", Title: "T", Source: long(bundle.MaxSourceBytes + 1)}, "body"},
	}
	for _, tc := range bad {
		wantInput(t, board.AdminCreatePage(ctx, pool, admin, tc.in, t0), tc.field, "")
		if tc.name != "slug in use" && tc.name != "slug in use, from the import" {
			// The same bad title or body is refused on an update, too.
			if tc.field != "slug" {
				wantInput(t, board.AdminUpdatePage(ctx, pool, admin, "faq", tc.in, t0), tc.field, "")
			}
		}
	}
	if count(t, pool, `select count(*) from static_pages`) != pages || count(t, pool, `select count(*) from mod_log`) != logs {
		t.Error("a refused write changed the pages or the log")
	}
	// The limit is the one of a bundle: a text of exactly that size is saved.
	if err := board.AdminCreatePage(ctx, pool, admin, board.PageInput{Slug: "big", Title: "Big", Source: long(bundle.MaxSourceBytes)}, t0); err != nil {
		t.Errorf("a page of %d bytes: %v", bundle.MaxSourceBytes, err)
	}

	// Update: the title, the text and a flag. The slug stays.
	t1 := t0.Add(time.Hour)
	up := board.PageInput{Slug: "ignored", Title: "FAQ", Source: "New text", Markdown: false, Smilies: true, Autolink: true}
	if err := board.AdminUpdatePage(ctx, pool, admin, "faq", up, t1); err != nil {
		t.Fatal(err)
	}
	f, _ = board.AdminPage(ctx, pool, "faq")
	if f.Slug != "faq" || f.Title != "FAQ" || f.Source != "New text" || f.Markdown || !f.UpdatedAt.Equal(t1) {
		t.Errorf("page = %+v", f)
	}
	wantLog(t, pool, "page.update", "page", 0, map[string]any{"slug": "faq", "title": "FAQ", "fields": []string{"title", "source", "markdown"}})
	if err := board.AdminUpdatePage(ctx, pool, admin, "nowhere", up, t1); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("update of a missing page = %v", err)
	}
	logs = count(t, pool, `select count(*) from mod_log`)
	if err := board.AdminUpdatePage(ctx, pool, admin, "faq", up, t1); !errors.Is(err, board.ErrNoChange) {
		t.Errorf("update with no change = %v", err)
	}
	if count(t, pool, `select count(*) from mod_log`) != logs {
		t.Error("a save with no change wrote a log row")
	}

	// Delete.
	if err := board.AdminDeletePage(ctx, pool, admin, "faq", t1); err != nil {
		t.Fatal(err)
	}
	if _, err := board.Page(ctx, pool, "faq"); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("the deleted page reads as %v", err)
	}
	wantLog(t, pool, "page.delete", "page", 0, map[string]any{"slug": "faq", "title": "FAQ"})
	if err := board.AdminDeletePage(ctx, pool, admin, "faq", t1); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("delete of a missing page = %v", err)
	}
}

// A save of a page parses the text again with the smilies of the board now.
func TestAdminPageSaveRefreshesSmilies(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	admin := viewerFor(t, pool, alice)
	in := board.PageInput{Slug: "fun", Title: "Fun", Source: "Hello :P world", Markdown: true, Smilies: true, Autolink: true}
	if err := board.AdminCreatePage(ctx, pool, admin, in, t0); err != nil {
		t.Fatal(err)
	}
	hasSmiley := func() bool {
		return count(t, pool, `select count(*) from static_pages where slug = 'fun' and jsonb_path_exists(body_tree, '$.** ? (@.n == "smiley")')`) == 1
	}
	if hasSmiley() {
		t.Fatal("the page shows a smiley for a code that no smiley holds")
	}
	if _, err := board.AdminCreateSmiley(ctx, pool, admin, board.SmileyInput{Code: ":P", Description: "tongue"}, "smilies/s-0123456789abcdef.png", t0); err != nil {
		t.Fatal(err)
	}
	if err := board.AdminUpdatePage(ctx, pool, admin, "fun", in, t0); err != nil {
		t.Fatalf("save with the same text = %v", err)
	}
	if !hasSmiley() {
		t.Error("the page was not parsed with the new smiley")
	}
	wantLog(t, pool, "page.update", "page", 0, map[string]any{"fields": []string{"tree"}})
	// With smilies off in the page, the code stays text.
	in.Smilies = false
	if err := board.AdminUpdatePage(ctx, pool, admin, "fun", in, t0); err != nil {
		t.Fatal(err)
	}
	if hasSmiley() {
		t.Error("a page with smilies off holds a smiley")
	}
}

// Only an admin writes. Another member and a guest are refused, and nothing
// changes.
func TestAdminContentForbidden(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	logs := count(t, pool, `select count(*) from mod_log`)
	before := map[string]int{
		"pages":   count(t, pool, `select count(*) from static_pages`),
		"smilies": count(t, pool, `select count(*) from smilies`),
		"ranks":   count(t, pool, `select count(*) from ranks`),
	}
	for name, v := range map[string]board.Viewer{"bob": moderator(t, pool, bob), "carol": viewerFor(t, pool, carol), "guest": asGuest(t, pool)} {
		pin := board.PageInput{Slug: "x", Title: "X", Source: "x"}
		sin := board.SmileyInput{Code: ":x", Description: ""}
		rin := board.RankInput{Title: "X"}
		errs := map[string]error{
			"page.create":  board.AdminCreatePage(ctx, pool, v, pin, t0),
			"page.update":  board.AdminUpdatePage(ctx, pool, v, "about", pin, t0),
			"page.delete":  board.AdminDeletePage(ctx, pool, v, "about", t0),
			"smiley.move":  board.AdminMoveSmiley(ctx, pool, v, 1, false, t0),
			"rank.create":  nil,
			"smiley.make":  nil,
			"smiley.edit":  nil,
			"smiley.erase": nil,
			"rank.edit":    nil,
			"rank.erase":   nil,
		}
		_, errs["smiley.make"] = board.AdminCreateSmiley(ctx, pool, v, sin, "smilies/s-0123456789abcdef.png", t0)
		_, errs["smiley.edit"] = board.AdminUpdateSmiley(ctx, pool, v, 1, sin, "", t0)
		_, errs["smiley.erase"] = board.AdminDeleteSmiley(ctx, pool, v, 1, t0)
		_, errs["rank.create"] = board.AdminCreateRank(ctx, pool, v, rin, "", t0)
		_, errs["rank.edit"] = board.AdminUpdateRank(ctx, pool, v, 1, rin, "", false, t0)
		_, errs["rank.erase"] = board.AdminDeleteRank(ctx, pool, v, 1, t0)
		for what, err := range errs {
			if !errors.Is(err, board.ErrForbidden) {
				t.Errorf("%s: %s = %v, want ErrForbidden", name, what, err)
			}
		}
	}
	after := map[string]int{
		"pages":   count(t, pool, `select count(*) from static_pages`),
		"smilies": count(t, pool, `select count(*) from smilies`),
		"ranks":   count(t, pool, `select count(*) from ranks`),
	}
	for k, n := range before {
		if after[k] != n {
			t.Errorf("%s: %d rows before, %d after a refused write", k, n, after[k])
		}
	}
	if count(t, pool, `select count(*) from mod_log`) != logs {
		t.Error("a refused write wrote a log row")
	}
}

const (
	smileA = "smilies/s-0000000000000001.png"
	smileB = "smilies/s-0000000000000002.png"
)

func TestAdminSmileyWrites(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	admin := viewerFor(t, pool, alice)

	// Create: last in the list; the log names the code and the image.
	id, err := board.AdminCreateSmiley(ctx, pool, admin, board.SmileyInput{Code: "  :P ", Description: " tongue "}, smileA, t0)
	if err != nil {
		t.Fatal(err)
	}
	sm, err := board.AdminSmileyByID(ctx, pool, id)
	if err != nil || sm.Code != ":P" || sm.Description != "tongue" || sm.Image != smileA || sm.Position != 3 {
		t.Errorf("smiley = %+v, %v", sm, err)
	}
	wantLog(t, pool, "smiley.create", "smiley", id, map[string]any{"code": ":P", "image": smileA})

	// The renderer and the parser see it at once.
	lookup, err := board.SmileyLookup(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := lookup(":P"); !ok || got.Image != "/files/"+smileA || got.Alt != "tongue" {
		t.Errorf("lookup = %+v, %v", got, ok)
	}
	b, err := board.ParseBody(ctx, pool, admin, 2, "Hi :P", &markup.Options{Markdown: true, Smilies: true})
	if err != nil || !strings.Contains(string(b.JSON), `"code":":P"`) {
		t.Errorf("a post parsed after the add holds no smiley: %s, %v", b.JSON, err)
	}

	// Bad values change nothing.
	logs := count(t, pool, `select count(*) from mod_log`)
	for name, in := range map[string]board.SmileyInput{
		"code in use":           {Code: ":P"},
		"code in use, imported": {Code: ":)"},
		"code empty":            {Code: "  "},
		"code 33 characters":    {Code: strings.Repeat("x", 33)},
		"code control":          {Code: "a\tb"},
		"code newline":          {Code: ":\n)"},
		"code not UTF-8":        {Code: "a\xffb"},
		"description control":   {Code: ":ok", Description: "a\x01b"},
		"description too long":  {Code: ":ok", Description: strings.Repeat("d", 256)},
	} {
		_, err := board.AdminCreateSmiley(ctx, pool, admin, in, smileB, t0)
		wantInput(t, err, map[bool]string{true: "description", false: "code"}[strings.HasPrefix(name, "description")], "")
	}
	// A 32-character code is fine.
	if _, err := board.AdminCreateSmiley(ctx, pool, admin, board.SmileyInput{Code: strings.Repeat("y", 32)}, smileB, t0); err != nil {
		t.Errorf("a code of 32 characters: %v", err)
	}
	// A path that is not under smilies/ is a fault of the caller, not of the admin.
	for _, p := range []string{"", "ranks/x.png", "smilies/../x.png", "/smilies/x.png", "smilies/.hidden.png", "smilies/", "smilies\\x.png"} {
		if _, err := board.AdminCreateSmiley(ctx, pool, admin, board.SmileyInput{Code: ":path"}, p, t0); err == nil {
			t.Errorf("image path %q was accepted", p)
		}
	}
	if count(t, pool, `select count(*) from mod_log`) != logs+1 { // the 32-character code
		t.Error("a refused create wrote a log row")
	}

	// Update: code, description, image. The old image is freed when no row names it.
	freed, err := board.AdminUpdateSmiley(ctx, pool, admin, id, board.SmileyInput{Code: ":p", Description: "tongue out"}, smileB, t0)
	if err != nil || freed != smileA {
		t.Errorf("update: freed %q, %v; want %q", freed, err, smileA)
	}
	wantLog(t, pool, "smiley.update", "smiley", id, map[string]any{"code": ":p", "fields": []string{"code", "description", "image"}})
	if _, err := board.AdminUpdateSmiley(ctx, pool, admin, id, board.SmileyInput{Code: ":)"}, "", t0); err == nil {
		t.Error("a code in use was accepted on update")
	} else {
		wantInput(t, err, "code", "")
	}
	if _, err := board.AdminUpdateSmiley(ctx, pool, admin, id, board.SmileyInput{Code: ":p", Description: "tongue out"}, "", t0); !errors.Is(err, board.ErrNoChange) {
		t.Errorf("update with no change = %v", err)
	}
	if _, err := board.AdminUpdateSmiley(ctx, pool, admin, 9999, board.SmileyInput{Code: ":q"}, "", t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("update of a missing smiley = %v", err)
	}
	// An image that another smiley names too stays.
	other, err := board.AdminCreateSmiley(ctx, pool, admin, board.SmileyInput{Code: ":shared"}, smileB, t0)
	if err != nil {
		t.Fatal(err)
	}
	if freed, err := board.AdminUpdateSmiley(ctx, pool, admin, other, board.SmileyInput{Code: ":shared"}, smileA, t0); err != nil || freed != "" {
		t.Errorf("update of a shared image: freed %q, %v; want none", freed, err)
	}

	// Delete: the image is freed unless shared.
	if freed, err := board.AdminDeleteSmiley(ctx, pool, admin, id, t0); err != nil || freed != "" {
		t.Errorf("delete of a smiley with a shared image: freed %q, %v", freed, err)
	}
	wantLog(t, pool, "smiley.delete", "smiley", id, map[string]any{"code": ":p"})
	if freed, err := board.AdminDeleteSmiley(ctx, pool, admin, other, t0); err != nil || freed != smileA {
		t.Errorf("delete of the last smiley with the image: freed %q, %v; want %q", freed, err, smileA)
	}
	if _, err := board.AdminDeleteSmiley(ctx, pool, admin, other, t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("delete of a missing smiley = %v", err)
	}
	// An imported image that a rank names is not freed either.
	if _, err := board.AdminUpdateSmiley(ctx, pool, admin, 1, board.SmileyInput{Code: ":)", Description: "smile"}, smileA, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update ranks set image_path = 'smilies/smile.gif' where id = 1`); err != nil {
		t.Fatal(err)
	}
	if freed, err := board.AdminDeleteSmiley(ctx, pool, admin, 2, t0); err != nil || freed != "smilies/grin.png" {
		t.Errorf("delete of an imported smiley: freed %q, %v", freed, err)
	}
	if lookup, _ := board.SmileyLookup(ctx, pool); lookup != nil {
		if _, ok := lookup(":D"); ok {
			t.Error("the lookup still knows a deleted smiley")
		}
	}
}

func TestAdminSmileyMove(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	admin := viewerFor(t, pool, alice)
	order := func() string {
		list, err := board.AdminSmilies(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		var codes []string
		for _, s := range list {
			codes = append(codes, s.Code)
		}
		return strings.Join(codes, " ")
	}
	if got := order(); got != ":) :D" {
		t.Fatalf("order = %q", got)
	}
	if err := board.AdminMoveSmiley(ctx, pool, admin, 1, false, t0); err != nil {
		t.Fatal(err)
	}
	if got := order(); got != ":D :)" {
		t.Errorf("after down: %q", got)
	}
	wantLog(t, pool, "smiley.reorder", "smiley", 1, map[string]any{"code": ":)", "from": 1, "to": 2})
	logs := count(t, pool, `select count(*) from mod_log`)
	// At the ends a move does nothing and writes no row.
	for _, c := range []struct {
		id int64
		up bool
	}{{1, false}, {2, true}} {
		if err := board.AdminMoveSmiley(ctx, pool, admin, c.id, c.up, t0); !errors.Is(err, board.ErrNoChange) {
			t.Errorf("move %d up=%v at the end = %v", c.id, c.up, err)
		}
	}
	if err := board.AdminMoveSmiley(ctx, pool, admin, 99, true, t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("move of a missing smiley = %v", err)
	}
	if count(t, pool, `select count(*) from mod_log`) != logs {
		t.Error("a move with no effect wrote a log row")
	}
	// Equal positions from an import settle into 1, 2, 3 ... on a move.
	for _, c := range []string{":a", ":b", ":c"} {
		if _, err := board.AdminCreateSmiley(ctx, pool, admin, board.SmileyInput{Code: c}, smileA, t0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `update smilies set position = 7`); err != nil {
		t.Fatal(err)
	}
	if got := order(); got != ":) :D :a :b :c" {
		t.Fatalf("order with equal positions = %q", got)
	}
	if err := board.AdminMoveSmiley(ctx, pool, admin, 5, true, t0); err != nil { // :c up
		t.Fatal(err)
	}
	if got := order(); got != ":) :D :a :c :b" {
		t.Errorf("after :c up: %q", got)
	}
	if n := count(t, pool, `select count(distinct position) from smilies`); n != 5 {
		t.Errorf("%d distinct positions, want 5", n)
	}
}

const (
	rankA = "ranks/r-0000000000000001.png"
	rankB = "ranks/r-0000000000000002.png"
)

func TestAdminRankWrites(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	admin := viewerFor(t, pool, alice)

	id, err := board.AdminCreateRank(ctx, pool, admin, board.RankInput{Title: "  Regular ", MinPosts: 50}, rankA, t0)
	if err != nil {
		t.Fatal(err)
	}
	rk, err := board.AdminRankByID(ctx, pool, id)
	if err != nil || rk.Title != "Regular" || rk.MinPosts != 50 || rk.Special || rk.Image != rankA || rk.Members != 0 {
		t.Errorf("rank = %+v, %v", rk, err)
	}
	wantLog(t, pool, "rank.create", "rank", id, map[string]any{"title": "Regular", "min_posts": 50, "special": false, "image": rankA})
	// A rank with no image and a special rank.
	sid, err := board.AdminCreateRank(ctx, pool, admin, board.RankInput{Title: "Staff", MinPosts: 0, Special: true}, "", t0)
	if err != nil {
		t.Fatal(err)
	}
	if rk, _ := board.AdminRankByID(ctx, pool, sid); rk.Image != "" || !rk.Special {
		t.Errorf("special rank = %+v", rk)
	}
	if n := count(t, pool, `select count(*) from ranks where id = $1 and image_path is null`, sid); n != 1 {
		t.Error("a rank with no image stores an empty string, not null")
	}
	list, err := board.AdminRanks(ctx, pool)
	if err != nil || len(list) != 3 || list[0].Title != "Newcomer" || list[1].Title != "Regular" || list[2].Title != "Staff" {
		t.Errorf("ranks = %+v, %v", list, err)
	}
	if list[0].Members != 2 {
		t.Errorf("Newcomer: %d members, want 2", list[0].Members)
	}

	logs := count(t, pool, `select count(*) from mod_log`)
	for _, tc := range map[string]struct {
		in    board.RankInput
		field string
	}{
		"title empty":      {board.RankInput{Title: " "}, "title"},
		"title control":    {board.RankInput{Title: "a\x07b"}, "title"},
		"title newline":    {board.RankInput{Title: "a\nb"}, "title"},
		"title long":       {board.RankInput{Title: strings.Repeat("t", 256)}, "title"},
		"title not UTF-8":  {board.RankInput{Title: "a\xffb"}, "title"},
		"posts negative":   {board.RankInput{Title: "x", MinPosts: -1}, "min_posts"},
		"posts over int32": {board.RankInput{Title: "x", MinPosts: 1 << 31}, "min_posts"},
	} {
		_, err := board.AdminCreateRank(ctx, pool, admin, tc.in, "", t0)
		wantInput(t, err, tc.field, "")
		_, err = board.AdminUpdateRank(ctx, pool, admin, id, tc.in, "", false, t0)
		wantInput(t, err, tc.field, "")
	}
	if _, err := board.AdminCreateRank(ctx, pool, admin, board.RankInput{Title: "Max", MinPosts: 1<<31 - 1}, "", t0); err != nil {
		t.Errorf("the largest threshold: %v", err)
	}
	if _, err := board.AdminCreateRank(ctx, pool, admin, board.RankInput{Title: "x"}, "smilies/x.png", t0); err == nil {
		t.Error("an image path under smilies/ was accepted for a rank")
	}
	if count(t, pool, `select count(*) from mod_log`) != logs+1 {
		t.Error("a refused write wrote a log row")
	}

	// Update: fields, a new image (the old one is freed), the removal of the image.
	freed, err := board.AdminUpdateRank(ctx, pool, admin, id, board.RankInput{Title: "Veteran", MinPosts: 100, Special: false}, rankB, false, t0)
	if err != nil || freed != rankA {
		t.Errorf("update: freed %q, %v; want %q", freed, err, rankA)
	}
	wantLog(t, pool, "rank.update", "rank", id, map[string]any{"title": "Veteran", "fields": []string{"title", "min_posts", "image"}})
	freed, err = board.AdminUpdateRank(ctx, pool, admin, id, board.RankInput{Title: "Veteran", MinPosts: 100}, "", true, t0)
	if err != nil || freed != rankB {
		t.Errorf("remove the image: freed %q, %v; want %q", freed, err, rankB)
	}
	wantLog(t, pool, "rank.update", "rank", id, map[string]any{"fields": []string{"image"}})
	if _, err := board.AdminUpdateRank(ctx, pool, admin, id, board.RankInput{Title: "Veteran", MinPosts: 100}, "", true, t0); !errors.Is(err, board.ErrNoChange) {
		t.Errorf("update with no change = %v", err)
	}
	if _, err := board.AdminUpdateRank(ctx, pool, admin, 9999, board.RankInput{Title: "x"}, "", false, t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("update of a missing rank = %v", err)
	}
	// A new image wins over the removal.
	if _, err := board.AdminUpdateRank(ctx, pool, admin, id, board.RankInput{Title: "Veteran", MinPosts: 100}, rankA, true, t0); err != nil {
		t.Fatal(err)
	}
	if rk, _ := board.AdminRankByID(ctx, pool, id); rk.Image != rankA {
		t.Errorf("image = %q, want the new one", rk.Image)
	}
}

// Deleting a rank that members hold sets their rank to null.
func TestAdminRankDelete(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	admin := viewerFor(t, pool, alice)
	if n := count(t, pool, `select count(*) from users where rank_id = 1`); n != 2 {
		t.Fatalf("%d members hold rank 1, want 2", n)
	}
	freed, err := board.AdminDeleteRank(ctx, pool, admin, 1, t0)
	if err != nil || freed != "ranks/newcomer.png" {
		t.Errorf("delete: freed %q, %v", freed, err)
	}
	if n := count(t, pool, `select count(*) from users where rank_id is not null`); n != 0 {
		t.Errorf("%d members still name a rank", n)
	}
	if n := count(t, pool, `select count(*) from users`); n != 4 {
		t.Errorf("%d members after the delete, want 4", n)
	}
	wantLog(t, pool, "rank.delete", "rank", 1, map[string]any{"title": "Newcomer", "members": 2})
	if _, err := board.AdminDeleteRank(ctx, pool, admin, 1, t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("delete of a missing rank = %v", err)
	}
}

func TestValidContentPath(t *testing.T) {
	for p, want := range map[string]bool{
		"smilies/smile.gif":                   true,
		"smilies/s-0123456789abcdef.png":      true,
		"smilies/sub/x.gif":                   true,
		"smilies/":                            false,
		"smilies":                             false,
		"ranks/x.png":                         false,
		"smilies/../ranks/x.png":              false,
		"smilies/./x.png":                     false,
		"smilies//x.png":                      false,
		"/smilies/x.png":                      false,
		"smilies/.x.png":                      false,
		"smilies/sub/.x.png":                  false,
		"smilies/a\\b.png":                    false,
		"smilies/" + strings.Repeat("a", 250): false,
	} {
		if got := board.ValidContentPath("smilies", p); got != want {
			t.Errorf("ValidContentPath(smilies, %q) = %v, want %v", p, got, want)
		}
	}
}
