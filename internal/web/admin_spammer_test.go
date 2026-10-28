package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/internal/bundletest"
)

func TestAdminSpammerAndUndo(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	visible := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(t.Context(), `select count(*) from posts where author_id = 2 and visibility <> 'deleted'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := visible(); n != 2 {
		t.Fatalf("fixture: bob has %d posts, want 2", n)
	}
	if b := bodyOf(t, getAs(t, h, "/admin/members/2", alice)); !strings.Contains(b, `href="/admin/members/2/spammer"`) {
		t.Error("the member page has no Spammer link")
	}
	// The owner's page and the admin's own page have none, and their confirm
	// page is missing.
	if b := bodyOf(t, getAs(t, h, "/admin/members/1", alice)); strings.Contains(b, "/spammer") {
		t.Error("the owner's page has a Spammer link")
	}
	if rec := getAs(t, h, "/admin/members/1/spammer", alice); rec.Code != http.StatusNotFound {
		t.Errorf("the owner's confirm page = %d", rec.Code)
	}
	page := bodyOf(t, getAs(t, h, "/admin/members/2/spammer", alice))
	for _, want := range []string{"Mark <bdi>bob</bdi> as a spammer?", "deletes 2 posts", "hides 1 topics", `name="back" value="/admin/members/2/spammer"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the confirm page lacks %q", want)
		}
	}

	// Without a pass, the POST goes to the confirm step and changes nothing.
	rec, _ := postAs(t, h, "/admin/members/2/spammer", alice, url.Values{"back": {"/admin/members/2/spammer"}})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/admin/confirm?") || visible() != 2 {
		t.Fatalf("POST without a pass = %d to %q, %d posts left", rec.Code, rec.Header().Get("Location"), visible())
	}
	confirmPass(t, h, alice)
	rec, _ = postAs(t, h, "/admin/members/2/spammer", alice, url.Values{"back": {"/admin/members/2/spammer"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/members/2?done=spammer" {
		t.Fatalf("POST = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if n := visible(); n != 0 {
		t.Errorf("bob has %d posts after the action, want 0", n)
	}
	rows := modLog(t, pool)
	last := rows[len(rows)-1]
	if last.Actor != 1 || last.Action != "user.spammer" || last.Target != "user" || last.ID != 2 {
		t.Fatalf("logged %+v", last)
	}
	var logID string
	if err := pool.QueryRow(t.Context(), `select id::text from mod_log where action = 'user.spammer'`).Scan(&logID); err != nil {
		t.Fatal(err)
	}
	undo := "/admin/log/" + logID + "/undo"
	if b := bodyOf(t, getAs(t, h, "/admin/log", alice)); !strings.Contains(b, `action="`+undo+`"`) {
		t.Error("the log has no Undo form")
	}
	rec, _ = postAs(t, h, undo, alice, url.Values{"back": {"/admin/log"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/log?done=undone" {
		t.Fatalf("undo = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if n := visible(); n != 2 {
		t.Errorf("bob has %d posts after the undo, want 2", n)
	}
	if b := bodyOf(t, getAs(t, h, "/admin/log?done=undone", alice)); strings.Contains(b, `action="`+undo+`"`) || !strings.Contains(b, "(undone)") {
		t.Error("the log still offers the Undo")
	}
	rec, _ = postAs(t, h, undo, alice, url.Values{"back": {"/admin/log"}})
	if rec.Header().Get("Location") != "/admin/log?done=closed" {
		t.Errorf("second undo = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	// A member who is no admin gets the 404 of the admin area.
	carol := signIn(t, h, "carol", testPassword)
	if rec := getAs(t, h, "/admin/members/2/spammer", carol); rec.Code != http.StatusNotFound {
		t.Errorf("carol's confirm page = %d", rec.Code)
	}
}

func TestQueuePage(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	// Post 3, carol's reply in forum 2, is held.
	bob := signIn(t, h, "bob", testPassword)
	b := bodyOf(t, getAs(t, h, "/queue", bob))
	if strings.Contains(b, `action="/p/3/delete"`) {
		t.Error("the queue offers Delete to a moderator without mod.delete")
	}
	if _, err := pool.Exec(t.Context(), `insert into acl_user_grants (user_id, forum_id, option, setting) values (2, 2, 'mod.delete', 3)`); err != nil {
		t.Fatal(err)
	}
	b = bodyOf(t, getAs(t, h, "/queue", bob))
	for _, want := range []string{`action="/p/3/approve"`, `action="/p/3/delete"`, `name="from" value="queue"`, `<meta name="robots" content="noindex">`} {
		if !strings.Contains(b, want) {
			t.Errorf("the queue lacks %q", want)
		}
	}
	if !strings.Contains(bodyOf(t, getAs(t, h, "/", bob)), `href="/queue"`) {
		t.Error("bob's nav has no Queue link")
	}
	// Approve goes back to the queue, which is then empty.
	rec, _ := postAs(t, h, "/p/3/approve", bob, url.Values{"from": {"queue"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/queue" {
		t.Fatalf("approve = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if b := bodyOf(t, getAs(t, h, "/queue", bob)); !strings.Contains(b, "No post is waiting for approval.") {
		t.Error("the queue is not empty after the approve")
	}
	// A delete from the queue asks, keeps from=queue, and goes back.
	if _, err := pool.Exec(t.Context(), `update posts set visibility = 'unapproved' where id = 2`); err != nil {
		t.Fatal(err)
	}
	rec, _ = postAs(t, h, "/p/2/delete", bob, url.Values{"from": {"queue"}})
	confirm := bodyOf(t, rec)
	if !strings.Contains(confirm, `name="from" value="queue"`) || !strings.Contains(confirm, `href="/queue"`) {
		t.Error("the delete confirm loses the queue")
	}
	rec, _ = postAs(t, h, "/p/2/delete", bob, url.Values{"from": {"queue"}, "confirm": {"1"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/queue" {
		t.Errorf("delete = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	// A member who approves nowhere, and a guest, get 404 and no link.
	carol := signIn(t, h, "carol", testPassword)
	if rec := getAs(t, h, "/queue", carol); rec.Code != http.StatusNotFound {
		t.Errorf("carol's queue = %d", rec.Code)
	}
	if strings.Contains(bodyOf(t, getAs(t, h, "/", carol)), `href="/queue"`) {
		t.Error("carol's nav has a Queue link")
	}
	if rec := getAs(t, h, "/queue", nil); rec.Code != http.StatusNotFound {
		t.Errorf("the guest's queue = %d", rec.Code)
	}
}
