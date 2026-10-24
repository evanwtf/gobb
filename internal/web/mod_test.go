package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// modSite is the posting board with bob a moderator of forum 2: the test gives him
// mod.delete, mod.approve, mod.edit and mod.lock there. Topic 1 holds the posts
// 1 (alice), 2 (bob), 3 (carol, held) and 4 (a guest). The index counts the posts
// of forum 2 and of its subforum 3: 3 approved in topic 1, and 2 in topic 2.
func modSite(t *testing.T) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	h, pool := siteWithPool(t, postingBoard(), Options{})
	for _, o := range []string{"mod.delete", "mod.approve", "mod.edit", "mod.lock"} {
		exec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (2, 2, '`+o+`', 3)`)
	}
	return h, pool
}

var (
	formAction   = regexp.MustCompile(`<form[^>]*\baction="([^"]*)"`)
	repliesCount = regexp.MustCompile(`(\d+) repl(?:y|ies)`)
	forumCount   = regexp.MustCompile(`data-label="Posts"><span class="forum-count">([\d,]+)</span>`)
)

// formActions lists the action of every form of a page.
func formActions(page string) []string {
	var out []string
	for _, m := range formAction.FindAllStringSubmatch(page, -1) {
		out = append(out, m[1])
	}
	return out
}

// articleOf returns the post article with the given ID, or "".
func articleOf(page string, id int) string {
	_, rest, ok := strings.Cut(page, fmt.Sprintf(`id="p%d"`, id))
	if !ok {
		return ""
	}
	article, _, _ := strings.Cut(rest, "</article>")
	return article
}

