package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// legacyRoute returns a route row for the tests. A nil match means no pairs.
func legacyRoute(path, kind string, idParam *string, targetID *int64, pos int) bundle.LegacyRoute {
	return bundle.LegacyRoute{Path: path, Match: map[string]string{}, IDParam: idParam, TargetKind: kind, TargetID: targetID, Position: pos}
}

// location returns the Location header of rec, or "".
func location(rec *httptest.ResponseRecorder) string { return rec.Header().Get("Location") }

func TestLegacyRedirects(t *testing.T) {
	h, _ := smallSite(t)
	tests := []struct {
		target string
		status int
		loc    string
	}{
		{"/old/thread.cgi?view=full&id=1", 301, "/t/1"},
		{"/old/thread.cgi?view=full&id=1&page=25", 301, "/t/1?page=2"},
		{"/old/thread.cgi?view=full&id=1&page=24", 301, "/t/1"},
		{"/old/thread.cgi?view=full&id=1&page=0", 301, "/t/1"},
		{"/old/thread.cgi?view=full&id=1&page=50", 301, "/t/1?page=3"},
		// Forum 3 is hidden from a guest.
		{"/old/thread.cgi?view=full&id=2", 404, ""},
		{"/old/thread.cgi?view=full&id=abc", 404, ""},
		{"/old/thread.cgi?view=full&id=99", 404, ""},
		{"/old/thread.cgi?view=full&id=0", 404, ""},
		{"/old/thread.cgi?view=full&id=-1", 404, ""},
		{"/old/thread.cgi?view=full&id=01", 404, ""},
		{"/old/thread.cgi?view=full&id=%2B1", 404, ""},
		{"/old/thread.cgi?view=full&id=" + strings.Repeat("9", 30), 404, ""},
		// The offset is a whole number of 0 or more.
		{"/old/thread.cgi?view=full&id=1&page=abc", 404, ""},
		{"/old/thread.cgi?view=full&id=1&page=-1", 404, ""},
		{"/old/thread.cgi?view=full&id=1&page=%2B5", 404, ""},
		{"/old/thread.cgi?view=full&id=1&page=1.5", 404, ""},
		{"/old/thread.cgi?view=full&id=1&page=", 404, ""},
		{"/old/thread.cgi?view=full&id=1&page=" + strings.Repeat("9", 30), 404, ""},
		// A page beyond the largest page number would answer 400 at the target.
		{"/old/thread.cgi?view=full&id=1&page=" + "999999999", 404, ""},
		// Odd forms: the first value of a repeated parameter counts.
		{"/old/thread.cgi?view=full&id=1&id=2", 301, "/t/1"},
		{"/old/thread.cgi?view=full&id=2&id=1", 404, ""},
		{"/old/thread.cgi?view=full&id=1&page=25&page=75", 301, "/t/1?page=2"},
		{"/old/thread.cgi?view=brief&view=full&id=1", 404, ""},
		{"/old/thread.cgi?view=full&view=brief&id=1", 301, "/t/1"},
		// An empty ID value is a value that is not an ID.
		{"/old/thread.cgi?view=full&id=", 404, ""},
		{"/old/thread.cgi?view=full&id", 404, ""},
		{"/old/thread.cgi?view=full", 404, ""},
		{"/old/thread.cgi?view=brief&id=1", 404, ""},
		{"/old/thread.cgi", 404, ""},
		// The path is decoded once, and it must match whole.
		{"/old/thread%2Ecgi?view=full&id=1", 301, "/t/1"},
		{"/old/thread%252Ecgi?view=full&id=1", 404, ""},
		{"/old%2Fthread.cgi?view=full&id=1", 301, "/t/1"},
		{"/old/thread.cgi/?view=full&id=1", 404, ""},
		{"/old//thread.cgi?view=full&id=1", 404, ""},
		{"/old/./thread.cgi?view=full&id=1", 404, ""},
		{"/Old/thread.cgi?view=full&id=1", 404, ""},
		// Route 2 names a gone user.
		{"/old/profile.cgi", 410, ""},
		{"/old/profile.cgi?x=1", 410, ""},
		{"/old/about.cgi", 301, "/pages/about"},
		{"/old/images/smilies/smile.gif", 301, "/files/smilies/smile.gif"},
		{"/old/images/smilies/smile%2Egif", 301, "/files/smilies/smile.gif"},
		{"/old/images/smilies/nope.gif", 404, ""},
		{"/old/images/smilies/", 404, ""},
		{"/old/images/smilies/../../x", 404, ""},
		{"/old/images/other/smile.gif", 404, ""},
	}
	for _, tc := range tests {
		rec := get(t, h, tc.target)
		if rec.Code != tc.status || location(rec) != tc.loc {
			t.Errorf("GET %.90s: %d %q, want %d %q", tc.target, rec.Code, location(rec), tc.status, tc.loc)
		}
	}
	// A HEAD request gets the same answer.
	if rec := do(t, h, http.MethodHead, "/old/about.cgi"); rec.Code != 301 || location(rec) != "/pages/about" {
		t.Errorf("HEAD: %d %q", rec.Code, location(rec))
	}
	// A POST is not a legacy request.
	if rec := do(t, h, http.MethodPost, "/old/about.cgi"); rec.Code != http.StatusNotFound {
		t.Errorf("POST: %d", rec.Code)
	}
	// The redirect leads to the file.
	if rec := get(t, h, "/files/smilies/smile.gif"); rec.Code != 200 {
		t.Errorf("the target of the old smiley path: %d", rec.Code)
	}
}

