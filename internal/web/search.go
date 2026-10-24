package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/evanwtf/gobb/internal/board"
)

// forumOption is one entry of the forum select of the search form.
type forumOption struct {
	ID       int64
	Label    string
	Selected bool
}

// searchData is the data of search.html.
type searchData struct {
	// Text, Author and Sort are the values of the form. Sort is "new" or "best".
	Text, Author, Sort string
	// Forums are the forums the viewer may search. ForumID is the chosen one, or 0.
	Forums  []forumOption
	ForumID int64
	// Searched is true when the query asks for something, so the page shows a
	// result list or a "no posts" line.
	Searched bool
	Hits     []board.Hit
	Total    int
	// Capped is true when the total hit the limit of 1,000.
	Capped bool
	Pager  *pager
}

// searchHref returns the link to page n of a search. The link keeps every filter
// that is not at its default. Page 1 has no page.
func (s *site) searchHref(text string, forum int64, author, sort string, n int) string {
	var q []string
	if text != "" {
		q = append(q, "q="+url.QueryEscape(text))
	}
	if forum != 0 {
		q = append(q, "forum="+strconv.FormatInt(forum, 10))
	}
	if author != "" {
		q = append(q, "author="+url.QueryEscape(author))
	}
	if sort == "best" {
		q = append(q, "sort=best")
	}
	if n > 1 {
		q = append(q, "page="+strconv.Itoa(n))
	}
	href := s.path("search")
	if len(q) > 0 {
		href += "?" + strings.Join(q, "&")
	}
	return href
}

// search serves GET /search?q=&forum=&author=&sort=&page=. A viewer without
// account.search gets 403 before any other check. A text over 256 bytes gets 400,
// and so does a page or forum number that is not a number. A page past the last
// gets 404.
func (s *site) search(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	page, err := board.ParsePage(query.Get("page"))
	if err != nil {
		s.badPage(w, r)
		return
	}
	p, err := s.begin(w, r, "Search", "search")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !p.Viewer.Perms.Has("account.search", 0) {
		p.Title = "No access"
		d := errorData{Status: http.StatusForbidden, Heading: "No access"}
		if p.Viewer.UserID == 0 {
			d.Message = "Sign in to search the board."
			d.Href, d.HrefLabel = s.loginHref(p.Return), "Sign in"
		} else {
			d.Message = "You do not have the right to search the board."
		}
		p.Data = d
		s.render(w, http.StatusForbidden, "error.html", p)
		return
	}
	var forumID int64
	if v := query.Get("forum"); v != "" {
		forumID, err = strconv.ParseInt(v, 10, 64)
		if err != nil || forumID < 0 {
			s.fail(w, r, http.StatusBadRequest, "Bad request", "The forum number in this address is not valid.")
			return
		}
	}
	sort := "new"
	if query.Get("sort") == "best" {
		sort = "best"
	}
	d := searchData{Text: query.Get("q"), Author: query.Get("author"), Sort: sort, ForumID: forumID}

	sp, err := board.Search(r.Context(), s.pool, p.Viewer, board.SearchQuery{
		Text: d.Text, ForumID: forumID, Author: d.Author, Sort: sort, Page: page,
	})
	switch {
	case errors.Is(err, board.ErrBadSearch):
		s.fail(w, r, http.StatusBadRequest, "Bad request", "The search words or the author name are too long. Use at most 256 bytes.")
		return
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	forums, err := board.SearchForums(r.Context(), s.pool, p.Viewer)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, f := range forums {
		d.Forums = append(d.Forums, forumOption{
			ID: f.ID, Selected: f.ID == forumID,
			Label: strings.Repeat("  ", f.Depth) + f.Name,
		})
	}
	d.Searched = strings.TrimSpace(d.Text) != "" || strings.TrimSpace(d.Author) != ""
	d.Hits, d.Total = sp.Hits, sp.Total
	d.Capped = sp.Total >= 1000
	d.Pager = newPagerWith(func(n int) string {
		return s.searchHref(d.Text, forumID, d.Author, sort, n)
	}, sp.Page, sp.Pages)
	p.Data = d
	s.render(w, http.StatusOK, "search.html", p)
}
