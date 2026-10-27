package web

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/login"
)

// contentSite is siteWithPool that also returns the data directory.
func contentSite(t *testing.T, b *bundletest.Board, opt Options) (http.Handler, *pgxpool.Pool, string) {
	t.Helper()
	pool, dataDir := boardtest.Load(t, b)
	opt.DataDir = dataDir
	if opt.LoginFloor == 0 {
		opt.LoginFloor = time.Millisecond
	}
	h, err := New(t.Context(), pool, opt)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	return h, pool, dataDir
}

// uploadForm posts a multipart form to target as the visitor of cookies, with a
// good token, the fields, and data as the file of the field "image" under the
// name filename. A nil data sends no file.
func uploadForm(t *testing.T, h http.Handler, cookies []*http.Cookie, target string, fields url.Values, filename string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	page := getAs(t, h, "/", cookies)
	cookies = jar(cookies, page)
	m := csrfField.FindStringSubmatch(bodyOf(t, page))
	if m == nil {
		t.Fatal("the index has no csrf field")
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("csrf", m[1])
	for k, vs := range fields {
		for _, v := range vs {
			_ = mw.WriteField(k, v)
		}
	}
	if data != nil {
		fw, err := mw.CreateFormFile("image", filename)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(data)
	}
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, target, &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// filesIn lists the names in the folder dir of the data directory, sorted.
func filesIn(t *testing.T, dataDir, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, dir))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

// newFile returns the one name in names that is not in before.
func newFile(t *testing.T, before, names []string) string {
	t.Helper()
	var added []string
	for _, n := range names {
		if !slices.Contains(before, n) {
			added = append(added, n)
		}
	}
	if len(added) != 1 {
		t.Fatalf("new files = %v, want exactly one", added)
	}
	return added[0]
}

var staticBodyRE = regexp.MustCompile(`(?s)<div class="post-body static-body">(.*?)</div>\s*</article>`)

// staticBody returns the HTML of the body of the page /pages/slug.
func staticBody(t *testing.T, h http.Handler, slug string) string {
	t.Helper()
	rec := get(t, h, "/pages/"+slug)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /pages/%s = %d", slug, rec.Code)
	}
	m := staticBodyRE.FindStringSubmatch(bodyOf(t, rec))
	if m == nil {
		t.Fatalf("/pages/%s has no static body", slug)
	}
	return m[1]
}

// see checks that rec is a 303 to want.
func see(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
		t.Errorf("answer = %d to %q, want 303 to %q\n%.300s", rec.Code, rec.Header().Get("Location"), want, bodyOf(t, rec))
	}
}

