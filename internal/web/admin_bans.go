package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/evanwtf/gobb/internal/board"
)

// The Bans page of the admin area: the bans in force, a form to add one and a
// button to lift each. Add and lift are dangerous writes: they need a confirmed
// password ([adminRoutes.dangerous]). Each one goes through [board.AddBan] or
// [board.LiftBan], which write the mod_log row in the same transaction.

// maxBanBody is the cap of the body of the add form: two reasons of 255
// characters of up to 4 bytes each, and a few short fields.
const maxBanBody = 16 << 10

// banDoneNotices are the notices that /admin/bans?done=... can show. Any other
// value shows nothing.
var banDoneNotices = map[string]string{
	"added":  "The ban is added.",
	"lifted": "The ban is lifted.",
	"gone":   "That ban is already gone.",
}

// The layouts of the end of a ban: the value of a datetime-local field, and the
// same with a space, for a browser that shows a plain text field.
var banEndLayouts = []string{"2006-01-02T15:04", "2006-01-02 15:04"}

// adminBansRoutes registers the routes of the Bans page.
func (s *site) adminBansRoutes(a adminRoutes) {
	a.page("/bans", s.adminBans)
	a.dangerous("/bans", maxBanBody, s.adminBanAdd)
	a.dangerous("/bans/{id}/lift", maxPostBody, s.adminBanLift)
}

// adminBanForm holds the values of the add form, as sent.
type adminBanForm struct {
	Kind, Target, Reason, ReasonShown, Ends string
	ConfirmWide                             bool
}

// adminBanErr holds the messages that sit beside the fields of the add form.
type adminBanErr struct {
	Kind, Target, Reason, ReasonShown, Ends, ConfirmWide string
}

// adminBansData is the data of admin-bans.html.
type adminBansData struct {
	Crumbs  []crumb
	Entries []board.BanEntry
	Pager   *pager
	// Done is the fixed notice of a change that succeeded, or empty.
	Done string
	Form adminBanForm
	Err  adminBanErr
	// Back is the admin page that the lift buttons return to after the confirm
	// page, and AddBack the one that the add form returns to, with its kind and
	// target. Zone is the name of the zone of the end field.
	Back, AddBack, Zone string
}

// adminBans serves GET /admin/bans. The query values kind and target fill the
// add form, so that another page can link to a ban of a member.
func (s *site) adminBans(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, err := board.ParsePage(q.Get("page"))
	if err != nil {
		s.badPage(w, r)
		return
	}
	p, err := s.adminBegin(w, r, "Bans")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := adminBansData{Done: banDoneNotices[q.Get("done")], Form: adminBanForm{Kind: board.BanUserKind}}
	if k := q.Get("kind"); k == board.BanUserKind || k == board.BanEmailKind || k == board.BanIPKind {
		d.Form.Kind = k
	}
	if t := q.Get("target"); len(t) <= maxBanTarget {
		d.Form.Target = t
	}
	d.Back = s.bansHref(page)
	s.renderBans(w, r, http.StatusOK, p, d, page)
}

// maxBanTarget is the longest target that the form takes, in bytes: an email
// address of 254 bytes is the longest kind.
const maxBanTarget = 254

// bansHref returns the link to page n of the Bans page.
func (s *site) bansHref(n int) string {
	return pageHref(s.path("admin", "bans"), n)
}

