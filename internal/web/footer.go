package web

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/version"
)

// startKey is the context key of the time that a request entered the handler
// chain.
type startKey struct{}

// timed starts the clock and the statement counter of a request. The context
// carries both, and nothing else changes: the counter is only a number.
func timed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := db.WithCounter(context.WithValue(r.Context(), startKey{}, time.Now()))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// footerLine returns the build line of a page that answers with status. The line
// holds the version and the build. A page that answers 2xx also holds the time
// since the request entered the handler chain and the count of its statements. A
// page with no clock in its context (an error page that needs no database)
// holds the version and the build only. The line holds nothing about the host,
// the viewer or the data.
func footerLine(ctx context.Context, status int) string {
	tail := "gobb " + version.Version + " · build " + version.Build()
	if ctx == nil || status < 200 || status > 299 {
		return tail
	}
	start, ok := ctx.Value(startKey{}).(time.Time)
	c := db.CounterFrom(ctx)
	if !ok || c == nil {
		return tail
	}
	n := c.Count()
	queries := strconv.FormatInt(n, 10) + " queries"
	if n == 1 {
		queries = "1 query"
	}
	return "Rendered in " + formatRender(time.Since(start)) + " with " + queries + " · " + tail
}

// formatRender shows d in seconds with two decimals, rounded up to the next
// 10 ms. It never shows 0.00.
func formatRender(d time.Duration) string {
	const step = 10 * time.Millisecond
	cs := int64((d + step - 1) / step)
	cs = max(cs, 1)
	return strconv.FormatInt(cs/100, 10) + "." + string('0'+byte(cs%100/10)) + string('0'+byte(cs%10)) + " s"
}
