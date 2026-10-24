package web

import (
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// newTestSite imports b into a test database and returns the handler of a site
// on it, and the data directory. A zero BasePath means "/".
func newTestSite(t *testing.T, b *bundletest.Board, opt Options) (http.Handler, string) {
	t.Helper()
	pool, dataDir := boardtest.Load(t, b)
	opt.DataDir = dataDir
	h, err := New(t.Context(), pool, opt)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	return h, dataDir
}

// smallSite is a site on bundletest.Small with the default options.
func smallSite(t *testing.T) (http.Handler, string) {
	t.Helper()
	return newTestSite(t, bundletest.Small(), Options{})
}

// do sends one request with method to h and returns the recorded answer.
func do(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// get sends a GET request to h and returns the recorded answer.
func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, h, http.MethodGet, target)
}

// bodyOf returns the body of rec as a string.
func bodyOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	b, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// bundleForum returns a forum row. A parent of 0 means a top-level forum.
func bundleForum(id, parent int64, kind, name string, pos int) bundle.Forum {
	f := bundle.Forum{ID: id, Kind: kind, Name: name, Position: pos}
	if parent != 0 {
		f.ParentID = &parent
	}
	return f
}

// guestGrant returns a grant of option to the guests group on forum.
func guestGrant(forum int64, option string) bundle.Grant {
	group := int64(1)
	s := bundle.Yes
	return bundle.Grant{GroupID: &group, ForumID: forum, Option: &option, Setting: &s}
}

// at returns a UTC time.
func at(year int, month time.Month, day, hour, min int) time.Time {
	return time.Date(year, month, day, hour, min, 0, 0, time.UTC)
}

func ptr[T any](v T) *T { return &v }

// bundleTopic returns an approved topic by alice.
func bundleTopic(id, forum int64, title, kind string, created time.Time) bundle.Topic {
	return bundle.Topic{
		ID: id, ForumID: forum, Title: title, AuthorID: ptr(int64(1)), AuthorName: "alice",
		CreatedAt: created, Kind: kind, Visibility: "approved",
	}
}

// bundlePost returns an approved post by user author with a Markdown body.
func bundlePost(id, topic, author int64, name string, created time.Time, source string) bundle.Post {
	return bundle.Post{
		ID: id, TopicID: topic, AuthorID: ptr(author), AuthorName: name, CreatedAt: created,
		Subject: "Re: topic", Visibility: "approved",
		Body: bundle.Body{Source: source, Markdown: true, Smilies: true, Autolink: true},
	}
}

// bundlePoll returns a poll for topic with two options.
func bundlePoll(topic int64) bundle.Poll {
	return bundle.Poll{
		TopicID: topic, Question: "Tea or coffee?", MaxOptions: 1,
		Options: []bundle.PollOption{{ID: 1, Text: "Tea", Votes: 4}, {ID: 2, Text: "Coffee", Votes: 6}},
	}
}

// rowOf returns the table row of the given class that holds text, or "".
func rowOf(body, class, text string) string {
	for _, part := range strings.Split(body, `<tr class="`+class)[1:] {
		row, _, _ := strings.Cut(part, "</tr>")
		if strings.Contains(row, text) {
			return row
		}
	}
	return ""
}

// csrfField finds the value of a hidden csrf field in a page.
var csrfField = regexp.MustCompile(`name="csrf" value="([^"]*)"`)

// withoutCSRF replaces the value of every csrf field in body, so that the pages
// of two guests with no cookie can be compared byte for byte. A guest with no
// cookie gets a new random secret, and so a new token, on every page.
func withoutCSRF(body string) string {
	return csrfField.ReplaceAllString(body, `name="csrf" value="TOKEN"`)
}

// shape returns body with the csrf tokens and the address that was asked for
// masked out. The sign-in link and the scheme picker send a visitor back to the
// page that he asked for, so the page of /t/2 holds "/t/2" and the page of
// /t/99 holds "/t/99". Nothing else may differ between the 404 of a hidden
// page and the 404 of a missing one.
func shape(body, target string) string {
	body = withoutCSRF(body)
	for _, form := range []string{url.QueryEscape(target), template.HTMLEscapeString(target), target} {
		body = strings.ReplaceAll(body, form, "REQUEST")
	}
	return body
}

// request sends one request to h with the given cookies and form values.
func request(t *testing.T, h http.Handler, method, target string, cookies []*http.Cookie, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, target, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// getAs sends a GET request with cookies.
func getAs(t *testing.T, h http.Handler, target string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	return request(t, h, http.MethodGet, target, cookies, nil)
}

// jar merges the cookies that rec sets into cookies. A cookie with a negative
// MaxAge removes the cookie of the same name.
func jar(cookies []*http.Cookie, rec *httptest.ResponseRecorder) []*http.Cookie {
	out := append([]*http.Cookie(nil), cookies...)
	for _, set := range rec.Result().Cookies() {
		kept := out[:0]
		for _, c := range out {
			if c.Name != set.Name {
				kept = append(kept, c)
			}
		}
		out = kept
		if set.MaxAge >= 0 && set.Value != "" {
			out = append(out, &http.Cookie{Name: set.Name, Value: set.Value})
		}
	}
	return out
}

// postAs sends a form with the cookies of a visitor. It fetches the index first
// to learn the csrf token of the visitor, unless form holds one. The result is
// the answer to the POST, and the cookies after both requests.
func postAs(t *testing.T, h http.Handler, target string, cookies []*http.Cookie, form url.Values) (*httptest.ResponseRecorder, []*http.Cookie) {
	t.Helper()
	return postAsAt(t, h, "/", target, cookies, form)
}

// postAsAt is postAs for a site under a base path: home is its index.
func postAsAt(t *testing.T, h http.Handler, home, target string, cookies []*http.Cookie, form url.Values) (*httptest.ResponseRecorder, []*http.Cookie) {
	t.Helper()
	if form.Get("csrf") == "" {
		page := getAs(t, h, home, cookies)
		cookies = jar(cookies, page)
		m := csrfField.FindStringSubmatch(bodyOf(t, page))
		if m == nil {
			t.Fatal("the index has no csrf field")
		}
		form = cloneForm(form)
		form.Set("csrf", m[1])
	}
	rec := request(t, h, http.MethodPost, target, cookies, form)
	return rec, jar(cookies, rec)
}

func cloneForm(f url.Values) url.Values {
	out := url.Values{}
	for k, v := range f {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// signIn signs name in with password and returns the cookies of the visitor,
// the session cookie among them. It fails the test when the sign-in fails.
func signIn(t *testing.T, h http.Handler, name, password string) []*http.Cookie {
	t.Helper()
	form := url.Values{"name": {name}, "password": {password}}
	rec, cookies := postAs(t, h, "/login", nil, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("sign-in of %s: status %d\n%s", name, rec.Code, bodyOf(t, rec))
	}
	for _, c := range cookies {
		if c.Name == "gobb_session" {
			return cookies
		}
	}
	t.Fatalf("sign-in of %s set no session cookie", name)
	return nil
}
