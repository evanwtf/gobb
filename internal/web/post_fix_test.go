package web

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/bundletest"
)

const (
	wantServerError = "Something went wrong, and your post was not saved. Your text is below. Try again in a moment."
	wantGone        = "This topic is no longer available. Copy your text before you leave this page."
	wantExpired     = "This form expired. Use your browser's Back button to get your text back, then reload the page and post again."
)

// exec runs SQL on the test database.
func exec(t *testing.T, pool *pgxpool.Pool, sql string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql); err != nil {
		t.Fatal(err)
	}
}

// text reads the page as plain text with the character references resolved.
func pageText(page string) string { return html.UnescapeString(page) }

// formHolds fails the test unless the page is a form page that holds want in
// the text box byte for byte, with the notice.
func formHolds(t *testing.T, where, page, want, notice string) {
	t.Helper()
	if got, ok := textareaOf(t, page, "body"); !ok || got != want {
		t.Errorf("%s: the text box holds %d bytes (found %v), want the %d bytes that were sent", where, len(got), ok, len(want))
	}
	if !strings.Contains(pageText(page), notice) {
		t.Errorf("%s: the page lacks the notice %q", where, notice)
	}
}

// A database error during a save answers 500 with the form, the exact bytes and
// a fixed notice that carries no detail.
func TestSaveErrorKeepsTheText(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", "example-password")
	exec(t, pool, `create function p4_boom() returns trigger language plpgsql as $$ begin raise exception 'boom: secret detail'; end $$;
create trigger p4_boom before insert on posts for each row execute function p4_boom()`)
	body := longBody()
	for _, c := range []struct {
		name, target string
		form         url.Values
	}{
		{"new topic", "/f/2/post", url.Values{"title": {"A title"}, "body": {body}}},
		{"reply", "/t/1/reply", url.Values{"body": {body}}},
	} {
		rec := send(t, h, c.target, a, c.form)
		page := bodyOf(t, rec)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: status %d, want 500", c.name, rec.Code)
		}
		formHolds(t, c.name, page, body, wantServerError)
		if strings.Contains(page, "boom") || strings.Contains(page, "secret detail") {
			t.Errorf("%s: the page shows the detail of the error", c.name)
		}
		if !strings.Contains(page, `name="preview"`) {
			t.Errorf("%s: the page has no form to try again", c.name)
		}
	}
	// A title survives too.
	rec := send(t, h, "/f/2/post", a, url.Values{"title": {"Keep me"}, "body": {"x"}})
	if got, _ := inputValue(bodyOf(t, rec), "title"); got != "Keep me" {
		t.Errorf("the title field holds %q", got)
	}
}

// A database error during a preview answers 500 with the form and the bytes.
func TestPreviewErrorKeepsTheText(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", "example-password")
	// The parse reads the smilies; take the table away after the sign-in.
	exec(t, pool, `alter table smilies rename to smilies_gone`)
	body := longBody()
	rec := send(t, h, "/t/1/reply", a, url.Values{"body": {body}, "preview": {"1"}})
	page := bodyOf(t, rec)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", rec.Code)
	}
	formHolds(t, "preview", page, body, wantServerError)
	if strings.Contains(page, "smilies") {
		t.Error("the page shows the detail of the error")
	}
}

// A request that runs out of time during the save answers 503 with the form.
func TestSaveTimeoutKeepsTheText(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", "example-password")
	exec(t, pool, `create function p4_slow() returns trigger language plpgsql as $$ begin perform pg_sleep(3); return new; end $$;
create trigger p4_slow before insert on posts for each row execute function p4_slow()`)
	old := requestTimeout
	requestTimeout = 700 * time.Millisecond
	t.Cleanup(func() { requestTimeout = old })
	body := longBody()
	rec := send(t, h, "/t/1/reply", a, url.Values{"body": {body}})
	page := bodyOf(t, rec)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", rec.Code)
	}
	formHolds(t, "timeout", page, body, wantServerError)
}

