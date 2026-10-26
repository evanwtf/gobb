package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/ratelimit"
)

// MailOptions turns the pages of the password reset on. A site with no
// MailOptions has no reset route and no link to one.
type MailOptions struct {
	// PublicURL is the address of the site for links in email: absolute, and
	// ending with the base path and "/". A link is built from it and from nothing
	// in the request.
	PublicURL string
	// Wake tells the email worker that a message is waiting. It must not block.
	// It may be nil.
	Wake func()
}

// The limits of the reset pages. Each count lives in memory in a sliding window
// and starts over with the server, like the limits on sign-in.
const (
	resetAddrLimit    = 5  // requests from one client address in resetWindow
	resetBoardLimit   = 30 // requests on the whole board in resetWindow
	resetDeadLimit    = 20 // dead-link hits from one client address in resetWindow
	resetAccountLimit = 3  // emails to one account in resetAccountWindow
	resetWindow       = time.Hour
	// resetAccountWindow is the window of the limit on emails to one account.
	resetAccountWindow = 24 * time.Hour
	resetLimitMaxKeys  = 10_000
	// defaultResetFloor is the least time that a request for a reset takes.
	defaultResetFloor = 400 * time.Millisecond
	// resetBoardKey is the one key of the limit on the whole board.
	resetBoardKey = "board"
)

// resetLimits holds the limiters of the reset pages.
type resetLimits struct {
	// addr counts the requests of each client address, board the requests of the
	// whole board, account the emails to each account (by user ID), and dead the
	// hits on a link that board.ResetLink refuses, for each client address.
	addr, board, account, dead *ratelimit.Limiter
}

func newResetLimits() resetLimits {
	return resetLimits{
		addr:    ratelimit.New(resetAddrLimit, resetWindow, resetLimitMaxKeys),
		board:   ratelimit.New(resetBoardLimit, resetWindow, 1),
		account: ratelimit.New(resetAccountLimit, resetAccountWindow, resetLimitMaxKeys),
		dead:    ratelimit.New(resetDeadLimit, resetWindow, resetLimitMaxKeys),
	}
}

// The texts of the reset pages.
const (
	msgResetNoName   = "Enter your username or email address."
	msgResetDiffers  = "The two passwords differ."
	msgDeadLink      = "This link has expired or has been used."
	resetLinkHeading = "Link not valid"
)

// resetData is the data of reset.html.
type resetData struct {
	// Sent shows the "Check your email" notice in place of the form.
	Sent bool
	// Error is the headline of a refused request, and NameError the text beside
	// the field.
	Error, NameError string
}

// resetLinkData is the data of reset-link.html.
type resetLinkData struct {
	Username, Token string
	// Notice is the headline of a refused save, and PasswordError and AgainError
	// the texts beside the fields.
	Notice, PasswordError, AgainError string
}

