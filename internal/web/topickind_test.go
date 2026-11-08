package web

import (
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	kindOptionRe = regexp.MustCompile(`<option value="([a-z]+)"( selected)?>([A-Za-z]+)</option>`)
	topicLinkRe  = regexp.MustCompile(`<a class="topic-link" href="/t/(\d+)">`)
)

// kindSiteWithPin is modSite with mod.pin for bob in forum 2. alice is the admin
// of the small board, carol a plain member. Topic 1 is sticky.
func kindSiteWithPin(t *testing.T) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	h, pool := modSite(t)
	exec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (2, 2, 'mod.pin', 3)`)
	return h, pool
}

// kindForm returns the "Topic type" form of a page whose action is action, or "".
func kindForm(page, action string) string {
	_, rest, ok := strings.Cut(page, `<form class="mark-form kind-form" method="post" action="`+action+`">`)
	if !ok {
		return ""
	}
	form, _, _ := strings.Cut(rest, "</form>")
	return form
}

// kindSelect lists the options of the first "kind" select in html as
// "value=Label", with a "*" after the selected one.
func kindSelect(html string) []string {
	_, rest, ok := strings.Cut(html, `name="kind">`)
	if !ok {
		return nil
	}
	sel, _, _ := strings.Cut(rest, "</select>")
	var out []string
	for _, m := range kindOptionRe.FindAllStringSubmatch(sel, -1) {
		o := m[1] + "=" + m[3]
		if m[2] != "" {
			o += "*"
		}
		out = append(out, o)
	}
	return out
}

// storedKind reads the type of topic id.
func storedKind(t *testing.T, pool *pgxpool.Pool, id int) string {
	t.Helper()
	var k string
	if err := pool.QueryRow(t.Context(), `select kind from topics where id = $1`, id).Scan(&k); err != nil {
		t.Fatal(err)
	}
	return k
}

// listedTopics lists the topic IDs of a forum page in order: the pinned topics,
// then the page.
func listedTopics(page string) []int {
	var out []int
	for _, m := range topicLinkRe.FindAllStringSubmatch(page, -1) {
		n, _ := strconv.Atoi(m[1])
		out = append(out, n)
	}
	return out
}

func TestTopicKindControl(t *testing.T) {
	h, pool := kindSiteWithPin(t)
	a := signIn(t, h, "alice", "example-password")
	b := signIn(t, h, "bob", "example-password")
	c := signIn(t, h, "carol", "example-password")

	// The moderator: a POST form with the token, a labelled select with the three
	// types and the current one selected, and a submit button.
	page := bodyOf(t, getAs(t, h, "/t/1", b))
	form := kindForm(page, "/t/1/type")
	if form == "" {
		t.Fatalf("the moderator's page has no type form; forms %v", formActions(page))
	}
	if !csrfField.MatchString(form) {
		t.Errorf("the type form carries no token: %s", form)
	}
	if !strings.Contains(form, `<label for="topic-kind">Topic type</label>`) || !strings.Contains(form, `<select id="topic-kind" name="kind">`) {
		t.Errorf("the select has no label: %s", form)
	}
	if !strings.Contains(form, `<button type="submit" class="button-quiet">Set</button>`) {
		t.Errorf("the type form has no submit button: %s", form)
	}
	if strings.Contains(form, "style=") || strings.Contains(form, "onchange") || strings.Contains(form, "<script") {
		t.Errorf("the type form holds script or style: %s", form)
	}
	if got, want := kindSelect(form), []string{"normal=Normal", "sticky=Sticky*", "announce=Announcement"}; !slices.Equal(got, want) {
		t.Errorf("moderator options %v, want %v", got, want)
	}
	// The form sits in the row of Lock, before the page links.
	_, row, _ := strings.Cut(page, `<div class="head-row">`)
	row, _, _ = strings.Cut(row, `<nav`)
	if !strings.Contains(row, `action="/t/1/lock"`) || !strings.Contains(row, `action="/t/1/type"`) {
		t.Errorf("Lock and the type form do not share the head row: %s", row)
	}

	// The admin without mod.pin may only make the topic global. With mod.pin,
	// the admin gets all four.
	if got, want := kindSelect(kindForm(bodyOf(t, getAs(t, h, "/t/1", a)), "/t/1/type")),
		[]string{"sticky=Sticky*", "global=Global"}; !slices.Equal(got, want) {
		t.Errorf("admin options %v, want %v", got, want)
	}
	exec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (1, 2, 'mod.pin', 3)`)
	if got, want := kindSelect(kindForm(bodyOf(t, getAs(t, h, "/t/1", a)), "/t/1/type")),
		[]string{"normal=Normal", "sticky=Sticky*", "announce=Announcement", "global=Global"}; !slices.Equal(got, want) {
		t.Errorf("admin with mod.pin options %v, want %v", got, want)
	}

	// A plain member and a guest see no type control.
	for who, cookies := range map[string][]*http.Cookie{"carol": c, "a guest": nil} {
		page := bodyOf(t, getAs(t, h, "/t/1", cookies))
		if slices.Contains(formActions(page), "/t/1/type") || strings.Contains(page, "Topic type") {
			t.Errorf("%s sees the type control", who)
		}
	}
}

