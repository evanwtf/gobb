package board_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// settingsRow is the part of a users row that the settings writes change.
type settingsRow struct {
	Timezone, DateFormat                  string
	Location, Occupation, Interests, Site string
	Day, Month, Year                      *int
	Avatar                                *string
}

func readSettings(t *testing.T, pool *pgxpool.Pool, id int64) settingsRow {
	t.Helper()
	var r settingsRow
	err := pool.QueryRow(context.Background(), `
select timezone, date_format, location, occupation, interests, website,
       birth_day, birth_month, birth_year, avatar_path
  from users where id = $1`, id).
		Scan(&r.Timezone, &r.DateFormat, &r.Location, &r.Occupation, &r.Interests, &r.Site,
			&r.Day, &r.Month, &r.Year, &r.Avatar)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSetTimezone(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := viewerFor(t, pool, carol)

	if err := board.SetTimezone(ctx, pool, c, "America/Chicago"); err != nil {
		t.Fatal(err)
	}
	if got := readSettings(t, pool, carol).Timezone; got != "America/Chicago" {
		t.Errorf("timezone = %q, want America/Chicago", got)
	}
	for _, bad := range []string{"Etc/GMT+4", "UTC", "Mars/Base", "", "america/chicago", " America/Chicago"} {
		wantInput(t, board.SetTimezone(ctx, pool, c, bad), "timezone", "Pick a time zone from the list.")
	}
	if got := readSettings(t, pool, carol).Timezone; got != "America/Chicago" {
		t.Errorf("a refused zone changed the timezone to %q", got)
	}
}

func TestSetDateFormat(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := viewerFor(t, pool, carol)

	for _, f := range []string{"relative", "iso", "standard"} {
		if err := board.SetDateFormat(ctx, pool, c, f); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if got := readSettings(t, pool, carol).DateFormat; got != f {
			t.Errorf("date_format = %q, want %q", got, f)
		}
	}
	if err := board.SetDateFormat(ctx, pool, c, "iso"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"custom", "", "ISO", "iso "} {
		wantInput(t, board.SetDateFormat(ctx, pool, c, bad), "dateformat", "Pick one of the three formats.")
	}
	if got := readSettings(t, pool, carol).DateFormat; got != "iso" {
		t.Errorf("a refused format changed date_format to %q", got)
	}
}

func TestSetAboutPartial(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := viewerFor(t, pool, carol)

	all := board.AboutChange{
		Location: ptr("Springfield"), Occupation: ptr("Baker"),
		Interests: ptr("Bread"), Website: ptr("https://example.org/c"),
	}
	if err := board.SetAbout(ctx, pool, c, all); err != nil {
		t.Fatal(err)
	}
	// A value is trimmed.
	if err := board.SetAbout(ctx, pool, c, board.AboutChange{Location: ptr("  Shelbyville \t")}); err != nil {
		t.Fatal(err)
	}
	r := readSettings(t, pool, carol)
	if r.Location != "Shelbyville" || r.Occupation != "Baker" || r.Interests != "Bread" || r.Site != "https://example.org/c" {
		t.Errorf("after a change of Location only: %+v", r)
	}
	// A set but empty field clears; a nil field stays.
	if err := board.SetAbout(ctx, pool, c, board.AboutChange{Occupation: ptr("   ")}); err != nil {
		t.Fatal(err)
	}
	r = readSettings(t, pool, carol)
	if r.Occupation != "" || r.Location != "Shelbyville" {
		t.Errorf("after clearing the occupation: %+v", r)
	}
	// An empty change is not an error and changes nothing.
	if err := board.SetAbout(ctx, pool, c, board.AboutChange{}); err != nil {
		t.Fatal(err)
	}
	if got := readSettings(t, pool, carol); !reflect.DeepEqual(got, r) {
		t.Errorf("an empty change moved the row: %+v -> %+v", r, got)
	}
}

func TestAboutRules(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := viewerFor(t, pool, carol)
	if err := board.SetAbout(ctx, pool, c, board.AboutChange{
		Location: ptr("Home"), Occupation: ptr("Job"), Interests: ptr("Fun"), Website: ptr("https://example.org/"),
	}); err != nil {
		t.Fatal(err)
	}
	before := readSettings(t, pool, carol)

	const (
		long100  = "Use at most 100 characters."
		long200  = "Use at most 200 characters."
		oneLine  = "Use one line of plain text."
		siteForm = "Enter an address that starts with https:// or http://."
	)
	cases := []struct {
		name    string
		change  board.AboutChange
		field   string
		message string
	}{
		{"location 101 chars", board.AboutChange{Location: ptr(strings.Repeat("é", 101))}, "location", long100},
		{"occupation 101 chars", board.AboutChange{Occupation: ptr(strings.Repeat("a", 101))}, "occupation", long100},
		{"interests 201 chars", board.AboutChange{Interests: ptr(strings.Repeat("a", 201))}, "interests", long200},
		{"location newline", board.AboutChange{Location: ptr("a\nb")}, "location", oneLine},
		{"occupation tab", board.AboutChange{Occupation: ptr("a\tb")}, "occupation", oneLine},
		{"interests NUL", board.AboutChange{Interests: ptr("a\x00b")}, "interests", oneLine},
		{"location bad UTF-8", board.AboutChange{Location: ptr("a\xffb")}, "location", oneLine},
		{"website ftp", board.AboutChange{Website: ptr("ftp://example.org/")}, "website", siteForm},
		{"website javascript", board.AboutChange{Website: ptr("javascript:alert(1)")}, "website", siteForm},
		{"website no scheme", board.AboutChange{Website: ptr("example.org")}, "website", siteForm},
		{"website userinfo", board.AboutChange{Website: ptr("https://u:p@example.org/")}, "website", siteForm},
		{"website space", board.AboutChange{Website: ptr("https://exa mple.org/")}, "website", siteForm},
		{"website no host", board.AboutChange{Website: ptr("https:///x")}, "website", siteForm},
		{"website 201 bytes", board.AboutChange{Website: ptr("https://example.org/" + strings.Repeat("a", 181))}, "website", "Use an address of at most 200 characters."},
		// A refusal in one field stops the whole change.
		{"good then bad", board.AboutChange{Location: ptr("Changed"), Website: ptr("nope")}, "website", siteForm},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantInput(t, board.SetAbout(ctx, pool, c, tc.change), tc.field, tc.message)
			if got := readSettings(t, pool, carol); !reflect.DeepEqual(got, before) {
				t.Errorf("a refusal changed the row: %+v -> %+v", before, got)
			}
		})
	}

	// The limits themselves pass: 100 and 200 characters, 200 bytes.
	ok := board.AboutChange{
		Location: ptr(strings.Repeat("é", 100)), Occupation: ptr(strings.Repeat("a", 100)),
		Interests: ptr(strings.Repeat("a", 200)), Website: ptr("https://example.org/" + strings.Repeat("a", 180)),
	}
	if err := board.SetAbout(ctx, pool, c, ok); err != nil {
		t.Errorf("fields at the limit: %v", err)
	}
	// An empty website clears.
	if err := board.SetAbout(ctx, pool, c, board.AboutChange{Website: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if got := readSettings(t, pool, carol).Site; got != "" {
		t.Errorf("website = %q after clearing", got)
	}
}

func TestBirthdayRules(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := viewerFor(t, pool, carol)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

	set := board.Birthday{Day: 14, Month: 3, Year: 1990}
	if err := board.SetBirthday(ctx, pool, c, set, now); err != nil {
		t.Fatal(err)
	}
	want := settingsRow{Day: ptr(14), Month: ptr(3), Year: ptr(1990)}
	check := func(label string, w settingsRow) {
		t.Helper()
		r := readSettings(t, pool, carol)
		if !eqInt(r.Day, w.Day) || !eqInt(r.Month, w.Month) || !eqInt(r.Year, w.Year) {
			t.Errorf("%s: birthday = %v/%v/%v", label, deref(r.Day), deref(r.Month), deref(r.Year))
		}
	}
	check("set", want)

	const (
		pair = "Pick both a day and a month, or neither."
		year = "A year needs a day and a month."
	)
	cases := []struct {
		name    string
		b       board.Birthday
		message string
	}{
		{"day alone", board.Birthday{Day: 5}, pair},
		{"month alone", board.Birthday{Month: 5}, pair},
		{"day and year", board.Birthday{Day: 5, Year: 1990}, pair},
		{"year alone", board.Birthday{Year: 1990}, year},
		{"31 April", board.Birthday{Day: 31, Month: 4, Year: 1990}, "April has no day 31."},
		{"31 April no year", board.Birthday{Day: 31, Month: 4}, "April has no day 31."},
		{"30 February no year", board.Birthday{Day: 30, Month: 2}, "February has no day 30."},
		{"29 Feb 1999", board.Birthday{Day: 29, Month: 2, Year: 1999}, "February 1999 has no day 29."},
		{"29 Feb 1900", board.Birthday{Day: 29, Month: 2, Year: 1900}, "February 1900 has no day 29."},
		{"day 32", board.Birthday{Day: 32, Month: 1}, "January has no day 32."},
		{"year 2008", board.Birthday{Day: 1, Month: 1, Year: 2008}, "The latest year you can pick is 2007."},
		{"year 1899", board.Birthday{Day: 1, Month: 1, Year: 1899}, "The earliest year you can pick is 1900."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantInput(t, board.SetBirthday(ctx, pool, c, tc.b, now), "birthday", tc.message)
			check(tc.name, want)
		})
	}

	// These pass: the bounds, a leap day, and no year.
	for _, b := range []board.Birthday{
		{Day: 1, Month: 1, Year: 1900},
		{Day: 31, Month: 12, Year: 2007},
		{Day: 29, Month: 2, Year: 2000},
		{Day: 29, Month: 2},
		{Day: 25, Month: 12},
	} {
		if err := board.SetBirthday(ctx, pool, c, b, now); err != nil {
			t.Errorf("%+v: %v", b, err)
		}
	}

	// The latest year follows now.
	later := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := board.SetBirthday(ctx, pool, c, board.Birthday{Day: 1, Month: 1, Year: 2011}, later); err != nil {
		t.Errorf("2011 in 2030: %v", err)
	}
	wantInput(t, board.SetBirthday(ctx, pool, c, board.Birthday{Day: 1, Month: 1, Year: 2012}, later), "birthday", "The latest year you can pick is 2011.")

	// The empty value clears the birthday.
	if err := board.SetBirthday(ctx, pool, c, board.Birthday{}, now); err != nil {
		t.Fatal(err)
	}
	check("cleared", settingsRow{})
}

