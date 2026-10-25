package web

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/legacy"
)

// fallback answers a request that no route of the site matched. A GET or HEAD
// request whose path and query match a row of the legacy table gets a 301 to the
// page the row stands for. Anything else is a 404.
//
// The site's own routes win because the mux asks this handler last. The match
// runs on the request path under the base path, decoded once. Nothing is logged
// per request.
func (s *site) fallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		s.notFound(w, r)
		return
	}
	old := strings.TrimPrefix(r.URL.Path, "/")
	query := r.URL.Query()
	if rt, ok := s.legacy.Match(old, query); ok {
		s.legacyRoute(w, r, rt, query)
		return
	}
	if dir, rest, ok := s.legacy.File(old); ok {
		s.legacyFile(w, r, dir, rest)
		return
	}
	s.notFound(w, r)
}

// moved answers 301 to the site path to.
func (s *site) moved(w http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(w, r, to, http.StatusMovedPermanently)
}

// legacyFile answers 301 to /files/<dir>/<rest> when /files/ would serve that
// file, and 404 otherwise.
func (s *site) legacyFile(w http.ResponseWriter, r *http.Request, dir, rest string) {
	f, _, _, err := s.openImage(dir, rest)
	switch {
	case errors.Is(err, errNotServed):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	_ = f.Close()
	s.moved(w, r, s.path("files", dir, rest))
}

// legacyRoute answers a request that matched route rt. The kinds with a plain
// target go to the page, which checks the viewer itself. The kinds with an ID
// run the check of their own page first, so a target the viewer cannot see gets
// the 404 of a missing one before any redirect.
func (s *site) legacyRoute(w http.ResponseWriter, r *http.Request, rt legacy.Route, query url.Values) {
	switch rt.Kind {
	case "home":
		s.moved(w, r, s.path())
	case "members", "team", "search", "account":
		s.moved(w, r, s.path(rt.Kind))
	case "page":
		if rt.TargetSlug == nil {
			s.notFound(w, r)
			return
		}
		_, err := board.Page(r.Context(), s.pool, *rt.TargetSlug)
		switch {
		case errors.Is(err, board.ErrNotFound):
			s.notFound(w, r)
		case err != nil:
			s.serverError(w, r, err)
		default:
			s.moved(w, r, s.path("pages", *rt.TargetSlug))
		}
	case "forum", "topic", "post", "user", "attachment", "avatar":
		s.legacyTarget(w, r, rt, query)
	default:
		s.notFound(w, r)
	}
}

// legacyID reads the ID a route points at: the first value of its ID parameter,
// or its fixed target. A value that is not a positive integer gives false.
func legacyID(rt legacy.Route, query url.Values) (int64, bool) {
	if rt.IDParam != nil {
		vals := query[*rt.IDParam]
		if len(vals) == 0 {
			return 0, false
		}
		return parseID(vals[0])
	}
	if rt.TargetID != nil && *rt.TargetID > 0 {
		return *rt.TargetID, true
	}
	return 0, false
}

// legacyPage turns the offset of a route into a page number, with perPage items
// to a page. The page is 1 when the route has no offset parameter or the request
// has no offset. An offset must be a whole number of 0 or more, and its page
// must be one that ParsePage accepts.
func legacyPage(rt legacy.Route, query url.Values, perPage int) (int, bool) {
	if rt.OffsetParam == nil {
		return 1, true
	}
	vals, ok := query[*rt.OffsetParam]
	if !ok || len(vals) == 0 {
		return 1, true
	}
	raw := vals[0]
	if raw == "" {
		return 0, false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, false
		}
	}
	off, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	page := off/int64(perPage) + 1
	if page > 1_000_000 {
		return 0, false
	}
	if _, err := board.ParsePage(strconv.FormatInt(page, 10)); err != nil {
		return 0, false
	}
	return int(page), true
}

