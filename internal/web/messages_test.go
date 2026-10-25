package web

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/htmlcheck"
)

// messageSite is a site on the small board where alice's copy of message 2 is in
// her inbox again. The fixture marks it deleted, and the pages need a message
// with two live parties. Message 1 is alice to bob, with a blind copy to carol.
// Message 2 is bob to alice.
func messageSite(t *testing.T, opt Options) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	h, pool := siteWithPool(t, bundletest.Small(), opt)
	exec(t, pool, `update pm_recipients set is_deleted = false where pm_id = 2 and user_id = 1`)
	return h, pool
}

// grantBoardWide stores one option for a user, board-wide: 3 is yes, 2 is never.
func grantBoardWide(t *testing.T, pool *pgxpool.Pool, user int, setting int, option string) {
	t.Helper()
	exec(t, pool, fmt.Sprintf(`insert into acl_user_grants (user_id, forum_id, option, setting) values (%d, 0, '%s', %d)`, user, option, setting))
}

// asPageAs sends a GET as a top-level navigation, the only request that marks a
// message read.
func asPageAs(t *testing.T, h http.Handler, target string, cookies []*http.Cookie) string {
	t.Helper()
	rec := getWith(t, h, target, cookies, asPage)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d\n%s", target, rec.Code, bodyOf(t, rec))
	}
	return bodyOf(t, rec)
}

// sendMessage posts a message and returns the answer.
func sendMessage(t *testing.T, h http.Handler, cookies []*http.Cookie, to, subject, body string) *httpRecorder {
	t.Helper()
	rec, _ := postAs(t, h, "/messages/new", cookies, url.Values{"to": {to}, "subject": {subject}, "body": {body}})
	return rec
}

// httpRecorder is the type of the recorded answers, so the helpers read short.
type httpRecorder = httptest.ResponseRecorder

var messageLocation = regexp.MustCompile(`^/messages/(\d+)$`)

// sentID reads the ID of a sent message from the 303 that answered the send.
func sentID(t *testing.T, rec *httpRecorder) int64 {
	t.Helper()
	m := messageLocation.FindStringSubmatch(rec.Header().Get("Location"))
	if rec.Code != http.StatusSeeOther || m == nil {
		t.Fatalf("send = %d %q\n%s", rec.Code, rec.Header().Get("Location"), bodyOf(t, rec))
	}
	id, _ := strconv.ParseInt(m[1], 10, 64)
	return id
}

var tagText = regexp.MustCompile(`<[^>]*>`)

func TestMessagesFlow(t *testing.T) {
	h, _ := messageSite(t, Options{})
	alice := signIn(t, h, "alice", testPassword)
	bob := signIn(t, h, "bob", testPassword)

	// Alice opens the profile of bob and follows "Send a message".
	profile := bodyOf(t, getAs(t, h, "/u/2", alice))
	m := regexp.MustCompile(`href="([^"]*)">Send a message</a>`).FindStringSubmatch(profile)
	if m == nil {
		t.Fatalf("the profile has no \"Send a message\" link:\n%s", profile)
	}
	if m[1] != "/messages/new?to=bob" {
		t.Errorf("the link is %q", m[1])
	}
	form := bodyOf(t, getAs(t, h, html.UnescapeString(m[1]), alice))
	if to, ok := inputValue(form, "to"); !ok || to != "bob" {
		t.Fatalf("the To field holds %q, %v", to, ok)
	}
	// Alice sends. The answer is 303 to the new message.
	id := sentID(t, sendMessage(t, h, alice, "bob", "Lunch?", "Noon at the usual place?"))
	target := "/messages/" + strconv.FormatInt(id, 10)

	// Bob's nav shows one new message, in the markup that the layout writes.
	if !strings.Contains(bodyOf(t, getAs(t, h, "/", bob)), `Messages <span class="badge-new">1 new</span>`) {
		t.Error("bob's nav lacks the \"1 new\" pill")
	}
	// Alice has none: her copy of message 2 is read.
	if body := bodyOf(t, getAs(t, h, "/", alice)); !strings.Contains(body, `>Messages</a>`) || strings.Contains(body, `class="badge-new">`) {
		t.Error("alice's nav shows a pill or no Messages link")
	}
	// A GET that is not a navigation reads nothing.
	if rec := getAs(t, h, target, bob); rec.Code != http.StatusOK {
		t.Fatalf("plain GET %s = %d", target, rec.Code)
	}
	if !strings.Contains(bodyOf(t, getAs(t, h, "/", bob)), `<span class="badge-new">1 new</span>`) {
		t.Error("a GET with no fetch metadata marked the message read")
	}
	// Bob opens it. The page, and the next page, show no pill.
	page := asPageAs(t, h, target, bob)
	if !strings.Contains(page, "Lunch?") || !strings.Contains(page, "Noon at the usual place?") {
		t.Errorf("the message page lacks the message:\n%s", page)
	}
	if strings.Contains(page, `class="badge-new">`) {
		t.Error("the page that opens the message still shows the pill")
	}
	if body := bodyOf(t, getAs(t, h, "/", bob)); strings.Contains(body, `class="badge-new">`) || !strings.Contains(body, `>Messages</a>`) {
		t.Error("the pill stays after bob opened the message")
	}

	// Bob quotes it and sends.
	reply := bodyOf(t, getAs(t, h, target+"/reply?quote=1", bob))
	to, _ := inputValue(reply, "to")
	subject, _ := inputValue(reply, "subject")
	quote, _ := textareaOf(t, reply, "body")
	if to != "alice" || subject != "Re: Lunch?" || !strings.Contains(quote, "> Noon at the usual place?") {
		t.Fatalf("the quote form holds to=%q subject=%q body=%q", to, subject, quote)
	}
	rid := sentID(t, sendMessage(t, h, bob, to, subject, quote+"Noon is fine."))

	// Alice's inbox lists it. She opens it and sees the quote.
	inbox := bodyOf(t, getAs(t, h, "/messages", alice))
	if !strings.Contains(inbox, "Re: Lunch?") {
		t.Errorf("alice's inbox lacks the reply:\n%s", inbox)
	}
	page = asPageAs(t, h, "/messages/"+strconv.FormatInt(rid, 10), alice)
	q := regexp.MustCompile(`(?s)<blockquote>(.*?)</blockquote>`).FindStringSubmatch(page)
	if q == nil || !strings.Contains(html.UnescapeString(tagText.ReplaceAllString(q[1], "")), "alice wrote") {
		t.Errorf("the reply has no quote of alice:\n%s", page)
	}
	if !strings.Contains(page, "Noon is fine.") {
		t.Error("the reply lacks its own text")
	}
}

