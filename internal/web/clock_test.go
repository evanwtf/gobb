package web

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/bundletest"
)

func loadZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestRelativeBands(t *testing.T) {
	ny := loadZone(t, "America/New_York")
	now := time.Date(2026, 10, 25, 17, 7, 0, 0, ny)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	tests := []struct {
		name string
		t    time.Time
		want string
	}{
		{"9 s", ago(9 * time.Second), "just now"},
		{"0 s", now, "just now"},
		{"10 s", ago(10 * time.Second), "10 seconds ago"},
		{"92 s", ago(92 * time.Second), "92 seconds ago"},
		{"119 s", ago(119 * time.Second), "119 seconds ago"},
		{"120 s", ago(120 * time.Second), "2 minutes ago"},
		{"98 min", ago(98 * time.Minute), "98 minutes ago"},
		{"2 h", ago(2 * time.Hour), "2 hours ago"},
		{"39 h", ago(39 * time.Hour), "39 hours ago"},
		{"48 h", ago(48 * time.Hour), "2 days ago"},
		{"96 days", ago(96 * 24 * time.Hour), "96 days ago"},
		{"179 days", ago(179 * 24 * time.Hour), "179 days ago"},
		{"14 months", time.Date(2025, 8, 20, 12, 0, 0, 0, ny), "14 months ago"},
		{"time of day not reached", time.Date(2026, 4, 25, 18, 0, 0, 0, ny), "5 months ago"},
		{"23 months", time.Date(2024, 11, 25, 12, 0, 0, 0, ny), "23 months ago"},
		{"24 months is 2 years", time.Date(2024, 10, 25, 12, 0, 0, 0, ny), "2 years ago"},
		{"3 years", time.Date(2023, 9, 1, 12, 0, 0, 0, ny), "3 years ago"},
		{"30 s ahead", now.Add(30 * time.Second), "just now"},
		{"60 s ahead", now.Add(60 * time.Second), "just now"},
		{"5 min ahead", now.Add(5 * time.Minute), "Oct 25, 2026, 5:12 pm"},
		// The edges of the bands: the last value of one and the first of the next.
		{"119 min", ago(119 * time.Minute), "119 minutes ago"},
		{"47 h", ago(47 * time.Hour), "47 hours ago"},
		{"exactly 180 days", ago(180 * 24 * time.Hour), "5 months ago"},
		{"61 s ahead", now.Add(61 * time.Second), "Oct 25, 2026, 5:08 pm"},
	}
	for _, tt := range tests {
		if got := relText(tt.t, now, ny, timeLayout); got != tt.want {
			t.Errorf("%s: relText = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// TestMonthEnd pins the month-end rule: a month is whole only when its day of the
// month and time of day have come, so a time on the 31st does not complete a month
// on the 28th, even when the 28th is the last day of the month.
func TestMonthEnd(t *testing.T) {
	ny := loadZone(t, "America/New_York")
	at := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, ny) }
	tests := []struct {
		name   string
		t, now time.Time
		want   string
	}{
		{"31 August to 28 February", at(2026, time.August, 31), at(2027, time.February, 28), "5 months ago"},
		{"31 August to 1 March", at(2026, time.August, 31), at(2027, time.March, 1), "6 months ago"},
		{"30 August to 28 February", at(2026, time.August, 30), at(2027, time.February, 28), "5 months ago"},
		{"28 August to 28 February", at(2026, time.August, 28), at(2027, time.February, 28), "6 months ago"},
	}
	for _, tt := range tests {
		if got := relText(tt.t, tt.now, ny, timeLayout); got != tt.want {
			t.Errorf("%s: relText = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// TestFutureTimeKeepsItsLayout pins that a relative time more than a minute ahead
// shows the standard text in the layout of the call: a date stays a date.
func TestFutureTimeKeepsItsLayout(t *testing.T) {
	ny := loadZone(t, "America/New_York")
	now := time.Date(2026, 10, 25, 17, 7, 0, 0, ny)
	ahead := now.Add(5 * time.Minute)
	c := clock{Loc: ny, Format: "relative", Now: now}
	for _, tt := range []struct {
		name     string
		dateOnly bool
		want     string
	}{
		{"time", false, `<time datetime="2026-10-25T21:12:00Z" title="Oct 25, 2026, 5:12 pm">Oct 25, 2026, 5:12 pm</time>`},
		{"date", true, `<time datetime="2026-10-25T21:12:00Z" title="Oct 25, 2026">Oct 25, 2026</time>`},
	} {
		if got := string(timeTag(ahead, c, tt.dateOnly)); got != tt.want {
			t.Errorf("%s:\n got %s\nwant %s", tt.name, got, tt.want)
		}
	}
}

func TestRelativeAcrossDST(t *testing.T) {
	ny := loadZone(t, "America/New_York")
	tt := time.Date(2026, 10, 31, 20, 0, 0, 0, ny)
	now := time.Date(2026, 11, 1, 20, 0, 0, 0, ny)
	if got := relText(tt, now, ny, timeLayout); got != "25 hours ago" {
		t.Errorf("relText = %q, want 25 hours ago", got)
	}
}

func TestTimeTagFormats(t *testing.T) {
	ny := loadZone(t, "America/New_York")
	london := loadZone(t, "Europe/London")
	now := time.Date(2026, 10, 25, 17, 7, 0, 0, ny)
	at := now.Add(-3 * time.Hour)
	// at is 14:07 in New York; use the time itself for the standard forms.
	tests := []struct {
		name     string
		t        time.Time
		c        clock
		dateOnly bool
		want     string
	}{
		{"standard", now, clock{Loc: ny, Format: "standard", Now: now}, false,
			`<time datetime="2026-10-25T21:07:00Z">Oct 25, 2026, 5:07 pm</time>`},
		{"iso", now, clock{Loc: ny, Format: "iso", Now: now}, false,
			`<time datetime="2026-10-25T21:07:00Z">2026-10-25T17:07:00-04:00</time>`},
		{"iso London in January", time.Date(2027, 1, 5, 9, 30, 0, 0, time.UTC),
			clock{Loc: london, Format: "iso", Now: now}, false,
			`<time datetime="2027-01-05T09:30:00Z">2027-01-05T09:30:00+00:00</time>`},
		{"relative", at, clock{Loc: ny, Format: "relative", Now: now}, false,
			`<time datetime="2026-10-25T18:07:00Z" title="Oct 25, 2026, 2:07 pm">3 hours ago</time>`},
		{"standard date", now, clock{Loc: ny, Format: "standard", Now: now}, true,
			`<time datetime="2026-10-25T21:07:00Z">Oct 25, 2026</time>`},
		{"iso date", now, clock{Loc: ny, Format: "iso", Now: now}, true,
			`<time datetime="2026-10-25T21:07:00Z">2026-10-25</time>`},
		{"relative date", at, clock{Loc: ny, Format: "relative", Now: now}, true,
			`<time datetime="2026-10-25T18:07:00Z" title="Oct 25, 2026">3 hours ago</time>`},
		{"unknown format is standard", now, clock{Loc: ny, Format: "x", Now: now}, false,
			`<time datetime="2026-10-25T21:07:00Z">Oct 25, 2026, 5:07 pm</time>`},
		{"zero clock is UTC standard", now, clock{}, false,
			`<time datetime="2026-10-25T21:07:00Z">Oct 25, 2026, 9:07 pm</time>`},
	}
	for _, tt := range tests {
		if got := string(timeTag(tt.t, tt.c, tt.dateOnly)); got != tt.want {
			t.Errorf("%s:\n got %s\nwant %s", tt.name, got, tt.want)
		}
	}
}

func TestPagesUseViewerFormat(t *testing.T) {
	b := bundletest.Small()
	b.Users[1].DateFormat = "iso" // bob, in Europe/London
	h, _ := newTestSite(t, b, Options{})

	// A guest sees the standard form, in the board zone.
	guest := bodyOf(t, get(t, h, "/t/1"))
	if !strings.Contains(guest, `<time datetime="2019-05-02T09:00:00Z">May 2, 2019, 5:00 am</time>`) {
		t.Errorf("the guest does not see the standard text:\n%s", article(t, guest, 1))
	}

	// Alice has the standard form, in her own zone.
	a := signIn(t, h, "alice", testPassword)
	rec := getAs(t, h, "/t/1", a)
	if rec.Code != http.StatusOK {
		t.Fatalf("alice: status %d", rec.Code)
	}
	if body := bodyOf(t, rec); !strings.Contains(body, `>May 2, 2019, 5:00 am</time>`) || !strings.Contains(body, `>May 1, 2019</time>`) {
		t.Errorf("alice does not see the standard text:\n%s", article(t, body, 1))
	}

	// Carol has the relative form: the text is an age and the title is the date.
	c := signIn(t, h, "carol", testPassword)
	body := bodyOf(t, getAs(t, h, "/t/1", c))
	art := article(t, body, 1)
	re := regexp.MustCompile(`<time datetime="2019-05-02T09:00:00Z" title="May 2, 2019, 9:00 am">\d+ years ago</time>`)
	if !re.MatchString(art) {
		t.Errorf("carol does not see the relative text:\n%s", art)
	}
	if !regexp.MustCompile(`Joined: <time datetime="2019-05-01T10:00:00Z" title="May 1, 2019">\d+ years ago</time>`).MatchString(art) {
		t.Errorf("carol does not see the join date as an age:\n%s", art)
	}
	// The forum index and a forum page use the same form.
	for _, target := range []string{"/", "/f/2"} {
		page := bodyOf(t, getAs(t, h, target, c))
		if !strings.Contains(page, " ago</time>") {
			t.Errorf("carol on %s sees no relative text", target)
		}
	}

	// Bob has the ISO form, in London: British summer time in May.
	bob := signIn(t, h, "bob", testPassword)
	body = bodyOf(t, getAs(t, h, "/t/1", bob))
	if !strings.Contains(article(t, body, 1), `>2019-05-02T10:00:00+01:00</time>`) {
		t.Errorf("bob does not see the ISO text:\n%s", article(t, body, 1))
	}
	if !strings.Contains(article(t, body, 1), `Joined: <time datetime="2019-05-01T10:00:00Z">2019-05-01</time>`) {
		t.Errorf("bob does not see the ISO date:\n%s", article(t, body, 1))
	}
}
