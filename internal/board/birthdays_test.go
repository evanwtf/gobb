package board_test

import (
	"context"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/acl"
	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

func TestProfileAboutAndAge(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	bob := memberOn(t, pool, 2, "bob") // zone Europe/London

	at := func(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.UTC) }
	for _, c := range []struct {
		name string
		now  time.Time
		want int
	}{
		{"before the birthday", at(2027, 3, 13, 12), 42},
		{"on the birthday", at(2027, 3, 14, 12), 43},
		{"the year before", at(2026, 10, 25, 12), 42},
		// London is on UTC in March, so midnight UTC is midnight there.
		{"just after midnight in the zone", at(2027, 3, 14, 0), 43},
	} {
		p, err := board.Profile(ctx, pool, bob, 1, c.now)
		if err != nil {
			t.Fatal(err)
		}
		if p.Age == nil || *p.Age != c.want {
			t.Errorf("%s: Age = %v, want %d", c.name, p.Age, c.want)
		}
	}

	p, err := board.Profile(ctx, pool, bob, 1, at(2026, 10, 25, 12))
	if err != nil {
		t.Fatal(err)
	}
	if p.Location != "Lake Placid, NY" || p.Website != "https://example.com/alice" || p.Occupation != "" || p.Interests != "" {
		t.Errorf("about = %q %q %q %q", p.Location, p.Occupation, p.Interests, p.Website)
	}
	if !p.CanMessage {
		t.Error("bob may message alice")
	}

	// The age uses the calendar of the viewer's zone: 23:30 UTC on 13 March 2027
	// is 14 March in Auckland.
	auckland := bob
	auckland.Loc, _ = time.LoadLocation("Pacific/Auckland")
	p, _ = board.Profile(ctx, pool, auckland, 1, at(2027, 3, 13, 23))
	if p.Age == nil || *p.Age != 43 {
		t.Errorf("Age in Auckland = %v, want 43", p.Age)
	}

	// No year: no age. No about fields: all empty.
	p, _ = board.Profile(ctx, pool, bob, 2, at(2026, 10, 25, 12))
	if p.Age != nil {
		t.Errorf("bob Age = %d, want none", *p.Age)
	}
	if p.CanMessage {
		t.Error("bob may not message himself")
	}
	p, _ = board.Profile(ctx, pool, bob, 3, at(2026, 10, 25, 12))
	if p.Age != nil || p.Location != "" || p.Occupation != "" || p.Interests != "" || p.Website != "" {
		t.Errorf("carol = %+v", p)
	}
	// dave is inactive.
	p, _ = board.Profile(ctx, pool, bob, 4, at(2026, 10, 25, 12))
	if p.CanMessage {
		t.Error("an inactive member cannot be messaged")
	}
	// Without pm.send there is no message button.
	noSend := bob
	noSend.Perms = acl.Resolve(acl.Subject{Grants: []acl.Grant{{Option: "account.view_profiles", Setting: acl.Yes}}})
	if p, err = board.Profile(ctx, pool, noSend, 3, at(2026, 10, 25, 12)); err != nil || p.CanMessage {
		t.Errorf("without pm.send: CanMessage = %v, err = %v", p.CanMessage, err)
	}
}

