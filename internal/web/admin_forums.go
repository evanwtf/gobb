package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/evanwtf/gobb/internal/board"
)

// The forums part of the admin area: the tree of forums, and the forms that
// create, edit, move, reorder and lock a forum. Every route is on the admin mux,
// so the gate in front of it lets in admins only. Every write is a board function
// that goes through [board.AdminWrite]: it writes its mod_log row in the same
// transaction. None of these writes needs the confirmed password: the issue does
// not mark one as dangerous.

// maxAdminForumBody is the cap on the body of the create and edit forms. The
// rules field is the largest part: 5000 characters, each at most 4 bytes, and
// each byte written as %XX in a urlencoded form.
const maxAdminForumBody = 100 << 10

// forumNotices are the notices that ?done=... can show on the forum pages. Any
// other value shows none.
var forumNotices = map[string]string{
	"created":   "The forum was created.",
	"updated":   "The forum was saved.",
	"moved":     "The forum was moved.",
	"reordered": "The order was saved.",
	"locked":    "The forum is locked.",
	"unlocked":  "The forum is unlocked.",
	"nochange":  "Nothing changed.",
}

// adminForumRoutes adds the routes of the forum pages.
func (s *site) adminForumRoutes(a adminRoutes) {
	a.page("/forums", s.adminForums)
	a.page("/forums/new", s.adminForumNew)
	a.write("/forums/new", maxAdminForumBody, s.adminForumCreate)
	a.page("/forums/{id}", s.adminForumEdit)
	a.write("/forums/{id}/edit", maxAdminForumBody, s.adminForumSave)
	a.write("/forums/{id}/move", maxPostBody, s.adminForumMove)
	a.write("/forums/{id}/reorder", maxPostBody, s.adminForumReorder)
	a.write("/forums/{id}/lock", maxPostBody, s.adminForumLock(true))
	a.write("/forums/{id}/unlock", maxPostBody, s.adminForumLock(false))
}

// forumsData is the data of admin-forums.html.
type forumsData struct {
	Crumbs []crumb
	// Done is the notice of a change that succeeded. Notice is an error.
	Done, Notice string
	Tree         forumList
	// Empty is true when the board has no forum.
	Empty bool
}

// forumList is the forums under one parent. The template sets CSRF with With,
// from the token of the page, because the buttons of each row need it and a
// sub-template does not see the page.
type forumList struct {
	CSRF string
	Rows []forumRow
}

// With returns the list with the csrf token set.
func (l forumList) With(csrf string) forumList {
	l.CSRF = csrf
	return l
}

// forumRow is one row of the tree.
type forumRow struct {
	*board.AdminForum
	// First and Last are true for the first and the last forum under the parent.
	First, Last bool
	Kids        forumList
}

// treeRows builds the rows of a list of siblings.
func treeRows(list []*board.AdminForum) forumList {
	var out forumList
	for i, f := range list {
		out.Rows = append(out.Rows, forumRow{AdminForum: f, First: i == 0, Last: i == len(list)-1, Kids: treeRows(f.Children)})
	}
	return out
}

// forumsHref returns the link to the forum list, with a notice and the row to
// show.
func (s *site) forumsHref(done string, id int64) string {
	href := s.path("admin", "forums")
	if done != "" {
		href += "?done=" + done
	}
	if id != 0 {
		href += "#f" + strconv.FormatInt(id, 10)
	}
	return href
}

// forumHref returns the link to the page of forum id.
func (s *site) forumHref(id int64, done string) string {
	href := s.path("admin", "forums", strconv.FormatInt(id, 10))
	if done != "" {
		href += "?done=" + done
	}
	return href
}

// adminForums serves GET /admin/forums.
func (s *site) adminForums(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "Forums")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderForums(w, r, p, http.StatusOK, forumNotices[r.URL.Query().Get("done")], "")
}

// renderForums writes the forum list with a notice or an error.
func (s *site) renderForums(w http.ResponseWriter, r *http.Request, p *page, status int, done, notice string) {
	tree, err := board.AdminForums(r.Context(), s.pool, p.Viewer)
	if err != nil {
		s.adminForumError(w, r, err)
		return
	}
	p.Data = forumsData{Crumbs: s.adminCrumbs(), Done: done, Notice: notice, Tree: treeRows(tree), Empty: len(tree) == 0}
	s.render(w, status, "admin-forums.html", p)
}

// adminForumError answers an error that a form cannot show: a missing forum is
// the 404 page, anything else is a 500.
func (s *site) adminForumError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, board.ErrNotFound) || errors.Is(err, board.ErrForbidden) {
		s.notFound(w, r)
		return
	}
	s.serverError(w, r, err)
}

