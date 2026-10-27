package web

import (
	"bytes"
	"context"
	"errors"
	"hash/crc32"
	"image"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/mail"
)

// The tests of the settings page of the admin area (/admin/settings).

// fakeSender is a mail.Sender that keeps the messages it was given. When err is
// set, every send fails with it.
type fakeSender struct {
	mu   sync.Mutex
	sent []mail.Message
	err  error
}

func (f *fakeSender) Send(_ context.Context, m mail.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeSender) messages() []mail.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mail.Message(nil), f.sent...)
}

// settingsEnv is a site with the small board, its pool, the data directory and
// the site value. Alice is the admin.
type settingsEnv struct {
	h     http.Handler
	pool  *pgxpool.Pool
	dir   string
	site  *site
	alice []*http.Cookie
}

func newSettingsEnv(t *testing.T, opt Options) *settingsEnv {
	t.Helper()
	e := &settingsEnv{}
	onNew = func(s *site) { e.site = s }
	t.Cleanup(func() { onNew = nil })
	var dir string
	e.h, dir = newTestSite(t, bundletest.Small(), opt)
	onNew = nil
	e.dir = dir
	e.pool = e.site.pool
	if opt.BasePath != "" {
		e.alice = signInAt(t, e.h, opt.BasePath, "alice")
	} else {
		e.alice = signIn(t, e.h, "alice", testPassword)
	}
	return e
}

// form returns the main form with the values the board has now.
func (e *settingsEnv) form(t *testing.T) url.Values {
	t.Helper()
	s := e.stored(t)
	return url.Values{"board_name": {s.BoardName}, "board_description": {s.BoardDescription}, "timezone": {s.Timezone}, "scheme": {s.DefaultScheme}}
}

// withFields returns f with the given fields replaced.
func withFields(f url.Values, kv ...string) url.Values {
	out := cloneForm(f)
	for i := 0; i+1 < len(kv); i += 2 {
		out.Set(kv[i], kv[i+1])
	}
	return out
}

// save posts the main form as cookies.
func (e *settingsEnv) save(t *testing.T, cookies []*http.Cookie, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rec, _ := postAs(t, e.h, "/admin/settings", cookies, form)
	return rec
}

