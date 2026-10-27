package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/internal/bundletest"
)

func TestAdminGroupsPages(t *testing.T) {
	h, _ := siteWithPool(t, bundletest.Small(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	list := bodyOf(t, getAs(t, h, "/admin/groups", alice))
	for _, want := range []string{`href="/admin/groups/5"`, "<bdi>Beta testers</bdi>", "On request", "Built in", `action="/admin/groups"`} {
		if !strings.Contains(list, want) {
			t.Errorf("the list lacks %q", want)
		}
	}
	beta := bodyOf(t, getAs(t, h, "/admin/groups/5", alice))
	for _, want := range []string{
		`action="/admin/groups/5/members/3/approve"`, `action="/admin/groups/5/members/3/refuse"`,
		`action="/admin/groups/5/members/2/unlead"`, `action="/admin/groups/5/members/2/remove"`,
		`name="kind"`, "needs your password",
	} {
		if !strings.Contains(beta, want) {
			t.Errorf("the page of Beta testers lacks %q", want)
		}
	}
	if strings.Contains(beta, `/admin/groups/5/delete"`) {
		t.Error("a group with members offers a delete")
	}
	mods := bodyOf(t, getAs(t, h, "/admin/groups/3", alice))
	if strings.Contains(mods, `name="kind"`) || strings.Contains(mods, `id="group-name"`) || strings.Contains(mods, "/delete") {
		t.Error("a built-in group offers a new name, a new kind or a delete")
	}
	if guests := bodyOf(t, getAs(t, h, "/admin/groups/1", alice)); strings.Contains(guests, `/admin/groups/1/members"`) {
		t.Error("the guests group offers an add")
	}
	for _, target := range []string{"/admin/groups/99", "/admin/groups/x", "/admin/groups/05"} {
		if rec := getAs(t, h, target, alice); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d", target, rec.Code)
		}
	}
	// A member who is not an admin sees the 404 page.
	bob := signIn(t, h, "bob", testPassword)
	if rec := getAs(t, h, "/admin/groups", bob); rec.Code != http.StatusNotFound {
		t.Errorf("the list for a moderator = %d", rec.Code)
	}
}

func TestAdminGroupsWrites(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	count := func(sql string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(t.Context(), sql).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Create, then a refusal that keeps the values as sent.
	rec, _ := postAs(t, h, "/admin/groups", alice, url.Values{"name": {"Artists"}, "kind": {"hidden"}, "description": {"Draw"}})
	if rec.Code != http.StatusSeeOther || !strings.HasSuffix(rec.Header().Get("Location"), "?done=created") {
		t.Fatalf("create = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if count(`select count(*) from groups where name = 'Artists' and kind = 'hidden'`) != 1 {
		t.Error("the group is not there")
	}
	rec, _ = postAs(t, h, "/admin/groups", alice, url.Values{"name": {"artists"}, "kind": {"open"}, "description": {"<b>x</b>"}})
	if b := bodyOf(t, rec); rec.Code != http.StatusBadRequest || !strings.Contains(b, "Another group has that name.") || !strings.Contains(b, "&lt;b&gt;x&lt;/b&gt;") {
		t.Errorf("a taken name = %d", rec.Code)
	}

	// A built-in group refuses a new name and takes a description.
	rec, _ = postAs(t, h, "/admin/groups/3", alice, url.Values{"name": {"mods"}, "description": {"x"}})
	if rec.Code != http.StatusConflict || !strings.Contains(bodyOf(t, rec), "keeps its name") {
		t.Errorf("a rename of a built-in group = %d", rec.Code)
	}
	rec, _ = postAs(t, h, "/admin/groups/3", alice, url.Values{"name": {"moderators"}, "description": {"The mods"}})
	if rec.Code != http.StatusSeeOther || count(`select count(*) from groups where id = 3 and description = 'The mods'`) != 1 {
		t.Errorf("a new description of a built-in group = %d", rec.Code)
	}

	// A change of the members needs a pass.
	add := url.Values{"name": {"dave"}, "back": {"/admin/groups/5"}}
	rec, _ = postAs(t, h, "/admin/groups/5/members", alice, add)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/admin/confirm?next=") {
		t.Fatalf("an add without a pass = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if count(`select count(*) from group_members where group_id = 5 and user_id = 4`) != 0 {
		t.Fatal("the add ran without a pass")
	}
	confirmPass(t, h, alice)
	rec, _ = postAs(t, h, "/admin/groups/5/members", alice, add)
	if rec.Code != http.StatusSeeOther || count(`select count(*) from group_members where group_id = 5 and user_id = 4 and not is_pending`) != 1 {
		t.Errorf("an add = %d", rec.Code)
	}
	rec, _ = postAs(t, h, "/admin/groups/5/members", alice, url.Values{"name": {"nobody"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(bodyOf(t, rec), "No member has that name.") {
		t.Errorf("an add of nobody = %d", rec.Code)
	}
	rec, _ = postAs(t, h, "/admin/groups/5/members/3/approve", alice, url.Values{})
	if rec.Code != http.StatusSeeOther || count(`select count(*) from group_members where group_id = 5 and user_id = 3 and not is_pending`) != 1 {
		t.Errorf("an approve = %d", rec.Code)
	}
	if rec, _ = postAs(t, h, "/admin/groups/5/members/3/promote", alice, url.Values{}); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown change = %d", rec.Code)
	}
	if rec, _ = postAs(t, h, "/admin/groups/1/members", alice, url.Values{"name": {"carol"}}); rec.Code != http.StatusConflict {
		t.Errorf("an add to the guests group = %d", rec.Code)
	}
	if n := count(`select count(*) from mod_log where action like 'group.%' and actor_id = 1`); n != 4 {
		t.Errorf("group rows in the log = %d, want 4", n)
	}

	// Delete: refused while in use, then done.
	if rec, _ = postAs(t, h, "/admin/groups/5/delete", alice, url.Values{}); rec.Code != http.StatusConflict {
		t.Errorf("a delete of a group in use = %d", rec.Code)
	}
	var id string
	if err := pool.QueryRow(t.Context(), `select id::text from groups where name = 'Artists'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	rec, _ = postAs(t, h, "/admin/groups/"+id+"/delete", alice, url.Values{})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/groups?done=deleted" || count(`select count(*) from groups where name = 'Artists'`) != 0 {
		t.Errorf("a delete = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
}