func TestTopicKindPost(t *testing.T) {
	h, pool := kindSiteWithPin(t)
	a := signIn(t, h, "alice", "example-password")
	b := signIn(t, h, "bob", "example-password")
	c := signIn(t, h, "carol", "example-password")

	// A new topic in forum 2, newer than topic 1, so the order shows the type.
	rec := send(t, h, "/f/2/post", c, url.Values{"title": {"A newer topic"}, "body": {"Hello."}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("new topic = %d", rec.Code)
	}
	var newer int
	if err := pool.QueryRow(t.Context(), `select id from topics where title = 'A newer topic'`).Scan(&newer); err != nil {
		t.Fatal(err)
	}
	order := func(step string, want ...int) {
		t.Helper()
		if got := listedTopics(bodyOf(t, getAs(t, h, "/f/2", b))); !slices.Equal(got, want) {
			t.Errorf("%s: forum 2 lists %v, want %v", step, got, want)
		}
	}
	order("sticky topic 1", 1, newer)

	do := func(cookies []*http.Cookie, kind string, wantCode int) {
		t.Helper()
		rec := send(t, h, "/t/1/type", cookies, url.Values{"kind": {kind}})
		if rec.Code != wantCode {
			t.Fatalf("POST /t/1/type kind=%s = %d, want %d\n%s", kind, rec.Code, wantCode, bodyOf(t, rec))
		}
		if wantCode == http.StatusSeeOther && rec.Header().Get("Location") != "/t/1" {
			t.Errorf("POST /t/1/type kind=%s goes to %q, want /t/1", kind, rec.Header().Get("Location"))
		}
	}

	do(b, "normal", http.StatusSeeOther)
	if k := storedKind(t, pool, 1); k != "normal" {
		t.Errorf("topic 1 is %q, want normal", k)
	}
	order("normal topic 1", newer, 1)
	if l := modLog(t, pool); len(l) != 1 || l[0] != (logRow{2, "topic.kind", "topic", 1}) {
		t.Errorf("log after normal = %+v", l)
	}

	do(b, "announce", http.StatusSeeOther)
	if k := storedKind(t, pool, 1); k != "announce" {
		t.Errorf("topic 1 is %q, want announce", k)
	}
	order("announcement topic 1", 1, newer)
	page := bodyOf(t, getAs(t, h, "/t/1", b))
	if got, want := kindSelect(kindForm(page, "/t/1/type")), []string{"normal=Normal", "sticky=Sticky", "announce=Announcement*"}; !slices.Equal(got, want) {
		t.Errorf("after announce, options %v, want %v", got, want)
	}
	if !strings.Contains(page, `<span class="tag tag-announce">Announcement</span>`) {
		t.Error("the topic page does not say Announcement")
	}

	// The moderator may not set global. A value that is no type is a bad request.
	// A plain member, a guest, and a missing or hidden topic are refused. None of
	// these writes anything.
	before := dumpData(t, pool)
	do(b, "global", http.StatusForbidden)
	do(b, "pinned", http.StatusBadRequest)
	do(b, "", http.StatusBadRequest)
	// The rights and the topic come before the value: a viewer who may not
	// change the type gets 403, and a missing or hidden topic 404, whatever the
	// value.
	for _, kind := range []string{"normal", "pinned", ""} {
		do(c, kind, http.StatusForbidden)
		do(nil, kind, http.StatusForbidden)
		for _, target := range []string{"/t/9999/type", "/t/2/type"} {
			if rec := send(t, h, target, c, url.Values{"kind": {kind}}); rec.Code != http.StatusNotFound {
				t.Errorf("carol POST %s kind=%q = %d, want 404", target, kind, rec.Code)
			}
		}
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("a refused POST changed %v", diff)
	}

	// The admin sets global; the moderator then has no control and no right.
	do(a, "global", http.StatusSeeOther)
	if k := storedKind(t, pool, 1); k != "global" {
		t.Errorf("topic 1 is %q, want global", k)
	}
	order("global topic 1", 1, newer)
	if page := bodyOf(t, getAs(t, h, "/t/1", b)); kindForm(page, "/t/1/type") != "" {
		t.Error("the moderator is offered the type control on a global topic")
	}
	do(b, "normal", http.StatusForbidden)

	want := []logRow{{2, "topic.kind", "topic", 1}, {2, "topic.kind", "topic", 1}, {1, "topic.kind", "topic", 1}}
	if l := modLog(t, pool); !slices.Equal(l, want) {
		t.Errorf("log = %+v, want %+v", l, want)
	}
	wantCountersExact(t, pool, "changes of type")
}

// TestTopicKindNeedsToken: the type form refuses a POST with no token or a wrong
// one, and changes nothing.
func TestTopicKindNeedsToken(t *testing.T) {
	h, pool := kindSiteWithPin(t)
	b := signIn(t, h, "bob", "example-password")
	before := dumpData(t, pool)
	for what, form := range map[string]url.Values{
		"no token":    {"kind": {"announce"}},
		"wrong token": {"kind": {"announce"}, "csrf": {"nope"}},
	} {
		if rec := request(t, h, http.MethodPost, "/t/1/type", b, form); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", what, rec.Code)
		}
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("a POST with no good token changed %v", diff)
	}
	if k := storedKind(t, pool, 1); k != "sticky" {
		t.Errorf("topic 1 is %q, want sticky", k)
	}
	// The control: the same POST with the token works.
	if rec := send(t, h, "/t/1/type", b, url.Values{"kind": {"announce"}}); rec.Code != http.StatusSeeOther || storedKind(t, pool, 1) != "announce" {
		t.Errorf("with the token: %d, topic 1 %q", rec.Code, storedKind(t, pool, 1))
	}
}

