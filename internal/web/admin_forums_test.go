package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/bundletest"
)

// forumPost sends a form as the cookies of a visitor. It returns the answer.
func forumPost(t *testing.T, h http.Handler, cookies []*http.Cookie, target string, form url.Values) *http.Response {
	t.Helper()
	rec, _ := postAs(t, h, target, cookies, form)
	return rec.Result()
}

// idFromLocation reads the forum ID in "#f12" at the end of a Location.
func idFromLocation(t *testing.T, loc string) int64 {
	t.Helper()
	_, frag, found := strings.Cut(loc, "#f")
	id, err := strconv.ParseInt(frag, 10, 64)
	if !found || err != nil {
		t.Fatalf("Location %q has no forum ID", loc)
	}
	return id
}

// queryInt runs a query that returns one integer.
func queryInt(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAdminForumsPage(t *testing.T) {
	h, _ := siteWithPool(t, bundletest.Small(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	confirmPass(t, h, alice)

	index := bodyOf(t, getAs(t, h, "/admin/", alice))
	if !strings.Contains(index, `href="/admin/forums"`) {
		t.Errorf("the admin index has no link to the forums:\n%s", index)
	}
	for _, target := range []string{"/admin/forums", "/admin/forums/new", "/admin/forums/2"} {
		rec := getAs(t, h, target, alice)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", target, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: Cache-Control %q", target, cc)
		}
		if !strings.Contains(bodyOf(t, rec), `<meta name="robots" content="noindex">`) {
			t.Errorf("%s has no robots meta", target)
		}
	}
	body := bodyOf(t, getAs(t, h, "/admin/forums", alice))
	for _, want := range []string{
		"General", "Main Discussion", "Members Lounge", ">Category<", ">Forum<",
		"1 topic", "mod.lock", "does not reach the subforums",
		`href="/admin/forums/new"`, `href="/admin/forums/2"`, `action="/admin/forums/2/reorder"`,
		`href="/admin/forums/new?parent=1"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the forum list lacks %q", want)
		}
	}
	// Nested lists: forum 3 sits inside forum 2, which sits inside forum 1.
	if !regexp.MustCompile(`(?s)id="f1".*id="f2".*id="f3"`).MatchString(body) {
		t.Error("the forums are not in tree order")
	}
	// The up button of the first forum under a parent is off, and the down button
	// of the last.
	if !regexp.MustCompile(`(?s)reorder">\s*<input[^>]*>\s*<input[^>]*value="up">\s*<button[^>]*disabled`).MatchString(body) {
		t.Error("the up button of the first forum is on")
	}
	if rec := getAs(t, h, "/admin/forums?done=created", alice); !strings.Contains(bodyOf(t, rec), "The forum was created.") {
		t.Error("the notice is missing")
	}
	if rec := getAs(t, h, "/admin/forums?done=<script>", alice); strings.Contains(bodyOf(t, rec), "<script>") || strings.Contains(bodyOf(t, rec), `role="status"`) {
		t.Error("an unknown notice shows")
	}
	for _, target := range []string{"/admin/forums/x", "/admin/forums/0", "/admin/forums/999", "/admin/forums/02"} {
		if rec := getAs(t, h, target, alice); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", target, rec.Code)
		}
	}
}

