package importer

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/dbtest"
	"github.com/evanwtf/gobb/markup"
)

// utc returns a UTC time. Each value in the column tests is a distinct time.
func utc(year int, month time.Month, day, hour, min, sec int) time.Time {
	return time.Date(year, month, day, hour, min, sec, 0, time.UTC)
}

func ptr[T any](v T) *T { return &v }

// sameTime reports a fault when got is not the instant want.
func sameTime(t *testing.T, what string, got *time.Time, want time.Time) {
	t.Helper()
	if got == nil || !got.Equal(want) {
		t.Errorf("%s = %v, want %s", what, got, want)
	}
}

// TestImportKeepsEveryColumn changes a copy of the small board so that each
// value under test differs from its column default and from the values beside
// it. A column that the importer drops or swaps then fails.
func TestImportKeepsEveryColumn(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	small := bundletest.Small()
	regAt, visitAt, markAt := utc(2018, 3, 4, 5, 6, 7), utc(2021, 4, 5, 6, 7, 8), utc(2022, 5, 6, 7, 8, 9)
	editedAt, endsAt := utc(2020, 1, 17, 11, 12, 13), utc(2031, 12, 31, 23, 59, 58)
	dir := writeBoard(t, func(b *bundletest.Board) {
		// carol: every user column differs from the default.
		c := &b.Users[2]
		c.State, c.Owner, c.EmailVerified = "inactive", true, false
		c.RegisteredAt, c.LastVisitAt, c.LastMarkAt = regAt, &visitAt, &markAt
		c.PostCount, c.RankID = 777, ptr(int64(1))
		c.Timezone, c.ColorScheme = "Asia/Tokyo", "slate-deep"

		// post 3
		p := &b.Posts[2]
		p.EditedAt, p.EditedBy, p.EditCount = &editedAt, ptr(int64(2)), 3
		p.EditReason, p.ShowSignature, p.Visibility = "fixed a link", true, "deleted"

		// topic 2 has posts. Topic 3 is the moved-topic marker.
		b.Topics[1].Kind, b.Topics[1].Locked = "announce", true
		b.Topics[1].ViewCount, b.Topics[1].AuthorID = 1234, ptr(int64(3))
		b.Topics[2].Kind, b.Topics[2].Locked = "sticky", false
		b.Topics[2].ViewCount, b.Topics[2].AuthorID = 55, ptr(int64(3))

		a := &b.Attachments[0]
		a.Width, a.Height, a.Name, a.Comment, a.Position = ptr(640), ptr(480), "renamed.png", "a comment", 3

		b.Polls[0].EndsAt, b.Polls[0].Question = &endsAt, "Pick one"

		b.PMs[1].Subject, b.PMs[1].AuthorIP = "A different subject", ptr("2001:db8::99")
	})
	mustRun(t, pool, dir, t.TempDir(), false)

	t.Run("users", func(t *testing.T) {
		var hash, state string
		var owner, verified bool
		var reg time.Time
		var visit, mark *time.Time
		var posts int
		var rank *int64
		var tz, scheme string
		err := pool.QueryRow(ctx,
			`select password_hash, state, is_owner, email_verified, registered_at, last_visit_at,
			        last_mark_at, post_count, rank_id, timezone, color_scheme
			 from users where id = 3`).
			Scan(&hash, &state, &owner, &verified, &reg, &visit, &mark, &posts, &rank, &tz, &scheme)
		if err != nil {
			t.Fatal(err)
		}
		if hash != small.Users[2].Password.Hash || state != "inactive" || !owner || verified {
			t.Errorf("user 3 = %q %q owner=%v verified=%v", hash, state, owner, verified)
		}
		if !reg.Equal(regAt) {
			t.Errorf("registered_at = %s, want %s", reg, regAt)
		}
		sameTime(t, "last_visit_at", visit, visitAt)
		sameTime(t, "last_mark_at", mark, markAt)
		if posts != 777 || rank == nil || *rank != 1 || tz != "Asia/Tokyo" || scheme != "slate-deep" {
			t.Errorf("user 3 = %d %v %q %q", posts, rank, tz, scheme)
		}

		// Bob: verified and not an owner, the reverse of carol. Alice: both.
		for _, c := range []struct {
			id              int64
			owner, verified bool
			state           string
		}{{1, true, true, "active"}, {2, false, true, "active"}} {
			if err := pool.QueryRow(ctx,
				`select is_owner, email_verified, state from users where id = $1`, c.id).
				Scan(&owner, &verified, &state); err != nil {
				t.Fatal(err)
			}
			if owner != c.owner || verified != c.verified || state != c.state {
				t.Errorf("user %d = owner %v, verified %v, %s", c.id, owner, verified, state)
			}
		}
		// Version 2 has no notify_on_post. The column keeps its default.
		var allNotify bool
		if err := pool.QueryRow(ctx, `select bool_and(notify_on_post) from users`).Scan(&allNotify); err != nil || !allNotify {
			t.Errorf("notify_on_post of every user = %v, %v; want the default true", allNotify, err)
		}
	})

	t.Run("posts", func(t *testing.T) {
		var edited *time.Time
		var by *int64
		var count int
		var reason, vis string
		var sig bool
		if err := pool.QueryRow(ctx,
			`select edited_at, edited_by, edit_count, edit_reason, enable_sig, visibility
			 from posts where id = 3`).Scan(&edited, &by, &count, &reason, &sig, &vis); err != nil {
			t.Fatal(err)
		}
		sameTime(t, "edited_at", edited, editedAt)
		if by == nil || *by != 2 || count != 3 || reason != "fixed a link" || !sig || vis != "deleted" {
			t.Errorf("post 3 = %v %d %q %v %q", by, count, reason, sig, vis)
		}
		// Post 1 has enable_sig true in the bundle, and post 2 has it false.
		var sig1, sig2 bool
		if err := pool.QueryRow(ctx, `select enable_sig from posts where id = 1`).Scan(&sig1); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `select enable_sig from posts where id = 2`).Scan(&sig2); err != nil {
			t.Fatal(err)
		}
		if !sig1 || sig2 {
			t.Errorf("enable_sig of posts 1 and 2 = %v and %v, want true and false", sig1, sig2)
		}
	})

	t.Run("topics", func(t *testing.T) {
		for _, c := range []struct {
			id      int64
			kind    string
			locked  bool
			views   int
			author  int64
			movedTo int64 // 0 means NULL
		}{
			{2, "announce", true, 1234, 3, 0},
			{3, "sticky", false, 55, 3, 1},
		} {
			var kind string
			var locked bool
			var views int
			var author, moved *int64
			if err := pool.QueryRow(ctx,
				`select kind, locked, view_count, author_id, moved_to_topic_id from topics where id = $1`, c.id).
				Scan(&kind, &locked, &views, &author, &moved); err != nil {
				t.Fatal(err)
			}
			if kind != c.kind || locked != c.locked || views != c.views || author == nil || *author != c.author {
				t.Errorf("topic %d = %q %v %d %v", c.id, kind, locked, views, author)
			}
			if (c.movedTo == 0) != (moved == nil) || (moved != nil && *moved != c.movedTo) {
				t.Errorf("topic %d moved_to_topic_id = %v, want %d", c.id, moved, c.movedTo)
			}
		}
	})

	t.Run("attachments", func(t *testing.T) {
		var w, h *int
		var name, comment string
		var pos int
		if err := pool.QueryRow(ctx,
			`select width, height, real_name, comment, position from attachments where id = 1`).
			Scan(&w, &h, &name, &comment, &pos); err != nil {
			t.Fatal(err)
		}
		if w == nil || *w != 640 || h == nil || *h != 480 || name != "renamed.png" || comment != "a comment" || pos != 3 {
			t.Errorf("attachment 1 = %v %v %q %q %d", w, h, name, comment, pos)
		}
	})

	t.Run("polls", func(t *testing.T) {
		var ends *time.Time
		var question string
		if err := pool.QueryRow(ctx, `select ends_at, question from polls where topic_id = 2`).Scan(&ends, &question); err != nil {
			t.Fatal(err)
		}
		sameTime(t, "ends_at", ends, endsAt)
		if question != "Pick one" {
			t.Errorf("question = %q", question)
		}
	})

	t.Run("pms", func(t *testing.T) {
		for _, c := range []struct {
			id      int64
			subject string
			ip      string
			name    string
		}{
			{1, "Welcome aboard", "10.0.0.5", "alice"},
			{2, "A different subject", "2001:db8::99", "bob"},
		} {
			var subject, ip, name string
			if err := pool.QueryRow(ctx,
				`select subject, host(author_ip), author_name from pms where id = $1`, c.id).
				Scan(&subject, &ip, &name); err != nil {
				t.Fatal(err)
			}
			if subject != c.subject || ip != c.ip || name != c.name {
				t.Errorf("pm %d = %q %q %q", c.id, subject, ip, name)
			}
		}
	})
}

