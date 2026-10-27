package board_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// adminBob makes bob an admin who is not an owner and returns him.
func adminBob(t *testing.T, pool *pgxpool.Pool) board.Viewer {
	t.Helper()
	setGrant(t, pool, bob, 0, 3, "admin")
	return viewerFor(t, pool, bob)
}

// banRow is a row of bans, as the tests read it.
type banRow struct {
	ID                  int64
	Kind                string
	UserID              *int64
	Email, IP           *string
	Reason, ReasonShown string
	EndsAt              *time.Time
	CreatedBy           *int64
}

func banRows(t *testing.T, pool *pgxpool.Pool, kind string) []banRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
select id, kind, user_id, email, ip::text, reason, reason_shown, ends_at, created_by
  from bans where kind = $1 and id > 3 order by id`, kind)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []banRow
	for rows.Next() {
		var b banRow
		if err := rows.Scan(&b.ID, &b.Kind, &b.UserID, &b.Email, &b.IP, &b.Reason, &b.ReasonShown, &b.EndsAt, &b.CreatedBy); err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

func wantInputField(t *testing.T, err error, field string) {
	t.Helper()
	var in *board.InputError
	if !errors.As(err, &in) || in.Field != field {
		t.Errorf("err = %v, want an input error on %q", err, field)
	}
}

func TestAddUserBan(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	seedSessions(t, pool)
	if _, err := pool.Exec(ctx, `insert into user_ips (user_id, ip, last_seen_at) values ($1, '10.5.5.5', $2)`, bob, t0); err != nil {
		t.Fatal(err)
	}
	a := viewerFor(t, pool, alice)
	end := t0.Add(48 * time.Hour)
	id, err := board.AddBan(ctx, pool, a, board.BanDraft{Kind: "user", Target: " BOB ", Reason: " spam run ", ReasonShown: "Spam", EndsAt: &end}, t0)
	if err != nil {
		t.Fatal(err)
	}
	rows := banRows(t, pool, "user")
	if len(rows) != 1 || rows[0].ID != id || *rows[0].UserID != bob || rows[0].Reason != "spam run" || rows[0].ReasonShown != "Spam" ||
		rows[0].EndsAt == nil || !rows[0].EndsAt.Equal(end) || rows[0].CreatedBy == nil || *rows[0].CreatedBy != alice {
		t.Fatalf("ban = %+v", rows)
	}
	if n := sessionCount(t, pool, bob); n != 0 {
		t.Errorf("bob has %d sessions, want 0", n)
	}
	var ips int
	if err := pool.QueryRow(ctx, `select count(*) from user_ips where user_id = $1`, bob).Scan(&ips); err != nil || ips != 0 {
		t.Errorf("%d addresses of bob, %v, want 0", ips, err)
	}
	if n := sessionCount(t, pool, alice); n != 2 {
		t.Errorf("alice has %d sessions, want 2", n)
	}
	log := modRows(t, pool)
	if len(log) != 1 || log[0].Actor == nil || *log[0].Actor != alice || log[0].Action != "ban.add" || log[0].TargetType != "ban" || log[0].Target != id ||
		log[0].Details != `{"via": "web", "ends": "2026-10-26T12:00:00Z", "kind": "user", "target": "bob"}` {
		t.Errorf("mod_log = %+v", log)
	}

	// A second ban of a banned member adds nothing.
	_, err = board.AddBan(ctx, pool, a, board.BanDraft{Kind: "user", Target: "bob"}, t0)
	wantInputField(t, err, "target")
	if len(banRows(t, pool, "user")) != 1 || len(modRows(t, pool)) != 1 {
		t.Error("a refused ban changed something")
	}
	// Once the ban has ended, a new one goes in.
	if _, err := board.AddBan(ctx, pool, a, board.BanDraft{Kind: "user", Target: "bob"}, end.Add(time.Minute)); err != nil {
		t.Errorf("ban after the end: %v", err)
	}
}

func TestAddUserBanRefusals(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	a := viewerFor(t, pool, alice)
	b := adminBob(t, pool)
	long := strings.Repeat("x", 256)
	past := t0.Add(-time.Hour)
	for name, c := range map[string]struct {
		v     board.Viewer
		d     board.BanDraft
		field string
	}{
		"self":               {a, board.BanDraft{Kind: "user", Target: "alice"}, "target"},
		"an owner":           {b, board.BanDraft{Kind: "user", Target: "alice"}, "target"},
		"an unknown member":  {a, board.BanDraft{Kind: "user", Target: "nobody"}, "target"},
		"an empty target":    {a, board.BanDraft{Kind: "user", Target: " "}, "target"},
		"a bad kind":         {a, board.BanDraft{Kind: "group", Target: "bob"}, "kind"},
		"a long reason":      {a, board.BanDraft{Kind: "user", Target: "bob", Reason: long}, "reason"},
		"a long shown":       {a, board.BanDraft{Kind: "user", Target: "bob", ReasonShown: long}, "reason_shown"},
		"a NUL reason":       {a, board.BanDraft{Kind: "user", Target: "bob", Reason: "a\x00b"}, "reason"},
		"a bad UTF-8 shown":  {a, board.BanDraft{Kind: "user", Target: "bob", ReasonShown: "\xff"}, "reason_shown"},
		"an end in the past": {a, board.BanDraft{Kind: "user", Target: "bob", EndsAt: &past}, "ends"},
		"an end now":         {a, board.BanDraft{Kind: "user", Target: "bob", EndsAt: &t0}, "ends"},
	} {
		_, err := board.AddBan(ctx, pool, c.v, c.d, t0)
		if t.Failed() {
			break
		}
		wantInputField(t, err, c.field)
		if t.Failed() {
			t.Fatalf("case %q", name)
		}
	}
	if len(banRows(t, pool, "user")) != 0 || len(modRows(t, pool)) != 0 {
		t.Error("a refused ban changed something")
	}
	// A reason of 255 characters is fine.
	if _, err := board.AddBan(ctx, pool, a, board.BanDraft{Kind: "user", Target: "bob", Reason: strings.Repeat("é", 255)}, t0); err != nil {
		t.Errorf("255 characters: %v", err)
	}
}

func TestAddBanNeedsAdmin(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	for _, v := range []board.Viewer{viewerFor(t, pool, bob), viewerFor(t, pool, carol), asGuest(t, pool)} {
		if _, err := board.AddBan(ctx, pool, v, board.BanDraft{Kind: "ip", Target: "192.0.2.1"}, t0); !errors.Is(err, board.ErrForbidden) {
			t.Errorf("user %d adds: %v, want ErrForbidden", v.UserID, err)
		}
		if err := board.LiftBan(ctx, pool, v, 1, t0); !errors.Is(err, board.ErrForbidden) {
			t.Errorf("user %d lifts: %v, want ErrForbidden", v.UserID, err)
		}
	}
	if len(banRows(t, pool, "ip")) != 0 || len(modRows(t, pool)) != 0 {
		t.Error("a refused ban changed something")
	}
}

func TestAddEmailBan(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	b := adminBob(t, pool)
	id, err := board.AddBan(ctx, pool, b, board.BanDraft{Kind: "email", Target: "  Spam.Bot@Example.NET ", Reason: "r", ReasonShown: "No."}, t0)
	if err != nil {
		t.Fatal(err)
	}
	rows := banRows(t, pool, "email")
	// The clean form is stored: sign-in and the reset request compare it.
	if len(rows) != 1 || rows[0].ID != id || *rows[0].Email != "spam.bot@example.net" || rows[0].EndsAt != nil || *rows[0].CreatedBy != bob {
		t.Fatalf("ban = %+v", rows)
	}
	log := modRows(t, pool)
	if len(log) != 1 || log[0].Action != "ban.add" || log[0].Details != `{"via": "web", "kind": "email", "target": "spam.bot@example.net"}` {
		t.Errorf("mod_log = %+v", log)
	}
	for name, c := range map[string]string{
		"a held address":      "SPAM.BOT@example.net",
		"the import's ban":    "spammer@example.com",
		"no at sign":          "spam.bot",
		"a display name":      "Spam <a@example.net>",
		"two at signs":        "a@b@example.net",
		"the admin's address": "BOB@example.com",
		"an owner's address":  "alice@example.com",
		"empty":               "",
	} {
		_, err := board.AddBan(ctx, pool, b, board.BanDraft{Kind: "email", Target: c}, t0)
		if name == "the import's ban" {
			// The import's ban has no end: it holds that address.
			if err == nil {
				t.Errorf("%s: no error", name)
			}
			continue
		}
		var in *board.InputError
		if !errors.As(err, &in) || in.Field != "target" {
			t.Errorf("%s: %v, want an input error on target", name, err)
		}
	}
	if len(banRows(t, pool, "email")) != 1 || len(modRows(t, pool)) != 1 {
		t.Error("a refused ban changed something")
	}
}

func TestAddRangeBan(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	a := viewerFor(t, pool, alice)
	admin := netip.MustParseAddr("192.0.2.50")
	add := func(target string, wide bool) (int64, error) {
		return board.AddBan(ctx, pool, a, board.BanDraft{Kind: "ip", Target: target, ConfirmWide: wide, AdminIP: admin}, t0)
	}
	// Host bits are cleared, one address is a range of one, and the mapped form
	// is refused.
	for target, want := range map[string]string{
		"198.51.100.77/24": "198.51.100.0/24",
		"198.51.100.9":     "198.51.100.9/32",
		"2001:db8::1":      "2001:db8::1/128",
		"2001:db8:5::/48":  "2001:db8:5::/48",
	} {
		if _, err := add(target, false); err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		rows := banRows(t, pool, "ip")
		if got := *rows[len(rows)-1].IP; got != want {
			t.Errorf("%s stored as %s, want %s", target, got, want)
		}
	}
	n := len(banRows(t, pool, "ip"))
	for name, target := range map[string]string{
		"a mapped address": "::ffff:198.51.100.5",
		"a mapped range":   "::ffff:198.51.100.0/120",
		"text":             "not an address",
		"a zone":           "fe80::1%eth0",
		"a bad length":     "198.51.100.0/33",
		"empty":            "",
		"a held range":     "198.51.100.0/24",
		"the same, again":  "198.51.100.200/24",
		"the admin":        "192.0.2.50",
		"the admin's net":  "192.0.2.0/24",
	} {
		if _, err := add(target, true); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if len(banRows(t, pool, "ip")) != n {
		t.Error("a refused ban changed something")
	}
	// A wide range needs the second confirm.
	for _, target := range []string{"10.0.0.0/15", "10.0.0.0/8", "2001:db8::/47", "2001::/16"} {
		_, err := add(target, false)
		wantInputField(t, err, "confirm_wide")
		if _, err := add(target, true); err != nil {
			t.Errorf("%s with the confirm: %v", target, err)
		}
	}
	// A range of the whole net holds the admin's own address.
	if _, err := add("0.0.0.0/0", true); err == nil {
		t.Error("0.0.0.0/0 with the confirm: no error")
	}
	// The edge needs none.
	for _, target := range []string{"10.20.0.0/16", "2001:db8:9::/48"} {
		if _, err := add(target, false); err != nil {
			t.Errorf("%s: %v", target, err)
		}
	}
	log := modRows(t, pool)
	if last := log[len(log)-1]; last.Action != "ban.add" || last.Details != `{"via": "web", "kind": "ip", "target": "2001:db8:9::/48"}` {
		t.Errorf("last mod_log = %+v", last)
	}
}

func TestLiftBan(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	a := viewerFor(t, pool, alice)
	user, err := board.AddBan(ctx, pool, a, board.BanDraft{Kind: "user", Target: "carol"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	email, _ := board.AddBan(ctx, pool, a, board.BanDraft{Kind: "email", Target: "x@example.net"}, t0)
	ip, _ := board.AddBan(ctx, pool, a, board.BanDraft{Kind: "ip", Target: "198.51.100.0/24"}, t0)
	for _, c := range []struct {
		id      int64
		details string
	}{
		{user, `{"via": "web", "kind": "user", "target": "carol"}`},
		{email, `{"via": "web", "kind": "email", "target": "x@example.net"}`},
		{ip, `{"via": "web", "kind": "ip", "target": "198.51.100.0/24"}`},
	} {
		before := len(modRows(t, pool))
		if err := board.LiftBan(ctx, pool, a, c.id, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := pool.QueryRow(ctx, `select count(*) from bans where id = $1`, c.id).Scan(&n); err != nil || n != 0 {
			t.Errorf("ban %d: %d rows, %v", c.id, n, err)
		}
		log := modRows(t, pool)
		last := log[len(log)-1]
		if len(log) != before+1 || last.Action != "ban.lift" || last.TargetType != "ban" || last.Target != c.id || last.Details != c.details ||
			last.Actor == nil || *last.Actor != alice {
			t.Errorf("mod_log = %+v", last)
		}
		// A second lift finds nothing and writes nothing.
		if err := board.LiftBan(ctx, pool, a, c.id, t0.Add(time.Minute)); !errors.Is(err, board.ErrNotFound) {
			t.Errorf("second lift = %v, want ErrNotFound", err)
		}
		if len(modRows(t, pool)) != before+1 {
			t.Error("a refused lift wrote a row")
		}
	}
	// The member can be banned again after the lift.
	if _, err := board.AddBan(ctx, pool, a, board.BanDraft{Kind: "user", Target: "carol"}, t0.Add(time.Hour)); err != nil {
		t.Errorf("ban after the lift: %v", err)
	}
	// A ban that has ended is not in force: it cannot be lifted, and it stays.
	end := t0.Add(2 * time.Hour)
	id, err := board.AddBan(ctx, pool, a, board.BanDraft{Kind: "ip", Target: "203.0.113.0/24", EndsAt: &end}, t0.Add(time.Hour+time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := board.LiftBan(ctx, pool, a, id, end.Add(time.Second)); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("lift of an ended ban = %v, want ErrNotFound", err)
	}
	if err := board.LiftBan(ctx, pool, a, 99999, t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("lift of no ban = %v, want ErrNotFound", err)
	}
}

func TestListBans(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	a := viewerFor(t, pool, alice)
	end := t0.Add(time.Hour)
	if _, err := board.AddBan(ctx, pool, a, board.BanDraft{Kind: "user", Target: "carol", Reason: "r1", ReasonShown: "s1"}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := board.AddBan(ctx, pool, a, board.BanDraft{Kind: "ip", Target: "198.51.100.9", EndsAt: &end}, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	p, err := board.ListBans(ctx, pool, 1, t0.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	// The import's ban of user 4 has ended; its email and range bans are in force.
	if len(p.Entries) != 4 || p.Pages != 1 {
		t.Fatalf("%d entries, %d pages: %+v", len(p.Entries), p.Pages, p.Entries)
	}
	if e := p.Entries[0]; e.Kind != "ip" || e.Target() != "198.51.100.9" || e.CreatedBy.Name != "alice" || e.EndsAt == nil {
		t.Errorf("newest = %+v", e)
	}
	if e := p.Entries[1]; e.Kind != "user" || e.Target() != "carol" || e.Member.ID != 3 || e.Reason != "r1" || e.ReasonShown != "s1" {
		t.Errorf("second = %+v", e)
	}
	// After the end, the ban leaves the list.
	p, err = board.ListBans(ctx, pool, 1, end.Add(time.Second))
	if err != nil || len(p.Entries) != 3 {
		t.Errorf("after the end: %d entries, %v", len(p.Entries), err)
	}
	if _, err := board.ListBans(ctx, pool, 2, t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("page 2 = %v, want ErrNotFound", err)
	}
}

// TestParseBanRange covers the reading of a range, without a database.
func TestParseBanRange(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.1":       "192.0.2.1/32",
		" 192.0.2.77/24 ": "192.0.2.0/24",
		"2001:db8::7/32":  "2001:db8::/32",
		"::1":             "::1/128",
	} {
		p, err := board.ParseBanRange(in)
		if err != nil || p.String() != want {
			t.Errorf("%q = %v, %v; want %s", in, p, err, want)
		}
	}
	for _, in := range []string{"", "x", "1.2.3", "1.2.3.4/", "1.2.3.4/-1", "1.2.3.4/33", "::ffff:1.2.3.4", "fe80::1%en0", "1.2.3.4 5.6.7.8", "1.2.3.4/24/8"} {
		if _, err := board.ParseBanRange(in); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}