// TestAdminCreatesAForum: an admin creates a forum under General and copies the
// grants of Main Discussion. A member of a group that Main Discussion grants sees
// the forum in the index, and a guest too. A forum made with no source is seen by
// nobody.
func TestAdminCreatesAForum(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	confirmPass(t, h, alice)
	carol := signIn(t, h, "carol", testPassword)

	form := getAs(t, h, "/admin/forums/new?parent=1", alice)
	fb := bodyOf(t, form)
	if !regexp.MustCompile(`<option value="1" selected>General</option>`).MatchString(fb) ||
		strings.Count(fb, " selected>General</option>") != 2 {
		t.Errorf("the form does not default the parent and the source to the parent:\n%s", fb)
	}
	if !strings.Contains(fb, "nobody can see a forum") {
		t.Error("the form does not say that a forum without a grant is hidden")
	}

	rec := forumPost(t, h, alice, "/admin/forums/new", url.Values{
		"kind": {"forum"}, "name": {"  News  "}, "description": {"What is new"}, "rules": {"Be kind.\r\nBe brief."},
		"parent": {"1"}, "grants_from": {"2"},
	})
	if rec.StatusCode != http.StatusSeeOther || !strings.HasPrefix(rec.Header.Get("Location"), "/admin/forums?done=created#f") {
		t.Fatalf("create = %d to %q", rec.StatusCode, rec.Header.Get("Location"))
	}
	id := idFromLocation(t, rec.Header.Get("Location"))
	// A browser does not send the fragment.
	list := bodyOf(t, getAs(t, h, strings.Split(rec.Header.Get("Location"), "#")[0], alice))
	if !strings.Contains(list, "The forum was created.") || !strings.Contains(list, ">News</bdi>") {
		t.Errorf("the list after the create:\n%s", list)
	}
	var name, rules string
	if err := pool.QueryRow(t.Context(), `select name, rules from forums where id = $1`, id).Scan(&name, &rules); err != nil || name != "News" || rules != "Be kind.\nBe brief." {
		t.Errorf("row = %q %q (%v)", name, rules, err)
	}
	for who, cookies := range map[string][]*http.Cookie{"carol": carol, "a guest": nil} {
		body := bodyOf(t, getAs(t, h, "/", cookies))
		if !strings.Contains(body, `href="/f/`+strconv.FormatInt(id, 10)+`"`) || !strings.Contains(body, "News") {
			t.Errorf("%s does not see the new forum in the index", who)
		}
	}
	// The forum page renders as an imported forum: the rules show as text.
	if page := bodyOf(t, getAs(t, h, "/f/"+strconv.FormatInt(id, 10), carol)); !strings.Contains(page, "Be kind.\nBe brief.") {
		t.Errorf("the forum page lacks the rules:\n%s", page)
	}
	var who int64
	var action, details string
	if err := pool.QueryRow(t.Context(), `select actor_id, action, details::text from mod_log order by id desc limit 1`).Scan(&who, &action, &details); err != nil {
		t.Fatal(err)
	}
	if who != 1 || action != "forum.create" || !strings.Contains(details, `"via": "web"`) {
		t.Errorf("log = %d %s %s", who, action, details)
	}

	// No source: the form's first option, an empty value.
	rec = forumPost(t, h, alice, "/admin/forums/new", url.Values{"kind": {"forum"}, "name": {"Quiet"}, "parent": {"1"}, "grants_from": {""}})
	if rec.StatusCode != http.StatusSeeOther {
		t.Fatalf("create with no source = %d", rec.StatusCode)
	}
	if body := bodyOf(t, getAs(t, h, "/", carol)); strings.Contains(body, "Quiet") {
		t.Error("a member sees a forum with no grant")
	}
	// A link, and a top-level category.
	for _, f := range []url.Values{
		{"kind": {"link"}, "name": {"Wiki"}, "link_url": {"https://example.com/wiki"}, "parent": {"1"}, "grants_from": {"1"}},
		{"kind": {"category"}, "name": {"Archive"}, "grants_from": {""}},
	} {
		if rec := forumPost(t, h, alice, "/admin/forums/new", f); rec.StatusCode != http.StatusSeeOther {
			t.Errorf("create %v = %d", f, rec.StatusCode)
		}
	}
	if n := queryInt(t, pool, `select count(*) from forums where name in ('Wiki') and link_url = 'https://example.com/wiki' and kind = 'link'`); n != 1 {
		t.Error("the link is not stored")
	}
}

