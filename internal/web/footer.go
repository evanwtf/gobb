package web

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/version"
)

// reqClockKey is the context key of the clock of a request.
type reqClockKey struct{}

// reqClock is what the context holds of the request: the time that it entered the
// handler chain, and whether it is a GET or HEAD request.
type reqClock struct {
	start time.Time
	read  bool
}

// withClock starts the clock of a request with method.
func withClock(ctx context.Context, method string) context.Context {
	return context.WithValue(ctx, reqClockKey{}, reqClock{start: time.Now(), read: method == http.MethodGet || method == http.MethodHead})
}

// timed starts the clock and the statement counter of a request. The context
// carries both, and nothing else changes: the counter is only a number.
func timed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := db.WithCounter(withClock(r.Context(), r.Method))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// footerLine returns the build line of a page that answers with status.
//
// The plain line holds the version and the build. It is the line of every
// answer to a POST, of every non-2xx answer, and of a page that has no clock in
// its context (an error page that needs no database).
//
// A 2xx answer to a GET or HEAD request adds the time since the request entered
// the handler chain. An admin (the board-wide admin right) also sees the count
// of the statements that the request ran. Nobody else does: the count is exact,
// and on a page like the failed sign-in it told a stranger whether a username
// exists. The time alone shows nothing of the data.
//
// The time stops before the last run of the layout template, which takes about
// a millisecond. The count has to stop there too: the template runs no query.
func footerLine(ctx context.Context, status int, admin bool) string {
	tail := plainFooter()
	if ctx == nil || status < 200 || status > 299 {
		return tail
	}
	clk, ok := ctx.Value(reqClockKey{}).(reqClock)
	if !ok || !clk.read {
		return tail
	}
	line := "Rendered in " + formatRender(time.Since(clk.start))
	if c := db.CounterFrom(ctx); admin && c != nil {
		n := c.Count()
		if n == 1 {
			line += " with 1 query"
		} else {
			line += " with " + strconv.FormatInt(n, 10) + " queries"
		}
	}
	return line + " · " + tail
}

// plainFooter returns the build line without the time and the count.
func plainFooter() string {
	return "gobb " + version.Version + " · build " + version.Build()
}

// formatRender shows d in seconds with two decimals, rounded up to the next
// 10 ms. It never shows 0.00.
func formatRender(d time.Duration) string {
	const step = 10 * time.Millisecond
	cs := int64((d + step - 1) / step)
	cs = max(cs, 1)
	return strconv.FormatInt(cs/100, 10) + "." + string('0'+byte(cs%100/10)) + string('0'+byte(cs%10)) + " s"
}