func TestLegacyRedirectsUnderBasePath(t *testing.T) {
	h, _ := newTestSite(t, bundletest.Small(), Options{BasePath: "/forum/"})
	tests := []struct {
		target string
		status int
		loc    string
	}{
		{"/forum/old/thread.cgi?view=full&id=1", 301, "/forum/t/1"},
		{"/forum/old/thread.cgi?view=full&id=1&page=25", 301, "/forum/t/1?page=2"},
		{"/forum/old/about.cgi", 301, "/forum/pages/about"},
		{"/forum/old/images/smilies/smile.gif", 301, "/forum/files/smilies/smile.gif"},
		{"/forum/old/profile.cgi", 410, ""},
		// Outside the base path, nothing applies.
		{"/old/thread.cgi?view=full&id=1", 404, ""},
		// The base path is not part of the route path.
		{"/forum/forum/old/thread.cgi?view=full&id=1", 404, ""},
	}
	for _, tc := range tests {
		rec := get(t, h, tc.target)
		if rec.Code != tc.status || location(rec) != tc.loc {
			t.Errorf("GET %s: %d %q, want %d %q", tc.target, rec.Code, location(rec), tc.status, tc.loc)
		}
	}
}

// kindsBoard holds one route of each kind, all under old/k/, with the ID in the
// parameter "id".
func kindsBoard() *bundletest.Board {
	b := bundletest.Small()
	id := ptr("id")
	r := func(kind string, withID bool) bundle.LegacyRoute {
		lr := legacyRoute("old/k/"+kind, kind, nil, nil, 0)
		if withID {
			lr.IDParam = id
		}
		return lr
	}
	b.LegacyRoutes = append(b.LegacyRoutes,
		r("home", false), r("members", false), r("team", false), r("search", false), r("account", false),
		r("forum", true), r("topic", true), r("post", true), r("user", true), r("attachment", true), r("avatar", true),
	)
	forum := r("forum", true)
	forum.Path, forum.OffsetParam = "old/k/forum-offset", ptr("start")
	topic := r("topic", true)
	topic.Path, topic.OffsetParam = "old/k/topic-offset", ptr("start")
	fixed := legacyRoute("old/k/fixed-topic", "topic", nil, ptr(int64(1)), 0)
	b.LegacyRoutes = append(b.LegacyRoutes, forum, topic, fixed)
	b.Forums = append(b.Forums, bundleForum(4, 0, "link", "Elsewhere", 5))
	b.Forums[3].LinkURL = ptr("https://example.com/out")
	b.Grants = append(b.Grants, guestGrant(4, "forum.list"))
	// Markers of moved topics in forum 2, which guests read: topic 4 points at a
	// topic they read, topic 5 at a topic they do not.
	for id, to := range map[int64]int64{4: 1, 5: 2} {
		marker := bundleTopic(id, 2, "Moved", "normal", at(2019, 5, 3, 9, 0))
		marker.MovedTo = ptr(to)
		b.Topics = append(b.Topics, marker)
	}
	return b
}

