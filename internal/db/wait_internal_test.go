package db

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The WARN line of the pool timeouts: the first at once, then at most one a
// minute, with the count since the last line.
func TestTimeoutLog(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	lines := func() []string {
		s := strings.TrimSpace(buf.String())
		if s == "" {
			return nil
		}
		return strings.Split(s, "\n")
	}

	l := &timeoutLog{every: time.Minute}
	t0 := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	l.note(t0)
	if got := lines(); len(got) != 1 || !strings.Contains(got[0], "level=WARN") || !strings.Contains(got[0], "count=1") {
		t.Fatalf("after the first timeout: %q, want one WARN line with count=1", got)
	}
	for i := 1; i <= 5; i++ {
		l.note(t0.Add(time.Duration(i) * 10 * time.Second))
	}
	if got := lines(); len(got) != 1 {
		t.Fatalf("within the minute: %d lines, want 1", len(got))
	}
	l.note(t0.Add(time.Minute))
	got := lines()
	if len(got) != 2 || !strings.Contains(got[1], "count=6") {
		t.Fatalf("after a minute: %q, want a second line with count=6", got)
	}
	// After a quiet period, the next timeout writes at once.
	l.note(t0.Add(10 * time.Minute))
	if got := lines(); len(got) != 3 || !strings.Contains(got[2], "count=1") {
		t.Errorf("after a quiet period: %q, want a third line with count=1", got)
	}
}