// When the page of the viewer cannot be built after the token passed (the
// settings cannot be read), the text still comes back, in a bare form.
func TestPageErrorGivesABareForm(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", "example-password")
	token := formToken(t, bodyOf(t, getAs(t, h, "/t/1/reply", a)))
	exec(t, pool, `alter table settings rename to settings_gone`)
	body := longBody()
	rec := request(t, h, http.MethodPost, "/t/1/reply", a, url.Values{"body": {body}, "csrf": {token}})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", rec.Code)
	}
	page := bodyOf(t, rec)
	formHolds(t, "page error", page, body, wantServerError)
	if strings.Contains(page, "settings") {
		t.Error("the page shows the detail of the error")
	}
}

// A target that goes away while the member writes: 404 and a bare form that holds
// only the text. The page names no topic, shows no crumbs and has no submit.
func TestGoneTargetGivesABareForm(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", "example-password")
	body := longBody()
	exec(t, pool, `update topics set visibility = 'deleted' where id = 1`)
	for _, c := range []struct {
		target string
		form   url.Values
	}{
		{"/t/1/reply", url.Values{"body": {body}}},
		{"/t/1/reply", url.Values{"body": {body}, "preview": {"1"}}},
		{"/t/9999/reply", url.Values{"body": {body}}},
		{"/f/99/post", url.Values{"title": {"t"}, "body": {body}}},
		{"/p/9999/edit", url.Values{"body": {body}}},
	} {
		rec := send(t, h, c.target, a, c.form)
		page := bodyOf(t, rec)
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST %s: status %d, want 404", c.target, rec.Code)
		}
		formHolds(t, "POST "+c.target, page, body, wantGone)
		for _, bad := range []string{"Welcome to the example board", `aria-label="Breadcrumb"`, `name="preview"`, `class="post-form"`, ">Post reply<", `name="title"`} {
			if strings.Contains(page, bad) {
				t.Errorf("POST %s: the bare form holds %q", c.target, bad)
			}
		}
	}
	// The hidden topic and the missing one give the same page.
	hidden := shape(bodyOf(t, send(t, h, "/t/1/reply", a, url.Values{"body": {"same"}})), "/t/1/reply")
	missing := shape(bodyOf(t, send(t, h, "/t/9999/reply", a, url.Values{"body": {"same"}})), "/t/9999/reply")
	if hidden != missing {
		t.Error("a hidden topic and a missing one give different pages")
	}
}

// A failed token or Cross-Origin check never echoes the body. The 403 says the
// form expired and where to get the text back. A session that ended while the
// member wrote reaches the same page.
func TestExpiredFormNeverEchoes(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", "example-password")
	const secret = "unique-secret-text-7731"
	form := url.Values{"title": {secret}, "body": {secret}, "csrf": {"wrong"}}
	check := func(where string, rec *httptest.ResponseRecorder) {
		t.Helper()
		page := bodyOf(t, rec)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", where, rec.Code)
		}
		if !strings.Contains(pageText(page), wantExpired) {
			t.Errorf("%s: the 403 lacks %q", where, wantExpired)
		}
		if strings.Contains(page, secret) || strings.Contains(page, "<textarea") {
			t.Errorf("%s: the page echoes the body", where)
		}
	}
	for _, target := range []string{"/f/2/post", "/t/1/reply", "/p/1/edit"} {
		check("wrong token "+target, request(t, h, http.MethodPost, target, a, form))
		r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		for _, c := range a {
			r.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		check("cross-site "+target, rec)
	}
	// Signed out while writing: the form is from a live session, the POST is not.
	token := formToken(t, bodyOf(t, getAs(t, h, "/t/1/reply", a)))
	exec(t, pool, `delete from sessions`)
	check("session ended", request(t, h, http.MethodPost, "/t/1/reply", a, url.Values{"body": {secret}, "csrf": {token}}))
	// A guest with no cookie.
	check("guest", request(t, h, http.MethodPost, "/t/1/reply", nil, form))
}

// Posting a body of 60,000 bytes made of 3-byte characters, with a 120-character
// title, works through the real form encoding (the encoded body is about 180 KB).
func TestLargestPostFitsTheCap(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", "example-password")
	body := strings.Repeat("€", 20_000) // 60,000 bytes
	title := strings.Repeat("é", 120)
	form := url.Values{"title": {title}, "body": {body}}
	if n := len(body); n != 60_000 {
		t.Fatalf("body is %d bytes", n)
	}
	if enc := len(form.Encode()); enc < 170_000 || enc > maxPostingBody {
		t.Fatalf("encoded form is %d bytes; the cap is %d", enc, maxPostingBody)
	}
	rec := send(t, h, "/f/2/post", a, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want 303\n%.300s", rec.Code, bodyOf(t, rec))
	}
	var n int
	if err := pool.QueryRow(t.Context(), `select length(body_source) from posts order by id desc limit 1`).Scan(&n); err != nil || n != 20_000 {
		t.Errorf("stored length = %d, %v", n, err)
	}
}

