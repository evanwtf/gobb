// Package batch collects the small writes of page views in memory and sends
// them to the database in one transaction about every 30 s (spec 10, "Batched
// writes"): the view counts of topics, the activity times of members, and the
// addresses of signed-in visits. A page view then does not write.
//
// A crash loses the writes since the last flush. That is accepted: they are
// counts and times, not content.
package batch

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
)

// visitGap is the least time between two activity times of one member that are
// both kept. It matches the write gate of board.RecordVisit: a time less than
// five minutes after the last written one changes nothing.
const visitGap = 5 * time.Minute

// DefaultEvery is the default time between two flushes.
const DefaultEvery = 30 * time.Second

// Writer collects the writes. The zero value is not usable; call [New]. Its
// methods are safe for use by many goroutines.
type Writer struct {
	mu     sync.Mutex
	views  map[int64]int64
	visits map[int64][]time.Time
	addrs  map[addrKey]addrSeen

	flushes, failures atomic.Uint64
	// flushing makes two flushes run one after the other, so the writes of one
	// member keep their order.
	flushing sync.Mutex
}

type addrKey struct {
	user int64
	// rng is the IPv4 address, or the /64 of an IPv6 address
	// ([board.AddressRange]): trust is per /64, so the addresses of one /64
	// make one write.
	rng netip.Prefix
}

// addrSeen is the newest address of a range and the time it was used. The
// address itself goes to user_ips, as a sign-in writes it: the Spammer action
// bans each address of the member.
type addrSeen struct {
	ip netip.Addr
	at time.Time
}

// New returns an empty writer.
func New() *Writer {
	return &Writer{views: map[int64]int64{}, visits: map[int64][]time.Time{}, addrs: map[addrKey]addrSeen{}}
}

// View adds one view to topic id.
func (w *Writer) View(id int64) {
	w.mu.Lock()
	w.views[id]++
	w.mu.Unlock()
}

// Visit records that member user was active at at. A time less than five
// minutes after the last one kept for the member is dropped, as the database
// would drop it.
func (w *Writer) Visit(user int64, at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ts := w.visits[user]
	if n := len(ts); n > 0 && at.Sub(ts[n-1]) <= visitGap {
		return
	}
	w.visits[user] = append(ts, at)
}

// LastVisit returns the latest activity time of member user that waits for a
// flush, and false when none waits.
func (w *Writer) LastVisit(user int64) (time.Time, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ts := w.visits[user]
	if len(ts) == 0 {
		return time.Time{}, false
	}
	return ts[len(ts)-1], true
}

// Address records that member user used address ip at at. The unspecified
// address stands for a client whose address could not be read, and is never
// recorded.
func (w *Writer) Address(user int64, ip netip.Addr, at time.Time) {
	rng, ok := board.AddressRange(ip)
	if !ok {
		return
	}
	k := addrKey{user: user, rng: rng}
	ip = ip.Unmap().WithZone("")
	w.mu.Lock()
	if at.After(w.addrs[k].at) {
		w.addrs[k] = addrSeen{ip: ip, at: at}
	}
	w.mu.Unlock()
}

// Stats are the counts of a writer, for the metrics.
type Stats struct {
	// Flushes counts the flushes that wrote, Failures the ones that failed.
	Flushes, Failures uint64
	// Pending is the number of writes that wait: topics, member times and
	// addresses.
	Pending int
}

// Stats returns the counts of the writer.
func (w *Writer) Stats() Stats {
	w.mu.Lock()
	n := len(w.views) + len(w.visits) + len(w.addrs)
	w.mu.Unlock()
	return Stats{Flushes: w.flushes.Load(), Failures: w.failures.Load(), Pending: n}
}

// take empties the writer and returns what it held.
func (w *Writer) take() (map[int64]int64, map[int64][]time.Time, map[addrKey]addrSeen) {
	w.mu.Lock()
	defer w.mu.Unlock()
	v, t, a := w.views, w.visits, w.addrs
	w.views, w.visits, w.addrs = map[int64]int64{}, map[int64][]time.Time{}, map[addrKey]addrSeen{}
	return v, t, a
}

