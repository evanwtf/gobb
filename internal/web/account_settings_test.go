package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/login"
)

// The tests of the settings on the account page: the forms as a browser without
// script posts them, the same routes as gobb.js uses them (with X-Gobb-Save: 1),
// and the avatar upload.

// saveHeader marks a request from gobb.js. The answer is then 204 or JSON.
var saveHeader = map[string]string{"X-Gobb-Save": "1"}

// postingSiteWithData is siteWithPool on the posting board, with the data
// directory too.
func postingSiteWithData(t *testing.T) (http.Handler, *pgxpool.Pool, string) {
	t.Helper()
	return postingSiteOpts(t, Options{})
}

// postingSiteOpts is postingSiteWithData with options; the data directory is set.
func postingSiteOpts(t *testing.T, opt Options) (http.Handler, *pgxpool.Pool, string) {
	t.Helper()
	pool, dataDir := boardtest.Load(t, postingBoard())
	opt.DataDir, opt.LoginFloor = dataDir, time.Millisecond
	h, err := New(t.Context(), pool, opt)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	return h, pool, dataDir
}

// accountToken returns the csrf token that the account page gives to cookies.
func accountToken(t *testing.T, h http.Handler, cookies []*http.Cookie) string {
	t.Helper()
	rec := getAs(t, h, "/account", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /account = %d", rec.Code)
	}
	return formToken(t, bodyOf(t, rec))
}

