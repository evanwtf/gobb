package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/evanwtf/gobb/internal/board"
)

// The smiley editor of the admin area: the codes that turn into small images.

// smileyDone are the notices that the smiley pages can show. Any other value
// shows nothing.
var smileyDone = map[string]string{
	"created":  "The smiley is added.",
	"saved":    "The smiley is saved.",
	"moved":    "The smiley moved.",
	"deleted":  "The smiley is deleted.",
	"nochange": "Nothing changed.",
}

// smileyForm holds the values of the smiley form, and the message beside each
// field.
type smileyForm struct {
	Code, Description string
	Err               fieldErrs
	// Notice is a message on top of the form: a failed write.
	Notice string
}

// adminSmiliesData is the data of admin-smilies.html: the list and the form that
// adds a smiley.
type adminSmiliesData struct {
	Crumbs []crumb
	Rows   []adminSmileyRow
	Add    smileyForm
	Done   string
	Action string
}

// adminSmileyRow is one smiley of the list.
type adminSmileyRow struct {
	board.AdminSmiley
	Src, EditHref, MoveHref, DeleteHref string
	First, Last                         bool
}

// smileyCrumbs returns the trail Index > Admin > Smilies.
func (s *site) smileyCrumbs() []crumb {
	return append(s.adminCrumbs(), crumb{Label: "Smilies", Href: s.path("admin", "smilies")})
}

// renderSmilies writes the list of smilies. add holds the values of the add
// form.
func (s *site) renderSmilies(w http.ResponseWriter, r *http.Request, status int, p *page, add smileyForm, done string) {
	list, err := board.AdminSmilies(r.Context(), s.pool)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := adminSmiliesData{Crumbs: s.smileyCrumbs(), Add: add, Done: smileyDone[done], Action: s.path("admin", "smilies")}
	for i, sm := range list {
		id := strconv.FormatInt(sm.ID, 10)
		d.Rows = append(d.Rows, adminSmileyRow{AdminSmiley: sm, Src: s.path("files", sm.Image),
			EditHref: s.path("admin", "smilies", id), MoveHref: s.path("admin", "smilies", id, "move"),
			DeleteHref: s.path("admin", "smilies", id, "delete"), First: i == 0, Last: i == len(list)-1})
	}
	p.Data = d
	s.render(w, status, "admin-smilies.html", p)
}

// adminSmilies serves GET /admin/smilies.
func (s *site) adminSmilies(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "Smilies")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderSmilies(w, r, http.StatusOK, p, smileyForm{}, r.URL.Query().Get("done"))
}

// adminSmileyData is the data of admin-smiley.html.
type adminSmileyData struct {
	Crumbs []crumb
	Smiley board.AdminSmiley
	Src    string
	Form   smileyForm
	Action string
}

// storedSmiley builds the page of smiley id of the path. When it fails, it has
// answered.
func (s *site) storedSmiley(w http.ResponseWriter, r *http.Request) (*page, adminSmileyData, bool) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return nil, adminSmileyData{}, false
	}
	p, err := s.adminBegin(w, r, "Smiley")
	if err != nil {
		s.serverError(w, r, err)
		return nil, adminSmileyData{}, false
	}
	sm, err := board.AdminSmileyByID(r.Context(), s.pool, id)
	switch {
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return nil, adminSmileyData{}, false
	case err != nil:
		s.serverError(w, r, err)
		return nil, adminSmileyData{}, false
	}
	return p, adminSmileyData{Crumbs: append(s.smileyCrumbs(), crumb{Label: sm.Code, Href: s.path("admin", "smilies", strconv.FormatInt(id, 10))}),
		Smiley: sm, Src: s.path("files", sm.Image), Action: s.path("admin", "smilies", strconv.FormatInt(id, 10)),
		Form: smileyForm{Code: sm.Code, Description: sm.Description}}, true
}

// renderSmiley writes the form of one smiley with d.
func (s *site) renderSmiley(w http.ResponseWriter, status int, p *page, d adminSmileyData) {
	p.Data = d
	s.render(w, status, "admin-smiley.html", p)
}

// adminSmileyEdit serves GET /admin/smilies/{id}.
func (s *site) adminSmileyEdit(w http.ResponseWriter, r *http.Request) {
	p, d, ok := s.storedSmiley(w, r)
	if !ok {
		return
	}
	s.renderSmiley(w, http.StatusOK, p, d)
}