// richPage is imported by the tests, and saved again in the editor.
func richPage() bundle.StaticPage {
	src := "# Rules\r\n\r\nBe **kind** :) and visit https://example.com/rules today.\r\n\r\n- one\r\n- two\r\n\r\n> quote\r\n\r\n[a link](https://example.com/a)\r\n"
	return bundle.StaticPage{Slug: "rich", Title: "Rich", Body: bundle.Body{Source: strings.ReplaceAll(src, "\r\n", "\n"), Markdown: true, Smilies: true, Autolink: true},
		UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func contentBoard() *bundletest.Board {
	b := bundletest.Small()
	b.StaticPages = append(b.StaticPages, richPage())
	return b
}

func TestAdminContentIndexLinks(t *testing.T) {
	h, _, _ := contentSite(t, contentBoard(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	index := bodyOf(t, getAs(t, h, "/admin/", alice))
	for _, href := range []string{`href="/admin/pages"`, `href="/admin/smilies"`, `href="/admin/ranks"`} {
		if !strings.Contains(index, href) {
			t.Errorf("the admin index lacks %s", href)
		}
	}
	for _, path := range []string{"/admin/pages", "/admin/pages/new", "/admin/pages/about", "/admin/pages/about/delete",
		"/admin/smilies", "/admin/smilies/1", "/admin/smilies/1/delete", "/admin/ranks", "/admin/ranks/1", "/admin/ranks/1/delete"} {
		if rec := getAs(t, h, path, alice); rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d", path, rec.Code)
		}
	}
	for _, path := range []string{"/admin/pages/nowhere", "/admin/pages/nowhere/delete", "/admin/smilies/99", "/admin/smilies/x", "/admin/smilies/0/delete",
		"/admin/ranks/99", "/admin/ranks/99/delete"} {
		if rec := getAs(t, h, path, alice); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}

// A page saved in the editor renders as the page that the import brought.
func TestAdminPageRendersLikeTheImport(t *testing.T) {
	h, pool, _ := contentSite(t, contentBoard(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	imported := richPage()
	// A browser sends the line breaks of a text box as CRLF.
	form := url.Values{"slug": {"rich-copy"}, "title": {"Rich"}, "markdown": {"1"}, "smilies": {"1"}, "autolink": {"1"},
		"body": {strings.ReplaceAll(imported.Body.Source, "\n", "\r\n")}}
	rec, _ := postAs(t, h, "/admin/pages", alice, form)
	see(t, rec, "/admin/pages/rich-copy?done=created")
	if a, b := staticBody(t, h, "rich"), staticBody(t, h, "rich-copy"); a != b || !strings.Contains(a, `class="smiley"`) {
		t.Errorf("the saved page differs from the imported page:\n%s\n---\n%s", a, b)
	}
	var same bool
	if err := pool.QueryRow(t.Context(), `select a.body_tree = b.body_tree and a.body_source = b.body_source from static_pages a, static_pages b
 where a.slug = 'rich' and b.slug = 'rich-copy'`).Scan(&same); err != nil || !same {
		t.Errorf("the stored columns differ (%v)", err)
	}
	// The saved page is in the footer and in the list.
	if !strings.Contains(bodyOf(t, get(t, h, "/")), `href="/pages/rich-copy"`) {
		t.Error("the footer lacks the new page")
	}
	if list := bodyOf(t, getAs(t, h, "/admin/pages?done=created", alice)); !strings.Contains(list, `href="/admin/pages/rich-copy"`) || !strings.Contains(list, "The page is added.") {
		t.Errorf("the list lacks the new page or the notice")
	}
}

func TestAdminPageWritesAndRefusals(t *testing.T) {
	h, pool, _ := contentSite(t, contentBoard(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	post := func(target string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		rec, _ := postAs(t, h, target, alice, form)
		return rec
	}
	rec := post("/admin/pages", url.Values{"slug": {"faq"}, "title": {"FAQ"}, "body": {"Ask <b>away</b> & wait."}, "markdown": {"1"}})
	see(t, rec, "/admin/pages/faq?done=created")
	rows := modLog(t, pool)
	if last := rows[len(rows)-1]; last.Actor != 1 || last.Action != "page.create" || last.Target != "page" {
		t.Errorf("log row = %+v", last)
	}
	var via, slug string
	if err := pool.QueryRow(t.Context(), `select details->>'via', details->>'slug' from mod_log order by id desc limit 1`).Scan(&via, &slug); err != nil || via != "web" || slug != "faq" {
		t.Errorf("details via %q slug %q (%v)", via, slug, err)
	}
	// The notice shows on the page that the 303 leads to, and the text is escaped.
	page := bodyOf(t, getAs(t, h, "/admin/pages/faq?done=created", alice))
	if !strings.Contains(page, "The page is added.") || !strings.Contains(page, "Ask &lt;b&gt;away&lt;/b&gt; &amp; wait.") {
		t.Errorf("the edit page lacks the notice or the escaped text:\n%s", page)
	}
	if body := staticBody(t, h, "faq"); strings.Contains(body, "<b>away") {
		t.Errorf("the page body holds raw HTML: %s", body)
	}
	if strings.Contains(bodyOf(t, getAs(t, h, "/admin/pages/faq?done=bogus", alice)), `role="status"`) {
		t.Error("an unknown notice code shows a notice")
	}

	// Refusals: 400, the form again with the typed values and the message beside
	// the field, and no change.
	before := dumpData(t, pool)
	for _, tc := range []struct {
		name, target string
		form         url.Values
		field        string
		keep         string
	}{
		{"slug in use", "/admin/pages", url.Values{"slug": {"faq"}, "title": {"T"}, "body": {"kept text"}}, "page-slug", "kept text"},
		{"slug bad", "/admin/pages", url.Values{"slug": {"Bad Slug"}, "title": {"T"}, "body": {"kept text"}}, "page-slug", "Bad Slug"},
		{"slug reserved", "/admin/pages", url.Values{"slug": {"new"}, "title": {"T"}, "body": {"kept text"}}, "page-slug", "kept text"},
		{"title empty", "/admin/pages", url.Values{"slug": {"ok"}, "title": {" "}, "body": {"kept text"}}, "page-title", "kept text"},
		{"body empty", "/admin/pages", url.Values{"slug": {"ok"}, "title": {"T"}, "body": {""}}, "page-body", `value="T"`},
		{"body too long", "/admin/pages/faq", url.Values{"title": {"T"}, "body": {strings.Repeat("a", bundle.MaxSourceBytes+1)}}, "page-body", `value="T"`},
		{"title on update", "/admin/pages/faq", url.Values{"title": {""}, "body": {"kept text"}}, "page-title", "kept text"},
	} {
		rec := post(tc.target, tc.form)
		body := bodyOf(t, rec)
		if rec.Code != http.StatusBadRequest || !strings.Contains(body, `id="`+tc.field+`-error"`) || !strings.Contains(body, `aria-invalid="true"`) || !strings.Contains(body, tc.keep) {
			t.Errorf("%s: %d, error beside %s: %v, kept %q: %v", tc.name, rec.Code, tc.field, strings.Contains(body, tc.field+"-error"), tc.keep, strings.Contains(body, tc.keep))
		}
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("refused writes changed %v", diff)
	}

	// Update, and the same update again.
	rec = post("/admin/pages/faq", url.Values{"title": {"FAQ 2"}, "body": {"New text"}, "markdown": {"1"}, "smilies": {"1"}})
	see(t, rec, "/admin/pages/faq?done=saved")
	rec = post("/admin/pages/faq", url.Values{"title": {"FAQ 2"}, "body": {"New text"}, "markdown": {"1"}, "smilies": {"1"}})
	see(t, rec, "/admin/pages/faq?done=nochange")
	if n := len(modLog(t, pool)); n != len(rows)+1 {
		t.Errorf("%d log rows, want %d", n, len(rows)+1)
	}
	// A POST to a missing page is the 404 page.
	if rec := post("/admin/pages/nowhere", url.Values{"title": {"T"}, "body": {"x"}}); rec.Code != http.StatusNotFound {
		t.Errorf("POST to a missing page = %d", rec.Code)
	}

	// Delete: the confirm page names the page; the POST deletes it.
	confirm := bodyOf(t, getAs(t, h, "/admin/pages/about/delete", alice))
	if !strings.Contains(confirm, `action="/admin/pages/about/delete"`) || !strings.Contains(confirm, "1 old address") {
		t.Errorf("the confirm page:\n%s", confirm)
	}
	rec = post("/admin/pages/faq/delete", url.Values{})
	see(t, rec, "/admin/pages?done=deleted")
	if rec := get(t, h, "/pages/faq"); rec.Code != http.StatusNotFound {
		t.Errorf("the deleted page = %d", rec.Code)
	}
	if strings.Contains(bodyOf(t, get(t, h, "/")), `href="/pages/faq"`) {
		t.Error("the footer still links the deleted page")
	}
	if rec := post("/admin/pages/faq/delete", url.Values{}); rec.Code != http.StatusNotFound {
		t.Errorf("second delete = %d", rec.Code)
	}
}

// Only an admin gets to the editors, and a refused request changes nothing.
func TestAdminContentRefusesOthers(t *testing.T) {
	h, pool, dataDir := contentSite(t, contentBoard(), Options{})
	visitors := map[string][]*http.Cookie{"a guest": nil, "bob": signIn(t, h, "bob", testPassword)}
	// The snapshot comes after the sign-in, which writes the users.
	before, files := dumpData(t, pool), filesIn(t, dataDir, "smilies")
	for who, cookies := range visitors {
		for _, target := range []string{"/admin/pages", "/admin/pages/about", "/admin/pages/about/delete", "/admin/smilies/1/move",
			"/admin/ranks/1/delete", "/admin/smilies/1/delete"} {
			if rec, _ := postAs(t, h, target, cookies, url.Values{"title": {"x"}, "body": {"x"}, "slug": {"x"}, "dir": {"up"}}); rec.Code != http.StatusNotFound {
				t.Errorf("%s: POST %s = %d, want 404", who, target, rec.Code)
			}
		}
		for _, target := range []string{"/admin/smilies", "/admin/smilies/1", "/admin/ranks", "/admin/ranks/1"} {
			rec := uploadForm(t, h, cookies, target, url.Values{"code": {":x"}, "title": {"x"}, "min_posts": {"1"}}, "x.png", testPNG(t, 8, 8))
			if rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
				t.Errorf("%s: upload to %s = %d, want 404 or 403", who, target, rec.Code)
			}
		}
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("refused requests changed %v", diff)
	}
	if got := filesIn(t, dataDir, "smilies"); !slices.Equal(got, files) {
		t.Errorf("files = %v, want %v", got, files)
	}
}

func TestAdminSmileyEditor(t *testing.T) {
	h, pool, dataDir := contentSite(t, contentBoard(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	before := filesIn(t, dataDir, "smilies")

	// Add a smiley. The name of the file is made by the server.
	rec := uploadForm(t, h, alice, "/admin/smilies", url.Values{"code": {":P"}, "description": {"tongue"}}, "../../evil name.png", testPNG(t, 20, 16))
	see(t, rec, "/admin/smilies?done=created")
	name := newFile(t, before, filesIn(t, dataDir, "smilies"))
	if !regexp.MustCompile(`^s-[0-9a-f]{16}\.png$`).MatchString(name) {
		t.Errorf("file name = %q", name)
	}
	if path := "/files/smilies/" + name; get(t, h, path).Code != http.StatusOK {
		t.Errorf("GET %s = %d", path, get(t, h, path).Code)
	}
	var id int64
	var image string
	if err := pool.QueryRow(t.Context(), `select id, image_path from smilies where code = ':P'`).Scan(&id, &image); err != nil || image != "smilies/"+name {
		t.Fatalf("row: id %d image %q (%v)", id, image, err)
	}
	rows := modLog(t, pool)
	if last := rows[len(rows)-1]; last.Actor != 1 || last.Action != "smiley.create" || last.Target != "smiley" || last.ID != int(id) {
		t.Errorf("log row = %+v", last)
	}
	var via string
	if err := pool.QueryRow(t.Context(), `select details->>'via' from mod_log order by id desc limit 1`).Scan(&via); err != nil || via != "web" {
		t.Errorf("via = %q (%v)", via, err)
	}
	if list := bodyOf(t, getAs(t, h, "/admin/smilies?done=created", alice)); !strings.Contains(list, "<code>:P</code>") || !strings.Contains(list, "The smiley is added.") ||
		!strings.Contains(list, `src="/files/smilies/`+name+`"`) {
		t.Errorf("the list lacks the smiley:\n%s", list)
	}

	// The next page that is saved shows it; a page that was saved before keeps
	// the code as text until it is saved again. Deleting the smiley turns every
	// use back into text at the next render.
	rec, _ = postAs(t, h, "/admin/pages", alice, url.Values{"slug": {"fun"}, "title": {"Fun"}, "body": {"Hello :P"}, "smilies": {"1"}})
	see(t, rec, "/admin/pages/fun?done=created")
	if body := staticBody(t, h, "fun"); !strings.Contains(body, `<img class="smiley" src="/files/smilies/`+name+`"`) {
		t.Errorf("the page does not show the new smiley: %s", body)
	}

	// Bad uploads and bad values: the status, the message beside the field, no row, no
	// file, no log row.
	snapshot, logs := dumpData(t, pool), len(modLog(t, pool))
	files := filesIn(t, dataDir, "smilies")
	big := bytes.Repeat([]byte{0x89}, 1<<20+10)
	for _, tc := range []struct {
		name   string
		fields url.Values
		data   []byte
		status int
		field  string
		text   string
	}{
		{"not an image", url.Values{"code": {":a"}}, []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), 400, "add-image-error", "Pick a PNG, JPEG or GIF."},
		{"too many pixels", url.Values{"code": {":a"}}, testPNG(t, 513, 8), 400, "add-image-error", "at most 512 pixels"},
		{"over 1 MB", url.Values{"code": {":a"}}, big, 413, "add-image-error", "at most 1 MB"},
		{"no file", url.Values{"code": {":a"}}, nil, 400, "add-image-error", "Pick an image."},
		{"code in use", url.Values{"code": {":P"}}, testPNG(t, 8, 8), 400, "add-code-error", "Another smiley has this code."},
		{"code in use, imported", url.Values{"code": {":)"}}, testPNG(t, 8, 8), 400, "add-code-error", "Another smiley has this code."},
		{"code empty", url.Values{"code": {" "}}, testPNG(t, 8, 8), 400, "add-code-error", "Enter a code."},
		{"code long", url.Values{"code": {strings.Repeat("x", 33)}}, testPNG(t, 8, 8), 400, "add-code-error", "1 to 32 characters"},
		{"description long", url.Values{"code": {":a"}, "description": {strings.Repeat("d", 256)}}, testPNG(t, 8, 8), 400, "add-description-error", "longer than 255"},
	} {
		rec := uploadForm(t, h, alice, "/admin/smilies", tc.fields, "x.png", tc.data)
		body := bodyOf(t, rec)
		if rec.Code != tc.status || !strings.Contains(body, `id="`+tc.field+`"`) || !strings.Contains(body, tc.text) {
			t.Errorf("%s: status %d (want %d); error %s: %v; text %q: %v", tc.name, rec.Code, tc.status, tc.field, strings.Contains(body, tc.field), tc.text, strings.Contains(body, tc.text))
		}
		if tc.fields.Get("code") != "" && strings.TrimSpace(tc.fields.Get("code")) != "" && !strings.Contains(body, `value="`+tc.fields.Get("code")+`"`) {
			t.Errorf("%s: the form lost the typed code", tc.name)
		}
	}
	if diff := diffTables(snapshot, dumpData(t, pool)); len(diff) != 0 || len(modLog(t, pool)) != logs {
		t.Errorf("refused writes changed %v", diff)
	}
	if got := filesIn(t, dataDir, "smilies"); !slices.Equal(got, files) {
		t.Errorf("a refused upload left files: %v, want %v", got, files)
	}

	// Edit: a new code and image. The generated old file goes; the page that
	// holds the old code shows it as text until the page is saved again.
	rec = uploadForm(t, h, alice, fmt.Sprintf("/admin/smilies/%d", id), url.Values{"code": {":p"}, "description": {"tongue out"}}, "n.png", testPNG(t, 10, 10))
	see(t, rec, "/admin/smilies?done=saved")
	after := filesIn(t, dataDir, "smilies")
	if slices.Contains(after, name) {
		t.Error("the replaced file is still on disk")
	}
	if len(after) != len(files) {
		t.Errorf("files = %v, want %d", after, len(files))
	}
	if body := staticBody(t, h, "fun"); strings.Contains(body, `class="smiley"`) || !strings.Contains(body, ":P") {
		t.Errorf("the page after the code change: %s", body)
	}
	// The same form again: no change, a notice, no log row.
	logs = len(modLog(t, pool))
	rec = uploadForm(t, h, alice, fmt.Sprintf("/admin/smilies/%d", id), url.Values{"code": {":p"}, "description": {"tongue out"}}, "", nil)
	see(t, rec, "/admin/smilies?done=nochange")
	if len(modLog(t, pool)) != logs {
		t.Error("a save with no change wrote a log row")
	}
	// A code in use on edit: 400 on the edit page.
	rec = uploadForm(t, h, alice, fmt.Sprintf("/admin/smilies/%d", id), url.Values{"code": {":)"}}, "", nil)
	if body := bodyOf(t, rec); rec.Code != http.StatusBadRequest || !strings.Contains(body, `id="smiley-code-error"`) {
		t.Errorf("code in use on edit = %d", rec.Code)
	}
	// An imported file is replaced and removed too, once no row names it.
	rec = uploadForm(t, h, alice, "/admin/smilies/1", url.Values{"code": {":)"}, "description": {"smile"}}, "n.png", testPNG(t, 10, 10))
	see(t, rec, "/admin/smilies?done=saved")
	if slices.Contains(filesIn(t, dataDir, "smilies"), "smile.gif") {
		t.Error("the replaced imported file is still on disk")
	}

	// Reorder.
	order := func() string {
		rows, err := pool.Query(t.Context(), `select code from smilies order by position, id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var codes []string
		for rows.Next() {
			var c string
			_ = rows.Scan(&c)
			codes = append(codes, c)
		}
		return strings.Join(codes, " ")
	}
	if got := order(); got != ":) :D :p" {
		t.Fatalf("order = %q", got)
	}
	rec, _ = postAs(t, h, fmt.Sprintf("/admin/smilies/%d/move", id), alice, url.Values{"dir": {"up"}})
	see(t, rec, "/admin/smilies?done=moved")
	if got := order(); got != ":) :p :D" {
		t.Errorf("after up: %q", got)
	}
	rows = modLog(t, pool)
	if last := rows[len(rows)-1]; last.Action != "smiley.reorder" || last.Actor != 1 {
		t.Errorf("log row = %+v", last)
	}
	rec, _ = postAs(t, h, "/admin/smilies/1/move", alice, url.Values{"dir": {"up"}})
	see(t, rec, "/admin/smilies?done=nochange")
	if rec, _ := postAs(t, h, "/admin/smilies/1/move", alice, url.Values{"dir": {"sideways"}}); rec.Code != http.StatusNotFound {
		t.Errorf("a bad direction = %d", rec.Code)
	}

	// Delete: the confirm page, then the POST. The file goes after the commit.
	confirm := bodyOf(t, getAs(t, h, fmt.Sprintf("/admin/smilies/%d/delete", id), alice))
	if !strings.Contains(confirm, "show the code as text") || !strings.Contains(confirm, fmt.Sprintf(`action="/admin/smilies/%d/delete"`, id)) {
		t.Errorf("the confirm page:\n%s", confirm)
	}
	var current string
	_ = pool.QueryRow(t.Context(), `select image_path from smilies where id = $1`, id).Scan(&current)
	rec, _ = postAs(t, h, fmt.Sprintf("/admin/smilies/%d/delete", id), alice, url.Values{})
	see(t, rec, "/admin/smilies?done=deleted")
	if slices.Contains(filesIn(t, dataDir, "smilies"), strings.TrimPrefix(current, "smilies/")) {
		t.Error("the file of the deleted smiley is on disk")
	}
	if got := order(); got != ":) :D" {
		t.Errorf("order after delete = %q", got)
	}
	if rec, _ := postAs(t, h, fmt.Sprintf("/admin/smilies/%d/delete", id), alice, url.Values{}); rec.Code != http.StatusNotFound {
		t.Errorf("second delete = %d", rec.Code)
	}
}

// A file that two rows name stays on disk until the last row goes.
func TestAdminSmileySharedFile(t *testing.T) {
	h, pool, dataDir := contentSite(t, contentBoard(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	// The two smilies share one file.
	exec(t, pool, `update smilies set image_path = 'smilies/smile.gif' where id = 2`)
	rec, _ := postAs(t, h, "/admin/smilies/1/delete", alice, url.Values{})
	see(t, rec, "/admin/smilies?done=deleted")
	if !slices.Contains(filesIn(t, dataDir, "smilies"), "smile.gif") {
		t.Fatal("the shared file went with the first smiley")
	}
	rec, _ = postAs(t, h, "/admin/smilies/2/delete", alice, url.Values{})
	see(t, rec, "/admin/smilies?done=deleted")
	if slices.Contains(filesIn(t, dataDir, "smilies"), "smile.gif") {
		t.Error("the file stayed after the last row")
	}
}

func TestAdminRankEditor(t *testing.T) {
	h, pool, dataDir := contentSite(t, contentBoard(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	before := filesIn(t, dataDir, "ranks")

	rec := uploadForm(t, h, alice, "/admin/ranks", url.Values{"title": {"Regular"}, "min_posts": {"50"}}, "r.png", testPNG(t, 40, 12))
	see(t, rec, "/admin/ranks?done=created")
	name := newFile(t, before, filesIn(t, dataDir, "ranks"))
	if !regexp.MustCompile(`^r-[0-9a-f]{16}\.png$`).MatchString(name) {
		t.Errorf("file name = %q", name)
	}
	var id int64
	if err := pool.QueryRow(t.Context(), `select id from ranks where title = 'Regular'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	rows := modLog(t, pool)
	if last := rows[len(rows)-1]; last.Actor != 1 || last.Action != "rank.create" || last.Target != "rank" || last.ID != int(id) {
		t.Errorf("log row = %+v", last)
	}
	// A special rank with no image.
	rec = uploadForm(t, h, alice, "/admin/ranks", url.Values{"title": {"Staff"}, "min_posts": {"0"}, "special": {"1"}}, "", nil)
	see(t, rec, "/admin/ranks?done=created")
	list := bodyOf(t, getAs(t, h, "/admin/ranks", alice))
	for _, want := range []string{"Newcomer", "Regular", "Staff", "special", "from 50 posts", "2 members hold it", `src="/files/ranks/` + name + `"`} {
		if !strings.Contains(list, want) {
			t.Errorf("the list lacks %q", want)
		}
	}

	// Refusals.
	snapshot, logs, files := dumpData(t, pool), len(modLog(t, pool)), filesIn(t, dataDir, "ranks")
	for _, tc := range []struct {
		name   string
		fields url.Values
		data   []byte
		status int
		field  string
		text   string
	}{
		{"title empty", url.Values{"title": {" "}, "min_posts": {"1"}}, nil, 400, "add-title-error", "Enter a title."},
		{"title long", url.Values{"title": {strings.Repeat("t", 256)}, "min_posts": {"1"}}, nil, 400, "add-title-error", "longer than 255"},
		{"posts text", url.Values{"title": {"x"}, "min_posts": {"many"}}, nil, 400, "add-min-error", "whole number"},
		{"posts empty", url.Values{"title": {"x"}, "min_posts": {""}}, nil, 400, "add-min-error", "whole number"},
		{"posts negative", url.Values{"title": {"x"}, "min_posts": {"-1"}}, nil, 400, "add-min-error", "0 to 2,147,483,647"},
		{"posts over int32", url.Values{"title": {"x"}, "min_posts": {"2147483648"}}, nil, 400, "add-min-error", "0 to 2,147,483,647"},
		{"posts over int64", url.Values{"title": {"x"}, "min_posts": {"99999999999999999999"}}, nil, 400, "add-min-error", "0 to 2,147,483,647"},
		{"bad image", url.Values{"title": {"x"}, "min_posts": {"1"}}, []byte("GIF89 not really"), 400, "add-image-error", "Pick a PNG, JPEG or GIF."},
		{"big image", url.Values{"title": {"x"}, "min_posts": {"1"}}, testPNG(t, 8, 600), 400, "add-image-error", "512 pixels"},
	} {
		rec := uploadForm(t, h, alice, "/admin/ranks", tc.fields, "x.png", tc.data)
		body := bodyOf(t, rec)
		if rec.Code != tc.status || !strings.Contains(body, `id="`+tc.field+`"`) || !strings.Contains(body, tc.text) {
			t.Errorf("%s: status %d (want %d); error %s: %v; text %q: %v", tc.name, rec.Code, tc.status, tc.field, strings.Contains(body, tc.field), tc.text, strings.Contains(body, tc.text))
		}
		if !strings.Contains(body, `value="`+tc.fields.Get("min_posts")+`"`) && tc.fields.Get("min_posts") != "" {
			t.Errorf("%s: the form lost the typed number", tc.name)
		}
	}
	if diff := diffTables(snapshot, dumpData(t, pool)); len(diff) != 0 || len(modLog(t, pool)) != logs {
		t.Errorf("refused writes changed %v", diff)
	}
	if got := filesIn(t, dataDir, "ranks"); !slices.Equal(got, files) {
		t.Errorf("a refused upload left files: %v, want %v", got, files)
	}

	// Edit: fields and a new image; the old generated file goes.
	target := fmt.Sprintf("/admin/ranks/%d", id)
	rec = uploadForm(t, h, alice, target, url.Values{"title": {"Veteran"}, "min_posts": {"100"}}, "n.png", testPNG(t, 30, 30))
	see(t, rec, "/admin/ranks?done=saved")
	if after := filesIn(t, dataDir, "ranks"); slices.Contains(after, name) || len(after) != len(files) {
		t.Errorf("files after the replace = %v", after)
	}
	// Remove the image.
	rec = uploadForm(t, h, alice, target, url.Values{"title": {"Veteran"}, "min_posts": {"100"}, "remove_image": {"1"}}, "", nil)
	see(t, rec, "/admin/ranks?done=saved")
	if got := filesIn(t, dataDir, "ranks"); len(got) != len(files)-1 {
		t.Errorf("files after the removal = %v", got)
	}
	rec = uploadForm(t, h, alice, target, url.Values{"title": {"Veteran"}, "min_posts": {"100"}, "remove_image": {"1"}}, "", nil)
	see(t, rec, "/admin/ranks?done=nochange")
	// A bad value on edit: the edit page again.
	rec = uploadForm(t, h, alice, target, url.Values{"title": {"Veteran"}, "min_posts": {"x"}}, "", nil)
	if body := bodyOf(t, rec); rec.Code != http.StatusBadRequest || !strings.Contains(body, `id="rank-min-error"`) || !strings.Contains(body, `value="x"`) {
		t.Errorf("bad number on edit = %d", rec.Code)
	}

	// Delete a rank that members hold: the confirm text says what happens to them.
	confirm := bodyOf(t, getAs(t, h, "/admin/ranks/1/delete", alice))
	if !strings.Contains(confirm, "2 members hold this rank. They lose it") || !strings.Contains(confirm, `action="/admin/ranks/1/delete"`) {
		t.Errorf("the confirm page:\n%s", confirm)
	}
	rec, _ = postAs(t, h, "/admin/ranks/1/delete", alice, url.Values{})
	see(t, rec, "/admin/ranks?done=deleted")
	var held, users int
	if err := pool.QueryRow(t.Context(), `select count(*) filter (where rank_id is not null), count(*) from users`).Scan(&held, &users); err != nil || held != 0 || users != 4 {
		t.Errorf("members with a rank %d of %d (%v)", held, users, err)
	}
	if slices.Contains(filesIn(t, dataDir, "ranks"), "newcomer.png") {
		t.Error("the file of the deleted rank is on disk")
	}
	rows = modLog(t, pool)
	if last := rows[len(rows)-1]; last.Action != "rank.delete" || last.Actor != 1 || last.ID != 1 {
		t.Errorf("log row = %+v", last)
	}
	// The members still show a rank from their post count? Newcomer is gone, so none.
	if rec, _ := postAs(t, h, "/admin/ranks/1/delete", alice, url.Values{}); rec.Code != http.StatusNotFound {
		t.Errorf("second delete = %d", rec.Code)
	}
}

// The upload of an admin takes a slot like an avatar does: one upload for each
// admin, and a share of the memory gate.
func TestAdminUploadLimits(t *testing.T) {
	gate := login.NewGate(1 << 20)
	var s *site
	onNew = func(got *site) { s = got }
	t.Cleanup(func() { onNew = nil })
	h, pool, dataDir := contentSite(t, contentBoard(), Options{LoginGate: gate, LoginGateWait: time.Millisecond})
	onNew = nil
	alice := signIn(t, h, "alice", testPassword)
	// The sign-in is done: now the uploads find the whole budget taken.
	held, err := gate.Acquire(context.Background(), gate.Budget(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	snapshot, files := dumpData(t, pool), filesIn(t, dataDir, "smilies")

	// The gate is full: 503 with Retry-After, and nothing is written.
	rec := uploadForm(t, h, alice, "/admin/smilies", url.Values{"code": {":x"}}, "x.png", testPNG(t, 8, 8))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" || !strings.Contains(bodyOf(t, rec), msgBusy) {
		t.Errorf("full gate: %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	// The admin has an upload in flight: 503 at once.
	s.uploading.Store(int64(1), struct{}{})
	rec = uploadForm(t, h, alice, "/admin/ranks", url.Values{"title": {"x"}, "min_posts": {"1"}}, "x.png", testPNG(t, 8, 8))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("second upload of one admin = %d, want 503", rec.Code)
	}
	if diff := diffTables(snapshot, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("refused uploads changed %v", diff)
	}
	if got := filesIn(t, dataDir, "smilies"); !slices.Equal(got, files) {
		t.Errorf("files = %v", got)
	}
}

// The new pages and forms carry what the admin area needs: noindex, no-store,
// the csrf token in each form, and escaped values.
func TestAdminContentPagesAreAdminPages(t *testing.T) {
	h, _, _ := contentSite(t, contentBoard(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	for _, target := range []string{"/admin/pages", "/admin/pages/new", "/admin/pages/about", "/admin/pages/about/delete", "/admin/smilies", "/admin/smilies/1",
		"/admin/smilies/1/delete", "/admin/ranks", "/admin/ranks/1", "/admin/ranks/1/delete"} {
		rec := getAs(t, h, target, alice)
		body := bodyOf(t, rec)
		if !strings.Contains(body, `<meta name="robots" content="noindex`) || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: noindex or no-store missing (Cache-Control %q)", target, rec.Header().Get("Cache-Control"))
		}
		if forms := strings.Count(body, `method="post"`); forms == 0 || forms > strings.Count(body, `name="csrf"`) {
			t.Errorf("%s: %d POST forms and %d tokens", target, forms, strings.Count(body, `name="csrf"`))
		}
	}
}