// legacyTarget answers a route whose kind points at one row.
func (s *site) legacyTarget(w http.ResponseWriter, r *http.Request, rt legacy.Route, query url.Values) {
	id, ok := legacyID(rt, query)
	if !ok {
		s.notFound(w, r)
		return
	}
	idText := strconv.FormatInt(id, 10)
	ctx := r.Context()
	p, err := s.begin(w, r, "", "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	switch rt.Kind {
	case "forum":
		page, ok := legacyPage(rt, query, board.TopicsPerPage)
		if !ok {
			s.notFound(w, r)
			return
		}
		_, err := board.ForumView(ctx, s.pool, p.Viewer, id, 1)
		var link board.LinkError
		switch {
		case err == nil:
			s.moved(w, r, pageHref(s.path("f", idText), page))
		case errors.As(err, &link):
			// A link forum has no pages: /f/{id} sends the visitor on, unless
			// the site refuses its URL. Then the page is a 404 and so is this.
			if _, ok := s.linkTarget(link.URL); !ok {
				s.notFound(w, r)
				return
			}
			s.moved(w, r, s.path("f", idText))
		case errors.Is(err, board.ErrForbidden):
			// The page's own refusal: a forum that the viewer may list and not
			// read.
			s.denied(w, p, "this forum")
		case errors.Is(err, board.ErrNotFound):
			s.notFound(w, r)
		default:
			s.serverError(w, r, err)
		}
	case "topic":
		page, ok := legacyPage(rt, query, board.PostsPerPage)
		if !ok {
			s.notFound(w, r)
			return
		}
		_, err := board.TopicView(ctx, s.pool, p.Viewer, id, 1)
		var moved board.MovedError
		switch {
		case err == nil:
			s.moved(w, r, pageHref(s.path("t", idText), page))
		case errors.As(err, &moved):
			// The viewer sees the target, so the old link may go straight to it.
			s.moved(w, r, s.path("t", strconv.FormatInt(moved.To, 10)))
		case errors.Is(err, board.ErrNotFound):
			s.notFound(w, r)
		default:
			s.serverError(w, r, err)
		}
	case "post":
		// /p/{id} answers 302 to the page that holds the post for this viewer, so
		// the permanent answer does not depend on the viewer.
		if _, _, err := board.PostLocation(ctx, s.pool, p.Viewer, id); err != nil {
			s.postRedirectError(w, r, err)
			return
		}
		s.moved(w, r, s.path("p", idText))
	case "user":
		_, err := board.Profile(ctx, s.pool, p.Viewer, id, s.opt.Now())
		if s.profileFailed(w, r, p, err) {
			return
		}
		s.moved(w, r, s.path("u", idText))
	case "attachment":
		_, err := board.AttachmentFile(ctx, s.pool, p.Viewer, id)
		if s.attachmentFailed(w, r, err) {
			return
		}
		s.moved(w, r, s.path("a", idText))
	case "avatar":
		stored, err := board.AvatarPath(ctx, s.pool, id)
		if errors.Is(err, board.ErrNotFound) {
			s.notFound(w, r)
			return
		}
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		dir, name, _ := strings.Cut(stored, "/")
		if dir != "avatars" {
			// /files/ would serve another directory, but it holds no avatar.
			s.notFound(w, r)
			return
		}
		f, _, _, err := s.openImage(dir, name)
		switch {
		case errors.Is(err, errNotServed):
			s.notFound(w, r)
			return
		case err != nil:
			s.serverError(w, r, err)
			return
		}
		_ = f.Close()
		s.moved(w, r, s.path("files", stored))
	}
}

// warnHidden logs a warning for each route and each file prefix of the legacy
// table that can never apply: one that a route of the site answers first (the
// mux sends such a request to the site), one whose path is not in its clean form
// (the guard answers 404 before the mux), and, at the host root, the route
// "healthz" (the root mux owns it). The first check asks the mux itself, so it
// follows the routes as they are.
func (s *site) warnHidden(mux *http.ServeMux) {
	owner := func(p string) string {
		req, err := http.NewRequest(http.MethodGet, "http://site.invalid/", nil)
		if err != nil {
			return ""
		}
		req.URL.Path = "/" + p
		_, pattern := mux.Handler(req)
		if pattern == "/" {
			return ""
		}
		if pattern == "" {
			return "(redirect)"
		}
		return pattern
	}
	for _, rt := range s.legacy.Routes() {
		switch {
		case !isCleanPath("/" + rt.Path):
			slog.Warn("legacy route has a path that is not clean and never applies", "path", rt.Path, "kind", rt.Kind)
		case (rt.Path == "healthz" || rt.Path == "favicon.ico") && s.base == "/":
			slog.Warn("legacy route is hidden by a route at the host root and never applies", "path", rt.Path, "kind", rt.Kind)
		default:
			if pat := owner(rt.Path); pat != "" {
				slog.Warn("legacy route is hidden by a route of the site and never applies", "path", rt.Path, "kind", rt.Kind, "site_route", pat)
			}
		}
	}
	for _, prefix := range s.legacy.Prefixes() {
		if !isCleanPath("/" + prefix + "x") {
			slog.Warn("legacy file prefix has a path that is not clean and never applies", "prefix", prefix)
			continue
		}
		if pat := owner(prefix + "x"); pat != "" {
			slog.Warn("legacy file prefix is hidden in part by a route of the site", "prefix", prefix, "site_route", pat)
		}
	}
}