// noReferrer sets Referrer-Policy: no-referrer on every answer of next, a
// refusal of the guard included, because the token is in the path.
func noReferrer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// foldControls returns s with each control character replaced by a space, so
// that a board name cannot hold a line break in a header.
func foldControls(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// resetMailFor returns the builder of the reset email for the board called
// boardName. A link is publicURL, "reset/" and the token. The subject holds the
// board name with each control character folded to a space: a name with a line
// break must not make the queue refuse the email of an existing account, which
// would show that the account exists.
func resetMailFor(boardName, publicURL string) board.ResetMail {
	name := foldControls(boardName)
	base := strings.TrimSuffix(publicURL, "/") + "/"
	return func(username, token string) (subject, body string) {
		subject = "Reset your password on " + name
		body = "Someone asked to reset the password of the account " + foldControls(username) + " on " + name + ".\n\n" +
			"To choose a new password, open this link within 1 hour:\n\n" +
			base + "reset/" + token + "\n\n" +
			"The link works once. If you did not ask for this, ignore this email.\n" +
			"Your password stays the same.\n"
		return subject, body
	}
}

// resetFloor returns the least time that a request for a reset takes.
func (s *site) resetFloor() time.Duration {
	if s.opt.ResetFloor > 0 {
		return s.opt.ResetFloor
	}
	return defaultResetFloor
}

// waitUntil waits until the floor has passed since start, or until ctx ends.
func waitUntil(ctx context.Context, start time.Time, floor time.Duration) {
	left := floor - time.Since(start)
	if left <= 0 {
		return
	}
	timer := time.NewTimer(left)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// renderReset draws reset.html. The page is never cached: it holds a csrf token.
func (s *site) renderReset(w http.ResponseWriter, p *page, status int, d resetData) {
	p.Data = d
	if d.Sent {
		p.Title = "Check your email"
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, status, "reset.html", p)
}

// resetForm serves GET /reset.
func (s *site) resetForm(w http.ResponseWriter, r *http.Request) {
	p, err := s.begin(w, r, "Reset your password", "reset")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderReset(w, p, http.StatusOK, resetData{Sent: r.URL.Query().Get("sent") == "1"})
}

// resetTooMany answers 429 on the request form. In format, "N minutes" becomes
// the wait.
func (s *site) resetTooMany(w http.ResponseWriter, p *page, wait time.Duration, format string) {
	w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(wait)))
	s.renderReset(w, p, http.StatusTooManyRequests, resetData{Error: strings.Replace(format, "N minutes", waitText(wait), 1)})
}

// resetSubmit serves POST /reset. Whatever the name finds, the answer is the
// same 303 after the same floor of time: an unknown name, an inactive account, a
// banned one, one without account.password and one over its email limit all look
// like a request that queued an email. A refused csrf token (the guard) and an
// empty name answer before any limit counts.
func (s *site) resetSubmit(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	p, err := s.begin(w, r, "Reset your password", "reset")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if name == "" {
		s.renderReset(w, p, http.StatusBadRequest, resetData{NameError: msgResetNoName})
		return
	}
	now := s.opt.Now()
	if ok, wait := s.resets.addr.Take(addrKey(s.clientIP(r)), now); !ok {
		s.resetTooMany(w, p, wait, "Too many requests from your address. Try again in N minutes.")
		return
	}
	if ok, wait := s.resets.board.Take(resetBoardKey, now); !ok {
		s.resetTooMany(w, p, wait, "The board is sending too many emails. Try again in N minutes.")
		return
	}
	allow := func(userID int64) bool {
		ok, _ := s.resets.account.Take(strconv.FormatInt(userID, 10), now)
		return ok
	}
	mailFor := resetMailFor(p.Settings.BoardName, s.opt.Mail.PublicURL)
	queued, err := board.RequestReset(r.Context(), s.pool, name, allow, mailFor, now)
	if err != nil {
		// The answer stays the same: an error that shows only for an account that
		// exists would tell a visitor which names exist. The log has the error,
		// which holds no name, address or token.
		slog.Error("reset request failed", "err", err)
	}
	if queued > 0 && s.opt.Mail.Wake != nil {
		s.opt.Mail.Wake()
	}
	waitUntil(r.Context(), start, s.resetFloor())
	http.Redirect(w, r, s.path("reset")+"?sent=1", http.StatusSeeOther)
}

// resetPage begins a page of /reset/{token}. The forms and the sign-in link of
// the layout return to the board and not to this address, so the token is on the
// page only in the form action.
func (s *site) resetPage(w http.ResponseWriter, r *http.Request, title string) (*page, bool) {
	p, err := s.begin(w, r, title, "reset")
	if err != nil {
		s.resetError(w, err)
		return nil, false
	}
	p.Return = s.path("reset")
	if p.LoginHref != "" {
		p.LoginHref = s.path("login")
	}
	return p, true
}