// TestAdminForumFormRefusals: a form that fails renders again with the values and
// a message beside the field, status 400, and changes nothing.
func TestAdminForumFormRefusals(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	confirmPass(t, h, alice)
	before := dumpData(t, pool)

	long := strings.Repeat("n", 101)
	for _, c := range []struct {
		name   string
		target string
		form   url.Values
		field  string
		msg    string
		keep   string
	}{
		{"an empty name", "/admin/forums/new", url.Values{"kind": {"forum"}, "name": {" "}, "description": {"keep <me>"}}, "forum-name-error", "Enter a name.", "keep &lt;me&gt;"},
		{"a long name", "/admin/forums/new", url.Values{"kind": {"forum"}, "name": {long}}, "forum-name-error", "longer than 100 characters", long[:50]},
		{"a bad kind", "/admin/forums/new", url.Values{"kind": {"board"}, "name": {"X"}}, "forum-kind-error", "Pick a kind", ""},
		{"a link with no address", "/admin/forums/new", url.Values{"kind": {"link"}, "name": {"X"}}, "forum-link-error", "Enter the address", ""},
		{"a link to a script", "/admin/forums/new", url.Values{"kind": {"link"}, "name": {"X"}, "link_url": {"javascript:alert(1)"}}, "forum-link-error", "starts with http://", "javascript:alert(1)"},
		{"a parent that is not a number", "/admin/forums/new", url.Values{"kind": {"forum"}, "name": {"X"}, "parent": {"abc"}}, "forum-parent-error", "Pick a parent", ""},
		{"a missing parent", "/admin/forums/new", url.Values{"kind": {"forum"}, "name": {"X"}, "parent": {"999"}}, "forum-parent-error", "Pick a parent", ""},
		{"a source that is not a number", "/admin/forums/new", url.Values{"kind": {"forum"}, "name": {"X"}, "grants_from": {"-1"}}, "forum-grants-error", "Pick a forum", ""},
		{"a missing source", "/admin/forums/new", url.Values{"kind": {"forum"}, "name": {"X"}, "grants_from": {"999"}}, "forum-grants-error", "Pick a forum", ""},
		{"a script in the rules", "/admin/forums/new", url.Values{"kind": {"forum"}, "name": {"X"}, "rules": {"<b>ok</b>\x07"}}, "forum-rules-error", "control characters", "&lt;b&gt;ok&lt;/b&gt;"},
		{"edit: an empty name", "/admin/forums/2/edit", url.Values{"kind": {"forum"}, "name": {""}, "rules": {"keep these rules"}}, "forum-name-error", "Enter a name.", "keep these rules"},
		{"edit: a forum with topics to a link", "/admin/forums/2/edit", url.Values{"kind": {"link"}, "name": {"X"}, "link_url": {"https://example.com/"}}, "forum-kind-error", "holds topics", ""},
		{"edit: a forum with topics to a category", "/admin/forums/3/edit", url.Values{"kind": {"category"}, "name": {"X"}}, "forum-kind-error", "holds topics", ""},
		{"edit: a forum with subforums to a link", "/admin/forums/1/edit", url.Values{"kind": {"link"}, "name": {"X"}, "link_url": {"https://example.com/"}}, "forum-kind-error", "has subforums", ""},
		{"move: under its own child", "/admin/forums/1/move", url.Values{"parent": {"3"}}, "move-parent-error", "cannot go under itself", ""},
		{"move: under itself", "/admin/forums/2/move", url.Values{"parent": {"2"}}, "move-parent-error", "cannot go under itself", ""},
		{"move: under a missing forum", "/admin/forums/3/move", url.Values{"parent": {"999"}}, "move-parent-error", "Pick a parent", ""},
		{"move: a parent that is not a number", "/admin/forums/3/move", url.Values{"parent": {"1e3"}}, "move-parent-error", "Pick a parent", ""},
		{"lock: a category", "/admin/forums/1/lock", url.Values{}, "", "Only a forum holds topics", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec, _ := postAs(t, h, c.target, alice, c.form)
			body := bodyOf(t, rec)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400\n%s", rec.Code, body)
			}
			if !strings.Contains(body, c.msg) || !strings.Contains(body, `role="alert"`) {
				t.Errorf("no message %q beside the field", c.msg)
			}
			if c.field != "" && (!strings.Contains(body, `id="`+c.field+`"`) || !strings.Contains(body, `aria-invalid="true"`)) {
				t.Errorf("the field %s is not marked", c.field)
			}
			if c.keep != "" && !strings.Contains(body, c.keep) {
				t.Errorf("the form lost %q", c.keep)
			}
			if strings.Contains(body, "<b>ok</b>") || strings.Contains(body, "keep <me>") {
				t.Error("the page echoes markup unescaped")
			}
		})
	}
	// Bad IDs in the path are 404, and a bad direction is 400 on the list.
	for _, target := range []string{"/admin/forums/999/edit", "/admin/forums/999/move", "/admin/forums/999/reorder", "/admin/forums/999/lock", "/admin/forums/999/unlock", "/admin/forums/x/edit"} {
		if rec, _ := postAs(t, h, target, alice, url.Values{"kind": {"forum"}, "name": {"X"}, "dir": {"up"}, "parent": {"1"}}); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", target, rec.Code)
		}
	}
	rec, _ := postAs(t, h, "/admin/forums/2/reorder", alice, url.Values{"dir": {"sideways"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(bodyOf(t, rec), "Pick up or down.") {
		t.Errorf("a bad direction = %d", rec.Code)
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("a refused form changed %v", diff)
	}
}

func TestAdminEditsAForum(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	confirmPass(t, h, alice)

	page := bodyOf(t, getAs(t, h, "/admin/forums/2", alice))
	for _, want := range []string{`value="Main Discussion"`, `action="/admin/forums/2/edit"`, `action="/admin/forums/2/move"`,
		`action="/admin/forums/2/lock"`, `href="/f/2"`, "mod.lock"} {
		if !strings.Contains(page, want) {
			t.Errorf("the edit page lacks %q", want)
		}
	}
	// The own forum and its subforums are not offered as a parent; the others are.
	moveSelect := regexp.MustCompile(`(?s)<select id="move-parent".*?</select>`).FindString(page)
	if strings.Contains(moveSelect, "Main Discussion") || !strings.Contains(moveSelect, ">General<") {
		t.Errorf("the parent list is wrong:\n%s", moveSelect)
	}

	rec := forumPost(t, h, alice, "/admin/forums/2/edit", url.Values{"kind": {"forum"}, "name": {"Main Talk"}, "description": {"Chat"}, "rules": {"Rule one."}})
	if rec.StatusCode != http.StatusSeeOther || rec.Header.Get("Location") != "/admin/forums/2?done=updated" {
		t.Fatalf("edit = %d to %q", rec.StatusCode, rec.Header.Get("Location"))
	}
	if body := bodyOf(t, getAs(t, h, "/admin/forums/2?done=updated", alice)); !strings.Contains(body, "The forum was saved.") || !strings.Contains(body, `value="Main Talk"`) || !strings.Contains(body, "Rule one.") {
		t.Errorf("the page after the edit:\n%s", body)
	}
	if n := queryInt(t, pool, `select count(*) from mod_log where action = 'forum.update' and target_id = 2 and details->>'via' = 'web' and actor_id = 1`); n != 1 {
		t.Errorf("%d forum.update rows", n)
	}
	// The same form again: no change, a notice, no row.
	rec = forumPost(t, h, alice, "/admin/forums/2/edit", url.Values{"kind": {"forum"}, "name": {"Main Talk"}, "description": {"Chat"}, "rules": {"Rule one."}})
	if rec.StatusCode != http.StatusSeeOther || rec.Header.Get("Location") != "/admin/forums/2?done=nochange" {
		t.Errorf("same edit = %d to %q", rec.StatusCode, rec.Header.Get("Location"))
	}
	if n := queryInt(t, pool, `select count(*) from mod_log where action = 'forum.update'`); n != 1 {
		t.Errorf("%d forum.update rows after a no-op", n)
	}
	// The forum page shows the new name, the description and the rules.
	fp := bodyOf(t, getAs(t, h, "/f/2", alice))
	if !strings.Contains(fp, "Main Talk") || !strings.Contains(fp, "Rule one.") {
		t.Error("the forum page lacks the edit")
	}
}

// TestAdminMovesAreSafe: a move that would make a cycle is refused and changes
// nothing; a good move shows on the next request of the index.
func TestAdminMovesAreSafe(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	confirmPass(t, h, alice)
	before := dumpData(t, pool)
	for _, c := range []struct{ id, parent string }{{"1", "2"}, {"1", "3"}, {"2", "3"}, {"2", "2"}} {
		rec, _ := postAs(t, h, "/admin/forums/"+c.id+"/move", alice, url.Values{"parent": {c.parent}})
		if rec.Code != http.StatusBadRequest || !strings.Contains(bodyOf(t, rec), "cannot go under itself or under one of its subforums") {
			t.Errorf("move %s under %s = %d", c.id, c.parent, rec.Code)
		}
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Fatalf("a refused move changed %v", diff)
	}

	// Forum 3 (Members Lounge) goes to the top level. The next request of the index
	// for an admin shows it there.
	rec := forumPost(t, h, alice, "/admin/forums/3/move", url.Values{"parent": {""}})
	if rec.StatusCode != http.StatusSeeOther || rec.Header.Get("Location") != "/admin/forums/3?done=moved" {
		t.Fatalf("move = %d to %q", rec.StatusCode, rec.Header.Get("Location"))
	}
	if n := queryInt(t, pool, `select count(*) from forums where id = 3 and parent_id is null`); n != 1 {
		t.Error("forum 3 has a parent")
	}
	if n := queryInt(t, pool, `select count(*) from mod_log where action = 'forum.move' and target_id = 3 and details->>'via' = 'web'`); n != 1 {
		t.Errorf("%d forum.move rows", n)
	}
	// Moving to the parent it has is no change.
	rec = forumPost(t, h, alice, "/admin/forums/3/move", url.Values{"parent": {""}})
	if rec.Header.Get("Location") != "/admin/forums/3?done=nochange" {
		t.Errorf("move to the same parent = %q", rec.Header.Get("Location"))
	}
	body := bodyOf(t, getAs(t, h, "/admin/forums", alice))
	if !regexp.MustCompile(`(?s)id="f1".*id="f2".*</li>\s*</ul>\s*</li>\s*<li id="f3"`).MatchString(body) {
		t.Errorf("forum 3 is not at the top level:\n%s", body)
	}
}

// TestAdminReordersForums: the up and down buttons rewrite the positions of the
// siblings, and no two share one.
func TestAdminReordersForums(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	confirmPass(t, h, alice)
	exec(t, pool, `insert into forums (id, parent_id, kind, name, position) values (20, 1, 'forum', 'Second', 5), (21, 1, 'forum', 'Third', 5)`)
	exec(t, pool, `update forums set position = 5 where id = 2`)

	order := func() string {
		rows, err := pool.Query(t.Context(), `select id from forums where parent_id = 1 order by position, id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, strconv.Itoa(id))
		}
		return strings.Join(ids, ",")
	}
	if got := order(); got != "2,20,21" {
		t.Fatalf("order = %s", got)
	}
	rec := forumPost(t, h, alice, "/admin/forums/2/reorder", url.Values{"dir": {"down"}})
	if rec.StatusCode != http.StatusSeeOther || rec.Header.Get("Location") != "/admin/forums?done=reordered#f2" {
		t.Fatalf("reorder = %d to %q", rec.StatusCode, rec.Header.Get("Location"))
	}
	if got := order(); got != "20,2,21" {
		t.Errorf("after down: %s", got)
	}
	if n := queryInt(t, pool, `select count(distinct position) from forums where parent_id = 1`); n != 3 {
		t.Errorf("%d distinct positions among 3 forums", n)
	}
	forumPost(t, h, alice, "/admin/forums/21/reorder", url.Values{"dir": {"up"}})
	if got := order(); got != "20,21,2" {
		t.Errorf("after up: %s", got)
	}
	// The first forum cannot go up: a notice, no row.
	rows := queryInt(t, pool, `select count(*) from mod_log where action = 'forum.reorder'`)
	rec = forumPost(t, h, alice, "/admin/forums/20/reorder", url.Values{"dir": {"up"}})
	if rec.Header.Get("Location") != "/admin/forums?done=nochange#f20" || queryInt(t, pool, `select count(*) from mod_log where action = 'forum.reorder'`) != rows {
		t.Errorf("first up = %q", rec.Header.Get("Location"))
	}
	if n := queryInt(t, pool, `select count(*) from mod_log where action = 'forum.reorder' and details->>'via' = 'web'`); n != 2 {
		t.Errorf("%d forum.reorder rows", n)
	}
}

func TestAdminLocksForums(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	confirmPass(t, h, alice)
	carol := signIn(t, h, "carol", testPassword)

	rec := forumPost(t, h, alice, "/admin/forums/2/lock", url.Values{})
	if rec.StatusCode != http.StatusSeeOther || rec.Header.Get("Location") != "/admin/forums/2?done=locked" {
		t.Fatalf("lock = %d to %q", rec.StatusCode, rec.Header.Get("Location"))
	}
	page := bodyOf(t, getAs(t, h, "/admin/forums/2?done=locked", alice))
	if !strings.Contains(page, "The forum is locked.") || !strings.Contains(page, `action="/admin/forums/2/unlock"`) || strings.Contains(page, `action="/admin/forums/2/lock"`) {
		t.Errorf("the page after the lock:\n%s", page)
	}
	if !strings.Contains(bodyOf(t, getAs(t, h, "/admin/forums", alice)), `tag-warn">Locked<`) {
		t.Error("the list does not show the lock")
	}
	// The lock shows on the board, and stops a reply from a member with no mod.lock.
	if !strings.Contains(bodyOf(t, getAs(t, h, "/f/2", carol)), "Locked") {
		t.Error("the forum page does not show the lock")
	}
	if strings.Contains(bodyOf(t, getAs(t, h, "/t/1", carol)), ">Reply</a>") {
		t.Error("a member sees the Reply button in a locked forum")
	}
	if rec := forumPost(t, h, alice, "/admin/forums/2/lock", url.Values{}); rec.Header.Get("Location") != "/admin/forums/2?done=nochange" {
		t.Errorf("lock twice = %q", rec.Header.Get("Location"))
	}
	rec = forumPost(t, h, alice, "/admin/forums/2/unlock", url.Values{})
	if rec.StatusCode != http.StatusSeeOther || rec.Header.Get("Location") != "/admin/forums/2?done=unlocked" {
		t.Fatalf("unlock = %d to %q", rec.StatusCode, rec.Header.Get("Location"))
	}
	if !strings.Contains(bodyOf(t, getAs(t, h, "/t/1", carol)), ">Reply</a>") {
		t.Error("a member sees no Reply button after the unlock")
	}
	for _, a := range []string{"forum.lock", "forum.unlock"} {
		if n := queryInt(t, pool, `select count(*) from mod_log where action = $1 and target_id = 2 and details->>'via' = 'web' and actor_id = 1`, a); n != 1 {
			t.Errorf("%d %s rows", n, a)
		}
	}
	// A category has no lock button.
	if strings.Contains(bodyOf(t, getAs(t, h, "/admin/forums/1", alice)), `action="/admin/forums/1/lock"`) {
		t.Error("a category has a lock button")
	}
}

// TestAdminForumWritesNeedAdmin: a member, a moderator and a guest get the 404
// page on every forum route, and the data stays. (The gate test walks the routes
// with a form that is not valid; this one sends valid forms.)
func TestAdminForumWritesNeedAdmin(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	visitors := map[string][]*http.Cookie{"carol": signIn(t, h, "carol", testPassword), "bob": signIn(t, h, "bob", testPassword), "a guest": nil}
	before := dumpData(t, pool)
	for who, cookies := range visitors {
		for target, form := range map[string]url.Values{
			"/admin/forums/new":          {"kind": {"forum"}, "name": {"Taken"}, "parent": {"1"}, "grants_from": {"2"}},
			"/admin/forums/2/edit":       {"kind": {"forum"}, "name": {"Taken"}},
			"/admin/forums/3/move":       {"parent": {"1"}},
			"/admin/forums/2/reorder":    {"dir": {"down"}},
			"/admin/forums/2/lock":       {},
			"/admin/forums/2/unlock":     {},
			"/admin/forums/new?parent=1": {},
		} {
			if rec, _ := postAs(t, h, target, cookies, form); rec.Code != http.StatusNotFound {
				t.Errorf("%s POST %s = %d, want 404", who, target, rec.Code)
			}
		}
		for _, target := range []string{"/admin/forums", "/admin/forums/new", "/admin/forums/2"} {
			if rec := getAs(t, h, target, cookies); rec.Code != http.StatusNotFound {
				t.Errorf("%s GET %s = %d, want 404", who, target, rec.Code)
			}
		}
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("a refused request changed %v", diff)
	}
}

func TestAdminForumsUnderBasePath(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{BasePath: "/forum/"})
	alice := signInAt(t, h, "/forum/", "alice")
	body := bodyOf(t, getAs(t, h, "/forum/admin/forums", alice))
	for _, want := range []string{`href="/forum/admin/forums/new"`, `action="/forum/admin/forums/2/reorder"`, `href="/forum/admin/forums/2"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the list lacks %q", want)
		}
	}
	rec, _ := postAsAt(t, h, "/forum/", "/forum/admin/forums/3/lock", alice, url.Values{})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/forum/admin/forums/3?done=locked" {
		t.Errorf("lock under the base path = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if n := queryInt(t, pool, `select count(*) from forums where id = 3 and locked`); n != 1 {
		t.Error("forum 3 is not locked")
	}
	if rec, _ := postAsAt(t, h, "/forum/", "/forum/admin/confirm", alice, url.Values{"password": {testPassword}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm under the base path = %d", rec.Code)
	}
	rec, _ = postAsAt(t, h, "/forum/", "/forum/admin/forums/new", alice, url.Values{"kind": {"forum"}, "name": {"X"}, "parent": {"1"}})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/forum/admin/forums?done=created#f") {
		t.Errorf("create under the base path = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
}

// TestAdminForumCreateNeedsPass: a new forum copies grants, so the create form
// asks for the password first, and a POST without a pass creates nothing.
func TestAdminForumCreateNeedsPass(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	page := bodyOf(t, getAs(t, h, "/admin/forums/new", alice))
	if !strings.Contains(page, `href="/admin/confirm?next=%2Fadmin%2Fforums%2Fnew"`) || strings.Contains(page, `name="name"`) {
		t.Error("the create page without a pass shows the form, or no link to the confirm page")
	}
	rec, _ := postAs(t, h, "/admin/forums/new", alice, url.Values{"kind": {"forum"}, "name": {"Sneaky"}, "back": {"/admin/forums/new"}})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/admin/confirm?") {
		t.Errorf("create without a pass = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	var n int
	if err := pool.QueryRow(t.Context(), `select count(*) from forums where name = 'Sneaky'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("the forum was created: %d, %v", n, err)
	}
	confirmPass(t, h, alice)
	if page := bodyOf(t, getAs(t, h, "/admin/forums/new", alice)); !strings.Contains(page, `name="name"`) {
		t.Error("the create page with a pass shows no form")
	}
}

// TestAdminForumMoveNeedsPass: a move changes who sees a forum in the index, so
// it needs a confirmed password.
func TestAdminForumMoveNeedsPass(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	rec, _ := postAs(t, h, "/admin/forums/3/move", alice, url.Values{"parent": {"1"}, "back": {"/admin/forums/3"}})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/admin/confirm?") {
		t.Errorf("a move without a pass = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if n := queryInt(t, pool, `select count(*) from forums where id = 3 and parent_id = 2`); n != 1 {
		t.Error("forum 3 moved without a pass")
	}
}
