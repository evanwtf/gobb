package legacy_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/legacy"
)

// loadSmall loads the table of bundletest.Small, after run has added rows.
func loadSmall(t *testing.T, run func(pool *pgxpool.Pool)) *legacy.Table {
	t.Helper()
	pool, _ := boardtest.Load(t, bundletest.Small())
	if run != nil {
		run(pool)
	}
	tab, err := legacy.Load(context.Background(), pool)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return tab
}

// addRoute inserts one route row with an explicit ID.
func addRoute(t *testing.T, pool *pgxpool.Pool, id int64, path, match string, idParam *string, kind string, pos int) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
insert into legacy_routes (id, path, match, id_param, target_kind, position)
values ($1, $2, $3::jsonb, $4, $5, $6)`, id, path, match, idParam, kind, pos)
	if err != nil {
		t.Fatalf("insert route: %v", err)
	}
}

func q(raw string) url.Values {
	v, err := url.ParseQuery(raw)
	if err != nil {
		panic(err)
	}
	return v
}

func ptr[T any](v T) *T { return &v }

func TestMatch(t *testing.T) {
	tab := loadSmall(t, func(pool *pgxpool.Pool) {
		// Same path as route 1, no match pairs, a different ID parameter, and a
		// lower position.
		addRoute(t, pool, 10, "old/thread.cgi", `{}`, ptr("p"), "post", 0)
		// Two routes that apply together with equal positions. The higher row ID
		// goes in first, so insertion order does not decide.
		addRoute(t, pool, 21, "old/tie.cgi", `{}`, nil, "members", 5)
		addRoute(t, pool, 20, "old/tie.cgi", `{}`, nil, "team", 5)
		// A match pair with an empty value.
		addRoute(t, pool, 30, "old/empty.cgi", `{"x":""}`, nil, "search", 0)
	})

	tests := []struct {
		name     string
		path     string
		query    string
		wantKind string // "" means no match
		wantID   string // the IDParam of the route, when it matters
	}{
		{"route 1", "old/thread.cgi", "view=full&id=1", "topic", "id"},
		{"other view value", "old/thread.cgi", "view=brief&id=1", "", ""},
		{"no ID parameter", "old/thread.cgi", "view=full", "", ""},
		{"lower position wins", "old/thread.cgi", "view=full&id=1&p=4", "post", "p"},
		{"the post route alone", "old/thread.cgi", "p=4", "post", "p"},
		{"equal positions: lower row ID", "old/tie.cgi", "", "team", ""},
		{"first value of a repeated match parameter", "old/thread.cgi", "view=brief&view=full&id=1", "", ""},
		{"first value counts, first is good", "old/thread.cgi", "view=full&view=brief&id=1", "topic", "id"},
		{"an empty ID value still counts as the parameter", "old/thread.cgi", "view=full&id=", "topic", "id"},
		{"a bare key is the parameter with an empty value", "old/thread.cgi", "view=full&id", "topic", "id"},
		{"empty match value, parameter present", "old/empty.cgi", "x=", "search", ""},
		{"empty match value, parameter absent", "old/empty.cgi", "", "", ""},
		{"empty match value, other value", "old/empty.cgi", "x=1", "", ""},
		{"a trailing slash is another path", "old/thread.cgi/", "view=full&id=1", "", ""},
		{"a leading slash is another path", "/old/thread.cgi", "view=full&id=1", "", ""},
		{"case counts", "Old/Thread.cgi", "view=full&id=1", "", ""},
		{"an unknown path", "old/none.cgi", "", "", ""},
		{"the empty path", "", "view=full&id=1", "", ""},
		{"no-id route 2", "old/profile.cgi", "anything=1", "user", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, ok := tab.Match(tc.path, q(tc.query))
			if tc.wantKind == "" {
				if ok {
					t.Fatalf("Match(%q, %q) = %+v, want no match", tc.path, tc.query, r)
				}
				return
			}
			if !ok {
				t.Fatalf("Match(%q, %q): no match, want kind %s", tc.path, tc.query, tc.wantKind)
			}
			if r.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", r.Kind, tc.wantKind)
			}
			if got := deref(r.IDParam); got != tc.wantID {
				t.Errorf("IDParam = %q, want %q", got, tc.wantID)
			}
		})
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// The fields of a route come back as the bundle wrote them.
func TestMatchFields(t *testing.T) {
	tab := loadSmall(t, nil)
	r, ok := tab.Match("old/thread.cgi", q("view=full&id=1"))
	if !ok {
		t.Fatal("no match")
	}
	if r.Path != "old/thread.cgi" || r.Match["view"] != "full" || deref(r.IDParam) != "id" ||
		deref(r.OffsetParam) != "page" || r.Kind != "topic" || r.Position != 1 || r.TargetID != nil || r.TargetSlug != nil {
		t.Errorf("route = %+v", r)
	}
	r, ok = tab.Match("old/profile.cgi", nil)
	if !ok || r.TargetID == nil || *r.TargetID != 8 {
		t.Errorf("profile route = %+v, %v", r, ok)
	}
	r, ok = tab.Match("old/about.cgi", nil)
	if !ok || r.TargetSlug == nil || *r.TargetSlug != "about" {
		t.Errorf("about route = %+v, %v", r, ok)
	}
}

func TestFile(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	for _, f := range [][2]string{
		{"old/images/", "ranks"},
		{"old/images/ranks/", "ranks"},
		{"old/images/smilies/big/", "smilies"},
	} {
		if _, err := pool.Exec(context.Background(), `insert into legacy_files (prefix, dir) values ($1, $2)`, f[0], f[1]); err != nil {
			t.Fatal(err)
		}
	}
	tab, err := legacy.Load(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path, dir, rest string
		ok              bool
	}{
		{"old/images/smilies/smile.gif", "smilies", "smile.gif", true},
		{"old/images/smilies/big/grin.png", "smilies", "grin.png", true},
		{"old/images/smilies/small/grin.png", "smilies", "small/grin.png", true},
		{"old/images/ranks/star.png", "ranks", "star.png", true},
		{"old/images/logo.png", "ranks", "logo.png", true},
		{"old/images/other/deep/x.gif", "ranks", "other/deep/x.gif", true},
		{"old/other/x.gif", "", "", false},
		{"old/images/smilies/", "", "", false},
		{"old/images/smilies", "ranks", "smilies", true},
		{"old/images/", "", "", false},
		{"/old/images/smilies/smile.gif", "", "", false},
		{"", "", "", false},
		{"old/images/smilies/" + strings.Repeat("a/", 5000) + "x.gif", "smilies", strings.Repeat("a/", 5000) + "x.gif", true},
	}
	for _, tc := range tests {
		dir, rest, ok := tab.File(tc.path)
		if dir != tc.dir || rest != tc.rest || ok != tc.ok {
			t.Errorf("File(%.60q) = %q, %.20q, %v; want %q, %.20q, %v", tc.path, dir, rest, ok, tc.dir, tc.rest, tc.ok)
		}
	}
}

// Match and File do not scan the table: a table of 10,000 files and routes
// answers in a bounded number of steps, and the answers stay right.
func TestLargeTable(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
insert into legacy_files (prefix, dir)
select 'big/' || n || '/', 'smilies' from generate_series(1, 10000) n`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
insert into legacy_routes (path, match, target_kind, position)
select 'r/' || n, '{}', 'home', n from generate_series(1, 10000) n`); err != nil {
		t.Fatal(err)
	}
	tab, err := legacy.Load(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if dir, rest, ok := tab.File("big/9999/x.gif"); !ok || dir != "smilies" || rest != "x.gif" {
		t.Errorf("File = %q %q %v", dir, rest, ok)
	}
	if _, ok := tab.Match("r/10000", nil); !ok {
		t.Error("no match for r/10000")
	}
	allocs := testing.AllocsPerRun(100, func() {
		tab.File("big/9999/x.gif")
		tab.Match("r/10000", nil)
	})
	if allocs > 10 {
		t.Errorf("%v allocations per lookup", allocs)
	}
	// A long path costs a bounded number of probes, not one per element.
	long := "big/" + strings.Repeat("1/", 100000) + "x.gif"
	if dir, rest, ok := tab.File(long); !ok || dir != "smilies" || len(rest) != len(long)-len("big/1/") {
		t.Errorf("a long path: %q %d %v", dir, len(rest), ok)
	}
	if got := testing.AllocsPerRun(10, func() { tab.File(long) }); got > 10 {
		t.Errorf("%v allocations for a long path", got)
	}
}

// Load logs one INFO line with the counts, and Match and File log nothing.
func TestLoadLogs(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	tab, err := legacy.Load(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("Load logged %d lines, want 1:\n%s", len(lines), buf.String())
	}
	for _, want := range []string{"level=INFO", "routes=3", "files=1"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("log line %q lacks %q", lines[0], want)
		}
	}
	buf.Reset()
	tab.Match("old/thread.cgi", q("view=full&id=1"))
	tab.File("old/images/smilies/smile.gif")
	if buf.Len() != 0 {
		t.Errorf("a lookup logged %q", buf.String())
	}
}

func TestRoutes(t *testing.T) {
	tab := loadSmall(t, nil)
	rs := tab.Routes()
	if len(rs) != 3 {
		t.Fatalf("Routes = %d, want 3", len(rs))
	}
	if fs := tab.Prefixes(); len(fs) != 1 || fs[0] != "old/images/smilies/" {
		t.Errorf("Prefixes = %v", fs)
	}
}