func hiddenMarkerBoard() *bundletest.Board {
	b := bundletest.Small()
	// Marker 3 stands in forum 2, which carol reads, and points at topic 2 in
	// forum 3, which she does not.
	for i := range b.Topics {
		if b.Topics[i].ID == 3 {
			b.Topics[i].ForumID = 2
			b.Topics[i].Title = "Secret marker title"
			b.Topics[i].MovedTo = ptr(int64(2))
		}
	}
	return b
}

// A moved-topic marker is never a reply target. The form, the preview and the
// save answer the 404 of a missing topic, and show no title.
func TestMarkerIsNotAReplyTarget(t *testing.T) {
	for name, b := range map[string]*bundletest.Board{"target hidden": hiddenMarkerBoard(), "target visible": markerToVisible()} {
		t.Run(name, func(t *testing.T) {
			h, _ := siteWithPool(t, b, Options{})
			c := signIn(t, h, "carol", "example-password")
			get404 := func(target string) string {
				rec := getAs(t, h, target, c)
				if rec.Code != http.StatusNotFound {
					t.Errorf("GET %s = %d, want 404", target, rec.Code)
				}
				return bodyOf(t, rec)
			}
			if shape(get404("/t/3/reply"), "/t/3/reply") != shape(get404("/t/9999/reply"), "/t/9999/reply") {
				t.Error("GET: the marker and a missing topic give different pages")
			}
			const text = "text for the marker"
			for _, form := range []url.Values{{"body": {text}}, {"body": {text}, "preview": {"1"}}} {
				rec := send(t, h, "/t/3/reply", c, form)
				page := bodyOf(t, rec)
				if rec.Code != http.StatusNotFound {
					t.Errorf("POST (preview %v) = %d, want 404", form.Has("preview"), rec.Code)
				}
				if strings.Contains(page, "Secret marker title") || strings.Contains(page, "Welcome to the example board") {
					t.Errorf("POST (preview %v): the page shows a title", form.Has("preview"))
				}
				miss := bodyOf(t, send(t, h, "/t/9999/reply", c, form))
				if shape(page, "/t/3/reply") != shape(miss, "/t/9999/reply") {
					t.Errorf("POST (preview %v): the marker and a missing topic give different pages", form.Has("preview"))
				}
			}
		})
	}
}

// markerToVisible is the small board with marker 3 moved into forum 2, pointing
// at topic 1 in forum 2.
func markerToVisible() *bundletest.Board {
	b := bundletest.Small()
	for i := range b.Topics {
		if b.Topics[i].ID == 3 {
			b.Topics[i].ForumID = 2
			b.Topics[i].Title = "Secret marker title"
		}
	}
	return b
}

// A quote of a post the viewer reads, from a forum where the viewer may not
// reply, goes into a topic where the viewer may reply (ruling T3-C1). The quoted
// post's own topic may be locked.
func TestQuoteAcrossForums(t *testing.T) {
	b := bundletest.Small()
	// Members read forum 3 and hold no topic.reply there.
	allowGroup(b, 2, 3, "forum.list")
	allowGroup(b, 2, 3, "forum.read")
	h, pool := siteWithPool(t, b, Options{})
	c := signIn(t, h, "carol", "example-password")
	// Post 5, in topic 2 of forum 3, is bob's.
	exec(t, pool, `update topics set locked = true where id = 2`)
	rec := getAs(t, h, "/t/1/reply?quote=5", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("quote across forums = %d, want 200\n%.300s", rec.Code, bodyOf(t, rec))
	}
	src, _ := textareaOf(t, bodyOf(t, rec), "body")
	if !strings.Contains(src, "> Which color do you like best?") {
		t.Errorf("the form holds %q", src)
	}
	rec = send(t, h, "/t/1/reply", c, url.Values{"body": {src + "Blue."}})
	if rec.Code != http.StatusSeeOther {
		t.Errorf("reply with the quote = %d", rec.Code)
	}
	// The reply rights of the target still apply.
	exec(t, pool, `update topics set locked = true where id = 1`)
	if rec := getAs(t, h, "/t/1/reply?quote=5", c); rec.Code != http.StatusForbidden {
		t.Errorf("quote into a locked topic = %d, want 403", rec.Code)
	}
}