// stored returns the settings in the database.
func (e *settingsEnv) stored(t *testing.T) board.Settings {
	t.Helper()
	s, err := board.LoadSettings(t.Context(), e.pool)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// logRows returns the count of mod_log rows with the action.
func (e *settingsEnv) logRows(t *testing.T, action string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(t.Context(), `select count(*) from mod_log where action = $1`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// uploadHeader posts a file to /admin/settings/header as cookies, with a token
// from the settings page. A nil data sends the form with no file.
func (e *settingsEnv) uploadHeader(t *testing.T, cookies []*http.Cookie, data []byte, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	token := formToken(t, bodyOf(t, getAs(t, e.h, "/admin/settings", cookies)))
	return e.uploadWithToken(t, cookies, token, "header", data, hdr)
}

func (e *settingsEnv) uploadWithToken(t *testing.T, cookies []*http.Cookie, token, field string, data []byte, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if token != "" {
		_ = mw.WriteField("csrf", token)
	}
	if data != nil {
		fw, err := mw.CreateFormFile(field, "banner.png")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(data)
	}
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/admin/settings/header", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	return serveReq(e.h, r)
}

func (e *settingsEnv) headerFiles(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(e.dir, "header"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ent := range ents {
		out = append(out, ent.Name())
	}
	return out
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+3] = 90, 255
	}
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// webpBytes is a lossy WebP of 1 x 1 pixel (base64 of a real file).
func webpBytes(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 0, 64)
	const enc = "UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA"
	const al = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var acc uint
	var bits int
	for _, c := range enc {
		i := strings.IndexRune(al, c)
		if i < 0 {
			continue
		}
		acc = acc<<6 | uint(i)
		bits += 6
		if bits >= 8 {
			bits -= 8
			b = append(b, byte(acc>>uint(bits)))
			acc &= 1<<uint(bits) - 1
		}
	}
	return b
}

func TestAdminSettingsPage(t *testing.T) {
	e := newSettingsEnv(t, Options{})
	rec := getAs(t, e.h, "/admin/settings", e.alice)
	body := bodyOf(t, rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/settings = %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q", cc)
	}
	if !strings.Contains(body, `<meta name="robots" content="noindex">`) {
		t.Error("no robots meta")
	}
	for _, want := range []string{
		`name="board_name" value="Example Board"`,
		`<option value="America/New_York" selected>`,
		`name="scheme" id="scheme-slate" value="slate" checked`,
		`id="scheme-slate-deep" value="slate-deep">`,
		`action="/admin/settings"`, `action="/admin/settings/header"`, `enctype="multipart/form-data"`,
		`accept="image/png,image/jpeg,image/webp"`,
		"Sign-up mode", "Email notifications", "roadmap row 7", "roadmap row 5",
		"Email is off on this server",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// The two read only settings are text, not controls.
	for _, bad := range []string{`name="registration"`, `name="email_notifications"`, `/admin/settings/mail-test`} {
		if strings.Contains(body, bad) {
			t.Errorf("the page holds %q", bad)
		}
	}
	// The board has a header image: the preview and the remove form show.
	if !strings.Contains(body, `action="/admin/settings/header/remove"`) {
		t.Error("no remove form for a board with a header image")
	}
	// The index links the page.
	if idx := bodyOf(t, getAs(t, e.h, "/admin/", e.alice)); !strings.Contains(idx, `href="/admin/settings"`) {
		t.Error("the admin index has no link to the settings")
	}
	// The notices come from a fixed list: the value is never printed.
	page := bodyOf(t, getAs(t, e.h, "/admin/settings?done=%3Cb%3Ex%3C%2Fb%3E&reason=%3Cb%3E", e.alice))
	if strings.Contains(page, "<b>x</b>") || strings.Contains(page, "notice-inline") {
		t.Error("an unknown notice value shows")
	}
	page = bodyOf(t, getAs(t, e.h, "/admin/settings?done=mail-failed&reason=%3Cb%3Ehi", e.alice))
	if !strings.Contains(page, "The test email was not sent. Check") || strings.Contains(page, "&lt;b&gt;") || strings.Contains(page, "<b>hi") {
		t.Errorf("a reason that is not a summary shows:\n%s", page)
	}
	page = bodyOf(t, getAs(t, e.h, "/admin/settings?done=mail-failed&reason=smtp+550+5.1.1", e.alice))
	if !strings.Contains(page, "(smtp 550 5.1.1)") {
		t.Error("a summary does not show")
	}
}

func TestAdminSettingsPageHasNoImageForAnEmptyHeader(t *testing.T) {
	e := newSettingsEnv(t, Options{})
	exec(t, e.pool, `update settings set value = 'null' where key = 'header_image'`)
	body := bodyOf(t, getAs(t, e.h, "/admin/settings", e.alice))
	if strings.Contains(body, `/admin/settings/header/remove`) || !strings.Contains(body, "Upload an image") {
		t.Error("the page offers to remove an image that is not there")
	}
}

// A new name shows on the next page, on the static 500 and 503 pages, in the
// frame of the error pages and in the subject of the next reset email.
func TestAdminSettingsSaveReachesEveryPlace(t *testing.T) {
	e := newSettingsEnv(t, testMailOptions())
	const name = "Fish & <Chips> Forum"
	// With no header image, the header shows the name and the description.
	exec(t, e.pool, `update settings set value = 'null' where key = 'header_image'`)
	before := dumpData(t, e.pool)
	rec := e.save(t, e.alice, withFields(e.form(t), "board_name", "  "+name+"  ", "board_description", "Plain & simple", "timezone", "Asia/Tokyo", "scheme", "slate-deep"))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/settings?done=saved" {
		t.Fatalf("save = %d to %q\n%s", rec.Code, rec.Header().Get("Location"), bodyOf(t, rec))
	}
	if diff := diffTables(before, dumpData(t, e.pool)); len(diff) != 2 || diff[0] != "mod_log" || diff[1] != "settings" {
		t.Errorf("the save changed %v, want mod_log and settings", diff)
	}
	if got := e.stored(t); got.BoardName != name || got.BoardDescription != "Plain & simple" || got.Timezone != "Asia/Tokyo" || got.DefaultScheme != "slate-deep" {
		t.Errorf("stored = %+v", got)
	}

	// The page after the redirect shows the notice, and the new name in the title
	// and the header, escaped.
	page := bodyOf(t, getAs(t, e.h, "/admin/settings?done=saved", e.alice))
	if !strings.Contains(page, "Settings saved.") {
		t.Error("no notice")
	}
	escaped := "Fish &amp; &lt;Chips&gt; Forum"
	next := bodyOf(t, getAs(t, e.h, "/", nil))
	if !strings.Contains(next, "<title>"+escaped) && !strings.Contains(next, escaped+"</title>") {
		t.Errorf("the title of the next page lacks the new name:\n%.600s", next)
	}
	if strings.Contains(next, "<Chips>") || strings.Contains(next, "Example Board") {
		t.Error("the next page holds raw markup or the old name")
	}
	if !strings.Contains(next, "Plain &amp; simple") {
		t.Error("the next page lacks the new description")
	}

	// The frame of the error pages and the static pages follow.
	if f := e.site.frame.Load(); f == nil || f.settings.BoardName != name || f.settings.DefaultScheme != "slate-deep" || f.loc.String() != "Asia/Tokyo" {
		t.Errorf("frame = %+v", f)
	}
	for _, status := range []int{http.StatusInternalServerError, http.StatusServiceUnavailable} {
		w := httptest.NewRecorder()
		e.site.writeStatic(w, status)
		got := w.Body.String()
		if w.Code != status || !strings.Contains(got, escaped) || strings.Contains(got, "Example Board") || !strings.Contains(got, "slate-deep") {
			t.Errorf("static %d page (status %d) lacks the new name or scheme:\n%.800s", status, w.Code, got)
		}
	}
	// The subject of the next reset email holds the new name.
	guest := getAs(t, e.h, "/reset", nil)
	cookies := jar(nil, guest)
	rec, _ = postAs(t, e.h, "/reset", cookies, url.Values{"name": {"carol"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /reset = %d", rec.Code)
	}
	var subject string
	if err := e.pool.QueryRow(t.Context(), `select subject from email_queue order by id desc limit 1`).Scan(&subject); err != nil {
		t.Fatal(err)
	}
	if subject != "Reset your password on "+name {
		t.Errorf("subject = %q", subject)
	}

	// A request that asks for the static page when the error template fails gets
	// the new one, too: break the template and ask for a 503.
	for name := range e.site.pages {
		e.site.pages[name] = brokenTemplate
	}
	w := httptest.NewRecorder()
	e.site.renderFailed(w, http.StatusServiceUnavailable)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), escaped) {
		t.Errorf("the page after a template failure lacks the new name:\n%.600s", w.Body.String())
	}
}

func TestAdminSettingsLogRow(t *testing.T) {
	e := newSettingsEnv(t, Options{})
	rec := e.save(t, e.alice, withFields(e.form(t), "board_name", "Second Name", "scheme", "slate-light"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save = %d", rec.Code)
	}
	var (
		actor           int64
		action, details string
		target          int64
		targetType      string
	)
	if err := e.pool.QueryRow(t.Context(), `select actor_id, action, target_type, target_id, details::text from mod_log order by id desc limit 1`).
		Scan(&actor, &action, &targetType, &target, &details); err != nil {
		t.Fatal(err)
	}
	if actor != 1 || action != "settings.update" || targetType != "settings" || target != 0 ||
		details != `{"via": "web", "keys": ["board_name", "default_color_scheme"]}` {
		t.Errorf("row = %d %s %s %d %s", actor, action, targetType, target, details)
	}
	// The log page shows the row with the keys.
	log := bodyOf(t, getAs(t, e.h, "/admin/log?action=settings.update", e.alice))
	if !strings.Contains(log, "settings.update") || !strings.Contains(log, "board_name") {
		t.Errorf("the log page lacks the row:\n%s", log)
	}
}

func TestAdminSettingsNoChange(t *testing.T) {
	e := newSettingsEnv(t, Options{})
	before := dumpData(t, e.pool)
	rec := e.save(t, e.alice, e.form(t))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/settings?done=nochange" {
		t.Fatalf("a save of the same values = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if diff := diffTables(before, dumpData(t, e.pool)); len(diff) != 0 {
		t.Errorf("a save that changed nothing changed %v", diff)
	}
	if e.logRows(t, "settings.update") != 0 {
		t.Error("a save that changed nothing wrote a row")
	}
	if page := bodyOf(t, getAs(t, e.h, rec.Header().Get("Location"), e.alice)); !strings.Contains(page, "Nothing changed") {
		t.Error("the page does not say that nothing changed")
	}
}

func TestAdminSettingsRefuses(t *testing.T) {
	e := newSettingsEnv(t, Options{})
	before := dumpData(t, e.pool)
	cases := []struct {
		name   string
		form   url.Values
		errors map[string]string // error id -> message
		echo   map[string]string // input name -> value shown
	}{
		{"empty name", withFields(e.form(t), "board_name", " "), map[string]string{"board-name-error": "Enter a name of 1 to 100 characters."}, nil},
		{"long name", withFields(e.form(t), "board_name", strings.Repeat("n", 101)), map[string]string{"board-name-error": "Use at most 100 characters."}, map[string]string{"board_name": strings.Repeat("n", 101)}},
		{"line break in the name", withFields(e.form(t), "board_name", "Two\r\nlines"), map[string]string{"board-name-error": "Use one line of plain text."}, nil},
		{"hidden character in the name", withFields(e.form(t), "board_name", "A\u202eB"), map[string]string{"board-name-error": "Remove the hidden formatting character."}, nil},
		{"long description", withFields(e.form(t), "board_description", strings.Repeat("d", 501)), map[string]string{"board-description-error": "Use at most 500 characters."}, nil},
		{"unknown zone", withFields(e.form(t), "timezone", "Mars/Olympus"), map[string]string{"board-timezone-error": "Pick a time zone from the list."}, nil},
		{"unknown scheme", withFields(e.form(t), "scheme", "neon"), map[string]string{"board-scheme-error": "Pick one of the color schemes."}, nil},
		{"no scheme", func() url.Values { f := e.form(t); f.Del("scheme"); return f }(), map[string]string{"board-scheme-error": "Pick one of the color schemes."}, nil},
		{"two faults", withFields(e.form(t), "board_name", "", "timezone", "x"), map[string]string{
			"board-name-error": "Enter a name of 1 to 100 characters.", "board-timezone-error": "Pick a time zone from the list."}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := withFields(tc.form, "board_description", tc.form.Get("board_description"))
			rec := e.save(t, e.alice, form)
			body := bodyOf(t, rec)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400\n%s", rec.Code, body)
			}
			for id, msg := range tc.errors {
				want := `id="` + id + `" role="alert">` + msg + `</p>`
				if !strings.Contains(body, want) {
					t.Errorf("the page lacks %s", want)
				}
			}
			if n := strings.Count(body, `class="field-error"`); n != len(tc.errors) {
				t.Errorf("the page shows %d messages, want %d", n, len(tc.errors))
			}
			if !strings.Contains(body, `aria-invalid="true"`) {
				t.Error("no field is marked invalid")
			}
			for name, val := range tc.echo {
				if got, _ := inputValue(body, name); got != val {
					t.Errorf("the field %s holds %q, want %q", name, got, val)
				}
			}
			// A zone or a scheme that gobb does not offer never comes back.
			if strings.Contains(body, "Mars/Olympus") || strings.Contains(body, "neon") {
				t.Error("the page echoes a choice that does not exist")
			}
			if diff := diffTables(before, dumpData(t, e.pool)); len(diff) != 0 {
				t.Errorf("a refused save changed %v", diff)
			}
		})
	}
	// The values that were sent come back, not the stored ones.
	rec := e.save(t, e.alice, withFields(e.form(t), "board_name", "Kept <text>", "board_description", "Kept & text", "timezone", "Asia/Tokyo", "scheme", "slate-light", "board_description", strings.Repeat("d", 501)))
	body := bodyOf(t, rec)
	if v, _ := inputValue(body, "board_name"); v != "Kept <text>" {
		t.Errorf("name field = %q", v)
	}
	if !strings.Contains(body, `<option value="Asia/Tokyo" selected>`) || !strings.Contains(body, `id="scheme-slate-light" value="slate-light" checked`) {
		t.Error("the choices that were sent do not come back")
	}
}

func TestAdminSettingsKeepsAnOddZone(t *testing.T) {
	e := newSettingsEnv(t, Options{})
	exec(t, e.pool, `update settings set value = '"Etc/GMT+5"' where key = 'timezone'`)
	body := bodyOf(t, getAs(t, e.h, "/admin/settings", e.alice))
	if !strings.Contains(body, `<option value="Etc/GMT&#43;5" selected>Etc/GMT&#43;5 (current setting)</option>`) {
		t.Errorf("the odd zone is not offered as the current one")
	}
	rec := e.save(t, e.alice, withFields(e.form(t), "board_name", "Odd Zone Board", "timezone", "Etc/GMT+5"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save = %d", rec.Code)
	}
	if got := e.stored(t); got.BoardName != "Odd Zone Board" || got.Timezone != "Etc/GMT+5" {
		t.Errorf("stored = %+v", got)
	}
}

func TestAdminSettingsNeedAnAdmin(t *testing.T) {
	e := newSettingsEnv(t, Options{})
	for _, who := range []string{"", "bob", "carol"} {
		var cookies []*http.Cookie
		if who != "" {
			cookies = signIn(t, e.h, who, testPassword)
		}
		before := dumpData(t, e.pool)
		for _, target := range []string{"/admin/settings", "/admin/settings/header/remove"} {
			if rec := getAs(t, e.h, target, cookies); rec.Code != http.StatusNotFound && target == "/admin/settings" {
				t.Errorf("%q: GET %s = %d, want 404", who, target, rec.Code)
			}
			rec, _ := postAs(t, e.h, target, cookies, withFields(e.form(t), "board_name", "Taken"))
			if rec.Code != http.StatusNotFound {
				t.Errorf("%q: POST %s = %d, want 404", who, target, rec.Code)
			}
		}
		// The upload: a guest or a member without a session gets 403 from the
		// upload guard, any signed-in member gets 404 from the admin gate.
		rec := e.uploadWithToken(t, cookies, "x", "header", testPNG(t, 10, 10), nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%q: POST header = %d, want 404", who, rec.Code)
		}
		if diff := diffTables(before, dumpData(t, e.pool)); len(diff) != 0 {
			t.Errorf("%q changed %v", who, diff)
		}
	}
}

func TestAdminHeaderUpload(t *testing.T) {
	e := newSettingsEnv(t, Options{})
	was := e.stored(t).HeaderImage
	if _, err := os.Stat(filepath.Join(e.dir, was)); err != nil {
		t.Fatalf("setup: the imported header image is not on disk: %v", err)
	}
	cases := []struct {
		name string
		data []byte
		ext  string
		mime string
	}{
		{"png", testPNG(t, 40, 12), "png", "image/png"},
		{"jpeg", jpegBytes(t, 40, 12), "jpg", "image/jpeg"},
		{"webp", webpBytes(t), "png", "image/png"},
	}
	prev := was
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := e.logRows(t, "settings.update")
			rec := e.uploadHeader(t, e.alice, tc.data, nil)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/settings?done=header" {
				t.Fatalf("upload = %d to %q\n%s", rec.Code, rec.Header().Get("Location"), bodyOf(t, rec))
			}
			got := e.stored(t).HeaderImage
			if !regexp.MustCompile(`^header/h-[0-9a-f]{16}\.` + tc.ext + `$`).MatchString(got) {
				t.Fatalf("header = %q", got)
			}
			// The file is a new image of the format of its name, and the old one
			// is gone.
			onDisk, err := os.ReadFile(filepath.Join(e.dir, got))
			if err != nil || http.DetectContentType(onDisk) != tc.mime {
				t.Errorf("the stored file is not a new %s (%v)", tc.mime, err)
			}
			if _, err := os.Stat(filepath.Join(e.dir, prev)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the replaced file %s is still there (%v)", prev, err)
			}
			if fi, err := os.Stat(filepath.Join(e.dir, got)); err != nil || fi.Mode().Perm() != 0o644 {
				t.Errorf("mode of the file: %v %v", fi, err)
			}
			prev = got
			if n := e.logRows(t, "settings.update"); n != rows+1 {
				t.Errorf("log rows = %d, want %d", n, rows+1)
			}
			// The next page shows it, and /files serves it as an image.
			page := bodyOf(t, getAs(t, e.h, "/", nil))
			if !strings.Contains(page, `src="/files/`+got+`"`) {
				t.Errorf("the next page lacks the image")
			}
			img := get(t, e.h, "/files/"+got)
			if img.Code != http.StatusOK || img.Header().Get("Content-Type") != tc.mime {
				t.Errorf("GET /files/%s = %d %s", got, img.Code, img.Header().Get("Content-Type"))
			}
			// The static pages show it, too.
			w := httptest.NewRecorder()
			e.site.writeStatic(w, http.StatusServiceUnavailable)
			if !strings.Contains(w.Body.String(), `/files/`+got) {
				t.Error("the static 503 page lacks the new header image")
			}
		})
	}
	if files := e.headerFiles(t); len(files) != 1 {
		t.Errorf("header folder holds %v, want the last upload only", files)
	}
	var details string
	if err := e.pool.QueryRow(t.Context(), `select details::text from mod_log where action = 'settings.update' order by id desc limit 1`).Scan(&details); err != nil || details != `{"via": "web", "keys": ["header_image"]}` {
		t.Errorf("details = %q (%v)", details, err)
	}
}