// postWith sends a form with a good token, extra headers and the cookies of a
// visitor. The token comes from /account.
func postWith(t *testing.T, h http.Handler, target string, cookies []*http.Cookie, form url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	form = cloneForm(form)
	if form.Get("csrf") == "" {
		form.Set("csrf", accountToken(t, h, cookies))
	}
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// multipartBody builds the body of an avatar upload: the csrf field (when token
// is not empty) and a file in the field "avatar".
func multipartBody(t *testing.T, token, filename string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if token != "" {
		if err := mw.WriteField("csrf", token); err != nil {
			t.Fatal(err)
		}
	}
	fw, err := mw.CreateFormFile("avatar", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

// upload posts a file to /account/avatar with a good token.
func upload(t *testing.T, h http.Handler, cookies []*http.Cookie, data []byte, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return uploadWithToken(t, h, cookies, accountToken(t, h, cookies), data, hdr)
}

func uploadWithToken(t *testing.T, h http.Handler, cookies []*http.Cookie, token string, data []byte, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	body, ctype := multipartBody(t, token, "me.png", data)
	r := httptest.NewRequest(http.MethodPost, "/account/avatar", body)
	r.Header.Set("Content-Type", ctype)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// testPNG returns a PNG of w by hh pixels.
func testPNG(t *testing.T, w, hh int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, hh))
	for y := range hh {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// answerJSON decodes a JSON answer into a map, and checks the headers that every
// JSON answer carries.
func answerJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("the answer is not a JSON object of strings: %v\n%s", err, rec.Body.String())
	}
	return out
}

var avatarName = regexp.MustCompile(`^avatars/u1-[0-9a-f]{16}\.png$`)

// avatarOf reads the stored avatar path of user id: "" for none.
func avatarOf(t *testing.T, pool *pgxpool.Pool, id int) string {
	t.Helper()
	var p *string
	if err := pool.QueryRow(t.Context(), `select avatar_path from users where id = $1`, id).Scan(&p); err != nil {
		t.Fatal(err)
	}
	if p == nil {
		return ""
	}
	return *p
}

// colOrEmpty reads one column of one user as text; NULL is "".
func colOrEmpty(t *testing.T, pool *pgxpool.Pool, id int, col string) string {
	t.Helper()
	var v string
	if err := pool.QueryRow(t.Context(), `select coalesce(`+col+`::text, '') from users where id = $1`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestAccountSettingsNoScript(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", testPassword)

	for _, tc := range []struct {
		name, target, done string
		form               url.Values
		col, want          string
	}{
		{"time zone", "/account/timezone", "timezone", url.Values{"timezone": {"Europe/London"}}, "timezone", "Europe/London"},
		{"date format", "/account/dateformat", "dateformat", url.Values{"dateformat": {"iso"}}, "date_format", "iso"},
		{"about you", "/account/about", "about", url.Values{
			"location": {"Lake Placid, NY"}, "occupation": {"Ski instructor"}, "interests": {"Old radios"},
			"website": {"https://example.com/sample"}, "bday": {"14"}, "bmonth": {"3"}, "byear": {"1984"},
		}, "location", "Lake Placid, NY"},
	} {
		rec := postWith(t, h, tc.target, a, tc.form, nil)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/account?done="+tc.done {
			t.Errorf("%s: %d %q, want 303 /account?done=%s\n%s", tc.name, rec.Code, rec.Header().Get("Location"), tc.done, bodyOf(t, rec))
		}
		if got := userCol(t, pool, 1, tc.col); got != tc.want {
			t.Errorf("%s: %s = %q, want %q", tc.name, tc.col, got, tc.want)
		}
	}
	for col, want := range map[string]string{"occupation": "Ski instructor", "interests": "Old radios",
		"website": "https://example.com/sample", "birth_day": "14", "birth_month": "3", "birth_year": "1984"} {
		if got := userCol(t, pool, 1, col); got != want {
			t.Errorf("%s = %q, want %q", col, got, want)
		}
	}
	// The page shows the stored values.
	page := bodyOf(t, getAs(t, h, "/account?done=about", a))
	if got, _ := inputValue(page, "location"); got != "Lake Placid, NY" {
		t.Errorf("the location box holds %q", got)
	}
	if !strings.Contains(page, "Your changes are saved.") {
		t.Error("the page lacks the notice of the About you save")
	}

	// Remove the avatar: alice has avatars/1.png from the fixture.
	if avatarOf(t, pool, 1) == "" {
		t.Fatal("the fixture gives alice no avatar")
	}
	rec := postWith(t, h, "/account/avatar/remove", a, url.Values{}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/account?done=avatar-removed" {
		t.Errorf("remove avatar: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if avatarOf(t, pool, 1) != "" {
		t.Error("the avatar path is still stored")
	}

	// A bad website: 400 with the page, the sent value in its box, and the message
	// beside it. Nothing is stored.
	rec = postWith(t, h, "/account/about", a, url.Values{"website": {"ftp://example.com/x"}, "location": {"Nowhere"}}, nil)
	page = bodyOf(t, rec)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad website = %d, want 400", rec.Code)
	}
	if got, _ := inputValue(page, "website"); got != "ftp://example.com/x" {
		t.Errorf("the website box holds %q, want the sent value", got)
	}
	if !strings.Contains(page, "Enter an address that starts with https:// or http://.") {
		t.Error("the page lacks the message beside the website box")
	}
	if got := userCol(t, pool, 1, "location"); got != "Lake Placid, NY" {
		t.Errorf("a refused save stored a location: %q", got)
	}
	// A bad birthday gives 400 beside the birthday, and the lists keep the sent values.
	rec = postWith(t, h, "/account/about", a, url.Values{"bday": {"31"}, "bmonth": {"2"}, "byear": {""}}, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(bodyOf(t, rec), "February has no day 31.") {
		t.Errorf("bad birthday = %d", rec.Code)
	}
	// A bad zone and a bad format.
	if rec := postWith(t, h, "/account/timezone", a, url.Values{"timezone": {"UTC"}}, nil); rec.Code != http.StatusBadRequest ||
		!strings.Contains(bodyOf(t, rec), "Pick a time zone from the list.") {
		t.Errorf("bad zone = %d", rec.Code)
	}
	if rec := postWith(t, h, "/account/dateformat", a, url.Values{"dateformat": {"fancy"}}, nil); rec.Code != http.StatusBadRequest ||
		!strings.Contains(bodyOf(t, rec), "Pick one of the three formats.") {
		t.Errorf("bad format = %d", rec.Code)
	}
}

func TestAutoSaveAnswers(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", testPassword)

	rec := postWith(t, h, "/account/timezone", a, url.Values{"timezone": {"Asia/Tokyo"}}, saveHeader)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Errorf("good zone = %d with %d bytes, want 204 and no body", rec.Code, rec.Body.Len())
	}
	if got := userCol(t, pool, 1, "timezone"); got != "Asia/Tokyo" {
		t.Errorf("timezone = %q", got)
	}
	rec = postWith(t, h, "/account/dateformat", a, url.Values{"dateformat": {"relative"}}, saveHeader)
	if rec.Code != http.StatusNoContent {
		t.Errorf("good format = %d, want 204", rec.Code)
	}
	rec = postWith(t, h, "/account/about", a, url.Values{"website": {"example.com"}}, saveHeader)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad website = %d, want 400", rec.Code)
	}
	got := answerJSON(t, rec)
	if got["field"] != "website" || got["message"] != "Enter an address that starts with https:// or http://." || len(got) != 2 {
		t.Errorf("bad website answer = %v", got)
	}
	// A birthday refusal names the field "birthday".
	rec = postWith(t, h, "/account/about", a, url.Values{"bday": {"30"}, "bmonth": {"2"}, "byear": {"1990"}}, saveHeader)
	if got := answerJSON(t, rec); rec.Code != http.StatusBadRequest || got["field"] != "birthday" {
		t.Errorf("bad birthday = %d %v", rec.Code, got)
	}
	// A zone that is not listed: 400 on "timezone".
	rec = postWith(t, h, "/account/timezone", a, url.Values{"timezone": {"UTC"}}, saveHeader)
	if got := answerJSON(t, rec); rec.Code != http.StatusBadRequest || got["field"] != "timezone" {
		t.Errorf("bad zone = %d %v", rec.Code, got)
	}
	if got := userCol(t, pool, 1, "timezone"); got != "Asia/Tokyo" {
		t.Errorf("a refused save changed the zone to %q", got)
	}

	// A guest gets 403 JSON, with a guest token and with none.
	for _, target := range []string{"/account/timezone", "/account/dateformat", "/account/about", "/account/avatar/remove", "/account/signature"} {
		page := getAs(t, h, "/", nil)
		guest := jar(nil, page)
		rec := postWith(t, h, target, guest, url.Values{"csrf": {formToken(t, bodyOf(t, page))}, "timezone": {"Asia/Tokyo"}}, saveHeader)
		if rec.Code != http.StatusForbidden {
			t.Errorf("guest POST %s = %d, want 403", target, rec.Code)
			continue
		}
		if msg := answerJSON(t, rec)["message"]; msg == "" {
			t.Errorf("guest POST %s: the JSON answer has no message", target)
		}
	}
	// The signature answers the same way as the other fields.
	rec = postWith(t, h, "/account/signature", a, url.Values{"signature": {"Out on the trails."}}, saveHeader)
	if rec.Code != http.StatusNoContent {
		t.Errorf("signature = %d, want 204\n%s", rec.Code, rec.Body.String())
	}
	rec = postWith(t, h, "/account/signature", a, url.Values{"signature": {strings.Repeat("x", 2001)}}, saveHeader)
	if got := answerJSON(t, rec); rec.Code != http.StatusBadRequest || got["field"] != "signature" || got["message"] == "" {
		t.Errorf("long signature = %d %v", rec.Code, got)
	}
}

// guardPost answers its own refusals as JSON for a request from gobb.js, with the
// status codes of the page.
func TestAutoSaveGuardAnswersJSON(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", testPassword)
	token := accountToken(t, h, a)
	form := url.Values{"timezone": {"Asia/Tokyo"}, "csrf": {token}}

	send := func(body string, ctype string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/account/timezone", strings.NewReader(body))
		r.Header.Set("Content-Type", ctype)
		r.Header.Set("X-Gobb-Save", "1")
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		for _, c := range a {
			r.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	const ct = "application/x-www-form-urlencoded"
	for _, tc := range []struct {
		name string
		rec  *httptest.ResponseRecorder
		want int
	}{
		{"no token", send("timezone=Asia%2FTokyo", ct, nil), http.StatusForbidden},
		{"wrong token", send("timezone=Asia%2FTokyo&csrf=nope", ct, nil), http.StatusForbidden},
		{"cross-site", send(form.Encode(), ct, map[string]string{"Sec-Fetch-Site": "cross-site"}), http.StatusForbidden},
		{"foreign origin", send(form.Encode(), ct, map[string]string{"Origin": "https://attacker.example"}), http.StatusForbidden},
		{"too large", send("junk="+strings.Repeat("x", maxPostBody), ct, nil), http.StatusRequestEntityTooLarge},
		{"bad form", send("a=%zz", ct, nil), http.StatusBadRequest},
	} {
		if tc.rec.Code != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, tc.rec.Code, tc.want)
			continue
		}
		if msg := answerJSON(t, tc.rec)["message"]; msg == "" {
			t.Errorf("%s: no message in the JSON answer", tc.name)
		}
	}
	if got := userCol(t, pool, 1, "timezone"); got != "America/New_York" {
		t.Errorf("a refused request stored the zone %q", got)
	}
	// Without the header the answer is the page.
	rec := request(t, h, http.MethodPost, "/account/timezone", a, url.Values{"timezone": {"Asia/Tokyo"}})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("without the header: %d %q, want the HTML 403", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestAutoSaveIsPartial(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", testPassword)
	exec(t, pool, `update users set location = 'Lake Placid', occupation = 'Ski instructor', website = 'https://example.com/a',
		birth_day = 14, birth_month = 3, birth_year = 1984 where id = 1`)

	rec := postWith(t, h, "/account/about", a, url.Values{"website": {"https://example.org/b"}}, saveHeader)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("website alone = %d\n%s", rec.Code, rec.Body.String())
	}
	for col, want := range map[string]string{"location": "Lake Placid", "occupation": "Ski instructor", "website": "https://example.org/b",
		"birth_day": "14", "birth_month": "3", "birth_year": "1984"} {
		if got := colOrEmpty(t, pool, 1, col); got != want {
			t.Errorf("%s = %q, want %q", col, got, want)
		}
	}
	// An empty value that is present clears the field.
	rec = postWith(t, h, "/account/about", a, url.Values{"occupation": {""}}, saveHeader)
	if rec.Code != http.StatusNoContent || colOrEmpty(t, pool, 1, "occupation") != "" || colOrEmpty(t, pool, 1, "location") != "Lake Placid" {
		t.Errorf("clearing the occupation: %d", rec.Code)
	}
	// bday and bmonth without byear leave the birthday as it is.
	rec = postWith(t, h, "/account/about", a, url.Values{"bday": {"2"}, "bmonth": {"5"}}, saveHeader)
	if rec.Code != http.StatusNoContent {
		t.Errorf("bday and bmonth = %d", rec.Code)
	}
	for col, want := range map[string]string{"birth_day": "14", "birth_month": "3", "birth_year": "1984"} {
		if got := colOrEmpty(t, pool, 1, col); got != want {
			t.Errorf("%s = %q, want %q (the birthday must not change)", col, got, want)
		}
	}
	// All three together change it, and the empty three clear it.
	rec = postWith(t, h, "/account/about", a, url.Values{"bday": {"2"}, "bmonth": {"5"}, "byear": {""}}, saveHeader)
	if rec.Code != http.StatusNoContent || colOrEmpty(t, pool, 1, "birth_day") != "2" || colOrEmpty(t, pool, 1, "birth_year") != "" {
		t.Errorf("a birthday with no year: %d day=%q year=%q", rec.Code, colOrEmpty(t, pool, 1, "birth_day"), colOrEmpty(t, pool, 1, "birth_year"))
	}
	rec = postWith(t, h, "/account/about", a, url.Values{"bday": {""}, "bmonth": {""}, "byear": {""}}, saveHeader)
	if rec.Code != http.StatusNoContent || colOrEmpty(t, pool, 1, "birth_day") != "" {
		t.Errorf("clearing the birthday = %d", rec.Code)
	}
}

func TestAvatarUpload(t *testing.T) {
	h, pool, dataDir := postingSiteWithData(t)
	a := signIn(t, h, "alice", testPassword)
	oldPath := avatarOf(t, pool, 1)
	if oldPath != "avatars/1.png" || !fileExists(filepath.Join(dataDir, oldPath)) {
		t.Fatalf("the fixture avatar is %q", oldPath)
	}

	// Without script: 303.
	rec := upload(t, h, a, testPNG(t, 300, 200), nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/account?done=avatar" {
		t.Fatalf("upload = %d %q\n%s", rec.Code, rec.Header().Get("Location"), bodyOf(t, rec))
	}
	first := avatarOf(t, pool, 1)
	if !avatarName.MatchString(first) {
		t.Fatalf("stored path %q does not match %s", first, avatarName)
	}
	f, err := os.Open(filepath.Join(dataDir, first))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(f)
	_ = f.Close()
	if err != nil || cfg.Width != 128 || cfg.Height != 128 {
		t.Errorf("the stored file is %dx%d (%v), want 128x128", cfg.Width, cfg.Height, err)
	}
	if info, err := os.Stat(filepath.Join(dataDir, first)); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("file mode = %v (%v), want 0644", info.Mode().Perm(), err)
	}
	if fileExists(filepath.Join(dataDir, oldPath)) {
		t.Error("the old avatar file is still there")
	}

	// With the header: 200 and the site path of the stored file.
	rec = upload(t, h, a, testPNG(t, 64, 64), saveHeader)
	if rec.Code != http.StatusOK {
		t.Fatalf("scripted upload = %d\n%s", rec.Code, rec.Body.String())
	}
	second := avatarOf(t, pool, 1)
	if !avatarName.MatchString(second) || second == first {
		t.Errorf("second path %q, first %q: want a new name", second, first)
	}
	if got := answerJSON(t, rec); got["src"] != "/files/"+second || len(got) != 1 {
		t.Errorf("answer = %v, want src /files/%s", got, second)
	}
	if fileExists(filepath.Join(dataDir, first)) {
		t.Error("the first avatar file is still there after the second upload")
	}
	// The site serves the new file.
	if rec := getAs(t, h, "/files/"+second, a); rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "image/png") {
		t.Errorf("GET /files/%s = %d %q", second, rec.Code, rec.Header().Get("Content-Type"))
	}
	// The account page shows it.
	if page := bodyOf(t, getAs(t, h, "/account", a)); !strings.Contains(page, `src="/files/`+second+`"`) {
		t.Error("the account page does not show the new avatar")
	}

	// Remove: 204 with the header, the path is cleared and the file is gone.
	rec = postWith(t, h, "/account/avatar/remove", a, url.Values{}, saveHeader)
	if rec.Code != http.StatusNoContent {
		t.Errorf("remove = %d, want 204", rec.Code)
	}
	if avatarOf(t, pool, 1) != "" || fileExists(filepath.Join(dataDir, second)) {
		t.Error("the avatar remains after a remove")
	}
	// A remove with no avatar is not an error.
	if rec := postWith(t, h, "/account/avatar/remove", a, url.Values{}, saveHeader); rec.Code != http.StatusNoContent {
		t.Errorf("second remove = %d, want 204", rec.Code)
	}
}

// The folder of the avatars is made when it is missing.
func TestAvatarUploadMakesTheFolder(t *testing.T) {
	h, pool, dataDir := postingSiteWithData(t)
	if err := os.RemoveAll(filepath.Join(dataDir, "avatars")); err != nil {
		t.Fatal(err)
	}
	a := signIn(t, h, "bob", testPassword)
	rec := upload(t, h, a, testPNG(t, 40, 40), saveHeader)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload with no folder = %d\n%s", rec.Code, rec.Body.String())
	}
	var p string
	if err := pool.QueryRow(t.Context(), `select avatar_path from users where id = 2`).Scan(&p); err != nil || !regexp.MustCompile(`^avatars/u2-[0-9a-f]{16}\.png$`).MatchString(p) {
		t.Errorf("bob's avatar path = %q (%v)", p, err)
	}
	if !fileExists(filepath.Join(dataDir, p)) {
		t.Error("the file is not on disk")
	}
}