// The edit fallback goes to the topic of the post, not to a topic with the
// number of the post.
func TestEditFallbackGoesToTheTopic(t *testing.T) {
	b := postingBoard()
	h, pool := siteWithPool(t, b, Options{})
	c := signIn(t, h, "carol", "example-password")
	// Post 3 is carol's own held reply in topic 1. After the save it is deleted,
	// so carol cannot see it, and /p/3 would be a 404.
	exec(t, pool, `create function p4_hide() returns trigger language plpgsql as $$ begin new.visibility := 'deleted'; return new; end $$;
create trigger p4_hide before update on posts for each row execute function p4_hide()`)
	rec := send(t, h, "/p/3/edit", c, url.Values{"body": {"changed"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/t/1" {
		t.Errorf("edit fallback = %d %q, want 303 /t/1", rec.Code, rec.Header().Get("Location"))
	}
}

// A preview runs the checks of a save: the title, the guest name, the reason.
func TestPreviewChecksTheTitleAndTheName(t *testing.T) {
	long := strings.Repeat("t", 121)
	h, _ := siteWithPool(t, guestBoard(false), Options{})
	rec := postGuest(t, h, "/f/2/post", nil, url.Values{"title": {long}, "body": {"x"}, "guest_name": {"Visitor"}, "preview": {"1"}})
	page := bodyOf(t, rec)
	if rec.Code != http.StatusBadRequest || !strings.Contains(page, "The title is longer than 120 characters.") || strings.Contains(page, `class="post-body"`) {
		t.Errorf("preview with a long title = %d", rec.Code)
	}
	rec = postGuest(t, h, "/f/2/post", nil, url.Values{"title": {"ok"}, "body": {"x"}, "guest_name": {"Alice"}, "preview": {"1"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(bodyOf(t, rec), "That name belongs to a member.") {
		t.Errorf("preview with a member's name = %d", rec.Code)
	}
	rec = postGuest(t, h, "/f/2/post", nil, url.Values{"title": {"ok"}, "body": {"x"}, "guest_name": {"Visitor"}, "preview": {"1"}})
	if rec.Code != http.StatusOK {
		t.Errorf("preview with good fields = %d", rec.Code)
	}

	// A moderator's edit of another member's post needs a reason of 255 characters
	// at most: the preview says so too. The title rule applies to a first post only.
	h2, pool := siteWithPool(t, postingBoard(), Options{})
	exec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (2, 2, 'mod.edit', 3)`)
	m := signIn(t, h2, "bob", "example-password")
	rec = send(t, h2, "/p/1/edit", m, url.Values{"title": {"fine"}, "body": {"x"}, "reason": {strings.Repeat("r", 256)}, "preview": {"1"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(bodyOf(t, rec), "The reason is too long") {
		t.Errorf("preview with a long reason = %d", rec.Code)
	}
	rec = send(t, h2, "/p/1/edit", m, url.Values{"title": {long}, "body": {"x"}, "reason": {"r"}, "preview": {"1"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(bodyOf(t, rec), "The title is longer than 120 characters.") {
		t.Errorf("edit preview with a long title = %d", rec.Code)
	}
	rec = send(t, h2, "/p/2/edit", m, url.Values{"title": {long}, "body": {"x"}, "preview": {"1"}})
	if rec.Code != http.StatusOK {
		t.Errorf("edit preview of a reply ignores the title: %d", rec.Code)
	}
}

// The links in the confirm and form pages carry no secret; the POST pages of
// every kind keep the strict policy.
func TestPostPagesKeepThePolicy(t *testing.T) {
	h, _ := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", "example-password")
	long := strings.Repeat("t", 121)
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"400":     send(t, h, "/f/2/post", a, url.Values{"title": {long}, "body": {"x"}}),
		"403":     send(t, h, "/p/2/edit", a, url.Values{"body": {"x"}}),
		"preview": send(t, h, "/t/1/reply", a, url.Values{"body": {"<script>alert(1)</script>"}, "preview": {"1"}}),
		"confirm": send(t, h, "/p/1/delete", a, url.Values{}),
		"404":     send(t, h, "/t/9999/reply", a, url.Values{"body": {"x"}}),
		"token":   request(t, h, http.MethodPost, "/t/1/reply", a, url.Values{"body": {"x"}}),
	} {
		csp := rec.Header().Get("Content-Security-Policy")
		checkScriptSrc(t, name, csp)
		if csp != wantBaseCSP {
			t.Errorf("%s (%d): policy %q, want the base policy", name, rec.Code, csp)
		}
	}
}

func TestPostBodiesSeesAStrayEnd(t *testing.T) {
	good := `<article class="post"><div class="post-main"><div class="post-body"><p>x <b>y</b></p></div>
    <p class="edit-note">Edited.</p>
    <div class="signature">sig</div>
  </div>
</article>`
	if bs, pr := postBodies(good); len(bs) != 1 || len(pr) != 0 || bs[0] != "<p>x <b>y</b></p>" {
		t.Errorf("good page: %q %q", bs, pr)
	}
	for name, page := range map[string]string{
		"markup after a stray end": `<article class="post"><div class="post-main"><div class="post-body"><p>x</p></div><img src=x onerror=alert(1)><p>y</p></div></div></article>`,
		"text after a stray end":   `<article class="post"><div class="post-main"><div class="post-body">x</div> loose text </div></div></article>`,
		"a second end":             `<article class="post"><div class="post-main"><div class="post-body">x</div></div><script src=x></script></article>`,
		"not closed":               `<article class="post"><div class="post-main"><div class="post-body"><p>x</p>`,
	} {
		if _, pr := postBodies(page); len(pr) == 0 {
			t.Errorf("%s: no problem reported", name)
		}
	}
}

func TestPolicyProblems(t *testing.T) {
	// A page with a YouTube embed. No embed adds a script origin (ruling R49).
	withEmbed := "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' https:; connect-src 'self'; font-src 'self'; frame-src https://www.youtube-nocookie.com; form-action 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'"
	if got := policyProblems(wantBaseCSP, wantBaseCSP, false); len(got) != 0 {
		t.Errorf("the base policy: %v", got)
	}
	if got := policyProblems(withEmbed, wantBaseCSP, true); len(got) != 0 {
		t.Errorf("a real embed policy: %v", got)
	}
	// Each change is refused on a page with no embed...
	for name, change := range map[string][2]string{
		"style-src":   {"style-src 'self'", "style-src 'self' 'unsafe-inline'"},
		"connect-src": {"connect-src 'self'", "connect-src 'self' https://x.example"},
		"script-src":  {"script-src 'self'", "script-src 'self' https://x.example"},
		"frame-src":   {"frame-src 'none'", "frame-src https://www.youtube-nocookie.com"},
		"img-src":     {"img-src 'self' https:", "img-src *"},
		"media-src":   {"object-src 'none'", "object-src 'none'; media-src *"},
	} {
		weak := strings.Replace(wantBaseCSP, change[0], change[1], 1)
		if got := policyProblems(weak, wantBaseCSP, false); len(got) == 0 {
			t.Errorf("no embed, %s changed: not reported", name)
		}
	}
	// ...and on a page with one, the directives that no embed explains stay fixed,
	// and the four that an embed explains may only hold what it needs.
	for name, change := range map[string][2]string{
		"img-src":          {"img-src 'self' https:", "img-src *"},
		"font-src":         {"font-src 'self'", "font-src 'self' https://x.example"},
		"worker-src":       {"object-src 'none'", "object-src 'none'; worker-src *"},
		"frame-src":        {"frame-src https://www.youtube-nocookie.com", "frame-src https://evil.example"},
		"script unsafe":    {"script-src 'self'", "script-src 'self' 'unsafe-inline'"},
		"script star":      {"script-src 'self'", "script-src *"},
		"connect any":      {"connect-src 'self'", "connect-src *"},
		"style any":        {"style-src 'self'", "style-src 'self' https://x.example"},
		"form-action":      {"form-action 'self'", "form-action *"},
		"script no self":   {"script-src 'self'", "script-src https://x.example"},
		"script with path": {"script-src 'self'", "script-src 'self' https://x.example/a.js"},
		// A bare https origin that the embed table does not hold (ruling T3 CSP).
		"script evil origin":  {"script-src 'self'", "script-src 'self' https://evil.example"},
		"connect evil origin": {"connect-src 'self'", "connect-src 'self' https://evil.example"},
		// The scripted providers add nothing any more (ruling R49).
		"script x origin":        {"script-src 'self'", "script-src 'self' https://platform.x.com"},
		"connect x origin":       {"connect-src 'self'", "connect-src 'self' https://syndication.twitter.com"},
		"script frame-only host": {"script-src 'self'", "script-src 'self' https://www.youtube-nocookie.com"},
	} {
		weak := strings.Replace(withEmbed, change[0], change[1], 1)
		if got := policyProblems(weak, wantBaseCSP, true); len(got) == 0 {
			t.Errorf("embed, %s changed: not reported", name)
		}
	}
}

// memberViewer returns the viewer for member id.
func memberViewer(t *testing.T, pool *pgxpool.Pool, id int64) board.Viewer {
	t.Helper()
	set, err := board.LoadSettings(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	v, err := board.Member(t.Context(), pool, id, set)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// A delete or a restore that committed always answers 303: the topic, else the
// forum, else the index.
func TestAfterDeleteTarget(t *testing.T) {
	var s *site
	onNew = func(got *site) { s = got }
	t.Cleanup(func() { onNew = nil })
	_, pool := siteWithPool(t, postingBoard(), Options{})
	onNew = nil
	ctx := t.Context()
	v := memberViewer(t, pool, 1)
	dt := board.DeleteForm{PostID: 1, TopicID: 1, ForumID: 2}
	if got := s.fallbackTarget(ctx, v, dt.TopicID, dt.ForumID); got != "/t/1" {
		t.Errorf("topic readable: %q", got)
	}
	exec(t, pool, `update topics set visibility = 'deleted' where id = 1`)
	if got := s.fallbackTarget(ctx, v, dt.TopicID, dt.ForumID); got != "/f/2" {
		t.Errorf("topic gone: %q", got)
	}
	dead, cancel := context.WithCancel(ctx)
	cancel()
	if got := s.fallbackTarget(dead, v, dt.TopicID, dt.ForumID); got != "/" {
		t.Errorf("database unreachable: %q", got)
	}
	if got := s.fallbackTarget(ctx, v, 1, 0); got != "/" {
		t.Errorf("no forum: %q", got)
	}
}

// The "form expired" page echoes nothing of the request: not the body, and not
// the path or the query either (they would land in the scheme picker and in the
// sign-in link).
func TestExpiredFormEchoesNoQuery(t *testing.T) {
	h, _ := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", "example-password")
	const marker = "query-marker-5521"
	form := url.Values{"body": {"x"}, "csrf": {"wrong"}}
	for name, cookies := range map[string][]*http.Cookie{"member": a, "guest": nil} {
		for _, target := range []string{"/t/1/reply?x=" + marker, "/f/2/post?" + marker + "=1", "/p/1/edit?next=" + marker} {
			rec := request(t, h, http.MethodPost, target, cookies, form)
			page := bodyOf(t, rec)
			if rec.Code != http.StatusForbidden || !strings.Contains(pageText(page), wantExpired) {
				t.Errorf("%s POST %s: status %d, expired text present: %v", name, target, rec.Code, strings.Contains(pageText(page), wantExpired))
			}
			if strings.Contains(page, marker) {
				t.Errorf("%s POST %s: the page echoes the query", name, target)
			}
			if strings.Contains(page, `action="/login`) || strings.Contains(page, `href="/login`) && strings.Contains(page, "next=") {
				t.Errorf("%s POST %s: the page carries a return address", name, target)
			}
		}
		// A cross-site request is the same page.
		r := httptest.NewRequest(http.MethodPost, "/t/1/reply?x="+marker, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		for _, c := range cookies {
			r.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if page := bodyOf(t, rec); rec.Code != http.StatusForbidden || strings.Contains(page, marker) {
			t.Errorf("%s cross-site: status %d, echoes the query: %v", name, rec.Code, strings.Contains(page, marker))
		}
	}
}
