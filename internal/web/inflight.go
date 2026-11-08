package web

import (
	"bytes"
	"io"
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

// inFlightLimits are the two semaphores of the in-flight limits. Both are nil
// when the limits are off.
type inFlightLimits struct {
	// all is taken by [limitInFlight] for every request but the cheap ones
	// it exempts, the cached guest pages and the password POSTs.
	all chan struct{}
	// signin is for the password POSTs ([isPasswordPost]) alone. A password
	// POST takes its slot after [limitInFlight] has read its body, so a slow
	// body holds none, and keeps it to the end of the request. A wrong
	// password holds it for the floor of a failed sign-in and the gate wait,
	// so a flood of them fills these slots and not the main ones.
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

// limitInFlight lets at most cap(all) requests run at one time. A request past
// the limit gets 503 at once with Retry-After, on the busy page that needs no
// database ([site.refuseInFlight]), and counts in
// gobb_in_flight_refusals_total{limit="all"}. It does not wait: a queue would
// hold the memory of each waiting request, which is what the limit exists to
// bound.
//
// These requests skip the limit:
//   - GET /healthz (the health check of the container; it pings the database
//     at most once a second, [Health]), and GET or HEAD of /favicon.ico and of
//     a known file under <base>static/ ([staticTypes]): served from memory, and
//     the busy page itself links the stylesheet. Any other method, and an
//     unknown static name (whose 404 page runs queries), takes a slot.
//   - a guest page in the page cache ([site.serveGuestHit]): no render and no
//     database, and it is what most visitors ask for.
//   - the password POSTs ([isPasswordPost]): they take a slot of
//     GOBB_MAX_SIGNIN_IN_FLIGHT instead, once their body is read
//     ([readPasswordBody]). One past that limit gets the same 503 at once and
//     counts in gobb_in_flight_refusals_total{limit="signin"}.
//
// A slow request body or a slow reader holds its slot for as long as it
// lasts. A reverse proxy that buffers the bodies (nginx does by default) keeps
// that from the server.
//
// The slot is given back in a deferred call, so a handler that panics frees it
// too.
func (s *site) limitInFlight(next http.Handler) http.Handler {
	return limitInFlight(s.inFlight, s.base, s.refuseInFlight, s.serveGuestHit, next)
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
// email address (both check the current password), the confirm page of the
// admin area (it checks the admin's password), and the new password of a reset
// link. base is the base path of the site. A path with escapes (RawPath set) is
// not one: it takes a slot of the main limit, which is the safe side.
func isPasswordPost(r *http.Request, base string) bool {
	if r.Method != http.MethodPost || r.URL.RawPath != "" {
		return false
	}
	p, ok := strings.CutPrefix(r.URL.Path, base)
	if !ok {
		return false
	}
	switch p {
	case "login", "account/password", "account/email", "admin/confirm":
		return true
	}
	token, ok := strings.CutPrefix(p, "reset/")
	return ok && token != "" && !strings.Contains(token, "/")
}

// exempt reports whether r is a cheap request that skips the main limit: GET
// /healthz, and GET or HEAD of /favicon.ico or of a known static file.
func exempt(r *http.Request, base string) bool {
	if r.URL.RawPath != "" {
		return false
	}
	p := r.URL.Path
	if p == "/healthz" {
		return r.Method == http.MethodGet
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if p == "/favicon.ico" {
		return true
	}
	name, ok := strings.CutPrefix(p, base+"static/")
	_, known := staticTypes[name]
	return ok && known
}

// limitInFlight is [site.limitInFlight] with its parts: the limits (nil
// semaphores for none), the base path of the site, the answer to a refused
// request, and bypass, which may answer a request before the limits and then
// returns true. bypass may be nil.
func limitInFlight(l inFlightLimits, base string, refuse func(http.ResponseWriter), bypass func(http.ResponseWriter, *http.Request) bool, next http.Handler) http.Handler {
	if l.all == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exempt(r, base) {
			next.ServeHTTP(w, r)
			return
		}
		slots, name := l.all, "all"
		if isPasswordPost(r, base) {
			if l.signin == nil {
				next.ServeHTTP(w, r)
				return
			}
			readPasswordBody(r)
			slots, name = l.signin, "signin"
		} else if bypass != nil && bypass(w, r) {
			return
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

// maxPasswordBody is the most of a password POST that [readPasswordBody] reads
// before the slot: the largest cap of those routes, and one byte more, so that
// the cap of the route still sees a body that is too large.
const maxPasswordBody = max(maxLoginBody, maxAccountBody) + 1

// readPasswordBody reads up to maxPasswordBody bytes of the body of r into
// memory and puts them back in front of the rest, so the handler reads the
// same bytes. A slow client then holds no sign-in slot while it sends. The
// handler's own cap and parse still judge the body; a read error comes back to
// the handler from the rest of the body.
//
// The cost is the bytes of the bodies that are read at one time, at most
// maxPasswordBody each; a sign-in form is a few hundred bytes.
func readPasswordBody(r *http.Request) {
	if r.Body == nil || r.Body == http.NoBody {
		return
	}
	buf, _ := io.ReadAll(io.LimitReader(r.Body, maxPasswordBody))
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(buf), r.Body), r.Body}
}
