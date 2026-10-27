package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/evanwtf/gobb/internal/board"
)

// The rank editor of the admin area: the titles and images of the members.

// rankDone are the notices that the rank pages can show. Any other value shows
// nothing.
var rankDone = map[string]string{
	"created":  "The rank is added.",
	"saved":    "The rank is saved.",
	"deleted":  "The rank is deleted.",
	"nochange": "Nothing changed.",
}

// rankForm holds the values of the rank form, as the admin typed them, and the
// message beside each field.
type rankForm struct {
	Title, MinPosts string
	Special, Remove bool
	Err             fieldErrs
	// Notice is a message on top of the form: a failed write.
	Notice string
}

// adminRanksData is the data of admin-ranks.html: the list and the form that adds
// a rank.
type adminRanksData struct {
	Crumbs []crumb
	Rows   []adminRankRow
	Add    rankForm
	Done   string
	Action string
}

// adminRankRow is one rank of the list.
type adminRankRow struct {
	board.AdminRank
	Src, EditHref, DeleteHref string
}

// rankCrumbs returns the trail Index > Admin > Ranks.
func (s *site) rankCrumbs() []crumb {
	return append(s.adminCrumbs(), crumb{Label: "Ranks", Href: s.path("admin", "ranks")})
}

// imageSrc returns the site path of the image file under the data directory, or
// empty for no image.
func (s *site) imageSrc(path string) string {
	if path == "" {
		return ""
	}
	return s.path("files", path)
}

// renderRanks writes the list of ranks. add holds the values of the add form.
func (s *site) renderRanks(w http.ResponseWriter, r *http.Request, status int, p *page, add rankForm, done string) {
	list, err := board.AdminRanks(r.Context(), s.pool)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := adminRanksData{Crumbs: s.rankCrumbs(), Add: add, Done: rankDone[done], Action: s.path("admin", "ranks")}
	if add.MinPosts == "" && add.Err == nil {
		d.Add.MinPosts = "0"
	}
	for _, rk := range list {
		id := strconv.FormatInt(rk.ID, 10)
		d.Rows = append(d.Rows, adminRankRow{AdminRank: rk, Src: s.imageSrc(rk.Image),
			EditHref: s.path("admin", "ranks", id), DeleteHref: s.path("admin", "ranks", id, "delete")})
	}
	p.Data = d
	s.render(w, status, "admin-ranks.html", p)
}

// adminRanks serves GET /admin/ranks.
func (s *site) adminRanks(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "Ranks")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderRanks(w, r, http.StatusOK, p, rankForm{}, r.URL.Query().Get("done"))
}

// adminRankData is the data of admin-rank.html.
type adminRankData struct {
	Crumbs []crumb
	Rank   board.AdminRank
	Src    string
	Form   rankForm
	Action string
}

// storedRank builds the page of rank id of the path. When it fails, it has
// answered.
func (s *site) storedRank(w http.ResponseWriter, r *http.Request) (*page, adminRankData, bool) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return nil, adminRankData{}, false
	}
	p, err := s.adminBegin(w, r, "Rank")
	if err != nil {
		s.serverError(w, r, err)
		return nil, adminRankData{}, false
	}
	rk, err := board.AdminRankByID(r.Context(), s.pool, id)
	switch {
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return nil, adminRankData{}, false
	case err != nil:
		s.serverError(w, r, err)
		return nil, adminRankData{}, false
	}
	idText := strconv.FormatInt(id, 10)
	return p, adminRankData{Crumbs: append(s.rankCrumbs(), crumb{Label: rk.Title, Href: s.path("admin", "ranks", idText)}),
		Rank: rk, Src: s.imageSrc(rk.Image), Action: s.path("admin", "ranks", idText),
		Form: rankForm{Title: rk.Title, MinPosts: strconv.Itoa(rk.MinPosts), Special: rk.Special}}, true
}

// renderRank writes the form of one rank with d.
func (s *site) renderRank(w http.ResponseWriter, status int, p *page, d adminRankData) {
	p.Data = d
	s.render(w, status, "admin-rank.html", p)
}

// adminRankEdit serves GET /admin/ranks/{id}.
func (s *site) adminRankEdit(w http.ResponseWriter, r *http.Request) {
	p, d, ok := s.storedRank(w, r)
	if !ok {
		return
	}
	s.renderRank(w, http.StatusOK, p, d)
}

// rankFormOf reads the rank form: the values as typed, and the input for the
// board. The threshold is read here because the board takes a number; a text
// that is no whole number is the message beside the field, and the board is not
// called.
func rankFormOf(r *http.Request) (rankForm, board.RankInput) {
	f := r.PostForm
	form := rankForm{Title: f.Get("title"), MinPosts: f.Get("min_posts"), Special: f.Get("special") == "1",
		Remove: f.Get("remove_image") == "1", Err: fieldErrs{}}
	in := board.RankInput{Title: form.Title, Special: form.Special}
	n, err := strconv.ParseInt(strings.TrimSpace(form.MinPosts), 10, 64)
	if err != nil {
		// A number too big for 64 bits is a fault too. The board names the range.
		form.Err["min_posts"] = "Enter a whole number from 0 to 2,147,483,647."
		return form, in
	}
	in.MinPosts = n
	return form, in
}