func TestAdminHeaderUploadRefuses(t *testing.T) {
	e := newSettingsEnv(t, Options{})
	was := e.stored(t).HeaderImage
	filesBefore := e.headerFiles(t)
	before := dumpData(t, e.pool)
	big := append(testPNG(t, 20, 20), make([]byte, 1<<20)...)
	cases := []struct {
		name   string
		data   []byte
		status int
		msg    string
	}{
		{"a text file named like an image", []byte("<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>"), 400, msgHeaderFormat},
		{"html", []byte("<!doctype html><title>x</title>"), 400, msgHeaderFormat},
		{"an empty file", []byte{}, 400, msgHeaderFormat},
		{"a png cut short", testPNG(t, 30, 30)[:60], 400, msgHeaderFormat},
		{"a gif", []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"), 400, msgHeaderFormat},
		{"a file just over 1 MB", big, 413, msgFileTooLarge},
		{"a png of 9,000 pixels wide", pngHeaderOnly(9000, 10), 400, msgAvatarPixels},
		{"a png of 17,000,000 pixels", pngHeaderOnly(5000, 3400), 400, msgAvatarPixels},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.uploadHeader(t, e.alice, tc.data, nil)
			body := bodyOf(t, rec)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d\n%.300s", rec.Code, tc.status, body)
			}
			if !strings.Contains(body, tc.msg) {
				t.Errorf("the answer lacks %q:\n%.400s", tc.msg, body)
			}
		})
	}
	// No file in the form, and a file in another field.
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"no file":       e.uploadHeader(t, e.alice, nil, nil),
		"wrong field":   e.uploadWithToken(t, e.alice, formToken(t, bodyOf(t, getAs(t, e.h, "/admin/settings", e.alice))), "avatar", testPNG(t, 10, 10), nil),
		"a huge upload": e.uploadHeader(t, e.alice, bytes.Repeat([]byte("x"), 3<<20), nil),
	} {
		want := http.StatusBadRequest
		if name == "a huge upload" {
			want = http.StatusRequestEntityTooLarge
		}
		if rec.Code != want {
			t.Errorf("%s: status %d, want %d", name, rec.Code, want)
		}
	}
	if got := e.stored(t).HeaderImage; got != was {
		t.Errorf("header = %q after the refusals", got)
	}
	if files := e.headerFiles(t); strings.Join(files, ",") != strings.Join(filesBefore, ",") {
		t.Errorf("header folder holds %v, was %v", files, filesBefore)
	}
	if diff := diffTables(before, dumpData(t, e.pool)); len(diff) != 0 {
		t.Errorf("a refused upload changed %v", diff)
	}
}