func eqInt(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestSettingsRights(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	g := asGuest(t, pool)

	// A guest gets ErrForbidden from each function.
	wantIs(t, board.SetTimezone(ctx, pool, g, "America/Chicago"), board.ErrForbidden, "SetTimezone by a guest")
	wantIs(t, board.SetDateFormat(ctx, pool, g, "iso"), board.ErrForbidden, "SetDateFormat by a guest")
	wantIs(t, board.SetAbout(ctx, pool, g, board.AboutChange{Location: ptr("x")}), board.ErrForbidden, "SetAbout by a guest")
	wantIs(t, board.SetBirthday(ctx, pool, g, board.Birthday{Day: 1, Month: 1}, now), board.ErrForbidden, "SetBirthday by a guest")
	_, err := board.SetAvatar(ctx, pool, g, "avatars/u1-0123456789abcdef.png")
	wantIs(t, err, board.ErrForbidden, "SetAvatar by a guest")
	_, err = board.RemoveAvatar(ctx, pool, g)
	wantIs(t, err, board.ErrForbidden, "RemoveAvatar by a guest")

	// With the rights removed, the matching writes refuse and the others work.
	denyOpts(t, pool, carol, 0, "account.profile", "account.avatar")
	before := readSettings(t, pool, carol)
	c := viewerFor(t, pool, carol)
	wantIs(t, board.SetAbout(ctx, pool, c, board.AboutChange{Location: ptr("x")}), board.ErrForbidden, "SetAbout without account.profile")
	wantIs(t, board.SetBirthday(ctx, pool, c, board.Birthday{Day: 1, Month: 1}, now), board.ErrForbidden, "SetBirthday without account.profile")
	_, err = board.SetAvatar(ctx, pool, c, "avatars/u3-0123456789abcdef.png")
	wantIs(t, err, board.ErrForbidden, "SetAvatar without account.avatar")
	_, err = board.RemoveAvatar(ctx, pool, c)
	wantIs(t, err, board.ErrForbidden, "RemoveAvatar without account.avatar")
	if got := readSettings(t, pool, carol); !reflect.DeepEqual(got, before) {
		t.Errorf("a refused write changed the row: %+v -> %+v", before, got)
	}
	if err := board.SetTimezone(ctx, pool, c, "America/Denver"); err != nil {
		t.Errorf("SetTimezone without the account rights: %v", err)
	}
	if err := board.SetDateFormat(ctx, pool, c, "relative"); err != nil {
		t.Errorf("SetDateFormat without the account rights: %v", err)
	}
}

func TestSettingsAvatarPath(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := viewerFor(t, pool, carol)

	old, err := board.RemoveAvatar(ctx, pool, c)
	if err != nil || old != "" {
		t.Fatalf("RemoveAvatar with none: %q, %v", old, err)
	}
	p1, p2 := "avatars/u3-0123456789abcdef.png", "avatars/u3-fedcba9876543210.png"
	if old, err = board.SetAvatar(ctx, pool, c, p1); err != nil || old != "" {
		t.Fatalf("first SetAvatar: %q, %v", old, err)
	}
	if got := readSettings(t, pool, carol).Avatar; got == nil || *got != p1 {
		t.Fatalf("avatar_path = %v, want %s", got, p1)
	}
	if old, err = board.SetAvatar(ctx, pool, c, p2); err != nil || old != p1 {
		t.Fatalf("second SetAvatar: %q, %v; want old %s", old, err, p1)
	}
	if old, err = board.RemoveAvatar(ctx, pool, c); err != nil || old != p2 {
		t.Fatalf("RemoveAvatar: %q, %v; want old %s", old, err, p2)
	}
	if got := readSettings(t, pool, carol).Avatar; got != nil {
		t.Errorf("avatar_path = %q after remove, want NULL", *got)
	}
	// A path outside avatars/ is a fault of the caller.
	for _, bad := range []string{"", "x.png", "avatars/", "avatars/../etc/passwd", "/avatars/a.png", "avatars/a\x00.png", "avatars\\a.png"} {
		if _, err := board.SetAvatar(ctx, pool, c, bad); err == nil {
			t.Errorf("SetAvatar(%q) = nil, want an error", bad)
		}
	}
}
