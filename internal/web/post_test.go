package web

import (
	"bufio"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/htmlcheck"
	"github.com/evanwtf/gobb/markup"
)

// postingBoard is the small board where every member may post in forum 2 with no
// approval, delete the own post, count posts and post images.
func postingBoard() *bundletest.Board {
	b := bundletest.Small()
	for _, o := range []string{"post.no_approval", "post.delete_own", "post.counts", "post.images"} {
		allowGroup(b, 2, 2, o)
	}
	return b
}

// textareaOf returns the text of the textarea named name in a page, as a browser
// reads it: the tokenizer resolves the character references, a reference to a
// carriage return keeps the carriage return, and the first line break after the
// start tag is dropped, as the HTML parser drops it.
func textareaOf(t *testing.T, page, name string) (string, bool) {
	t.Helper()
	z := xhtml.NewTokenizer(strings.NewReader(page))
	var sb strings.Builder
	in, found := false, false
	for {
		switch z.Next() {
		case xhtml.ErrorToken:
			return sb.String(), found
		case xhtml.StartTagToken:
			tn, hasAttr := z.TagName()
			if string(tn) != "textarea" {
				continue
			}
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				if string(k) == "name" && string(v) == name {
					in, found = true, true
				}
			}
		case xhtml.TextToken:
			if in {
				text := z.Text()
				if sb.Len() == 0 && len(text) > 0 && text[0] == '\n' {
					text = text[1:]
				}
				sb.Write(text)
			}
		case xhtml.EndTagToken:
			if tn, _ := z.TagName(); in && string(tn) == "textarea" {
				return sb.String(), true
			}
		}
	}
}

// inputValue returns the value attribute of the input named name, or false.
func inputValue(page, name string) (string, bool) {
	z := xhtml.NewTokenizer(strings.NewReader(page))
	for {
		switch z.Next() {
		case xhtml.ErrorToken:
			return "", false
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			tn, hasAttr := z.TagName()
			if string(tn) != "input" {
				continue
			}
			attrs := map[string]string{}
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				attrs[string(k)] = string(v)
			}
			if attrs["name"] == name {
				return attrs["value"], true
			}
		}
	}
}

// postBodies returns the raw inner HTML of every element with the class
// post-body, cut from the page between its start tag and its matching end tag,
// counted by depth. The bytes are the ones the server wrote, not a tree that a
// parser repaired.
//
// A stray end tag inside a body would end the element early and let the bytes
// after it escape the check. So the markup that follows each cut must be the
// template's own: the attachments, the edit note, the signature, the address or
// the buttons of the post, or the end of the post (the </div> of the post and the
// </article>). Anything else is reported in problems.
func postBodies(page string) (bodies, problems []string) {
	z := xhtml.NewTokenizer(strings.NewReader(page))
	// after is 0 outside the tail of a body. It is 1 right after a body ends, when
	// the next markup must be an optional part of the post or the end of the
	// post-main element. It is 2 after that end, when </article> must follow.
	pos, start, depth, after := 0, -1, 0, 0
	for {
		tt := z.Next()
		raw := z.Raw()
		before := pos
		pos += len(raw)
		if tt == xhtml.ErrorToken {
			if start >= 0 {
				problems = append(problems, "a post-body element is not closed")
			}
			return bodies, problems
		}
		if after > 0 && start < 0 && !(tt == xhtml.TextToken && strings.TrimSpace(string(raw)) == "") {
			ok := false
			switch tt {
			case xhtml.StartTagToken:
				tn, hasAttr := z.TagName()
				class := ""
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					if string(k) == "class" {
						class = string(v)
					}
				}
				ok = after == 1 && (string(tn) == "section" && class == "attachments" ||
					string(tn) == "p" && (class == "edit-note" || class == "post-ip") ||
					string(tn) == "div" && (class == "signature" || class == "post-actions"))
				after = 0
			case xhtml.EndTagToken:
				tn, _ := z.TagName()
				switch {
				case after == 1 && string(tn) == "div":
					ok, after = true, 2
				case after == 2 && string(tn) == "article":
					ok, after = true, 0
				default:
					after = 0
				}
			default:
				after = 0
			}
			if !ok {
				problems = append(problems, fmt.Sprintf("unexpected markup after a post body: %q", raw))
			}
		}
		switch tt {
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			tn, hasAttr := z.TagName()
			if string(tn) != "div" || tt == xhtml.SelfClosingTagToken {
				continue
			}
			if start >= 0 {
				depth++
				continue
			}
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				if string(k) == "class" && slicesContains(strings.Fields(string(v)), "post-body") {
					start, depth = pos, 1
				}
			}
		case xhtml.EndTagToken:
			if tn, _ := z.TagName(); start >= 0 && string(tn) == "div" {
				if depth--; depth == 0 {
					bodies = append(bodies, page[start:before])
					start, after = -1, 1
				}
			}
		}
	}
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// articleCount counts the posts on a page.
func articleCount(page string) int { return strings.Count(page, `<article class="post`) }

// formToken reads the csrf token of a page.
func formToken(t *testing.T, page string) string {
	t.Helper()
	m := csrfField.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("the page has no csrf field")
	}
	return m[1]
}

// send POSTs a form to a posting URL with the cookies and the token of the
// visitor, and returns the answer.
func send(t *testing.T, h http.Handler, target string, cookies []*http.Cookie, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rec, _ := postAs(t, h, target, cookies, form)
	return rec
}