// Only a path that board.ValidAvatarPath accepts is deleted after a change.
func TestAvatarOldFileCheck(t *testing.T) {
	h, pool, dataDir := postingSiteWithData(t)
	a := signIn(t, h, "alice", testPassword)
	keep := filepath.Join(dataDir, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, stored := range []string{"avatars/../keep.txt", "avatars/.hidden", "keep.txt", "avatars//x.png"} {
		exec(t, pool, `update users set avatar_path = '`+stored+`' where id = 1`)
		if rec := upload(t, h, a, testPNG(t, 20, 20), saveHeader); rec.Code != http.StatusOK {
			t.Fatalf("upload over %q = %d", stored, rec.Code)
		}
		if !fileExists(keep) {
			t.Fatalf("a change over the stored path %q deleted a file outside the avatars", stored)
		}
		exec(t, pool, `update users set avatar_path = '`+stored+`' where id = 1`)
		if rec := postWith(t, h, "/account/avatar/remove", a, url.Values{}, saveHeader); rec.Code != http.StatusNoContent {
			t.Fatalf("remove over %q = %d", stored, rec.Code)
		}
		if !fileExists(keep) {
			t.Fatalf("a remove over the stored path %q deleted a file outside the avatars", stored)
		}
	}
}

func TestAvatarRefusals(t *testing.T) {
	h, pool, dataDir := postingSiteWithData(t)
	a := signIn(t, h, "alice", testPassword)
	token := accountToken(t, h, a)
	before := avatarOf(t, pool, 1)

	// 2 MiB: 413, as a page and as JSON.
	big := bytes.Repeat([]byte{0x89}, 2<<20)
	rec := uploadWithToken(t, h, a, token, big, nil)
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(bodyOf(t, rec), "<html") {
		t.Errorf("2 MiB = %d, want 413 in the layout", rec.Code)
	}
	rec = uploadWithToken(t, h, a, token, big, saveHeader)
	if rec.Code != http.StatusRequestEntityTooLarge || answerJSON(t, rec)["message"] == "" {
		t.Errorf("2 MiB with the header = %d", rec.Code)
	}
	// Just over 1 MiB of file, under the body cap: the maker refuses it.
	rec = uploadWithToken(t, h, a, token, append(testPNG(t, 8, 8), make([]byte, 1<<20)...), saveHeader)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("1 MiB + a PNG = %d, want 413", rec.Code)
	}

	// A text file.
	rec = uploadWithToken(t, h, a, token, []byte("hello, this is not an image"), saveHeader)
	if got := answerJSON(t, rec); rec.Code != http.StatusBadRequest || got["message"] != "Pick a PNG, JPEG or GIF." || got["field"] != "avatar" {
		t.Errorf("text file = %d %v", rec.Code, got)
	}
	// And as a page: the account page with the message beside the file box.
	rec = uploadWithToken(t, h, a, token, []byte("hello"), nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(bodyOf(t, rec), "Pick a PNG, JPEG or GIF.") {
		t.Errorf("text file as a page = %d", rec.Code)
	}

	// 50,000 pixels wide.
	rec = uploadWithToken(t, h, a, token, testPNG(t, 50000, 1), saveHeader)
	if got := answerJSON(t, rec); rec.Code != http.StatusBadRequest ||
		got["message"] != "That image is too large. Use one of at most 8,000 pixels a side." {
		t.Errorf("50,000 pixels = %d %v", rec.Code, got)
	}

	// No file in the form.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("csrf", token)
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/account/avatar", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("X-Gobb-Save", "1")
	for _, c := range a {
		r.AddCookie(c)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("no file = %d, want 400", rec.Code)
	}

	// No token, and a wrong one.
	for _, tok := range []string{"", "nope"} {
		rec = uploadWithToken(t, h, a, tok, testPNG(t, 20, 20), nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("token %q = %d, want 403", tok, rec.Code)
		}
	}
	if avatarOf(t, pool, 1) != before {
		t.Error("a refused upload changed the avatar")
	}
	entries, _ := fs.ReadDir(os.DirFS(dataDir), "avatars")
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "u1-") {
			t.Errorf("a refused upload left the file %s", e.Name())
		}
	}
}

// A member without account.avatar gets 403 and nothing is written.
func TestAvatarNeedsTheRight(t *testing.T) {
	h, pool, dataDir := postingSiteWithData(t)
	a := signIn(t, h, "alice", testPassword)
	exec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (1, 0, 'account.avatar', 2)`)
	exec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (1, 0, 'account.profile', 2)`)
	if rec := upload(t, h, a, testPNG(t, 20, 20), saveHeader); rec.Code != http.StatusForbidden {
		t.Errorf("upload without the right = %d, want 403", rec.Code)
	}
	if rec := postWith(t, h, "/account/avatar/remove", a, url.Values{}, saveHeader); rec.Code != http.StatusForbidden {
		t.Errorf("remove without the right = %d, want 403", rec.Code)
	}
	if rec := postWith(t, h, "/account/about", a, url.Values{"location": {"x"}}, saveHeader); rec.Code != http.StatusForbidden {
		t.Errorf("about without the right = %d, want 403", rec.Code)
	}
	if avatarOf(t, pool, 1) != "avatars/1.png" || !fileExists(filepath.Join(dataDir, "avatars/1.png")) {
		t.Error("a refused change touched the avatar")
	}
	// The page shows neither section, and still shows the time zone.
	page := bodyOf(t, getAs(t, h, "/account", a))
	for _, absent := range []string{`name="avatar"`, `name="location"`} {
		if strings.Contains(page, absent) {
			t.Errorf("the page still holds %s", absent)
		}
	}
	if !strings.Contains(page, `name="timezone"`) {
		t.Error("the page lacks the time zone")
	}
}