// putBack returns the writes of a failed flush to the writer, ahead of the ones
// that came since, so that the next flush tries them again.
func (w *Writer) putBack(views map[int64]int64, visits map[int64][]time.Time, addrs map[addrKey]addrSeen) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for id, n := range views {
		w.views[id] += n
	}
	for u, ts := range visits {
		w.visits[u] = append(ts, w.visits[u]...)
	}
	for k, seen := range addrs {
		if seen.at.After(w.addrs[k].at) {
			w.addrs[k] = seen
		}
	}
}

// Flush writes what the writer holds, in one transaction. On a failure the
// writes go back to the writer for the next flush, and Flush returns the error.
// An empty writer writes nothing and counts no flush.
func (w *Writer) Flush(ctx context.Context, pool *pgxpool.Pool) error {
	w.flushing.Lock()
	defer w.flushing.Unlock()
	views, visits, addrs := w.take()
	if len(views) == 0 && len(visits) == 0 && len(addrs) == 0 {
		return nil
	}
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := writeVisits(ctx, tx, visits); err != nil {
			return err
		}
		if err := writeAddresses(ctx, tx, addrs); err != nil {
			return err
		}
		// The views come last: their topic row locks, which a reply also
		// takes, are then held only until the COMMIT.
		return writeViews(ctx, tx, views)
	})
	if err != nil {
		w.putBack(views, visits, addrs)
		w.failures.Add(1)
		return fmt.Errorf("batch: flush: %w", err)
	}
	w.flushes.Add(1)
	return nil
}

func writeViews(ctx context.Context, tx pgx.Tx, views map[int64]int64) error {
	ids := make([]int64, 0, len(views))
	for id := range views {
		ids = append(ids, id)
	}
	// One order for every flush, so two writers never wait on each other's rows.
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	ns := make([]int64, len(ids))
	for i, id := range ids {
		ns[i] = views[id]
	}
	return board.AddViews(ctx, tx, ids, ns)
}

// writeVisits applies the activity times of each member in order, with the rule
// of board.RecordVisit. Each member has one time in a flush, or a few when the
// flushes failed.
func writeVisits(ctx context.Context, tx pgx.Tx, visits map[int64][]time.Time) error {
	users := make([]int64, 0, len(visits))
	for u := range visits {
		users = append(users, u)
	}
	sort.Slice(users, func(i, j int) bool { return users[i] < users[j] })
	for _, u := range users {
		for _, at := range visits[u] {
			if err := board.RecordVisit(ctx, tx, u, at); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeAddresses records the addresses of signed-in visits in one statement,
// with the rule of board.RecordAddresses: a member who is banned or not active
// by now gets no row.
func writeAddresses(ctx context.Context, tx pgx.Tx, addrs map[addrKey]addrSeen) error {
	keys := make([]addrKey, 0, len(addrs))
	for k := range addrs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].user != keys[j].user {
			return keys[i].user < keys[j].user
		}
		return keys[i].rng.Addr().Less(keys[j].rng.Addr())
	})
	users := make([]int64, len(keys))
	ips := make([]netip.Addr, len(keys))
	ats := make([]time.Time, len(keys))
	for i, k := range keys {
		users[i], ips[i], ats[i] = k.user, addrs[k].ip, addrs[k].at
	}
	return board.RecordAddresses(ctx, tx, users, ips, ats)
}

// Run flushes the writer every interval until ctx ends. It does not flush at the
// end: the caller flushes once more after the server has stopped, so the writes
// of the last requests are in it ([Writer.Flush]). A failed flush is logged, and
// its writes wait for the next one.
func (w *Writer) Run(ctx context.Context, pool *pgxpool.Pool, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.Flush(ctx, pool); err != nil && ctx.Err() == nil {
				slog.Warn("batched writes: flush failed; the writes wait for the next one", "err", err)
			}
		}
	}
}
