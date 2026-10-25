package web

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/db"
)

// getPaths lists the GET paths of the site that a post body can point an image
// at. Each task that adds a page adds its GET paths here.
var getPaths = []string{
	"/",
	"/f/1",
	"/f/2",
	"/f/2?page=1",
	"/f/3",
	"/t/1",
	"/t/1?page=1",
	"/t/2",
	"/t/3",
	"/t/1/new",
	"/t/2/new",
	"/p/1",
	"/p/3",
	"/a/1",
	"/a/2",
	"/new",
	"/f/2/post",
	"/f/3/post",
	"/t/1/reply",
	"/t/1/reply?quote=1",
	"/t/1/reply?quote=5",
	"/p/1/edit",
	"/p/2/edit",
	"/u/1",
	"/u/8",
	"/members",
	"/members?sort=posts",
	"/team",
	"/pages/about",
	"/account",
	"/messages",
	"/messages?page=2",
	"/messages/sent",
	"/messages/new",
	"/messages/new?to=bob",
	"/messages/1",
	"/messages/2",
	"/messages/1/reply",
	"/messages/1/reply?quote=1",
	"/messages/2/reply",
	"/search?q=example",
	"/search?author=alice",
	"/login",
	"/static/gobb.css",
	"/files/avatars/1.png",
	"/old/thread.cgi?view=full&id=1",
	"/old/thread.cgi?view=full&id=2",
	"/old/profile.cgi",
	"/old/about.cgi",
	"/old/images/smilies/smile.gif",
	"/old/nothing.cgi",
}

// dumpData returns every row of every data table as text, in a stable order.
// It leaves out the sessions table and the two visit columns of users, which
// move with any request of a signed-in member.
func dumpData(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	ctx := context.Background()
	tables, err := db.DataTables(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, name := range tables {
		if name == "sessions" {
			continue
		}
		var dump string
		err := pool.QueryRow(ctx, `
select coalesce(string_agg(r, E'\n' order by r), '')
  from (select (to_jsonb(x) - 'last_active_at' - 'last_visit_at')::text as r from `+name+` x) q`).Scan(&dump)
		if err != nil {
			t.Fatalf("dump %s: %v", name, err)
		}
		out[name] = dump
	}
	return out
}

// diffTables names the tables whose dumps differ.
func diffTables(a, b map[string]string) []string {
	var out []string
	for name := range a {
		if a[name] != b[name] {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// TestGetHasNoSideEffects is Review Focus 2. A post can hold an image whose
// address is a path of the site, and the browser then sends a GET with the
// cookies of every reader. That GET, marked as an image load, changes no data.
//
// The rule has one accepted exception: a GET whose cookie names a dead session
// deletes that session's row (see isNavigation). carol's session is live, so
// the exception does not arise here.
func TestGetHasNoSideEffects(t *testing.T) {
	h, pool := siteWithPool(t, pagedBoard(), Options{})
	carol := signIn(t, h, "carol", "example-password")
	paths := append(slices.Clone(getPaths), "/t/7", "/t/7/new", "/t/7?page=2", "/f/2?page=2")

	// The dump must see a real change: a navigation to a topic is one.
	before := dumpData(t, pool)
	getWith(t, h, "/t/1", carol, asPage)
	if diff := diffTables(before, dumpData(t, pool)); !slices.Equal(diff, []string{"topic_marks", "topics"}) {
		t.Fatalf("a navigation changed %v, want [topic_marks topics]", diff)
	}

	before = dumpData(t, pool)
	for _, p := range paths {
		for _, hdr := range []map[string]string{asImage, {"Sec-Fetch-Dest": "empty"}, {"Sec-Fetch-Dest": "iframe"}} {
			rec := getWith(t, h, p, carol, hdr)
			if rec.Code >= http.StatusInternalServerError {
				t.Errorf("GET %s (%s): status %d", p, hdr["Sec-Fetch-Dest"], rec.Code)
			}
		}
		if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
			t.Fatalf("GET %s changed %v", p, diff)
		}
	}
	// A guest is held to the same rule.
	for _, p := range paths {
		getWith(t, h, p, nil, asImage)
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("guest GETs changed %v", diff)
	}
	// So is a request that carries no fetch metadata and no HTML accept header.
	for _, p := range paths {
		getAs(t, h, p, carol)
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("plain GETs changed %v", diff)
	}
}
