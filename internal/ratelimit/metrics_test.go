package ratelimit_test

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/metrics"
	"github.com/evanwtf/gobb/internal/ratelimit"
)

var seq atomic.Int64

// uniqueName returns a limiter name that no earlier test or run used, so that the
// counters of the name start at 0 and `go test -count=2` pins exact values.
func uniqueName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("test_rlm_%s_%d", strings.ToLower(t.Name()), seq.Add(1))
}

func scrape(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	if _, err := metrics.Default.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// wantLines fails for each line that the scrape lacks. A line is the series and
// its value, such as `gobb_rate_limit_keys{limit="x"} 3`.
func wantLines(t *testing.T, out string, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if !strings.Contains(out, "\n"+l+"\n") {
			t.Errorf("the scrape lacks %q", l)
		}
	}
}

// rateLimitLines returns the lines of the scrape that belong to rate limits.
func rateLimitLines(out string) string {
	var keep []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "gobb_rate_limit") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

func series(name, limit string) string { return fmt.Sprintf(`%s{limit=%q}`, name, limit) }

// A known sequence on one limiter (limit 4, three keys): takes, a refusal, a
// TryTake refusal, a refund that removes an event, one that finds none, an Add,
// and an eviction. Every metric of the limiter then has an exact value.
func TestRateLimitMetricsSequence(t *testing.T) {
	name := uniqueName(t)
	l := ratelimit.New(4, time.Hour, 3).Named(name)
	now := time.Now()

	for i := range 4 {
		if ok, _ := l.Take("a", now); !ok {
			t.Fatalf("take %d of a was refused", i+1)
		}
	}
	if ok, _ := l.Take("a", now); ok {
		t.Fatal("the fifth take passed a limit of 4")
	}
	if ok, _ := l.TryTake("a", now); ok {
		t.Fatal("TryTake passed a limit of 4")
	}
	if over, _ := l.Over("a", now); !over {
		t.Fatal("Over = false at the limit")
	}
	// a has 4 of 4: one key full, the fullest at 1.
	out := scrape(t)
	wantLines(t, out,
		series("gobb_rate_limit_keys", name)+" 1",
		series("gobb_rate_limit_keys_full", name)+" 1",
		series("gobb_rate_limit_fullest_ratio", name)+" 1",
	)

	l.Refund("a", now)                     // removes one event: a has 3
	l.Refund("a", now.Add(time.Second))    // no event at that time: nothing
	l.Refund("nobody", now)                // no such key: nothing
	l.Add("b", now)                        // b has 1
	l.Add("c", now)                        // c has 1; the table holds 3 keys
	l.Add("d", now)                        // new key in a full table: one live key goes
	if ok, _ := l.TryTake("a", now); !ok { // a has 4 again, if the eviction spared it
		t.Fatal("a has no room after the refund")
	}

	out = scrape(t)
	wantLines(t, out,
		// 4 takes + 3 adds + 1 TryTake = 8 recorded events. One Take refused.
		series("gobb_rate_limit_allowed_total", name)+" 8",
		series("gobb_rate_limited_total", name)+" 1",
		series("gobb_rate_limit_refunds_total", name)+" 1",
		series("gobb_rate_limit_evictions_total", name)+" 1",
		series("gobb_rate_limit_keys", name)+" 3",
		series("gobb_rate_limit_keys_full", name)+" 1",
		series("gobb_rate_limit_fullest_ratio", name)+" 1",
		series("gobb_rate_limit_max_events", name)+" 4",
		series("gobb_rate_limit_window_seconds", name)+" 3600",
		series("gobb_rate_limit_max_keys", name)+" 3",
		// Fill after each event: a 0.25, 0.5, 0.75, 1; b, c, d 0.25; a again 1.
		// So 0.25: 4 events, 0.5: 5, 0.75: 6, 0.9: 6, 1: 8. The sum is 4.25.
		fmt.Sprintf(`gobb_rate_limit_fill_ratio_bucket{limit=%q,le="0.25"} 4`, name),
		fmt.Sprintf(`gobb_rate_limit_fill_ratio_bucket{limit=%q,le="0.5"} 5`, name),
		fmt.Sprintf(`gobb_rate_limit_fill_ratio_bucket{limit=%q,le="0.75"} 6`, name),
		fmt.Sprintf(`gobb_rate_limit_fill_ratio_bucket{limit=%q,le="0.9"} 6`, name),
		fmt.Sprintf(`gobb_rate_limit_fill_ratio_bucket{limit=%q,le="1"} 8`, name),
		fmt.Sprintf(`gobb_rate_limit_fill_ratio_bucket{limit=%q,le="+Inf"} 8`, name),
		fmt.Sprintf(`gobb_rate_limit_fill_ratio_sum{limit=%q} 4.25`, name),
		fmt.Sprintf(`gobb_rate_limit_fill_ratio_count{limit=%q} 8`, name),
	)
}

