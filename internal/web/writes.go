package web

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/evanwtf/gobb/internal/board"
)

// The small writes of page views: a view of a topic, the activity time of a
// member, and the address of a member's visit. With Options.Writes set they
// wait in memory for the next flush ([batch.Writer]); without it, each one is
// written in the request.

// visitStart is the gap in activity that starts a new visit (board.RecordVisit).
const visitStart = 30 * time.Minute

// visitRefresh is the least gap between two activity times that are written.
const visitRefresh = 5 * time.Minute

// addView adds one view to topic id. A failure is logged; the page still shows.
func (s *site) addView(ctx context.Context, id int64) {
	if s.writes != nil {
		s.writes.View(id)
		return
	}
	if err := board.CountView(ctx, s.pool, id); err != nil {
		slog.Warn("cannot count a view", "topic", id, "err", err)
	}
}

// recordVisit records the navigation r of member v: the activity time, and the
// address of the visit (spec 10, "Sessions"). Without batched writes, begin has
// written the activity time already, before it read v.
//
// With batched writes, the activity time waits for the flush, except at the
// start of a new visit: that write moves the member's last visit, which the
// "new since your last visit" list of this very page reads, so it is written now
// and v follows it. It comes once per visit of a member, at most every 30
// minutes.
func (s *site) recordVisit(r *http.Request, v *board.Viewer) {
	now := s.opt.Now()
	ip := s.clientIP(r)
	if s.writes == nil {
		if err := board.RecordAddress(r.Context(), s.pool, v.UserID, ip, now); err != nil {
			slog.Warn("cannot record the address of a visit", "err", err)
		}
		return
	}
	s.writes.Address(v.UserID, ip, now)
	last := v.LastActive
	if t, ok := s.writes.LastVisit(v.UserID); ok && t.After(last) {
		last = t
	}
	switch gap := now.Sub(last); {
	case !last.IsZero() && gap > visitStart:
		if err := board.RecordVisit(r.Context(), s.pool, v.UserID, now); err != nil {
			slog.Warn("cannot record a visit", "err", err)
			return
		}
		// The write set last_visit_at to the last activity that the database
		// held, which begin read as v.LastActive.
		if !v.LastActive.IsZero() {
			v.LastVisit = v.LastActive
		}
		v.LastActive = now
	case last.IsZero() || gap > visitRefresh:
		s.writes.Visit(v.UserID, now)
	}
}