// forumFormData is the data of admin-forum.html.
type forumFormData struct {
	Crumbs []crumb
	// New is true for the create form.
	New bool
	// Forum is the stored forum of the edit page; it is nil on the create form.
	Forum *board.AdminForum
	// The values of the form, as the admin sent them.
	Kind, Name, Description, Rules, LinkURL string
	// Parent is the ID of the parent as text, or empty for the top level.
	// GrantsFrom is the ID of the source of the grants, or empty for none.
	Parent, GrantsFrom string
	// Err holds the message of each field that failed, by the name of the field.
	// Notice is the message for the whole page. Done is a success notice.
	Err          map[string]string
	Notice, Done string
	// Parents are the places a forum may go. Sources are the forums whose grants
	// a new forum may copy.
	Parents, Sources []forumChoice
	Kinds            []string
}

// forumChoice is one option of a list of forums.
type forumChoice struct {
	ID    int64
	Label string
}

// forumChoices lists the forums in tree order, each name indented by its depth.
// skip, when set, leaves out that forum and everything below it. links is false
// to leave out the links, which cannot hold a subforum.
func forumChoices(tree []*board.AdminForum, skip int64, links bool) []forumChoice {
	var out []forumChoice
	var walk func(list []*board.AdminForum)
	walk = func(list []*board.AdminForum) {
		for _, f := range list {
			if f.ID == skip {
				continue
			}
			if links || f.Kind != "link" {
				out = append(out, forumChoice{ID: f.ID, Label: strings.Repeat("  ", f.Depth) + f.Name})
			}
			walk(f.Children)
		}
	}
	walk(tree)
	return out
}

// findForum returns the forum id in the tree, or nil.
func findForum(tree []*board.AdminForum, id int64) *board.AdminForum {
	for _, f := range board.Flatten(tree) {
		if f.ID == id {
			return f
		}
	}
	return nil
}

// idField reads a form field that holds an ID or nothing. Empty gives 0. Any
// other value that is not a whole number above 0 gives false.
func idField(raw string) (int64, bool) {
	if raw == "" {
		return 0, true
	}
	return parseID(raw)
}

// adminForumNew serves GET /admin/forums/new. The query "parent" picks the
// parent; the source of the grants is the parent.
func (s *site) adminForumNew(w http.ResponseWriter, r *http.Request) {
	d := forumFormData{New: true, Kind: "forum"}
	if id, ok := idField(r.URL.Query().Get("parent")); ok && id != 0 {
		d.Parent, d.GrantsFrom = strconv.FormatInt(id, 10), strconv.FormatInt(id, 10)
	}
	p, err := s.adminBegin(w, r, "New forum")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderForumForm(w, r, p, http.StatusOK, 0, d)
}

// renderForumForm writes the create form (id 0) or the edit page of forum id with
// d. It fills in the stored forum and the lists, and the values of the edit
// page when d holds none.
func (s *site) renderForumForm(w http.ResponseWriter, r *http.Request, p *page, status int, id int64, d forumFormData) {
	tree, err := board.AdminForums(r.Context(), s.pool, p.Viewer)
	if err != nil {
		s.adminForumError(w, r, err)
		return
	}
	d.Crumbs = append(s.adminCrumbs(), crumb{Label: "Forums", Href: s.path("admin", "forums")})
	d.Kinds = board.ForumKinds
	if id == 0 {
		d.New = true
		d.Parents = forumChoices(tree, 0, false)
		d.Sources = forumChoices(tree, 0, true)
	} else {
		f := findForum(tree, id)
		if f == nil {
			s.notFound(w, r)
			return
		}
		d.Forum = f
		d.Parents = forumChoices(tree, id, false)
		if d.Kind == "" {
			d.Kind, d.Name, d.Description, d.Rules, d.LinkURL = f.Kind, f.Name, f.Description, f.Rules, f.LinkURL
		}
		if d.Parent == "" && f.ParentID != 0 {
			d.Parent = strconv.FormatInt(f.ParentID, 10)
		}
	}
	p.Data = d
	s.render(w, status, "admin-forum.html", p)
}

// formInput reads the fields of the create and edit forms.
func formInput(r *http.Request) board.ForumInput {
	f := r.PostForm
	return board.ForumInput{Kind: f.Get("kind"), Name: f.Get("name"), Description: f.Get("description"),
		Rules: f.Get("rules"), LinkURL: f.Get("link_url")}
}

// inputValues returns the form data that echoes in.
func inputValues(in board.ForumInput) forumFormData {
	return forumFormData{Kind: in.Kind, Name: in.Name, Description: in.Description, Rules: in.Rules, LinkURL: in.LinkURL}
}

// showInputError renders the form again with the message of err beside its field
// and status 400, and reports true. An error that is not an input error is
// handled as a missing forum (404) or a server error, and also gives true. It
// gives false for a nil error.
func (s *site) showInputError(w http.ResponseWriter, r *http.Request, p *page, id int64, d forumFormData, err error) bool {
	var in *board.InputError
	switch {
	case err == nil:
		return false
	case errors.As(err, &in):
		d.Err = map[string]string{in.Field: in.Message}
		s.renderForumForm(w, r, p, http.StatusBadRequest, id, d)
	default:
		s.adminForumError(w, r, err)
	}
	return true
}