// pngHeaderOnly is the signature and the IHDR chunk of a PNG of w x h: enough
// for the checks of the header, with no pixel data.
func pngHeaderOnly(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	ihdr[0], ihdr[1], ihdr[2], ihdr[3] = byte(w>>24), byte(w>>16), byte(w>>8), byte(w)
	ihdr[4], ihdr[5], ihdr[6], ihdr[7] = byte(h>>24), byte(h>>16), byte(h>>8), byte(h)
	ihdr[8], ihdr[9] = 8, 2
	b.Write([]byte{0, 0, 0, 13})
	chunk := append([]byte("IHDR"), ihdr...)
	b.Write(chunk)
	sum := crc32.ChecksumIEEE(chunk)
	b.Write([]byte{byte(sum >> 24), byte(sum >> 16), byte(sum >> 8), byte(sum)})
	return b.Bytes()
}

func TestAdminHeaderUploadMemory(t *testing.T) {
	// A gate too small for the decode refuses an image that is in the limits, and
	// a busy gate gives 503; neither changes anything.
	gate := login.NewGate(2048)
	e := newSettingsEnv(t, Options{LoginGate: gate, LoginGateWait: 20 * time.Millisecond})
	was := e.stored(t).HeaderImage
	big := pngHeaderOnly(3000, 3000)
	rec := e.uploadHeader(t, e.alice, big, nil)
	if body := bodyOf(t, rec); rec.Code != http.StatusBadRequest || !strings.Contains(body, msgAvatarMemory) {
		t.Errorf("too much memory = %d\n%.300s", rec.Code, body)
	}
	hold, err := gate.Acquire(t.Context(), 2048, 0)
	if err != nil {
		t.Fatal(err)
	}
	rec = e.uploadHeader(t, e.alice, testPNG(t, 8, 8), nil)
	hold()
	if body := bodyOf(t, rec); rec.Code != http.StatusServiceUnavailable || !strings.Contains(body, msgBusy) || rec.Header().Get("Retry-After") == "" {
		t.Errorf("busy gate = %d\n%.300s", rec.Code, body)
	}
	if e.stored(t).HeaderImage != was {
		t.Error("a refused upload changed the header image")
	}
	// The gate is free again.
	if rel, err := gate.Acquire(t.Context(), 2048, 0); err != nil {
		t.Errorf("the gate is not free after the uploads: %v", err)
	} else {
		rel()
	}
}

