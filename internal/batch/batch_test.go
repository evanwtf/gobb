package batch_test

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/batch"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

func views(t *testing.T, pool *pgxpool.Pool, id int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), `select view_count from topics where id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFlushWritesViewsVisitsAndAddresses(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	ctx := t.Context()
	w := batch.New()
	before1, before2 := views(t, pool, 1), views(t, pool, 2)
	for range 3 {
		w.View(1)
	}
	w.View(2)
	// carol (3) has no activity yet; two times 10 minutes apart are both kept.
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	w.Visit(3, at)
	w.Visit(3, at.Add(time.Minute)) // within five minutes: dropped
	w.Visit(3, at.Add(10*time.Minute))
	w.Address(3, netip.MustParseAddr("198.51.100.9"), at)
	w.Address(3, netip.MustParseAddr("::ffff:198.51.100.9"), at.Add(time.Minute))
	w.Address(3, netip.IPv4Unspecified(), at)
	if st := w.Stats(); st.Pending != 4 {
		t.Errorf("pending = %d, want 4 (two topics, one member, one address)", st.Pending)
	}
	// Nothing is written before the flush.
	if views(t, pool, 1) != before1 {
		t.Fatal("a view reached the database before the flush")
	}
	if err := w.Flush(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if got := views(t, pool, 1); got != before1+3 {
		t.Errorf("topic 1 views = %d, want %d", got, before1+3)
	}
	if got := views(t, pool, 2); got != before2+1 {
		t.Errorf("topic 2 views = %d, want %d", got, before2+1)
	}
	var active time.Time
	if err := pool.QueryRow(ctx, `select last_active_at from users where id = 3`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if !active.Equal(at.Add(10 * time.Minute)) {
		t.Errorf("last_active_at = %s, want %s", active, at.Add(10*time.Minute))
	}
	var seen time.Time
	var n int
	if err := pool.QueryRow(ctx, `select count(*), max(last_seen_at) from user_ips where user_id = 3`).Scan(&n, &seen); err != nil {
		t.Fatal(err)
	}
	if n != 1 || !seen.Equal(at.Add(time.Minute)) {
		t.Errorf("user_ips of carol: %d rows, last %s; want 1 row at %s", n, seen, at.Add(time.Minute))
	}
	st := w.Stats()
	if st.Flushes != 1 || st.Failures != 0 || st.Pending != 0 {
		t.Errorf("stats = %+v", st)
	}
	// An empty flush writes nothing and counts nothing.
	if err := w.Flush(ctx, pool); err != nil || w.Stats().Flushes != 1 {
		t.Errorf("an empty flush: %v, %+v", err, w.Stats())
	}
}

func TestFlushSkipsTheAddressOfABannedMember(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	ctx := t.Context()
	w := batch.New()
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	w.Address(3, netip.MustParseAddr("198.51.100.10"), at)
	w.Address(4, netip.MustParseAddr("198.51.100.11"), at) // dave is inactive
	// The ban comes after the visit and before the flush.
	if _, err := pool.Exec(ctx, `insert into bans (kind, user_id, created_at) values ('user', 3, $1)`, at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from user_ips where user_id in (3, 4)`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d addresses of a banned or inactive member were written", n)
	}
}

func TestFailedFlushKeepsTheWrites(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	w := batch.New()
	before := views(t, pool, 1)
	w.View(1)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := w.Flush(ctx, pool); err == nil {
		t.Fatal("a flush with an ended context succeeded")
	}
	if st := w.Stats(); st.Failures != 1 || st.Pending != 1 {
		t.Errorf("stats after a failure = %+v", st)
	}
	w.View(1)
	if err := w.Flush(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if got := views(t, pool, 1); got != before+2 {
		t.Errorf("views = %d, want %d: the failed flush lost its writes", got, before+2)
	}
}

func TestRunFlushesOnTheTickAndTheLastFlushAfterIt(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	w := batch.New()
	before := views(t, pool, 1)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx, pool, 20*time.Millisecond)
	}()
	w.View(1)
	deadline := time.Now().Add(5 * time.Second)
	for views(t, pool, 1) != before+1 {
		if time.Now().After(deadline) {
			t.Fatal("the tick did not flush the view")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	// A view after the timer stopped waits for the flush at shutdown.
	w.View(1)
	if views(t, pool, 1) != before+1 {
		t.Fatal("a view was written with no flush")
	}
	if err := w.Flush(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if got := views(t, pool, 1); got != before+2 {
		t.Errorf("views after the last flush = %d, want %d", got, before+2)
	}
}

// Trust is per /64: the addresses of one IPv6 /64 make one write, of the
// newest address.
func TestFlushMasksIPv6To64(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	w := batch.New()
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	w.Address(3, netip.MustParseAddr("2001:db8:1:2::5"), at)
	w.Address(3, netip.MustParseAddr("2001:db8:1:2:aaaa::9"), at.Add(time.Minute))
	w.Address(3, netip.MustParseAddr("2001:db8:1:3::5"), at)
	if st := w.Stats(); st.Pending != 2 {
		t.Errorf("pending = %d, want 2 (two /64s)", st.Pending)
	}
	if err := w.Flush(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(t.Context(), `select ip::text from user_ips where user_id = 3 and family(ip) = 6 order by ip`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	if want := []string{"2001:db8:1:2:aaaa::9/128", "2001:db8:1:3::5/128"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("user_ips = %v, want %v", got, want)
	}
}
