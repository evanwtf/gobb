package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/evanwtf/gobb/internal/board"
)

// The Spammer action on the member page, and its Undo on the log page. The
// action asks once on its own page, which says what it changes; the POST is a
// dangerous one, so it needs a confirmed password too.

// adminSpammerData is the data of admin-spammer.html.
type adminSpammerData struct {
	Crumbs []crumb
	Member board.MemberDetail
	Plan   board.SpammerPlan
	// Action is the address that the form posts to, and Cancel the link back to
	// the member page.
	Action, Cancel string
}

// adminSpammerRoutes adds the routes of the Spammer action and its Undo.
func (s *site) adminSpammerRoutes(a adminRoutes) {
	a.page("/members/{id}/spammer", s.adminSpammerPage)
	a.dangerous("/members/{id}/spammer", maxPostBody, s.adminSpammer)
	a.dangerous("/log/{id}/undo", maxPostBody, s.adminLogUndo)
}

// adminSpammerPage serves GET /admin/members/{id}/spammer: the confirm page.
func (s *site) adminSpammerPage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, d, ok := s.memberPage(w, r, id)
	if !ok {
		return
	}
	if d.Self || d.Owner {
		s.notFound(w, r)
		return
	}
	plan, err := board.PlanSpammer(r.Context(), s.pool, id, s.opt.Now())
	switch {
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	p.Title = "Mark " + d.Name + " as a spammer?"
	p.Data = adminSpammerData{
		Crumbs: append(d.Crumbs, crumb{Label: d.Name, Href: d.Href}),
		Member: d.MemberDetail,
		Plan:   plan,
		Action: d.Href + "/spammer",
		Cancel: d.Href,
	}
	s.render(w, http.StatusOK, "admin-spammer.html", p)
}

// adminSpammer serves POST /admin/members/{id}/spammer. It runs the action and
// goes back to the member page.
func (s *site) adminSpammer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, d, ok := s.memberPage(w, r, id)
	if !ok {
		return
	}
	_, err := board.MarkSpammer(r.Context(), s.pool, p.Viewer, id, ClientIP(r, s.opt.TrustedProxies), s.opt.Now())
	if s.memberRefused(w, r, p, d, err) {
		return
	}
	http.Redirect(w, r, d.Href+"?done=spammer", http.StatusSeeOther)
}

// adminLogUndo serves POST /admin/log/{id}/undo: the Undo of a Spammer action.
func (s *site) adminLogUndo(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, err := s.adminBegin(w, r, "Log")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	logHref := s.path("admin", "log")
	err = board.UndoSpammer(r.Context(), s.pool, p.Viewer, id, s.opt.Now())
	switch {
	case err == nil:
		http.Redirect(w, r, logHref+"?done=undone", http.StatusSeeOther)
	case errors.Is(err, board.ErrUndoClosed):
		http.Redirect(w, r, logHref+"?done=closed", http.StatusSeeOther)
	case errors.Is(err, board.ErrNotFound), errors.Is(err, board.ErrForbidden):
		s.notFound(w, r)
	default:
		slog.Error("admin undo failed", "log", strconv.FormatInt(id, 10), "err", err)
		s.serverError(w, r, err)
	}
}
