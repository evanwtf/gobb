package web

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/evanwtf/gobb/internal/board"
)

// crumb is one link of the trail above a page.
type crumb struct {
	Label, Href string
}

// forumData is the data of forum.html.
type forumData struct {
	Forum     *board.Forum
	Crumbs    []crumb
	Subforums []*board.Forum
	Pinned    []topicRowView
	Topics    []topicRowView
	Pager     *pager
	// IsCategory is true for a category: it lists forums and no topics.
	IsCategory bool
}

// topicRowView is a topic row with the time zone of the viewer.
type topicRowView struct {
	board.TopicRow
	Loc *time.Location
}

func rowViews(rows []board.TopicRow, loc *time.Location) []topicRowView {
	out := make([]topicRowView, len(rows))
	for i, r := range rows {
		out[i] = topicRowView{TopicRow: r, Loc: loc}
	}
	return out
}

// pathID reads a positive integer path value. Anything else is not a page.
func pathID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id, err == nil && id > 0
}

// badPage answers 400 for a ?page= value that ParsePage refuses.
func (s *site) badPage(w http.ResponseWriter, r *http.Request) {
	s.fail(w, r, http.StatusBadRequest, "Bad request", "The page number in this address is not valid.")
}

// crumbsFor builds the trail Index > ancestors, from the ancestors of a forum.
func (s *site) crumbsFor(ancestors []*board.Forum) []crumb {
	out := []crumb{{Label: "Index", Href: s.path()}}
	for _, f := range ancestors {
		out = append(out, crumb{Label: f.Name, Href: s.path("f", strconv.FormatInt(f.ID, 10))})
	}
	return out
}

// forum serves GET /f/{id}: a forum with its topics, or a category with its
// forums. A link forum answers 302 to its URL.
func (s *site) forum(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	page, err := board.ParsePage(r.URL.Query().Get("page"))
	if err != nil {
		s.badPage(w, r)
		return
	}
	p, err := s.begin(r, "", "index")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	fp, err := board.ForumView(r.Context(), s.pool, p.Viewer, id, page)
	var link board.LinkError
	switch {
	case errors.As(err, &link):
		http.Redirect(w, r, link.URL, http.StatusFound)
		return
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	p.Title = fp.Forum.Name
	loc := p.Viewer.Loc
	p.Data = forumData{
		Forum:      fp.Forum,
		Crumbs:     s.crumbsFor(fp.Crumbs),
		Subforums:  fp.Subforums,
		Pinned:     rowViews(fp.Pinned, loc),
		Topics:     rowViews(fp.Topics, loc),
		Pager:      newPager(s.path("f", strconv.FormatInt(id, 10)), fp.Page, fp.Pages),
		IsCategory: fp.Forum.Kind == "category",
	}
	s.render(w, http.StatusOK, "forum.html", p)
}