// adminSmileyCreate serves POST /admin/smilies. The image is required. The new
// file is written first and the row second; when the row is refused, the file
// goes.
func (s *site) adminSmileyCreate(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "Smilies")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	in := board.SmileyInput{Code: r.PostForm.Get("code"), Description: r.PostForm.Get("description")}
	form := smileyForm{Code: in.Code, Description: in.Description, Err: fieldErrs{}}
	up := s.storeImage(w, r, board.SmileyDir, "s")
	switch {
	case up.Msg != "":
		form.Err["image"] = up.Msg
		s.renderSmilies(w, r, up.Status, p, form, "")
		return
	case up.Path == "":
		form.Err["image"] = "Pick an image."
		s.renderSmilies(w, r, http.StatusBadRequest, p, form, "")
		return
	}
	_, err = board.AdminCreateSmiley(detached(r), s.pool, p.Viewer, in, up.Path, s.opt.Now())
	o := outcomeOf(r, "smiley.create", err)
	if o.kind != outDone {
		s.deleteContentFile(board.SmileyDir, up.Path)
	}
	switch o.kind {
	case outDone:
		http.Redirect(w, r, s.path("admin", "smilies")+"?done=created", http.StatusSeeOther)
	case outMissing:
		s.notFound(w, r)
	default:
		form.Err, form.Notice = o.formAnswer(form.Err, "code", "description")
		s.renderSmilies(w, r, o.status, p, form, "")
	}
}

// adminSmileySave serves POST /admin/smilies/{id}. A new image is optional.
func (s *site) adminSmileySave(w http.ResponseWriter, r *http.Request) {
	p, d, ok := s.storedSmiley(w, r)
	if !ok {
		return
	}
	in := board.SmileyInput{Code: r.PostForm.Get("code"), Description: r.PostForm.Get("description")}
	d.Form = smileyForm{Code: in.Code, Description: in.Description, Err: fieldErrs{}}
	up := s.storeImage(w, r, board.SmileyDir, "s")
	if up.Msg != "" {
		d.Form.Err["image"] = up.Msg
		s.renderSmiley(w, up.Status, p, d)
		return
	}
	freed, err := board.AdminUpdateSmiley(detached(r), s.pool, p.Viewer, d.Smiley.ID, in, up.Path, s.opt.Now())
	o := outcomeOf(r, "smiley.update", err)
	if o.kind != outDone {
		s.deleteContentFile(board.SmileyDir, up.Path)
	}
	switch o.kind {
	case outDone:
		s.deleteContentFile(board.SmileyDir, freed)
		http.Redirect(w, r, s.path("admin", "smilies")+"?done=saved", http.StatusSeeOther)
	case outNoChange:
		http.Redirect(w, r, s.path("admin", "smilies")+"?done=nochange", http.StatusSeeOther)
	case outMissing:
		s.notFound(w, r)
	default:
		d.Form.Err, d.Form.Notice = o.formAnswer(d.Form.Err, "code", "description")
		s.renderSmiley(w, o.status, p, d)
	}
}

// adminSmileyMove serves POST /admin/smilies/{id}/move with dir=up or dir=down.
func (s *site) adminSmileyMove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	dir := r.PostForm.Get("dir")
	if !ok || (dir != "up" && dir != "down") {
		s.notFound(w, r)
		return
	}
	p, err := s.adminBegin(w, r, "Smilies")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	o := outcomeOf(r, "smiley.reorder", board.AdminMoveSmiley(detached(r), s.pool, p.Viewer, id, dir == "up", s.opt.Now()))
	switch o.kind {
	case outDone:
		http.Redirect(w, r, s.path("admin", "smilies")+"?done=moved", http.StatusSeeOther)
	case outNoChange:
		http.Redirect(w, r, s.path("admin", "smilies")+"?done=nochange", http.StatusSeeOther)
	case outMissing:
		s.notFound(w, r)
	default:
		s.renderSmilies(w, r, o.status, p, smileyForm{Notice: o.msg}, "")
	}
}

// adminSmileyDeleteConfirm serves GET /admin/smilies/{id}/delete.
func (s *site) adminSmileyDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	sm, err := board.AdminSmileyByID(r.Context(), s.pool, id)
	switch {
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	idText := strconv.FormatInt(id, 10)
	s.renderDelete(w, r, append(s.smileyCrumbs(), crumb{Label: sm.Code, Href: s.path("admin", "smilies", idText)}), adminDeleteData{
		Heading: "Delete the smiley " + sm.Code + "?",
		Lines: []string{"The smiley is deleted. Its image file goes too, unless another smiley or a rank uses the same file.",
			"Posts that use the code " + sm.Code + " show the code as text from now on. A post saved later does not turn the code into an image."},
		Action: s.path("admin", "smilies", idText, "delete"), Cancel: s.path("admin", "smilies")})
}

// adminSmileyDelete serves POST /admin/smilies/{id}/delete.
func (s *site) adminSmileyDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, err := s.adminBegin(w, r, "Smilies")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	freed, err := board.AdminDeleteSmiley(detached(r), s.pool, p.Viewer, id, s.opt.Now())
	o := outcomeOf(r, "smiley.delete", err)
	switch o.kind {
	case outDone:
		s.deleteContentFile(board.SmileyDir, freed)
		http.Redirect(w, r, s.path("admin", "smilies")+"?done=deleted", http.StatusSeeOther)
	case outMissing:
		s.notFound(w, r)
	default:
		s.renderSmilies(w, r, o.status, p, smileyForm{Notice: o.msg}, "")
	}
}