func TestAvatarUploadRateLimit(t *testing.T) {
	h, pool, _ := postingSiteWithData(t)
	a := signIn(t, h, "alice", testPassword)
	token := accountToken(t, h, a)
	img := testPNG(t, 16, 16)
	for i := range 10 {
		if rec := uploadWithToken(t, h, a, token, img, saveHeader); rec.Code != http.StatusOK {
			t.Fatalf("upload %d = %d", i+1, rec.Code)
		}
	}
	before := avatarOf(t, pool, 1)
	rec := uploadWithToken(t, h, a, token, img, saveHeader)
	if got := answerJSON(t, rec); rec.Code != http.StatusTooManyRequests || got["message"] != "Too many uploads. Wait an hour and try again." {
		t.Errorf("11th upload = %d %v", rec.Code, got)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("the 429 has no Retry-After")
	}
	if avatarOf(t, pool, 1) != before {
		t.Error("the refused upload changed the avatar")
	}
	// Another member has a count of their own.
	b := signIn(t, h, "bob", testPassword)
	if rec := upload(t, h, b, img, saveHeader); rec.Code != http.StatusOK {
		t.Errorf("bob's first upload = %d", rec.Code)
	}
}

func TestSaveRateLimit(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", testPassword)
	token := accountToken(t, h, a)
	zones := []string{"Europe/Berlin", "Asia/Tokyo"}
	for i := range 120 {
		rec := postWith(t, h, "/account/timezone", a, url.Values{"timezone": {zones[i%2]}, "csrf": {token}}, saveHeader)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("save %d = %d\n%s", i+1, rec.Code, rec.Body.String())
		}
	}
	if got := userCol(t, pool, 1, "timezone"); got != "Asia/Tokyo" {
		t.Fatalf("timezone after 120 saves = %q", got)
	}
	for _, target := range []string{"/account/timezone", "/account/dateformat", "/account/about", "/account/avatar/remove", "/account/signature"} {
		rec := postWith(t, h, target, a, url.Values{"timezone": {"Pacific/Auckland"}, "dateformat": {"iso"}, "location": {"x"}, "signature": {"x"}, "csrf": {token}}, saveHeader)
		if got := answerJSON(t, rec); rec.Code != http.StatusTooManyRequests || got["message"] != "Too many changes. Wait a few minutes and try again." {
			t.Errorf("save over the limit on %s = %d %v", target, rec.Code, got)
		}
		if rec.Header().Get("Retry-After") == "" {
			t.Errorf("%s: the 429 has no Retry-After", target)
		}
	}
	if got := userCol(t, pool, 1, "timezone"); got != "Asia/Tokyo" {
		t.Errorf("a refused save stored %q", got)
	}
	if got := userCol(t, pool, 1, "date_format"); got != "standard" {
		t.Errorf("a refused save stored the format %q", got)
	}
	// Without script the answer is the page, with the message.
	rec := postWith(t, h, "/account/timezone", a, url.Values{"timezone": {"Pacific/Auckland"}, "csrf": {token}}, nil)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(bodyOf(t, rec), "Too many changes. Wait a few minutes and try again.") {
		t.Errorf("page over the limit = %d", rec.Code)
	}
	// Another member is not affected.
	b := signIn(t, h, "bob", testPassword)
	if rec := postWith(t, h, "/account/timezone", b, url.Values{"timezone": {"Asia/Tokyo"}}, saveHeader); rec.Code != http.StatusNoContent {
		t.Errorf("bob's save = %d", rec.Code)
	}
}

func TestAccountPageUnknownStoredZone(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", testPassword)
	exec(t, pool, `update users set timezone = 'Asia/Old_Name' where id = 1`)
	rec := getAs(t, h, "/account", a)
	page := bodyOf(t, rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /account = %d", rec.Code)
	}
	if !regexp.MustCompile(`<option value="Asia/Old_Name" selected>Asia/Old_Name \(your current setting\)</option>`).MatchString(page) {
		t.Errorf("the page lacks the stored zone as the selected first option")
	}
	if n := strings.Count(page, " selected"); n < 1 {
		t.Error("nothing is selected")
	}
	// A date format save still works.
	if rec := postWith(t, h, "/account/dateformat", a, url.Values{"dateformat": {"iso"}}, saveHeader); rec.Code != http.StatusNoContent {
		t.Errorf("date format save = %d, want 204", rec.Code)
	}
	if got := userCol(t, pool, 1, "timezone"); got != "Asia/Old_Name" {
		t.Errorf("the date format save changed the zone to %q", got)
	}
}

func TestAccountPageSettingsSections(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", testPassword)
	exec(t, pool, `update users set timezone = 'Europe/London', date_format = 'relative', location = 'Lake Placid',
		birth_day = 14, birth_month = 3, birth_year = 1984 where id = 1`)
	page := bodyOf(t, getAs(t, h, "/account", a))
	for _, want := range []string{
		`action="/account/timezone"`, `action="/account/dateformat"`, `action="/account/about"`,
		`action="/account/avatar"`, `action="/account/avatar/remove"`, `enctype="multipart/form-data"`,
		`<optgroup label="Common">`, `<optgroup label="All zones">`,
		`<option value="Europe/London" selected>`,
		`id="timezone-status"`, `id="dateformat-status"`, `id="location-status"`, `id="website-status"`,
		`id="birthday-status"`, `id="avatar-status"`, `id="signature-status"`,
		`role="status" aria-live="polite"`, `save-fallback`, `data-autosave`,
		"Changes save as you make them.", `href="/u/1"`,
		`<option value="3" selected>March</option>`, `<option value="1984" selected>1984</option>`,
		"Leave out the year if you don't want your age shown.",
		"A PNG, JPEG or GIF of at most 1 MB.",
		"Right now it is",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the account page lacks %s", want)
		}
	}
	if got, _ := inputValue(page, "location"); got != "Lake Placid" {
		t.Errorf("location box = %q", got)
	}
	if !regexp.MustCompile(`name="dateformat" id="[^"]*" value="relative" checked`).MatchString(page) &&
		!regexp.MustCompile(`value="relative" checked`).MatchString(page) {
		t.Error("the relative format is not checked")
	}
	// The latest year is now - 19 and the list ends at 1900.
	latest := regexp.MustCompile(`<select[^>]*name="byear"[^>]*>(?s:(.*?))</select>`).FindStringSubmatch(page)
	if latest == nil {
		t.Fatal("no year list")
	}
	years := regexp.MustCompile(`<option value="(\d+)"`).FindAllStringSubmatch(latest[1], -1)
	if len(years) < 100 || years[len(years)-1][1] != "1900" {
		t.Errorf("year list: %d entries, last %v", len(years), years[len(years)-1])
	}
	wantTop := time.Now().UTC().Year() - 19
	if years[0][1] != strconv.Itoa(wantTop) {
		t.Errorf("first year = %s, want %d", years[0][1], wantTop)
	}
	// The password and email forms keep their button and are not auto-saved.
	for _, form := range regexp.MustCompile(`(?s)<form[^>]*action="/account/(?:password|email)"[^>]*>`).FindAllString(page, -1) {
		if strings.Contains(form, "data-autosave") {
			t.Errorf("%s is auto-saved", form)
		}
	}
	// No inline script, style attribute or style element.
	if strings.Contains(page, " style=") || strings.Contains(page, "<style") || strings.Contains(page, "<script>") {
		t.Error("the account page holds inline script or style")
	}
}