func TestMessageLists(t *testing.T) {
	h, _ := messageSite(t, Options{MessageSends: 1000, MemberPosts: 1000})
	alice := signIn(t, h, "alice", testPassword)
	bob := signIn(t, h, "bob", testPassword)

	inbox := bodyOf(t, getAs(t, h, "/messages", alice))
	for _, want := range []string{"Re: Welcome aboard", `href="/messages/2"`, `href="/u/2"`, `href="/messages/sent"`, `aria-current="page">Inbox`} {
		if !strings.Contains(inbox, want) {
			t.Errorf("alice's inbox lacks %q", want)
		}
	}
	if strings.Contains(inbox, "Welcome aboard</a>") && strings.Contains(inbox, `href="/messages/1"`) {
		t.Error("alice's inbox lists message 1, which she wrote")
	}
	sent := bodyOf(t, getAs(t, h, "/messages/sent", alice))
	if !strings.Contains(sent, `href="/messages/1"`) || !strings.Contains(sent, `href="/u/2"`) || !strings.Contains(sent, `aria-current="page">Sent`) {
		t.Errorf("alice's sent list is wrong:\n%s", sent)
	}
	// The blind copy is not in the list of the author.
	if strings.Contains(sent, "carol") {
		t.Error("the sent list names a blind-copy recipient")
	}
	// The unread row is marked until bob opens it.
	unread := bodyOf(t, getAs(t, h, "/messages", bob))
	if rowOf(unread, "topic-row row-new", "Welcome aboard") != "" {
		t.Error("bob's message 1 is read in the fixture, but the row is marked new")
	}
	sendMessage(t, h, alice, "bob", "Fresh", "New text")
	unread = bodyOf(t, getAs(t, h, "/messages", bob))
	if rowOf(unread, "topic-row row-new", "Fresh") == "" {
		t.Errorf("the unread message has no new row:\n%s", unread)
	}

	// Bob holds 27 messages: the old one, "Fresh" and 25 more. That makes two pages.
	for i := range 25 {
		sentID(t, sendMessage(t, h, alice, "bob", fmt.Sprintf("Bulk %02d", i), fmt.Sprintf("Text %d", i)))
	}
	p1 := bodyOf(t, getAs(t, h, "/messages", bob))
	p2 := bodyOf(t, getAs(t, h, "/messages?page=2", bob))
	if n := strings.Count(p1, `<tr class="topic-row`); n != 25 {
		t.Errorf("page 1 has %d rows, want 25", n)
	}
	if n := strings.Count(p2, `<tr class="topic-row`); n != 2 || !strings.Contains(p2, "Welcome aboard") {
		t.Errorf("page 2 has %d rows, want the two oldest", n)
	}
	if !strings.Contains(p1, `href="/messages?page=2"`) {
		t.Error("page 1 has no link to page 2")
	}
	if rec := getAs(t, h, "/messages?page=3", bob); rec.Code != http.StatusNotFound {
		t.Errorf("page 3 = %d, want 404", rec.Code)
	}
	if rec := getAs(t, h, "/messages?page=x", bob); rec.Code != http.StatusBadRequest {
		t.Errorf("page x = %d, want 400", rec.Code)
	}
}

