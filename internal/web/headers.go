package web

import (
	"net/http"
	"slices"
	"strings"

	"github.com/evanwtf/gobb/markup"
)

// policy is the Content-Security-Policy of one response. The zero value is the
// base policy of the site: nothing loads except from the site itself, and
// images may come from any https host, because posts hotlink them.
type policy struct {
	// Frame, Script and Connect add origins to frame-src, script-src and
	// connect-src. They come from the embeds of the posts on the page.
	Frame, Script, Connect []string
	// InlineStyle adds 'unsafe-inline' to style-src. The provider scripts set
	// style attributes on the frames they build. gobb itself never writes a
	// style attribute, so post text gains nothing from it.
	InlineStyle bool
}

// baseCSP is the policy of a response with no embed.
var baseCSP = policy{}.String()

// add merges what the embeds of a rendered tree need into the policy.
func (p *policy) add(r markup.Rendered) {
	p.Frame = mergeSorted(p.Frame, r.FrameSrc)
	p.Script = mergeSorted(p.Script, r.ScriptSrc)
	p.Connect = mergeSorted(p.Connect, r.ConnectSrc)
	if len(r.Scripts) > 0 {
		p.InlineStyle = true
	}
}

// mergeSorted returns the sorted union of a and b, with no duplicates.
func mergeSorted(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	out := slices.Concat(a, b)
	slices.Sort(out)
	return slices.Compact(out)
}

// String returns the policy as the header value. An origin in a list comes
// from the embed table of the markup package, never from post text.
func (p policy) String() string {
	style := "'self'"
	if p.InlineStyle {
		style += " 'unsafe-inline'"
	}
	frame := "'none'"
	if len(p.Frame) > 0 {
		frame = strings.Join(p.Frame, " ")
	}
	return strings.Join([]string{
		"default-src 'none'",
		"script-src " + strings.Join(append([]string{"'self'"}, p.Script...), " "),
		"style-src " + style,
		"img-src 'self' https:",
		"connect-src " + strings.Join(append([]string{"'self'"}, p.Connect...), " "),
		"font-src 'self'",
		"frame-src " + frame,
		"form-action 'self'",
		"frame-ancestors 'none'",
		"base-uri 'none'",
		"object-src 'none'",
	}, "; ")
}

// The default Cache-Control of a response. A request that carries a session
// cookie may come from a member, so it gets the stricter value. A page
// replaces the default with the value for the viewer that the page knows.
const (
	cacheGuest  = "private, no-cache"
	cacheMember = "private, no-store"
)

// secure sets the security headers on every response: the base policy and the
// five fixed headers. It also sets a default Cache-Control, so that a redirect,
// whose target depends on the viewer, is never kept by a shared cache. A
// handler that needs more replaces the policy before it writes the body. The
// download of an attachment adds a sandbox to it, and /static/, /files/ and /a/
// replace the Cache-Control with their own.
//
// A response that answers an error and is not a page gets X-Robots-Tag, as a
// page gets the robots meta element.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", baseCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if _, err := r.Cookie(sessionCookie); err == nil {
			h.Set("Cache-Control", cacheMember)
		} else {
			h.Set("Cache-Control", cacheGuest)
		}
		next.ServeHTTP(&robotsWriter{ResponseWriter: w}, r)
	})
}

// robotsWriter adds X-Robots-Tag to an error that is not an HTML page.
type robotsWriter struct {
	http.ResponseWriter
	wrote bool
}

// WriteHeader adds the header, then writes the status.
func (w *robotsWriter) WriteHeader(code int) {
	if !w.wrote {
		w.wrote = true
		h := w.Header()
		if code >= 400 && !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
			h.Set("X-Robots-Tag", "noindex")
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write writes the body. A body with no status first means 200.
func (w *robotsWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap returns the writer below, for [http.NewResponseController].
func (w *robotsWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// noindexPages are the page templates that search engines must not index.
var noindexPages = map[string]bool{
	"login.html": true, "new.html": true, "postform.html": true, "confirm.html": true, "search.html": true, "error.html": true, "account.html": true,
	"messages.html": true, "message.html": true, "message-new.html": true, "reset.html": true, "reset-link.html": true,
	"admin.html": true, "admin-bans.html": true, "admin-confirm.html": true, "admin-forum.html": true, "admin-forums.html": true, "admin-log.html": true, "admin-member.html": true, "admin-members.html": true,
}

// cacheControl returns the Cache-Control value of an HTML page. Every page
// carries a csrf token and many echo a next value, so no shared cache may keep
// one. A member's page may hold private data, so a browser keeps nothing.
func cacheControl(member bool) string {
	if member {
		return cacheMember
	}
	return cacheGuest
}
