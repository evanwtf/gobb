package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/evanwtf/gobb/internal/board"
)

// The page editor of the admin area: the static pages that /pages/{slug} serves.

// pageDone are the notices that /admin/pages?done=... and the page of one page
// can show. Any other value shows nothing.
var pageDone = map[string]string{
	"created":  "The page is added.",
	"saved":    "The page is saved.",
	"deleted":  "The page is deleted.",
	"nochange": "Nothing changed: the page is saved as it was.",
}

// adminPagesData is the data of admin-pages.html.
type adminPagesData struct {
	Crumbs []crumb
	Rows   []adminPageRow
	Done   string
}

// adminPageRow is one page of the list.
type adminPageRow struct {
	Slug, Title, EditHref, ViewHref string
	UpdatedAt                       time.Time
}

// adminPages serves GET /admin/pages.
func (s *site) adminPages(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "Pages")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	links, err := board.PageLinks(r.Context(), s.pool)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := adminPagesData{Crumbs: s.adminCrumbs(), Done: pageDone[r.URL.Query().Get("done")]}
	for _, l := range links {
		d.Rows = append(d.Rows, adminPageRow{Slug: l.Slug, Title: l.Title, UpdatedAt: l.UpdatedAt,
			EditHref: s.path("admin", "pages", l.Slug), ViewHref: s.path("pages", l.Slug)})
	}
	p.Data = d
	s.render(w, http.StatusOK, "admin-pages.html", p)
}

// adminPageData is the data of admin-page.html: the form of one page, new or
// stored.
type adminPageData struct {
	Crumbs []crumb
	// New is true for the form of a new page. The slug is a field then, and a
	// fixed text after.
	New  bool
	Form board.PageInput
	Err  fieldErrs
	// Notice is a message on top of the form: a failed write.
	Notice, Done string
	UpdatedAt    time.Time
	// OldLinks is the count of old addresses that lead to the page.
	OldLinks int
	// Action is the address of the form, ViewHref the public page and DeleteHref
	// the confirm page of the delete.
	Action, ViewHref, DeleteHref string
}

// pageCrumbs returns the trail Index > Admin > Pages.
func (s *site) pageCrumbs() []crumb {
	return append(s.adminCrumbs(), crumb{Label: "Pages", Href: s.path("admin", "pages")})
}

// renderPageForm writes the form of a page with d.
func (s *site) renderPageForm(w http.ResponseWriter, status int, p *page, d adminPageData) {
	d.Crumbs = s.pageCrumbs()
	p.Data = d
	s.render(w, status, "admin-page.html", p)
}

// adminPageNew serves GET /admin/pages/new.
func (s *site) adminPageNew(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "New page")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderPageForm(w, http.StatusOK, p, adminPageData{New: true, Action: s.path("admin", "pages"),
		Form: board.PageInput{Markdown: true, Smilies: true, Autolink: true}})
}

// storedPage builds the data of the form of the page with the slug of the path.
// When it fails, it has answered.
func (s *site) storedPage(w http.ResponseWriter, r *http.Request) (*page, adminPageData, bool) {
	slug := r.PathValue("slug")
	p, err := s.adminBegin(w, r, "Page")
	if err != nil {
		s.serverError(w, r, err)
		return nil, adminPageData{}, false
	}
	f, err := board.AdminPage(r.Context(), s.pool, slug)
	switch {
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return nil, adminPageData{}, false
	case err != nil:
		s.serverError(w, r, err)
		return nil, adminPageData{}, false
	}
	p.Title = f.Title
	return p, adminPageData{Form: f.PageInput, UpdatedAt: f.UpdatedAt, OldLinks: f.OldLinks,
		Action:     s.path("admin", "pages", slug),
		ViewHref:   s.path("pages", slug),
		DeleteHref: s.path("admin", "pages", slug, "delete")}, true
}