// topicReplies reads the reply count that the topic page shows.
func topicReplies(t *testing.T, page string) int {
	t.Helper()
	m := repliesCount.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("the topic page shows no reply count")
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// indexPosts reads the post count that the index shows for forum 2.
func indexPosts(t *testing.T, h http.Handler, cookies []*http.Cookie) int {
	t.Helper()
	row := rowOf(bodyOf(t, getAs(t, h, "/", cookies)), "forum-row", "Main Discussion")
	m := forumCount.FindStringSubmatch(row)
	if m == nil {
		t.Fatalf("the index row of forum 2 shows no post count:\n%s", row)
	}
	n, _ := strconv.Atoi(strings.ReplaceAll(m[1], ",", ""))
	return n
}

type logRow struct {
	Actor  int
	Action string
	Target string
	ID     int
}

// modLog reads the moderation log in order.
func modLog(t *testing.T, pool *pgxpool.Pool) []logRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select coalesce(actor_id, 0), action, target_type, target_id from mod_log order by id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []logRow
	for rows.Next() {
		var r logRow
		if err := rows.Scan(&r.Actor, &r.Action, &r.Target, &r.ID); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestModeratorControls(t *testing.T) {
	h, pool := modSite(t)
	b := signIn(t, h, "bob", "example-password")
	topic := func() string {
		t.Helper()
		rec := getAs(t, h, "/t/1", b)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /t/1 = %d", rec.Code)
		}
		return bodyOf(t, rec)
	}
	hasForm := func(page, action string) bool { return slices.Contains(formActions(page), action) }

	// The start: the moderator sees the controls that fit each post.
	page := topic()
	for _, want := range []string{"/p/1/delete", "/p/2/delete", "/p/3/delete", "/p/4/delete", "/p/3/approve", "/t/1/lock"} {
		if !hasForm(page, want) {
			t.Errorf("the moderator's page lacks the form %s; has %v", want, formActions(page))
		}
	}
	for _, none := range []string{"/p/1/approve", "/p/4/approve", "/p/1/restore", "/p/4/restore", "/t/1/unlock"} {
		if hasForm(page, none) {
			t.Errorf("the moderator's page has the form %s at the start", none)
		}
	}
	if got := topicReplies(t, page); got != 2 {
		t.Fatalf("replies at the start = %d, want 2", got)
	}
	if got := indexPosts(t, h, b); got != 5 {
		t.Fatalf("forum posts at the start = %d, want 5", got)
	}
	if !strings.Contains(articleOf(page, 3), "Waiting for approval") {
		t.Error("the held post lacks its label")
	}
	if len(modLog(t, pool)) != 0 {
		t.Fatal("the fixture has log rows")
	}

	// do sends a moderator POST and checks the answer.
	do := func(target string, form url.Values, wantLoc string) {
		t.Helper()
		rec := send(t, h, target, b, form)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != wantLoc {
			t.Fatalf("POST %s = %d %q, want 303 %q\n%s", target, rec.Code, rec.Header().Get("Location"), wantLoc, bodyOf(t, rec))
		}
	}
	check := func(step string, replies, posts, logRows int) string {
		t.Helper()
		page := topic()
		if got := topicReplies(t, page); got != replies {
			t.Errorf("%s: the topic page shows %d replies, want %d", step, got, replies)
		}
		if got := indexPosts(t, h, b); got != posts {
			t.Errorf("%s: the index shows %d posts, want %d", step, got, posts)
		}
		if got := len(modLog(t, pool)); got != logRows {
			t.Errorf("%s: the log holds %d rows, want %d", step, got, logRows)
		}
		return page
	}
	lastLog := func() logRow {
		t.Helper()
		l := modLog(t, pool)
		return l[len(l)-1]
	}

	// Delete a reply: the first POST asks, the second deletes.
	rec := send(t, h, "/p/4/delete", b, url.Values{})
	if rec.Code != http.StatusOK || !strings.Contains(bodyOf(t, rec), "Delete this post?") {
		t.Fatalf("the first POST of a delete = %d, want the confirm page", rec.Code)
	}
	check("after the confirm page", 2, 5, 0)
	do("/p/4/delete", url.Values{"confirm": {"1"}}, "/t/1")
	page = check("after the delete", 1, 4, 1)
	if l := lastLog(); l != (logRow{2, "post.delete", "post", 4}) {
		t.Errorf("delete log = %+v", l)
	}
	art := articleOf(page, 4)
	if !strings.Contains(art, ">Deleted<") || !hasForm(art, "/p/4/restore") || hasForm(art, "/p/4/delete") {
		t.Errorf("the deleted post shows Deleted, Restore and no Delete: %s", art)
	}
	if !strings.Contains(art, ">Restore<") {
		t.Error("the deleted post has no Restore label")
	}

	// A deleted post is not the moderator's to see only: the guest sees nothing of it.
	if g := bodyOf(t, get(t, h, "/t/1")); strings.Contains(g, "Hello from a guest.") || strings.Contains(g, ">Deleted<") {
		t.Error("a guest sees the deleted post")
	}

	// Restore it.
	do("/p/4/restore", url.Values{}, "/p/4")
	page = check("after the restore", 2, 5, 2)
	if l := lastLog(); l != (logRow{2, "post.restore", "post", 4}) {
		t.Errorf("restore log = %+v", l)
	}
	if art := articleOf(page, 4); strings.Contains(art, ">Deleted<") || hasForm(art, "/p/4/restore") || !hasForm(art, "/p/4/delete") {
		t.Errorf("the restored post: %s", art)
	}
	if loc := getAs(t, h, "/p/4", b).Header().Get("Location"); loc != "/t/1#p4" {
		t.Errorf("GET /p/4 = %q", loc)
	}

	// Approve the held post.
	do("/p/3/approve", url.Values{}, "/p/3")
	page = check("after the approval", 3, 6, 3)
	if l := lastLog(); l != (logRow{2, "post.approve", "post", 3}) {
		t.Errorf("approve log = %+v", l)
	}
	if art := articleOf(page, 3); strings.Contains(art, "Waiting for approval") || hasForm(art, "/p/3/approve") {
		t.Errorf("the approved post still waits: %s", art)
	}

	// Lock the topic, then unlock it.
	do("/t/1/lock", url.Values{}, "/t/1")
	page = check("after the lock", 3, 6, 4)
	if l := lastLog(); l != (logRow{2, "topic.lock", "topic", 1}) {
		t.Errorf("lock log = %+v", l)
	}
	if !hasForm(page, "/t/1/unlock") || hasForm(page, "/t/1/lock") || !strings.Contains(page, `<span class="tag">Locked</span>`) {
		t.Errorf("the locked topic offers Unlock only, and says Locked; forms %v", formActions(page))
	}
	do("/t/1/lock", url.Values{}, "/t/1") // a second click changes nothing
	check("after a second lock", 3, 6, 4)
	do("/t/1/unlock", url.Values{}, "/t/1")
	page = check("after the unlock", 3, 6, 5)
	if l := lastLog(); l != (logRow{2, "topic.unlock", "topic", 1}) {
		t.Errorf("unlock log = %+v", l)
	}
	if !hasForm(page, "/t/1/lock") || hasForm(page, "/t/1/unlock") || strings.Contains(page, `<span class="tag">Locked</span>`) {
		t.Error("the unlocked topic offers Lock only")
	}

	// The labels are the words of the brief.
	for _, label := range []string{">Lock<"} {
		if !strings.Contains(page, label) {
			t.Errorf("the page lacks the label %s", label)
		}
	}
}

// Each action is a small form with the token, and no script or style.
func TestModeratorFormsAreSmallPosts(t *testing.T) {
	h, pool := modSite(t)
	exec(t, pool, `update posts set visibility = 'deleted', visibility_before_delete = 'approved' where id = 4`)
	b := signIn(t, h, "bob", "example-password")
	rec := getAs(t, h, "/t/1", b)
	page := bodyOf(t, rec)
	for _, action := range []string{"/p/4/restore", "/p/3/approve", "/t/1/lock"} {
		open := `<form class="mark-form" method="post" action="` + action + `">`
		_, rest, ok := strings.Cut(page, open)
		if !ok {
			t.Fatalf("no POST form for %s", action)
		}
		form, _, _ := strings.Cut(rest, "</form>")
		if !csrfField.MatchString(form) {
			t.Errorf("the form %s carries no token: %s", action, form)
		}
		if strings.Contains(form, "style=") || strings.Contains(form, "onclick") || strings.Contains(form, "<script") {
			t.Errorf("the form %s holds script or style: %s", action, form)
		}
		if strings.Count(form, "<input") != 1 {
			t.Errorf("the form %s holds more than the token: %s", action, form)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp != wantBaseCSP {
		t.Errorf("policy %q", csp)
	}
	if extra := checkScripts(t, "/t/1", page, rec.Header().Get("Content-Security-Policy")); extra != 0 {
		t.Errorf("%d extra scripts", extra)
	}
}

func TestNoModControlsWithoutRights(t *testing.T) {
	h, pool := modSite(t)
	exec(t, pool, `update posts set visibility = 'deleted', visibility_before_delete = 'approved' where id = 4`)
	c := signIn(t, h, "carol", "example-password")
	for who, cookies := range map[string][]*http.Cookie{"carol": c, "a guest": nil} {
		page := bodyOf(t, getAs(t, h, "/t/1", cookies))
		for _, action := range formActions(page) {
			if strings.HasSuffix(action, "/restore") || strings.HasSuffix(action, "/approve") ||
				strings.HasSuffix(action, "/lock") || strings.HasSuffix(action, "/unlock") {
				t.Errorf("%s sees the control %s", who, action)
			}
			// Carol may delete her own held post, and nothing else.
			if strings.HasSuffix(action, "/delete") && (who != "carol" || action != "/p/3/delete") {
				t.Errorf("%s sees the control %s", who, action)
			}
		}
		if strings.Contains(page, ">Deleted<") || strings.Contains(page, "Hello from a guest.") {
			t.Errorf("%s sees the deleted post", who)
		}
	}
	if page := bodyOf(t, getAs(t, h, "/t/1", c)); !strings.Contains(articleOf(page, 3), "Waiting for approval") {
		t.Error("carol does not see the label on her held post")
	}

	before := dumpData(t, pool)
	for _, who := range []struct {
		name    string
		cookies []*http.Cookie
	}{{"carol", c}, {"a guest", nil}} {
		for _, k := range []struct {
			target string
			want   int
		}{
			{"/p/1/delete", 403}, {"/p/2/restore", 403}, {"/p/3/approve", 403}, {"/p/1/approve", 403},
			{"/t/1/lock", 403}, {"/t/1/unlock", 403},
			{"/p/5/restore", 404}, {"/p/5/approve", 404}, {"/t/2/lock", 404}, {"/t/2/unlock", 404},
			{"/p/9999/restore", 404}, {"/p/9999/approve", 404}, {"/t/9999/lock", 404}, {"/t/9999/unlock", 404},
		} {
			want := k.want
			// Carol's held post is not there for a guest.
			if who.cookies == nil && k.target == "/p/3/approve" {
				want = 404
			}
			rec := send(t, h, k.target, who.cookies, url.Values{"confirm": {"1"}})
			if rec.Code != want {
				t.Errorf("%s POST %s = %d, want %d", who.name, k.target, rec.Code, want)
			}
		}
		// The deleted post is hidden from a viewer without mod.delete: the answer is
		// the one for a missing post, and the page says no more.
		hidden := send(t, h, "/p/4/restore", who.cookies, url.Values{})
		missing := send(t, h, "/p/9999/restore", who.cookies, url.Values{})
		if hidden.Code != 404 || missing.Code != 404 {
			t.Errorf("%s: restore of a hidden post = %d, of a missing one = %d, want 404 twice", who.name, hidden.Code, missing.Code)
		}
		if a, b := shape(bodyOf(t, hidden), "/p/4/restore"), shape(bodyOf(t, missing), "/p/9999/restore"); a != b {
			t.Errorf("%s: the 404 of a hidden post differs from the 404 of a missing one", who.name)
		}
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("a refused POST changed %v", diff)
	}
	if len(modLog(t, pool)) != 0 {
		t.Error("a refused POST wrote a log row")
	}
}

func TestLockedTopicRefusesReply(t *testing.T) {
	h, pool := modSite(t)
	a := signIn(t, h, "alice", "example-password")
	b := signIn(t, h, "bob", "example-password")
	rec := send(t, h, "/t/1/lock", b, url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("lock = %d", rec.Code)
	}
	posts := countPostRows(t, pool)

	// The page offers no Reply to alice, and the form says why.
	page := bodyOf(t, getAs(t, h, "/t/1", a))
	if strings.Contains(page, `/t/1/reply"`) {
		t.Error("alice is offered Reply in a locked topic")
	}
	if g := getAs(t, h, "/t/1/reply", a); g.Code != http.StatusForbidden || !strings.Contains(bodyOf(t, g), "This topic is locked.") {
		t.Errorf("GET /t/1/reply = %d", g.Code)
	}

	const text = "I wrote this before it was locked.\n\nTwo <b>lines</b> & more."
	rec = send(t, h, "/t/1/reply", a, url.Values{"body": {text}})
	body := bodyOf(t, rec)
	if rec.Code != http.StatusForbidden || !strings.Contains(body, "This topic is locked.") {
		t.Fatalf("alice's reply = %d, want 403 with the notice\n%s", rec.Code, body)
	}
	if got, ok := textareaOf(t, body, "body"); !ok || got != text {
		t.Errorf("the form returns %q, want %q", got, text)
	}
	if countPostRows(t, pool) != posts {
		t.Error("a refused reply saved a post")
	}

	// A moderator still replies.
	rec = send(t, h, "/t/1/reply", b, url.Values{"body": {"A moderator speaks."}})
	if rec.Code != http.StatusSeeOther || countPostRows(t, pool) != posts+1 {
		t.Errorf("bob's reply = %d, posts %d -> %d", rec.Code, posts, countPostRows(t, pool))
	}
	if page := bodyOf(t, getAs(t, h, "/t/1", b)); !strings.Contains(page, `/t/1/reply"`) {
		t.Error("the moderator is not offered Reply in a locked topic")
	}

	// After the unlock alice replies.
	send(t, h, "/t/1/unlock", b, url.Values{})
	if rec = send(t, h, "/t/1/reply", a, url.Values{"body": {text}}); rec.Code != http.StatusSeeOther {
		t.Errorf("alice's reply after the unlock = %d", rec.Code)
	}

	// A locked forum says so, and a plain refusal does not say "locked".
	exec(t, pool, `update forums set locked = true where id = 2`)
	rec = send(t, h, "/t/1/reply", a, url.Values{"body": {"x"}})
	if rec.Code != http.StatusForbidden || !strings.Contains(bodyOf(t, rec), "This forum is locked.") {
		t.Errorf("reply in a locked forum = %d", rec.Code)
	}
	exec(t, pool, `update forums set locked = false where id = 2`)
	rec = send(t, h, "/t/1/reply", nil, url.Values{"body": {"x"}, "guest_name": {"Someone"}})
	if body := bodyOf(t, rec); rec.Code != http.StatusForbidden || strings.Contains(body, "is locked") {
		t.Errorf("a guest with no right: %d, mentions a lock: %v", rec.Code, strings.Contains(body, "is locked"))
	}
}

// An author who holds topic.lock_own locks the own topic, and unlocks only the
// own lock. The page offers what the board allows.
func TestAuthorLockButtons(t *testing.T) {
	h, pool := modSite(t)
	exec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (3, 2, 'topic.lock_own', 3)`)
	c := signIn(t, h, "carol", "example-password")
	b := signIn(t, h, "bob", "example-password")
	rec := send(t, h, "/f/2/post", c, url.Values{"title": {"Carol's topic"}, "body": {"Hello."}})
	topic := "/t/" + strconv.Itoa(mustTopic(t, pool, idOf(t, rec)))
	forms := func(cookies []*http.Cookie) []string { return formActions(bodyOf(t, getAs(t, h, topic, cookies))) }
	has := func(cookies []*http.Cookie, action string) bool { return slices.Contains(forms(cookies), action) }

	if !has(c, topic+"/lock") || has(c, topic+"/unlock") {
		t.Fatalf("the author of an open topic is offered %v", forms(c))
	}
	// Carol is not the author of topic 1.
	if slices.Contains(formActions(bodyOf(t, getAs(t, h, "/t/1", c))), "/t/1/lock") {
		t.Error("carol is offered Lock on another member's topic")
	}
	if rec := send(t, h, topic+"/lock", c, url.Values{}); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != topic {
		t.Fatalf("author lock = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if !has(c, topic+"/unlock") || has(c, topic+"/lock") {
		t.Errorf("the author of a topic that the author locked is offered %v", forms(c))
	}
	if rec := send(t, h, topic+"/unlock", c, url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("author unlock = %d", rec.Code)
	}
	// A moderator locks it: the author may not unlock.
	send(t, h, topic+"/lock", b, url.Values{})
	if has(c, topic+"/unlock") || has(c, topic+"/lock") {
		t.Errorf("the author of a topic that a moderator locked is offered %v", forms(c))
	}
	if rec := send(t, h, topic+"/unlock", c, url.Values{}); rec.Code != http.StatusForbidden {
		t.Errorf("author unlock of a moderator's lock = %d, want 403", rec.Code)
	}
	if !has(b, topic+"/unlock") {
		t.Error("the moderator is not offered Unlock")
	}
}

func TestModeratorEditReason(t *testing.T) {
	h, pool := modSite(t)
	b := signIn(t, h, "bob", "example-password")
	c := signIn(t, h, "carol", "example-password")
	reason := func(page string) bool { _, ok := inputValue(page, "reason"); return ok }

	// The field shows when the viewer edits another member's post.
	for _, k := range []struct {
		who     string
		cookies []*http.Cookie
		target  string
		want    bool
	}{
		{"bob, alice's first post", b, "/p/1/edit", true},
		{"bob, a guest's reply", b, "/p/4/edit", true},
		{"bob, his own reply", b, "/p/2/edit", false},
		{"carol, her own reply", c, "/p/3/edit", false},
	} {
		rec := getAs(t, h, k.target, k.cookies)
		page := bodyOf(t, rec)
		if rec.Code != http.StatusOK || reason(page) != k.want {
			t.Errorf("%s: GET %s = %d, reason field %v, want %v", k.who, k.target, rec.Code, reason(page), k.want)
		}
		if k.want && !strings.Contains(page, ">Edit reason<") {
			t.Errorf("%s: the field is not labeled Edit reason", k.who)
		}
	}

	// A reason with markup is stored as typed, and shown as text.
	const script = `<script>alert(1)</script> "quoted" & more`
	rec := send(t, h, "/p/4/edit", b, url.Values{"body": {"Edited by a moderator."}, "reason": {script}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/p/4" {
		t.Fatalf("moderator edit = %d %q\n%s", rec.Code, rec.Header().Get("Location"), bodyOf(t, rec))
	}
	grec := getAs(t, h, "/t/1", b)
	page := bodyOf(t, grec)
	art := articleOf(page, 4)
	_, noteRest, ok := strings.Cut(art, `<p class="edit-note">`)
	if !ok {
		t.Fatalf("the edited post has no edit note:\n%s", art)
	}
	note, _, _ := strings.Cut(noteRest, "</p>")
	if !strings.Contains(note, "Reason: &lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("the edit note does not escape the reason: %s", note)
	}
	if got := plainText(note); !strings.Contains(got, "Reason: "+script) {
		t.Errorf("the note reads %q, want the reason as plain text", got)
	}
	if strings.Contains(page, "<script>alert") {
		t.Error("the page holds the script of the reason")
	}
	if extra := checkScripts(t, "/t/1", page, grec.Header().Get("Content-Security-Policy")); extra != 0 {
		t.Errorf("%d extra scripts", extra)
	}
	// The edit form shows the reason back as an attribute, escaped.
	if rec := getAs(t, h, "/p/4/edit", b); strings.Contains(bodyOf(t, rec), "<script>alert") {
		t.Error("the edit form holds the script of the reason")
	}

	var stored, details string
	if err := pool.QueryRow(t.Context(), `select edit_reason from posts where id = 4`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != script {
		t.Errorf("stored reason = %q", stored)
	}
	if err := pool.QueryRow(t.Context(), `select details->>'reason' from mod_log where action = 'post.edit'`).Scan(&details); err != nil {
		t.Fatal(err)
	}
	if details != script {
		t.Errorf("logged reason = %q", details)
	}
	if l := modLog(t, pool); len(l) != 1 || l[0] != (logRow{2, "post.edit", "post", 4}) {
		t.Errorf("log = %+v", l)
	}

	// 255 characters pass, 256 fail with the form and the text.
	long := strings.Repeat("é", 256)
	rec = send(t, h, "/p/4/edit", b, url.Values{"body": {"Second **edit**."}, "reason": {long}})
	body := bodyOf(t, rec)
	if rec.Code != http.StatusBadRequest || !strings.Contains(body, "The reason is too long") {
		t.Fatalf("a 256-character reason = %d", rec.Code)
	}
	if got, _ := inputValue(body, "reason"); got != long {
		t.Error("the form lost the reason")
	}
	if got, _ := textareaOf(t, body, "body"); got != "Second **edit**." {
		t.Errorf("the form returns %q", got)
	}
	if rec = send(t, h, "/p/4/edit", b, url.Values{"body": {"Second **edit**."}, "reason": {long[:len(long)-2]}}); rec.Code != http.StatusSeeOther {
		t.Errorf("a 255-character reason = %d", rec.Code)
	}

	// An author's own edit stores no reason, whatever the form says.
	rec = send(t, h, "/p/3/edit", c, url.Values{"body": {"Carol edits."}, "reason": {"forged"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("own edit = %d", rec.Code)
	}
	if err := pool.QueryRow(t.Context(), `select edit_reason from posts where id = 3`).Scan(&stored); err != nil || stored != "" {
		t.Errorf("own edit stored the reason %q (%v)", stored, err)
	}
}