// countPostRows counts the rows of the posts table.
func countPostRows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), `select count(*) from posts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// idOf reads the post ID from a Location of the form /p/N.
func idOf(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	loc := rec.Header().Get("Location")
	n, err := strconv.Atoi(strings.TrimPrefix(loc, "/p/"))
	if rec.Code != http.StatusSeeOther || err != nil {
		t.Fatalf("answer = %d %q, want 303 /p/N\n%s", rec.Code, loc, bodyOf(t, rec))
	}
	return n
}

// topicOf follows /p/N and returns the topic page it leads to.
func topicOf(t *testing.T, h http.Handler, postID int, cookies []*http.Cookie) string {
	t.Helper()
	rec := getAs(t, h, fmt.Sprintf("/p/%d", postID), cookies)
	if rec.Code != http.StatusFound {
		t.Fatalf("GET /p/%d = %d", postID, rec.Code)
	}
	loc := rec.Header().Get("Location")
	page := getAs(t, h, strings.SplitN(loc, "#", 2)[0], cookies)
	if page.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", loc, page.Code)
	}
	return bodyOf(t, page)
}

// plainText removes the tags of a fragment.
var tagRe = regexp.MustCompile(`<[^>]*>`)

func plainText(s string) string { return html.UnescapeString(tagRe.ReplaceAllString(s, "")) }

func TestPostingFlow(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", "example-password")

	// The forum page offers a new topic; the form has a title and the hint.
	forum := bodyOf(t, getAs(t, h, "/f/2", a))
	if !strings.Contains(forum, `href="/f/2/post"`) || !strings.Contains(forum, "New topic") {
		t.Error("the forum page has no New topic button")
	}
	rec := getAs(t, h, "/f/2/post", a)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /f/2/post = %d", rec.Code)
	}
	form := bodyOf(t, rec)
	for _, want := range []string{`name="title"`, `name="body"`, `name="preview"`, `action="/f/2/post"`,
		"Markdown works here: **bold**, *italic*, [link](https://example.com), &gt; quote."} {
		if !strings.Contains(form, want) {
			t.Errorf("the new-topic form lacks %q", want)
		}
	}
	if strings.Contains(form, "Your name") {
		t.Error("a member sees the guest name field")
	}

	// Post a topic with the token from the form.
	rec = send(t, h, "/f/2/post", a, url.Values{
		"csrf": {formToken(t, form)}, "title": {"Posting test"}, "body": {"First **post** of the test."},
	})
	first := idOf(t, rec)
	page := topicOf(t, h, first, a)
	if !strings.Contains(page, "<strong>post</strong>") || !strings.Contains(page, "Posting test") {
		t.Error("the topic page does not show the new post")
	}
	var topicID int
	if err := pool.QueryRow(t.Context(), `select topic_id from posts where id = $1`, first).Scan(&topicID); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		fmt.Sprintf(`href="/t/%d/reply"`, topicID),
		fmt.Sprintf(`href="/t/%d/reply?quote=%d"`, topicID, first),
		fmt.Sprintf(`href="/p/%d/edit"`, first),
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the topic page lacks %s", want)
		}
	}
	if n := strings.Count(page, ">Reply</a>"); n != 2 {
		t.Errorf("the topic page has %d Reply buttons, want 2 (top and bottom)", n)
	}
	// alice owns the first post, which has no replies yet: she may delete it.
	if !strings.Contains(page, fmt.Sprintf(`action="/p/%d/delete"`, first)) {
		t.Error("the topic page has no Delete form for the first post")
	}

	// Reply with a quote of it.
	rec = getAs(t, h, fmt.Sprintf("/t/%d/reply?quote=%d", topicID, first), a)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET reply with quote = %d", rec.Code)
	}
	src, _ := textareaOf(t, bodyOf(t, rec), "body")
	if !strings.HasPrefix(src, fmt.Sprintf("> **alice** [wrote](/p/%d):", first)) || !strings.Contains(src, "> First **post** of the test.") {
		t.Errorf("the quote form holds %q", src)
	}
	rec = send(t, h, fmt.Sprintf("/t/%d/reply", topicID), a, url.Values{"body": {src + "My answer."}})
	reply := idOf(t, rec)
	page = topicOf(t, h, reply, a)
	m := regexp.MustCompile(`(?s)<blockquote.*?</blockquote>`).FindString(page)
	if m == "" || !strings.Contains(plainText(m), "alice wrote") {
		t.Errorf("the reply shows no quote from alice:\n%s", m)
	}
	if !strings.Contains(page, "My answer.") {
		t.Error("the reply text is missing")
	}

	// Edit the reply. The form holds the stored text and no title.
	rec = getAs(t, h, fmt.Sprintf("/p/%d/edit", reply), a)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET edit = %d", rec.Code)
	}
	editForm := bodyOf(t, rec)
	if stored, _ := textareaOf(t, editForm, "body"); stored != src+"My answer." {
		t.Errorf("the edit form holds %q", stored)
	}
	if strings.Contains(editForm, `name="title"`) || strings.Contains(editForm, `name="reason"`) {
		t.Error("the edit of an own reply asks for a title or a reason")
	}
	rec = send(t, h, fmt.Sprintf("/p/%d/edit", reply), a, url.Values{"body": {"My *edited* answer."}})
	if idOf(t, rec) != reply {
		t.Error("the edit does not return to the post")
	}
	page = topicOf(t, h, reply, a)
	if !strings.Contains(page, "<em>edited</em>") || strings.Contains(page, "My answer.") {
		t.Error("the page does not show the edited text")
	}
	if !strings.Contains(page, `class="edit-note"`) {
		t.Error("the page has no edit note")
	}

	// Delete the reply: the first POST asks, the second deletes.
	before := dumpData(t, pool)
	rec = send(t, h, fmt.Sprintf("/p/%d/delete", reply), a, url.Values{})
	if rec.Code != http.StatusOK {
		t.Fatalf("first delete POST = %d, want 200", rec.Code)
	}
	ask := bodyOf(t, rec)
	if !strings.Contains(ask, "Delete this post?") || !strings.Contains(ask, `name="confirm" value="1"`) {
		t.Errorf("the confirm page lacks the question or the second form:\n%s", ask)
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("the confirm step changed %v", diff)
	}
	rec = send(t, h, fmt.Sprintf("/p/%d/delete", reply), a, url.Values{"confirm": {"1"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != fmt.Sprintf("/t/%d", topicID) {
		t.Errorf("confirmed delete = %d %q, want 303 /t/%d", rec.Code, rec.Header().Get("Location"), topicID)
	}
	page = bodyOf(t, getAs(t, h, fmt.Sprintf("/t/%d", topicID), a))
	if strings.Contains(page, "My *edited*") || strings.Contains(page, "<em>edited</em>") {
		t.Error("the deleted reply is still on the page")
	}

	// The first post is the whole topic now: deleting it takes the topic away from
	// alice, so the answer goes to the forum.
	rec = send(t, h, fmt.Sprintf("/p/%d/delete", first), a, url.Values{"confirm": {"1"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/f/2" {
		t.Errorf("delete of the only post = %d %q, want 303 /f/2", rec.Code, rec.Header().Get("Location"))
	}
}

func TestQuoteHiddenPostIsNotFound(t *testing.T) {
	b := postingBoard()
	h, pool := siteWithPool(t, b, Options{})
	alice := signIn(t, h, "alice", "example-password")
	carol := signIn(t, h, "carol", "example-password")

	// Post 5 sits in forum 3, which carol cannot read. Post 3 is carol's held
	// reply: alice may not see it. A deleted post is hidden from both.
	if _, err := pool.Exec(t.Context(), `update posts set visibility = 'deleted' where id = 4`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		who    string
		cookie []*http.Cookie
		quote  int
		secret string
	}{
		{"carol, post in an unreadable forum", carol, 5, "Which color do you like best"},
		{"alice, another member's unapproved post", alice, 3, "waits for a moderator"},
		{"alice, a deleted post", alice, 4, "Hello from a guest"},
	} {
		rec := getAs(t, h, fmt.Sprintf("/t/1/reply?quote=%d", c.quote), c.cookie)
		body := bodyOf(t, rec)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", c.who, rec.Code)
		}
		if strings.Contains(body, c.secret) {
			t.Errorf("%s: the page holds the text of the post", c.who)
		}
		if strings.Contains(body, "<textarea") {
			t.Errorf("%s: the page holds a form", c.who)
		}
		// The same page as for a post that does not exist, for the same viewer.
		missing := get2(t, h, "/t/1/reply?quote=9999", c.cookie)
		want := fmt.Sprintf("/t/1/reply?quote=%d", c.quote)
		if shape(body, want) != shape(missing, "/t/1/reply?quote=9999") {
			t.Errorf("%s: the 404 differs from the 404 of a missing post", c.who)
		}
	}
	// A bad ID is a 404 too.
	for _, q := range []string{"0", "-1", "x", "1e3", ""} {
		rec := getAs(t, h, "/t/1/reply?quote="+q, alice)
		want := http.StatusNotFound
		if q == "" {
			want = http.StatusOK
		}
		if rec.Code != want {
			t.Errorf("quote=%q: status %d, want %d", q, rec.Code, want)
		}
	}
	// The topic itself must be one the viewer can read and reply to.
	if rec := getAs(t, h, "/t/2/reply", carol); rec.Code != http.StatusNotFound {
		t.Errorf("reply form of an unreadable topic = %d, want 404", rec.Code)
	}
	// The reply rights of the target topic still apply to a quote: a locked topic
	// gives 403 whatever the quoted post. (A quote from another forum, and from a
	// locked topic, is allowed: see TestQuoteAcrossForums.)
	if _, err := pool.Exec(t.Context(), `update topics set locked = true where id = 1`); err != nil {
		t.Fatal(err)
	}
	if rec := getAs(t, h, "/t/1/reply?quote=2", alice); rec.Code != http.StatusForbidden {
		t.Errorf("reply form of a locked topic = %d, want 403", rec.Code)
	}
}

// get2 returns the body of a GET as carol, for the shape comparison.
func get2(t *testing.T, h http.Handler, target string, cookies []*http.Cookie) string {
	t.Helper()
	return bodyOf(t, getAs(t, h, target, cookies))
}

// longBody returns about 2,000 words with a leading line break, "<", "&", a
// non-ASCII character and CR LF line ends.
func longBody() string {
	var sb strings.Builder
	sb.WriteString("\r\nTop: 5 < 6 && 7 > 3, café ünïcode ☃ \"quoted\" 'single'\r\n")
	for i := range 2000 {
		sb.WriteString("word")
		sb.WriteString(strconv.Itoa(i))
		if i%10 == 9 {
			sb.WriteString("\r\n")
		} else {
			sb.WriteString(" ")
		}
	}
	return sb.String()
}

func TestFormKeepsTextOnError(t *testing.T) {
	h, _ := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", "example-password")
	body := longBody()
	title := strings.Repeat("t", 121)

	rec := send(t, h, "/f/2/post", a, url.Values{"title": {title}, "body": {body}})
	page := bodyOf(t, rec)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if got, ok := textareaOf(t, page, "body"); !ok || got != body {
		t.Errorf("the text box holds %d bytes, want the %d bytes that were sent (equal: %v)", len(got), len(body), got == body)
	}
	if got, _ := inputValue(page, "title"); got != title {
		t.Errorf("the title field holds %q", got)
	}
	if !strings.Contains(page, "The title is longer than 120 characters.") {
		t.Error("the form lacks the message")
	}

	// The same for a body over the limit, a reply, an edit, and a preview.
	huge := body + strings.Repeat("x", 61_000)
	for _, c := range []struct{ name, target, want string }{
		{"new topic, text too long", "/f/2/post", "The text is too long"},
		{"reply, text too long", "/t/1/reply", "The text is too long"},
		{"edit, text too long", "/p/1/edit", "The text is too long"},
	} {
		form := url.Values{"title": {"ok"}, "body": {huge}}
		rec := send(t, h, c.target, a, form)
		page := bodyOf(t, rec)
		if rec.Code != http.StatusBadRequest || !strings.Contains(page, c.want) {
			t.Errorf("%s: status %d, message present: %v", c.name, rec.Code, strings.Contains(page, c.want))
		}
		if got, _ := textareaOf(t, page, "body"); got != huge {
			t.Errorf("%s: the text box lost the text (%d bytes, want %d)", c.name, len(got), len(huge))
		}
	}

	// A body with no text, previewed, shows the form and the message and no preview.
	rec = send(t, h, "/t/1/reply", a, url.Values{"body": {" \r\n "}, "preview": {"1"}})
	page = bodyOf(t, rec)
	if rec.Code != http.StatusBadRequest || !strings.Contains(page, "Write some text first.") || strings.Contains(page, `class="post-body"`) {
		t.Errorf("preview of an input error: status %d\n%s", rec.Code, page)
	}
	if got, _ := textareaOf(t, page, "body"); got != " \r\n " {
		t.Errorf("the text box holds %q", got)
	}

	// A preview keeps the exact text too.
	rec = send(t, h, "/t/1/reply", a, url.Values{"body": {body}, "preview": {"1"}})
	page = bodyOf(t, rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status %d", rec.Code)
	}
	if got, _ := textareaOf(t, page, "body"); got != body {
		t.Errorf("after a preview the text box holds %d bytes, want %d", len(got), len(body))
	}

	// A refusal keeps the text as well: the topic is locked after the form loaded.
	h2, pool := siteWithPool(t, postingBoard(), Options{})
	a2 := signIn(t, h2, "alice", "example-password")
	if _, err := pool.Exec(t.Context(), `update topics set locked = true where id = 1`); err != nil {
		t.Fatal(err)
	}
	rec = send(t, h2, "/t/1/reply", a2, url.Values{"body": {body}})
	page = bodyOf(t, rec)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("reply to a locked topic = %d, want 403", rec.Code)
	}
	if got, _ := textareaOf(t, page, "body"); got != body {
		t.Errorf("a 403 lost the text (%d bytes, want %d)", len(got), len(body))
	}
}

func TestPreviewSavesNothing(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", "example-password")
	before := dumpData(t, pool)
	n := countPostRows(t, pool)

	for _, c := range []struct {
		target string
		form   url.Values
	}{
		{"/f/2/post", url.Values{"title": {"Draft"}, "body": {"Some **bold** text."}, "preview": {"1"}}},
		{"/t/1/reply", url.Values{"body": {"Some **bold** text."}, "preview": {"1"}}},
		{"/p/2/edit", url.Values{"title": {"Welcome to the example board"}, "body": {"Some **bold** text."}, "preview": {"1"}}},
	} {
		// alice may edit only her own post: use post 1 for the edit.
		target := c.target
		if target == "/p/2/edit" {
			target = "/p/1/edit"
		}
		rec := send(t, h, target, a, c.form)
		page := bodyOf(t, rec)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: preview status %d", target, rec.Code)
			continue
		}
		bodies, problems := postBodies(page)
		for _, pr := range problems {
			t.Errorf("%s: %s", target, pr)
		}
		if len(bodies) != 1 || !strings.Contains(bodies[0], "<strong>bold</strong>") {
			t.Errorf("%s: preview bodies = %q", target, bodies)
			continue
		}
		htmlcheck.Check(t, bodies[0])
		// The preview is a post as the topic page shows it, with the author card.
		if !strings.Contains(page, `<article class="post`) || !strings.Contains(page, "Posts: <b>120</b>") || !strings.Contains(page, "alice") {
			t.Errorf("%s: the preview does not look like a post", target)
		}
		if got, _ := textareaOf(t, page, "body"); got != "Some **bold** text." {
			t.Errorf("%s: the text box holds %q", target, got)
		}
	}
	if got := countPostRows(t, pool); got != n {
		t.Errorf("posts: %d, want %d", got, n)
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("a preview changed %v", diff)
	}
}

