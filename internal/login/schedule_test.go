package login_test

import (
	"errors"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/login"
)

// scheduled returns a service with the default schedule, a fake Verify and no
// floor, with no Defensive Mode unless defense is set.
func scheduled(t *testing.T, defense *login.Defense) (*login.Service, *fakeVerify) {
	t.Helper()
	_, pool := newService(t)
	f := &fakeVerify{}
	svc := login.NewWith(pool, login.Options{Schedule: login.NewSchedule(login.DefaultSchedule()), Defense: defense, Verify: f.verify})
	svc.Floor = 0
	return svc, f
}

// step is one try of a schedule walk: the time after the start, and whether
// the schedule refuses it with the given wait, or lets the password check run.
type step struct {
	at   time.Duration
	wait time.Duration // 0: the check runs and fails
}

// walk runs steps with a wrong password. name(i) and from(i) pick the name and
// the address of try i. A refused try must run no password check.
func walk(t *testing.T, svc *login.Service, f *fakeVerify, steps []step, name func(i int) string, from func(i int) netip.Addr, reason string) {
	t.Helper()
	for i, s := range steps {
		before := f.calls.Load()
		_, err := svc.Check(t.Context(), name(i), "wrong", from(i), now.Add(s.at))
		if s.wait == 0 {
			if !errors.Is(err, login.ErrWrong) {
				t.Fatalf("step %d at +%v: %v, want ErrWrong", i, s.at, err)
			}
			if f.calls.Load() != before+1 {
				t.Fatalf("step %d at +%v: the password check did not run once", i, s.at)
			}
			continue
		}
		l, ok := isLimited(err)
		if !ok || l.RetryAfter != s.wait || l.Reason != reason {
			t.Fatalf("step %d at +%v: %v, want a %s wait of %v", i, s.at, err, reason, s.wait)
		}
		if f.calls.Load() != before {
			t.Fatalf("step %d at +%v: a refused try ran the password check", i, s.at)
		}
	}
}

// The owner's schedule, step by step: two free failures, then waits of 15 s,
// 15 s, 60 s, 60 s, 60 s, then 900 s before every later try. A try made before
// its wait ends is refused, runs no password check, and does not move the wait.
func scheduleSteps() []step {
	s := time.Second
	return []step{
		{0, 0}, {0, 0}, // the two free failures
		{0, 0},            // failure 3: the next try waits 15 s
		{14 * s, 1 * s},   // refused; the count does not advance
		{15 * s, 0},       // failure 4: 15 s
		{29 * s, 1 * s},   //
		{30 * s, 0},       // failure 5: 60 s
		{89 * s, 1 * s},   //
		{90 * s, 0},       // failure 6: 60 s
		{150 * s, 0},      // failure 7: 60 s
		{150 * s, 60 * s}, //
		{210 * s, 0},      // failure 8: 900 s
		{1109 * s, 1 * s}, //
		{1110 * s, 0},     // failure 9: 900 s
		{1110 * s, 900 * s},
		{2010 * s, 0}, // failure 10: 900 s
		{2011 * s, 899 * s},
	}
}

func TestScheduleOfTheAddress(t *testing.T) {
	svc, f := scheduled(t, nil)
	from := netip.MustParseAddr("192.0.2.10")
	// A new name for every try, so only the address counts.
	walk(t, svc, f, scheduleSteps(), func(i int) string { return fmt.Sprintf("guess%d", i) },
		func(int) netip.Addr { return from }, login.ReasonAddress)
}

func TestScheduleOfTheName(t *testing.T) {
	svc, f := scheduled(t, nil)
	// A new address for every try, so only the name counts.
	walk(t, svc, f, scheduleSteps(), func(int) string { return "alice" }, addr, login.ReasonName)
}

// An IPv6 client is one /64: the walk holds across its addresses.
func TestScheduleOfAnIPv6Range(t *testing.T) {
	svc, f := scheduled(t, nil)
	walk(t, svc, f, scheduleSteps(), func(i int) string { return fmt.Sprintf("guess%d", i) },
		func(i int) netip.Addr { return netip.MustParseAddr(fmt.Sprintf("2001:db8:5:6::%x", i+1)) }, login.ReasonAddress)
}