func TestLegacyKinds(t *testing.T) {
	h, pool := siteWithPool(t, kindsBoard(), Options{})
	alice := signIn(t, h, "alice", "example-password")
	bob := signIn(t, h, "bob", "example-password")
	guestRule := func(t *testing.T, cookies []*http.Cookie, target string) *httptest.ResponseRecorder {
		return getAs(t, h, target, cookies)
	}
	tests := []struct {
		who    string // "guest" or "alice"
		target string
		status int
		loc    string
	}{
		{"guest", "/old/k/home", 301, "/"},
		{"guest", "/old/k/members", 301, "/members"},
		{"guest", "/old/k/team", 301, "/team"},
		{"guest", "/old/k/search", 301, "/search"},
		{"guest", "/old/k/account", 301, "/account"},
		{"guest", "/old/k/home?id=7&q=x", 301, "/"},

		{"guest", "/old/k/forum?id=2", 301, "/f/2"},
		{"guest", "/old/k/forum?id=1", 301, "/f/1"},
		{"guest", "/old/k/forum?id=3", 404, ""},
		{"alice", "/old/k/forum?id=3", 301, "/f/3"},
		{"guest", "/old/k/forum?id=99", 404, ""},
		{"guest", "/old/k/forum?id=x", 404, ""},
		{"guest", "/old/k/forum", 404, ""},
		// A link forum redirects to its page, which sends the visitor on.
		{"guest", "/old/k/forum?id=4", 301, "/f/4"},
		{"guest", "/old/k/forum-offset?id=2&start=0", 301, "/f/2"},
		{"guest", "/old/k/forum-offset?id=2&start=49", 301, "/f/2"},
		{"guest", "/old/k/forum-offset?id=2&start=50", 301, "/f/2?page=2"},
		{"guest", "/old/k/forum-offset?id=2&start=120", 301, "/f/2?page=3"},
		{"guest", "/old/k/forum?id=2&start=120", 301, "/f/2"},

		{"guest", "/old/k/topic?id=1", 301, "/t/1"},
		{"guest", "/old/k/topic?id=2", 404, ""},
		{"alice", "/old/k/topic?id=2", 301, "/t/2"},
		{"guest", "/old/k/topic-offset?id=1&start=24", 301, "/t/1"},
		{"guest", "/old/k/topic-offset?id=1&start=25", 301, "/t/1?page=2"},
		{"guest", "/old/k/topic-offset?id=1&start=100", 301, "/t/1?page=5"},
		// The marker of a moved topic goes to its target, if the viewer sees it.
		// Marker 3 sits in forum 3, which guests cannot read.
		{"guest", "/old/k/topic?id=3", 404, ""},
		{"alice", "/old/k/topic?id=3", 301, "/t/1"},
		{"guest", "/old/k/topic?id=4", 301, "/t/1"},
		{"guest", "/old/k/topic?id=5", 404, ""},
		{"alice", "/old/k/topic?id=5", 301, "/t/2"},
		{"guest", "/old/k/fixed-topic", 301, "/t/1"},
		{"guest", "/old/k/fixed-topic?id=2", 301, "/t/1"},

		{"guest", "/old/k/post?id=1", 301, "/p/1"},
		{"guest", "/old/k/post?id=5", 404, ""},
		{"guest", "/old/k/post?id=3", 404, ""},
		{"guest", "/old/k/post?id=99", 404, ""},
		{"alice", "/old/k/post?id=5", 301, "/p/5"},
		// Post 3 waits for a moderator: bob moderates.
		{"bob", "/old/k/post?id=3", 301, "/p/3"},
		{"alice", "/old/k/post?id=3", 404, ""},

		{"alice", "/old/k/user?id=2", 301, "/u/2"},
		{"alice", "/old/k/user?id=8", 410, ""},
		{"guest", "/old/k/user?id=8", 410, ""},
		{"alice", "/old/k/user?id=99", 404, ""},
		{"alice", "/old/k/user?id=x", 404, ""},

		// In this board nobody holds account.download, so the page answers 403.
		{"alice", "/old/k/attachment?id=1", 403, ""},
		{"alice", "/old/k/attachment?id=99", 404, ""},

		{"guest", "/old/k/avatar?id=1", 301, "/files/avatars/1.png"},
		{"guest", "/old/k/avatar?id=2", 404, ""},
		{"guest", "/old/k/avatar?id=99", 404, ""},
		{"guest", "/old/k/avatar?id=8", 404, ""},
		{"guest", "/old/k/avatar", 404, ""},
	}
	for _, tc := range tests {
		var cookies []*http.Cookie
		switch tc.who {
		case "alice":
			cookies = alice
		case "bob":
			cookies = bob
		}
		rec := guestRule(t, cookies, tc.target)
		if rec.Code != tc.status || location(rec) != tc.loc {
			t.Errorf("%s GET %s: %d %q, want %d %q", tc.who, tc.target, rec.Code, location(rec), tc.status, tc.loc)
		}
	}

	// A profile or an attachment that the page refuses is refused the same way by
	// the old link: the answer is the one of the page, body and all. One that
	// the page serves is a 301 to the page.
	for _, c := range []struct{ legacy, page string }{
		{"/old/k/user?id=2", "/u/2"},
		{"/old/k/user?id=8", "/u/8"},
		{"/old/k/attachment?id=1", "/a/1"},
		{"/old/k/attachment?id=2", "/a/2"},
		{"/old/k/attachment?id=99", "/a/99"},
	} {
		for _, cookies := range [][]*http.Cookie{nil, alice} {
			l, p := getAs(t, h, c.legacy, cookies), getAs(t, h, c.page, cookies)
			if p.Code == http.StatusOK {
				if l.Code != http.StatusMovedPermanently || location(l) != c.page {
					t.Errorf("%s: %d %q, but %s serves", c.legacy, l.Code, location(l), c.page)
				}
				continue
			}
			if l.Code != p.Code || shape(bodyOf(t, l), c.legacy) != shape(bodyOf(t, p), c.page) {
				t.Errorf("%s: %d, but %s: %d", c.legacy, l.Code, c.page, p.Code)
			}
		}
	}

	// The old link to an avatar leads to a file that serves. When the file is
	// gone, the old link answers 404 and not a redirect to a 404.
	if rec := get(t, h, "/files/avatars/1.png"); rec.Code != 200 {
		t.Fatalf("avatar file: %d", rec.Code)
	}
	if rec := get(t, h, "/old/k/avatar?id=1"); rec.Code != 301 {
		t.Fatalf("avatar: %d", rec.Code)
	}
	// A static page that is gone gives 404.
	if rec := get(t, h, "/old/about.cgi"); rec.Code != 301 {
		t.Fatalf("about: %d", rec.Code)
	}
	if _, err := pool.Exec(context.Background(), `delete from static_pages where slug = 'about'`); err != nil {
		t.Fatal(err)
	}
	if rec := get(t, h, "/old/about.cgi"); rec.Code != 404 {
		t.Errorf("a page that is gone: %d, want 404", rec.Code)
	}
}