// TestNewTopicKindField: the new-topic form offers the type to the same viewers
// as the topic page, Normal first and selected, and a forged type from a viewer
// without the right is not applied.
func TestNewTopicKindField(t *testing.T) {
	h, pool := kindSiteWithPin(t)
	a := signIn(t, h, "alice", "example-password")
	b := signIn(t, h, "bob", "example-password")
	c := signIn(t, h, "carol", "example-password")

	if page := bodyOf(t, getAs(t, h, "/f/2/post", c)); strings.Contains(page, `name="kind"`) || strings.Contains(page, "Topic type") {
		t.Error("a plain member's new-topic form offers a type")
	}
	page := bodyOf(t, getAs(t, h, "/f/2/post", b))
	if !strings.Contains(page, `<label for="post-kind">Topic type</label>`) {
		t.Error("the moderator's new-topic form has no labelled type field")
	}
	if got, want := kindSelect(page), []string{"normal=Normal*", "sticky=Sticky", "announce=Announcement"}; !slices.Equal(got, want) {
		t.Errorf("moderator form options %v, want %v", got, want)
	}
	// The admin, with no mod.pin, topic.sticky or topic.announce here, gets Global only.
	if got, want := kindSelect(bodyOf(t, getAs(t, h, "/f/2/post", a))),
		[]string{"normal=Normal*", "global=Global"}; !slices.Equal(got, want) {
		t.Errorf("admin form options %v, want %v", got, want)
	}
	// A reply form never offers a type.
	if page := bodyOf(t, getAs(t, h, "/t/1/reply", a)); strings.Contains(page, `name="kind"`) {
		t.Error("the reply form offers a type")
	}

	// A preview keeps the type that was sent.
	rec := send(t, h, "/f/2/post", b, url.Values{"title": {"Preview"}, "body": {"Text."}, "kind": {"announce"}, "preview": {"1"}})
	if got, want := kindSelect(bodyOf(t, rec)), []string{"normal=Normal", "sticky=Sticky", "announce=Announcement*"}; rec.Code != http.StatusOK || !slices.Equal(got, want) {
		t.Errorf("preview = %d, options %v, want %v", rec.Code, got, want)
	}

	start := func(cookies []*http.Cookie, title, kind string) string {
		t.Helper()
		rec := send(t, h, "/f/2/post", cookies, url.Values{"title": {title}, "body": {"Body of " + title}, "kind": {kind}})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%s: %d\n%s", title, rec.Code, bodyOf(t, rec))
		}
		var k string
		if err := pool.QueryRow(t.Context(), `select kind from topics where title = $1`, title).Scan(&k); err != nil {
			t.Fatal(err)
		}
		return k
	}
	for _, k := range []string{"sticky", "announce", "global"} {
		if got := start(c, "Forged by carol "+k, k); got != "normal" {
			t.Errorf("carol forges %s: topic is %q, want normal", k, got)
		}
	}
	if n := len(modLog(t, pool)); n != 0 {
		t.Errorf("forged types wrote %d log rows", n)
	}
	if got := start(b, "Bob default", "normal"); got != "normal" {
		t.Errorf("bob normal: %q", got)
	}
	if got := start(b, "Bob sticky", "sticky"); got != "sticky" {
		t.Errorf("bob sticky: %q", got)
	}
	if got := start(b, "Bob forges global", "global"); got != "normal" {
		t.Errorf("bob forges global: %q, want normal", got)
	}
	if got := start(a, "Alice global", "global"); got != "global" {
		t.Errorf("alice global: %q", got)
	}
	if n := len(modLog(t, pool)); n != 2 {
		t.Errorf("%d log rows, want 2 (bob's sticky and alice's global)", n)
	}
}

