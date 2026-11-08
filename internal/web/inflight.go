package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/evanwtf/gobb/internal/metrics"
)

// DefaultMaxInFlight is the most requests under way at one time when
// Options.MaxInFlight is zero. It matches config.DefaultMaxInFlight.
const DefaultMaxInFlight = 256

// limitInFlight lets at most cap(s.inFlight) requests run at one time. A
// request past the limit gets 503 at once with Retry-After, on the busy page
// that needs no database ([site.refuseInFlight]). It does not wait: a queue would hold
// the memory of each waiting request, which is what the limit exists to bound.
//
// Three paths skip the limit, because they cost almost nothing and an operator
// or a refused page needs them: /healthz (the health check of the container),
// /favicon.ico and the static files under <base>static/ (served from memory;
// the busy page itself links the stylesheet).
//
// The slot is given back in a deferred call, so a handler that panics frees it
// too.
func (s *site) limitInFlight(next http.Handler) http.Handler {
	return limitInFlight(s.inFlight, s.base, s.refuseInFlight, next)
}

// refuseInFlight answers a request past the in-flight limit: 503 with
// Retry-After, on the themed 503 page that New renders once
// ([site.writeStatic]). It runs no template, so a flood of refusals costs
// little memory and CPU.
func (s *site) refuseInFlight(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(poolBusyRetrySeconds))
	s.writeStatic(w, http.StatusServiceUnavailable)
}

// limitInFlight is [site.limitInFlight] with its parts: the semaphore (nil for
// no limit), the base path of the site and the answer to a refused request.
func limitInFlight(slots chan struct{}, base string, refuse func(http.ResponseWriter), next http.Handler) http.Handler {
	if slots == nil {
		return next
	}
	static := base + "static/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/healthz" || p == "/favicon.ico" || strings.HasPrefix(p, static) {
			next.ServeHTTP(w, r)
			return
		}
		select {
		case slots <- struct{}{}:
		default:
			metrics.InFlightRefusals.Inc()
			refuse(w)
			return
		}
		defer func() { <-slots }()
		next.ServeHTTP(w, r)
	})
}