// One upload in flight for each admin: a second gets 503 at once, and the first
// one's slot is free again after it ends.
func TestAdminHeaderUploadSlots(t *testing.T) {
	e := newSettingsEnv(t, Options{})
	e.site.uploading.Store(int64(1), struct{}{})
	rec := e.uploadHeader(t, e.alice, testPNG(t, 8, 8), nil)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Errorf("a second upload = %d", rec.Code)
	}
	e.site.uploading.Delete(int64(1))
	// The slots of the server: fill them.
	for range cap(e.site.uploadSlots) {
		e.site.uploadSlots <- struct{}{}
	}
	if rec := e.uploadHeader(t, e.alice, testPNG(t, 8, 8), nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("an upload with the server full = %d", rec.Code)
	}
	for range cap(e.site.uploadSlots) {
		<-e.site.uploadSlots
	}
	if rec := e.uploadHeader(t, e.alice, testPNG(t, 8, 8), nil); rec.Code != http.StatusSeeOther {
		t.Errorf("an upload after the slots are free = %d", rec.Code)
	}
}

func TestAdminHeaderUploadNeedsToken(t *testing.T) {
	e := newSettingsEnv(t, Options{})
	was := e.stored(t).HeaderImage
	before := dumpData(t, e.pool)
	for _, token := range []string{"", "nope"} {
		if rec := e.uploadWithToken(t, e.alice, token, "header", testPNG(t, 8, 8), nil); rec.Code != http.StatusForbidden {
			t.Errorf("token %q: status %d, want 403", token, rec.Code)
		}
	}
	good := formToken(t, bodyOf(t, getAs(t, e.h, "/admin/settings", e.alice)))
	if rec := e.uploadWithToken(t, e.alice, good, "header", testPNG(t, 8, 8), map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site: status %d, want 403", rec.Code)
	}
	if e.stored(t).HeaderImage != was || len(diffTables(before, dumpData(t, e.pool))) != 0 {
		t.Error("a refused upload changed something")
	}
}

