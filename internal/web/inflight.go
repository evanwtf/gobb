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

// DefaultMaxSigninInFlight is the most password POSTs under way at one time
// when Options.MaxSigninInFlight is zero. It matches
// config.DefaultMaxSigninInFlight.
const DefaultMaxSigninInFlight = 32

// inFlightLimits are the two semaphores of [limitInFlight]. Both are nil when
// the limit is off.
type inFlightLimits struct {
	// all is shared by every request but the password POSTs.
	all chan struct{}
	// signin is for the password POSTs ([isPasswordPost]) alone. A wrong
	// password holds its slot for the floor of a failed sign-in and the gate
	// wait, so a flood of them would otherwise fill all and refuse the pages.
	signin chan struct{}
}

// newInFlightLimits sizes the limits from the options: zero means the
// default, a negative MaxInFlight turns both off, and the sign-in limit is
// never more than the main one.
func newInFlightLimits(opt Options) inFlightLimits {
	all := opt.MaxInFlight
	switch {
	case all < 0:
		return inFlightLimits{}
	case all == 0:
		all = DefaultMaxInFlight
	}
	signin := opt.MaxSigninInFlight
	if signin <= 0 {
		signin = DefaultMaxSigninInFlight
	}
	return inFlightLimits{all: make(chan struct{}, all), signin: make(chan struct{}, min(signin, all))}
}

// limitInFlight lets at most cap(all) requests run at one time, and at most
// cap(signin) password POSTs, on their own slots. A request past its limit gets
// 503 at once with Retry-After, on the busy page that needs no database
// ([site.refuseInFlight]), and counts in gobb_in_flight_refusals_total with
// limit "all" or "signin". It does not wait: a queue would hold the memory of
// each waiting request, which is what the limit exists to bound.
//
// Three paths skip the limits, because they cost almost nothing and an
// operator or a refused page needs them: /healthz (the health check of the
// container), /favicon.ico and the static files under <base>static/ (served
// from memory; the busy page itself links the stylesheet).
//
// The slot is given back in a deferred call, so a handler that panics frees it
// too.
func (s *site) limitInFlight(next http.Handler) http.Handler {
	return limitInFlight(s.inFlight, s.base, s.refuseInFlight, next)
}

// refuseInFlight answers a request past an in-flight limit: 503 with
// Retry-After, on the themed 503 page that New renders once
// ([site.writeStatic]). It runs no template, so a flood of refusals costs
// little memory and CPU.
func (s *site) refuseInFlight(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(poolBusyRetrySeconds))
	s.writeStatic(w, http.StatusServiceUnavailable)
}

// isPasswordPost reports whether r is a POST that checks or hashes a password
// through the memory gate: the sign-in, the change of the password or of the
// email address (both check the current password), and the new password of a
// reset link. path is r.URL.Path; base is the base path of the site.
func isPasswordPost(r *http.Request, base string) bool {
	if r.Method != http.MethodPost {
		return false
	}
	p, ok := strings.CutPrefix(r.URL.Path, base)
	if !ok {
		return false
	}
	switch p {
	case "login", "account/password", "account/email":
		return true
	}
	token, ok := strings.CutPrefix(p, "reset/")
	return ok && token != "" && !strings.Contains(token, "/")
}

// limitInFlight is [site.limitInFlight] with its parts: the limits (nil
// semaphores for none), the base path of the site and the answer to a refused
// request.
func limitInFlight(l inFlightLimits, base string, refuse func(http.ResponseWriter), next http.Handler) http.Handler {
	if l.all == nil {
		return next
	}
	static := base + "static/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/healthz" || p == "/favicon.ico" || strings.HasPrefix(p, static) {
			next.ServeHTTP(w, r)
			return
		}
		slots, name := l.all, "all"
		if l.signin != nil && isPasswordPost(r, base) {
			slots, name = l.signin, "signin"
		}
		select {
		case slots <- struct{}{}:
		default:
			metrics.InFlightRefusals.Inc(name)
			refuse(w)
			return
		}
		defer func() { <-slots }()
		next.ServeHTTP(w, r)
	})
}
