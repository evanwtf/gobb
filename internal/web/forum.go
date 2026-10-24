package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
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
	// CanStart shows the New topic button. PostHref is the address of the form.
	CanStart bool
	PostHref string
	// Held is true after a post that waits for approval: the page then says so.
	Held bool
}

// topicRowView is a topic row with the time zone of the viewer.
type topicRowView struct {
	board.TopicRow
	Loc *time.Location
	// Forum is the forum of the topic on the "new since your last visit" list.
	// It is nil on a forum page.
	Forum *board.Forum
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
	return parseID(r.PathValue(name))
}

// parseID reads a positive integer in its canonical spelling.
func parseID(raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	// Only the canonical spelling is a page: "+5" and "05" are not.
	return id, err == nil && id > 0 && strconv.FormatInt(id, 10) == raw
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
	p, err := s.begin(w, r, "", "index")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	fp, err := board.ForumView(r.Context(), s.pool, p.Viewer, id, page)
	var link board.LinkError
	switch {
	case errors.As(err, &link):
		target, ok := s.linkTarget(link.URL)
		if !ok {
			s.notFound(w, r)
			return
		}
		http.Redirect(w, r, target, http.StatusFound)
		return
	case errors.Is(err, board.ErrForbidden):
		s.denied(w, p, "this forum")
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
		CanStart:   fp.CanStart,
		PostHref:   s.path("f", strconv.FormatInt(id, 10), "post"),
		Held:       r.URL.Query().Get("held") == "1",
	}
	s.render(w, http.StatusOK, "forum.html", p)
}

// linkTarget turns the link_url of a link forum into a URL that is safe to send
// a visitor to. A site path gets the base path in front. An https or mailto URL
// goes as it is. An http URL becomes the https URL. A scheme may be in any
// case. Any other value, such as a relative path, a scheme-relative "//host",
// or a javascript: or data: URL, gives false, because the board links nowhere
// else. The upgrade is the one the renderer applies to an image.
func (s *site) linkTarget(raw string) (string, bool) {
	switch {
	case strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") && !strings.Contains(raw, `\`):
		return s.base + raw[1:], true
	case hasPrefixFold(raw, "https://"), hasPrefixFold(raw, "mailto:"):
		return raw, true
	case hasPrefixFold(raw, "http://"):
		return "https://" + raw[len("http://"):], true
	}
	return "", false
}

// hasPrefixFold reports whether s starts with prefix, ignoring ASCII case. A
// scheme has no case, and the importer accepts any.
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
