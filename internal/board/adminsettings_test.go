package board_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// lastLog returns the newest row of mod_log.
func lastLog(t *testing.T, pool *pgxpool.Pool) (actor int64, action, targetType string, target int64, details string) {
	t.Helper()
	err := pool.QueryRow(context.Background(), `select coalesce(actor_id, 0), action, target_type, target_id, details::text from mod_log order by id desc limit 1`).
		Scan(&actor, &action, &targetType, &target, &details)
	if err != nil {
		t.Fatal(err)
	}
	return actor, action, targetType, target, details
}

func loadSettings(t *testing.T, pool *pgxpool.Pool) board.Settings {
	t.Helper()
	s, err := board.LoadSettings(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// current is the settings form with the values the small board has now.
func current(s board.Settings) board.SettingsChange {
	return board.SettingsChange{Name: s.BoardName, Description: s.BoardDescription, Timezone: s.Timezone, Scheme: s.DefaultScheme}
}

func TestUpdateSettings(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	alice := viewerFor(t, pool, 1)
	before := loadSettings(t, pool)
	rows := logCount(t, pool)

	// Only the keys that differ are stored and named. The text is trimmed.
	c := current(before)
	c.Name, c.Scheme = "  The New Name \t", "slate-deep"
	keys, err := board.UpdateSettings(ctx, pool, alice, c, t0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"board_name", "default_color_scheme"}; !slices.Equal(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	got := loadSettings(t, pool)
	want := before
	want.BoardName, want.DefaultScheme = "The New Name", "slate-deep"
	if got != want {
		t.Errorf("settings = %+v, want %+v", got, want)
	}
	actor, action, targetType, target, details := lastLog(t, pool)
	if logCount(t, pool) != rows+1 || actor != 1 || action != "settings.update" || targetType != "settings" || target != 0 ||
		details != `{"via": "web", "keys": ["board_name", "default_color_scheme"]}` {
		t.Errorf("log row = %d %s %s %d %s (rows %d, was %d)", actor, action, targetType, target, details, logCount(t, pool), rows)
	}

	// All four, in order. The description may be empty.
	keys, err = board.UpdateSettings(ctx, pool, alice, board.SettingsChange{Name: "Second", Description: "A line.", Timezone: "Asia/Tokyo", Scheme: "slate-light"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"board_name", "board_description", "timezone", "default_color_scheme"}; !slices.Equal(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	if _, err := board.UpdateSettings(ctx, pool, alice, board.SettingsChange{Name: "Second", Timezone: "Asia/Tokyo", Scheme: "slate-light"}, t0); err != nil {
		t.Fatal(err)
	}
	if s := loadSettings(t, pool); s.BoardDescription != "" {
		t.Errorf("description = %q, want empty", s.BoardDescription)
	}

	// The other settings stay as they were.
	if s := loadSettings(t, pool); s.Registration != before.Registration || s.EmailNotifications != before.EmailNotifications || s.HeaderImage != before.HeaderImage {
		t.Errorf("a save changed a key it does not own: %+v", s)
	}
}

func TestUpdateSettingsNoChange(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	alice := viewerFor(t, pool, 1)
	rows := logCount(t, pool)
	c := current(loadSettings(t, pool))
	// The same values, and the same values with spaces around the text.
	for _, in := range []board.SettingsChange{c, {Name: " " + c.Name + " ", Description: c.Description, Timezone: c.Timezone, Scheme: c.Scheme}} {
		keys, err := board.UpdateSettings(ctx, pool, alice, in, t0)
		if !errors.Is(err, board.ErrNoChange) || keys != nil {
			t.Errorf("same values: %v, %v; want ErrNoChange", keys, err)
		}
	}
	if logCount(t, pool) != rows {
		t.Error("a save that changed nothing wrote a row")
	}
}

func TestUpdateSettingsRefuses(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	alice := viewerFor(t, pool, 1)
	before := loadSettings(t, pool)
	rows := logCount(t, pool)
	cases := []struct {
		name  string
		edit  func(*board.SettingsChange)
		field string
		msg   string
	}{
		{"empty name", func(c *board.SettingsChange) { c.Name = "" }, "name", "Enter a name of 1 to 100 characters."},
		{"blank name", func(c *board.SettingsChange) { c.Name = " \t " }, "name", "Enter a name of 1 to 100 characters."},
		{"long name", func(c *board.SettingsChange) { c.Name = strings.Repeat("a", 101) }, "name", "Use at most 100 characters."},
		{"long name, wide characters", func(c *board.SettingsChange) { c.Name = strings.Repeat("é", 101) }, "name", "Use at most 100 characters."},
		{"line break in the name", func(c *board.SettingsChange) { c.Name = "Two\nlines" }, "name", "Use one line of plain text."},
		{"NUL in the name", func(c *board.SettingsChange) { c.Name = "a\x00b" }, "name", "Use one line of plain text."},
		{"line separator in the name", func(c *board.SettingsChange) { c.Name = "a\u2028b" }, "name", "Use one line of plain text."},
		{"bidi override in the name", func(c *board.SettingsChange) { c.Name = "a\u202eb" }, "name", "Remove the hidden formatting character."},
		{"name that is not UTF-8", func(c *board.SettingsChange) { c.Name = "a\xffb" }, "name", "Use one line of plain text."},
		{"long description", func(c *board.SettingsChange) { c.Description = strings.Repeat("d", 501) }, "description", "Use at most 500 characters."},
		{"line break in the description", func(c *board.SettingsChange) { c.Description = "a\r\nb" }, "description", "Use one line of plain text."},
		{"hidden character in the description", func(c *board.SettingsChange) { c.Description = "a\u200bb" }, "description", "Remove the hidden formatting character."},
		{"unknown zone", func(c *board.SettingsChange) { c.Timezone = "Mars/Olympus" }, "timezone", "Pick a time zone from the list."},
		{"offset zone", func(c *board.SettingsChange) { c.Timezone = "UTC+2" }, "timezone", "Pick a time zone from the list."},
		{"empty zone", func(c *board.SettingsChange) { c.Timezone = "" }, "timezone", "Pick a time zone from the list."},
		{"unknown scheme", func(c *board.SettingsChange) { c.Scheme = "neon" }, "scheme", "Pick one of the color schemes."},
		{"empty scheme", func(c *board.SettingsChange) { c.Scheme = "" }, "scheme", "Pick one of the color schemes."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := current(before)
			c.Description = "kept" // so that a refusal is not also a no-change
			tc.edit(&c)
			_, err := board.UpdateSettings(ctx, pool, alice, c, t0)
			var in *board.InputError
			if !errors.As(err, &in) || in.Field != tc.field || in.Message != tc.msg {
				t.Fatalf("error = %v, want %s: %q", err, tc.field, tc.msg)
			}
			if strings.ContainsAny(in.Message, "\x00\n\u202e\u200b") || strings.Contains(in.Message, "Mars") || strings.Contains(in.Message, "neon") {
				t.Errorf("the message holds text from the request: %q", in.Message)
			}
			if loadSettings(t, pool) != before || logCount(t, pool) != rows {
				t.Error("a refused save changed something")
			}
		})
	}

	// Several faults come back together, so the form shows each one.
	_, err := board.UpdateSettings(ctx, pool, alice, board.SettingsChange{Name: "", Description: strings.Repeat("d", 501), Timezone: "x", Scheme: "y"}, t0)
	var fields []string
	for _, e := range joined(err) {
		var in *board.InputError
		if !errors.As(e, &in) {
			t.Fatalf("part %v is not an input error", e)
		}
		fields = append(fields, in.Field)
	}
	if want := []string{"name", "description", "timezone", "scheme"}; !slices.Equal(fields, want) {
		t.Errorf("fields = %v, want %v", fields, want)
	}
	if loadSettings(t, pool) != before || logCount(t, pool) != rows {
		t.Error("a refused save changed something")
	}
}

// joined returns the parts of an error made by errors.Join, or the error alone.
func joined(err error) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		return j.Unwrap()
	}
	return []error{err}
}

// The zone the board has is accepted when it is not in the list, so that its
// name can still change; any other zone must be in the list.
func TestUpdateSettingsKeepsAnOddZone(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	alice := viewerFor(t, pool, 1)
	if _, err := pool.Exec(ctx, `update settings set value = '"Etc/GMT+5"' where key = 'timezone'`); err != nil {
		t.Fatal(err)
	}
	c := current(loadSettings(t, pool))
	c.Name = "Renamed"
	if keys, err := board.UpdateSettings(ctx, pool, alice, c, t0); err != nil || !slices.Equal(keys, []string{"board_name"}) {
		t.Fatalf("rename with an odd zone: %v %v", keys, err)
	}
	c.Timezone = "Etc/GMT+6"
	var in *board.InputError
	if _, err := board.UpdateSettings(ctx, pool, alice, c, t0); !errors.As(err, &in) || in.Field != "timezone" {
		t.Errorf("another odd zone: %v, want a refusal of timezone", err)
	}
	c.Timezone = "Europe/London"
	if _, err := board.UpdateSettings(ctx, pool, alice, c, t0); err != nil {
		t.Errorf("a listed zone: %v", err)
	}
}

func TestSettingsWritesNeedAdmin(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	before := loadSettings(t, pool)
	rows := logCount(t, pool)
	c := current(before)
	c.Name = "Taken over"
	// bob moderates, carol is a member, and the guest is not signed in.
	for _, v := range []board.Viewer{moderator(t, pool, 2), viewerFor(t, pool, 3), asGuest(t, pool)} {
		if _, err := board.UpdateSettings(ctx, pool, v, c, t0); !errors.Is(err, board.ErrForbidden) {
			t.Errorf("user %d: UpdateSettings = %v, want ErrForbidden", v.UserID, err)
		}
		if _, err := board.SetHeaderImage(ctx, pool, v, "header/h-0123456789abcdef.png", t0); !errors.Is(err, board.ErrForbidden) {
			t.Errorf("user %d: SetHeaderImage = %v, want ErrForbidden", v.UserID, err)
		}
		if _, err := board.RemoveHeaderImage(ctx, pool, v, t0); !errors.Is(err, board.ErrForbidden) {
			t.Errorf("user %d: RemoveHeaderImage = %v, want ErrForbidden", v.UserID, err)
		}
	}
	if loadSettings(t, pool) != before || logCount(t, pool) != rows {
		t.Error("a refused write changed something")
	}
}

func TestHeaderImage(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	alice := viewerFor(t, pool, 1)
	was := loadSettings(t, pool).HeaderImage
	if was == "" {
		t.Fatal("setup: the small board has no header image")
	}
	rows := logCount(t, pool)

	// A new image replaces the old one, and the old path comes back for deletion.
	const first, second = "header/h-0123456789abcdef.png", "header/h-fedcba9876543210.webp"
	old, err := board.SetHeaderImage(ctx, pool, alice, first, t0)
	if err != nil || old != was {
		t.Fatalf("SetHeaderImage = %q, %v; want %q", old, err, was)
	}
	if got := loadSettings(t, pool).HeaderImage; got != first {
		t.Errorf("header = %q", got)
	}
	actor, action, targetType, target, details := lastLog(t, pool)
	if logCount(t, pool) != rows+1 || actor != 1 || action != "settings.update" || targetType != "settings" || target != 0 ||
		details != `{"via": "web", "keys": ["header_image"]}` {
		t.Errorf("log row = %d %s %s %d %s", actor, action, targetType, target, details)
	}
	if old, err = board.SetHeaderImage(ctx, pool, alice, second, t0); err != nil || old != first {
		t.Fatalf("second SetHeaderImage = %q, %v; want %q", old, err, first)
	}

	// Removal clears the setting (JSON null) and returns the path.
	if old, err = board.RemoveHeaderImage(ctx, pool, alice, t0); err != nil || old != second {
		t.Fatalf("RemoveHeaderImage = %q, %v; want %q", old, err, second)
	}
	if got := loadSettings(t, pool).HeaderImage; got != "" {
		t.Errorf("header = %q after the removal", got)
	}
	var null bool
	if err := pool.QueryRow(ctx, `select value = 'null'::jsonb from settings where key = 'header_image'`).Scan(&null); err != nil || !null {
		t.Errorf("stored value is JSON null: %v %v", null, err)
	}
	// With no image, a removal changes nothing and writes no row.
	rows = logCount(t, pool)
	if old, err = board.RemoveHeaderImage(ctx, pool, alice, t0); !errors.Is(err, board.ErrNoChange) || old != "" {
		t.Errorf("second removal = %q, %v; want ErrNoChange", old, err)
	}
	if logCount(t, pool) != rows {
		t.Error("a removal that changed nothing wrote a row")
	}

	// A path that gobb does not make is an error of the caller.
	for _, bad := range []string{"", "avatars/h-0123456789abcdef.png", "header/../x.png", "header/banner.png", "header/h-0123456789abcdef.gif",
		"header/h-0123456789ABCDEF.png", "/header/h-0123456789abcdef.png", "header/h-0123456789abcdef.png/x", "header/h-0123.png"} {
		if _, err := board.SetHeaderImage(ctx, pool, alice, bad, t0); err == nil {
			t.Errorf("SetHeaderImage(%q) succeeded", bad)
		}
	}
	if logCount(t, pool) != rows {
		t.Error("a refused path wrote a row")
	}
}

func TestValidHeaderPath(t *testing.T) {
	for p, want := range map[string]bool{
		"header/banner.png":              true,
		"header/h-0123456789abcdef.webp": true,
		"header/a/b.jpg":                 true,
		"header/":                        false,
		"header":                         false,
		"avatars/banner.png":             false,
		"header/.hidden":                 false,
		"header/../x":                    false,
		"header//x":                      false,
		"header/a b.png":                 false,
		"header/a\x00b":                  false,
		"":                               false,
	} {
		if got := board.ValidHeaderPath(p); got != want {
			t.Errorf("ValidHeaderPath(%q) = %v, want %v", p, got, want)
		}
	}
}

// Two saves at once each read the values the other left, so both rows are
// written and the last value stands; neither tears the other.
func TestUpdateSettingsConcurrent(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	alice := viewerFor(t, pool, 1)
	rows := logCount(t, pool)
	base := current(loadSettings(t, pool))
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := base
			c.Name = "Name " + strings.Repeat("x", i+1)
			_, errs[i] = board.UpdateSettings(ctx, pool, alice, c, t0)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("save %d: %v", i, err)
		}
	}
	if got := logCount(t, pool); got != rows+len(errs) {
		t.Errorf("rows = %d, want %d", got, rows+len(errs))
	}
	if n := loadSettings(t, pool).BoardName; !strings.HasPrefix(n, "Name x") {
		t.Errorf("name = %q", n)
	}
}