// The upload counts against the write limit of the admin.
func TestAdminHeaderUploadWriteLimit(t *testing.T) {
	e := newSettingsEnv(t, Options{AdminWrites: 2})
	for i := range 2 {
		if rec := e.uploadHeader(t, e.alice, testPNG(t, 8+i, 8), nil); rec.Code != http.StatusSeeOther {
			t.Fatalf("upload %d = %d", i+1, rec.Code)
		}
	}
	before := dumpData(t, e.pool)
	rec := e.uploadHeader(t, e.alice, testPNG(t, 12, 8), nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("upload 3 = %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if diff := diffTables(before, dumpData(t, e.pool)); len(diff) != 0 {
		t.Errorf("a refused upload changed %v", diff)
	}
}

// A write that fails after the file is on disk removes the new file and keeps
// the current image.
func TestAdminHeaderUploadWriteFails(t *testing.T) {
	e := newSettingsEnv(t, Options{})
	was := e.stored(t).HeaderImage
	filesBefore := e.headerFiles(t)
	// The setting row goes between the check and the write, so the write fails.
	beforeSetHeader = func() { exec(t, e.pool, `delete from settings where key = 'header_image'`) }
	t.Cleanup(func() { beforeSetHeader = nil })
	rec := e.uploadHeader(t, e.alice, testPNG(t, 8, 8), nil)
	beforeSetHeader = nil
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", rec.Code)
	}
	exec(t, e.pool, `insert into settings (key, value) values ('header_image', to_jsonb($$`+was+`$$::text))`)
	if got := e.stored(t).HeaderImage; got != was {
		t.Errorf("header = %q, want the old one", got)
	}
	if files := e.headerFiles(t); strings.Join(files, ",") != strings.Join(filesBefore, ",") {
		t.Errorf("header folder holds %v, was %v: the new file stays", files, filesBefore)
	}
	if e.logRows(t, "settings.update") != 0 {
		t.Error("a failed write left a row")
	}
}

func TestAdminHeaderRemove(t *testing.T) {
	e := newSettingsEnv(t, Options{})
	was := e.stored(t).HeaderImage
	rec, _ := postAs(t, e.h, "/admin/settings/header/remove", e.alice, url.Values{})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/settings?done=header-removed" {
		t.Fatalf("remove = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if got := e.stored(t).HeaderImage; got != "" {
		t.Errorf("header = %q", got)
	}
	if _, err := os.Stat(filepath.Join(e.dir, was)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the file stays: %v", err)
	}
	if e.logRows(t, "settings.update") != 1 {
		t.Error("no log row for the removal")
	}
	page := bodyOf(t, getAs(t, e.h, "/", nil))
	if strings.Contains(page, `class="header-image"`) {
		t.Error("the next page still shows an image")
	}
	w := httptest.NewRecorder()
	e.site.writeStatic(w, http.StatusInternalServerError)
	if strings.Contains(w.Body.String(), "/files/header/") {
		t.Error("the static page still shows the image")
	}
	// A second removal changes nothing and writes no row.
	before := dumpData(t, e.pool)
	rec, _ = postAs(t, e.h, "/admin/settings/header/remove", e.alice, url.Values{})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/settings?done=header-none" {
		t.Errorf("second remove = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if diff := diffTables(before, dumpData(t, e.pool)); len(diff) != 0 {
		t.Errorf("a removal that changed nothing changed %v", diff)
	}
}