// renderBans loads the bans in force and writes the Bans page with d. A page
// past the last is the 404 page.
func (s *site) renderBans(w http.ResponseWriter, r *http.Request, status int, p *page, d adminBansData, page int) {
	bp, err := board.ListBans(r.Context(), s.pool, page, s.opt.Now())
	switch {
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	d.Crumbs = s.adminCrumbs()
	d.Entries = bp.Entries
	d.Pager = newPagerWith(s.bansHref, bp.Page, bp.Pages)
	d.Zone = zoneName(p.Viewer.Loc)
	d.AddBack = s.path("admin", "bans")
	if d.Form.Target != "" {
		d.AddBack += "?" + url.Values{"kind": {d.Form.Kind}, "target": {d.Form.Target}}.Encode()
	}
	p.Data = d
	s.render(w, status, "admin-bans.html", p)
}

// zoneName returns the name of loc, or "UTC" for none.
func zoneName(loc *time.Location) string {
	if loc == nil {
		return "UTC"
	}
	return loc.String()
}

// parseBanEnd reads the end of a ban from text, in the zone loc. Empty text is no
// end and gives nil.
func parseBanEnd(text string, loc *time.Location) (*time.Time, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, true
	}
	if loc == nil {
		loc = time.UTC
	}
	for _, layout := range banEndLayouts {
		if t, err := time.ParseInLocation(layout, text, loc); err == nil {
			return &t, true
		}
	}
	return nil, false
}

// adminBanAdd serves POST /admin/bans. A refusal of the input shows the form
// again with 400, the values as sent and a message beside the field; nothing
// changes. A good add answers 303 to the page with a notice.
func (s *site) adminBanAdd(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "Bans")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f := adminBanForm{
		Kind: r.PostForm.Get("kind"), Target: strings.TrimSpace(r.PostForm.Get("target")),
		Reason: r.PostForm.Get("reason"), ReasonShown: r.PostForm.Get("reason_shown"),
		Ends: strings.TrimSpace(r.PostForm.Get("ends")), ConfirmWide: r.PostForm.Get("confirm_wide") == "1",
	}
	d := adminBansData{Form: f, Back: s.bansHref(1)}
	draft := board.BanDraft{Kind: f.Kind, Target: f.Target, Reason: f.Reason, ReasonShown: f.ReasonShown,
		ConfirmWide: f.ConfirmWide, AdminIP: ClientIP(r, s.opt.TrustedProxies)}
	end, ok := parseBanEnd(f.Ends, p.Viewer.Loc)
	if !ok {
		d.Err.Ends = "Enter the end as a date and a time, such as 2030-01-31 18:00."
		s.renderBans(w, r, http.StatusBadRequest, p, d, 1)
		return
	}
	draft.EndsAt = end
	_, err = board.AddBan(r.Context(), s.pool, p.Viewer, draft, s.opt.Now())
	var in *board.InputError
	switch {
	case errors.As(err, &in):
		d.Err = banFieldError(in)
		s.renderBans(w, r, http.StatusBadRequest, p, d, 1)
		return
	case errors.Is(err, board.ErrLastAdmin):
		d.Err.Target = "The board needs at least one active admin who can sign in, so this ban was not added."
		s.renderBans(w, r, http.StatusConflict, p, d, 1)
		return
	case errors.Is(err, board.ErrForbidden):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, s.bansHref(1)+"?done=added", http.StatusSeeOther)
}

// banFieldError puts the message of in beside its field. An error on a field the
// form does not have goes to the target.
func banFieldError(in *board.InputError) adminBanErr {
	var e adminBanErr
	switch in.Field {
	case "kind":
		e.Kind = in.Message
	case "reason":
		e.Reason = in.Message
	case "reason_shown":
		e.ReasonShown = in.Message
	case "ends":
		e.Ends = in.Message
	case "confirm_wide":
		e.ConfirmWide = in.Message
	default:
		e.Target = in.Message
	}
	return e
}

// adminBanLift serves POST /admin/bans/{id}/lift: 303 to the page with a notice.
// A ban that is gone already gets the notice "gone". An ID that is not a number
// above 0 is the 404 page.
func (s *site) adminBanLift(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		s.notFound(w, r)
		return
	}
	p, err := s.adminBegin(w, r, "Bans")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	done := "lifted"
	switch err := board.LiftBan(r.Context(), s.pool, p.Viewer, id, s.opt.Now()); {
	case errors.Is(err, board.ErrNotFound):
		done = "gone"
	case errors.Is(err, board.ErrForbidden):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, s.bansHref(1)+"?"+url.Values{"done": {done}}.Encode(), http.StatusSeeOther)
}
