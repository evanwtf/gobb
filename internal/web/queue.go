package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/evanwtf/gobb/internal/board"
)

// The approval queue at /queue: the held posts in the forums where the viewer
// may approve, oldest first, each with Approve and Delete. A viewer who may
// approve nowhere, a guest too, gets the themed 404 page.

// queueData is the data of queue.html.
type queueData struct {
	Crumbs []crumb
	Posts  []board.HeldPost
	Total  int
	Pager  *pager
}

// queuePage serves GET /queue.
func (s *site) queuePage(w http.ResponseWriter, r *http.Request) {
	page, err := board.ParsePage(r.URL.Query().Get("page"))
	if err != nil {
		s.badPage(w, r)
		return
	}
	p, err := s.begin(w, r, "Approval queue", "queue")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	q, err := board.HeldPosts(r.Context(), s.pool, p.Viewer, page)
	switch {
	case errors.Is(err, board.ErrForbidden), errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	p.Noindex = true
	p.Data = queueData{
		Crumbs: []crumb{{Label: "Index", Href: s.path()}},
		Posts:  q.Posts,
		Total:  q.Total,
		Pager: newPagerWith(func(n int) string {
			if n > 1 {
				return s.path("queue") + "?page=" + strconv.Itoa(n)
			}
			return s.path("queue")
		}, q.Page, q.Pages),
	}
	s.render(w, http.StatusOK, "queue.html", p)
}

// fromQueue reports whether a moderator step came from the queue page: its
// form holds from=queue. The answer then goes back to the queue.
func fromQueue(r *http.Request) bool {
	return r.PostForm.Get("from") == "queue"
}