// An attachment redirect follows the download right of the page.
func TestLegacyAttachmentRights(t *testing.T) {
	b := kindsBoard()
	b.Grants = append(b.Grants, guestGrant(0, "account.download"), guestGrant(2, "attach.download"))
	allowGroup(b, 2, 0, "account.download")
	h, _ := newTestSite(t, b, Options{})
	alice := signIn(t, h, "alice", "example-password")
	for _, tc := range []struct {
		cookies []*http.Cookie
		target  string
		status  int
		loc     string
	}{
		{nil, "/old/k/attachment?id=1", 301, "/a/1"},
		// An attachment with no post shows to its uploader and to an admin.
		{nil, "/old/k/attachment?id=2", 404, ""},
		{alice, "/old/k/attachment?id=2", 301, "/a/2"},
	} {
		rec := getAs(t, h, tc.target, tc.cookies)
		if rec.Code != tc.status || location(rec) != tc.loc {
			t.Errorf("GET %s: %d %q, want %d %q", tc.target, rec.Code, location(rec), tc.status, tc.loc)
		}
	}
	// The redirect leads to a file that serves.
	if rec := get(t, h, "/a/1"); rec.Code != 200 {
		t.Errorf("/a/1: %d", rec.Code)
	}
}

func TestLegacyAvatarFileGone(t *testing.T) {
	h, dataDir := newTestSite(t, kindsBoard(), Options{})
	if rec := get(t, h, "/old/k/avatar?id=1"); rec.Code != 301 {
		t.Fatalf("avatar: %d", rec.Code)
	}
	if err := os.Remove(filepath.Join(dataDir, "avatars", "1.png")); err != nil {
		t.Fatal(err)
	}
	if rec := get(t, h, "/old/k/avatar?id=1"); rec.Code != 404 {
		t.Errorf("avatar file gone: %d, want 404", rec.Code)
	}
}