func TestAccountGuest(t *testing.T) {
	h, _ := siteWithPool(t, postingBoard(), Options{})
	rec := get(t, h, "/account")
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Errorf("guest GET /account = %d %q, want 302 to /login", rec.Code, rec.Header().Get("Location"))
	}
	if rec.Header().Get("X-Robots-Tag") != "noindex" {
		t.Errorf("the guest redirect lacks X-Robots-Tag: noindex")
	}
	for _, target := range []string{"/account/timezone", "/account/dateformat", "/account/about", "/account/avatar",
		"/account/avatar/remove", "/account/signature", "/account/password", "/account/email"} {
		page := getAs(t, h, "/", nil)
		guest := jar(nil, page)
		form := url.Values{"csrf": {formToken(t, bodyOf(t, page))}, "timezone": {"Asia/Tokyo"}, "dateformat": {"iso"},
			"location": {"x"}, "signature": {"x"}, "current": {"x"}, "next": {"x"}, "email": {"a@example.com"}}
		var rec *httptest.ResponseRecorder
		if target == "/account/avatar" {
			body, ctype := multipartBody(t, form.Get("csrf"), "me.png", testPNG(t, 8, 8))
			r := httptest.NewRequest(http.MethodPost, target, body)
			r.Header.Set("Content-Type", ctype)
			for _, c := range guest {
				r.AddCookie(c)
			}
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, r)
		} else {
			rec = request(t, h, http.MethodPost, target, guest, form)
		}
		if rec.Code != http.StatusForbidden {
			t.Errorf("guest POST %s = %d, want 403", target, rec.Code)
		}
	}
	// A member's page carries noindex and no-store.
	a := signIn(t, h, "alice", testPassword)
	rec = getAs(t, h, "/account", a)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "private, no-store" ||
		!strings.Contains(bodyOf(t, rec), `<meta name="robots" content="noindex`) {
		t.Errorf("member /account: %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
}

func TestScriptHasNoHTMLSinks(t *testing.T) {
	src, err := fs.ReadFile(staticFS, "static/gobb.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, sink := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function", "setTimeout(\"", "setInterval(\""} {
		if bytes.Contains(src, []byte(sink)) {
			t.Errorf("gobb.js holds %s", sink)
		}
	}
	for _, want := range []string{"X-Gobb-Save", "credentials", "keepalive", "pagehide", "data-autosave"} {
		if !bytes.Contains(src, []byte(want)) {
			t.Errorf("gobb.js lacks %s", want)
		}
	}
}

// Under a base path the answer to an upload names the file under that path.
func TestAvatarUploadSrcUnderBasePath(t *testing.T) {
	pool, dataDir := boardtest.Load(t, postingBoard())
	h, err := New(t.Context(), pool, Options{DataDir: dataDir, LoginFloor: time.Millisecond, BasePath: "/forum/"})
	if err != nil {
		t.Fatal(err)
	}
	a := signInAt(t, h, "/forum/", "alice")
	token := formToken(t, bodyOf(t, getAs(t, h, "/forum/account", a)))
	body, ctype := multipartBody(t, token, "me.png", testPNG(t, 30, 30))
	r := httptest.NewRequest(http.MethodPost, "/forum/account/avatar", body)
	r.Header.Set("Content-Type", ctype)
	r.Header.Set("X-Gobb-Save", "1")
	for _, c := range a {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload = %d\n%s", rec.Code, rec.Body.String())
	}
	stored := avatarOf(t, pool, 1)
	if got := answerJSON(t, rec)["src"]; got != "/forum/files/"+stored {
		t.Errorf("src = %q, want /forum/files/%s", got, stored)
	}
	if rec := getAs(t, h, "/forum/files/"+stored, a); rec.Code != http.StatusOK {
		t.Errorf("GET of the src = %d", rec.Code)
	}
}

// avatarFiles lists the files of member id in the avatars folder.
func avatarFiles(t *testing.T, dataDir string, id int) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, "avatars"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "u"+strconv.Itoa(id)+"-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// An imported file can be the avatar of several members. A change by one of them
// leaves the file; the change of the last one deletes it.
func TestAvatarSharedFileStays(t *testing.T) {
	h, pool, dataDir := postingSiteWithData(t)
	a, b := signIn(t, h, "alice", testPassword), signIn(t, h, "bob", testPassword)
	shared := filepath.Join(dataDir, "avatars", "shared.png")
	for _, remove := range []bool{false, true} {
		if err := os.WriteFile(shared, testPNG(t, 8, 8), 0o644); err != nil {
			t.Fatal(err)
		}
		exec(t, pool, `update users set avatar_path = 'avatars/shared.png' where id in (1, 2)`)
		change := func(c []*http.Cookie) *httptest.ResponseRecorder {
			if remove {
				return postWith(t, h, "/account/avatar/remove", c, url.Values{}, saveHeader)
			}
			return upload(t, h, c, testPNG(t, 30, 30), saveHeader)
		}
		if rec := change(a); rec.Code >= 300 {
			t.Fatalf("alice (remove=%v) = %d", remove, rec.Code)
		}
		if !fileExists(shared) {
			t.Fatalf("remove=%v: the shared file was deleted while bob still names it", remove)
		}
		if got := avatarOf(t, pool, 2); got != "avatars/shared.png" {
			t.Errorf("remove=%v: bob's avatar = %q", remove, got)
		}
		if rec := change(b); rec.Code >= 300 {
			t.Fatalf("bob (remove=%v) = %d", remove, rec.Code)
		}
		if fileExists(shared) {
			t.Errorf("remove=%v: the shared file stays after the last member changed away", remove)
		}
	}
}

// A refusal of one part of the About you form never drops what was sent for the
// other. Each message sits beside its field, and the page shows every sent value.
func TestAboutKeepsEverythingSent(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", testPassword)
	exec(t, pool, `update users set location = 'Old place', website = '', birth_day = null, birth_month = null, birth_year = null where id = 1`)
	const msgSite = "Enter an address that starts with https:// or http://."
	selected := func(page, value, label string) bool {
		return strings.Contains(page, `<option value="`+value+`" selected>`+label+`</option>`)
	}

	// Good text, bad birthday: the text is stored, and the birthday shows as sent.
	rec := postWith(t, h, "/account/about", a, url.Values{"location": {"Nowhere"}, "bday": {"31"}, "bmonth": {"2"}, "byear": {"1990"}}, nil)
	page := bodyOf(t, rec)
	if rec.Code != http.StatusBadRequest || !strings.Contains(page, "February 1990 has no day 31.") && !strings.Contains(page, "February has no day 31.") {
		t.Fatalf("good text, bad birthday = %d", rec.Code)
	}
	if got := userCol(t, pool, 1, "location"); got != "Nowhere" {
		t.Errorf("the good text was not stored: %q", got)
	}
	if !selected(page, "31", "31") || !selected(page, "2", "February") || !selected(page, "1990", "1990") {
		t.Error("the page does not show the birthday that was sent")
	}
	if colOrEmpty(t, pool, 1, "birth_day") != "" {
		t.Error("a refused birthday was stored")
	}

	// Bad website, good birthday: the birthday is stored, and the text shows as sent.
	rec = postWith(t, h, "/account/about", a, url.Values{"location": {"Kept in the box"}, "website": {"ftp://x.example"},
		"bday": {"14"}, "bmonth": {"3"}, "byear": {"1984"}}, nil)
	page = bodyOf(t, rec)
	if rec.Code != http.StatusBadRequest || !strings.Contains(page, msgSite) {
		t.Fatalf("bad website, good birthday = %d", rec.Code)
	}
	if colOrEmpty(t, pool, 1, "birth_day") != "14" || colOrEmpty(t, pool, 1, "birth_year") != "1984" {
		t.Error("the good birthday was dropped")
	}
	if got, _ := inputValue(page, "website"); got != "ftp://x.example" {
		t.Errorf("website box = %q", got)
	}
	if got, _ := inputValue(page, "location"); got != "Kept in the box" {
		t.Errorf("location box = %q", got)
	}
	if got := userCol(t, pool, 1, "location"); got != "Nowhere" {
		t.Errorf("a refused text save stored a location: %q", got)
	}
	if !selected(page, "3", "March") {
		t.Error("the page does not show the stored birthday")
	}

	// Both bad: both messages.
	rec = postWith(t, h, "/account/about", a, url.Values{"website": {"nope"}, "bday": {"30"}, "bmonth": {"2"}, "byear": {""}}, nil)
	page = bodyOf(t, rec)
	if rec.Code != http.StatusBadRequest || !strings.Contains(page, msgSite) || !strings.Contains(page, "February has no day 30.") {
		t.Errorf("both bad = %d; want both messages", rec.Code)
	}
	if colOrEmpty(t, pool, 1, "birth_day") != "14" {
		t.Error("a refused birthday changed the stored one")
	}

	// A birthday that is not numbers is refused beside the birthday, and the text still goes.
	rec = postWith(t, h, "/account/about", a, url.Values{"location": {"Third"}, "bday": {"x"}, "bmonth": {"3"}, "byear": {""}}, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(bodyOf(t, rec), msgBirthdayForm) || userCol(t, pool, 1, "location") != "Third" {
		t.Errorf("a birthday of letters = %d", rec.Code)
	}
}

// readerFunc is an io.Reader made of a function.
type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// A guest's upload is refused without a read of the body.
func TestAvatarGuestBodyIsNotRead(t *testing.T) {
	h, pool, _ := postingSiteWithData(t)
	page := getAs(t, h, "/", nil)
	guest := jar(nil, page)
	token := formToken(t, bodyOf(t, page))
	body, ctype := multipartBody(t, token, "me.png", bytes.Repeat([]byte{0x89}, 1<<20))
	for _, hdr := range []map[string]string{nil, saveHeader} {
		var reads atomic.Int32
		r := httptest.NewRequest(http.MethodPost, "/account/avatar", readerFunc(func(p []byte) (int, error) {
			reads.Add(1)
			return body.Read(p)
		}))
		r.Header.Set("Content-Type", ctype)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		for _, c := range guest {
			r.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Errorf("guest upload (header %v) = %d, want 403", hdr != nil, rec.Code)
		}
		if hdr != nil {
			if answerJSON(t, rec)["message"] == "" {
				t.Error("the JSON 403 has no message")
			}
		}
		if n := reads.Load(); n != 0 {
			t.Errorf("the server read the body of a guest %d times", n)
		}
	}
	if avatarOf(t, pool, 1) != "avatars/1.png" {
		t.Error("a guest changed an avatar")
	}
}

// The server takes only maxUploadsInFlight uploads at one time. The next one gets
// 503 at once, and the slot is free again when the first ends.
func TestAvatarUploadsInFlightAreCapped(t *testing.T) {
	var s *site
	onNew = func(got *site) { s = got }
	t.Cleanup(func() { onNew = nil })
	h, pool, _ := postingSiteWithData(t)
	onNew = nil
	if cap(s.uploadSlots) != maxUploadsInFlight || maxUploadsInFlight != 4 {
		t.Fatalf("the slots hold %d, want 4", cap(s.uploadSlots))
	}
	s.uploadSlots = make(chan struct{}, 1)
	a, b := signIn(t, h, "alice", testPassword), signIn(t, h, "bob", testPassword)

	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	held := httptest.NewRequest(http.MethodPost, "/account/avatar", io.MultiReader(strings.NewReader("--x\r\n"),
		readerFunc(func([]byte) (int, error) {
			once.Do(func() { close(started) })
			<-release
			return 0, io.EOF
		})))
	held.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	held.Header.Set("X-Gobb-Save", "1")
	for _, c := range a {
		held.AddCookie(c)
	}
	done := make(chan *httptest.ResponseRecorder)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, held)
		done <- rec
	}()
	<-started

	before := avatarOf(t, pool, 2)
	rec := upload(t, h, b, testPNG(t, 20, 20), saveHeader)
	if got := answerJSON(t, rec); rec.Code != http.StatusServiceUnavailable || got["message"] != msgBusy || rec.Header().Get("Retry-After") != "5" {
		t.Errorf("second upload = %d %v, want 503 with the busy text", rec.Code, got)
	}
	if avatarOf(t, pool, 2) != before {
		t.Error("the refused upload changed the avatar")
	}
	close(release)
	// The held upload ends with its body, which holds no token: refused, not 503.
	if rec := <-done; rec.Code != http.StatusForbidden && rec.Code != http.StatusBadRequest {
		t.Errorf("the held upload = %d, want 403 or 400", rec.Code)
	}
	if rec := upload(t, h, b, testPNG(t, 20, 20), saveHeader); rec.Code != http.StatusOK {
		t.Errorf("an upload after the slot is free = %d", rec.Code)
	}
}

