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
	// Notice is a message above the results: why the query was refused, or a
	// hint. It is empty when there is none.
	Notice string
}

// echo returns s as text for a form field: invalid UTF-8 and NUL bytes, which a
// query that search refused may hold, become U+FFFD and nothing.
func echo(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, "\uFFFD"), "\x00", "")
}

// parseForum reads a ?forum= value: empty or ASCII digits only. 0 means every
// forum.
func parseForum(s string) (int64, bool) {
	if s == "" {
		return 0, true
	}
	if len(s) > 18 {
		return 0, false
	}
	var n int64
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int64(s[i]-'0')
	}
	return n, true
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
	forumID, ok := parseForum(query.Get("forum"))
	if !ok {
		s.fail(w, r, http.StatusBadRequest, "Bad request", "The forum number in this address is not valid.")
		return
	}
	sort := "new"
	if query.Get("sort") == "best" {
		sort = "best"
	}
	d := searchData{Text: query.Get("q"), Author: query.Get("author"), Sort: sort, ForumID: forumID}
	status := http.StatusOK

	sp, err := board.Search(r.Context(), s.pool, p.Viewer, board.SearchQuery{
		Text: d.Text, ForumID: forumID, Author: d.Author, Sort: sort, Page: page,
	})
	switch {
	case errors.Is(err, board.ErrSearchTooLong):
		status, d.Notice = http.StatusBadRequest, "The search words or the author name are longer than 256 bytes. Shorten them and try again."
	case errors.Is(err, board.ErrSearchBadText):
		status, d.Notice = http.StatusBadRequest, "The search words or the author name hold a character that search cannot use."
	case errors.Is(err, board.ErrSearchTooManyTerms):
		status, d.Notice = http.StatusBadRequest, "Use at most "+strconv.Itoa(board.MaxSearchTerms)+" words in one search. Remove some and try again."
	case errors.Is(err, board.ErrSearchNoWord):
		d.Notice = "Add a word to find."
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
			Label: strings.Repeat("\u00a0\u00a0", f.Depth) + f.Name,
		})
	}
	// A refused query shows the form and the message, and no results.
	d.Text, d.Author = echo(d.Text), echo(d.Author)
	if d.Notice == "" {
		d.Searched = strings.TrimSpace(d.Text) != "" || strings.TrimSpace(d.Author) != ""
		d.Hits, d.Total = sp.Hits, sp.Total
		d.Capped = sp.Total >= 1000
		d.Pager = newPagerWith(func(n int) string {
			return s.searchHref(d.Text, forumID, d.Author, sort, n)
		}, sp.Page, sp.Pages)
	}
	p.Data = d
	s.render(w, status, "search.html", p)
}
