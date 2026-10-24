package web

import (
	"io"
	"net/http"
	"net/http/httptest"
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
