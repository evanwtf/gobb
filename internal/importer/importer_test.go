package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/dbtest"
	"github.com/evanwtf/gobb/markup"
)

// loaded lists the data files that the loaders in this package cover: all 23.
var loaded = bundle.Files

// writeBoard writes a board to disk. It applies edit first, when edit is set.
func writeBoard(t *testing.T, edit func(b *bundletest.Board)) string {
	t.Helper()
	b := bundletest.Small()
	if edit != nil {
		edit(b)
	}
	return bundletest.Write(t, b)
}

func run(t *testing.T, pool *pgxpool.Pool, src, dataDir string, replace bool) (map[string]int, error) {
	t.Helper()
	return Run(context.Background(), pool, src, Options{DataDir: dataDir, Replace: replace})
}

func mustRun(t *testing.T, pool *pgxpool.Pool, src, dataDir string, replace bool) map[string]int {
	t.Helper()
	counts, err := run(t, pool, src, dataDir, replace)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return counts
}

func count(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	// table is a literal from the test.
	if err := pool.QueryRow(context.Background(), "select count(*) from "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func ids(t *testing.T, pool *pgxpool.Pool, query string) []int64 {
	t.Helper()
	rows, err := pool.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func sameIDs(t *testing.T, what string, got []int64, want ...int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s = %v, want %v", what, got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s = %v, want %v", what, got, want)
			return
		}
	}
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func noStaging(t *testing.T, dataDir string) {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dataDir, ".import-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m) > 0 {
		t.Errorf("staging directories remain: %v", m)
	}
}