func TestMessageBccStaysHidden(t *testing.T) {
	h, _ := messageSite(t, Options{})
	alice := signIn(t, h, "alice", testPassword)
	bob := signIn(t, h, "bob", testPassword)
	carol := signIn(t, h, "carol", testPassword)

	// The author sees the blind copy. The recipient sees none of it.
	if body := asPageAs(t, h, "/messages/1", alice); !strings.Contains(body, "Bcc") || !strings.Contains(body, `<a href="/u/3"><bdi>carol</bdi></a>`) {
		t.Errorf("the author does not see the blind copy:\n%s", body)
	}
	body := asPageAs(t, h, "/messages/1", bob)
	if strings.Contains(body, "carol") || strings.Contains(body, "Bcc") || strings.Contains(body, "/u/3") {
		t.Errorf("bob sees the blind copy:\n%s", body)
	}
	// The blind-copy recipient sees the message, with her own name only.
	body = asPageAs(t, h, "/messages/1", carol)
	if !strings.Contains(body, "Bcc") || !strings.Contains(body, "Glad you joined, bob.") {
		t.Errorf("carol does not see her copy:\n%s", body)
	}
	if strings.Contains(body, `<a href="/u/2"><bdi>bob</bdi></a>, `) {
		t.Error("carol sees extra names in Bcc")
	}
	// The quote form of a recipient holds no blind copy either.
	reply := bodyOf(t, getAs(t, h, "/messages/1/reply?quote=1", bob))
	if to, _ := inputValue(reply, "to"); to != "alice" || strings.Contains(reply, "carol") {
		t.Errorf("bob's reply form: to=%q, names carol: %v", to, strings.Contains(reply, "carol"))
	}
}

func TestMessagePagesNotFound(t *testing.T) {
	h, _ := messageSite(t, Options{})
	carol := signIn(t, h, "carol", testPassword)
	// Alice is a party to message 2, so the page exists for her.
	alice := signIn(t, h, "alice", testPassword)
	if body := asPageAs(t, h, "/messages/2", alice); !strings.Contains(body, "Re: Welcome aboard") {
		t.Fatalf("alice does not read message 2:\n%s", body)
	}
	missing := bodyOf(t, getAs(t, h, "/messages/99", carol))
	for _, target := range []string{"/messages/2", "/messages/2/reply", "/messages/2/reply?quote=1"} {
		rec := getAs(t, h, target, carol)
		body := bodyOf(t, rec)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
		if strings.Contains(body, "Welcome aboard") || strings.Contains(body, "Thanks, alice.") {
			t.Errorf("GET %s shows message 2", target)
		}
		if shape(body, target) != shape(missing, "/messages/99") {
			t.Errorf("GET %s differs from the page of a missing message", target)
		}
	}
	rec, _ := postAs(t, h, "/messages/2/delete", carol, url.Values{"confirm": {"1"}})
	if rec.Code != http.StatusNotFound || strings.Contains(bodyOf(t, rec), "Welcome aboard") {
		t.Errorf("POST /messages/2/delete = %d", rec.Code)
	}
	rec, _ = postAs(t, h, "/messages/2/delete", carol, url.Values{})
	if rec.Code != http.StatusNotFound || strings.Contains(bodyOf(t, rec), "Welcome aboard") {
		t.Errorf("POST /messages/2/delete (confirm step) = %d", rec.Code)
	}
	// Bad IDs are 404 as well.
	for _, target := range []string{"/messages/0", "/messages/x", "/messages/01", "/messages/-1", "/messages/99/reply"} {
		if rec := getAs(t, h, target, carol); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
	}
}

