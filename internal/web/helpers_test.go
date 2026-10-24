package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

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