func TestImportPeopleAndPermissions(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	dir := writeBoard(t, func(b *bundletest.Board) {
		b.Users[1].Username = "Bob"
		b.Users[1].Email = " Bob@Example.COM "
	})
	counts := mustRun(t, pool, dir, t.TempDir(), false)

	r, err := bundle.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range loaded {
		if counts[f] != r.Manifest().Counts[f] {
			t.Errorf("counts[%s] = %d, want %d", f, counts[f], r.Manifest().Counts[f])
		}
	}
	if counts["files"] != 7 {
		t.Errorf("counts[files] = %d, want 7", counts["files"])
	}
	sameIDs(t, "ranks", ids(t, pool, "select id from ranks order by id"), 1)
	sameIDs(t, "users", ids(t, pool, "select id from users order by id"), 1, 2, 3, 4)
	sameIDs(t, "gone_users", ids(t, pool, "select id from gone_users"), 8)
	sameIDs(t, "groups", ids(t, pool, "select id from groups order by id"), 1, 2, 3, 4, 5)
	sameIDs(t, "roles", ids(t, pool, "select id from acl_roles order by id"), 1, 2)
	sameIDs(t, "forums", ids(t, pool, "select id from forums order by id"), 1, 2, 3)
	for table, want := range map[string]int{
		"group_members": 8, "acl_role_settings": 19, "acl_group_grants": 17,
		"acl_user_grants": 1, "bans": 3, "smilies": 2, "legacy_routes": 3,
		"legacy_files": 1, "static_pages": 2, "settings": 7,
	} {
		if n := count(t, pool, table); n != want {
			t.Errorf("%s has %d rows, want %d", table, n, want)
		}
	}

	var clean, emailClean, scheme, avatar string
	if err := pool.QueryRow(ctx,
		`select username_clean, email_clean, password_scheme, coalesce(avatar_path, '')
		 from users where id = 2`).Scan(&clean, &emailClean, &scheme, &avatar); err != nil {
		t.Fatal(err)
	}
	if clean != "bob" || emailClean != "bob@example.com" || scheme != "legacy" || avatar != "" {
		t.Errorf("user 2 = %q %q %q %q", clean, emailClean, scheme, avatar)
	}
	if err := pool.QueryRow(ctx, `select coalesce(avatar_path, '') from users where id = 1`).Scan(&avatar); err != nil {
		t.Fatal(err)
	}
	if avatar != "avatars/1.png" {
		t.Errorf("avatar_path = %q", avatar)
	}

	// The signature of user 1 has a tree. The empty signature of user 2 has none.
	var typ *string
	if err := pool.QueryRow(ctx, `select jsonb_typeof(signature_tree) from users where id = 1`).Scan(&typ); err != nil {
		t.Fatal(err)
	}
	if typ == nil || *typ != "object" {
		t.Errorf("signature_tree of user 1 = %v, want an object", typ)
	}
	if err := pool.QueryRow(ctx, `select jsonb_typeof(signature_tree) from users where id = 2`).Scan(&typ); err != nil {
		t.Fatal(err)
	}
	if typ != nil {
		t.Errorf("signature_tree of user 2 = %q, want NULL", *typ)
	}
	var src string
	var md, sm, al bool
	if err := pool.QueryRow(ctx,
		`select signature_source, signature_markdown, signature_smilies, signature_autolink
		 from users where id = 1`).Scan(&src, &md, &sm, &al); err != nil {
		t.Fatal(err)
	}
	if src != "Example signature" || !md || !sm || !al {
		t.Errorf("signature = %q %v %v %v", src, md, sm, al)
	}

	// Static pages always have a tree.
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from static_pages where jsonb_typeof(body_tree) = 'object'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("%d static pages have a tree, want 2", n)
	}

	// A grant of an option keeps its setting. A role grant has no option.
	var setting int16
	if err := pool.QueryRow(ctx,
		`select setting from acl_user_grants where user_id = 4 and option = 'forum.read'`).Scan(&setting); err != nil {
		t.Fatal(err)
	}
	if setting != 2 {
		t.Errorf("never stored as %d, want 2", setting)
	}
	if err := pool.QueryRow(ctx,
		`select setting from acl_role_settings where role_id = 1 and option = 'topic.global'`).Scan(&setting); err != nil {
		t.Fatal(err)
	}
	if setting != 2 {
		t.Errorf("role setting never stored as %d, want 2", setting)
	}
	var boardWide int
	if err := pool.QueryRow(ctx,
		`select count(*) from acl_group_grants where forum_id = 0`).Scan(&boardWide); err != nil {
		t.Fatal(err)
	}
	if boardWide != 4 {
		t.Errorf("%d board-wide group grants, want 4", boardWide)
	}

	// Settings: the key is the JSON name and the value is its JSON.
	want := map[string]string{
		"board_name":           `"Example Board"`,
		"board_description":    `"A board for examples"`,
		"timezone":             `"America/New_York"`,
		"default_color_scheme": `"slate"`,
		"registration":         `"approval"`,
		"email_notifications":  `true`,
		"header_image":         `"header/banner.png"`,
	}
	rows, err := pool.Query(ctx, `select key, value::text from settings`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		got[k] = v
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("settings[%s] = %s, want %s", k, got[k], v)
		}
	}

	// The legacy route keeps its match object.
	var match string
	if err := pool.QueryRow(ctx, `select match::text from legacy_routes where path = 'old/thread.cgi'`).Scan(&match); err != nil {
		t.Fatal(err)
	}
	if match != `{"view": "full"}` {
		t.Errorf("match = %s", match)
	}

	// The next insert without an ID follows the largest imported ID.
	var next int64
	if err := pool.QueryRow(ctx,
		`insert into users (username, username_clean, email, email_clean, password_hash,
		                    password_scheme, state, registered_at)
		 values ('erin', 'erin', '', '', '', 'gobb', 'active', now()) returning id`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	// Users run 1 to 4 and gone user 8 holds ID 8, so the next ID is 9.
	if next != 9 {
		t.Errorf("next user ID = %d, want 9", next)
	}
	if err := pool.QueryRow(ctx,
		`insert into forums (kind, name) values ('forum', 'x') returning id`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next != 4 {
		t.Errorf("next forum ID = %d, want 4", next)
	}
}

func TestImportNullHeaderImage(t *testing.T) {
	pool := dbtest.New(t)
	dir := writeBoard(t, func(b *bundletest.Board) {
		b.Settings.HeaderImage = nil
		delete(b.Files, "header/banner.png")
	})
	counts := mustRun(t, pool, dir, t.TempDir(), false)
	var v string
	if err := pool.QueryRow(context.Background(), `select value::text from settings where key = 'header_image'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "null" {
		t.Errorf("header_image = %s, want null", v)
	}
	if counts["files"] != 6 {
		t.Errorf("counts[files] = %d, want 6", counts["files"])
	}
}

func TestImportCopiesFiles(t *testing.T) {
	pool := dbtest.New(t)
	b := bundletest.Small()
	dir := bundletest.Write(t, b)
	data := t.TempDir()
	mustRun(t, pool, dir, data, false)

	for p, content := range b.Files {
		got, err := os.ReadFile(filepath.Join(data, filepath.FromSlash(p)))
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if sha(got) != sha(content) {
			t.Errorf("%s: wrong content", p)
		}
	}
	noStaging(t, data)
	// The source bundle stays whole for a directory import.
	if _, err := os.Stat(filepath.Join(dir, "files", "avatars", "1.png")); err != nil {
		t.Errorf("the source file is gone: %v", err)
	}
}

func TestImportCreatesEmptyDirectories(t *testing.T) {
	pool := dbtest.New(t)
	dir := writeBoard(t, func(b *bundletest.Board) {
		b.Users[0].Avatar = nil
		delete(b.Files, "avatars/1.png")
	})
	data := t.TempDir()
	mustRun(t, pool, dir, data, false)
	for _, d := range dataDirs {
		st, err := os.Stat(filepath.Join(data, d))
		if err != nil || !st.IsDir() {
			t.Fatalf("%s: %v", d, err)
		}
		if st.Mode().Perm() != 0o755 {
			t.Errorf("%s mode = %v, want 0755", d, st.Mode().Perm())
		}
	}
	entries, err := os.ReadDir(filepath.Join(data, "avatars"))
	if err != nil || len(entries) != 0 {
		t.Errorf("avatars = %v, %v; want empty", entries, err)
	}
}

func TestImportFromArchive(t *testing.T) {
	pool := dbtest.New(t)
	b := bundletest.Small()
	dir := bundletest.Write(t, b)
	archive := filepath.Join(t.TempDir(), "board.tar.gz")
	if err := bundle.Pack(dir, archive); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	counts := mustRun(t, pool, archive, data, false)

	direct := dbtest.New(t)
	want := mustRun(t, direct, dir, t.TempDir(), false)
	if len(counts) != len(want) {
		t.Errorf("counts = %v, want %v", counts, want)
	}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("counts[%s] = %d, want %d", k, counts[k], v)
		}
	}
	for p, content := range b.Files {
		got, err := os.ReadFile(filepath.Join(data, filepath.FromSlash(p)))
		if err != nil || sha(got) != sha(content) {
			t.Errorf("%s: %v", p, err)
		}
	}
	noStaging(t, data)
	if n := count(t, pool, "users"); n != 4 {
		t.Errorf("users = %d, want 4", n)
	}
}

func TestImportRefusesNonEmpty(t *testing.T) {
	pool := dbtest.New(t)
	dir := writeBoard(t, nil)
	data := t.TempDir()
	mustRun(t, pool, dir, data, false)

	before := count(t, pool, "users")
	_, err := run(t, pool, dir, data, false)
	if err == nil {
		t.Fatal("second import succeeded")
	}
	if !strings.Contains(err.Error(), "acl_group_grants") {
		t.Errorf("error does not name a table: %v", err)
	}
	if n := count(t, pool, "users"); n != before {
		t.Errorf("users = %d, want %d", n, before)
	}
	if _, err := os.Stat(filepath.Join(data, "avatars", "1.png")); err != nil {
		t.Error(err)
	}
	noStaging(t, data)
	entries, _ := filepath.Glob(filepath.Join(data, ".replaced-*"))
	if len(entries) > 0 {
		t.Errorf("a refused import moved files: %v", entries)
	}
}

func TestImportRefusesNonEmptyDataDir(t *testing.T) {
	pool := dbtest.New(t)
	data := t.TempDir()
	if err := os.MkdirAll(filepath.Join(data, "avatars"), 0o755); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(data, "avatars", "stray.png")
	if err := os.WriteFile(stray, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := run(t, pool, writeBoard(t, nil), data, false)
	if err == nil || !strings.Contains(err.Error(), "avatars") {
		t.Fatalf("error = %v, want one that names avatars", err)
	}
	if n := count(t, pool, "users"); n != 0 {
		t.Errorf("users = %d, want 0", n)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Error(err)
	}
	noStaging(t, data)
}

func TestImportReplaceKeepsOldFiles(t *testing.T) {
	pool := dbtest.New(t)
	data := t.TempDir()
	mustRun(t, pool, writeBoard(t, nil), data, false)

	newAvatar := []byte("a different avatar")
	second := writeBoard(t, func(b *bundletest.Board) {
		b.Settings.BoardName = "Second Board"
		delete(b.Files, "avatars/1.png")
		b.Files["avatars/new.png"] = newAvatar
		ref := bundletest.Ref("avatars/new.png", newAvatar)
		b.Users[0].Avatar = &ref
		b.Users = b.Users[:3]
		b.Users[2].Username = "carol2"
		b.GroupMembers = slices.DeleteFunc(b.GroupMembers, func(m bundle.GroupMember) bool { return m.UserID == 4 })
		b.Grants = b.Grants[:16]
		b.Bans = b.Bans[1:]
	})
	mustRun(t, pool, second, data, true)

	sameIDs(t, "users", ids(t, pool, "select id from users order by id"), 1, 2, 3)
	var name string
	if err := pool.QueryRow(context.Background(), `select value #>> '{}' from settings where key = 'board_name'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Second Board" {
		t.Errorf("board_name = %q", name)
	}
	if n := count(t, pool, "bans"); n != 2 {
		t.Errorf("bans = %d, want 2", n)
	}
	// restart identity: the bans of the second bundle start at 1.
	sameIDs(t, "ban ids", ids(t, pool, "select id from bans order by id"), 1, 2)

	if _, err := os.Stat(filepath.Join(data, "avatars", "1.png")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the old avatar is still in place: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(data, "avatars", "new.png"))
	if err != nil || string(got) != string(newAvatar) {
		t.Errorf("new avatar: %q, %v", got, err)
	}
	old, err := filepath.Glob(filepath.Join(data, ".replaced-*", "avatars", "1.png"))
	if err != nil || len(old) != 1 {
		t.Fatalf("old avatar under .replaced-*: %v, %v", old, err)
	}
	stamp := filepath.Base(filepath.Dir(filepath.Dir(old[0])))
	if len(stamp) != len(".replaced-20061023T150405Z") || !strings.HasSuffix(stamp, "Z") {
		t.Errorf("replaced directory name = %q", stamp)
	}
	noStaging(t, data)
}

func TestImportStopsOnCheck(t *testing.T) {
	pool := dbtest.New(t)
	data := t.TempDir()
	dir := writeBoard(t, func(b *bundletest.Board) {
		b.Users[0].RankID = new(int64)
		*b.Users[0].RankID = 99
	})
	_, err := run(t, pool, dir, data, false)
	if err == nil {
		t.Fatal("import succeeded")
	}
	if !strings.Contains(err.Error(), "users.jsonl:1: rank_id") {
		t.Errorf("error is not the message of Check: %v", err)
	}
	for _, table := range []string{"users", "forums", "settings"} {
		if n := count(t, pool, table); n != 0 {
			t.Errorf("%s has %d rows", table, n)
		}
	}
	noStaging(t, data)
	for _, d := range dataDirs {
		if _, err := os.Stat(filepath.Join(data, d)); err == nil {
			t.Errorf("%s exists", d)
		}
	}
}

func TestImportRefusesChangedFile(t *testing.T) {
	pool := dbtest.New(t)
	data := t.TempDir()
	dir := writeBoard(t, nil)
	// The file has the size that the row states, but not its SHA-256. The
	// importer would copy it without complaint, so only Check can refuse the
	// import here. The test proves that Check catches the changed file, and that
	// its message names the path.
	if err := os.WriteFile(filepath.Join(dir, "files", "avatars", "1.png"),
		make([]byte, len(bundletest.Small().Files["avatars/1.png"])), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := run(t, pool, dir, data, false)
	if err == nil || !strings.Contains(err.Error(), "avatars/1.png") {
		t.Fatalf("error = %v", err)
	}
	noStaging(t, data)
}

func TestImportLockSerializes(t *testing.T) {
	first := dbtest.New(t)
	second := dbtest.Connect(t, first)
	data := t.TempDir()
	dir := writeBoard(t, nil)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, pool := range []*pgxpool.Pool{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = run(t, pool, dir, data, false)
		}()
	}
	wg.Wait()

	var ok, failed int
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		failed++
		if !strings.Contains(err.Error(), "has rows") {
			t.Errorf("the loser failed for another reason: %v", err)
		}
	}
	if ok != 1 || failed != 1 {
		t.Fatalf("results = %v, want one success and one failure", errs)
	}
	if n := count(t, first, "users"); n != 4 {
		t.Errorf("users = %d, want 4", n)
	}
	noStaging(t, data)
}