// adminRankCreate serves POST /admin/ranks. The image is optional.
func (s *site) adminRankCreate(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "Ranks")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	form, in := rankFormOf(r)
	if len(form.Err) > 0 {
		s.renderRanks(w, r, http.StatusBadRequest, p, form, "")
		return
	}
	up := s.storeImage(w, r, board.RankDir, "r")
	if up.Msg != "" {
		form.Err["image"] = up.Msg
		s.renderRanks(w, r, up.Status, p, form, "")
		return
	}
	_, err = board.AdminCreateRank(detached(r), s.pool, p.Viewer, in, up.Path, s.opt.Now())
	o := outcomeOf(r, "rank.create", err)
	if o.kind != outDone {
		s.deleteContentFile(board.RankDir, up.Path)
	}
	switch o.kind {
	case outDone:
		http.Redirect(w, r, s.path("admin", "ranks")+"?done=created", http.StatusSeeOther)
	case outMissing:
		s.notFound(w, r)
	default:
		form.Err, form.Notice = o.formAnswer(form.Err, "title", "min_posts")
		s.renderRanks(w, r, o.status, p, form, "")
	}
}

// adminRankSave serves POST /admin/ranks/{id}. A new image is optional, and so is
// the removal of the image.
func (s *site) adminRankSave(w http.ResponseWriter, r *http.Request) {
	p, d, ok := s.storedRank(w, r)
	if !ok {
		return
	}
	form, in := rankFormOf(r)
	d.Form = form
	if len(form.Err) > 0 {
		s.renderRank(w, http.StatusBadRequest, p, d)
		return
	}
	up := s.storeImage(w, r, board.RankDir, "r")
	if up.Msg != "" {
		d.Form.Err["image"] = up.Msg
		s.renderRank(w, up.Status, p, d)
		return
	}
	freed, err := board.AdminUpdateRank(detached(r), s.pool, p.Viewer, d.Rank.ID, in, up.Path, form.Remove, s.opt.Now())
	o := outcomeOf(r, "rank.update", err)
	if o.kind != outDone {
		s.deleteContentFile(board.RankDir, up.Path)
	}
	switch o.kind {
	case outDone:
		s.deleteContentFile(board.RankDir, freed)
		http.Redirect(w, r, s.path("admin", "ranks")+"?done=saved", http.StatusSeeOther)
	case outNoChange:
		http.Redirect(w, r, s.path("admin", "ranks")+"?done=nochange", http.StatusSeeOther)
	case outMissing:
		s.notFound(w, r)
	default:
		d.Form.Err, d.Form.Notice = o.formAnswer(d.Form.Err, "title", "min_posts")
		s.renderRank(w, o.status, p, d)
	}
}

// adminRankDeleteConfirm serves GET /admin/ranks/{id}/delete. The text says what
// happens to the members who hold the rank.
func (s *site) adminRankDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	rk, err := board.AdminRankByID(r.Context(), s.pool, id)
	switch {
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	idText := strconv.FormatInt(id, 10)
	lines := []string{"The rank is deleted. Its image file goes too, unless a smiley or another rank uses the same file."}
	if rk.Members > 0 {
		lines = append(lines, plural(rk.Members, "member holds", "members hold")+" this rank. They lose it, and they get the rank that their post count earns.")
	} else {
		lines = append(lines, "No member holds this rank by hand. Members who hold it by post count get the rank below it.")
	}
	s.renderDelete(w, r, append(s.rankCrumbs(), crumb{Label: rk.Title, Href: s.path("admin", "ranks", idText)}), adminDeleteData{
		Heading: "Delete the rank \"" + rk.Title + "\"?", Lines: lines,
		Action: s.path("admin", "ranks", idText, "delete"), Cancel: s.path("admin", "ranks", idText)})
}

// adminRankDelete serves POST /admin/ranks/{id}/delete.
func (s *site) adminRankDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, err := s.adminBegin(w, r, "Ranks")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	freed, err := board.AdminDeleteRank(detached(r), s.pool, p.Viewer, id, s.opt.Now())
	o := outcomeOf(r, "rank.delete", err)
	switch o.kind {
	case outDone:
		s.deleteContentFile(board.RankDir, freed)
		http.Redirect(w, r, s.path("admin", "ranks")+"?done=deleted", http.StatusSeeOther)
	case outMissing:
		s.notFound(w, r)
	default:
		s.renderRanks(w, r, o.status, p, rankForm{Notice: o.msg}, "")
	}
}