// The no-script signature counts as a save, like every other setting.
func TestSignatureFormCountsAsASave(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", testPassword)
	token := accountToken(t, h, a)
	for i := range 120 {
		rec := postWith(t, h, "/account/dateformat", a, url.Values{"dateformat": {"iso"}, "csrf": {token}}, saveHeader)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("save %d = %d", i+1, rec.Code)
		}
	}
	rec := postWith(t, h, "/account/signature", a, url.Values{"signature": {"Over the limit."}, "csrf": {token}}, nil)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(bodyOf(t, rec), msgTooManySaves) {
		t.Errorf("the form signature over the limit = %d, want 429", rec.Code)
	}
	if got := userCol(t, pool, 1, "signature_source"); got == "Over the limit." {
		t.Error("the signature was stored over the limit")
	}
	// And one signature form counts: the 120th save is the last.
	h, _ = siteWithPool(t, postingBoard(), Options{})
	a = signIn(t, h, "alice", testPassword)
	token = accountToken(t, h, a)
	for i := range 120 {
		if rec := postWith(t, h, "/account/signature", a, url.Values{"signature": {"Text."}, "csrf": {token}}, nil); rec.Code != http.StatusSeeOther {
			t.Fatalf("form signature %d = %d", i+1, rec.Code)
		}
	}
	if rec := postWith(t, h, "/account/timezone", a, url.Values{"timezone": {"Asia/Tokyo"}, "csrf": {token}}, saveHeader); rec.Code != http.StatusTooManyRequests {
		t.Errorf("a save after 120 form signatures = %d, want 429", rec.Code)
	}
}

// A session lookup that fails answers a request from gobb.js with JSON.
func TestAutoSaveIdentifyFailureIsJSON(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", testPassword)
	token := accountToken(t, h, a)
	pool.Close()
	for _, target := range []string{"/account/timezone", "/account/avatar"} {
		var rec *httptest.ResponseRecorder
		if target == "/account/avatar" {
			rec = uploadWithToken(t, h, a, token, testPNG(t, 8, 8), saveHeader)
		} else {
			rec = postWith(t, h, target, a, url.Values{"timezone": {"Asia/Tokyo"}, "csrf": {token}}, saveHeader)
		}
		if got := answerJSON(t, rec); rec.Code != http.StatusInternalServerError || got["message"] != msgWriteFailed {
			t.Errorf("%s with a dead database = %d %v, want 500 JSON", target, rec.Code, got)
		}
	}
}

// When the write of the path fails, the new file is deleted and the old one stays.
func TestAvatarNewFileDeletedWhenTheWriteFails(t *testing.T) {
	h, pool, dataDir := postingSiteWithData(t)
	a := signIn(t, h, "alice", testPassword)
	token := accountToken(t, h, a)
	beforeSetAvatar = func() { exec(t, pool, `update users set state = 'inactive' where id = 1`) }
	t.Cleanup(func() { beforeSetAvatar = nil })
	rec := uploadWithToken(t, h, a, token, testPNG(t, 20, 20), saveHeader)
	if got := answerJSON(t, rec); rec.Code != http.StatusNotFound || got["message"] != msgNotSaved {
		t.Errorf("upload with a failed write = %d %v, want 404 and the neutral text", rec.Code, got)
	}
	if files := avatarFiles(t, dataDir, 1); len(files) != 0 {
		t.Errorf("the new file stays after a failed write: %v", files)
	}
	if !fileExists(filepath.Join(dataDir, "avatars/1.png")) || avatarOf(t, pool, 1) != "avatars/1.png" {
		t.Error("the old avatar was lost")
	}
}

// A full gate answers an upload with 503 and the busy text, and stores nothing.
func TestAvatarGateBusy(t *testing.T) {
	gate := login.NewGate(1 << 16)
	h, pool, dataDir := postingSiteOpts(t, Options{LoginGate: gate, LoginGateWait: 20 * time.Millisecond})
	a := signIn(t, h, "alice", testPassword)
	token := accountToken(t, h, a)
	hold, err := gate.Acquire(t.Context(), 1<<16, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	for _, hdr := range []map[string]string{saveHeader, nil} {
		rec := uploadWithToken(t, h, a, token, testPNG(t, 20, 20), hdr)
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" {
			t.Fatalf("upload on a full gate = %d, Retry-After %q, want 503 and 5", rec.Code, rec.Header().Get("Retry-After"))
		}
		if hdr != nil {
			if got := answerJSON(t, rec); got["message"] != msgBusy {
				t.Errorf("message = %v", got)
			}
		} else if !strings.Contains(bodyOf(t, rec), msgBusy) {
			t.Error("the page lacks the busy text")
		}
	}
	if avatarOf(t, pool, 1) != "avatars/1.png" || len(avatarFiles(t, dataDir, 1)) != 0 {
		t.Error("a busy gate changed the avatar")
	}
}

// A request that is cancelled before the write does not leave the row and the
// files out of step: the write still finishes.
func TestAvatarWriteSurvivesACancelledRequest(t *testing.T) {
	h, pool, dataDir := postingSiteWithData(t)
	a := signIn(t, h, "alice", testPassword)
	token := accountToken(t, h, a)
	ctx, cancel := context.WithCancel(t.Context())
	beforeSetAvatar = cancel
	t.Cleanup(func() { beforeSetAvatar = nil })
	body, ctype := multipartBody(t, token, "me.png", testPNG(t, 20, 20))
	r := httptest.NewRequest(http.MethodPost, "/account/avatar", body).WithContext(ctx)
	r.Header.Set("Content-Type", ctype)
	r.Header.Set("X-Gobb-Save", "1")
	for _, c := range a {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	stored := avatarOf(t, pool, 1)
	if !avatarName.MatchString(stored) || !fileExists(filepath.Join(dataDir, stored)) {
		t.Errorf("after a cancelled request the row names %q (file there: %v)", stored, fileExists(filepath.Join(dataDir, stored)))
	}
	if fileExists(filepath.Join(dataDir, "avatars/1.png")) {
		t.Error("the old file stays after the write committed")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Error("the request was not cancelled")
	}
}

// The mode of a new avatar file is 0644 whatever the umask.
func TestAvatarFileModeIgnoresTheUmask(t *testing.T) {
	h, pool, dataDir := postingSiteWithData(t)
	a := signIn(t, h, "alice", testPassword)
	token := accountToken(t, h, a)
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	if rec := uploadWithToken(t, h, a, token, testPNG(t, 20, 20), saveHeader); rec.Code != http.StatusOK {
		t.Fatalf("upload = %d", rec.Code)
	}
	info, err := os.Stat(filepath.Join(dataDir, avatarOf(t, pool, 1)))
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v (%v), want 0644", info.Mode().Perm(), err)
	}
}

