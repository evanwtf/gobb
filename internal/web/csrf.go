package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"sync"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/session"
)

// The largest body that a POST route reads. A sign-in form carries a password
// of up to 4,096 bytes and a name, so it gets the larger cap.
const (
	maxLoginBody = 16 << 10
	maxPostBody  = 4 << 10
	// maxPostingBody is the cap of the posting forms. A body of 60,000 bytes of
	// 3-byte characters percent-encodes to 180,000 bytes, and the title and the
	// other fields come on top.
	maxPostingBody = 256 << 10
)

// newCSRFSecret returns 32 random bytes in base64url, the value of a guest's
// gobb_csrf cookie.
func newCSRFSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// protect wraps a POST handler. The request must pass the cross-origin check
// of net/http (Sec-Fetch-Site, or else Origin against Host), and its form must
// carry the csrf token of the visitor. The handler then reads the parsed form
// from r.PostForm. A request that fails either check gets 403 and the handler
// does not run.
//
// The body is capped at limit bytes before anything reads it, so a large body
// never reaches the csrf check: it gets 413 in the layout.
//
// A form of a member's page that comes after the session ended carries the
// member's token, which no longer matches. It gets 403 with "Your session
// ended. You are signed out.", not the advice to reload.
func (s *site) protect(limit int64, h http.HandlerFunc) http.Handler {
	return s.protectOr(limit, h, nil)
}

// protectOr is [site.protect] with one more case: when the token fails and the
// visitor has no live session, signedOut answers instead of the 403. Sign-out
// uses it, because a visitor who is signed out has nothing to protect.
func (s *site) protectOr(limit int64, h, signedOut http.HandlerFunc) http.Handler {
	return s.guardPost(s.crossOrigin, limit, h, signedOut, false)
}

// protectPosting is [site.protect] for a form that carries the text of a post. A
// request that fails the cross-origin check or the token check is not trusted,
// so the answer never echoes its body. The 403 says that the form expired and
// tells the member to use the Back button to get the text back: a signed-out
// session, a wrong token and a cross-site request all reach this one page.
func (s *site) protectPosting(limit int64, h http.HandlerFunc) http.Handler {
	return s.guardPost(s.crossOriginPosting, limit, h, nil, true)
}

// The text of the 403 of a posting form that cannot be checked.
const (
	expiredHeading = "Form expired"
	expiredMessage = "This form expired. Use your browser's Back button to get your text back, then reload the page and post again."
)

// protectUpload is [site.protect] for a form that carries a file. It reads the
// body as multipart/form-data, with the whole body (limit bytes) held in memory:
// limit is the size of the largest file and a little more for the other parts,
// so no part spills to disk. The token is a text part of the form. A body that is
// not multipart is read as a urlencoded form, so a request with no file reaches
// the handler, which refuses it.
//
// The cross-origin check runs before any read. Then the session is resolved
// before the body: a guest, or a visitor whose session ended, gets 403 and the
// server reads nothing. So does a member who lacks the right account.avatar,
// which is checked on the rights of the member before any slot is taken. The
// uploads in flight are capped twice, and a request
// over either cap gets 503 at once, before any read: the server takes
// maxUploadsInFlight at one time, and one member has one upload in flight at a
// time. After the read, the token is checked, as in [site.protect]: the cap of
// the body holds the cost.
func (s *site) protectUpload(limit int64, h http.HandlerFunc) http.Handler {
	g := guard{limit: limit, tooBig: msgFileTooLarge, member: true,
		option: "account.avatar", denied: "You do not have the right to change your avatar.", slots: func() chan struct{} { return s.uploadSlots }, oneEach: &s.uploading, parse: func(r *http.Request) error {
			// ParseMultipartForm drops the error of a body that is not multipart (it
			// returns ErrNotMultipart instead), and a body over the cap is one such
			// error. So the content type picks the parser.
			if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "multipart/form-data" {
				return r.ParseForm()
			}
			return r.ParseMultipartForm(limit)
		}}
	return s.guardWith(s.crossOrigin, g, h, nil)
}

// msgFileTooLarge is the text of the 413 of an upload.
const msgFileTooLarge = "That file is too large. Use one of at most 1 MB."

func (s *site) guardPost(cop *http.CrossOriginProtection, limit int64, h, signedOut http.HandlerFunc, expired bool) http.Handler {
	g := guard{limit: limit, tooBig: "The form you sent is too large.", expired: expired,
		parse: func(r *http.Request) error { return r.ParseForm() }}
	return s.guardWith(cop, g, h, signedOut)
}

// guard says how guardWith reads a request: the cap of the body, the text of the
// 413, how it parses the body, and whether a failed token answers with the
// "form expired" page of a posting form.
type guard struct {
	limit   int64
	tooBig  string
	expired bool
	parse   func(*http.Request) error
	// member asks for a signed-in visitor before the body is read: a guest, or a
	// visitor whose session ended, gets 403 and the server reads nothing. slots,
	// when set, is the semaphore of the requests that may be in the guard and the
	// handler at one time; a request that finds it full gets 503 at once. Both are
	// for the upload, whose body is read into memory.
	member bool
	slots  func() chan struct{}
	// oneEach, when set with member, holds the IDs of the members who have a
	// request in the guard and the handler. A second request of the same member
	// gets 503 at once.
	oneEach *sync.Map
	// option, when set with member, is the right that the member needs. A member
	// without it gets 403 with the text denied, before a slot is taken and before
	// the body is read.
	option, denied string
}

// The header that tells the server a request comes from gobb.js. The answer is
// then 204 or JSON, never a page.
const saveHeaderName = "X-Gobb-Save"

// saveRequested reports whether r comes from the script of the account page,
// which wants a JSON answer.
func saveRequested(r *http.Request) bool { return r.Header.Get(saveHeaderName) == "1" }

