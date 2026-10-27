package ratelimit

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/metrics"
)

var base = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

// The scan counts the keys that are at the limit now and finds the fullest key.
// It counts only the events inside the window, reads without trimming, and holds
// the keys that no event keeps alive until a write drops them.
func TestScan(t *testing.T) {
	l := New(3, time.Minute, 100)
	l.Add("three", base)
	l.Add("three", base.Add(time.Second))
	l.Add("three", base.Add(2*time.Second))
	l.Add("two", base)
	l.Add("two", base.Add(time.Second))
	l.Add("one", base)
	// "mixed" has one event that leaves the window before the scan and one that
	// stays.
	l.Add("mixed", base)
	l.Add("mixed", base.Add(50*time.Second))

	if got := l.scan(base.Add(3 * time.Second)); got != (fill{keys: 4, full: 1, fullest: 1}) {
		t.Errorf("scan at +3s = %+v", got)
	}
	// At +61s the window is (+1s, +61s]. "three" keeps its event at +2s, "two"
	// none, "mixed" its event at +50s, and "one" none: it is still held, and it
	// counts for nothing.
	if got := l.scan(base.Add(61 * time.Second)); got != (fill{keys: 4, full: 0, fullest: 1.0 / 3}) {
		t.Errorf("scan at +61s = %+v", got)
	}
	// At +3m nothing is live; the keys stay until a write drops them.
	if got := l.scan(base.Add(3 * time.Minute)); got != (fill{keys: 4}) {
		t.Errorf("scan at +3m = %+v", got)
	}
	if l.Len() != 4 || !l.Has("one") {
		t.Errorf("the scan changed the keys: Len = %d", l.Len())
	}
}

// One scrape reads a limiter once, at one time. The clock here jumps two windows
// ahead after its first reading, so a gauge that read the time again would see
// the key gone while another gauge saw it at the limit. The test swaps the
// package variable clock, which is safe only because no test in this package
// runs in parallel.
func TestScrapeGaugesAgree(t *testing.T) {
	l := New(2, time.Minute, 10).Named("test_agree")
	l.Add("k", base)
	l.Add("k", base)
	reads := 0
	clock = func() time.Time {
		reads++
		return base.Add(time.Duration(reads-1) * 2 * time.Minute)
	}
	t.Cleanup(func() { clock = time.Now })

	var b strings.Builder
	if _, err := metrics.Default.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"\ngobb_rate_limit_keys{limit=\"test_agree\"} 1\n",
		"\ngobb_rate_limit_keys_full{limit=\"test_agree\"} 1\n",
		"\ngobb_rate_limit_fullest_ratio{limit=\"test_agree\"} 1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the scrape lacks %q", want)
		}
	}
	if reads != 1 {
		t.Errorf("one scrape read the clock %d times, want 1", reads)
	}
}

func TestScanEmpty(t *testing.T) {
	if got := New(3, time.Minute, 10).scan(base); got != (fill{}) {
		t.Errorf("scan of an empty limiter = %+v", got)
	}
}

// BenchmarkScan is the cost of the scrape-time scan, and the time that it holds
// the lock, at maxKeys keys. The keys hold one to five events of a limit of 20.
func BenchmarkScan(b *testing.B) {
	for _, n := range []int{10_000, 100_000} {
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			l := New(20, time.Hour, n)
			for i := range n {
				key := fmt.Sprintf("key-%d", i)
				for j := range 1 + i%5 {
					l.Add(key, base.Add(time.Duration(i*10+j)*time.Millisecond))
				}
			}
			now := base.Add(time.Minute)
			b.ResetTimer()
			for b.Loop() {
				l.scan(now)
			}
		})
	}
}