// Every answer to a POST of the settings carries the strict policy of the site,
// page or JSON: the policy is set for all responses before any handler runs
// (secure in headers.go), and the account handlers add no embed to it.
func TestAccountPostAnswersCSP(t *testing.T) {
	h, _, _ := postingSiteWithData(t)
	a := signIn(t, h, "alice", testPassword)
	token := accountToken(t, h, a)
	bad := png8(t)
	type req struct {
		name string
		do   func(hdr map[string]string) *httptest.ResponseRecorder
	}
	form := func(target string, f url.Values) func(map[string]string) *httptest.ResponseRecorder {
		return func(hdr map[string]string) *httptest.ResponseRecorder {
			f = cloneForm(f)
			f.Set("csrf", token)
			return postWith(t, h, target, a, f, hdr)
		}
	}
	for _, tc := range []req{
		{"timezone", form("/account/timezone", url.Values{"timezone": {"UTC"}})},
		{"dateformat", form("/account/dateformat", url.Values{"dateformat": {"fancy"}})},
		{"about", form("/account/about", url.Values{"website": {"nope"}})},
		{"signature", form("/account/signature", url.Values{"signature": {strings.Repeat("x", 2001)}})},
		{"avatar remove", form("/account/avatar/remove", url.Values{})},
		{"avatar", func(hdr map[string]string) *httptest.ResponseRecorder {
			return uploadWithToken(t, h, a, token, bad, hdr)
		}},
	} {
		for _, hdr := range []map[string]string{nil, saveHeader} {
			rec := tc.do(hdr)
			where := tc.name + " (script: " + strconv.FormatBool(hdr != nil) + ")"
			csp := rec.Header().Get("Content-Security-Policy")
			checkScriptSrc(t, where, csp)
			for name, want := range map[string]string{"object-src": "'none'", "base-uri": "'none'", "form-action": "'self'", "default-src": "'none'"} {
				if got, _ := directive(csp, name); got != want {
					t.Errorf("%s: %s = %q, want %q", where, name, got, want)
				}
			}
			if strings.Contains(csp, "unsafe") {
				t.Errorf("%s: the policy holds unsafe: %s", where, csp)
			}
			if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Errorf("%s: Cache-Control = %q", where, got)
			}
			if body := bodyOf(t, rec); strings.Contains(body, "<script>") || strings.Contains(body, " style=") || strings.Contains(body, "<style") {
				t.Errorf("%s: the answer holds inline script or style", where)
			}
		}
	}
}

// png8 is a text that is not an image, for a refused upload.
func png8(t *testing.T) []byte { t.Helper(); return []byte("not an image at all") }

func TestZoneCity(t *testing.T) {
	for zone, want := range map[string]string{
		"America/New_York": "New York", "Europe/London": "London",
		"America/Argentina/Buenos_Aires": "Buenos Aires", "Asia/Old_Name": "Old Name", "Etc": "Etc",
	} {
		if got := zoneCity(zone); got != want {
			t.Errorf("zoneCity(%q) = %q, want %q", zone, got, want)
		}
	}
}

// The hint under the time zone names the time in the standard format and the city.
func TestAccountZoneHint(t *testing.T) {
	now := time.Date(2026, 10, 25, 20, 44, 0, 0, time.UTC)
	h, pool := siteWithPool(t, postingBoard(), Options{Now: func() time.Time { return now }})
	a := signIn(t, h, "alice", testPassword)
	exec(t, pool, `update users set timezone = 'America/Los_Angeles' where id = 1`)
	page := bodyOf(t, getAs(t, h, "/account", a))
	want := `Right now it is <span id="timezone-now">Oct 25, 2026, 1:44 pm</span> in <span id="timezone-city">Los Angeles</span>.`
	if !strings.Contains(page, want) {
		t.Errorf("the hint is not %q", want)
	}
}

// sessionLookups counts the queries that look a session up.
type sessionLookups struct{ n atomic.Int64 }

func (c *sessionLookups) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.Contains(d.SQL, "from sessions s join users u") {
		c.n.Add(1)
	}
	return ctx
}

func (*sessionLookups) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// A refused upload of a visitor with a dead session cookie looks the cookie up once:
// the refusal page uses the identity that the guard found.
func TestRefusedUploadLooksTheSessionUpOnce(t *testing.T) {
	pool, dataDir := boardtest.Load(t, postingBoard())
	cfg := pool.Config()
	count := &sessionLookups{}
	cfg.ConnConfig.Tracer = count
	counted, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(counted.Close)
	h, err := New(t.Context(), counted, Options{DataDir: dataDir, LoginFloor: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	dead := []*http.Cookie{{Name: sessionCookie, Value: "no-such-session"}}
	for _, hdr := range []map[string]string{nil, saveHeader} {
		before := count.n.Load()
		rec := uploadWithToken(t, h, dead, "x", testPNG(t, 8, 8), hdr)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("upload with a dead session = %d, want 403", rec.Code)
		}
		if got := count.n.Load() - before; got != 1 {
			t.Errorf("header %v: the dead cookie was looked up %d times, want 1", hdr != nil, got)
		}
	}
}

// One member has one upload in flight. A second upload of the same member gets 503
// at once, as JSON for gobb.js; another member is not affected; and the member can
// upload again when the first one ends.
func TestAvatarOneUploadInFlightPerMember(t *testing.T) {
	h, pool, _ := postingSiteWithData(t)
	a, b := signIn(t, h, "alice", testPassword), signIn(t, h, "bob", testPassword)
	token := accountToken(t, h, a)

	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	held := httptest.NewRequest(http.MethodPost, "/account/avatar", io.MultiReader(strings.NewReader("--x\r\n"),
		readerFunc(func([]byte) (int, error) {
			once.Do(func() { close(started) })
			<-release
			return 0, io.EOF
		})))
	held.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	held.Header.Set("X-Gobb-Save", "1")
	for _, c := range a {
		held.AddCookie(c)
	}
	done := make(chan *httptest.ResponseRecorder)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, held)
		done <- rec
	}()
	<-started

	before := avatarOf(t, pool, 1)
	for _, hdr := range []map[string]string{saveHeader, nil} {
		rec := uploadWithToken(t, h, a, token, testPNG(t, 20, 20), hdr)
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" {
			t.Errorf("second upload of the member (header %v) = %d, Retry-After %q, want 503 and 5", hdr != nil, rec.Code, rec.Header().Get("Retry-After"))
		}
		if hdr != nil {
			if got := answerJSON(t, rec); got["message"] != msgBusy {
				t.Errorf("the JSON 503 = %v, want the busy text", got)
			}
		}
	}
	if avatarOf(t, pool, 1) != before {
		t.Error("a refused upload changed the avatar")
	}
	if rec := upload(t, h, b, testPNG(t, 20, 20), saveHeader); rec.Code != http.StatusOK {
		t.Errorf("another member's upload = %d, want 200", rec.Code)
	}
	close(release)
	<-done
	if rec := uploadWithToken(t, h, a, token, testPNG(t, 20, 20), saveHeader); rec.Code != http.StatusOK {
		t.Errorf("an upload after the first ended = %d, want 200", rec.Code)
	}
}

// A refused text box and a failed birthday write give one page with both: the
// input message beside the field and the notice of the failure.
func TestAboutInputErrorSurvivesAFailedWrite(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{})
	a := signIn(t, h, "alice", testPassword)
	// A write of the birthday day 15 fails with an error that is not an input error.
	exec(t, pool, `create function refuse_day() returns trigger language plpgsql as
		$$ begin raise exception 'test failure'; end $$`)
	exec(t, pool, `create trigger refuse_day before update of birth_day on users
		for each row when (new.birth_day = 15) execute function refuse_day()`)
	rec := postWith(t, h, "/account/about", a, url.Values{"website": {"nope"}, "bday": {"15"}, "bmonth": {"3"}, "byear": {"1984"}}, nil)
	page := bodyOf(t, rec)
	const msgSite = "Enter an address that starts with https:// or http://."
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(page, msgSite) {
		t.Error("the message of the refused website is lost")
	}
	if !strings.Contains(page, msgWriteFailed) {
		t.Error("the notice of the failed write is missing")
	}
	if got, _ := inputValue(page, "website"); got != "nope" {
		t.Errorf("website box = %q, want the value that was sent", got)
	}
}

