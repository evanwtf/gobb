package web

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
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
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/boardtest"
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
	pool, dataDir := boardtest.Load(t, postingBoard())
	h, err := New(t.Context(), pool, Options{DataDir: dataDir, LoginFloor: time.Millisecond})
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
	wantTop := time.Now().Year() - 19
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