// adminForumCreate serves POST /admin/forums/new.
func (s *site) adminForumCreate(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "New forum")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	in := formInput(r)
	d := inputValues(in)
	d.Parent, d.GrantsFrom = r.PostForm.Get("parent"), r.PostForm.Get("grants_from")
	parent, ok := idField(d.Parent)
	if !ok {
		s.showInputError(w, r, p, 0, d, &board.InputError{Field: "parent", Message: "Pick a parent from the list."})
		return
	}
	from, ok := idField(d.GrantsFrom)
	if !ok {
		s.showInputError(w, r, p, 0, d, &board.InputError{Field: "grants_from", Message: "Pick a forum from the list, or none."})
		return
	}
	in.ParentID, in.GrantsFrom = parent, from
	id, err := board.CreateForum(r.Context(), s.pool, p.Viewer, in, s.opt.Now())
	if s.showInputError(w, r, p, 0, d, err) {
		return
	}
	http.Redirect(w, r, s.forumsHref("created", id), http.StatusSeeOther)
}

// forumID reads the ID in the path of a forum route, or answers 404.
func (s *site) forumID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
	}
	return id, ok
}

// adminForumEdit serves GET /admin/forums/{id}.
func (s *site) adminForumEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := s.forumID(w, r)
	if !ok {
		return
	}
	p, err := s.adminBegin(w, r, "Edit forum")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderForumForm(w, r, p, http.StatusOK, id, forumFormData{Done: forumNotices[r.URL.Query().Get("done")]})
}

// finish answers the end of a write on forum id: a 303 to the page at where
// ("page" for the page of the forum, "list" for the forum list) with the notice
// done, or for [board.ErrNoChange] the notice "Nothing changed". A failure that
// is not a missing forum or a bad input is a 500.
func (s *site) finish(w http.ResponseWriter, r *http.Request, id int64, err error, done, where string) {
	if errors.Is(err, board.ErrNoChange) {
		err, done = nil, "nochange"
	}
	if err != nil {
		s.adminForumError(w, r, err)
		return
	}
	target := s.forumHref(id, done)
	if where == "list" {
		target = s.forumsHref(done, id)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// adminForumSave serves POST /admin/forums/{id}/edit.
func (s *site) adminForumSave(w http.ResponseWriter, r *http.Request) {
	id, ok := s.forumID(w, r)
	if !ok {
		return
	}
	p, err := s.adminBegin(w, r, "Edit forum")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	in := formInput(r)
	err = board.UpdateForum(r.Context(), s.pool, p.Viewer, id, in, s.opt.Now())
	var bad *board.InputError
	if errors.As(err, &bad) {
		s.showInputError(w, r, p, id, inputValues(in), err)
		return
	}
	s.finish(w, r, id, err, "updated", "page")
}

// adminForumMove serves POST /admin/forums/{id}/move.
func (s *site) adminForumMove(w http.ResponseWriter, r *http.Request) {
	id, ok := s.forumID(w, r)
	if !ok {
		return
	}
	p, err := s.adminBegin(w, r, "Edit forum")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	raw := r.PostForm.Get("parent")
	parent, ok := idField(raw)
	if !ok {
		err = &board.InputError{Field: "parent", Message: "Pick a parent from the list."}
	} else {
		err = board.MoveForum(r.Context(), s.pool, p.Viewer, id, parent, s.opt.Now())
	}
	var bad *board.InputError
	if errors.As(err, &bad) {
		s.showInputError(w, r, p, id, forumFormData{Parent: raw}, err)
		return
	}
	s.finish(w, r, id, err, "moved", "page")
}

// adminForumReorder serves POST /admin/forums/{id}/reorder, from the up and down
// buttons of the forum list.
func (s *site) adminForumReorder(w http.ResponseWriter, r *http.Request) {
	id, ok := s.forumID(w, r)
	if !ok {
		return
	}
	p, err := s.adminBegin(w, r, "Forums")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	err = board.ReorderForum(r.Context(), s.pool, p.Viewer, id, r.PostForm.Get("dir"), s.opt.Now())
	var bad *board.InputError
	if errors.As(err, &bad) {
		s.renderForums(w, r, p, http.StatusBadRequest, "", bad.Message)
		return
	}
	s.finish(w, r, id, err, "reordered", "list")
}

// adminForumLock serves POST /admin/forums/{id}/lock and /unlock.
func (s *site) adminForumLock(locked bool) http.HandlerFunc {
	done := "unlocked"
	if locked {
		done = "locked"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.forumID(w, r)
		if !ok {
			return
		}
		p, err := s.adminBegin(w, r, "Edit forum")
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		err = board.SetForumLock(r.Context(), s.pool, p.Viewer, id, locked, s.opt.Now())
		var bad *board.InputError
		if errors.As(err, &bad) {
			s.showInputError(w, r, p, id, forumFormData{}, err)
			return
		}
		s.finish(w, r, id, err, done, "page")
	}
}
