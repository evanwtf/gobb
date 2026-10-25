package web

import (
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/ratelimit"
)

// The limits on the posting forms. A POST to a posting form (a new topic, a
// reply or an edit; a save or a preview) costs a parse of up to 60,000 bytes, and
// a guest's POST also asks whether a name belongs to a member. Each limit is a
// count in a sliding window. The counts live in memory and start over with the
// server, like the limits on sign-in.
const (
	postWindow         = 10 * time.Minute
	defaultMemberPosts = 60 // POSTs for each member in postWindow
	defaultGuestPosts  = 20 // POSTs for each guest address in postWindow
	defaultNameChecks  = 10 // guest-name checks for each guest address in postWindow
	// postLimitMaxKeys is the most keys that one limiter holds. The production
	// container has 256 MiB, and a guest key can hold 20 timestamps, so 10,000
	// keys are a few megabytes for each limiter. A member who posts is one key; a
	// board with 10,000 members and guests inside ten minutes is a busy board.
	postLimitMaxKeys = 10_000
)

// msgPostingTooFast is the text of both limits. A guest over the name-check limit
// sees it too: the text says nothing about names.
const msgPostingTooFast = "You are posting too fast. Wait a few minutes and try again."

// postLimits holds the limiters of the posting forms.
type postLimits struct {
	// members counts the POSTs of each member, by member ID.
	members *ratelimit.Limiter
	// guests counts the POSTs of each guest address.
	guests *ratelimit.Limiter
	// names counts the guest-name checks of each address, from a preview or a
	// save. The check says whether a name belongs to a member, so without a limit
	// it would list the usernames of the board.
	names *ratelimit.Limiter
}

// newPostLimits returns the limiters, with the counts of opt; a zero count means
// the default.
func newPostLimits(opt Options) postLimits {
	or := func(n, def int) int {
		if n <= 0 {
			return def
		}
		return n
	}
	return postLimits{
		members: ratelimit.New(or(opt.MemberPosts, defaultMemberPosts), postWindow, postLimitMaxKeys),
		guests:  ratelimit.New(or(opt.GuestPosts, defaultGuestPosts), postWindow, postLimitMaxKeys),
		names:   ratelimit.New(or(opt.NameChecks, defaultNameChecks), postWindow, postLimitMaxKeys),
	}
}

// addrKey is the key of a client address in a limiter: the address for IPv4, the
// /64 for IPv6, since one client holds a whole /64, as the sign-in limit counts.
func addrKey(ip netip.Addr) string {
	if !ip.IsValid() {
		return "invalid"
	}
	ip = ip.Unmap().WithZone("")
	bits := 64
	if ip.Is4() {
		bits = 32
	}
	return netip.PrefixFrom(ip, bits).Masked().String()
}

// takePost counts a POST of the viewer against the posting limit. It reports
// false, with the wait, when the viewer is over it.
func (s *site) takePost(v board.Viewer, r *http.Request) (ok bool, wait time.Duration) {
	now := s.opt.Now()
	if v.UserID != 0 {
		return s.postLimits.members.Take(strconv.FormatInt(v.UserID, 10), now)
	}
	return s.postLimits.guests.Take(addrKey(s.clientIP(r)), now)
}

// takeNameCheck counts a guest-name check against the limit of the address. It
// runs before the check, so the answer that is over the limit tells nothing about
// the name.
func (s *site) takeNameCheck(r *http.Request) (ok bool, wait time.Duration) {
	return s.postLimits.names.Take(addrKey(s.clientIP(r)), s.opt.Now())
}

// tooFast answers 429 with the form and the text of the member. It says only that
// the member is over a limit. rights and hard are the results of the form frame:
// when the form cannot be built, the answer is the bare form.
func (s *site) tooFast(w http.ResponseWriter, r *http.Request, p *page, fd postFormData, rights, hard error, wait time.Duration, msg string) {
	w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(wait)))
	if hard != nil || errors.Is(rights, board.ErrNotFound) {
		s.bareForm(w, r, p, http.StatusTooManyRequests, msg, fd.Source)
		return
	}
	fd.Notice = msg
	s.renderForm(w, http.StatusTooManyRequests, p, fd)
}