func TestMessagesNeedSignIn(t *testing.T) {
	h, pool := messageSite(t, Options{})
	for _, target := range []string{"/messages", "/messages/sent", "/messages?page=2", "/messages/new", "/messages/new?to=bob", "/messages/1", "/messages/99",
		"/messages/1/reply", "/messages/1/reply?quote=1"} {
		rec := get(t, h, target)
		want := "/login?next=" + url.QueryEscape(target)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
			t.Errorf("guest GET %s = %d %q, want 302 %q", target, rec.Code, rec.Header().Get("Location"), want)
		}
		if rec.Header().Get("X-Robots-Tag") != "noindex" {
			t.Errorf("guest GET %s: no X-Robots-Tag", target)
		}
		if body := bodyOf(t, rec); strings.Contains(body, "Welcome aboard") {
			t.Errorf("guest GET %s shows a message", target)
		}
	}
	before := dumpData(t, pool)
	for _, target := range []string{"/messages/new", "/messages/1/delete"} {
		form := url.Values{"to": {"bob"}, "subject": {"Hi"}, "body": {"Text"}, "confirm": {"1"}}
		// No token: the guard refuses. A good guest token: the handler refuses.
		if rec := request(t, h, http.MethodPost, target, nil, form); rec.Code != http.StatusForbidden {
			t.Errorf("guest POST %s with no token = %d, want 403", target, rec.Code)
		}
		if rec, _ := postAs(t, h, target, nil, form); rec.Code != http.StatusForbidden {
			t.Errorf("guest POST %s with a token = %d, want 403", target, rec.Code)
		}
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("a guest POST changed %v", diff)
	}
	// The guest comes back to the page after signing in.
	rec, _ := postAs(t, h, "/login", nil, url.Values{"name": {"alice"}, "password": {testPassword}, "next": {"/messages/sent"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/messages/sent" {
		t.Errorf("sign-in with next = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestMessagesNoStore(t *testing.T) {
	h, pool := messageSite(t, Options{})
	grantBoardWide(t, pool, 1, 3, "pm.delete")
	alice := signIn(t, h, "alice", testPassword)
	for _, target := range []string{"/messages", "/messages/sent", "/messages/new", "/messages/new?to=bob", "/messages/1", "/messages/2",
		"/messages/1/reply", "/messages/1/reply?quote=1", "/messages/99", "/messages?page=9"} {
		rec := getAs(t, h, target, alice)
		want := http.StatusOK
		if target == "/messages/99" || target == "/messages?page=9" {
			want = http.StatusNotFound
		}
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", target, rec.Code, want)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "private, no-store" {
			t.Errorf("GET %s: Cache-Control = %q", target, cc)
		}
		if !strings.Contains(bodyOf(t, rec), `<meta name="robots" content="noindex">`) {
			t.Errorf("GET %s: no robots noindex (status %d)", target, rec.Code)
		}
	}
	// The same for the answers of the POST routes.
	for _, c := range []struct {
		target string
		form   url.Values
	}{
		{"/messages/new", url.Values{"to": {"nobody"}, "subject": {"x"}, "body": {"x"}}},
		{"/messages/new", url.Values{"to": {"bob"}, "subject": {"x"}, "body": {"x"}, "preview": {"1"}}},
		{"/messages/1/delete", url.Values{}},
	} {
		rec, _ := postAs(t, h, c.target, alice, c.form)
		if cc := rec.Header().Get("Cache-Control"); cc != "private, no-store" {
			t.Errorf("POST %s: Cache-Control = %q", c.target, cc)
		}
		if !strings.Contains(bodyOf(t, rec), `<meta name="robots" content="noindex">`) {
			t.Errorf("POST %s: no robots noindex (status %d)", c.target, rec.Code)
		}
	}
}

func TestMessageFormKeepsText(t *testing.T) {
	h, pool := messageSite(t, Options{})
	alice := signIn(t, h, "alice", testPassword)
	before := dumpData(t, pool)
	for _, c := range []struct {
		name, to, subject, body, want, field string
	}{
		{"unknown", "nobody", "Hello there", "Some **text** </textarea><b>", "No member named “nobody”.", "msg-to-error"},
		{"no subject", "bob", "  ", "Some text", "Enter a subject.", "msg-subject-error"},
		{"no recipient", "", "Hi", "Some text", "Add at least one member.", "msg-to-error"},
		{"self", "alice", "Hi", "Some text", "You cannot send a message to yourself.", "msg-to-error"},
		{"no body", "bob", "Hi", "  ", "Write some text first.", "msg-body-error"},
		{"several without pm.send_many", "bob, carol", "Hi", "Some text", "You can send a message to one member at a time.", "msg-to-error"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := sendMessage(t, h, alice, c.to, c.subject, c.body)
			body := bodyOf(t, rec)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400\n%s", rec.Code, body)
			}
			if to, _ := inputValue(body, "to"); to != c.to {
				t.Errorf("To = %q, want %q", to, c.to)
			}
			if sub, _ := inputValue(body, "subject"); sub != c.subject {
				t.Errorf("Subject = %q, want %q", sub, c.subject)
			}
			if got, _ := textareaOf(t, body, "body"); got != c.body {
				t.Errorf("body = %q, want %q", got, c.body)
			}
			if c.want != "" && !strings.Contains(html.UnescapeString(body), c.want) {
				t.Errorf("the page lacks %q", c.want)
			}
			if !strings.Contains(body, `id="`+c.field+`"`) {
				t.Errorf("the message is not beside its field (%s)", c.field)
			}
			if strings.Contains(body, "<b>") {
				t.Error("the page echoes the body as markup")
			}
		})
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("a refused send changed %v", diff)
	}
}

