package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/evanwtf/gobb/internal/session"
)

// The largest body that a POST route reads. A sign-in form carries a password
// of up to 4,096 bytes and a name, so it gets the larger cap.
const (
	maxLoginBody = 16 << 10
	maxPostBody  = 4 << 10
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
func (s *site) protect(limit int64, h http.HandlerFunc) http.Handler {
	return s.crossOrigin.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		if err := r.ParseForm(); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				s.fail(w, r, http.StatusRequestEntityTooLarge, "Request too large", "The form you sent is too large.")
				return
			}
			s.fail(w, r, http.StatusBadRequest, "Bad request", "The form could not be read.")
			return
		}
		// The identity is read once. The handler's own call of begin finds it in
		// the context of the request it gets.
		id, err := s.identify(w, r)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), identityKey{}, id))
		// Read the token from the body only. A token in the address would show up
		// in logs and in the Referer header.
		if !session.CheckCSRF(id.secret(), r.PostForm.Get("csrf")) {
			s.fail(w, r, http.StatusForbidden, "Form not accepted",
				"The form could not be checked. Go back, reload the page and send the form again.")
			return
		}
		h(w, r)
	}))
}

// denyCrossOrigin answers a POST that http.CrossOriginProtection refused.
func (s *site) denyCrossOrigin(w http.ResponseWriter, r *http.Request) {
	s.fail(w, r, http.StatusForbidden, "Form not accepted",
		"This form was sent from another site, or by a browser that does not say where it came from.")
}
