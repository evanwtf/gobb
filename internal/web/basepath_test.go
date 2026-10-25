package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// sitePathAttr finds the value of every href, src and action attribute.
var sitePathAttr = regexp.MustCompile(`\b(?:href|src|action)="([^"]*)"`)

// checkBasePath fails the test for any href, src or action in body that is not
// a site path under base, an https or mailto URL, or an anchor.
func checkBasePath(t *testing.T, where, body, base string) {
	t.Helper()
	for _, m := range sitePathAttr.FindAllStringSubmatch(body, -1) {
		v := m[1]
		switch {
		case strings.HasPrefix(v, base), strings.HasPrefix(v, "https:"),
			strings.HasPrefix(v, "mailto:"), strings.HasPrefix(v, "#"):
		default:
			t.Errorf("%s: %q is not under %s", where, v, base)
		}
	}
}

// TestBasePathEverywhere checks that every link and source on every page is
// under the base path. Later tasks add their pages to the list.
func TestBasePathEverywhere(t *testing.T) {
	b := linkBoard()
	allowGroup(b, 2, 2, "post.delete_own")
	// Alice, an admin, moderates forum 2 for the moderator forms below.
	for _, o := range []string{"mod.delete", "mod.approve", "mod.lock"} {
		allowGroup(b, 4, 2, o)
	}
	for _, g := range []int64{1, 2} {
		allowGroup(b, g, 0, "account.search")
		allowGroup(b, g, 2, "forum.search")
	}
	h, pool := siteWithPool(t, b, Options{BasePath: "/forum/"})
	pages := []string{"/forum/", "/forum/f/1", "/forum/f/2", "/forum/t/1", "/forum/t/5", "/forum/t/6", "/forum/login", "/forum/team", "/forum/pages/about", "/forum/pages/rules", "/forum/search", "/forum/search?q=example", "/forum/search?author=alice"}
	for _, p := range pages {
		rec := get(t, h, p)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", p, rec.Code)
			continue
		}
		body := bodyOf(t, rec)
		if !sitePathAttr.MatchString(body) {
			t.Errorf("%s: no links found", p)
		}
		checkBasePath(t, p, body, "/forum/")
	}
	// A member's pages hold the badges, the jump links and the mark-read forms.
	// They stay under the base path too.
	alice := signInAt(t, h, "/forum/", "alice")
	if _, err := pool.Exec(t.Context(), `update users set last_visit_at = '2019-06-01T00:00:00Z', last_mark_at = null where id = 1`); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/forum/", "/forum/f/2", "/forum/f/1", "/forum/new", "/forum/t/1", "/forum/u/1", "/forum/members", "/forum/team", "/forum/search?q=example",
		"/forum/messages", "/forum/messages/sent", "/forum/messages/1", "/forum/messages/new?to=bob", "/forum/messages/1/reply?quote=1"} {
		rec := getAs(t, h, p, alice)
		if rec.Code != http.StatusOK {
			t.Errorf("member %s: status %d", p, rec.Code)
			continue
		}
		body := bodyOf(t, rec)
		checkBasePath(t, "member "+p, body, "/forum/")
		if p != "/forum/t/1" && !strings.Contains(body, `action="/forum/`) {
			t.Errorf("member %s: no form", p)
		}
	}
	for _, want := range []string{`action="/forum/read"`, `href="/forum/t/1/new"`} {
		if !strings.Contains(bodyOf(t, getAs(t, h, "/forum/", alice))+bodyOf(t, getAs(t, h, "/forum/f/2", alice)), want) {
			t.Errorf("the member pages lack %s", want)
		}
	}
	if !strings.Contains(bodyOf(t, getAs(t, h, "/forum/f/2", alice)), `action="/forum/f/2/read"`) {
		t.Error("the forum page lacks the mark-forum form under the base path")
	}
	if rec := getAs(t, h, "/forum/t/1/new", alice); rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/forum/t/1") {
		t.Errorf("/forum/t/1/new = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec, _ := postAsAt(t, h, "/forum/", "/forum/f/2/read", alice, url.Values{})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/forum/f/2" {
		t.Errorf("POST /forum/f/2/read = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec, _ = postAsAt(t, h, "/forum/", "/forum/read", alice, url.Values{})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/forum/" {
		t.Errorf("POST /forum/read = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := get(t, h, "/forum/new"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/forum/login?next=%2Fforum%2Fnew" {
		t.Errorf("guest /forum/new = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// The posting pages and their answers keep the prefix too: the forms, the
	// preview, the confirm step, and the redirects.
	for _, p := range []string{"/forum/f/2/post", "/forum/t/1/reply", "/forum/t/1/reply?quote=1", "/forum/p/1/edit"} {
		rec := getAs(t, h, p, alice)
		if rec.Code != http.StatusOK {
			t.Errorf("member %s: status %d", p, rec.Code)
			continue
		}
		body := bodyOf(t, rec)
		checkBasePath(t, "member "+p, body, "/forum/")
		if !strings.Contains(body, `<form class="post-form" method="post" action="/forum/`) {
			t.Errorf("member %s: the form does not post under the base path", p)
		}
	}
	for _, c := range []struct{ target, want string }{
		{"/forum/f/2/post", `href="/forum/f/2"`},
		{"/forum/t/1/reply", `href="/forum/t/1"`},
		{"/forum/p/1/edit", `href="/forum/p/1"`},
	} {
		rec, _ := postAsAt(t, h, "/forum/", c.target, alice, url.Values{"title": {"Base"}, "body": {"Some **text**."}, "preview": {"1"}})
		body := bodyOf(t, rec)
		if rec.Code != http.StatusOK || !strings.Contains(body, "<strong>text</strong>") || !strings.Contains(body, c.want) {
			t.Errorf("preview at %s: status %d, cancel link %s present: %v", c.target, rec.Code, c.want, strings.Contains(body, c.want))
		}
		checkBasePath(t, "preview "+c.target, body, "/forum/")
	}
	rec, _ = postAsAt(t, h, "/forum/", "/forum/f/2/post", alice, url.Values{"title": {"Base"}, "body": {"Made under a base path."}})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/forum/p/") {
		t.Errorf("POST /forum/f/2/post = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	made := rec.Header().Get("Location")
	rec, _ = postAsAt(t, h, "/forum/", made+"/delete", alice, url.Values{})
	if body := bodyOf(t, rec); rec.Code != http.StatusOK || !strings.Contains(body, `action="`+made+`/delete"`) || !strings.Contains(body, "Delete this post?") {
		t.Errorf("POST %s/delete = %d", made, rec.Code)
	} else {
		checkBasePath(t, "confirm "+made, body, "/forum/")
	}
	rec, _ = postAsAt(t, h, "/forum/", made+"/delete", alice, url.Values{"confirm": {"1"}})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/forum/") {
		t.Errorf("confirmed delete = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// The moderator forms and their answers keep the prefix: Approve on the held
	// post, Lock, Unlock, Delete with its confirm step, and Restore.
	mods := func(want ...string) {
		t.Helper()
		body := bodyOf(t, getAs(t, h, "/forum/t/1", alice))
		checkBasePath(t, "moderator /forum/t/1", body, "/forum/")
		for _, w := range want {
			if !strings.Contains(body, `<form class="mark-form" method="post" action="`+w+`">`) {
				t.Errorf("/forum/t/1 lacks the form %s", w)
			}
		}
	}
	mods("/forum/p/3/approve", "/forum/t/1/lock")
	for _, c := range []struct{ target, loc string }{
		{"/forum/p/3/approve", "/forum/p/3"},
		{"/forum/t/1/lock", "/forum/t/1"},
	} {
		if rec, _ = postAsAt(t, h, "/forum/", c.target, alice, url.Values{}); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != c.loc {
			t.Errorf("POST %s = %d %q, want 303 %q", c.target, rec.Code, rec.Header().Get("Location"), c.loc)
		}
	}
	mods("/forum/t/1/unlock")
	rec, _ = postAsAt(t, h, "/forum/", "/forum/p/4/delete", alice, url.Values{})
	if body := bodyOf(t, rec); rec.Code != http.StatusOK || !strings.Contains(body, `action="/forum/p/4/delete"`) {
		t.Errorf("moderator delete, first step = %d", rec.Code)
	}
	for _, c := range []struct {
		target string
		form   url.Values
		loc    string
	}{
		{"/forum/t/1/unlock", url.Values{}, "/forum/t/1"},
		{"/forum/p/4/delete", url.Values{"confirm": {"1"}}, "/forum/t/1"},
		{"/forum/p/4/restore", url.Values{}, "/forum/p/4"},
	} {
		if c.target == "/forum/p/4/restore" {
			mods("/forum/p/4/restore")
		}
		if rec, _ = postAsAt(t, h, "/forum/", c.target, alice, c.form); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != c.loc {
			t.Errorf("POST %s = %d %q, want 303 %q", c.target, rec.Code, rec.Header().Get("Location"), c.loc)
		}
	}
	for _, p := range []string{"/t/1/lock", "/p/4/restore"} {
		if rec := request(t, h, http.MethodPost, p, alice, url.Values{}); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s without the prefix: status %d, want 404", p, rec.Code)
		}
	}

	// The pages that refuse a guest, and the account redirect, keep the prefix too.
	for _, c := range []struct {
		path string
		code int
	}{{"/forum/u/1", 403}, {"/forum/u/8", 410}, {"/forum/members", 403}} {
		rec := get(t, h, c.path)
		if rec.Code != c.code {
			t.Errorf("guest %s: status %d, want %d", c.path, rec.Code, c.code)
		}
		checkBasePath(t, "guest "+c.path, bodyOf(t, rec), "/forum/")
	}
	if rec := get(t, h, "/forum/account"); rec.Header().Get("Location") != "/forum/login?next=%2Fforum%2Faccount" {
		t.Errorf("guest /forum/account = %q", rec.Header().Get("Location"))
	}
	// The account page: 200, every link, source and form action under the prefix,
	// and the three forms post to the prefixed routes.
	rec = getAs(t, h, "/forum/account", alice)
	if rec.Code != http.StatusOK {
		t.Errorf("alice /forum/account = %d", rec.Code)
	}
	account := bodyOf(t, rec)
	checkBasePath(t, "alice /forum/account", account, "/forum/")
	for _, want := range []string{`action="/forum/account/password"`, `action="/forum/account/email"`, `action="/forum/account/signature"`,
		`action="/forum/account/timezone"`, `action="/forum/account/dateformat"`, `action="/forum/account/about"`,
		`action="/forum/account/avatar"`, `action="/forum/account/avatar/remove"`} {
		if !strings.Contains(account, want) {
			t.Errorf("/forum/account lacks %s", want)
		}
	}
	// The three POST routes exist only under the prefix.
	for _, p := range []string{"/account/password", "/account/email", "/account/signature", "/account/timezone",
		"/account/dateformat", "/account/about", "/account/avatar", "/account/avatar/remove"} {
		if rec := request(t, h, http.MethodPost, p, alice, url.Values{}); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s without the prefix: status %d, want 404", p, rec.Code)
		}
	}

	// The message pages, their guests' redirects and their answers keep the prefix.
	if rec := get(t, h, "/forum/messages/1"); rec.Header().Get("Location") != "/forum/login?next=%2Fforum%2Fmessages%2F1" {
		t.Errorf("guest /forum/messages/1 = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec, _ = postAsAt(t, h, "/forum/", "/forum/messages/new", alice, url.Values{"to": {"bob"}, "subject": {"Base"}, "body": {"Some **text**."}, "preview": {"1"}})
	if body := bodyOf(t, rec); rec.Code != http.StatusOK || !strings.Contains(body, "<strong>text</strong>") || !strings.Contains(body, `action="/forum/messages/new"`) {
		t.Errorf("message preview = %d", rec.Code)
	} else {
		checkBasePath(t, "message preview", body, "/forum/")
	}
	rec, _ = postAsAt(t, h, "/forum/", "/forum/messages/new", alice, url.Values{"to": {"nobody"}, "subject": {"Base"}, "body": {"Text"}})
	if body := bodyOf(t, rec); rec.Code != http.StatusBadRequest {
		t.Errorf("refused message = %d", rec.Code)
	} else {
		checkBasePath(t, "refused message", body, "/forum/")
	}
	rec, _ = postAsAt(t, h, "/forum/", "/forum/messages/new", alice, url.Values{"to": {"bob"}, "subject": {"Base"}, "body": {"Made under a base path."}})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/forum/messages/") {
		t.Errorf("POST /forum/messages/new = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if _, err := pool.Exec(t.Context(), `insert into acl_user_grants (user_id, forum_id, option, setting) values (1, 0, 'pm.delete', 3)`); err != nil {
		t.Fatal(err)
	}
	rec, _ = postAsAt(t, h, "/forum/", "/forum/messages/1/delete", alice, url.Values{})
	if body := bodyOf(t, rec); rec.Code != http.StatusOK || !strings.Contains(body, `action="/forum/messages/1/delete"`) {
		t.Errorf("message delete, first step = %d", rec.Code)
	} else {
		checkBasePath(t, "message confirm", body, "/forum/")
	}
	rec, _ = postAsAt(t, h, "/forum/", "/forum/messages/1/delete", alice, url.Values{"confirm": {"1"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/forum/messages/sent" {
		t.Errorf("message delete = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, p := range []string{"/messages", "/messages/1"} {
		if rec := getAs(t, h, p, alice); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s without the prefix: status %d, want 404", p, rec.Code)
		}
	}

	// The rich post holds links and images that the author wrote as site paths.
	body := bodyOf(t, get(t, h, "/forum/t/5"))
	for _, want := range []string{`href="/forum/t/1"`, `src="/forum/a/3"`, `src="/forum/files/smilies/smile.gif"`} {
		if !strings.Contains(body, want) {
			t.Errorf("/forum/t/5 lacks %s", want)
		}
	}
	// Redirects keep the base path too.
	for _, c := range []struct{ from, to string }{
		{"/forum/p/4", "/forum/t/1#p4"},
		{"/forum/t/3", "/forum/t/1"},
		// A link forum with a site path as its target.
		{"/forum/f/4", "/forum/pages/rules?x=1#top"},
	} {
		rec := get(t, h, c.from)
		if loc := rec.Header().Get("Location"); loc != c.to {
			t.Errorf("%s: Location %q, want %q (status %d)", c.from, loc, c.to, rec.Code)
		}
	}
	for _, p := range []string{"/t/1", "/f/2", "/p/4", "/a/1", "/search"} {
		if rec := get(t, h, p); rec.Code != http.StatusNotFound {
			t.Errorf("%s without the prefix: status %d, want 404", p, rec.Code)
		}
	}
}

// signInAt signs in the made-up user name under a base path whose index is home.
func signInAt(t *testing.T, h http.Handler, home, name string) []*http.Cookie {
	t.Helper()
	rec, cookies := postAsAt(t, h, home, home+"login", nil, url.Values{"name": {name}, "password": {"example-password"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("sign-in of %s: status %d", name, rec.Code)
	}
	return cookies
}
