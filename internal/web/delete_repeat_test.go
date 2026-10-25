package web

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

// TestRepeatedDeleteConfirmIsASuccess: a second click on Delete in the confirm
// page meets a post that is gone for the viewer. The author already knows the
// post exists, so the second POST answers the same 303 as the first. Any other
// viewer still meets 404 (spec 14), and the second POST changes nothing.
func TestRepeatedDeleteConfirmIsASuccess(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", "example-password")
	c := signIn(t, h, "carol", "example-password")

	rec := send(t, h, "/t/1/reply", a, url.Values{"body": {"A reply to delete twice."}})
	reply := idOf(t, rec)
	target := fmt.Sprintf("/p/%d/delete", reply)
	confirm := url.Values{"confirm": {"1"}}

	first := send(t, h, target, a, confirm)
	if first.Code != http.StatusSeeOther {
		t.Fatalf("first confirm = %d, want 303", first.Code)
	}
	after := dumpData(t, pool)
	second := send(t, h, target, a, confirm)
	if second.Code != http.StatusSeeOther || second.Header().Get("Location") != first.Header().Get("Location") {
		t.Errorf("second confirm = %d %q, want 303 %q", second.Code, second.Header().Get("Location"), first.Header().Get("Location"))
	}
	if diff := diffTables(after, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("the second confirm changed %v", diff)
	}

	// Another member, and a guest, meet 404 for the same deleted post, with and
	// without the confirm.
	for _, who := range []struct {
		name    string
		cookies []*http.Cookie
	}{{"carol", c}, {"a guest", nil}} {
		for _, form := range []url.Values{confirm, {}} {
			if rec := send(t, h, target, who.cookies, form); rec.Code != http.StatusNotFound {
				t.Errorf("%s, POST %s %v = %d, want 404", who.name, target, form, rec.Code)
			}
		}
	}

	// The author's POST without the confirm still meets 404: the page that asks
	// has nothing left to ask about.
	if rec := send(t, h, target, a, url.Values{}); rec.Code != http.StatusNotFound {
		t.Errorf("author, POST %s without confirm = %d, want 404", target, rec.Code)
	}

	// A post that does not exist, and a deleted post that alice did not write
	// (post 4 is a guest's), stay 404.
	if _, err := pool.Exec(t.Context(), `update posts set visibility = 'deleted' where id = 4`); err != nil {
		t.Fatal(err)
	}
	for _, other := range []string{"/p/9999/delete", "/p/4/delete"} {
		if rec := send(t, h, other, a, confirm); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", other, rec.Code)
		}
	}

	// The first post of a topic: the answer is the forum both times.
	rec = send(t, h, "/f/2/post", a, url.Values{"title": {"Delete twice"}, "body": {"Only post."}})
	only := idOf(t, rec)
	target = fmt.Sprintf("/p/%d/delete", only)
	r1 := send(t, h, target, a, confirm)
	r2 := send(t, h, target, a, confirm)
	if r1.Code != http.StatusSeeOther || r2.Code != http.StatusSeeOther || r1.Header().Get("Location") != "/f/2" || r2.Header().Get("Location") != "/f/2" {
		t.Errorf("two confirms of the only post = %d %q, %d %q; want 303 /f/2 twice", r1.Code, r1.Header().Get("Location"), r2.Code, r2.Header().Get("Location"))
	}
	if rec := send(t, h, target, c, confirm); rec.Code != http.StatusNotFound {
		t.Errorf("carol, POST %s = %d, want 404", target, rec.Code)
	}
}
