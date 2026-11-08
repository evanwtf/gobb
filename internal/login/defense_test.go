package login_test

import (
	"bytes"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/metrics"
)

// captureLog sends the default logger to a buffer for the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

// lines returns the non-empty lines of buf.
func lines(buf *bytes.Buffer) []string {
	var out []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// Defensive Mode turns on at the 101st failed sign-in in 60 s, and off 10
// minutes after the rate falls back to 100 or fewer. Each change writes one
// line, with counts and no address, and sets the gauge.
func TestDefenseOnAndOff(t *testing.T) {
	buf := captureLog(t)
	d := login.NewDefense(login.DefaultDefense())
	// 100 failures over 59 s: not more than 100 in 60 s.
	for i := range 100 {
		d.Record(now.Add(time.Duration(i) * 590 * time.Millisecond))
	}
	at := now.Add(59 * time.Second)
	if d.On(at) {
		t.Fatal("on after 100 failures in 60 s")
	}
	if n := len(lines(buf)); n != 0 {
		t.Fatalf("%d log lines before the trigger", n)
	}
	// The 101st, still inside 60 s of the first: on.
	d.Record(at)
	if !d.On(at) {
		t.Fatal("off after 101 failures in 60 s")
	}
	if got := metrics.DefensiveMode.Value(); got != 1 {
		t.Errorf("gobb_defensive_mode = %v, want 1", got)
	}
	got := lines(buf)
	if len(got) != 1 || !strings.Contains(got[0], "level=WARN") || !strings.Contains(got[0], `msg="defensive mode on"`) ||
		!strings.Contains(got[0], "failed_signins=101") {
		t.Fatalf("log after the trigger = %q, want one WARN line that says on, with failed_signins=101", got)
	}
	// The rate falls to 100 when the first failure leaves the window: at
	// now+60 s. Defensive Mode stays on for 10 minutes after that.
	below := now.Add(60 * time.Second)
	if !d.On(below.Add(10*time.Minute - time.Second)) {
		t.Fatal("off before 10 quiet minutes")
	}
	d.Refresh(below.Add(10 * time.Minute))
	if d.On(below.Add(10 * time.Minute)) {
		t.Fatal("still on after 10 quiet minutes")
	}
	if got := metrics.DefensiveMode.Value(); got != 0 {
		t.Errorf("gobb_defensive_mode = %v, want 0", got)
	}
	got = lines(buf)
	if len(got) != 2 || !strings.Contains(got[1], "level=INFO") || !strings.Contains(got[1], `msg="defensive mode off"`) ||
		!strings.Contains(got[1], "on_seconds=601") {
		t.Fatalf("log = %q, want a second, INFO line that says off after 601 s on", got)
	}
	for _, l := range got {
		if strings.Contains(l, "192.0.2") || strings.Contains(l, "198.51.100") {
			t.Errorf("a log line holds an address: %q", l)
		}
	}
}

// A new burst during the quiet time keeps Defensive Mode on: the 10 minutes
// count from the end of the last burst.
func TestDefenseQuietRestarts(t *testing.T) {
	captureLog(t)
	d := login.NewDefense(login.DefaultDefense())
	burst := func(at time.Time) {
		for range 101 {
			d.Record(at)
		}
	}
	burst(now)
	second := now.Add(5 * time.Minute)
	burst(second)
	if !d.On(now.Add(60*time.Second + 10*time.Minute)) {
		t.Error("off 10 minutes after the first burst, with a second burst in between")
	}
	if d.On(second.Add(60*time.Second + 10*time.Minute)) {
		t.Error("on 10 quiet minutes after the second burst")
	}
}

// onDefense returns Defensive Mode, already on at now.
func onDefense(t *testing.T) *login.Defense {
	t.Helper()
	captureLog(t)
	d := login.NewDefense(login.DefaultDefense())
	for range login.DefaultDefenseFailures + 1 {
		d.Record(now)
	}
	if !d.On(now) {
		t.Fatal("Defensive Mode did not turn on")
	}
	return d
}

// While Defensive Mode is on, an untrusted address gets no free failure: its
// first failure starts the 15 s wait. A trusted address keeps the normal
// schedule: two free failures.
func TestDefenseUntrustedGetsNoFreeTry(t *testing.T) {
	svc, f := scheduled(t, onDefense(t))
	stranger := netip.MustParseAddr("198.51.100.40")
	refused := metrics.SigninRefusals.Value(login.ReasonDefensive)
	walk(t, svc, f, []step{{0, 0}, {time.Second, 14 * time.Second}, {15 * time.Second, 0}, {16 * time.Second, 14 * time.Second}},
		func(i int) string { return []string{"g0", "g1", "g2", "g3"}[i] }, func(int) netip.Addr { return stranger }, login.ReasonDefensive)
	if got := metrics.SigninRefusals.Value(login.ReasonDefensive) - refused; got != 2 {
		t.Errorf("defensive refusals +%d, want +2", got)
	}
}

func TestDefenseTrustedKeepsTheSchedule(t *testing.T) {
	d := onDefense(t)
	_, pool := newService(t)
	f := &fakeVerify{}
	svc := login.NewWith(pool, login.Options{Schedule: login.NewSchedule(login.DefaultSchedule()), Defense: d, Verify: f.verify})
	svc.Floor = 0
	home := netip.MustParseAddr("192.0.2.70")
	exec(t, pool, `insert into user_ips (user_id, ip, last_seen_at) values (2, $1, $2)`, home, now)
	// bob's home is a trusted address. Any name from it keeps the normal
	// schedule: three tries at once, then 15 s.
	walk(t, svc, f, []step{{0, 0}, {0, 0}, {0, 0}, {time.Second, 14 * time.Second}},
		func(i int) string { return []string{"x0", "x1", "x2", "x3"}[i] }, func(int) netip.Addr { return home }, login.ReasonAddress)
}

// While Defensive Mode is on, an untrusted address waits at least 5 s between
// two tries, even after a good sign-in.
func TestDefenseGapBetweenTries(t *testing.T) {
	svc, _ := scheduled(t, onDefense(t))
	from := netip.MustParseAddr("198.51.100.41")
	if id, err := svc.Check(t.Context(), "alice", "right", from, now); err != nil || id != 1 {
		t.Fatalf("first try = %d, %v", id, err)
	}
	_, err := svc.Check(t.Context(), "alice", "right", from, now.Add(2*time.Second))
	if l, ok := isLimited(err); !ok || l.Reason != login.ReasonDefensive || l.RetryAfter != 3*time.Second {
		t.Fatalf("a try 2 s later = %v, want a defensive wait of 3 s", err)
	}
	if id, err := svc.Check(t.Context(), "alice", "right", from, now.Add(5*time.Second)); err != nil || id != 1 {
		t.Errorf("a try 5 s later = %d, %v", id, err)
	}
}

// A banned member's addresses are no longer trusted: in Defensive Mode they get
// the strict rules.
func TestDefenseBannedMemberIsNotTrusted(t *testing.T) {
	d := onDefense(t)
	_, pool := newService(t)
	f := &fakeVerify{}
	svc := login.NewWith(pool, login.Options{Schedule: login.NewSchedule(login.DefaultSchedule()), Defense: d, Verify: f.verify})
	svc.Floor = 0
	home := netip.MustParseAddr("192.0.2.71")
	exec(t, pool, `insert into user_ips (user_id, ip, last_seen_at) values (2, $1, $2)`, home, now)
	exec(t, pool, `insert into bans (kind, user_id, reason, reason_shown, created_at) values ('user', 2, '', '', $1)`, now)
	if _, err := svc.Check(t.Context(), "x0", "wrong", home, now); !errors.Is(err, login.ErrWrong) {
		t.Fatal(err)
	}
	_, err := svc.Check(t.Context(), "x1", "wrong", home, now)
	if l, ok := isLimited(err); !ok || l.Reason != login.ReasonDefensive {
		t.Errorf("the second try from a banned member's address = %v, want a defensive wait", err)
	}
	// An inactive member's address is not trusted either.
	exec(t, pool, `insert into user_ips (user_id, ip, last_seen_at) values (4, '192.0.2.72', $1)`, now)
	from := netip.MustParseAddr("192.0.2.72")
	_, _ = svc.Check(t.Context(), "x2", "wrong", from, now)
	if _, ok := isLimited(func() error { _, err := svc.Check(t.Context(), "x3", "wrong", from, now); return err }()); !ok {
		t.Error("an inactive member's address kept its free failures")
	}
}

// Every failed sign-in of a service counts in its Defensive Mode; a refused try
// and a good sign-in do not.
func TestFailuresFeedDefense(t *testing.T) {
	captureLog(t)
	d := login.NewDefense(login.DefenseConfig{Failures: 3})
	svc, _ := scheduled(t, d)
	for i := range 3 {
		_, _ = svc.Check(t.Context(), "nobody", "wrong", addr(i), now)
	}
	_, _ = svc.Check(t.Context(), "alice", "right", addr(10), now)
	_, _ = svc.Check(t.Context(), "nobody", "wrong", addr(11), now) // refused by the name
	if d.On(now) {
		t.Fatal("on after 3 failures, a success and a refusal, with a trigger of 3")
	}
	_, _ = svc.Check(t.Context(), "carol", "wrong", addr(12), now)
	if !d.On(now) {
		t.Error("off after 4 failures, with a trigger of 3")
	}
}
