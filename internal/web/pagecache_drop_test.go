package web

import (
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/board"
)

// The drops of the guest page cache: each write path that changes what a guest
// sees drops the pages it reaches, and a write that is refused drops nothing.

// guestKeys are the guest pages that the drop tests fill: the index, the forum
// of topics 1, 5 and 6, and those three topics. Topic 1 shows posts of alice
// and bob (carol's post waits for approval), topic 5 of alice and bob, topic 6
// of bob.
var guestKeys = map[string]cacheKey{
	"/":    {scheme: "slate", kind: cacheIndex},
	"/f/2": {scheme: "slate", kind: cacheForum, id: 2, page: 1},
	"/t/1": {scheme: "slate", kind: cacheTopic, id: 1, page: 1},
	"/t/5": {scheme: "slate", kind: cacheTopic, id: 5, page: 1},
	"/t/6": {scheme: "slate", kind: cacheTopic, id: 6, page: 1},
}

// fillGuestPages asks for each page of guestKeys as a guest, so the cache holds
// it.
func fillGuestPages(t *testing.T, h http.Handler, g *GuestCache) {
	t.Helper()
	for path, k := range guestKeys {
		if rec := get(t, h, path); rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		if _, ok := g.Pages.Peek(k.String()); !ok {
			t.Fatalf("%s is not in the cache after a guest asked for it", path)
		}
	}
}