// adminPageEdit serves GET /admin/pages/{slug}.
func (s *site) adminPageEdit(w http.ResponseWriter, r *http.Request) {
	p, d, ok := s.storedPage(w, r)
	if !ok {
		return
	}
	d.Done = pageDone[r.URL.Query().Get("done")]
	s.renderPageForm(w, http.StatusOK, p, d)
}

// pageInput reads the form of the page editor. The slug field is read for a new
// page only.
func pageInput(r *http.Request) board.PageInput {
	f := r.PostForm
	return board.PageInput{
		Slug:     strings.TrimSpace(f.Get("slug")),
		Title:    f.Get("title"),
		Source:   f.Get("body"),
		Markdown: f.Get("markdown") == "1",
		Smilies:  f.Get("smilies") == "1",
		Autolink: f.Get("autolink") == "1",
	}
}

// adminPageCreate serves POST /admin/pages.
func (s *site) adminPageCreate(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "New page")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	in := pageInput(r)
	o := outcomeOf(r, "page.create", board.AdminCreatePage(detached(r), s.pool, p.Viewer, in, s.opt.Now()))
	switch o.kind {
	case outDone:
		http.Redirect(w, r, s.path("admin", "pages", in.Slug)+"?done=created", http.StatusSeeOther)
	case outMissing:
		s.notFound(w, r)
	default:
		d := adminPageData{New: true, Form: in, Action: s.path("admin", "pages")}
		d.Err, d.Notice = o.formAnswer(nil, "slug", "title", "body")
		s.renderPageForm(w, o.status, p, d)
	}
}

// adminPageSave serves POST /admin/pages/{slug}.
func (s *site) adminPageSave(w http.ResponseWriter, r *http.Request) {
	p, d, ok := s.storedPage(w, r)
	if !ok {
		return
	}
	in := pageInput(r)
	in.Slug = d.Form.Slug
	o := outcomeOf(r, "page.update", board.AdminUpdatePage(detached(r), s.pool, p.Viewer, in.Slug, in, s.opt.Now()))
	switch o.kind {
	case outDone, outNoChange:
		done := "saved"
		if o.kind == outNoChange {
			done = "nochange"
		}
		http.Redirect(w, r, d.Action+"?done="+url.QueryEscape(done), http.StatusSeeOther)
	case outMissing:
		s.notFound(w, r)
	default:
		d.Form = in
		d.Err, d.Notice = o.formAnswer(nil, "title", "body")
		s.renderPageForm(w, o.status, p, d)
	}
}

// adminPageDeleteConfirm serves GET /admin/pages/{slug}/delete.
func (s *site) adminPageDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	f, err := board.AdminPage(r.Context(), s.pool, slug)
	switch {
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	lines := []string{"The page and its text are deleted. The address " + s.path("pages", slug) + " then answers \"not found\", and the link in the footer goes."}
	if f.OldLinks > 0 {
		lines = append(lines, plural(f.OldLinks, "old address", "old addresses")+" from the old board lead to this page. They answer \"not found\" too.")
	}
	s.renderDelete(w, r, append(s.pageCrumbs(), crumb{Label: f.Title, Href: s.path("admin", "pages", slug)}), adminDeleteData{
		Heading: "Delete the page \"" + f.Title + "\"?", Lines: lines,
		Action: s.path("admin", "pages", slug, "delete"), Cancel: s.path("admin", "pages", slug)})
}

// adminPageDelete serves POST /admin/pages/{slug}/delete.
func (s *site) adminPageDelete(w http.ResponseWriter, r *http.Request) {
	p, d, ok := s.storedPage(w, r)
	if !ok {
		return
	}
	o := outcomeOf(r, "page.delete", board.AdminDeletePage(detached(r), s.pool, p.Viewer, d.Form.Slug, s.opt.Now()))
	switch o.kind {
	case outDone:
		http.Redirect(w, r, s.path("admin", "pages")+"?done=deleted", http.StatusSeeOther)
	case outMissing:
		s.notFound(w, r)
	default:
		d.Notice = o.msg
		s.renderPageForm(w, o.status, p, d)
	}
}
