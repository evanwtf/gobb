package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/internal/bundletest"
)

func TestAdminPermsPages(t *testing.T) {
	h, _ := siteWithPool(t, bundletest.Small(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	index := bodyOf(t, getAs(t, h, "/admin/permissions", alice))
	for _, want := range []string{`href="/admin/permissions/0"`, `href="/admin/permissions/3"`, `class="depth-2"`, `href="/admin/permissions/members"`, `href="/admin/roles"`} {
		if !strings.Contains(index, want) {
			t.Errorf("the index lacks %q", want)
		}
	}
	// The preview, with the resolver of the pages.
	if b := bodyOf(t, getAs(t, h, "/admin/permissions?user=alice&forum=0", alice)); !strings.Contains(b, "<code>admin</code>") {
		t.Error("the preview of alice board-wide lacks admin")
	}
	if b := bodyOf(t, getAs(t, h, "/admin/permissions?user=carol&forum=0", alice)); strings.Contains(b, "<code>admin</code>") {
		t.Error("the preview of carol shows admin")
	}
	if rec := getAs(t, h, "/admin/permissions?user=nobody", alice); rec.Code != http.StatusBadRequest || !strings.Contains(bodyOf(t, rec), "No member has that name.") {
		t.Errorf("preview of nobody = %d", rec.Code)
	}
	// The grid of forum 3 offers the forum role, not the board role.
	grid := bodyOf(t, getAs(t, h, "/admin/permissions/3", alice))
	if !strings.Contains(grid, "Standard posting") || strings.Contains(grid, "Member account") || !strings.Contains(grid, "<code>topic.reply</code> Yes") {
		t.Errorf("the grid of forum 3 is wrong:\n%s", grid)
	}
	if board := bodyOf(t, getAs(t, h, "/admin/permissions/0", alice)); !strings.Contains(board, "Member account") || strings.Contains(board, `value="forum.read"`) {
		t.Error("the board-wide grid offers the wrong roles or options")
	}
	for _, target := range []string{"/admin/permissions/99", "/admin/permissions/x", "/admin/roles/99", "/admin/permissions/-1"} {
		if rec := getAs(t, h, target, alice); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d", target, rec.Code)
		}
	}
	members := bodyOf(t, getAs(t, h, "/admin/permissions/members", alice))
	if !strings.Contains(members, `<a href="/u/4"><bdi>dave</bdi></a>`) || !strings.Contains(members, "<code>forum.read</code> Never") {
		t.Errorf("the member grants lack dave's Never:\n%s", members)
	}
	role := bodyOf(t, getAs(t, h, "/admin/roles/1", alice))
	if !strings.Contains(role, `name="o:topic.start"`) || strings.Contains(role, `name="o:admin"`) {
		t.Error("the editor of a forum role shows the wrong options")
	}
}

func TestAdminPermsWrites(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	give := url.Values{"group": {"5"}, "role": {"1"}, "on": {"1"}, "back": {"/admin/permissions/3"}}
	// Without a pass: to the confirm page, and no change.
	rec, _ := postAs(t, h, "/admin/permissions/3/role", alice, give)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/admin/confirm?next=") {
		t.Fatalf("without a pass = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	count := func(sql string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(t.Context(), sql).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(`select count(*) from acl_group_grants where group_id = 5 and role_id = 1`) != 0 {
		t.Fatal("the grant ran without a pass")
	}
	confirmPass(t, h, alice)
	for _, c := range []struct {
		target string
		form   url.Values
		where  string
		action string
	}{
		{"/admin/permissions/3/role", give, "/admin/permissions/3?done=saved", "perm.grant"},
		{"/admin/permissions/3/role", give, "/admin/permissions/3?done=nochange", ""},
		{"/admin/permissions/3/option", url.Values{"group": {"5"}, "option": {"poll.vote"}, "setting": {"never"}}, "/admin/permissions/3?done=saved", "perm.grant"},
		{"/admin/permissions/members/add", url.Values{"name": {"Carol"}, "forum": {"3"}, "option": {"topic.start"}, "setting": {"yes"}}, "/admin/permissions/members?done=saved", "perm.grant"},
		{"/admin/permissions/members/add", url.Values{"name": {"carol"}, "forum": {"0"}, "role": {"2"}}, "/admin/permissions/members?done=saved", "perm.grant"},
		{"/admin/permissions/revoke", url.Values{"kind": {"user"}, "grant": {"1"}, "back": {"/admin/permissions/members?x=1"}}, "/admin/permissions/members?done=saved", "perm.revoke"},
		{"/admin/roles/1", url.Values{"o:topic.start": {"yes"}, "o:topic.reply": {"yes"}, "o:post.edit_own": {"yes"}, "o:post.markdown": {"yes"}, "o:post.smilies": {"never"}}, "/admin/roles/1?done=saved", "role.update"},
	} {
		before := len(modLog(t, pool))
		rec, _ := postAs(t, h, c.target, alice, c.form)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != c.where {
			t.Errorf("POST %s = %d to %q, want %q\n%.400s", c.target, rec.Code, rec.Header().Get("Location"), c.where, bodyOf(t, rec))
			continue
		}
		rows := modLog(t, pool)
		switch {
		case c.action == "" && len(rows) != before:
			t.Errorf("POST %s wrote a row for no change", c.target)
		case c.action != "" && (len(rows) != before+1 || rows[len(rows)-1].Action != c.action || rows[len(rows)-1].Actor != 1):
			t.Errorf("POST %s logged %v", c.target, rows[before:])
		}
	}
	if count(`select count(*) from acl_user_grants where user_id = 3 and forum_id = 3 and option = 'topic.start' and setting = 3`) != 1 ||
		count(`select count(*) from acl_user_grants where user_id = 4`) != 0 ||
		count(`select count(*) from acl_role_settings where role_id = 1 and option = 'post.smilies' and setting = 2`) != 1 {
		t.Error("the writes did not land")
	}
	// Refusals: a grant that does not fit, a bad form, an unknown member.
	for _, c := range []struct {
		target string
		form   url.Values
		status int
		want   string
	}{
		{"/admin/permissions/3/option", url.Values{"group": {"5"}, "option": {"admin"}, "setting": {"yes"}}, http.StatusBadRequest, "does not fit here"},
		{"/admin/permissions/0/role", url.Values{"group": {"5"}, "role": {"1"}, "on": {"1"}}, http.StatusBadRequest, "does not fit here"},
		{"/admin/permissions/3/option", url.Values{"group": {"5"}, "option": {"poll.vote"}, "setting": {"maybe"}}, http.StatusBadRequest, "Pick a group"},
		{"/admin/permissions/members/add", url.Values{"name": {"nobody"}, "option": {"poll.vote"}, "setting": {"yes"}}, http.StatusBadRequest, "No member has that name."},
		{"/admin/permissions/members/add", url.Values{"name": {"carol"}, "option": {"poll.vote"}, "setting": {"unset"}}, http.StatusBadRequest, "Pick a role"},
		{"/admin/roles/1", url.Values{"o:topic.start": {"maybe"}}, http.StatusBadRequest, "Each option must be"},
		{"/admin/roles/1", url.Values{"o:admin": {"yes"}}, http.StatusBadRequest, "does not fit here"},
		{"/admin/permissions/revoke", url.Values{"kind": {"user"}, "grant": {"999"}}, http.StatusBadRequest, "does not exist"},
	} {
		rec, _ := postAs(t, h, c.target, alice, c.form)
		if b := bodyOf(t, rec); rec.Code != c.status || !strings.Contains(b, c.want) {
			t.Errorf("POST %s %v = %d, lacks %q", c.target, c.form, rec.Code, c.want)
		}
	}
}

func TestAdminPermsKeepAnAdmin(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	exec(t, pool, `update users set is_owner = false where id = 1`)
	confirmPass(t, h, alice)
	rec, _ := postAs(t, h, "/admin/permissions/0/option", alice, url.Values{"group": {"4"}, "option": {"admin"}, "setting": {"unset"}})
	if rec.Code != http.StatusConflict || !strings.Contains(bodyOf(t, rec), "at least one active admin") {
		t.Errorf("remove the last admin = %d", rec.Code)
	}
	var n int
	if err := pool.QueryRow(t.Context(), `select count(*) from acl_group_grants where group_id = 4 and option = 'admin'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("the admin grant is gone: %d, %v", n, err)
	}
}