// A stored path that gobb does not delete stays on disk when the image changes.
func TestAdminHeaderKeepsAFileItMayNotDelete(t *testing.T) {
	e := newSettingsEnv(t, Options{})
	odd := "header/a b.png"
	if err := os.WriteFile(filepath.Join(e.dir, odd), testPNG(t, 4, 4), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file outside the folder, named by a path with "..".
	outside := filepath.Join(e.dir, "keep.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, stored := range []string{odd, "header/../keep.txt"} {
		exec(t, e.pool, `update settings set value = '"`+stored+`"' where key = 'header_image'`)
		if rec := e.uploadHeader(t, e.alice, testPNG(t, 8, 8), nil); rec.Code != http.StatusSeeOther {
			t.Fatalf("upload over %q = %d", stored, rec.Code)
		}
	}
	for _, p := range []string{odd, "keep.txt"} {
		if _, err := os.Stat(filepath.Join(e.dir, p)); err != nil {
			t.Errorf("%s was deleted: %v", p, err)
		}
	}
}

// Two saves at once, and pages that read the static error pages meanwhile: no
// race, and the pages end with the newest name.
func TestAdminSettingsConcurrent(t *testing.T) {
	e := newSettingsEnv(t, Options{AdminWrites: 1000})
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				w := httptest.NewRecorder()
				e.site.writeStatic(w, http.StatusServiceUnavailable)
				if !strings.Contains(w.Body.String(), "<html") {
					t.Error("a static page is empty")
					return
				}
			}
		}()
	}
	var writers sync.WaitGroup
	for i := range 6 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			name := "Concurrent " + strings.Repeat("x", i+1)
			if rec := e.save(t, e.alice, withFields(e.form(t), "board_name", name)); rec.Code != http.StatusSeeOther {
				t.Errorf("save %d = %d", i, rec.Code)
			}
		}()
	}
	writers.Wait()
	close(stop)
	readers.Wait()
	final := e.stored(t).BoardName
	w := httptest.NewRecorder()
	e.site.writeStatic(w, http.StatusServiceUnavailable)
	if !strings.Contains(w.Body.String(), final) {
		t.Errorf("the static page does not hold the name in the database, %q", final)
	}
	if f := e.site.frame.Load(); f.settings.BoardName != final {
		t.Errorf("the frame holds %q, want %q", f.settings.BoardName, final)
	}
}

