package login

import (
	"log/slog"
	"sync"
	"time"

	"github.com/evanwtf/gobb/internal/metrics"
)

// The defaults of Defensive Mode (spec 10, "Load protection"). (owner)
const (
	// DefaultDefenseFailures: Defensive Mode turns on when the board counts
	// more than this many failed sign-ins in DefaultDefenseWindow.
	DefaultDefenseFailures = 100
	// DefaultDefenseWindow is the window of the trigger.
	DefaultDefenseWindow = 60 * time.Second
	// DefaultDefenseQuiet is how long the rate stays at or below the trigger
	// before Defensive Mode turns off.
	DefaultDefenseQuiet = 10 * time.Minute
	// DefaultDefenseGap is the least time between two tries of an untrusted
	// address while Defensive Mode is on.
	DefaultDefenseGap = 5 * time.Second
	// MaxDefenseFailures is the highest trigger. Defense keeps the times of
	// the latest trigger+1 failures.
	MaxDefenseFailures = 10000
)

// DefenseConfig sets Defensive Mode. A zero field takes its default.
type DefenseConfig struct {
	// Failures: on when more than Failures failed sign-ins fall in Window.
	Failures int
	// Window is the window of the trigger.
	Window time.Duration
	// Quiet is how long the rate stays at or below the trigger before
	// Defensive Mode turns off.
	Quiet time.Duration
	// Gap is the least time between two tries of an untrusted address while
	// Defensive Mode is on. Zero means no gap; a negative value means the
	// default.
	Gap time.Duration
}

// DefaultDefense returns the defaults: on above 100 failures in 60 s, off
// after 10 minutes at or below that rate, 5 s between the tries of an
// untrusted address.
func DefaultDefense() DefenseConfig {
	return DefenseConfig{Failures: DefaultDefenseFailures, Window: DefaultDefenseWindow,
		Quiet: DefaultDefenseQuiet, Gap: DefaultDefenseGap}
}

// Defense is Defensive Mode: the state of the board under a flood of failed
// sign-ins. It counts the failed sign-ins of the whole board, from all
// addresses together. It is safe for concurrent use. The time comes from the
// caller.
//
// It turns on at the failure that makes more than Failures in the Window up
// to it. It turns off at the first call (Record, On or Refresh) at or after
// Quiet past the moment that the rate fell back to Failures or fewer. Each
// change writes one log line, with counts and no address, and sets the gauge
// gobb_defensive_mode.
type Defense struct {
	cfg DefenseConfig

	mu sync.Mutex
	// ring holds the times of the latest Failures+1 failures. next is the slot
	// of the next one; full says that every slot holds a time.
	ring []time.Time
	next int
	full bool
	on   bool
	// since is when Defensive Mode turned on, and during counts the failures
	// since then.
	since  time.Time
	during int64
}

// NewDefense returns Defensive Mode, off, with cfg.
func NewDefense(cfg DefenseConfig) *Defense {
	d := DefaultDefense()
	if cfg.Failures > 0 {
		d.Failures = min(cfg.Failures, MaxDefenseFailures)
	}
	if cfg.Window > 0 {
		d.Window = cfg.Window
	}
	if cfg.Quiet > 0 {
		d.Quiet = cfg.Quiet
	}
	if cfg.Gap >= 0 {
		d.Gap = cfg.Gap
	}
	return &Defense{cfg: d, ring: make([]time.Time, d.Failures+1)}
}

// Config returns the settings of d.
func (d *Defense) Config() DefenseConfig { return d.cfg }

// oldest returns the time of the oldest failure in the ring, and false when
// the ring is not yet full.
func (d *Defense) oldest() (time.Time, bool) {
	if !d.full {
		return time.Time{}, false
	}
	return d.ring[d.next], true
}

// over reports whether more than Failures failures fall in the window that
// ends at now.
func (d *Defense) over(now time.Time) bool {
	t, ok := d.oldest()
	return ok && now.Sub(t) < d.cfg.Window
}

// Record counts one failed sign-in at now, and turns Defensive Mode on when the
// count in the window passes the trigger.
func (d *Defense) Record(now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ring[d.next] = now
	d.next = (d.next + 1) % len(d.ring)
	if d.next == 0 {
		d.full = true
	}
	if d.on {
		d.during++
	}
	d.refresh(now)
}

// On reports whether Defensive Mode is on at now. It turns it off first when
// the quiet time has passed.
func (d *Defense) On(now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.refresh(now)
	return d.on
}

// Refresh turns Defensive Mode off when the quiet time has passed at now. The
// server calls it on a timer, so the line that says "off" comes on time even
// with no sign-in.
func (d *Defense) Refresh(now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.refresh(now)
}

// refresh changes the state at now, under the lock.
func (d *Defense) refresh(now time.Time) {
	switch {
	case !d.on && d.over(now):
		d.on, d.since, d.during = true, now, int64(d.cfg.Failures+1)
		metrics.DefensiveMode.Set(1)
		slog.Warn("defensive mode on", "failed_signins", d.cfg.Failures+1,
			"window_seconds", d.cfg.Window.Seconds(), "trigger", d.cfg.Failures)
	case d.on && !d.over(now):
		// The latest moment with more than Failures in the window is the
		// oldest failure in the ring plus the window. Any earlier moment over
		// the trigger ends sooner, so this never turns off early.
		t, _ := d.oldest()
		if below := t.Add(d.cfg.Window); !now.Before(below.Add(d.cfg.Quiet)) {
			d.on = false
			metrics.DefensiveMode.Set(0)
			slog.Info("defensive mode off", "on_seconds", int64(now.Sub(d.since)/time.Second),
				"failed_signins", d.during, "quiet_seconds", d.cfg.Quiet.Seconds())
		}
	}
}