// writeJSON writes v as the JSON answer of a request from gobb.js.
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		// The values are strings of this package; this cannot happen.
		b, status = []byte(`{"message":"Something went wrong."}`), http.StatusInternalServerError
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", cacheMember)
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// saveMessage writes the JSON answer {"message": msg} with status.
func saveMessage(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg})
}

// guardRefused answers a refusal of the guard: a JSON message for a request from
// gobb.js, the page otherwise.
func (s *site) guardRefused(w http.ResponseWriter, r *http.Request, status int, heading, msg string) {
	if saveRequested(r) {
		saveMessage(w, status, msg)
		return
	}
	s.fail(w, r, status, heading, msg)
}

// identifyFailed answers a request whose session could not be looked up: JSON
// with a fixed text for a request from gobb.js, the error page otherwise.
func (s *site) identifyFailed(w http.ResponseWriter, r *http.Request, err error) {
	if saveRequested(r) {
		slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		saveMessage(w, http.StatusInternalServerError, msgWriteFailed)
		return
	}
	s.serverError(w, r, err)
}

func (s *site) guardWith(cop *http.CrossOriginProtection, g guard, h, signedOut http.HandlerFunc) http.Handler {
	limit, expired := g.limit, g.expired
	return cop.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.member {
			// The session comes first: nobody who is not signed in makes the server
			// hold a body.
			id, err := s.identify(w, r)
			if err != nil {
				s.identifyFailed(w, r, err)
				return
			}
			// The identity goes into the request first, so the page of a refusal
			// does not look the session up a second time.
			r = r.WithContext(context.WithValue(r.Context(), identityKey{}, id))
			if id.token == "" {
				msg := "Sign in to upload a file."
				if id.ended {
					msg = "Your session ended. You are signed out."
				}
				s.guardRefused(w, r, http.StatusForbidden, "Form not accepted", msg)
				return
			}
			if g.option != "" {
				settings, err := board.LoadSettings(r.Context(), s.pool)
				var v board.Viewer
				if err == nil {
					v, err = board.Member(r.Context(), s.pool, id.sess.UserID, settings)
				}
				switch {
				case errors.Is(err, board.ErrNotFound) || (err == nil && !v.Perms.Has(g.option, 0)):
					s.guardRefused(w, r, http.StatusForbidden, "Form not accepted", g.denied)
					return
				case err != nil:
					s.identifyFailed(w, r, err)
					return
				}
			}
			if g.oneEach != nil {
				if _, busy := g.oneEach.LoadOrStore(id.sess.UserID, struct{}{}); busy {
					w.Header().Set("Retry-After", strconv.Itoa(busyRetrySeconds))
					s.guardRefused(w, r, http.StatusServiceUnavailable, "Server busy", msgBusy)
					return
				}
				defer g.oneEach.Delete(id.sess.UserID)
			}
			if g.slots != nil {
				slots := g.slots()
				select {
				case slots <- struct{}{}:
					defer func() { <-slots }()
				default:
					w.Header().Set("Retry-After", strconv.Itoa(busyRetrySeconds))
					s.guardRefused(w, r, http.StatusServiceUnavailable, "Server busy", msgBusy)
					return
				}
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		if err := g.parse(r); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				s.guardRefused(w, r, http.StatusRequestEntityTooLarge, "Request too large", g.tooBig)
				return
			}
			s.guardRefused(w, r, http.StatusBadRequest, "Bad request", "The form could not be read.")
			return
		}
		// The identity is read once. The handler's own call of begin finds it in
		// the context of the request it gets.
		id, err := s.identify(w, r)
		if err != nil {
			s.identifyFailed(w, r, err)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), identityKey{}, id))
		// Read the token from the body only. A token in the address would show up
		// in logs and in the Referer header.
		if !session.CheckCSRF(id.secret(), r.PostForm.Get("csrf")) {
			switch {
			case expired && !saveRequested(r):
				s.failExpired(w, r)
			case id.token == "" && signedOut != nil:
				signedOut(w, r)
			case id.ended:
				s.guardRefused(w, r, http.StatusForbidden, "Signed out", "Your session ended. You are signed out.")
			default:
				s.guardRefused(w, r, http.StatusForbidden, "Form not accepted",
					"The form could not be checked. Go back, reload the page and send the form again.")
			}
			return
		}
		h(w, r)
	}))
}

// denyExpired answers a posting form that http.CrossOriginProtection refused. It
// echoes nothing.
func (s *site) denyExpired(w http.ResponseWriter, r *http.Request) {
	if saveRequested(r) {
		saveMessage(w, http.StatusForbidden, msgCrossOrigin)
		return
	}
	s.failExpired(w, r)
}

// failExpired writes the 403 of a posting form that cannot be trusted. The page
// is built on [site.framePage], from the settings in memory: it echoes nothing of
// the request, not the body, and not the path or the query either, which a page
// built by begin writes into the scheme picker and the sign-in link.
func (s *site) failExpired(w http.ResponseWriter, r *http.Request) {
	p := s.framePage(r, expiredHeading)
	p.Data = errorData{Status: http.StatusForbidden, Heading: expiredHeading, Message: expiredMessage}
	s.render(w, http.StatusForbidden, "error.html", p)
}

// denyCrossOrigin answers a POST that http.CrossOriginProtection refused.
func (s *site) denyCrossOrigin(w http.ResponseWriter, r *http.Request) {
	s.guardRefused(w, r, http.StatusForbidden, "Form not accepted", msgCrossOrigin)
}

// msgCrossOrigin is the text of the 403 of a POST from another site.
const msgCrossOrigin = "This form was sent from another site, or by a browser that does not say where it came from."