func TestBirthdays(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	bob := memberOn(t, pool, 2, "bob")
	guest := guestOn(t, pool)
	at := func(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.UTC) }

	rows, err := board.Birthdays(ctx, pool, bob, at(2026, 3, 14, 12))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != 1 || rows[0].Name != "alice" || rows[0].Age == nil || *rows[0].Age != 42 {
		t.Fatalf("14 March 2026 = %+v", rows)
	}

	// Bob has no year: he shows with no age.
	rows, _ = board.Birthdays(ctx, pool, bob, at(2026, 7, 2, 12))
	if len(rows) != 1 || rows[0].Name != "bob" || rows[0].Age != nil {
		t.Errorf("2 July = %+v", rows)
	}

	// The viewer's zone decides the day: 23:30 UTC on 13 March is 14 March in
	// Auckland, and still the 13th in London.
	auckland := bob
	auckland.Loc, _ = time.LoadLocation("Pacific/Auckland")
	if rows, _ = board.Birthdays(ctx, pool, auckland, at(2026, 3, 13, 23)); len(rows) != 1 {
		t.Errorf("Auckland = %+v, want alice", rows)
	}
	if rows, _ = board.Birthdays(ctx, pool, bob, at(2026, 3, 13, 23)); len(rows) != 0 {
		t.Errorf("London = %+v, want none", rows)
	}

	if rows, err = board.Birthdays(ctx, pool, bob, at(2026, 10, 25, 12)); err != nil || len(rows) != 0 {
		t.Errorf("no birthday = %+v, %v", rows, err)
	}

	// A guest has no account.view_profiles: no rows and no error.
	if rows, err = board.Birthdays(ctx, pool, guest, at(2026, 3, 14, 12)); err != nil || rows != nil {
		t.Errorf("guest = %+v, %v", rows, err)
	}

	// An inactive member does not show.
	if _, err := pool.Exec(ctx, `update users set birth_day = 14, birth_month = 3 where id = 4`); err != nil {
		t.Fatal(err)
	}
	rows, _ = board.Birthdays(ctx, pool, bob, at(2026, 3, 14, 12))
	if len(rows) != 1 {
		t.Errorf("with an inactive member = %+v", rows)
	}
}

func TestBirthdaysLeapDay(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	bob := memberOn(t, pool, 2, "bob")
	if _, err := pool.Exec(ctx, `update users set birth_day = 29, birth_month = 2, birth_year = 2000 where id = 3`); err != nil {
		t.Fatal(err)
	}
	at := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, time.UTC) }
	carol := func(now time.Time) *board.BirthdayRow {
		rows, err := board.Birthdays(ctx, pool, bob, now)
		if err != nil {
			t.Fatal(err)
		}
		for i := range rows {
			if rows[i].Name == "carol" {
				return &rows[i]
			}
		}
		return nil
	}
	// Not a leap year: the birthday falls on 28 February, so the list shows her
	// that day with the new age, and not on 1 March or the day before.
	if r := carol(at(2027, 2, 28)); r == nil || r.Age == nil || *r.Age != 27 {
		t.Errorf("28 Feb 2027 = %+v, want age 27", r)
	}
	if r := carol(at(2027, 2, 27)); r != nil {
		t.Errorf("27 Feb 2027 = %+v, want none", r)
	}
	if r := carol(at(2027, 3, 1)); r != nil {
		t.Errorf("1 Mar 2027 = %+v, want none", r)
	}
	// A leap year: 28 February is not her day, 29 February is.
	if r := carol(at(2028, 2, 28)); r != nil {
		t.Errorf("28 Feb 2028 = %+v, want none", r)
	}
	if r := carol(at(2028, 2, 29)); r == nil || r.Age == nil || *r.Age != 28 {
		t.Errorf("29 Feb 2028 = %+v, want age 28", r)
	}

	// The profile age: reached on 28 February in a year that is not a leap year,
	// the same day as the list.
	age := func(now time.Time) int {
		p, err := board.Profile(ctx, pool, bob, 3, now)
		if err != nil || p.Age == nil {
			t.Fatalf("Profile: %v, %v", p.Age, err)
		}
		return *p.Age
	}
	if got := age(at(2027, 2, 27)); got != 26 {
		t.Errorf("age on 27 Feb 2027 = %d, want 26", got)
	}
	if got := age(at(2027, 2, 28)); got != 27 {
		t.Errorf("age on 28 Feb 2027 = %d, want 27", got)
	}
	if got := age(at(2028, 2, 28)); got != 27 {
		t.Errorf("age on 28 Feb 2028 = %d, want 27", got)
	}
	if got := age(at(2028, 2, 29)); got != 28 {
		t.Errorf("age on 29 Feb 2028 = %d, want 28", got)
	}
	if got := age(at(2027, 3, 1)); got != 27 {
		t.Errorf("age on 1 Mar 2027 = %d, want 27", got)
	}
}
