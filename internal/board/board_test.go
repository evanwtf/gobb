package board_test

import (
	"context"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

func TestLoadSettings(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())

	s, err := board.LoadSettings(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if s.BoardName != "Example Board" {
		t.Errorf("BoardName = %q", s.BoardName)
	}
	if s.BoardDescription != "A board for examples" {
		t.Errorf("BoardDescription = %q", s.BoardDescription)
	}
	if s.Timezone != "America/New_York" {
		t.Errorf("Timezone = %q", s.Timezone)
	}
	if s.DefaultScheme != "slate" {
		t.Errorf("DefaultScheme = %q", s.DefaultScheme)
	}
	if s.Registration != "approval" {
		t.Errorf("Registration = %q", s.Registration)
	}
	if !s.EmailNotifications {
		t.Error("EmailNotifications = false")
	}
	if s.HeaderImage != "header/banner.png" {
		t.Errorf("HeaderImage = %q", s.HeaderImage)
	}

	if _, err := pool.Exec(ctx, `delete from settings where key = 'board_name'`); err != nil {
		t.Fatal(err)
	}
	if _, err := board.LoadSettings(ctx, pool); err == nil || !strings.Contains(err.Error(), "board_name") {
		t.Errorf("error with board_name missing = %v, want one that names board_name", err)
	}
}

func TestLoadSettingsNullHeader(t *testing.T) {
	ctx := context.Background()
	b := bundletest.Small()
	b.Settings.HeaderImage = nil
	delete(b.Files, "header/banner.png")
	pool, _ := boardtest.Load(t, b)
	s, err := board.LoadSettings(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if s.HeaderImage != "" {
		t.Errorf("HeaderImage = %q, want empty", s.HeaderImage)
	}
}

func TestGuest(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	s, err := board.LoadSettings(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	v, err := board.Guest(ctx, pool, s)
	if err != nil {
		t.Fatal(err)
	}
	if v.UserID != 0 || v.Scheme != "slate" || v.Loc.String() != "America/New_York" {
		t.Errorf("guest = %+v", v)
	}
	if !v.Perms.Has("forum.read", 2) || v.Perms.Has("forum.read", 3) {
		t.Error("guest read rights are wrong")
	}
}

// SetScheme stores the color scheme of an active member, and reports false for
// a member who is not active or does not exist. It refuses a scheme that does
// not exist.
func TestSetScheme(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	scheme := func(id int64) string {
		var s string
		if err := pool.QueryRow(ctx, `select color_scheme from users where id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if ok, err := board.SetScheme(ctx, pool, 1, "slate-light"); err != nil || !ok || scheme(1) != "slate-light" {
		t.Errorf("alice: ok %v, err %v, scheme %q; want true, nil, slate-light", ok, err, scheme(1))
	}
	if ok, err := board.SetScheme(ctx, pool, 4, "slate-light"); err != nil || ok || scheme(4) != "slate" {
		t.Errorf("dave (inactive): ok %v, err %v, scheme %q; want false, nil, slate", ok, err, scheme(4))
	}
	if ok, err := board.SetScheme(ctx, pool, 99, "slate-light"); err != nil || ok {
		t.Errorf("no such user: ok %v, err %v; want false, nil", ok, err)
	}
	if ok, err := board.SetScheme(ctx, pool, 1, "neon"); err == nil || ok || scheme(1) != "slate-light" {
		t.Errorf("unknown scheme: ok %v, err %v, scheme %q; want an error and no change", ok, err, scheme(1))
	}
}