// TestPreviewOfEditAgreesWithSave: the preview of an edit parses with the flags
// and the images of the stored post, so it shows what the save will store.
func TestPreviewOfEditAgreesWithSave(t *testing.T) {
	b := bundletest.Small()
	// Post 3 is carol's. It was written with smilies off and holds an image, which
	// carol may keep, but members hold no post.images and hold post.smilies.
	b.Posts[2].Body = bundle.Body{Source: "plain :) ![pic](https://img.example/a.png)", Markdown: true, Smilies: false, Autolink: true}
	h, _ := siteWithPool(t, b, Options{})
	c := signIn(t, h, "carol", "example-password")
	src := "still :) and **more** ![pic](https://img.example/a.png)"
	rec := send(t, h, "/p/3/edit", c, url.Values{"body": {src}, "preview": {"1"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview of the edit = %d\n%s", rec.Code, bodyOf(t, rec))
	}
	bodies, _ := postBodies(bodyOf(t, rec))
	if len(bodies) != 1 {
		t.Fatalf("%d preview bodies", len(bodies))
	}
	preview := bodies[0]
	if n := strings.Count(preview, "<img"); n != 1 || !strings.Contains(preview, "img.example/a.png") {
		t.Errorf("the preview holds %d images, want the stored one only (a smiley is an image too): %s", n, preview)
	}
	if !strings.Contains(preview, "<strong>more</strong>") {
		t.Errorf("the preview lacks Markdown: %s", preview)
	}
	// A new image is refused in the preview as in the save.
	withNew := src + " ![new](https://img.example/b.png)"
	for _, form := range []url.Values{{"body": {withNew}, "preview": {"1"}}, {"body": {withNew}}} {
		rec = send(t, h, "/p/3/edit", c, form)
		if rec.Code != http.StatusBadRequest || !strings.Contains(bodyOf(t, rec), "You may not post images in this forum.") {
			t.Errorf("new image (preview %v) = %d", form.Has("preview"), rec.Code)
		}
	}
	// The save shows the same body as the preview.
	rec = send(t, h, "/p/3/edit", c, url.Values{"body": {src}})
	idOf(t, rec)
	saved, _ := postBodies(topicOf(t, h, 3, c))
	found := false
	for _, s := range saved {
		found = found || s == preview
	}
	if !found {
		t.Errorf("preview and save differ:\npreview: %s\nsaved:   %q", preview, saved)
	}
}

// tokenRow is one POST route of TestPostingNeedsToken. The actor holds every right
// that the request needs, and setup puts the board where the request would change
// something, so that a missing check on the token shows as a 303 and a change,
// and never as a refusal of the rights. The first step of the test sends the
// request with a good token and wants 303: a row that fails there is refused by
// something other than the token.
type tokenRow struct {
	target string
	actor  string // "" is a guest
	form   url.Values
	setup  string
}

var tokenRows = []tokenRow{
	{"/f/2/post", "alice", url.Values{"title": {"x"}, "body": {"No token."}}, ""},
	{"/t/1/reply", "alice", url.Values{"body": {"No token."}}, ""},
	{"/p/1/edit", "alice", url.Values{"title": {"x"}, "body": {"No token."}}, ""},
	{"/p/1/delete", "bob", url.Values{"confirm": {"1"}}, ""},
	{"/p/4/restore", "bob", url.Values{}, `update posts set visibility = 'deleted', visibility_before_delete = 'approved' where id = 4`},
	{"/p/3/approve", "bob", url.Values{}, ""},
	{"/t/1/lock", "bob", url.Values{}, ""},
	{"/t/1/unlock", "bob", url.Values{}, `update topics set locked = true where id = 1`},
	{"/f/2/post", "", url.Values{"title": {"x"}, "body": {"No token."}, "guest_name": {"Visitor 9"}}, ""},
	{"/t/1/reply", "", url.Values{"body": {"No token."}, "guest_name": {"Visitor 9"}}, ""},
	{"/account/password", "alice", url.Values{"current": {testPassword}, "next": {"a new pass phrase"}, "confirm": {"a new pass phrase"}}, ""},
	{"/account/email", "alice", url.Values{"email": {"alice.new@example.org"}, "current": {testPassword}}, ""},
	{"/account/signature", "alice", url.Values{"signature": {"No token."}}, ""},
	{"/login", "", url.Values{"name": {"alice"}, "password": {testPassword}}, ""},
	{"/logout", "alice", url.Values{}, ""},
	{"/scheme", "alice", url.Values{"scheme": {"slate-deep"}}, ""},
	{"/read", "alice", url.Values{}, ""},
	{"/f/2/read", "alice", url.Values{}, ""},
}

// tokenSite is the posting board where bob moderates forum 2 and guests may post
// there with no approval, with the setup of the row applied.
func tokenSite(t *testing.T, row tokenRow) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	b := postingBoard()
	b.Grants = append(b.Grants, guestGrant(2, "topic.start"), guestGrant(2, "topic.reply"),
		guestGrant(2, "post.no_approval"), guestGrant(2, "post.markdown"))
	h, pool := siteWithPool(t, b, Options{})
	for _, o := range []string{"mod.delete", "mod.approve", "mod.edit", "mod.lock"} {
		exec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (2, 2, '`+o+`', 3)`)
	}
	if row.setup != "" {
		exec(t, pool, row.setup)
	}
	return h, pool
}

// routePatterns asks a running site for the patterns on its muxes (the recorder
// in site.go, not the source text) and returns the POST routes, with the method
// stripped, and the patterns that name no method. Such a pattern accepts POST
// too, so the caller has to account for each one. A pattern with a method other
// than GET and POST fails the test: it needs a row of its own.
func routePatterns(t *testing.T) (post, methodless []string) {
	t.Helper()
	var s *site
	onNew = func(got *site) { s = got }
	t.Cleanup(func() { onNew = nil })
	smallSite(t)
	onNew = nil
	if s == nil || len(s.patterns) == 0 {
		t.Fatal("the site recorded no pattern")
	}
	for _, p := range s.patterns {
		method, rest, found := strings.Cut(p, " ")
		switch {
		case found && method == "POST":
			post = append(post, rest)
		case found && (method == "GET" || method == "HEAD"):
		case found:
			t.Errorf("pattern %q has a method that no table covers", p)
		default:
			methodless = append(methodless, p)
		}
	}
	if len(post) == 0 {
		t.Fatal("found no POST route on the mux")
	}
	return post, methodless
}

// patternRegexp turns a route pattern such as /p/{id}/edit into an expression
// that matches an address of that route.
func patternRegexp(pattern string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.MustCompile(`\\\{[a-z]+\\\}`).ReplaceAllString(regexp.QuoteMeta(pattern), `[^/]+`) + `$`)
}

// TestPostRoutesHaveTokenRows: every POST route on the mux has a row in
// tokenRows, and every row names a route that exists, the way TestPostRoutesHaveCaps
// forces a row in bodyCaps. A new route then cannot skip the token test.
func TestPostRoutesHaveTokenRows(t *testing.T) {
	patterns, methodless := routePatterns(t)
	for _, p := range patterns {
		re := patternRegexp(p)
		found := false
		for _, row := range tokenRows {
			found = found || re.MatchString(row.target)
		}
		if !found {
			t.Errorf("POST %s has no row in tokenRows", p)
		}
	}
	for _, row := range tokenRows {
		found := false
		for _, p := range patterns {
			found = found || patternRegexp(p).MatchString(row.target)
		}
		if !found {
			t.Errorf("tokenRows has %s, which no POST route matches", row.target)
		}
	}
	// A pattern with no method accepts POST. Each one on the mux is named here,
	// and must refuse a POST by itself, so it needs no token row.
	h, _ := smallSite(t)
	for _, p := range methodless {
		want, known := map[string]int{"/": http.StatusNotFound, "/healthz": http.StatusMethodNotAllowed}[p]
		if !known {
			t.Errorf("the mux has the method-less pattern %q: name it here with the answer to a POST, or give it a method", p)
			continue
		}
		if rec := request(t, h, http.MethodPost, p, nil, url.Values{}); rec.Code != want {
			t.Errorf("POST %s = %d, want %d", p, rec.Code, want)
		}
	}
	if !patternRegexp("/p/{id}/edit").MatchString("/p/1/edit") || patternRegexp("/p/{id}/edit").MatchString("/p/1/delete") {
		t.Error("patternRegexp does not match as it should")
	}
}

// TestPostingNeedsToken: every POST route that changes data refuses a request with
// no token, a wrong token, the token of another visitor, and a cross-site origin,
// and changes nothing. Each row is first sent with a good token to show that the
// token is the only thing in the way. The routes run in the order of the table,
// and a route that skips site.protect fails its own rows.
func TestPostingNeedsToken(t *testing.T) {
	for _, row := range tokenRows {
		name := row.target + " as " + row.actor
		if row.actor == "" {
			name = row.target + " as a guest"
		}
		t.Run(name, func(t *testing.T) {
			login := func(h http.Handler) []*http.Cookie {
				if row.actor == "" {
					return nil
				}
				return signIn(t, h, row.actor, "example-password")
			}
			// The control: a good token gets 303.
			h, _ := tokenSite(t, row)
			if rec := send(t, h, row.target, login(h), row.form); rec.Code != http.StatusSeeOther {
				t.Fatalf("POST %s with a good token = %d, want 303 (the row is refused by something other than the token)\n%s",
					row.target, rec.Code, bodyOf(t, rec))
			}

			h, pool := tokenSite(t, row)
			cookies := login(h)
			before := dumpData(t, pool)
			guestPage := getAs(t, h, "/", nil)
			wrong := cloneForm(row.form)
			wrong.Set("csrf", "nope")
			other := cloneForm(row.form)
			other.Set("csrf", formToken(t, bodyOf(t, guestPage)))
			for what, f := range map[string]url.Values{"no token": row.form, "wrong token": wrong} {
				if rec := request(t, h, http.MethodPost, row.target, cookies, f); rec.Code != http.StatusForbidden {
					t.Errorf("POST %s, %s: status %d, want 403", row.target, what, rec.Code)
				}
			}
			if row.actor != "" {
				// A guest's token does not work for a member.
				if rec := request(t, h, http.MethodPost, row.target, cookies, other); rec.Code != http.StatusForbidden {
					t.Errorf("POST %s with a guest's token: status %d, want 403", row.target, rec.Code)
				}
			}
			// A request from another site is refused on the origin alone: it carries
			// a good token for the visitor, and the control above showed that the
			// same request from the same site gets 303.
			page := getAs(t, h, "/", cookies)
			withToken := jar(cookies, page)
			good := cloneForm(row.form)
			good.Set("csrf", formToken(t, bodyOf(t, page)))
			for what, hdr := range map[string]map[string]string{
				"Sec-Fetch-Site cross-site": {"Sec-Fetch-Site": "cross-site"},
				"a foreign Origin":          {"Origin": "https://attacker.example"},
				"Sec-Fetch-Site same-site":  {"Sec-Fetch-Site": "same-site"},
			} {
				r := httptest.NewRequest(http.MethodPost, row.target, strings.NewReader(good.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				for k, v := range hdr {
					r.Header.Set(k, v)
				}
				for _, c := range withToken {
					r.AddCookie(c)
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, r)
				if rec.Code != http.StatusForbidden {
					t.Errorf("POST %s with a good token and %s: status %d, want 403", row.target, what, rec.Code)
				}
			}
			if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
				t.Errorf("a refused POST changed %v", diff)
			}
		})
	}
}

// TestWritesCheckOwnershipOnThePage: changing the ID in the URL gives 403 or 404,
// never a change (Review Focus 5).
func TestWritesCheckOwnershipOnThePage(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	c := signIn(t, h, "carol", "example-password")
	before := dumpData(t, pool)
	// Post 1 is alice's; post 2 is bob's; post 5 is in a forum carol cannot read.
	for _, c2 := range []struct {
		target string
		want   int
	}{
		{"/p/1/edit", 403}, {"/p/2/edit", 403}, {"/p/5/edit", 404}, {"/p/9999/edit", 404},
		{"/p/1/delete", 403}, {"/p/2/delete", 403}, {"/p/5/delete", 404}, {"/p/9999/delete", 404},
	} {
		if rec := getAs(t, h, c2.target, c); c2.target[len(c2.target)-4:] == "edit" && rec.Code != c2.want {
			t.Errorf("GET %s = %d, want %d", c2.target, rec.Code, c2.want)
		}
		rec := send(t, h, c2.target, c, url.Values{"body": {"hijack"}, "confirm": {"1"}})
		if rec.Code != c2.want {
			t.Errorf("POST %s = %d, want %d", c2.target, rec.Code, c2.want)
		}
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("a refused write changed %v", diff)
	}
}

func guestBoard(held bool) *bundletest.Board {
	b := richBoard()
	// Guests may start topics and reply in forum 2.
	b.Grants = append(b.Grants, guestGrant(2, "topic.start"), guestGrant(2, "topic.reply"), guestGrant(2, "post.markdown"))
	if !held {
		b.Grants = append(b.Grants, guestGrant(2, "post.no_approval"))
	}
	return b
}

func TestGuestPostingPage(t *testing.T) {
	h, pool := siteWithPool(t, guestBoard(false), Options{})

	// The guest sees New topic and the name field.
	forum := bodyOf(t, get(t, h, "/f/2"))
	if !strings.Contains(forum, "New topic") || !strings.Contains(forum, `href="/f/2/post"`) {
		t.Error("the guest sees no New topic where guests may start a topic")
	}
	form := bodyOf(t, get(t, h, "/f/2/post"))
	if !strings.Contains(form, "Your name") || !strings.Contains(form, `name="guest_name"`) {
		t.Error("the guest form lacks Your name")
	}
	// A guest name that belongs to a member is refused, and the form keeps the text.
	rec := postGuest(t, h, "/f/2/post", nil, url.Values{"title": {"Help"}, "body": {"Locked out."}, "guest_name": {"Alice"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a member's name: status %d, want 400", rec.Code)
	}
	page := bodyOf(t, rec)
	if !strings.Contains(page, "That name belongs to a member. Sign in, or choose another name.") {
		t.Error("the form lacks the member-name message")
	}
	if got, _ := inputValue(page, "guest_name"); got != "Alice" {
		t.Errorf("the name field holds %q", got)
	}
	// An empty name is refused.
	rec = postGuest(t, h, "/f/2/post", nil, url.Values{"title": {"Help"}, "body": {"Locked out."}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("no name: status %d, want 400", rec.Code)
	}

	// A guest posts a topic.
	rec = postGuest(t, h, "/f/2/post", nil, url.Values{"title": {"Help me"}, "body": {"I forgot my *password*."}, "guest_name": {"Visitor 9"}})
	first := idOf(t, rec)
	var authorID *int64
	var name, ip string
	if err := pool.QueryRow(t.Context(), `select author_id, author_name, host(author_ip) from posts where id = $1`, first).Scan(&authorID, &name, &ip); err != nil {
		t.Fatal(err)
	}
	if authorID != nil || name != "Visitor 9" || ip == "" {
		t.Errorf("stored author = %v %q %q", authorID, name, ip)
	}
	page = topicOf(t, h, first, nil)
	art := article(t, page, first)
	if !strings.Contains(art, "<bdi>Visitor 9</bdi>") || strings.Contains(art, "/u/") {
		t.Errorf("the guest's name is not plain text:\n%s", art)
	}
	if !strings.Contains(art, "Guest") {
		t.Error("the post lacks the Guest label")
	}
	// A guest sees no Edit or Delete, and no Quote of a post it may not reply to.
	if strings.Contains(art, "/edit") || strings.Contains(art, "/delete") || strings.Contains(art, ">Edit<") || strings.Contains(art, ">Delete<") {
		t.Errorf("a guest sees Edit or Delete:\n%s", art)
	}
	if rec := get(t, h, fmt.Sprintf("/p/%d/edit", first)); rec.Code != http.StatusForbidden {
		t.Errorf("guest GET edit = %d, want 403", rec.Code)
	}
	if rec := postGuest(t, h, fmt.Sprintf("/p/%d/delete", first), nil, url.Values{"confirm": {"1"}}); rec.Code != http.StatusForbidden {
		t.Errorf("guest POST delete = %d, want 403", rec.Code)
	}

	// A guest replies. The name is data, never markup.
	hostile := `<img src=x onerror=alert(1)>`
	rec = postGuest(t, h, fmt.Sprintf("/t/%d/reply", mustTopic(t, pool, first)), nil, url.Values{"body": {"Thanks."}, "guest_name": {hostile}})
	if rec.Code != http.StatusBadRequest {
		// The name rules refuse markup characters, or the page shows them as text.
		page = bodyOf(t, get(t, h, fmt.Sprintf("/t/%d", mustTopic(t, pool, first))))
		if strings.Contains(page, "<img src=x") {
			t.Error("a guest name was written as markup")
		}
	}
	rec = postGuest(t, h, fmt.Sprintf("/t/%d/reply", mustTopic(t, pool, first)), nil, url.Values{"body": {"Thanks."}, "guest_name": {"Visitor 9"}})
	idOf(t, rec)

	// A forum without the grant: no button, 403 on the form and on the POST.
	forum3 := bodyOf(t, get(t, h, "/f/3"))
	if strings.Contains(forum3, "New topic") {
		t.Error("the guest sees New topic where guests may not start a topic")
	}
	if rec := get(t, h, "/f/3/post"); rec.Code != http.StatusForbidden {
		t.Errorf("GET /f/3/post as a guest = %d, want 403", rec.Code)
	}
	if rec := postGuest(t, h, "/f/3/post", nil, url.Values{"title": {"x"}, "body": {"y"}, "guest_name": {"Visitor"}}); rec.Code != http.StatusForbidden {
		t.Errorf("POST /f/3/post as a guest = %d, want 403", rec.Code)
	}
	if rec := get(t, h, "/t/2/reply"); rec.Code != http.StatusForbidden {
		t.Errorf("GET /t/2/reply as a guest = %d, want 403", rec.Code)
	}
	if strings.Contains(bodyOf(t, get(t, h, "/t/2")), ">Reply</a>") {
		t.Error("the guest sees Reply where guests may not reply")
	}
	if rec := get(t, h, "/f/3/post?x=1"); rec.Code != http.StatusForbidden {
		t.Errorf("GET /f/3/post?x=1 = %d", rec.Code)
	}
}

// mustTopic returns the topic of a post.
func mustTopic(t *testing.T, pool *pgxpool.Pool, post int) int {
	t.Helper()
	var id int
	if err := pool.QueryRow(t.Context(), `select topic_id from posts where id = $1`, post).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// postGuest sends a form as a guest. The guest's token comes from the form page.
func postGuest(t *testing.T, h http.Handler, target string, cookies []*http.Cookie, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rec, _ := postAs(t, h, target, cookies, form)
	return rec
}

// A guest post that waits for approval is not visible to the guest, so the
// answer goes to the topic or the forum with a note, and not to a 404.
func TestGuestHeldPostAnswer(t *testing.T) {
	h, pool := siteWithPool(t, guestBoard(true), Options{})
	rec := postGuest(t, h, "/f/2/post", nil, url.Values{"title": {"Held"}, "body": {"Please let me in."}, "guest_name": {"Visitor 9"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/f/2?held=1" {
		t.Fatalf("held topic = %d %q, want 303 /f/2?held=1", rec.Code, rec.Header().Get("Location"))
	}
	page := bodyOf(t, get(t, h, "/f/2?held=1"))
	if !strings.Contains(page, "waits for a moderator to approve") {
		t.Error("the forum page lacks the note")
	}
	if strings.Contains(bodyOf(t, get(t, h, "/f/2")), "waits for a moderator to approve") {
		t.Error("the note shows without held=1")
	}
	var vis string
	if err := pool.QueryRow(t.Context(), `select visibility from posts order by id desc limit 1`).Scan(&vis); err != nil || vis != "unapproved" {
		t.Errorf("visibility = %q, %v", vis, err)
	}
	rec = postGuest(t, h, "/t/1/reply", nil, url.Values{"body": {"Held reply."}, "guest_name": {"Visitor 9"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/t/1?held=1" {
		t.Errorf("held reply = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if !strings.Contains(bodyOf(t, get(t, h, "/t/1?held=1")), "waits for a moderator to approve") {
		t.Error("the topic page lacks the note")
	}
}

// payload is one line of the corpus, decoded.
type payload struct {
	line int
	text string
}

var payloadEscape = regexp.MustCompile(`\{(?:U\+([0-9A-Fa-f]{4,6})|X:([0-9A-Fa-f]{2}))\}`)

func loadPayloads(t *testing.T) []payload {
	t.Helper()
	f, err := os.Open("../../markup/testdata/xss/payloads.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []payload
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if strings.HasPrefix(line, "%%") || line == "" {
			continue
		}
		text := payloadEscape.ReplaceAllStringFunc(line, func(m string) string {
			sub := payloadEscape.FindStringSubmatch(m)
			if sub[1] != "" {
				v, _ := strconv.ParseUint(sub[1], 16, 32)
				return string(rune(v))
			}
			v, _ := strconv.ParseUint(sub[2], 16, 8)
			return string([]byte{byte(v)})
		})
		out = append(out, payload{line: n, text: text})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// policySources returns the sources of a directive, or nil.
func policySources(policy, name string) []string {
	v, ok := directive(policy, name)
	if !ok {
		return nil
	}
	return strings.Fields(v)
}

// policyDirectives splits a policy into its directives.
func policyDirectives(policy string) map[string]string {
	out := map[string]string{}
	for _, d := range strings.Split(policy, ";") {
		d = strings.TrimSpace(d)
		if name, v, _ := strings.Cut(d, " "); name != "" {
			out[name] = v
		}
	}
	return out
}

// policyProblems compares the policy of a page with the policy of a page that
// has no payloads (base), directive by directive. Without an embed the two must
// be equal. With an embed, four directives may grow by what the embed needs and
// nothing else: frame-src, script-src and connect-src add only origins of the
// embed table (markup.EmbedOrigins, EmbedScriptOrigins, EmbedConnectOrigins) to
// 'self'; style-src may add 'unsafe-inline'.
// Every other directive stays as it is.
func policyProblems(policy, base string, embeds bool) []string {
	var out []string
	got, want := policyDirectives(policy), policyDirectives(base)
	explained := map[string]bool{}
	if embeds {
		explained = map[string]bool{"frame-src": true, "script-src": true, "connect-src": true, "style-src": true}
	}
	for name := range want {
		if !explained[name] && got[name] != want[name] {
			out = append(out, fmt.Sprintf("%s = %q, want %q", name, got[name], want[name]))
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			out = append(out, fmt.Sprintf("new directive %s %q", name, got[name]))
		}
	}
	if !embeds {
		return out
	}
	for _, src := range strings.Fields(got["frame-src"]) {
		ok := src == "'none'"
		for _, o := range markup.EmbedOrigins() {
			ok = ok || src == o
		}
		if !ok {
			out = append(out, fmt.Sprintf("frame-src holds %q, which is no embed origin", src))
		}
	}
	// The origins that an embed adds to script-src and connect-src are the ones of
	// the embed table, in the list of their own directive.
	for name, table := range map[string][]string{"script-src": markup.EmbedScriptOrigins(), "connect-src": markup.EmbedConnectOrigins()} {
		srcs := strings.Fields(got[name])
		if len(srcs) == 0 || srcs[0] != "'self'" {
			out = append(out, fmt.Sprintf("%s = %v", name, srcs))
		}
		for _, src := range srcs[min(1, len(srcs)):] {
			if !slices.Contains(table, src) {
				out = append(out, fmt.Sprintf("%s holds %q, which is no origin of the embed table", name, src))
			}
		}
	}
	for _, src := range strings.Fields(got["style-src"]) {
		if src != "'self'" && src != "'unsafe-inline'" {
			out = append(out, fmt.Sprintf("style-src holds %q", src))
		}
	}
	return out
}

// checkPolicy fails the test for a policy that is not the base policy and that no
// embed explains.
func checkPolicy(t *testing.T, where, policy, base string, embeds bool) {
	t.Helper()
	for _, pr := range policyProblems(policy, base, embeds) {
		t.Errorf("%s: policy: %s", where, pr)
	}
	checkScriptSrc(t, where, policy)
}

// checkScripts fails for a script element of a page that is not the script of the
// layout or a provider script from an origin that the policy allows. No script
// has a body.
func checkScripts(t *testing.T, where, page, policy string) (extra int) {
	t.Helper()
	srcs := policySources(policy, "script-src")
	z := xhtml.NewTokenizer(strings.NewReader(page))
	inScript := false
	for {
		switch z.Next() {
		case xhtml.ErrorToken:
			return extra
		case xhtml.EndTagToken:
			inScript = false
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			tn, hasAttr := z.TagName()
			if string(tn) != "script" {
				continue
			}
			inScript = true
			src := ""
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				if string(k) == "src" {
					src = string(v)
				}
			}
			if strings.HasPrefix(src, "/static/gobb.js?v=") {
				continue
			}
			extra++
			ok := false
			for _, s := range srcs {
				ok = ok || (strings.HasPrefix(s, "https://") && strings.HasPrefix(src, s+"/"))
			}
			if !ok {
				t.Errorf("%s: script src %q is not allowed by the policy %v", where, src, srcs)
			}
		case xhtml.TextToken:
			if inScript && strings.TrimSpace(string(z.Raw())) != "" {
				t.Errorf("%s: a script element has a body: %q", where, z.Raw())
			}
		}
	}
}

// checkScriptSrc fails for a script-src that allows inline script, eval, any
// host, or a data URL.
func checkScriptSrc(t *testing.T, where, policy string) {
	t.Helper()
	srcs := policySources(policy, "script-src")
	if len(srcs) == 0 || srcs[0] != "'self'" {
		t.Errorf("%s: script-src = %v", where, srcs)
	}
	for _, s := range srcs {
		if strings.Contains(s, "unsafe") || strings.Contains(s, "*") || strings.HasPrefix(s, "data:") || s == "https:" || s == "http:" {
			t.Errorf("%s: script-src holds %q", where, s)
		}
	}
}

// TestPayloadsThroughPosting posts every line of the script-injection corpus
// through the real form, and checks every post body on every page that shows it
// with the strict HTML check. It reads the raw bytes of each .post-body element.
func TestPayloadsThroughPosting(t *testing.T) {
	payloads := loadPayloads(t)
	if len(payloads) < 150 {
		t.Fatalf("%d payloads", len(payloads))
	}
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", "example-password")

	// Every payload goes through the preview and then through a save. A payload
	// that the form refuses (an empty text, or bytes that are not UTF-8) must come
	// back with the text in the form and no post.
	// The policy of a page with no payloads in it.
	basePolicy := getAs(t, h, "/t/2", a).Header().Get("Content-Security-Policy")
	if basePolicy != wantBaseCSP {
		t.Fatalf("the policy of a plain topic page is %q", basePolicy)
	}
	page := bodyOf(t, getAs(t, h, "/t/1/reply", a))
	token := formToken(t, page)
	previewed, accepted, refused, checked := 0, 0, 0, 0
	for _, p := range payloads {
		rec := request(t, h, http.MethodPost, "/t/1/reply", a, url.Values{"csrf": {token}, "body": {p.text}, "preview": {"1"}})
		out := bodyOf(t, rec)
		valid := utf8.ValidString(p.text) && !strings.ContainsRune(p.text, 0) && strings.TrimSpace(p.text) != ""
		switch {
		case rec.Code == http.StatusOK:
			previewed++
			bs, problems := postBodies(out)
			for _, pr := range problems {
				t.Errorf("payloads.txt:%d preview: %s", p.line, pr)
			}
			for _, b := range bs {
				htmlcheck.Check(t, b)
				checked++
			}
		case rec.Code == http.StatusBadRequest && !valid:
		default:
			t.Errorf("payloads.txt:%d: preview status %d for %q", p.line, rec.Code, p.text)
			continue
		}
		// The page shows the text in the box and nowhere else as markup.
		if valid {
			if got, _ := textareaOf(t, out, "body"); got != p.text {
				t.Errorf("payloads.txt:%d: the text box holds %q, want %q", p.line, got, p.text)
			}
		}
		where := fmt.Sprintf("payloads.txt:%d preview", p.line)
		csp := rec.Header().Get("Content-Security-Policy")
		extra := checkScripts(t, where, out, csp)
		checkPolicy(t, where, csp, basePolicy, extra > 0 || strings.Contains(out, "<iframe"))

		rec = request(t, h, http.MethodPost, "/t/1/reply", a, url.Values{"csrf": {token}, "body": {p.text}})
		switch {
		case rec.Code == http.StatusSeeOther:
			accepted++
		case rec.Code == http.StatusBadRequest:
			refused++
		default:
			t.Errorf("payloads.txt:%d: save status %d", p.line, rec.Code)
		}
	}
	if accepted < 100 {
		t.Fatalf("only %d payloads were saved", accepted)
	}

	// Page through the topic: every post body passes the check, and the policy is
	// the base policy unless an embed explains the difference.
	pages, bodies, posts := 0, 0, 0
	for n := 1; ; n++ {
		rec := getAs(t, h, "/t/1?page="+strconv.Itoa(n), a)
		if rec.Code == http.StatusNotFound {
			break
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("page %d: status %d", n, rec.Code)
		}
		out := bodyOf(t, rec)
		pages++
		bs, problems := postBodies(out)
		for _, pr := range problems {
			t.Errorf("topic page %d: %s", n, pr)
		}
		if len(bs) != articleCount(out) {
			t.Errorf("page %d: %d post bodies for %d posts", n, len(bs), articleCount(out))
		}
		for _, b := range bs {
			htmlcheck.Check(t, b)
		}
		bodies += len(bs)
		posts += articleCount(out)
		where := fmt.Sprintf("topic page %d", n)
		csp := rec.Header().Get("Content-Security-Policy")
		extra := checkScripts(t, where, out, csp)
		checkPolicy(t, where, csp, basePolicy, extra > 0 || strings.Contains(out, "<iframe"))
	}
	var visible int
	if err := pool.QueryRow(t.Context(), `select count(*) from posts where topic_id = 1 and visibility = 'approved'`).Scan(&visible); err != nil {
		t.Fatal(err)
	}
	// Post 3 of the fixture is held, so it does not show to alice.
	if bodies != visible {
		t.Errorf("checked %d post bodies on %d pages, but the topic holds %d approved posts", bodies, pages, visible)
	}
	t.Logf("payloads: %d; previewed: %d (%d preview bodies checked); saved: %d; refused: %d; topic pages: %d; post bodies checked on the pages: %d",
		len(payloads), previewed, checked, accepted, refused, pages, bodies)
}

func TestCSPStrictEverywhere(t *testing.T) {
	h, _ := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", "example-password")
	paths := append([]string{"/f/2/post", "/f/3/post", "/t/1/reply", "/t/1/reply?quote=1", "/t/2/reply", "/p/1/edit", "/p/2/edit"}, getPaths...)
	seen := 0
	for _, who := range []struct {
		name    string
		cookies []*http.Cookie
	}{{"guest", nil}, {"alice", a}} {
		for _, p := range paths {
			rec := getAs(t, h, p, who.cookies)
			csp := rec.Header().Get("Content-Security-Policy")
			where := who.name + " GET " + p
			checkScriptSrc(t, where, csp)
			for name, want := range map[string]string{"object-src": "'none'", "base-uri": "'none'", "form-action": "'self'"} {
				if got, _ := directive(csp, name); got != want {
					t.Errorf("%s: %s = %q, want %q", where, name, got, want)
				}
			}
			if strings.Contains(csp, "unsafe-eval") {
				t.Errorf("%s: policy has unsafe-eval", where)
			}
			seen++
		}
	}
	// style-src may hold 'unsafe-inline' only for a page that loads a provider
	// script; none of these pages does.
	if seen == 0 {
		t.Fatal("no path was checked")
	}
	// The form pages hold no inline script and no style attribute either. They
	// are real pages for alice, not the 404 that every missing path would give.
	for _, p := range []string{"/f/2/post", "/t/1/reply", "/t/1/reply?quote=1", "/p/1/edit"} {
		rec := getAs(t, h, p, a)
		body := bodyOf(t, rec)
		if rec.Code != http.StatusOK || !strings.Contains(body, `<form class="post-form"`) {
			t.Errorf("%s: status %d, and the page lacks the form", p, rec.Code)
		}
		if strings.Contains(body, " style=") || strings.Contains(body, "<style") || regexp.MustCompile(`(?i)\son[a-z]+=`).MatchString(body) {
			t.Errorf("%s holds an inline style or an event handler", p)
		}
	}
}

func TestTextareaValueEscapes(t *testing.T) {
	for _, s := range []string{"", "plain", "\nleading newline", "\r\nleading crlf", "a\r\nb\rc\nd", "</textarea><script>alert(1)</script>", "&amp; &#13; &lt;", "\x00nul", "\xffbad", "é☃"} {
		page := "<textarea name=\"body\">" + string(textareaValue(s)) + "</textarea>"
		got, ok := textareaOf(t, page, "body")
		want := s
		if !utf8.ValidString(want) || strings.IndexByte(want, 0) >= 0 {
			want = strings.Map(func(r rune) rune {
				if r == 0 {
					return utf8.RuneError
				}
				return r
			}, strings.ToValidUTF8(want, "�"))
		}
		if !ok || got != want {
			t.Errorf("textareaValue(%q) reads back as %q, want %q", s, got, want)
		}
	}
}