func TestAdminMailTest(t *testing.T) {
	sender := &fakeSender{}
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	opt := testMailOptions()
	opt.Mail.Sender = sender
	opt.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	e := newSettingsEnv(t, opt)

	page := bodyOf(t, getAs(t, e.h, "/admin/settings", e.alice))
	if !strings.Contains(page, `action="/admin/settings/mail-test"`) || !strings.Contains(page, "Send a test email") {
		t.Fatal("no test button with email on")
	}
	before := dumpData(t, e.pool)
	rec, _ := postAs(t, e.h, "/admin/settings/mail-test", e.alice, url.Values{})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/settings?done=mail-sent" {
		t.Fatalf("test = %d to %q\n%s", rec.Code, rec.Header().Get("Location"), bodyOf(t, rec))
	}
	msgs := sender.messages()
	if len(msgs) != 1 || msgs[0].To != "alice@example.com" || msgs[0].Subject != "Test email from Example Board" ||
		!strings.Contains(msgs[0].Body, "Email works.") {
		t.Fatalf("sent %+v", msgs)
	}
	if diff := diffTables(before, dumpData(t, e.pool)); len(diff) != 0 {
		t.Errorf("a test email changed %v", diff)
	}
	if body := bodyOf(t, getAs(t, e.h, rec.Header().Get("Location"), e.alice)); !strings.Contains(body, "Test email sent") || strings.Contains(body, "alice@example.com") {
		t.Errorf("the notice is wrong or shows the address:\n%s", body)
	}

	// One a minute for each admin: the second is 429 with Retry-After, sends
	// nothing, and shows the message beside the button.
	rec, _ = postAs(t, e.h, "/admin/settings/mail-test", e.alice, url.Values{})
	if body := bodyOf(t, rec); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || !strings.Contains(body, msgMailWait) {
		t.Errorf("second test = %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if len(sender.messages()) != 1 {
		t.Error("a limited test sent a message")
	}
	// Another admin has a limit of their own.
	exec(t, e.pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (2, 0, 'admin', 3)`)
	bob := signIn(t, e.h, "bob", testPassword)
	if rec, _ := postAs(t, e.h, "/admin/settings/mail-test", bob, url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("bob's test = %d", rec.Code)
	}
	if msgs := sender.messages(); len(msgs) != 2 || msgs[1].To != "bob@example.com" {
		t.Errorf("sent %+v", msgs)
	}
	// A minute later, alice may send again.
	mu.Lock()
	now = now.Add(time.Minute + time.Second)
	mu.Unlock()
	if rec, _ := postAs(t, e.h, "/admin/settings/mail-test", e.alice, url.Values{}); rec.Code != http.StatusSeeOther {
		t.Errorf("a test after a minute = %d", rec.Code)
	}
}

func TestAdminMailTestFailure(t *testing.T) {
	sender := &fakeSender{err: &mail.Error{Stage: "RCPT", Code: 550, Enhanced: "5.1.1"}}
	opt := testMailOptions()
	opt.Mail.Sender = sender
	e := newSettingsEnv(t, opt)
	rec, _ := postAs(t, e.h, "/admin/settings/mail-test", e.alice, url.Values{})
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || loc != "/admin/settings?done=mail-failed&reason=smtp+550+5.1.1" {
		t.Fatalf("a failed test = %d to %q", rec.Code, loc)
	}
	body := bodyOf(t, getAs(t, e.h, loc, e.alice))
	if !strings.Contains(body, "The test email was not sent (smtp 550 5.1.1).") || !strings.Contains(body, `class="form-error"`) {
		t.Errorf("the page lacks the failure:\n%s", body)
	}
	if strings.Contains(body, "alice@example.com") {
		t.Error("the failure shows the address")
	}
}

func TestAdminMailTestAddress(t *testing.T) {
	sender := &fakeSender{}
	opt := testMailOptions()
	opt.Mail.Sender = sender
	e := newSettingsEnv(t, opt)
	exec(t, e.pool, `update users set email = 'two words' where id = 1`)
	rec, _ := postAs(t, e.h, "/admin/settings/mail-test", e.alice, url.Values{})
	if body := bodyOf(t, rec); rec.Code != http.StatusBadRequest || !strings.Contains(body, msgMailAddress) {
		t.Errorf("a bad address = %d", rec.Code)
	}
	if len(sender.messages()) != 0 {
		t.Error("a message went to a bad address")
	}
}

// With email off, or with no sender, the page has no button and the route is off.
func TestAdminMailTestOffWithoutMail(t *testing.T) {
	noSender := testMailOptions()
	noSender.Mail.Sender = nil
	for name, opt := range map[string]Options{"email off": {}, "no sender": noSender} {
		t.Run(name, func(t *testing.T) {
			e := newSettingsEnv(t, opt)
			body := bodyOf(t, getAs(t, e.h, "/admin/settings", e.alice))
			if strings.Contains(body, "mail-test") || strings.Contains(body, "Send a test email") {
				t.Error("the page offers the test")
			}
			rec, _ := postAs(t, e.h, "/admin/settings/mail-test", e.alice, url.Values{})
			if rec.Code != http.StatusNotFound {
				t.Errorf("POST mail-test = %d, want 404", rec.Code)
			}
		})
	}
}

// Under a base path, the forms and the redirects keep the prefix.
func TestAdminSettingsUnderBasePath(t *testing.T) {
	e := newSettingsEnv(t, Options{BasePath: "/forum/"})
	e.alice = signInAt(t, e.h, "/forum/", "alice")
	body := bodyOf(t, getAs(t, e.h, "/forum/admin/settings", e.alice))
	for _, want := range []string{`action="/forum/admin/settings"`, `action="/forum/admin/settings/header"`, `action="/forum/admin/settings/header/remove"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	form := withFields(e.form(t), "board_name", "Prefixed")
	form.Set("csrf", formToken(t, body))
	rec := request(t, e.h, http.MethodPost, "/forum/admin/settings", e.alice, form)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/forum/admin/settings?done=saved" {
		t.Errorf("save = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSettingsHelpers(t *testing.T) {
	// inputErrors reads one error, a joined error, and nothing else.
	a, b := &board.InputError{Field: "name"}, &board.InputError{Field: "scheme"}
	if got := inputErrors(a); len(got) != 1 || got[0] != a {
		t.Errorf("one: %v", got)
	}
	if got := inputErrors(errors.Join(a, b)); len(got) != 2 || got[0] != a || got[1] != b {
		t.Errorf("joined: %v", got)
	}
	if got := inputErrors(errors.New("x")); len(got) != 0 {
		t.Errorf("other: %v", got)
	}
	if got := inputErrors(nil); len(got) != 0 {
		t.Errorf("nil: %v", got)
	}
	// The reason of a failed send passes only when it is a summary.
	for reason, ok := range map[string]bool{"smtp 550 5.1.1": true, "smtp 421": true, "timeout": true, "tls": true, "no starttls": true, "network": true,
		"": false, "smtp 550 x": false, "timeout<b>": false, "TIMEOUT": false, "smtp 5500": false, "network\n": false} {
		if mailReasonRE.MatchString(reason) != ok {
			t.Errorf("mailReasonRE(%q) = %v, want %v", reason, !ok, ok)
		}
	}
}

// TestAdminMailTestNeedsVerified: the test goes only to a verified address, since
// a changed address is not verified until a link proves it.
func TestAdminMailTestNeedsVerified(t *testing.T) {
	sender := &fakeSender{}
	opt := testMailOptions()
	opt.Mail.Sender = sender
	e := newSettingsEnv(t, opt)
	if _, err := e.pool.Exec(t.Context(), `update users set email_verified = false where id = 1`); err != nil {
		t.Fatal(err)
	}
	rec, _ := postAs(t, e.h, "/admin/settings/mail-test", e.alice, url.Values{})
	if rec.Code != http.StatusConflict || !strings.Contains(bodyOf(t, rec), "not verified") {
		t.Errorf("a test to an unverified address = %d", rec.Code)
	}
	if n := len(sender.messages()); n != 0 {
		t.Errorf("sent %d messages", n)
	}
}