// resetError answers a failure on a page of /reset/{token}. It logs the error
// and not the path, which holds the token, and answers from the settings in
// memory.
func (s *site) resetError(w http.ResponseWriter, err error) {
	if errors.Is(err, board.ErrNoSettings) {
		slog.Warn("the database holds no board; run gobb import")
		s.writeFailure(w, http.StatusServiceUnavailable)
		return
	}
	slog.Error("reset page failed", "err", err)
	s.writeFailure(w, http.StatusInternalServerError)
}

// resetDead answers a link that board.ResetLink refuses: the 404 page, and a
// count on the address. An address over the limit gets 429 instead.
func (s *site) resetDead(w http.ResponseWriter, r *http.Request, p *page) {
	status, heading, msg := http.StatusNotFound, resetLinkHeading, msgDeadLink
	href, label := s.path("reset"), "Ask for a new link"
	if ok, wait := s.resets.dead.Take(addrKey(s.clientIP(r)), s.opt.Now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(wait)))
		status, heading, msg = http.StatusTooManyRequests, "Too many tries", "Too many requests from your address. Try again in "+waitText(wait)+"."
		href, label = "", ""
	}
	p.Title = heading
	p.Data = errorData{Status: status, Heading: heading, Message: msg, Href: href, HrefLabel: label}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, status, "error.html", p)
}

// renderResetLink draws reset-link.html.
func (s *site) renderResetLink(w http.ResponseWriter, p *page, status int, d resetLinkData) {
	p.Data = d
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, status, "reset-link.html", p)
}

// resetLinkPage serves GET /reset/{token}. It changes nothing: a mail scanner
// that opens the link does not use it up.
func (s *site) resetLinkPage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	p, ok := s.resetPage(w, r, "Choose a new password")
	if !ok {
		return
	}
	acct, err := board.ResetLink(r.Context(), s.pool, token, s.opt.Now())
	switch {
	case errors.Is(err, board.ErrNotFound):
		s.resetDead(w, r, p)
	case err != nil:
		s.resetError(w, err)
	default:
		s.renderResetLink(w, p, http.StatusOK, resetLinkData{Username: acct.Username, Token: token})
	}
}

// resetComplete serves POST /reset/{token}.
func (s *site) resetComplete(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	p, ok := s.resetPage(w, r, "Choose a new password")
	if !ok {
		return
	}
	now := s.opt.Now()
	acct, err := board.ResetLink(r.Context(), s.pool, token, now)
	if errors.Is(err, board.ErrNotFound) {
		s.resetDead(w, r, p)
		return
	}
	if err != nil {
		s.resetError(w, err)
		return
	}
	d := resetLinkData{Username: acct.Username, Token: token}
	next, again := r.PostForm.Get("password"), r.PostForm.Get("again")
	// The cheap check comes first: two different values cost no hash.
	if next != again {
		d.AgainError = msgResetDiffers
		s.renderResetLink(w, p, http.StatusBadRequest, d)
		return
	}
	hold := func(ctx context.Context) (func(), error) {
		if beforeHold != nil {
			beforeHold()
		}
		return s.login.Hold(ctx)
	}
	_, err = board.CompleteReset(r.Context(), s.pool, token, next, hold, now)
	var in *board.InputError
	switch {
	case errors.As(err, &in):
		d.PasswordError = in.Message
		s.renderResetLink(w, p, http.StatusBadRequest, d)
		return
	case errors.Is(err, board.ErrNotFound):
		s.resetDead(w, r, p)
		return
	case errors.Is(err, login.ErrBusy):
		w.Header().Set("Retry-After", strconv.Itoa(busyRetrySeconds))
		d.Notice = msgBusy
		s.renderResetLink(w, p, http.StatusServiceUnavailable, d)
		return
	case err != nil:
		s.resetError(w, err)
		return
	}
	// CompleteReset deleted every session of the member, so a session cookie that
	// the request carried names a dead session.
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		s.setCookie(w, s.cookie(sessionCookie, "", -1))
	}
	http.Redirect(w, r, s.path("login")+"?reset=1", http.StatusSeeOther)
}
