package importer

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/dbtest"
	"github.com/evanwtf/gobb/internal/session"
)

// The import stores each member address as last seen at the time of the
// import, so the cleanup keeps it one year from the import and not a second
// longer. An IPv4-mapped address becomes its IPv4 address. The addresses of a
// member whom a user ban holds at the import are left out.
func TestImportMemberAddresses(t *testing.T) {
	// The test writes addresses from the documentation ranges, which a real
	// import refuses.
	oldAllowed := bundle.MemberAddressAllowed
	bundle.MemberAddressAllowed = func(a netip.Addr) bool { return a.IsValid() }
	t.Cleanup(func() { bundle.MemberAddressAllowed = oldAllowed })
	pool := dbtest.New(t)
	importAt := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	oldNow := now
	t.Cleanup(func() { now = oldNow })
	now = func() time.Time { return importAt }

	bob := int64(2)
	dir := writeBoard(t, func(b *bundletest.Board) {
		b.MemberAddresses = []bundle.MemberAddress{
			{UserID: 1, IP: "192.0.2.10"},
			{UserID: 1, IP: "::ffff:192.0.2.11"},
			{UserID: 2, IP: "2001:db8::5"},
			{UserID: 3, IP: "198.51.100.7"},
		}
		b.Bans = append(b.Bans, bundle.Ban{Kind: "user", UserID: &bob, Reason: "r", ReasonShown: "r",
			CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)})
	})
	counts, err := Run(context.Background(), pool, dir, Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if counts["member_addresses"] != 3 {
		t.Errorf("counts[member_addresses] = %d, want 3 (bob's row is left out)", counts["member_addresses"])
	}
	rows, err := pool.Query(t.Context(), `select user_id, host(ip), family(ip), last_seen_at from user_ips order by user_id, ip`)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		user   int64
		host   string
		family int
		seen   time.Time
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.user, &r.host, &r.family, &r.seen); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []row{{1, "192.0.2.10", 4, importAt}, {1, "192.0.2.11", 4, importAt}, {3, "198.51.100.7", 4, importAt}}
	if len(got) != len(want) {
		t.Fatalf("user_ips = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].user != want[i].user || got[i].host != want[i].host || got[i].family != want[i].family || !got[i].seen.Equal(want[i].seen) {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// One year after the import, less a second, the addresses stay.
	year := 365 * 24 * time.Hour
	if _, ips, err := session.Cleanup(t.Context(), pool, importAt.Add(year-time.Second), 0); err != nil || ips != 0 {
		t.Errorf("cleanup just before a year = %d addresses, %v; want 0", ips, err)
	}
	if _, ips, err := session.Cleanup(t.Context(), pool, importAt.Add(year+time.Second), 0); err != nil || ips != 3 {
		t.Errorf("cleanup just after a year = %d addresses, %v; want 3", ips, err)
	}
}

// A bundle without the file imports no address, and says 0.
func TestImportWithoutMemberAddresses(t *testing.T) {
	pool := dbtest.New(t)
	counts := mustRun(t, pool, writeBoard(t, nil), t.TempDir(), false)
	if n, ok := counts["member_addresses"]; !ok || n != 0 {
		t.Errorf("counts[member_addresses] = %d, %t; want 0, true", n, ok)
	}
	if n := count(t, pool, "user_ips"); n != 0 {
		t.Errorf("user_ips holds %d rows, want 0", n)
	}
}