func TestMessagePreview(t *testing.T) {
	h, pool := messageSite(t, Options{})
	alice := signIn(t, h, "alice", testPassword)
	before := dumpData(t, pool)
	rec, _ := postAs(t, h, "/messages/new", alice, url.Values{"to": {"bob"}, "subject": {"Draft"}, "body": {"Some **bold** text"}, "preview": {"1"}})
	body := bodyOf(t, rec)
	if rec.Code != http.StatusOK || !strings.Contains(body, "<strong>bold</strong>") || !strings.Contains(body, `id="preview-title"`) {
		t.Fatalf("preview = %d\n%s", rec.Code, body)
	}
	if got, _ := textareaOf(t, body, "body"); got != "Some **bold** text" {
		t.Errorf("the box holds %q", got)
	}
	if sub, _ := inputValue(body, "subject"); sub != "Draft" {
		t.Errorf("subject = %q", sub)
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("a preview changed %v", diff)
	}
}

func TestMessageDelete(t *testing.T) {
	h, pool := messageSite(t, Options{})
	bob := signIn(t, h, "bob", testPassword)
	alice := signIn(t, h, "alice", testPassword)
	// Without pm.delete a party gets no button, and a POST gets 403.
	if body := asPageAs(t, h, "/messages/1", bob); strings.Contains(body, "/messages/1/delete") {
		t.Error("bob sees Delete without pm.delete")
	}
	if rec, _ := postAs(t, h, "/messages/1/delete", bob, url.Values{"confirm": {"1"}}); rec.Code != http.StatusForbidden {
		t.Errorf("delete without the right = %d, want 403", rec.Code)
	}
	grantBoardWide(t, pool, 2, 3, "pm.delete")
	body := asPageAs(t, h, "/messages/1", bob)
	if !strings.Contains(body, `action="/messages/1/delete"`) {
		t.Fatalf("bob does not see Delete:\n%s", body)
	}
	// The first POST asks. It changes nothing.
	before := dumpData(t, pool)
	rec, _ := postAs(t, h, "/messages/1/delete", bob, url.Values{})
	ask := bodyOf(t, rec)
	if rec.Code != http.StatusOK || !strings.Contains(ask, "Delete this message?") || !strings.Contains(ask, `name="confirm" value="1"`) ||
		!strings.Contains(ask, `action="/messages/1/delete"`) || !strings.Contains(ask, `href="/messages/1"`) {
		t.Fatalf("confirm step = %d\n%s", rec.Code, ask)
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("the confirm step changed %v", diff)
	}
	// The second deletes from bob's view only.
	rec, _ = postAs(t, h, "/messages/1/delete", bob, url.Values{"confirm": {"1"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/messages" {
		t.Fatalf("delete = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := getAs(t, h, "/messages/1", bob); rec.Code != http.StatusNotFound {
		t.Errorf("bob reads the deleted message: %d", rec.Code)
	}
	if strings.Contains(bodyOf(t, getAs(t, h, "/messages", bob)), "Welcome aboard") {
		t.Error("the deleted message is in bob's inbox")
	}
	if !strings.Contains(asPageAs(t, h, "/messages/1", alice), "Glad you joined, bob.") {
		t.Error("alice lost her copy")
	}
}

func TestMessageButtonsFollowRights(t *testing.T) {
	h, pool := messageSite(t, Options{})
	grantBoardWide(t, pool, 2, 2, "pm.send") // never
	bob := signIn(t, h, "bob", testPassword)
	body := asPageAs(t, h, "/messages/1", bob)
	for _, bad := range []string{"/messages/1/reply", "Quote"} {
		if strings.Contains(body, bad) {
			t.Errorf("bob cannot send, but the page holds %q", bad)
		}
	}
	for _, target := range []string{"/messages/1/reply", "/messages/1/reply?quote=1", "/messages/new"} {
		if rec := getAs(t, h, target, bob); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s without pm.send = %d, want 403", target, rec.Code)
		}
	}
	rec := sendMessage(t, h, bob, "alice", "Hi", "Text")
	if rec.Code != http.StatusForbidden || !strings.Contains(bodyOf(t, rec), "Text") {
		t.Errorf("send without pm.send = %d", rec.Code)
	}
	// Alice may send: she sees both buttons.
	alice := signIn(t, h, "alice", testPassword)
	body = asPageAs(t, h, "/messages/2", alice)
	if !strings.Contains(body, `href="/messages/2/reply"`) || !strings.Contains(body, `href="/messages/2/reply?quote=1"`) {
		t.Errorf("alice lacks Reply or Quote:\n%s", body)
	}
	// carol loses pm.read: no link in the nav, and 403 on the pages.
	grantBoardWide(t, pool, 3, 2, "pm.read")
	carol := signIn(t, h, "carol", testPassword)
	if strings.Contains(bodyOf(t, getAs(t, h, "/", carol)), "Messages") {
		t.Error("a member without pm.read sees the Messages link")
	}
	for _, target := range []string{"/messages", "/messages/sent", "/messages/1"} {
		if rec := getAs(t, h, target, carol); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s without pm.read = %d, want 403", target, rec.Code)
		}
	}
}

func TestMessageNavCountFails(t *testing.T) {
	h, pool := messageSite(t, Options{})
	alice := signIn(t, h, "alice", testPassword)
	logs := captureLogs(t)
	exec(t, pool, `alter table pm_recipients rename to pm_recipients_gone`)
	rec := getAs(t, h, "/", alice)
	body := bodyOf(t, rec)
	if rec.Code != http.StatusOK || !strings.Contains(body, `>Messages</a>`) || strings.Contains(body, `class="badge-new">`) {
		t.Errorf("the index with a failing count = %d, nav:\n%s", rec.Code, body)
	}
	warned := false
	for _, l := range logs.lines() {
		// The line names the member, so an operator can tell whose count failed.
		warned = warned || (strings.Contains(l, "level=WARN") && strings.Contains(l, "unread messages") && strings.Contains(l, "user=1 "))
	}
	if !warned {
		t.Errorf("no WARN line for the failed count:\n%s", strings.Join(logs.lines(), "\n"))
	}
}

func TestMessageSendLimit(t *testing.T) {
	h, pool := messageSite(t, Options{})
	alice := signIn(t, h, "alice", testPassword)
	for i := range 10 {
		sentID(t, sendMessage(t, h, alice, "bob", fmt.Sprintf("Note %d", i), "Text"))
	}
	var n int
	if err := pool.QueryRow(t.Context(), `select count(*) from pms`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	rec := sendMessage(t, h, alice, "bob", "One too many", "The eleventh <text>")
	body := bodyOf(t, rec)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("the 11th send = %d (Retry-After %q)\n%s", rec.Code, rec.Header().Get("Retry-After"), body)
	}
	if to, _ := inputValue(body, "to"); to != "bob" {
		t.Errorf("To = %q", to)
	}
	if sub, _ := inputValue(body, "subject"); sub != "One too many" {
		t.Errorf("subject = %q", sub)
	}
	if got, _ := textareaOf(t, body, "body"); got != "The eleventh <text>" {
		t.Errorf("body = %q", got)
	}
	var after int
	if err := pool.QueryRow(t.Context(), `select count(*) from pms`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != n {
		t.Errorf("the refused send stored a message (%d, then %d)", n, after)
	}
	// Another member has a count of her own.
	bob := signIn(t, h, "bob", testPassword)
	sentID(t, sendMessage(t, h, bob, "alice", "Mine", "Text"))
}

// The posting limit answers 429 on the compose form with every value the member
// sent, and stores nothing.
func TestMessagePostingLimitKeepsValues(t *testing.T) {
	h, pool := messageSite(t, Options{MemberPosts: 1})
	alice := signIn(t, h, "alice", testPassword)
	sentID(t, sendMessage(t, h, alice, "bob", "First", "Text"))
	before := dumpData(t, pool)
	rec := sendMessage(t, h, alice, "bob, carol", "Second <one>", "Some **text** </textarea>")
	body := bodyOf(t, rec)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("the second POST = %d (Retry-After %q)\n%s", rec.Code, rec.Header().Get("Retry-After"), body)
	}
	if !strings.Contains(body, msgPostingTooFast) {
		t.Errorf("the page lacks %q", msgPostingTooFast)
	}
	if to, _ := inputValue(body, "to"); to != "bob, carol" {
		t.Errorf("To = %q", to)
	}
	if sub, _ := inputValue(body, "subject"); sub != "Second <one>" {
		t.Errorf("subject = %q", sub)
	}
	if got, _ := textareaOf(t, body, "body"); got != "Some **text** </textarea>" {
		t.Errorf("body = %q", got)
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("a refused POST changed %v", diff)
	}
}

// The "Send a message" link of a profile escapes the name of the member: a name
// with &, a space and # arrives whole in the To field.
func TestProfileMessageLinkEscapesTheName(t *testing.T) {
	const name = "R&D #1 team"
	h, pool := messageSite(t, Options{})
	exec(t, pool, `update users set username = '`+name+`', username_clean = lower('`+name+`') where id = 2`)
	alice := signIn(t, h, "alice", testPassword)
	profile := bodyOf(t, getAs(t, h, "/u/2", alice))
	m := regexp.MustCompile(`href="([^"]*)">Send a message</a>`).FindStringSubmatch(profile)
	if m == nil {
		t.Fatalf("the profile has no \"Send a message\" link:\n%s", profile)
	}
	href := html.UnescapeString(m[1])
	u, err := url.Parse(href)
	if err != nil {
		t.Fatalf("the link %q: %v", href, err)
	}
	if u.Path != "/messages/new" || u.Fragment != "" || u.Query().Get("to") != name {
		t.Errorf("the link %q gives path %q, fragment %q, to %q; want to %q", href, u.Path, u.Fragment, u.Query().Get("to"), name)
	}
	form := bodyOf(t, getAs(t, h, href, alice))
	if to, ok := inputValue(form, "to"); !ok || to != name {
		t.Errorf("the To field holds %q, %v; want %q", to, ok, name)
	}
}

func TestMessageRefusalsDoNotUseTheSendLimit(t *testing.T) {
	h, _ := messageSite(t, Options{})
	alice := signIn(t, h, "alice", testPassword)
	// A refused send gives its count back, so typos do not lock a member out.
	for range 12 {
		if rec := sendMessage(t, h, alice, "nobody", "Hi", "Text"); rec.Code != http.StatusBadRequest {
			t.Fatalf("refused send = %d, want 400", rec.Code)
		}
	}
	sentID(t, sendMessage(t, h, alice, "bob", "Hi", "Text"))
}

func TestMessagePayloadsPage(t *testing.T) {
	payloads := loadPayloads(t)
	if len(payloads) < 150 {
		t.Fatalf("%d payloads", len(payloads))
	}
	h, pool := messageSite(t, Options{MemberPosts: 100_000, MessageSends: 100_000})
	grantBoardWide(t, pool, 1, 3, "pm.images")
	alice := signIn(t, h, "alice", testPassword)
	bob := signIn(t, h, "bob", testPassword)
	basePolicy := getAs(t, h, "/messages", bob).Header().Get("Content-Security-Policy")
	if basePolicy != wantBaseCSP {
		t.Fatalf("the policy of the inbox is %q", basePolicy)
	}
	page := bodyOf(t, getAs(t, h, "/messages/new", alice))
	token := formToken(t, page)

	ids := map[int64]int{}
	previewed, refused, checked := 0, 0, 0
	for _, p := range payloads {
		valid := utf8.ValidString(p.text) && !strings.ContainsRune(p.text, 0) && strings.TrimSpace(p.text) != ""
		prev := request(t, h, http.MethodPost, "/messages/new", alice, url.Values{"csrf": {token}, "to": {"bob"}, "subject": {"Payload"}, "body": {p.text}, "preview": {"1"}})
		out := bodyOf(t, prev)
		if prev.Code == http.StatusOK {
			previewed++
			bs, problems := postBodies(out)
			for _, pr := range problems {
				t.Errorf("payloads.txt:%d preview: %s", p.line, pr)
			}
			for _, b := range bs {
				htmlcheck.Check(t, b)
				checked++
			}
		} else if prev.Code != http.StatusBadRequest || valid && !strings.Contains(out, "field-error") {
			t.Errorf("payloads.txt:%d: preview status %d", p.line, prev.Code)
		}
		if valid {
			if got, _ := textareaOf(t, out, "body"); got != p.text {
				t.Errorf("payloads.txt:%d: the box holds %q, want %q", p.line, got, p.text)
			}
		}
		csp := prev.Header().Get("Content-Security-Policy")
		extra := checkScripts(t, fmt.Sprintf("payloads.txt:%d preview", p.line), out, csp)
		checkPolicy(t, fmt.Sprintf("payloads.txt:%d preview", p.line), csp, basePolicy, extra > 0 || strings.Contains(out, "<iframe"))

		rec := request(t, h, http.MethodPost, "/messages/new", alice, url.Values{"csrf": {token}, "to": {"bob"}, "subject": {"Payload"}, "body": {p.text}})
		switch rec.Code {
		case http.StatusSeeOther:
			m := messageLocation.FindStringSubmatch(rec.Header().Get("Location"))
			if m == nil {
				t.Fatalf("payloads.txt:%d: Location %q", p.line, rec.Header().Get("Location"))
			}
			id, _ := strconv.ParseInt(m[1], 10, 64)
			ids[id] = p.line
		case http.StatusBadRequest:
			refused++
		default:
			t.Errorf("payloads.txt:%d: send status %d", p.line, rec.Code)
		}
	}
	if len(ids) < 100 {
		t.Fatalf("only %d messages were stored", len(ids))
	}
	bodies := 0
	for id, line := range ids {
		target := "/messages/" + strconv.FormatInt(id, 10)
		rec := getWith(t, h, target, bob, asPage)
		if rec.Code != http.StatusOK {
			t.Fatalf("payloads.txt:%d: GET %s = %d", line, target, rec.Code)
		}
		out := bodyOf(t, rec)
		bs, problems := postBodies(out)
		for _, pr := range problems {
			t.Errorf("payloads.txt:%d %s: %s", line, target, pr)
		}
		if len(bs) != 1 || articleCount(out) != 1 {
			t.Errorf("payloads.txt:%d %s: %d bodies, %d articles", line, target, len(bs), articleCount(out))
		}
		for _, b := range bs {
			htmlcheck.Check(t, b)
		}
		bodies += len(bs)
		where := fmt.Sprintf("payloads.txt:%d %s", line, target)
		csp := rec.Header().Get("Content-Security-Policy")
		extra := checkScripts(t, where, out, csp)
		checkPolicy(t, where, csp, basePolicy, extra > 0 || strings.Contains(out, "<iframe"))
	}
	t.Logf("payloads: %d; previewed: %d (%d bodies); stored: %d; refused: %d; message pages checked: %d", len(payloads), previewed, checked, len(ids), refused, bodies)
}

// Ruling R45: a message is marked read by a navigation that the member started,
// not by one that another site started (a link, or a script that opens the page
// in a window).
func TestMessageMarkNeedsAnOwnNavigation(t *testing.T) {
	h, pool := messageSite(t, Options{})
	alice := signIn(t, h, "alice", testPassword)
	bob := signIn(t, h, "bob", testPassword)
	read := func(id int64) bool {
		t.Helper()
		return countRows(t, pool, fmt.Sprintf(`select count(*) from pm_recipients where pm_id = %d and user_id = 2 and is_read`, id)) == 1
	}
	send := func() int64 {
		return sentID(t, sendMessage(t, h, alice, "bob", "Hello", "A short note."))
	}
	for _, site := range []string{"cross-site", "same-site"} {
		id := send()
		hdr := map[string]string{"Sec-Fetch-Dest": "document", "Sec-Fetch-Site": site}
		if rec := getWith(t, h, fmt.Sprintf("/messages/%d", id), bob, hdr); rec.Code != http.StatusOK {
			t.Fatalf("GET (%s): %d", site, rec.Code)
		}
		if read(id) {
			t.Errorf("a %s navigation marked message %d read", site, id)
		}
	}
	for name, hdr := range map[string]map[string]string{
		"same-origin":  {"Sec-Fetch-Dest": "document", "Sec-Fetch-Site": "same-origin"},
		"none":         {"Sec-Fetch-Dest": "document", "Sec-Fetch-Site": "none"},
		"older client": {"Accept": "text/html"},
	} {
		id := send()
		if rec := getWith(t, h, fmt.Sprintf("/messages/%d", id), bob, hdr); rec.Code != http.StatusOK {
			t.Fatalf("GET (%s): %d", name, rec.Code)
		}
		if !read(id) {
			t.Errorf("a %s navigation did not mark message %d read", name, id)
		}
	}
}

// Ruling R43: a message and its preview render an embed as a plain link, so the
// sender cannot pick third-party script for the most private page.
func TestMessageEmbedIsAPlainLink(t *testing.T) {
	h, _ := messageSite(t, Options{})
	alice := signIn(t, h, "alice", testPassword)
	bob := signIn(t, h, "bob", testPassword)
	const text = "Look at this:\n\nhttps://x.com/example/status/20\n\nAnd this:\n\nhttps://www.youtube.com/watch?v=dQw4w9WgXcQ\n"
	id := sentID(t, sendMessage(t, h, alice, "bob", "Links", text))
	preview, _ := postAs(t, h, "/messages/new", alice, url.Values{"to": {"bob"}, "subject": {"Links"}, "body": {text}, "preview": {"1"}})
	pages := map[string]*httpRecorder{
		"message": getWith(t, h, fmt.Sprintf("/messages/%d", id), bob, asPage),
		"preview": preview,
	}
	for name, rec := range pages {
		body := bodyOf(t, rec)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", name, rec.Code)
		}
		for _, want := range []string{`class="embed-link"`, `href="https://x.com/i/status/20"`, `href="https://www.youtube.com/watch?v=dQw4w9WgXcQ"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %s", name, want)
			}
		}
		for _, bad := range []string{"<iframe", "platform.x.com", "twitter-tweet", "widgets.js"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s holds %q", name, bad)
			}
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != wantBaseCSP {
			t.Errorf("%s: Content-Security-Policy = %q\nwant %q", name, got, wantBaseCSP)
		}
	}
}

// The inbox page takes the unread count from the nav and does not ask for it a
// second time. Alice is an admin, so her footer shows the count of queries. Her
// inbox and her sent list run the same queries, and so the inbox runs no more.
func TestInboxCountsUnreadOnce(t *testing.T) {
	h, _ := messageSite(t, Options{})
	alice := signIn(t, h, "alice", testPassword)
	bob := signIn(t, h, "bob", testPassword)
	sentID(t, sendMessage(t, h, bob, "alice", "Unread", "Waiting for alice."))
	queries := func(target string) int {
		t.Helper()
		line := buildLine(t, bodyOf(t, getWith(t, h, target, alice, asImage)))
		m := adminLine.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("%s: the line %q has no count", target, line)
		}
		n, _ := strconv.Atoi(m[3])
		return n
	}
	inbox, sent := queries("/messages"), queries("/messages/sent")
	if inbox != sent {
		t.Errorf("the inbox runs %d queries and the sent list %d; want the same", inbox, sent)
	}
	// The page still says how many messages wait.
	if body := bodyOf(t, getAs(t, h, "/messages", alice)); !strings.Contains(body, `<p class="page-sub">1 unread.</p>`) {
		t.Errorf("the inbox does not say 1 unread:\n%s", body)
	}
}