// Evictions count only the keys that had live events. A table full of keys with
// no live event drops them in makeRoom, and that is no eviction.
func TestRateLimitEvictionsCountLiveKeysOnly(t *testing.T) {
	name := uniqueName(t)
	l := ratelimit.New(2, time.Minute, 4).Named(name)
	now := time.Now()
	for _, k := range []string{"a", "b", "c", "d"} {
		l.Add(k, now.Add(-time.Hour)) // all expired by now
	}
	l.Add("e", now) // makeRoom drops the four expired keys
	if got := metrics.RateLimitEvictions.Value(name); got != 0 {
		t.Errorf("evictions after dropping expired keys = %d, want 0", got)
	}
	for _, k := range []string{"f", "g", "h"} {
		l.Add(k, now)
	}
	l.Add("i", now) // e..h are live: one goes
	if got := metrics.RateLimitEvictions.Value(name); got != 1 {
		t.Errorf("evictions = %d, want 1", got)
	}
	// A batch of more than one: maxKeys 200 drops 2 keys at once.
	big := ratelimit.New(2, time.Minute, 200).Named(name + "_big")
	for i := range 201 {
		big.Add(fmt.Sprintf("k%d", i), now)
	}
	if got := metrics.RateLimitEvictions.Value(name + "_big"); got != 2 {
		t.Errorf("evictions of a batch = %d, want 2", got)
	}
}

// A refused Take records nothing but the refusal, Over records nothing, and a
// limiter with no name records nothing and shows in no gauge.
func TestRateLimitMetricsRecordOnlyRecordedEvents(t *testing.T) {
	name := uniqueName(t)
	l := ratelimit.New(1, time.Hour, 10).Named(name)
	now := time.Now()
	l.Over("k", now)
	wantLines(t, scrape(t),
		series("gobb_rate_limit_allowed_total", name)+" 0",
		series("gobb_rate_limit_refunds_total", name)+" 0",
		series("gobb_rate_limit_evictions_total", name)+" 0",
		series("gobb_rate_limit_keys", name)+" 0",
		series("gobb_rate_limit_keys_full", name)+" 0",
		series("gobb_rate_limit_fullest_ratio", name)+" 0",
		fmt.Sprintf(`gobb_rate_limit_fill_ratio_count{limit=%q} 0`, name),
	)
	l.Take("k", now)
	l.Take("k", now) // refused
	l.TryTake("k", now)
	wantLines(t, scrape(t),
		series("gobb_rate_limit_allowed_total", name)+" 1",
		fmt.Sprintf(`gobb_rate_limit_fill_ratio_count{limit=%q} 1`, name),
	)

	before := rateLimitLines(scrape(t))
	plain := ratelimit.New(1, time.Hour, 10)
	plain.Take("k", now)
	plain.Take("k", now)
	plain.Add("j", now)
	plain.Refund("k", now)
	if after := rateLimitLines(scrape(t)); after != before {
		t.Error("a limiter with no name changed the scrape")
	}
	if strings.Contains(scrape(t), `limit=""`) {
		t.Error("a series has an empty limit label")
	}
}

// A limiter built a second time under one name replaces the first in the gauges:
// one series, with the settings of the second. The counters keep one count per
// name, so they do not double.
func TestRateLimitNamedTwiceReplaces(t *testing.T) {
	name := uniqueName(t)
	now := time.Now()
	first := ratelimit.New(2, time.Hour, 5).Named(name)
	first.Take("k", now)
	second := ratelimit.New(7, 2*time.Hour, 9).Named(name)
	second.Take("x", now)
	second.Take("y", now)

	out := scrape(t)
	for _, fam := range []string{
		"gobb_rate_limit_keys", "gobb_rate_limit_keys_full", "gobb_rate_limit_fullest_ratio",
		"gobb_rate_limit_max_events", "gobb_rate_limit_window_seconds", "gobb_rate_limit_max_keys",
	} {
		if n := strings.Count(out, "\n"+series(fam, name)+" "); n != 1 {
			t.Errorf("%s has %d series for the name, want 1", fam, n)
		}
	}
	wantLines(t, out,
		series("gobb_rate_limit_keys", name)+" 2", // the keys of the second only
		series("gobb_rate_limit_max_events", name)+" 7",
		series("gobb_rate_limit_window_seconds", name)+" 7200",
		series("gobb_rate_limit_max_keys", name)+" 9",
		series("gobb_rate_limit_allowed_total", name)+" 3", // 1 + 2: counted once each
	)
}

// Naming a limiter again renames it: the old name leaves the gauges.
func TestRateLimitRenamed(t *testing.T) {
	old, now := uniqueName(t), time.Now()
	l := ratelimit.New(1, time.Hour, 5).Named(old)
	renamed := old + "_new"
	l.Named(renamed)
	out := scrape(t)
	if strings.Contains(out, "\n"+series("gobb_rate_limit_max_keys", old)+" ") {
		t.Error("the old name is still in the gauges")
	}
	l.Take("k", now)
	wantLines(t, scrape(t), series("gobb_rate_limit_keys", renamed)+" 1")
}

// Racing takes and scrapes keep the counts exact, and pass -race.
func TestRateLimitMetricsParallel(t *testing.T) {
	name := uniqueName(t)
	l := ratelimit.New(1000, time.Hour, 50).Named(name)
	now := time.Now()
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 50 {
			_, _ = metrics.Default.WriteTo(io.Discard)
		}
	})
	for g := range 8 {
		wg.Go(func() {
			for i := range 100 {
				l.Take(fmt.Sprintf("k%d", (g+i)%20), now)
			}
		})
	}
	wg.Wait()
	if got := metrics.RateLimitAllowed.Value(name); got != 800 {
		t.Errorf("allowed = %d, want 800", got)
	}
	wantLines(t, scrape(t), fmt.Sprintf(`gobb_rate_limit_fill_ratio_count{limit=%q} 800`, name))
}