func TestImportNamesRowOnTreeError(t *testing.T) {
	pool := dbtest.New(t)
	data := t.TempDir()
	dir := writeBoard(t, func(b *bundletest.Board) {
		b.StaticPages[1].Body.Source = "BREAK"
	})
	old := parseBody
	t.Cleanup(func() { parseBody = old })
	parseBody = func(src string, opt markup.Options) *markup.Node {
		if src == "BREAK" {
			return &markup.Node{Kind: markup.Element, Name: "no-such-element"}
		}
		return old(src, opt)
	}
	_, err := run(t, pool, dir, data, false)
	if err == nil {
		t.Fatal("import succeeded")
	}
	if !strings.HasPrefix(err.Error(), "static_pages.jsonl:2 (slug rules): body: ") {
		t.Errorf("error = %v", err)
	}
	// The users loaded before the failure are rolled back.
	if n := count(t, pool, "users"); n != 0 {
		t.Errorf("users = %d, want 0", n)
	}
	noStaging(t, data)
}

func TestImportParsesWithSmileyList(t *testing.T) {
	pool := dbtest.New(t)
	dir := writeBoard(t, func(b *bundletest.Board) {
		b.Users[0].Signature = bundle.Body{Source: "hi :)", Markdown: true, Smilies: true}
		b.Users[1].Signature = bundle.Body{Source: "plain :)", Markdown: true}
	})
	type call struct {
		src string
		opt markup.Options
	}
	var calls []call
	old := parseBody
	t.Cleanup(func() { parseBody = old })
	parseBody = func(src string, opt markup.Options) *markup.Node {
		calls = append(calls, call{src, opt})
		return old(src, opt)
	}
	mustRun(t, pool, dir, t.TempDir(), false)

	var sawOn, sawOff bool
	for _, c := range calls {
		switch c.src {
		case "hi :)":
			sawOn = true
			if !c.opt.Smilies || !c.opt.Markdown || c.opt.Autolink || len(c.opt.SmileyList) != 2 {
				t.Errorf("options = %+v", c.opt)
			}
			codes := []string{c.opt.SmileyList[0].Code, c.opt.SmileyList[1].Code}
			if codes[0] != ":)" || codes[1] != ":D" {
				t.Errorf("codes = %v", codes)
			}
		case "plain :)":
			sawOff = true
			if c.opt.Smilies {
				t.Errorf("options = %+v", c.opt)
			}
		}
	}
	if !sawOn || !sawOff {
		t.Errorf("calls = %v", calls)
	}
	var n int
	if err := pool.QueryRow(context.Background(),
		`select count(*) from users where id = 1 and signature_tree::text like '%smiley%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("the signature of user 1 has no smiley element")
	}
}

func TestSettingsRowsFollowTheStruct(t *testing.T) {
	rows, err := settingRows(bundle.Settings{BoardName: "x", Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, r := range rows {
		keys = append(keys, r.key)
		if !json.Valid([]byte(r.value)) {
			t.Errorf("%s = %s is not JSON", r.key, r.value)
		}
	}
	if len(keys) != 7 {
		t.Errorf("keys = %v", keys)
	}
}

// TestCopyVerifiedRefusesChangedFile covers a file that changes after Check read
// it: the copy hashes what it wrote and refuses a mismatch.
func TestCopyVerifiedRefusesChangedFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	content := []byte("some bytes")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	want := bundletest.Ref("avatars/x.png", content)

	if err := copyVerified(src, filepath.Join(dir, "ok"), want); err != nil {
		t.Fatalf("a matching file: %v", err)
	}
	bad := want
	bad.SHA256 = strings.Repeat("0", 64)
	if err := copyVerified(src, filepath.Join(dir, "bad-sha"), bad); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Errorf("wrong hash: %v", err)
	}
	bad = want
	bad.Size++
	if err := copyVerified(src, filepath.Join(dir, "bad-size"), bad); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Errorf("wrong size: %v", err)
	}
	if err := os.Symlink(src, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := copyVerified(filepath.Join(dir, "link"), filepath.Join(dir, "from-link"), want); err == nil {
		t.Error("a symlink was copied")
	}
}
