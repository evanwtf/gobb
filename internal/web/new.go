package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/evanwtf/gobb/internal/board"
)

// isNavigation reports whether r is a top-level navigation by a person: a GET
// that the browser sends for the page itself and not for an image, a frame or a
// script that a page loads. Only such a request may change read bookkeeping
// (visits, topic marks and view counts). A post can hold an image whose address
// is a path of this site, and the browser sends that GET with the cookies of
// every reader.
//
// A browser sends Sec-Fetch-Dest, and "document" means a top-level navigation.
// A client that sends none counts when it asks for HTML. A prefetch does not
// count: the person has not opened the page yet.
//
// Sec-Fetch-Site does not matter here: a view and a visit count on any
// top-level navigation, whoever started it (ruling R51). A read mark needs more
// (see [isOwnNavigation]).
//
// Any other GET writes nothing, with one accepted exception that is not
// bookkeeping: a GET whose cookie names a dead session (expired, or of a user
// who is not active) deletes that session's row ([site.identify]). The row is
// worth nothing, and the delete changes no data that a page shows.
func isNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	for _, h := range []string{"Sec-Purpose", "Purpose"} {
		if strings.Contains(strings.ToLower(r.Header.Get(h)), "prefetch") {
			return false
		}
	}
	if dest := r.Header.Get("Sec-Fetch-Dest"); dest != "" {
		return dest == "document"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// isOwnNavigation reports whether r is a [isNavigation] that the member started.
// A browser sends Sec-Fetch-Site, and only "same-origin" (a click on this site)
// and "none" (a typed address, a bookmark) count. A link on another site, and a
// script that opens the page in a window, send "cross-site" or "same-site": the
// page they open counts a view and a visit but sets no read mark (rulings R45 and
// R51). A client that sends no Sec-Fetch-Site is judged on the other headers
// alone.
func isOwnNavigation(r *http.Request) bool {
	if !isNavigation(r) {
		return false
	}
	site := r.Header.Get("Sec-Fetch-Site")
	return site == "" || site == "same-origin" || site == "none"
}

// countView does the bookkeeping of a topic page that a person opened: it adds
// a view, and for a member who opened it from this site (see [isOwnNavigation]) it
// marks the topic read up to the newest post on the page. A page that shows only old posts leaves the newer posts unread. The
// caller runs it after the page is built (see [site.renderThen]). A failure is
// logged; the page still shows.
func (s *site) countView(r *http.Request, v board.Viewer, tp *board.TopicPage) {
	ctx := r.Context()
	if err := board.CountView(ctx, s.pool, tp.Topic.ID); err != nil {
		slog.Warn("cannot count a view", "topic", tp.Topic.ID, "err", err)
	}
	if v.UserID == 0 || len(tp.Posts) == 0 || !isOwnNavigation(r) {
		return
	}
	newest := tp.Posts[0].At
	for _, p := range tp.Posts {
		if p.At.After(newest) {
			newest = p.At
		}
	}
	if err := board.MarkTopic(ctx, s.pool, v.UserID, tp.Topic.ID, newest); err != nil {
		slog.Warn("cannot mark a topic read", "topic", tp.Topic.ID, "err", err)
	}
}

// formNext returns the place to send a member after a form: the site path in the
// next field, or fallback.
func (s *site) formNext(r *http.Request, fallback string) string {
	if next := s.sitePath(r.PostForm.Get("next")); next != "" {
		return next
	}
	return fallback
}

// newData is the data of new.html.
type newData struct {
	Topics []topicRowView
	Pager  *pager
	// Since is the time of the member's last visit.
	Since time.Time
	Total int
}

// newPage serves GET /new: the topics with posts since the member's last visit.
// A guest has no last visit and goes to sign in.
func (s *site) newPage(w http.ResponseWriter, r *http.Request) {
	p, err := s.begin(w, r, "New since your last visit", "new")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if p.Viewer.UserID == 0 {
		http.Redirect(w, r, s.loginHref(p.Return), http.StatusFound)
		return
	}
	page, err := board.ParsePage(r.URL.Query().Get("page"))
	if err != nil {
		s.badPage(w, r)
		return
	}
	np, err := board.NewSince(r.Context(), s.pool, p.Viewer, page)
	switch {
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	rows := make([]topicRowView, len(np.Topics))
	for i, row := range np.Topics {
		rows[i] = topicRowView{TopicRow: row.TopicRow, Clock: p.Clock, Forum: row.Forum}
	}
	// The nav of this page needs no second count: the list has it.
	p.navUnread = &np.Unread
	p.Data = newData{
		Topics: rows,
		Pager:  newPager(s.path("new"), np.Page, np.Pages),
		Since:  p.Viewer.LastVisit,
		Total:  np.Total,
	}
	// The page holds a different list after every visit.
	w.Header().Set("Cache-Control", "private, no-store")
	s.render(w, http.StatusOK, "new.html", p)
}

// topicNew serves GET /t/{id}/new: a redirect to the first post of the topic that
// the viewer has not read. A moved-topic marker answers 301 to the /new of its
// target. The permission check runs first, so a hidden topic
// gives the 404 of a missing one.
func (s *site) topicNew(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, err := s.begin(w, r, "", "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	postID, err := board.FirstUnread(r.Context(), s.pool, p.Viewer, id)
	var moved board.MovedError
	if errors.As(err, &moved) {
		http.Redirect(w, r, s.path("t", strconv.FormatInt(moved.To, 10), "new"), http.StatusMovedPermanently)
		return
	}
	if err != nil {
		s.postRedirectError(w, r, err)
		return
	}
	s.redirectToPost(w, r, p.Viewer, postID)
}

// markForumRead serves POST /f/{id}/read.
func (s *site) markForumRead(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, err := s.begin(w, r, "", "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	fallback := s.path("f", strconv.FormatInt(id, 10))
	if p.Viewer.UserID == 0 {
		http.Redirect(w, r, s.loginHref(s.formNext(r, fallback)), http.StatusSeeOther)
		return
	}
	err = board.MarkForum(r.Context(), s.pool, p.Viewer, id, s.opt.Now())
	switch {
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, s.formNext(r, fallback), http.StatusSeeOther)
}

// markAllRead serves POST /read.
func (s *site) markAllRead(w http.ResponseWriter, r *http.Request) {
	p, err := s.begin(w, r, "", "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	next := s.formNext(r, s.path())
	if p.Viewer.UserID == 0 {
		http.Redirect(w, r, s.loginHref(next), http.StatusSeeOther)
		return
	}
	if err := board.MarkAll(r.Context(), s.pool, p.Viewer.UserID, s.opt.Now()); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}