// TestImportStaticPageFlags proves that a static page keeps its three body flags.
func TestImportStaticPageFlags(t *testing.T) {
	pool := dbtest.New(t)
	dir := writeBoard(t, func(b *bundletest.Board) {
		b.StaticPages[0].Body.Markdown, b.StaticPages[0].Body.Smilies, b.StaticPages[0].Body.Autolink = false, true, false
		b.StaticPages[1].Body.Markdown, b.StaticPages[1].Body.Smilies, b.StaticPages[1].Body.Autolink = true, false, true
	})
	mustRun(t, pool, dir, t.TempDir(), false)
	for slug, want := range map[string][3]bool{
		"about": {false, true, false},
		"rules": {true, false, true},
	} {
		var md, sm, al bool
		if err := pool.QueryRow(context.Background(),
			`select enable_markdown, enable_smilies, enable_autolink from static_pages where slug = $1`, slug).
			Scan(&md, &sm, &al); err != nil {
			t.Fatal(err)
		}
		if got := [3]bool{md, sm, al}; got != want {
			t.Errorf("page %s flags = %v, want %v", slug, got, want)
		}
	}
}

// TestImportKeepsGoneUserIDsOut proves that a member who joins after an import
// never gets the ID of a gone user. Small has users 1 to 4 and gone user 8.
func TestImportKeepsGoneUserIDsOut(t *testing.T) {
	pool := dbtest.New(t)
	mustRun(t, pool, writeBoard(t, nil), t.TempDir(), false)
	for _, want := range []int64{9, 10} {
		var id int64
		if err := pool.QueryRow(context.Background(),
			`insert into users (username, username_clean, email, email_clean, password_hash,
			                    password_scheme, state, registered_at)
			 values ('u' || $1::text, 'u' || $1::text, '', '', '', 'gobb', 'active', now()) returning id`, strconv.FormatInt(want, 10)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if id != want {
			t.Errorf("new user ID = %d, want %d", id, want)
		}
	}
}

// TestImportGoneUserBelowTheUsers covers a gone user whose ID is below the
// largest user ID: the next ID follows the users.
func TestImportGoneUserBelowTheUsers(t *testing.T) {
	pool := dbtest.New(t)
	dir := writeBoard(t, func(b *bundletest.Board) {
		b.Users[3].ID = 20
		b.GroupMembers[5].UserID = 20
		b.Grants[16].UserID = ptr(int64(20))
		b.Bans[0].UserID = ptr(int64(20))
		b.GoneUsers[0].ID = 8
	})
	mustRun(t, pool, dir, t.TempDir(), false)
	var id int64
	if err := pool.QueryRow(context.Background(),
		`insert into users (username, username_clean, email, email_clean, password_hash,
		                    password_scheme, state, registered_at)
		 values ('x', 'x', '', '', '', 'gobb', 'active', now()) returning id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != 21 {
		t.Errorf("new user ID = %d, want 21", id)
	}
}

// TestImportGonePeople loads a private message whose author is gone, a message
// with no recipients, and an attachment whose uploader is gone.
func TestImportGonePeople(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	dir := writeBoard(t, func(b *bundletest.Board) {
		b.PMs[0].AuthorID, b.PMs[0].AuthorName = nil, "ghost"
		b.PMs[0].Recipients = b.PMs[0].Recipients[:1]
		b.PMs[1].Recipients = nil
		b.Attachments[0].UploaderID = nil
	})
	counts := mustRun(t, pool, dir, t.TempDir(), false)
	if counts["pms"] != 2 || counts["attachments"] != 2 {
		t.Errorf("counts = %v", counts)
	}
	var author *int64
	var name string
	if err := pool.QueryRow(ctx, `select author_id, author_name from pms where id = 1`).Scan(&author, &name); err != nil {
		t.Fatal(err)
	}
	if author != nil || name != "ghost" {
		t.Errorf("pm 1 = %v %q, want NULL and ghost", author, name)
	}
	if err := pool.QueryRow(ctx, `select author_id, author_name from pms where id = 2`).Scan(&author, &name); err != nil {
		t.Fatal(err)
	}
	if author == nil || *author != 2 || name != "bob" {
		t.Errorf("pm 2 = %v %q, want 2 and bob", author, name)
	}
	if n := count(t, pool, "pm_recipients"); n != 1 {
		t.Errorf("pm_recipients has %d rows, want 1", n)
	}
	var uploader *int64
	if err := pool.QueryRow(ctx, `select uploader_id from attachments where id = 1`).Scan(&uploader); err != nil {
		t.Fatal(err)
	}
	if uploader != nil {
		t.Errorf("uploader_id = %d, want NULL", *uploader)
	}
	if err := pool.QueryRow(ctx, `select uploader_id from attachments where id = 2`).Scan(&uploader); err != nil {
		t.Fatal(err)
	}
	if uploader == nil || *uploader != 1 {
		t.Errorf("uploader_id of attachment 2 = %v, want 1", uploader)
	}
}

// TestImportRefusesPMWithNobodyToSeeIt edits a written bundle so that message 1
// has no author ID and no recipient. The import stops with one error.
func TestImportRefusesPMWithNobodyToSeeIt(t *testing.T) {
	pool := dbtest.New(t)
	data := t.TempDir()
	dir := writeBoard(t, func(b *bundletest.Board) { b.PMs[0].AuthorID, b.PMs[0].AuthorName = nil, "ghost" })
	path := filepath.Join(dir, "pms.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := regexp.MustCompile(`"recipients":\[[^\]]*\]`).ReplaceAll(raw, []byte(`"recipients":[]`))
	if bytes.Equal(edited, raw) {
		t.Fatal("the edit changed nothing")
	}
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = run(t, pool, dir, data, false)
	var errs bundle.Errors
	if !errors.As(err, &errs) || len(errs) != 1 || !strings.HasPrefix(errs[0].Error(), "pms.jsonl:1: recipients: ") {
		t.Fatalf("error = %v, want one error on pms.jsonl:1 recipients", err)
	}
	if n := count(t, pool, "users"); n != 0 {
		t.Errorf("users = %d, want 0", n)
	}
	noStaging(t, data)
}

// breakOn makes parseBody return an invalid tree for the source src. It returns
// the function that puts the old parser back.
func breakOn(src string) (restore func()) {
	old := parseBody
	parseBody = func(s string, opt markup.Options) *markup.Node {
		if s == src {
			return &markup.Node{Kind: markup.Element, Name: "no-such-element"}
		}
		return old(s, opt)
	}
	return func() { parseBody = old }
}

// logRecords captures what slog writes while a test runs.
type logRecords struct {
	slog.Handler
	records []slog.Record
}

func (l *logRecords) Handle(_ context.Context, r slog.Record) error {
	l.records = append(l.records, r)
	return nil
}

func (l *logRecords) Enabled(context.Context, slog.Level) bool { return true }

// TestImportLogsWhichBundle checks the "import done" line: it names the
// manifest's source and created_at.
func TestImportLogsWhichBundle(t *testing.T) {
	pool := dbtest.New(t)
	dir := writeBoard(t, func(b *bundletest.Board) { b.Source = "a made-up source system" })
	r, err := bundle.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	logs := &logRecords{}
	old := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(old) })
	mustRun(t, pool, dir, t.TempDir(), false)

	var done *slog.Record
	for i := range logs.records {
		if logs.records[i].Message == "import done" {
			done = &logs.records[i]
		}
	}
	if done == nil {
		t.Fatal("no import done line")
	}
	attrs := map[string]slog.Value{}
	done.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value; return true })
	if v, ok := attrs["source"]; !ok || v.String() != "a made-up source system" {
		t.Errorf("source = %v", attrs["source"])
	}
	v, ok := attrs["created_at"]
	if !ok {
		t.Fatalf("no created_at attribute: %v", attrs)
	}
	if v.Kind() != slog.KindTime || !v.Time().Equal(r.Manifest().CreatedAt) {
		t.Errorf("created_at = %v, want %s", v, r.Manifest().CreatedAt)
	}
}

// TestBulkFlushesAtTheByteLimit lowers the byte limit. A batch then goes out
// early, and every row still loads.
func TestBulkFlushesAtTheByteLimit(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	old := maxBatchBytes
	t.Cleanup(func() { maxBatchBytes = old })

	load := func(limit int) (sent, rows int) {
		maxBatchBytes = limit
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "truncate smilies"); err != nil {
			t.Fatal(err)
		}
		b := newBulk(tx, "smilies", "smilies", "code", "image_path", "description", "position")
		text := strings.Repeat("x", 300)
		for i := 1; i <= 10; i++ {
			if err := b.add(ctx, i, text[:10]+string(rune('A'+i)), "smilies/x.png", text, i); err != nil {
				t.Fatal(err)
			}
		}
		before := b.sent
		if err := b.flush(ctx); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(ctx, "select count(*) from smilies").Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return before, rows
	}
	// Each row holds about 330 string bytes. At 1,000 bytes a batch goes out
	// every 4 rows: after rows 4 and 8.
	if sent, rows := load(1000); sent != 2 || rows != 10 {
		t.Errorf("limit 1000: %d batches sent before the last flush and %d rows loaded; want 2 and 10", sent, rows)
	}
	// The default limit holds all 10 rows in one batch.
	if sent, rows := load(8 << 20); sent != 0 || rows != 10 {
		t.Errorf("default limit: %d batches sent before the last flush and %d rows loaded; want 0 and 10", sent, rows)
	}
}

// TestImportWithSmallByteLimit imports a board with a low byte limit. Every
// batch is small, and every row loads.
func TestImportWithSmallByteLimit(t *testing.T) {
	pool := dbtest.New(t)
	old := maxBatchBytes
	t.Cleanup(func() { maxBatchBytes = old })
	maxBatchBytes = 20 << 10
	dir := bundletest.Write(t, bundletest.Large(3, 120)) // 360 posts of about 1.5 KiB each
	counts := mustRun(t, pool, dir, t.TempDir(), false)
	if counts["posts"] != 360 || count(t, pool, "posts") != 360 {
		t.Errorf("posts = %d / %d, want 360", counts["posts"], count(t, pool, "posts"))
	}
	ids := ids(t, pool, "select id from posts order by id")
	if len(ids) != 360 || ids[0] != 1 || ids[359] != 360 {
		t.Errorf("post IDs are %d long, first %v", len(ids), ids[:1])
	}
}

// TestImportFailsAfterCommit makes the move of the files fail after the commit.
// The rows stay, the old files stay, and the staging directory stays. A second
// run with Replace repairs the data directory.
func TestImportFailsAfterCommit(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	data := t.TempDir()
	first := bundletest.Small()
	mustRun(t, pool, bundletest.Write(t, first), data, false)

	newAvatar := []byte("a different avatar")
	second := bundletest.Small()
	second.Settings.BoardName = "Second Board"
	delete(second.Files, "avatars/1.png")
	second.Files["avatars/new.png"] = newAvatar
	ref := bundletest.Ref("avatars/new.png", newAvatar)
	second.Users[0].Avatar = &ref
	secondDir := bundletest.Write(t, second)

	// A regular file where the importer wants to make .replaced-<stamp>.
	failAt := utc(2030, 1, 2, 3, 4, 5)
	blocker := filepath.Join(data, ".replaced-"+failAt.Format(replacedName))
	if err := os.WriteFile(blocker, []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldNow := now
	t.Cleanup(func() { now = oldNow })
	now = func() time.Time { return failAt }

	_, err := run(t, pool, secondDir, data, true)
	if err == nil {
		t.Fatal("the import succeeded with the move blocked")
	}
	staged, _ := filepath.Glob(filepath.Join(data, ".import-*"))
	if len(staged) != 1 {
		t.Fatalf("staging directories = %v, want one", staged)
	}
	if !strings.Contains(err.Error(), staged[0]) || !strings.Contains(err.Error(), "--replace") ||
		!strings.Contains(err.Error(), "committed") {
		t.Errorf("error = %v\nwant it to name %s, --replace and the commit", err, staged[0])
	}
	// The rows are committed.
	var name string
	if err := pool.QueryRow(ctx, `select value #>> '{}' from settings where key = 'board_name'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Second Board" {
		t.Errorf("board_name = %q, want Second Board", name)
	}
	// The old files are intact.
	for p, content := range first.Files {
		got, err := os.ReadFile(filepath.Join(data, filepath.FromSlash(p)))
		if err != nil || string(got) != string(content) {
			t.Errorf("old file %s: %q, %v", p, got, err)
		}
	}
	// The staging directory holds the new files.
	if got, err := os.ReadFile(filepath.Join(staged[0], "files", "avatars", "new.png")); err != nil || string(got) != string(newAvatar) {
		t.Errorf("staged avatar: %q, %v", got, err)
	}

	// Run again with Replace. The failed run's staging directory stays.
	now = oldNow
	mustRun(t, pool, secondDir, data, true)
	for p, content := range second.Files {
		got, err := os.ReadFile(filepath.Join(data, filepath.FromSlash(p)))
		if err != nil || string(got) != string(content) {
			t.Errorf("file %s after the repair: %q, %v", p, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(data, "avatars", "1.png")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the old avatar is still in place: %v", err)
	}
	if _, err := os.Stat(staged[0]); err != nil {
		t.Errorf("the staging directory of the failed run is gone: %v", err)
	}
}

// TestImportIsAllOrNothing fails on post 1,501, after the first batch of 1,000
// posts went to the database. No table keeps a row.
func TestImportIsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	data := t.TempDir()
	b := bundletest.Large(3, 667) // 2,001 posts
	b.Posts[1500].Body.Source = "BREAK"
	dir := bundletest.Write(t, b)
	restore := breakOn("BREAK")
	t.Cleanup(restore)

	_, err := run(t, pool, dir, data, false)
	if err == nil || !strings.HasPrefix(err.Error(), "posts.jsonl:1501 (id 1501): body: ") {
		t.Fatalf("error = %v, want one that names posts.jsonl:1501", err)
	}
	tables, err := db.DataTables(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if n := count(t, pool, pgx.Identifier{table}.Sanitize()); n != 0 {
			t.Errorf("table %s has %d rows, want 0", table, n)
		}
	}
	entries, err := os.ReadDir(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("the data directory holds %s", e.Name())
	}
}

// TestImportVersion2Fields covers the columns that bundle format version 2 adds
// to users.
func TestImportVersion2Fields(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	mustRun(t, pool, writeBoard(t, nil), t.TempDir(), false)

	type row struct {
		format, location, occupation, interests, website string
		day, month, year                                 *int
	}
	read := func(name string) row {
		t.Helper()
		var r row
		err := pool.QueryRow(ctx,
			`select date_format, location, occupation, interests, website,
			        birth_day::int, birth_month::int, birth_year::int
			 from users where username = $1`, name).
			Scan(&r.format, &r.location, &r.occupation, &r.interests, &r.website,
				&r.day, &r.month, &r.year)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return r
	}

	alice := read("alice")
	if alice.location != "Lake Placid, NY" || alice.website != "https://example.com/alice" {
		t.Errorf("alice location, website = %q, %q", alice.location, alice.website)
	}
	if alice.day == nil || *alice.day != 14 || alice.month == nil || *alice.month != 3 ||
		alice.year == nil || *alice.year != 1984 {
		t.Errorf("alice birthday = %v/%v/%v, want 14/3/1984", alice.day, alice.month, alice.year)
	}
	if alice.format != "standard" {
		t.Errorf("alice date_format = %q, want standard", alice.format)
	}

	bob := read("bob")
	if bob.day == nil || *bob.day != 2 || bob.month == nil || *bob.month != 7 {
		t.Errorf("bob birthday = %v/%v, want 2/7", bob.day, bob.month)
	}
	if bob.year != nil {
		t.Errorf("bob birth_year = %d, want null", *bob.year)
	}

	if got := read("carol").format; got != "relative" {
		t.Errorf("carol date_format = %q, want relative", got)
	}
}

// TestImportNoWatches checks that an import leaves topic_watches and bookmarks
// empty: version 2 bundles carry neither.
func TestImportNoWatches(t *testing.T) {
	pool := dbtest.New(t)
	mustRun(t, pool, writeBoard(t, nil), t.TempDir(), false)
	for _, table := range []string{"topic_watches", "bookmarks"} {
		if n := count(t, pool, table); n != 0 {
			t.Errorf("%s has %d rows, want 0", table, n)
		}
	}
}

// TestMigrationChecks covers the constraints that migration 0012 adds to users.
func TestMigrationChecks(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	mustRun(t, pool, writeBoard(t, nil), t.TempDir(), false)

	for _, tc := range []struct{ name, set string }{
		{"year with no day", "birth_day = null, birth_month = null, birth_year = 1990"},
		{"day with no month", "birth_day = 5, birth_month = null, birth_year = null"},
		{"month with no day", "birth_day = null, birth_month = 5, birth_year = null"},
		{"day 0", "birth_day = 0, birth_month = 5"},
		{"day 32", "birth_day = 32, birth_month = 5"},
		{"month 13", "birth_day = 5, birth_month = 13"},
		{"year 1899", "birth_day = 5, birth_month = 5, birth_year = 1899"},
		{"unknown date format", "date_format = 'x'"},
		{"location too long", "location = repeat('a', 101)"},
		{"occupation too long", "occupation = repeat('a', 101)"},
		{"interests too long", "interests = repeat('a', 201)"},
		{"website too long", "website = repeat('a', 201)"},
	} {
		// The update is a literal from the test.
		_, err := pool.Exec(ctx, "update users set "+tc.set+" where username = 'carol'")
		if err == nil {
			t.Errorf("%s: update succeeded, want a check failure", tc.name)
		}
	}
	for _, set := range []string{
		"date_format = 'iso'",
		"location = repeat('a', 100), occupation = repeat('a', 100)",
		"interests = repeat('a', 200), website = repeat('a', 200)",
		"birth_day = 29, birth_month = 2, birth_year = 2000",
		"birth_day = null, birth_month = null, birth_year = null",
	} {
		if _, err := pool.Exec(ctx, "update users set "+set+" where username = 'carol'"); err != nil {
			t.Errorf("update %q: %v", set, err)
		}
	}
}
