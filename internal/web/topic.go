package web

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/markup"
)

// topicData is the data of topic.html.
type topicData struct {
	Topic  board.TopicRow
	Forum  *board.Forum
	Crumbs []crumb
	Posts  []postView
	Poll   *pollView
	Pager  *pager
}

// postView is a post with its rendered HTML.
type postView struct {
	board.Post
	// HTML and SigHTML come from markup.Render, which escapes everything.
	HTML, SigHTML template.HTML
	Loc           *time.Location
}

// pollView is a poll with the share of each option.
type pollView struct {
	board.Poll
	Options []optionView
	// Max is the scale of the bars: the total, and 1 when there are no votes.
	Max int
}

// optionView is one option of a poll with its share of the votes.
type optionView struct {
	board.PollOption
	Percent int
}

// topic serves GET /t/{id}.
func (s *site) topic(w http.ResponseWriter, r *http.Request) {
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
	ctx := r.Context()
	tp, err := board.TopicView(ctx, s.pool, p.Viewer, id, page)
	var moved board.MovedError
	switch {
	case errors.As(err, &moved):
		http.Redirect(w, r, s.path("t", strconv.FormatInt(moved.To, 10)), http.StatusMovedPermanently)
		return
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	// A top-level navigation counts a view and marks the page read, but only once
	// the page is built: a page that fails writes nothing. The page shows the view
	// that it counts.
	navigated := isNavigation(r)
	if navigated {
		tp.Topic.Views++
	}
	smileys, err := board.SmileyLookup(ctx, s.pool)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rc := markup.RenderContext{Smiley: smileys, BasePath: s.base}
	if !tp.CanDownload {
		// An attachment image that the viewer may not load shows as a note.
		note := "not available to you"
		if p.Viewer.UserID == 0 {
			note = "sign in to view"
		}
		rc.ImageNote = func(src string) string {
			if strings.HasPrefix(src, "/a/") {
				return note
			}
			return ""
		}
	}

	d := topicData{Topic: tp.Topic, Forum: tp.Forum}
	d.Crumbs = s.crumbsFor(tp.Crumbs)
	d.Crumbs = append(d.Crumbs, crumb{Label: tp.Forum.Name, Href: s.path("f", strconv.FormatInt(tp.Forum.ID, 10))})
	d.Posts = make([]postView, len(tp.Posts))
	for i, post := range tp.Posts {
		pv := postView{Post: post, Loc: p.Viewer.Loc}
		pv.HTML = markup.Render(post.Tree, rc).HTML
		if post.Signature != nil {
			pv.SigHTML = markup.Render(post.Signature, rc).HTML
		}
		d.Posts[i] = pv
	}
	if tp.Poll != nil {
		d.Poll = newPollView(*tp.Poll)
	}
	d.Pager = newPager(s.path("t", strconv.FormatInt(id, 10)), tp.Page, tp.Pages)
	p.Title = tp.Topic.Title
	p.Data = d
	// Only a navigation has bookkeeping to write. Any other request (an image
	// load, a prefetch) gets nil, so the page is built once and not twice.
	var after func()
	if navigated {
		after = func() { s.countView(r, p.Viewer, &tp) }
	}
	s.renderThen(w, http.StatusOK, "topic.html", p, after)
}

// newPollView adds the share of each option, as a whole percent of the total.
func newPollView(p board.Poll) *pollView {
	v := &pollView{Poll: p, Max: max(p.Total, 1)}
	for _, o := range p.Options {
		pct := 0
		if p.Total > 0 {
			pct = (o.Votes*100 + p.Total/2) / p.Total
		}
		v.Options = append(v.Options, optionView{PollOption: o, Percent: pct})
	}
	return v
}

// post serves GET /p/{id}: a redirect to the page of the topic that holds the
// post. The permission check runs first, so a hidden post gives the 404 of a
// missing one.
func (s *site) post(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, err := s.begin(w, r, "", "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirectToPost(w, r, p.Viewer, id)
}

// redirectToPost answers 302 to the page of the topic that holds post postID, as
// viewer v sees it. A post that v may not see gives the 404 of a missing one.
func (s *site) redirectToPost(w http.ResponseWriter, r *http.Request, v board.Viewer, postID int64) {
	topicID, page, err := board.PostLocation(r.Context(), s.pool, v, postID)
	if err != nil {
		s.postRedirectError(w, r, err)
		return
	}
	target := pageHref(s.path("t", strconv.FormatInt(topicID, 10)), page) + "#p" + strconv.FormatInt(postID, 10)
	http.Redirect(w, r, target, http.StatusFound)
}

// postRedirectError answers the error of a lookup that precedes a redirect to a
// post: a missing or hidden row is a 404, anything else a 500.
func (s *site) postRedirectError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, board.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	s.serverError(w, r, err)
}

// inlineTypes are the attachment types that the browser may show in the page.
// Any other type goes out as a download, in a sandbox, because the file would
// otherwise run on the origin of the site.
var inlineTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

// attachmentFailed answers the error of [board.AttachmentFile] and reports
// whether it did: 404 for a file that is missing or hidden, 403 for a viewer who
// may not download it. The old links to an attachment use it too.
func (s *site) attachmentFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
	case errors.Is(err, board.ErrForbidden):
		s.fail(w, r, http.StatusForbidden, "No access", "You do not have the right to download this file.")
	default:
		s.serverError(w, r, err)
	}
	return true
}

// attachment serves GET /a/{id}. The check is in board.AttachmentFile. The
// download count does not change.
func (s *site) attachment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, err := s.begin(w, r, "", "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	meta, err := board.AttachmentFile(r.Context(), s.pool, p.Viewer, id)
	if s.attachmentFailed(w, r, err) {
		return
	}
	root, err := os.OpenRoot(s.opt.DataDir)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(filepath.FromSlash(meta.Path))
	if err != nil {
		// The row names a file that is gone or outside the data directory.
		s.serverError(w, r, fmt.Errorf("attachment %d: %w", id, err))
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		s.serverError(w, r, fmt.Errorf("attachment %d: not a regular file", id))
		return
	}

	h := w.Header()
	disposition := "inline"
	contentType := meta.Mime
	if !inlineTypes[meta.Mime] {
		disposition = "attachment"
		contentType = "application/octet-stream"
		h.Set("Content-Security-Policy", "sandbox")
	}
	h.Set("Content-Type", contentType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, max-age=3600")
	h.Set("Content-Disposition", disposition+"; filename*=UTF-8''"+encodeFilename(meta.Name))
	http.ServeContent(w, r, "", time.Time{}, f)
}

// encodeFilename percent-encodes name for the filename* parameter (RFC 8187):
// every byte except the attr-char set becomes %XX.
func encodeFilename(name string) string {
	const attrChars = "!#$&+-.^_`|~"
	var sb strings.Builder
	for _, b := range []byte(name) {
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9', strings.IndexByte(attrChars, b) >= 0:
			sb.WriteByte(b)
		default:
			sb.WriteString("%" + strings.ToUpper(fmt.Sprintf("%02x", b)))
		}
	}
	return sb.String()
}

// initial returns the first letter or digit of name, in upper case, for an
// avatar that has no image. It returns "?" when the name has none.
func initial(name string) string {
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return string(unicode.ToUpper(r))
		}
	}
	return "?"
}