// A page that answers a refusal for a member who went inactive during the request
// is 404 with the neutral text, not 500: the upload page and the 429 page.
func TestRefusalPagesForAMemberWhoIsGone(t *testing.T) {
	gone := func(pool *pgxpool.Pool) func() {
		return func() { exec(t, pool, `update users set state = 'inactive' where id = 1`) }
	}
	t.Cleanup(func() { afterBegin = nil })

	t.Run("upload", func(t *testing.T) {
		h, pool := siteWithPool(t, postingBoard(), Options{})
		a := signIn(t, h, "alice", testPassword)
		token := accountToken(t, h, a)
		afterBegin = gone(pool)
		rec := uploadWithToken(t, h, a, token, png8(t), nil) // avatar.Make refuses it
		if rec.Code != http.StatusNotFound || !strings.Contains(bodyOf(t, rec), msgNotSaved) {
			t.Errorf("refused upload of a member who is gone = %d, want 404 with the neutral text", rec.Code)
		}
	})

	t.Run("429", func(t *testing.T) {
		h, pool := siteWithPool(t, postingBoard(), Options{MemberSaves: 1})
		a := signIn(t, h, "alice", testPassword)
		token := accountToken(t, h, a)
		afterBegin = nil
		if rec := postWith(t, h, "/account/timezone", a, url.Values{"timezone": {"Asia/Tokyo"}, "csrf": {token}}, saveHeader); rec.Code != http.StatusNoContent {
			t.Fatalf("the one save = %d", rec.Code)
		}
		afterBegin = gone(pool)
		rec := postWith(t, h, "/account/timezone", a, url.Values{"timezone": {"Asia/Tokyo"}, "csrf": {token}}, nil)
		if rec.Code != http.StatusNotFound || !strings.Contains(bodyOf(t, rec), msgNotSaved) {
			t.Errorf("429 page of a member who is gone = %d, want 404 with the neutral text", rec.Code)
		}
	})
}

// The no-script signature counts as a save before the page reads anything, as every
// other setting does: a try that fails on the read still counts.
func TestSignatureFormIsCountedFirst(t *testing.T) {
	h, pool := siteWithPool(t, postingBoard(), Options{MemberSaves: 1})
	a := signIn(t, h, "alice", testPassword)
	token := accountToken(t, h, a)
	afterBegin = func() { exec(t, pool, `update users set state = 'inactive' where id = 1`) }
	t.Cleanup(func() { afterBegin = nil })
	rec := postWith(t, h, "/account/signature", a, url.Values{"signature": {"First."}, "csrf": {token}}, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("the signature of a member who is gone = %d, want 404", rec.Code)
	}
	afterBegin = nil
	exec(t, pool, `update users set state = 'active' where id = 1`)
	rec = postWith(t, h, "/account/signature", a, url.Values{"signature": {"Second."}, "csrf": {token}}, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("the next signature = %d, want 429: the first try counted", rec.Code)
	}
	if body := bodyOf(t, rec); !strings.Contains(body, "Second.</textarea>") || strings.Contains(body, "First.") {
		t.Error("the 429 page does not show the signature that was sent")
	}
}

// The 429 page of the no-script "About you" shows the values that the member sent.
func TestAbout429ShowsTheSentValues(t *testing.T) {
	h, _ := siteWithPool(t, postingBoard(), Options{MemberSaves: 1})
	a := signIn(t, h, "alice", testPassword)
	token := accountToken(t, h, a)
	if rec := postWith(t, h, "/account/dateformat", a, url.Values{"dateformat": {"iso"}, "csrf": {token}}, saveHeader); rec.Code != http.StatusNoContent {
		t.Fatalf("the one save = %d", rec.Code)
	}
	rec := postWith(t, h, "/account/about", a, url.Values{"location": {"Sent place"}, "website": {"https://sent.example"},
		"bday": {"14"}, "bmonth": {"3"}, "byear": {"1984"}, "csrf": {token}}, nil)
	page := bodyOf(t, rec)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(page, msgTooManySaves) {
		t.Fatalf("about over the limit = %d", rec.Code)
	}
	if got, _ := inputValue(page, "location"); got != "Sent place" {
		t.Errorf("location box = %q, want the value that was sent", got)
	}
	if got, _ := inputValue(page, "website"); got != "https://sent.example" {
		t.Errorf("website box = %q, want the value that was sent", got)
	}
	if !strings.Contains(page, `<option value="14" selected>14</option>`) || !strings.Contains(page, `<option value="3" selected>March</option>`) {
		t.Error("the page does not show the birthday that was sent")
	}
}

// A member without account.avatar gets 403 before the server reads the body and
// before the upload takes a slot: such a member cannot hold the slots of the others.
func TestAvatarUploadWithoutTheRightTakesNoSlot(t *testing.T) {
	var s *site
	onNew = func(got *site) { s = got }
	t.Cleanup(func() { onNew = nil })
	h, pool, _ := postingSiteWithData(t)
	onNew = nil
	s.uploadSlots = make(chan struct{}, 1)
	a, b := signIn(t, h, "alice", testPassword), signIn(t, h, "bob", testPassword)
	exec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (1, 0, 'account.avatar', 2)`)
	token := accountToken(t, h, a)

	// Bob holds the one slot with a body that has not ended.
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	held := httptest.NewRequest(http.MethodPost, "/account/avatar", io.MultiReader(strings.NewReader("--x\r\n"),
		readerFunc(func([]byte) (int, error) {
			once.Do(func() { close(started) })
			<-release
			return 0, io.EOF
		})))
	held.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	held.Header.Set("X-Gobb-Save", "1")
	for _, c := range b {
		held.AddCookie(c)
	}
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), held)
		close(done)
	}()
	<-started

	// Alice lacks the right: 403, not 503, and the server does not read her body.
	for _, hdr := range []map[string]string{saveHeader, nil} {
		body, ctype := multipartBody(t, token, "me.png", testPNG(t, 20, 20))
		var reads atomic.Int32
		r := httptest.NewRequest(http.MethodPost, "/account/avatar", readerFunc(func(p []byte) (int, error) {
			reads.Add(1)
			return body.Read(p)
		}))
		r.Header.Set("Content-Type", ctype)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		for _, c := range a {
			r.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Errorf("upload without the right (header %v) = %d, want 403", hdr != nil, rec.Code)
		}
		if hdr != nil && answerJSON(t, rec)["message"] == "" {
			t.Error("the JSON 403 has no message")
		}
		if n := reads.Load(); n != 0 {
			t.Errorf("the server read the body of a member without the right %d times", n)
		}
	}
	// Her refusals left no mark: when Bob's upload ends, the slot is free.
	close(release)
	<-done
	if avatarOf(t, pool, 1) != "avatars/1.png" {
		t.Error("a refused upload changed the avatar")
	}
	exec(t, pool, `delete from acl_user_grants where user_id = 1 and option = 'account.avatar'`)
	if rec := uploadWithToken(t, h, a, token, testPNG(t, 20, 20), saveHeader); rec.Code != http.StatusOK {
		t.Errorf("upload after the right is back = %d, want 200", rec.Code)
	}
}

// An image within the limits of side and pixels, but over the memory budget of the
// gate, gets its own text; the limits keep theirs. The gate is small here, so a
// PNG of 1000 x 1000 (about 5,100 KiB) is over its budget of 4,096 KiB.
func TestAvatarRefusalTexts(t *testing.T) {
	gate := login.NewGate(4096)
	h, pool, dataDir := postingSiteOpts(t, Options{LoginGate: gate})
	a := signIn(t, h, "alice", testPassword)
	token := accountToken(t, h, a)
	before := avatarOf(t, pool, 1)

	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"over the memory budget", testPNG(t, 1000, 1000), "That image needs too much memory to process. Use a smaller one."},
		{"over the side limit", testPNG(t, 50000, 1), "That image is too large. Use one of at most 8,000 pixels a side."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := uploadWithToken(t, h, a, token, tc.data, saveHeader)
			if got := answerJSON(t, rec); rec.Code != http.StatusBadRequest || got["message"] != tc.want || got["field"] != "avatar" {
				t.Errorf("as JSON = %d %v, want 400 and %q", rec.Code, got, tc.want)
			}
			rec = uploadWithToken(t, h, a, token, tc.data, nil)
			if rec.Code != http.StatusBadRequest || !strings.Contains(bodyOf(t, rec), tc.want) {
				t.Errorf("as a page = %d, want 400 and %q", rec.Code, tc.want)
			}
		})
	}
	if avatarOf(t, pool, 1) != before || len(avatarFiles(t, dataDir, 1)) != 0 {
		t.Error("a refusal changed the avatar")
	}
}
