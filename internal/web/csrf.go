package web

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/evanwtf/gobb/internal/session"
)

// maxFormBytes is the largest form body that a POST route reads.
const maxFormBytes = 32 << 10

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
func (s *site) protect(h http.HandlerFunc) http.Handler {
	return s.crossOrigin.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		if err := r.ParseForm(); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				s.fail(w, r, http.StatusRequestEntityTooLarge, "Request too large", "The form you sent is too large.")
				return
			}
			s.fail(w, r, http.StatusBadRequest, "Bad request", "The form could not be read.")
			return
		}
		id, err := s.identify(r)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
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