// keptPaths returns the paths of guestKeys whose pages the cache still holds,
// sorted.
func keptPaths(g *GuestCache) []string {
	var out []string
	for path, k := range guestKeys {
		if _, ok := g.Pages.Peek(k.String()); ok {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

// form is a shorthand for url.Values with one value per field.
func form(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

// carolPostsFreely lets carol post in forum 2 with no approval.
const carolPostsFreely = `insert into acl_user_grants (user_id, forum_id, option, setting) values (3, 2, 'post.no_approval', 3)`

func TestGuestCacheDropsOnEachWritePath(t *testing.T) {
	type step struct {
		as     string // the member who writes: alice, bob or carol
		target string
		form   url.Values
		code   int
	}
	for _, c := range []struct {
		name string
		// sql runs first, to set the scene.
		sql string
		// before runs before the cache is filled: a write that sets the scene.
		before []step
		write  step
		kept   []string
	}{
		{name: "reply", sql: carolPostsFreely, write: step{"carol", "/t/5/reply", form("body", "A reply from carol."), http.StatusSeeOther},
			kept: []string{"/t/1", "/t/6"}},
		{name: "new topic", sql: carolPostsFreely, write: step{"carol", "/f/2/post", form("title", "Carol's topic", "body", "Hello."), http.StatusSeeOther},
			kept: []string{"/t/1", "/t/5", "/t/6"}},
		// A post that waits for approval changes nothing a guest sees.
		{name: "held reply", write: step{"carol", "/t/5/reply", form("body", "A reply that waits."), http.StatusSeeOther},
			kept: []string{"/", "/f/2", "/t/1", "/t/5", "/t/6"}},
		{name: "held new topic", write: step{"carol", "/f/2/post", form("title", "Waiting topic", "body", "Hello."), http.StatusSeeOther},
			kept: []string{"/", "/f/2", "/t/1", "/t/5", "/t/6"}},
		// bob's edit of his own post in topic 6: his pages, the topic, the listings.
		{name: "edit", write: step{"bob", "/p/9/edit", form("title", "Another poll", "body", "Vote now, please."), http.StatusSeeOther},
			kept: nil},
		// carol's held reply shows nowhere, so her edit drops only its topic and
		// the listings.
		{name: "edit of a post whose author shows nowhere else",
			before: []step{{"carol", "/t/6/reply", form("body", "Carol's own words."), http.StatusSeeOther}},
			write:  step{"carol", "/p/{last}/edit", form("body", "Carol's words, edited."), http.StatusSeeOther},
			kept:   []string{"/t/1", "/t/5"}},
		{name: "delete", write: step{"bob", "/p/8/delete", form("confirm", "1"), http.StatusSeeOther},
			kept: nil},
		{name: "restore",
			before: []step{{"bob", "/p/8/delete", form("confirm", "1"), http.StatusSeeOther}},
			write:  step{"bob", "/p/8/restore", form(), http.StatusSeeOther},
			kept:   nil},
		// carol's held post in topic 1: approval shows it, and changes her count.
		{name: "approve", write: step{"bob", "/p/3/approve", form(), http.StatusSeeOther},
			kept: []string{"/t/5", "/t/6"}},
		{name: "lock", write: step{"bob", "/t/6/lock", form(), http.StatusSeeOther},
			kept: []string{"/t/1", "/t/5"}},
		{name: "unlock",
			before: []step{{"bob", "/t/6/lock", form(), http.StatusSeeOther}},
			write:  step{"bob", "/t/6/unlock", form(), http.StatusSeeOther},
			kept:   []string{"/t/1", "/t/5"}},
		{name: "topic type", write: step{"bob", "/t/6/type", form("kind", "sticky"), http.StatusSeeOther},
			kept: []string{"/t/1", "/t/5"}},
		// A member's signature, avatar and profile show on each of their posts.
		{name: "signature", write: step{"alice", "/account/signature", form("signature", "New words."), http.StatusSeeOther},
			kept: []string{"/", "/f/2", "/t/6"}},
		{name: "avatar removed", write: step{"alice", "/account/avatar/remove", form(), http.StatusSeeOther},
			kept: []string{"/", "/f/2", "/t/6"}},
		{name: "profile", write: step{"bob", "/account/about", form("location", "Leeds"), http.StatusSeeOther},
			kept: []string{"/", "/f/2"}},
		// The index lists today's birthdays.
		{name: "birthday", write: step{"carol", "/account/about", form("bday", "1", "bmonth", "2", "byear", "1990"), http.StatusSeeOther},
			kept: []string{"/t/1", "/t/5", "/t/6"}},
		// Every admin write drops every page.
		{name: "admin static page", write: step{"alice", "/admin/pages", form("slug", "cache-test", "title", "Cache test", "markdown", "1", "body", "Be kind."), http.StatusSeeOther},
			kept: nil},
		{name: "admin settings", write: step{"alice", "/admin/settings/header/remove", form(), http.StatusSeeOther},
			kept: nil},

		// Writes that change nothing a guest sees drop nothing.
		{name: "time zone", write: step{"alice", "/account/timezone", form("timezone", "Europe/Paris"), http.StatusSeeOther},
			kept: []string{"/", "/f/2", "/t/1", "/t/5", "/t/6"}},
		{name: "mark read", write: step{"alice", "/read", form(), http.StatusSeeOther},
			kept: []string{"/", "/f/2", "/t/1", "/t/5", "/t/6"}},
		// A refused write drops nothing: a flood of refusals cannot empty the cache.
		{name: "refused reply of a guest", write: step{"", "/t/5/reply", form("body", "Spam."), http.StatusForbidden},
			kept: []string{"/", "/f/2", "/t/1", "/t/5", "/t/6"}},
		{name: "refused lock", write: step{"carol", "/t/5/lock", form(), http.StatusForbidden},
			kept: []string{"/", "/f/2", "/t/1", "/t/5", "/t/6"}},
		{name: "bad csrf", write: step{"alice", "/t/5/lock", form("csrf", "forged"), http.StatusForbidden},
			kept: []string{"/", "/f/2", "/t/1", "/t/5", "/t/6"}},
		{name: "bad input", write: step{"carol", "/t/5/reply", form("body", ""), http.StatusBadRequest},
			kept: []string{"/", "/f/2", "/t/1", "/t/5", "/t/6"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, pool, g := cachedSite(t, richBoard(), Options{MemberPosts: 1000, AdminWrites: 1000})
			// bob moderates forum 2.
			for _, o := range []string{"mod.delete", "mod.approve", "mod.edit", "mod.lock", "mod.pin"} {
				exec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (2, 2, '`+o+`', 3)`)
			}
			if c.sql != "" {
				exec(t, pool, c.sql)
			}
			// The sign-ins and the admin pass come first: they read the index.
			cookies := map[string][]*http.Cookie{"": nil}
			for _, name := range []string{"bob", "carol"} {
				cookies[name] = signIn(t, h, name, testPassword)
			}
			cookies["alice"] = adminWithPass(t, h)
			run := func(s step) {
				t.Helper()
				if strings.Contains(s.target, "{last}") {
					var id int
					if err := pool.QueryRow(t.Context(), `select max(id) from posts`).Scan(&id); err != nil {
						t.Fatal(err)
					}
					s.target = strings.ReplaceAll(s.target, "{last}", strconv.Itoa(id))
				}
				rec, _ := postAs(t, h, s.target, cookies[s.as], s.form)
				if rec.Code != s.code {
					t.Fatalf("POST %s as %q = %d, want %d\n%.600s", s.target, s.as, rec.Code, s.code, bodyOf(t, rec))
				}
			}
			for _, s := range c.before {
				run(s)
			}
			fillGuestPages(t, h, g)
			run(c.write)
			if got := keptPaths(g); !slices.Equal(got, c.kept) {
				t.Errorf("kept %v, want %v", got, c.kept)
			}
		})
	}
}

// A dropped page shows the change to the next guest.
func TestGuestCacheShowsAReplyAtOnce(t *testing.T) {
	h, pool, g := cachedSite(t, richBoard(), Options{})
	// alice's posts need no approval, so the reply shows to a guest.
	exec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (1, 2, 'post.no_approval', 3)`)
	alice := signIn(t, h, "alice", testPassword)
	fillGuestPages(t, h, g)
	rec, _ := postAs(t, h, "/t/5/reply", alice, form("body", "A brand new reply."))
	id := idOf(t, rec)
	if body := bodyOf(t, get(t, h, "/t/5")); !strings.Contains(body, "A brand new reply.") {
		t.Error("the guest page of the topic does not show the reply")
	}
	if body := bodyOf(t, get(t, h, "/f/2")); !strings.Contains(body, `href="/p/`+strconv.Itoa(id)+`"`) {
		t.Error("the forum page does not show the reply as the last post")
	}
}

// A forum that turns members-only is gone from the guest pages at once.
func TestGuestCacheHiddenForumIsNotServedStale(t *testing.T) {
	h, _, g := cachedSite(t, richBoard(), Options{})
	alice := adminWithPass(t, h)
	fillGuestPages(t, h, g)
	if !strings.Contains(bodyOf(t, get(t, h, "/t/5")), "A reply with a smiley") {
		t.Fatal("the topic is not visible to a guest at first")
	}
	rec, _ := postAs(t, h, "/admin/permissions/2/option", alice, form("group", "1", "option", "forum.read", "setting", "never"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("permission change = %d\n%.400s", rec.Code, bodyOf(t, rec))
	}
	if kept := keptPaths(g); len(kept) != 0 {
		t.Errorf("a change to the rights kept %v", kept)
	}
	for _, path := range []string{"/t/5", "/t/1"} {
		rec := get(t, h, path)
		if rec.Code == http.StatusOK || strings.Contains(bodyOf(t, rec), "A reply with a smiley") {
			t.Errorf("%s = %d for a guest after the forum turned members-only", path, rec.Code)
		}
	}
}

// A command of another process (gobb user, gobb ban, gobb import) bumps the
// epoch, and the watch empties the cache.
func TestGuestCacheEpochDropsEverything(t *testing.T) {
	h, pool, g := cachedSite(t, richBoard(), Options{})
	e := &epochWatch{g: g, pool: pool}
	e.check(t.Context()) // the first read at start
	fillGuestPages(t, h, g)
	e.check(t.Context())
	if len(keptPaths(g)) != len(guestKeys) {
		t.Fatal("a read with no change dropped pages")
	}
	if err := board.BumpPageEpoch(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	e.check(t.Context())
	if kept := keptPaths(g); len(kept) != 0 {
		t.Errorf("a new epoch kept %v", kept)
	}
	// The running watch sees a bump within its interval.
	fillGuestPages(t, h, g)
	ctx := t.Context()
	go e.watch(ctx, 20*time.Millisecond)
	if err := board.BumpPageEpoch(ctx, pool); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(keptPaths(g)) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the watch did not empty the cache")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
