package web

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/evanwtf/gobb/internal/board"
)

// The members pages of the admin area: find a member, and the page of one member
// with its buttons. The commands of `gobb user ...` do the same work on the host.

// memberDone are the notices that /admin/members/{id}?done=... can show. Any
// other value shows nothing.
var memberDone = map[string]string{
	"activate":   "The account is active.",
	"deactivate": "The account is inactive. Its sessions ended.",
	"rank":       "The rank is saved.",
	"verify":     "The email address is marked verified.",
	"nochange":   "Nothing changed: the member was in that state already.",
	"spammer":    "Marked as a spammer. The log has an Undo for 30 days.",
}

// adminMemberRoutes adds the routes of the members pages.
func (s *site) adminMemberRoutes(a adminRoutes) {
	a.page("/members", s.adminMembers)
	a.page("/members/{id}", s.adminMember)
	a.write("/members/{id}/activate", maxPostBody, s.adminMemberWrite("activate"))
	a.write("/members/{id}/deactivate", maxPostBody, s.adminMemberWrite("deactivate"))
	a.write("/members/{id}/rank", maxPostBody, s.adminMemberWrite("rank"))
	a.write("/members/{id}/verify-email", maxPostBody, s.adminMemberWrite("verify"))
	// A reset shows the new password to the admin, so it always needs a pass.
	a.dangerous("/members/{id}/reset-password", maxPostBody, s.adminResetPassword)
}

// adminMembersData is the data of admin-members.html.
type adminMembersData struct {
	Crumbs []crumb
	Query  string
	Rows   []board.MemberRow
	Pager  *pager
}

// adminMembers serves GET /admin/members: the search.
func (s *site) adminMembers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, err := board.ParsePage(q.Get("page"))
	if err != nil {
		s.badPage(w, r)
		return
	}
	p, err := s.adminBegin(w, r, "Members")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := adminMembersData{Crumbs: s.adminCrumbs(), Query: q.Get("q")}
	ms, err := board.FindMembers(r.Context(), s.pool, d.Query, page, s.opt.Now())
	switch {
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	d.Rows = ms.Rows
	d.Pager = newPagerWith(func(n int) string {
		v := url.Values{}
		if d.Query != "" {
			v.Set("q", d.Query)
		}
		if n > 1 {
			v.Set("page", strconv.Itoa(n))
		}
		href := s.path("admin", "members")
		if len(v) > 0 {
			href += "?" + v.Encode()
		}
		return href
	}, ms.Page, ms.Pages)
	p.Data = d
	s.render(w, http.StatusOK, "admin-members.html", p)
}

// adminMemberData is the data of admin-member.html.
type adminMemberData struct {
	Crumbs []crumb
	board.MemberDetail
	// Self is true on the admin's own page.
	Self bool
	// NeedsPass is true when a change to this member needs a confirmed password
	// (an owner or an admin) and the session holds no pass.
	NeedsPass bool
	// Done is the notice of a change that succeeded, Notice the message of a
	// refusal, and RankError the message beside the rank list.
	Done, Notice, RankError string
	// NewPassword is the password of a reset, shown once, on the answer to the
	// POST. It is empty on every other answer.
	NewPassword string
	// Href is the site path of this page; BanHref and LogHref link to the bans
	// page and to the log of this member.
	Href, BanHref, LogHref string
}

// adminMember serves GET /admin/members/{id}.
func (s *site) adminMember(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, d, ok := s.memberPage(w, r, id)
	if !ok {
		return
	}
	d.Done = memberDone[r.URL.Query().Get("done")]
	s.renderMember(w, http.StatusOK, p, d)
}

// memberPage builds the page of member id. When it fails, it has answered.
func (s *site) memberPage(w http.ResponseWriter, r *http.Request, id int64) (*page, adminMemberData, bool) {
	p, err := s.adminBegin(w, r, "Member")
	if err != nil {
		s.serverError(w, r, err)
		return nil, adminMemberData{}, false
	}
	m, err := board.AdminMember(r.Context(), s.pool, id, s.opt.Now())
	switch {
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return nil, adminMemberData{}, false
	case err != nil:
		s.serverError(w, r, err)
		return nil, adminMemberData{}, false
	}
	p.Title = m.Name
	idText := strconv.FormatInt(id, 10)
	d := adminMemberData{
		Crumbs:       append(s.adminCrumbs(), crumb{Label: "Members", Href: s.path("admin", "members")}),
		MemberDetail: m,
		Self:         id == p.Viewer.UserID,
		NeedsPass:    (m.Owner || m.IsAdmin) && !s.passFresh(r),
		Href:         s.path("admin", "members", idText),
		BanHref:      s.path("admin", "bans") + "?" + url.Values{"kind": {"user"}, "target": {m.Name}}.Encode(),
		LogHref:      s.path("admin", "log") + "?" + url.Values{"type": {"user"}, "target": {idText}}.Encode(),
	}
	return p, d, true
}

