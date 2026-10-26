package web

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/evanwtf/gobb/internal/metrics"
)

// routeKey is the context key of the route recorder of a request.
type routeKey struct{}

// routeRec holds the route that answered a request. Each handler that a routes
// mux registers writes its pattern there, so the innermost mux writes last.
type routeRec struct{ route string }

// withRoute wraps h so that it records pattern, without its method, as the route
// of the request: "GET /t/{id}" records "/t/{id}".
func withRoute(pattern string, h http.Handler) http.Handler {
	route := pattern
	if i := strings.IndexByte(pattern, ' '); i >= 0 {
		route = strings.TrimSpace(pattern[i+1:])
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rec, ok := r.Context().Value(routeKey{}).(*routeRec); ok {
			rec.route = route
		}
		h.ServeHTTP(w, r)
	})
}

// metered counts each request in gobb_http_requests_total and times it in
// gobb_http_request_duration_seconds. The route label is the pattern of the
// handler that answered, or "none" when no pattern matched and a mux answered
// by itself (a 405 for a method that no pattern takes). The path itself is never
// a label. A handler that panics counts as a 500, and the panic goes on.
func metered(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &routeRec{route: "none"}
		sw := &statusWriter{ResponseWriter: w}
		record := func(code int) {
			metrics.HTTPRequests.Inc(rec.route, metrics.Method(r.Method), metrics.StatusClass(code))
			metrics.HTTPDuration.Observe(time.Since(start).Seconds(), rec.route)
		}
		defer func() {
			if p := recover(); p != nil {
				record(http.StatusInternalServerError)
				panic(p)
			}
		}()
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), routeKey{}, rec)))
		record(sw.code())
	})
}

// statusWriter keeps the first final status that a handler writes.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 && code >= 200 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// ReadFrom lets io.Copy reach the ReadFrom of the writer below, which can send
// a file with no copy through user space.
func (w *statusWriter) ReadFrom(r io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return io.Copy(w.ResponseWriter, r)
}

// Unwrap returns the writer below, for [http.NewResponseController].
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// code is the status that the client got: 200 when the handler wrote nothing.
func (w *statusWriter) code() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