// A good sign-in clears the count of the name, and not the count of the address.
func TestGoodSignInClearsTheNameOnly(t *testing.T) {
	svc, f := scheduled(t, nil)
	ctx := t.Context()
	home := netip.MustParseAddr("192.0.2.20")
	for range 3 {
		if _, err := svc.Check(ctx, "alice", "wrong", home, now); !errors.Is(err, login.ErrWrong) {
			t.Fatal(err)
		}
	}
	at := now.Add(15 * time.Second)
	if id, err := svc.Check(ctx, "alice", "right", home, at); err != nil || id != 1 {
		t.Fatalf("the right password after the wait = %d, %v", id, err)
	}
	// The name is clear: three failures from strangers leave no wait.
	walk(t, svc, f, []step{{15 * time.Second, 0}, {15 * time.Second, 0}, {15 * time.Second, 0}, {15 * time.Second, 15 * time.Second}},
		func(int) string { return "alice" }, func(i int) netip.Addr { return addr(100 + i) }, login.ReasonName)
	// The address keeps its three failures: the next failure starts a wait.
	walk(t, svc, f, []step{{15 * time.Second, 0}, {16 * time.Second, 14 * time.Second}},
		func(i int) string { return fmt.Sprintf("other%d", i) }, func(int) netip.Addr { return home }, login.ReasonAddress)
}

// A count clears after 24 hours with no failure, and not a second sooner.
func TestScheduleResetsAfter24Hours(t *testing.T) {
	for _, c := range []struct {
		quiet time.Duration
		wait  time.Duration // the wait after one more failure
	}{
		{24*time.Hour - time.Second, 900 * time.Second}, // still failure 9
		{24 * time.Hour, 0},                             // failure 1 of a new count
	} {
		t.Run(c.quiet.String(), func(t *testing.T) {
			svc, f := scheduled(t, nil)
			from := netip.MustParseAddr("192.0.2.30")
			name := func(i int) string { return fmt.Sprintf("guess%d", i) }
			// Eight failures: the last at +210 s, so the wait is 900 s.
			steps := []step{{0, 0}, {0, 0}, {0, 0}, {15 * time.Second, 0}, {30 * time.Second, 0},
				{90 * time.Second, 0}, {150 * time.Second, 0}, {210 * time.Second, 0}}
			walk(t, svc, f, steps, name, func(int) netip.Addr { return from }, login.ReasonAddress)
			last := 210*time.Second + c.quiet
			walk(t, svc, f, []step{{last, 0}}, func(int) string { return "again" }, func(int) netip.Addr { return from }, login.ReasonAddress)
			_, err := svc.Check(t.Context(), "again2", "wrong", from, now.Add(last))
			if c.wait == 0 {
				if !errors.Is(err, login.ErrWrong) {
					t.Errorf("the second try after the reset = %v, want a free failure", err)
				}
				return
			}
			if l, ok := isLimited(err); !ok || l.RetryAfter != c.wait {
				t.Errorf("the next try = %v, want a wait of %v", err, c.wait)
			}
		})
	}
}

// WaitAfter gives the waits of the owner's schedule, and with no free failure
// (Defensive Mode for an untrusted address) the first failure starts the 15 s.
func TestWaitAfter(t *testing.T) {
	c := login.DefaultSchedule()
	s := time.Second
	normal := []time.Duration{0, 0, 15 * s, 15 * s, 60 * s, 60 * s, 60 * s, 900 * s, 900 * s}
	for i, want := range normal {
		if got := c.WaitAfter(i+1, c.FreeFailures); got != want {
			t.Errorf("WaitAfter(%d, 2) = %v, want %v", i+1, got, want)
		}
	}
	strict := []time.Duration{15 * s, 15 * s, 60 * s, 60 * s, 60 * s, 900 * s}
	for i, want := range strict {
		if got := c.WaitAfter(i+1, 0); got != want {
			t.Errorf("WaitAfter(%d, 0) = %v, want %v", i+1, got, want)
		}
	}
	if got := c.WaitAfter(0, 0); got != 0 {
		t.Errorf("WaitAfter(0, 0) = %v, want 0", got)
	}
}

// A full table drops the counts that changed least recently, never a count
// with a try in progress, and the newest counts stay.
func TestScheduleTableIsBounded(t *testing.T) {
	_, pool := newService(t)
	f := &fakeVerify{}
	sched := login.NewSchedule(login.ScheduleConfig{FreeFailures: 0, LastWait: time.Hour, MaxKeys: 16})
	svc := login.NewWith(pool, login.Options{Schedule: sched, Verify: f.verify})
	svc.Floor = 0
	for i := range 40 {
		_, _ = svc.Check(t.Context(), fmt.Sprintf("n%d", i), "wrong", addr(i), now.Add(time.Duration(i)*time.Second))
	}
	if a, _ := sched.Keys(); a > 16 {
		t.Errorf("the address table holds %d counts, want at most 16", a)
	}
	// The newest address still waits.
	if _, ok := isLimited(func() error {
		_, err := svc.Check(t.Context(), "x", "wrong", addr(39), now.Add(40*time.Second))
		return err
	}()); !ok {
		t.Error("the newest count was dropped")
	}
}