// A legacy file path answers 301 only for a file that /files/ serves.
func TestLegacyFileOnlyWhenItServes(t *testing.T) {
	b := bundletest.Small()
	b.LegacyFiles = append(b.LegacyFiles, bundle.LegacyFile{Prefix: "old/ranks/", Dir: "ranks"})
	h, dataDir := newTestSite(t, b, Options{})
	if rec := get(t, h, "/old/ranks/newcomer.png"); rec.Code != 301 || location(rec) != "/files/ranks/newcomer.png" {
		t.Errorf("rank: %d %q", rec.Code, location(rec))
	}
	// A file that is not an image does not serve, and so it does not redirect.
	if err := os.WriteFile(filepath.Join(dataDir, "smilies", "note.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := get(t, h, "/old/images/smilies/note.txt"); rec.Code != 404 {
		t.Errorf("text file: %d, want 404", rec.Code)
	}
	// A hidden file does not serve.
	if err := os.WriteFile(filepath.Join(dataDir, "smilies", ".hidden.gif"), []byte("GIF89a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := get(t, h, "/old/images/smilies/.hidden.gif"); rec.Code != 404 {
		t.Errorf("dot file: %d, want 404", rec.Code)
	}
	// The directory and the old path with no file name.
	if rec := get(t, h, "/old/ranks/"); rec.Code != 404 {
		t.Errorf("empty rest: %d, want 404", rec.Code)
	}
	if rec := get(t, h, "/old/ranks/sub/x.png"); rec.Code != 404 {
		t.Errorf("missing sub path: %d, want 404", rec.Code)
	}
}

// The site's own routes win over the legacy table.
func TestSiteRoutesWin(t *testing.T) {
	b := bundletest.Small()
	b.LegacyRoutes = append(b.LegacyRoutes,
		legacyRoute("team", "search", nil, nil, 0),
		legacyRoute("t/1", "home", nil, nil, 0),
		legacyRoute("members", "home", nil, nil, 0),
	)
	h, _ := newTestSite(t, b, Options{})
	if rec := get(t, h, "/team"); rec.Code != 200 {
		t.Errorf("/team: %d", rec.Code)
	}
	if rec := get(t, h, "/t/1"); rec.Code != 200 {
		t.Errorf("/t/1: %d", rec.Code)
	}
	if rec := get(t, h, "/members"); rec.Code != http.StatusForbidden {
		t.Errorf("/members: %d, want the 403 of the page", rec.Code)
	}
}

// recorder is a slog handler that keeps the records.
type recorder struct {
	mu   *sync.Mutex
	recs *[]slog.Record
}

func newRecorder() recorder { return recorder{mu: &sync.Mutex{}, recs: &[]slog.Record{}} }

func (r recorder) Enabled(context.Context, slog.Level) bool { return true }
func (r recorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	*r.recs = append(*r.recs, rec)
	return nil
}
func (r recorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r recorder) WithGroup(string) slog.Handler      { return r }

func (r recorder) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, rec := range *r.recs {
		var buf bytes.Buffer
		h := slog.NewTextHandler(&buf, nil)
		_ = h.Handle(context.Background(), rec)
		out = append(out, strings.TrimSpace(buf.String()))
	}
	return out
}

func captureLogs(t *testing.T) recorder {
	t.Helper()
	rec := newRecorder()
	old := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(old) })
	return rec
}

