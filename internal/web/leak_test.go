package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/internal/bundletest"
)

// leakBoard is the small board where guests may search, plus one legacy route
// of each kind that points into forum 3 (topic 2, post 5), and at attachment 1,
// which this board moves into post 5. It also holds control routes that point
// at IDs that do not exist.
func leakBoard() *bundletest.Board {
	b := searchSiteBoard()
	id := ptr("id")
	for _, kind := range []string{"forum", "topic", "post", "attachment"} {
		b.LegacyRoutes = append(b.LegacyRoutes, legacyRoute("old/hidden/"+kind, kind, id, nil, 0))
	}
	b.LegacyRoutes = append(b.LegacyRoutes,
		legacyRoute("old/fixed/forum", "forum", nil, ptr(int64(3)), 0),
		legacyRoute("old/fixed/topic", "topic", nil, ptr(int64(2)), 0),
		legacyRoute("old/fixed/post", "post", nil, ptr(int64(5)), 0),
		legacyRoute("old/fixed/attachment", "attachment", nil, ptr(int64(1)), 0),
	)
	b.Attachments[0].PostID = ptr(int64(5))
	return b
}

// hiddenText holds the two strings that only forum 3 holds.
var hiddenText = []string{"Favorite color", "Green, for me"}

func noHiddenText(t *testing.T, where, body string) {
	t.Helper()
	for _, s := range hiddenText {
		if strings.Contains(body, s) {
			t.Errorf("%s holds %q", where, s)
		}
	}
}

// TestHiddenForumNeverLeaks is Review Focus 1: a visitor who cannot read forum 3
// learns nothing of its topics through any path, a legacy redirect included.
func TestHiddenForumNeverLeaks(t *testing.T) {
	h, pool := siteWithPool(t, leakBoard(), Options{})
	carol := signIn(t, h, "carol", "example-password")

	for _, who := range []struct {
		name    string
		cookies []*http.Cookie
	}{{"guest", nil}, {"carol", carol}} {
		t.Run(who.name, func(t *testing.T) {
			// Each hidden target answers 404, and with the body of a missing one.
			for _, p := range []string{"/t/2", "/p/5", "/a/1", "/f/3"} {
				rec := getAs(t, h, p, who.cookies)
				if rec.Code != http.StatusNotFound || location(rec) != "" {
					t.Errorf("%s: %d %q, want 404", p, rec.Code, location(rec))
				}
			}
			// The marker in forum 3 is as hidden as the forum.
			if rec := getAs(t, h, "/t/3", who.cookies); rec.Code != http.StatusNotFound {
				t.Errorf("/t/3: %d, want 404", rec.Code)
			}
			missing := map[string]string{"forum": "99", "topic": "99", "post": "99", "attachment": "99"}
			hidden := map[string]string{"forum": "3", "topic": "2", "post": "5", "attachment": "1"}
			for kind, id := range hidden {
				target := "/old/hidden/" + kind + "?id=" + id
				control := "/old/hidden/" + kind + "?id=" + missing[kind]
				got, want := getAs(t, h, target, who.cookies), getAs(t, h, control, who.cookies)
				if got.Code != http.StatusNotFound || location(got) != "" {
					t.Errorf("%s: %d %q, want 404", target, got.Code, location(got))
				}
				if shape(bodyOf(t, got), target) != shape(bodyOf(t, want), control) {
					t.Errorf("%s: the body differs from the body of a missing target", target)
				}
			}
			for kind := range hidden {
				target := "/old/fixed/" + kind
				control := "/old/hidden/" + kind + "?id=99"
				got, want := getAs(t, h, target, who.cookies), getAs(t, h, control, who.cookies)
				if got.Code != http.StatusNotFound || location(got) != "" {
					t.Errorf("%s: %d %q, want 404", target, got.Code, location(got))
				}
				if shape(bodyOf(t, got), target) != shape(bodyOf(t, want), control) {
					t.Errorf("%s: the body differs from the body of a missing target", target)
				}
			}
		})
	}

	// The pages of the readable forums hold nothing of forum 3.
	pages := []string{"/", "/f/2", "/f/1", "/search?q=color", "/search?author=bob", "/search?q=green", "/new", "/t/1", "/old/thread.cgi?view=full&id=2"}
	check := func(label string) {
		for _, p := range pages {
			noHiddenText(t, label+" guest "+p, bodyOf(t, get(t, h, p)))
			noHiddenText(t, label+" carol "+p, bodyOf(t, getAs(t, h, p, carol)))
		}
	}
	check("kind normal:")

	// Make topic 2 global. A global topic shows in every forum, but only to a
	// viewer who reads its own forum.
	if _, err := pool.Exec(t.Context(), `update topics set kind = 'global' where id = 2`); err != nil {
		t.Fatal(err)
	}
	check("kind global:")
	// Alice reads forum 3, so she sees the title in it. The check is a real one.
	alice := signIn(t, h, "alice", "example-password")
	if body := bodyOf(t, getAs(t, h, "/f/2", alice)); !strings.Contains(body, "Favorite color") {
		t.Error("alice does not see the global topic of forum 3 on /f/2")
	}
	if rec := getAs(t, h, "/old/hidden/topic?id=2", alice); rec.Code != http.StatusMovedPermanently || location(rec) != "/t/2" {
		t.Errorf("alice: %d %q", rec.Code, location(rec))
	}
}

// A moved-topic marker in a readable forum, whose target is hidden, is as
// hidden as the target, on the page and through the old link.
func TestHiddenMarkerTargetNeverLeaks(t *testing.T) {
	h, _ := newTestSite(t, kindsBoard(), Options{})
	rec := get(t, h, "/t/5")
	control := get(t, h, "/t/99")
	if rec.Code != http.StatusNotFound || location(rec) != "" {
		t.Fatalf("/t/5: %d %q", rec.Code, location(rec))
	}
	if shape(bodyOf(t, rec), "/t/5") != shape(bodyOf(t, control), "/t/99") {
		t.Error("/t/5 differs from a missing page")
	}
	leg := get(t, h, "/old/k/topic?id=5")
	if leg.Code != http.StatusNotFound || location(leg) != "" {
		t.Errorf("legacy: %d %q", leg.Code, location(leg))
	}
}
