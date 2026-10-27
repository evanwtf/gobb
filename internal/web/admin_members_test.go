package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/internal/bundletest"
)

// confirmPass posts the password of the member of cookies to the confirm page,
// so the session holds a pass.
func confirmPass(t *testing.T, h http.Handler, cookies []*http.Cookie) {
	t.Helper()
	if rec, _ := postAs(t, h, "/admin/confirm", cookies, url.Values{"password": {testPassword}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm = %d", rec.Code)
	}
}

func TestAdminMembersSearch(t *testing.T) {
	h, _ := siteWithPool(t, bundletest.Small(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	all := bodyOf(t, getAs(t, h, "/admin/members", alice))
	for _, want := range []string{`href="/admin/members/4"`, "dave", "Inactive", "carol@example.com"} {
		if !strings.Contains(all, want) {
			t.Errorf("the list lacks %q", want)
		}
	}
	found := bodyOf(t, getAs(t, h, "/admin/members?q=car", alice))
	if !strings.Contains(found, `href="/admin/members/3"`) || strings.Contains(found, `href="/admin/members/2"`) {
		t.Error("the search for car does not find carol alone")
	}
	if b := bodyOf(t, getAs(t, h, "/admin/members?q=zzz", alice)); !strings.Contains(b, "No member matches.") {
		t.Error("no match shows no notice")
	}
	if rec := getAs(t, h, "/admin/members?page=5", alice); rec.Code != http.StatusNotFound {
		t.Errorf("page 5 = %d", rec.Code)
	}
	if rec := getAs(t, h, "/admin/members/999", alice); rec.Code != http.StatusNotFound {
		t.Errorf("member 999 = %d", rec.Code)
	}
}

func TestAdminMemberWrites(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	state := func(id string) (s string, rank, verified string) {
		t.Helper()
		if err := pool.QueryRow(t.Context(), `select state, coalesce(rank_id::text, ''), email_verified::text from users where id = `+id).Scan(&s, &rank, &verified); err != nil {
			t.Fatal(err)
		}
		return s, rank, verified
	}
	post := func(target string, form url.Values) *httpRecorder {
		t.Helper()
		rec, _ := postAs(t, h, target, alice, form)
		return rec
	}
	for _, c := range []struct {
		target string
		form   url.Values
		done   string
		action string
	}{
		{"/admin/members/3/deactivate", nil, "deactivate", "user.deactivate"},
		{"/admin/members/3/activate", nil, "activate", "user.activate"},
		{"/admin/members/4/activate", nil, "activate", "user.activate"},
		{"/admin/members/3/rank", url.Values{"rank": {"1"}}, "rank", "user.rank"},
		{"/admin/members/3/verify-email", nil, "verify", "user.verify_email"},
	} {
		rec := post(c.target, c.form)
		id := strings.Split(c.target, "/")[3]
		if want := "/admin/members/" + id + "?done=" + c.done; rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
			t.Errorf("POST %s = %d to %q", c.target, rec.Code, rec.Header().Get("Location"))
		}
		rows := modLog(t, pool)
		if last := rows[len(rows)-1]; last.Actor != 1 || last.Action != c.action || last.Target != "user" {
			t.Errorf("POST %s logged %+v", c.target, last)
		}
	}
	if s, rank, verified := state("3"); s != "active" || rank != "1" || verified != "true" {
		t.Errorf("carol = %s, rank %q, verified %s", s, rank, verified)
	}
	if s, _, _ := state("4"); s != "active" {
		t.Errorf("dave = %s", s)
	}
	// The same change again changes nothing, and says so.
	if rec := post("/admin/members/3/verify-email", nil); rec.Header().Get("Location") != "/admin/members/3?done=nochange" {
		t.Errorf("verify again goes to %q", rec.Header().Get("Location"))
	}
	if b := bodyOf(t, getAs(t, h, "/admin/members/3?done=nochange", alice)); !strings.Contains(b, "Nothing changed") {
		t.Error("the page shows no notice for nochange")
	}
	// A bad rank: the page again, with the message at the list.
	for _, rank := range []string{"x", "-1", "999"} {
		rec := post("/admin/members/3/rank", url.Values{"rank": {rank}})
		if b := bodyOf(t, rec); rec.Code != http.StatusBadRequest || !strings.Contains(b, `id="m-rank-error"`) {
			t.Errorf("rank %q = %d", rank, rec.Code)
		}
	}
	// Her own account: no deactivate or reset button, and the POST is refused.
	own := bodyOf(t, getAs(t, h, "/admin/members/1", alice))
	if strings.Contains(own, "/admin/members/1/deactivate") || strings.Contains(own, "/admin/members/1/reset-password") {
		t.Error("the own page has deactivate or reset buttons")
	}
	confirmPass(t, h, alice)
	if rec := post("/admin/members/1/deactivate", nil); rec.Code != http.StatusForbidden || !strings.Contains(bodyOf(t, rec), "your own account") {
		t.Errorf("deactivate self = %d", rec.Code)
	}
}

var shownPassword = regexp.MustCompile(`<code class="new-password">([A-Za-z0-9]{16})</code>`)

func TestAdminResetPasswordPage(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	bob := signIn(t, h, "bob", testPassword)
	form := url.Values{"back": {"/admin/members/2"}}
	// Without a pass, the reset sends alice to the confirm page and does nothing.
	rec, _ := postAs(t, h, "/admin/members/2/reset-password", alice, form)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/confirm?next=%2Fadmin%2Fmembers%2F2" {
		t.Fatalf("reset without a pass = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if rows := modLog(t, pool); len(rows) > 0 && rows[len(rows)-1].Action == "user.reset_password" {
		t.Fatal("a reset without a pass ran")
	}
	confirmPass(t, h, alice)
	rec, _ = postAs(t, h, "/admin/members/2/reset-password", alice, form)
	body := bodyOf(t, rec)
	m := shownPassword.FindStringSubmatch(body)
	if rec.Code != http.StatusOK || m == nil {
		t.Fatalf("reset = %d, shows no password", rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("headers = %v", rec.Header())
	}
	// bob's session ended, and the new password signs him in.
	if b := bodyOf(t, getAs(t, h, "/", bob)); strings.Contains(b, "Signed in as") {
		t.Error("bob's session survived the reset")
	}
	signIn(t, h, "bob", m[1])
	// The page shows the password on the answer to the POST only.
	if strings.Contains(bodyOf(t, getAs(t, h, "/admin/members/2", alice)), m[1]) {
		t.Error("the page shows the password again")
	}
	rows := modLog(t, pool)
	if last := rows[len(rows)-1]; last.Actor != 1 || last.Action != "user.reset_password" || last.ID != 2 {
		t.Errorf("logged %+v", last)
	}
}

// TestAdminMemberAdminTargets: a change to an admin needs a pass, and an admin
// who is not an owner cannot reset the owner's password.
func TestAdminMemberAdminTargets(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	grantBoardWide(t, pool, 2, 3, "admin")
	alice := signIn(t, h, "alice", testPassword)
	rec, _ := postAs(t, h, "/admin/members/2/verify-email", alice, url.Values{})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/confirm?next=%2Fadmin%2Fmembers%2F2" {
		t.Errorf("a change to an admin without a pass = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if b := bodyOf(t, getAs(t, h, "/admin/members/2", alice)); !strings.Contains(b, "need your password") {
		t.Error("the page does not say that a pass is needed")
	}
	confirmPass(t, h, alice)
	rec, _ = postAs(t, h, "/admin/members/2/deactivate", alice, url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Errorf("deactivate an admin with a pass = %d", rec.Code)
	}
	exec(t, pool, `update users set state = 'active' where id = 2`)

	bob := signIn(t, h, "bob", testPassword)
	confirmPass(t, h, bob)
	rec, _ = postAs(t, h, "/admin/members/1/reset-password", bob, url.Values{"back": {"/admin/members/1"}})
	if b := bodyOf(t, rec); rec.Code != http.StatusForbidden || !strings.Contains(b, "Only an owner") || shownPassword.MatchString(b) {
		t.Errorf("bob resets the owner = %d", rec.Code)
	}
}