func TestLegacyCollisionWarns(t *testing.T) {
	b := bundletest.Small()
	b.LegacyRoutes = append(b.LegacyRoutes, legacyRoute("search", "home", nil, nil, 0))
	b.LegacyFiles = append(b.LegacyFiles, bundle.LegacyFile{Prefix: "files/smilies/", Dir: "smilies"})
	pool, dataDir := boardtest.Load(t, b)
	logs := captureLogs(t)
	if _, err := New(context.Background(), pool, Options{DataDir: dataDir}); err != nil {
		t.Fatal(err)
	}
	var warns []string
	for _, l := range logs.lines() {
		if strings.Contains(l, "level=WARN") {
			warns = append(warns, l)
		}
	}
	if len(warns) != 2 {
		t.Fatalf("%d warnings, want 2 (the route and the file prefix):\n%s", len(warns), strings.Join(logs.lines(), "\n"))
	}
	if !strings.Contains(warns[0], "path=search") {
		t.Errorf("the warning does not name the route: %s", warns[0])
	}
	if !strings.Contains(warns[1], "prefix=files/smilies/") {
		t.Errorf("the warning does not name the prefix: %s", warns[1])
	}
	// The board without a collision logs none, and one INFO line with the counts
	// comes from the load.
	pool2, dir2 := boardtest.Load(t, bundletest.Small())
	logs = captureLogs(t)
	if _, err := New(context.Background(), pool2, Options{DataDir: dir2}); err != nil {
		t.Fatal(err)
	}
	for _, l := range logs.lines() {
		if strings.Contains(l, "level=WARN") {
			t.Errorf("unexpected warning: %s", l)
		}
	}
}

// A legacy hit logs nothing at INFO or above.
func TestLegacyHitLogsNothing(t *testing.T) {
	h, _ := smallSite(t)
	logs := captureLogs(t)
	for _, target := range []string{"/old/thread.cgi?view=full&id=1", "/old/about.cgi", "/old/images/smilies/smile.gif", "/old/profile.cgi", "/old/nothing"} {
		get(t, h, target)
	}
	for _, l := range logs.lines() {
		t.Errorf("a legacy request logged: %s", l)
	}
}

// A request that matches nothing in the legacy table is still the 404 page.
func TestLegacyNoMatchIs404Page(t *testing.T) {
	h, _ := smallSite(t)
	rec := get(t, h, "/old/nothing.cgi?id=1")
	if rec.Code != 404 || !strings.Contains(bodyOf(t, rec), "Page not found") {
		t.Errorf("status %d", rec.Code)
	}
}

// An http link forum goes to the https form of its target.
func TestLinkForumHTTPUpgrade(t *testing.T) {
	b := bundletest.Small()
	b.Forums = append(b.Forums,
		bundleForum(4, 0, "link", "Plain", 5),
		bundleForum(5, 0, "link", "Mixed", 6),
		bundleForum(6, 0, "link", "Secure", 7),
	)
	b.Forums[3].LinkURL = ptr("http://example.com/out?a=1")
	b.Forums[4].LinkURL = ptr("HTTP://example.com/x")
	b.Forums[5].LinkURL = ptr("https://example.com/y")
	for _, id := range []int64{4, 5, 6} {
		b.Grants = append(b.Grants, guestGrant(id, "forum.list"))
	}
	h, _ := newTestSite(t, b, Options{})
	for target, want := range map[string]string{
		"/f/4": "https://example.com/out?a=1",
		"/f/5": "https://example.com/x",
		"/f/6": "https://example.com/y",
	} {
		rec := get(t, h, target)
		if rec.Code != http.StatusFound || location(rec) != want {
			t.Errorf("%s: %d %q, want 302 %q", target, rec.Code, location(rec), want)
		}
	}
}