// renderMember writes the page of one member with d.
func (s *site) renderMember(w http.ResponseWriter, status int, p *page, d adminMemberData) {
	p.Data = d
	s.render(w, status, "admin-member.html", p)
}

// adminMemberWrite serves the POSTs of the member page other than the reset:
// activate, deactivate, rank and verify. A change to an owner or an admin needs
// a confirmed password; without one, the admin goes to the confirm page and the
// write does not run.
func (s *site) adminMemberWrite(what string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r, "id")
		if !ok {
			s.notFound(w, r)
			return
		}
		p, d, ok := s.memberPage(w, r, id)
		if !ok {
			return
		}
		if d.NeedsPass {
			http.Redirect(w, r, s.confirmHref(d.Href), http.StatusSeeOther)
			return
		}
		ctx, v, now := r.Context(), p.Viewer, s.opt.Now()
		var err error
		switch what {
		case "activate":
			err = board.AdminSetActive(ctx, s.pool, v, id, true, now)
		case "deactivate":
			err = board.AdminSetActive(ctx, s.pool, v, id, false, now)
		case "verify":
			err = board.AdminVerifyEmail(ctx, s.pool, v, id, now)
		case "rank":
			rank, perr := strconv.ParseInt(strings.TrimSpace(r.PostForm.Get("rank")), 10, 64)
			if perr != nil || rank < 0 {
				d.RankError = "Pick a rank from the list."
				s.renderMember(w, http.StatusBadRequest, p, d)
				return
			}
			err = board.AdminSetRank(ctx, s.pool, v, id, rank, now)
		}
		if s.memberRefused(w, r, p, d, err) {
			return
		}
		http.Redirect(w, r, d.Href+"?done="+what, http.StatusSeeOther)
	}
}

// adminResetPassword serves POST /admin/members/{id}/reset-password. The answer
// is the member page with the new password, shown once: not a redirect, so the
// password is in no address and no history entry.
func (s *site) adminResetPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, d, ok := s.memberPage(w, r, id)
	if !ok {
		return
	}
	pw, err := board.AdminResetPassword(r.Context(), s.pool, p.Viewer, id, s.opt.Now())
	if s.memberRefused(w, r, p, d, err) {
		return
	}
	d.NewPassword = pw
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.renderMember(w, http.StatusOK, p, d)
}

// memberRefused answers the error of a member write and reports whether it did.
// A write that found nothing to change goes back to the page with a notice.
func (s *site) memberRefused(w http.ResponseWriter, r *http.Request, p *page, d adminMemberData, err error) bool {
	var in *board.InputError
	status := http.StatusConflict
	switch {
	case err == nil:
		return false
	case errors.Is(err, board.ErrNoChange), errors.Is(err, board.ErrAlreadyActive), errors.Is(err, board.ErrAlreadyInactive):
		http.Redirect(w, r, d.Href+"?done=nochange", http.StatusSeeOther)
		return true
	case errors.As(err, &in):
		d.RankError, status = in.Message, http.StatusBadRequest
	case errors.Is(err, board.ErrSelf):
		d.Notice, status = "You cannot do this to your own account here.", http.StatusForbidden
	case errors.Is(err, board.ErrOwnerOnly):
		d.Notice, status = "Only an owner can do this to an owner.", http.StatusForbidden
	case errors.Is(err, board.ErrLastAdmin):
		d.Notice = "The board needs at least one active admin, so this change was not made."
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return true
	case errors.Is(err, board.ErrForbidden):
		s.notFound(w, r)
		return true
	default:
		slog.Error("admin member write failed", "path", r.URL.Path, "err", err)
		d.Notice, status = msgWriteFailed, failStatus(r, err)
	}
	s.renderMember(w, status, p, d)
	return true
}