// TestTopicStickyRightOnForms: a member with topic.sticky and no mod.pin gets
// Sticky on the new-topic form and starts a sticky topic. The member gets no
// Announcement, and no type control on the topic page.
func TestTopicStickyRightOnForms(t *testing.T) {
	h, pool := modSite(t)
	exec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (3, 2, 'topic.sticky', 3)`)
	c := signIn(t, h, "carol", "example-password")
	if got, want := kindSelect(bodyOf(t, getAs(t, h, "/f/2/post", c))), []string{"normal=Normal*", "sticky=Sticky"}; !slices.Equal(got, want) {
		t.Errorf("form options %v, want %v", got, want)
	}
	rec := send(t, h, "/f/2/post", c, url.Values{"title": {"Carol sticky"}, "body": {"Pinned by carol."}, "kind": {"sticky"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("new sticky topic = %d", rec.Code)
	}
	rec = send(t, h, "/f/2/post", c, url.Values{"title": {"Carol announce"}, "body": {"Forged."}, "kind": {"announce"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("new topic with a forged announce = %d", rec.Code)
	}
	kinds := map[string]string{}
	var stickyID int
	rows, err := pool.Query(t.Context(), `select id, title, kind from topics where title like 'Carol %'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int
		var title, kind string
		if err := rows.Scan(&id, &title, &kind); err != nil {
			t.Fatal(err)
		}
		kinds[title] = kind
		if title == "Carol sticky" {
			stickyID = id
		}
	}
	rows.Close()
	if kinds["Carol sticky"] != "sticky" || kinds["Carol announce"] != "normal" {
		t.Errorf("topic types %v, want sticky and normal", kinds)
	}
	target := "/t/" + strconv.Itoa(stickyID)
	if page := bodyOf(t, getAs(t, h, target, c)); kindForm(page, target+"/type") != "" || strings.Contains(page, "Topic type") {
		t.Error("carol sees the type control on her sticky topic")
	}
	if rec := send(t, h, target+"/type", c, url.Values{"kind": {"normal"}}); rec.Code != http.StatusForbidden {
		t.Errorf("carol changes her topic's type: %d, want 403", rec.Code)
	}
	if got := storedKind(t, pool, stickyID); got != "sticky" {
		t.Errorf("carol's topic is %q, want sticky", got)
	}
}
