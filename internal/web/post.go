package web

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/markup"
)

// The posting pages. Every right is checked in internal/board: a page calls the
// same check as the write (CanStartTopic, CanReply, LoadEdit, LoadDelete) before
// it shows a form, and the write checks again. A button that the page shows is
// never the check.

// postKind says which posting form a request is for.
type postKind int

const (
	kindTopic postKind = iota // POST /f/{id}/post
	kindReply                 // POST /t/{id}/reply
	kindEdit                  // POST /p/{id}/edit
)

// fieldErrors are the messages beside the fields of a form.
type fieldErrors struct {
	Title, Name, Body, Reason string
}

// postFormData is the data of postform.html.
type postFormData struct {
	Heading, Context string
	Action, Cancel   string
	Submit           string
	Crumbs           []crumb
	// TopicTitle is the title of the topic of the post, for a preview.
	TopicTitle string
	ShowTitle  bool
	ShowName   bool
	ShowReason bool
	// Title, GuestName, Reason and Source hold what the member sent, byte for byte.
	Title, GuestName, Reason, Source string
	Err                              fieldErrors
	// Notice is a message for the whole form, such as a refusal.
	Notice  string
	Preview *postView
	// Bare is true for the page that only hands the text back: the target is gone,
	// or the page could not be built. It shows no title, no crumbs and no submit
	// button, only the text.
	Bare bool

	// forumID is the forum of the post, for the parse of a preview. edit is set
	// for the edit of a post.
	forumID int64
	edit    *board.EditForm
}

// confirmData is the data of confirm.html.
type confirmData struct {
	Action, Cancel string
	// First is true when the post is the first of its topic: deleting it deletes
	// the topic.
	First bool
}

// textareaValue writes s as the content of a textarea, so that a browser reads
// back the same text. It escapes & < > and the quotes, writes a carriage return
// as a character reference (a parser turns a raw CR LF into LF before it reads
// the text, and the form would then lose the CR), and adds a line break up front
// when s starts with one, because a parser drops the first line break of a
// textarea. A NUL byte and a byte that is not UTF-8 cannot be written; they
// become U+FFFD.
func textareaValue(s string) template.HTML {
	var sb strings.Builder
	if strings.HasPrefix(s, "\n") {
		sb.WriteByte('\n')
	}
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1, r == 0:
			sb.WriteString("&#65533;")
		case r == '&':
			sb.WriteString("&amp;")
		case r == '<':
			sb.WriteString("&lt;")
		case r == '>':
			sb.WriteString("&gt;")
		case r == '"':
			sb.WriteString("&#34;")
		case r == '\'':
			sb.WriteString("&#39;")
		case r == '\r':
			sb.WriteString("&#13;")
		default:
			sb.WriteString(s[i : i+n])
		}
		i += n
	}
	return template.HTML(sb.String())
}

// splitErr separates the answers of a rights check from a failure of the
// database: rights is ErrNotFound or ErrForbidden, hard is anything else.
func splitErr(err error) (rights, hard error) {
	switch {
	case err == nil:
		return nil, nil
	case errors.Is(err, board.ErrNotFound), errors.Is(err, board.ErrForbidden):
		return err, nil
	}
	return nil, err
}

// deniedTo answers 403 in the layout for a viewer who may not do what the page
// is for. A guest gets the link to sign in.
func (s *site) deniedTo(w http.ResponseWriter, p *page, action string) {
	p.Title = "No access"
	d := errorData{Status: http.StatusForbidden, Heading: "No access"}
	if p.Viewer.UserID == 0 {
		d.Message = "Sign in to " + action + "."
		d.Href, d.HrefLabel = s.loginHref(p.Return), "Sign in"
	} else {
		d.Message = "You do not have the right to " + action + "."
	}
	p.Data = d
	s.render(w, http.StatusForbidden, "error.html", p)
}

// formFrame builds the heading of the form for kind and the ID in the path, after
// the rights check of the board. rights is ErrNotFound or ErrForbidden when the
// check refuses; for ErrForbidden the frame is still filled, so a POST can show
// the form again with the text that the member sent. hard is a failure of the
// database.
func (s *site) formFrame(ctx context.Context, r *http.Request, p *page, kind postKind, id int64) (fd postFormData, rights, hard error) {
	v := p.Viewer
	fd.ShowName = v.UserID == 0
	idText := strconv.FormatInt(id, 10)
	// What needs no read comes first, so that a failed read still leaves a form
	// that can be sent again.
	switch kind {
	case kindTopic:
		fd.Heading, fd.Submit = "New topic", "Post topic"
		fd.Action, fd.Cancel = s.path("f", idText, "post"), s.path("f", idText)
		fd.ShowTitle, fd.forumID = true, id
	case kindReply:
		fd.Heading, fd.Submit = "Post a reply", "Post reply"
		fd.Action, fd.Cancel = s.path("t", idText, "reply"), s.path("t", idText)
	case kindEdit:
		fd.Heading, fd.Submit = "Edit post", "Save changes"
		fd.Action, fd.Cancel = s.path("p", idText, "edit"), s.path("p", idText)
		// A refused edit still shows the form to a member who may see the post
		// (ErrForbidden), with the title field when the member sent one.
		fd.ShowTitle = r.PostForm.Has("title")
		fd.ShowReason = r.PostForm.Has("reason")
	}
	switch kind {
	case kindTopic:
		rights, hard = splitErr(board.CanStartTopic(ctx, s.pool, v, id))
		if hard != nil || errors.Is(rights, board.ErrNotFound) {
			return fd, rights, hard
		}
		forum, crumbs, err := board.ForumHead(ctx, s.pool, v, id)
		if err != nil {
			return fd, nil, err
		}
		fd.Context = "in " + forum.Name
		fd.Crumbs = append(s.crumbsFor(crumbs), crumb{Label: forum.Name, Href: s.path("f", idText)})
	case kindReply:
		rights, hard = splitErr(board.CanReply(ctx, s.pool, v, id))
		if hard != nil || errors.Is(rights, board.ErrNotFound) {
			return fd, rights, hard
		}
		title, forum, crumbs, err := board.TopicHead(ctx, s.pool, v, id)
		if err != nil {
			return fd, nil, err
		}
		fd.Context, fd.TopicTitle = "to "+title, title
		fd.Crumbs = append(s.crumbsFor(crumbs),
			crumb{Label: forum.Name, Href: s.path("f", strconv.FormatInt(forum.ID, 10))},
			crumb{Label: title, Href: s.path("t", idText)})
		fd.forumID = forum.ID
	case kindEdit:
		e, err := board.LoadEdit(ctx, s.pool, v, id)
		rights, hard = splitErr(err)
		if hard != nil || rights != nil {
			return fd, rights, hard
		}
		forum, crumbs, err := board.ForumHead(ctx, s.pool, v, e.ForumID)
		if err != nil {
			return fd, nil, err
		}
		topicHref := s.path("t", strconv.FormatInt(e.TopicID, 10))
		fd.Context, fd.TopicTitle = "in "+e.Title, e.Title
		fd.Crumbs = append(s.crumbsFor(crumbs),
			crumb{Label: forum.Name, Href: s.path("f", strconv.FormatInt(forum.ID, 10))},
			crumb{Label: e.Title, Href: topicHref})
		fd.ShowTitle, fd.ShowReason = e.First, e.Moderated
		fd.Title, fd.Source = e.Title, e.Source
		fd.forumID, fd.edit = e.ForumID, &e
	}
	return fd, rights, nil
}

// renderForm writes the form page with status.
func (s *site) renderForm(w http.ResponseWriter, status int, p *page, fd postFormData) {
	p.Title = fd.Heading
	p.Data = fd
	s.render(w, status, "postform.html", p)
}

// refuse answers a rights failure of a form page: 404 for what the viewer may not
// see, 403 for what the viewer may not do.
func (s *site) refuse(w http.ResponseWriter, r *http.Request, p *page, rights error, action string) {
	if errors.Is(rights, board.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	s.deniedTo(w, p, action)
}

// actionFor is the verb phrase of a refusal.
var actionFor = map[postKind]string{kindTopic: "start a topic here", kindReply: "reply here", kindEdit: "edit this post"}

// postForm serves the GET of a posting form.
func (s *site) postForm(kind postKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r, "id")
		if !ok {
			s.notFound(w, r)
			return
		}
		p, err := s.begin(w, r, "", "index")
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		fd, rights, hard := s.formFrame(r.Context(), r, p, kind, id)
		if hard != nil {
			s.serverError(w, r, hard)
			return
		}
		if rights != nil {
			s.refuse(w, r, p, rights, actionFor[kind])
			return
		}
		if kind == kindReply {
			if raw := r.URL.Query().Get("quote"); raw != "" {
				quoteID, ok := parseID(raw)
				if !ok {
					s.notFound(w, r)
					return
				}
				// The text of a post that the viewer cannot see never reaches the form.
				src, err := board.QuoteSource(r.Context(), s.pool, p.Viewer, quoteID)
				if rights, hard := splitErr(err); hard != nil {
					s.serverError(w, r, hard)
					return
				} else if rights != nil {
					s.refuse(w, r, p, rights, "quote this post")
					return
				}
				fd.Source = src
			}
		}
		s.renderForm(w, http.StatusOK, p, fd)
	}
}

// The notices of the pages that hand the text back.
const (
	noticeServerError = "Something went wrong, and your post was not saved. Your text is below. Try again in a moment."
	noticeGone        = "This topic is no longer available. Copy your text before you leave this page."
)

// failStatus is 503 for a request that ran out of time and 500 for any other
// failure of the database.
func failStatus(r *http.Request, err error) int {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.DeadlineExceeded) {
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

// bareForm answers with a page that only hands the text back, in a textarea. It
// shows no title, no crumbs and no submit button, so it reveals nothing that the
// member could not see: the target is gone, or the page could not be built. p is
// nil when even the page of the viewer could not be built; the page is then a
// guest's, from the settings in memory.
func (s *site) bareForm(w http.ResponseWriter, r *http.Request, p *page, status int, notice, text string) {
	if p == nil {
		p = s.framePage(r, "Your text")
	}
	s.renderForm(w, status, p, postFormData{Heading: "Your text", Notice: notice, Source: text, Bare: true})
}

// postSubmit serves the POST of a posting form: a preview, or the save. Whatever
// happens after the csrf check, the member's text comes back: in the form, or when
// there is no form to show, in a bare one.
func (s *site) postSubmit(kind postKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		form := r.PostForm
		d := board.Draft{Title: form.Get("title"), Source: form.Get("body"), GuestName: form.Get("guest_name"), IP: s.clientIP(r)}
		reason := form.Get("reason")
		id, ok := pathID(r, "id")
		if !ok {
			s.notFound(w, r)
			return
		}
		ctx := r.Context()
		p, err := s.begin(w, r, "", "index")
		if err != nil {
			slog.Error("posting: page", "path", r.URL.Path, "err", err)
			s.bareForm(w, r, nil, failStatus(r, err), noticeServerError, d.Source)
			return
		}
		fd, rights, hard := s.formFrame(ctx, r, p, kind, id)
		// What the member sent goes back into the form byte for byte, whatever
		// happens next.
		if kind != kindEdit || fd.ShowTitle {
			fd.Title = d.Title
		}
		fd.Source, fd.GuestName, fd.Reason = d.Source, d.GuestName, reason
		if hard != nil {
			s.writeFailed(w, r, p, fd, kind, hard)
			return
		}
		if rights != nil {
			s.writeFailed(w, r, p, fd, kind, rights)
			return
		}
		if form.Get("preview") != "" {
			s.preview(w, r, p, fd, kind, d, reason)
			return
		}
		postID, err := s.save(ctx, kind, p, id, d, reason)
		if err != nil {
			s.writeFailed(w, r, p, fd, kind, err)
			return
		}
		topicID := id
		if fd.edit != nil {
			topicID = fd.edit.TopicID
		}
		s.afterPost(w, r, p, kind, topicID, postID)
	}
}

// writeFailed answers the failure of a posting step with the text of the member:
//   - an [*board.InputError]: 400, the form, and the message beside its field;
//   - [board.ErrNotFound], the target gone or hidden: 404 and a bare form;
//   - [board.ErrForbidden]: 403 and the form, with a notice;
//   - any other error, a database failure or a timeout: 500 (503 for a timeout),
//     the form, and a fixed notice that carries no detail.
func (s *site) writeFailed(w http.ResponseWriter, r *http.Request, p *page, fd postFormData, kind postKind, err error) {
	var in *board.InputError
	switch {
	case errors.As(err, &in):
		s.inputFailed(w, p, fd, in)
	case errors.Is(err, board.ErrNotFound):
		s.bareForm(w, r, p, http.StatusNotFound, noticeGone, fd.Source)
	case errors.Is(err, board.ErrForbidden):
		fd.Notice = "You do not have the right to " + actionFor[kind] + ". Your text is below."
		if p.Viewer.UserID == 0 {
			fd.Notice = "You do not have the right to " + actionFor[kind] + " as a guest. Sign in, or copy your text first."
		}
		s.renderForm(w, http.StatusForbidden, p, fd)
	default:
		slog.Error("posting failed", "path", r.URL.Path, "err", err)
		fd.Notice = noticeServerError
		s.renderForm(w, failStatus(r, err), p, fd)
	}
}

// save runs the board write for kind.
func (s *site) save(ctx context.Context, kind postKind, p *page, id int64, d board.Draft, reason string) (int64, error) {
	now := s.opt.Now()
	switch kind {
	case kindTopic:
		return board.NewTopic(ctx, s.pool, p.Viewer, id, d, now)
	case kindReply:
		return board.Reply(ctx, s.pool, p.Viewer, id, d, now)
	}
	return id, board.EditPost(ctx, s.pool, p.Viewer, id, d, reason, now)
}

// inputFailed shows the form again with 400 and the message beside its field.
func (s *site) inputFailed(w http.ResponseWriter, p *page, fd postFormData, in *board.InputError) {
	switch in.Field {
	case "title":
		fd.Err.Title = in.Message
	case "guest_name":
		fd.Err.Name = in.Message
	case "reason":
		fd.Err.Reason = in.Message
	default:
		fd.Err.Body = in.Message
	}
	s.renderForm(w, http.StatusBadRequest, p, fd)
}

// afterPost answers a saved post with 303 to /p/{id}. A post that waits for
// approval is not visible to a guest (or to anyone else who cannot approve), so
// /p/{id} would be a 404: the answer then goes to the topic, or to the forum for
// a new topic, with the note that the post waits. For an edit, id is the topic
// of the post, and the answer goes to the topic with no note. For a new topic and
// a reply, id is the forum and the topic.
func (s *site) afterPost(w http.ResponseWriter, r *http.Request, p *page, kind postKind, id, postID int64) {
	_, _, err := board.PostLocation(r.Context(), s.pool, p.Viewer, postID)
	switch {
	case err == nil:
		http.Redirect(w, r, s.path("p", strconv.FormatInt(postID, 10)), http.StatusSeeOther)
	case errors.Is(err, board.ErrNotFound):
		http.Redirect(w, r, s.heldTarget(kind, id), http.StatusSeeOther)
	default:
		// The post is saved. Send the member to the page that holds it.
		slog.Warn("posting: locate the saved post", "post", postID, "err", err)
		http.Redirect(w, r, s.heldTarget(kind, id), http.StatusSeeOther)
	}
}

// heldTarget is where a saved post that the viewer cannot see sends the viewer.
func (s *site) heldTarget(kind postKind, id int64) string {
	idText := strconv.FormatInt(id, 10)
	switch kind {
	case kindTopic:
		return s.path("f", idText) + "?held=1"
	case kindReply:
		return s.path("t", idText) + "?held=1"
	}
	return s.path("t", idText)
}

// preview answers 200 with the form and the rendered post above it. It saves
// nothing. It runs the checks of a save that do not need the body
// ([board.CheckDraft]: the title, the guest name, the reason), then
// board.ParseBody as the save does (ParseBodyKeeping with the stored flags and
// tree for an edit, exactly as EditPost passes them), so a preview never
// disagrees with the save.
func (s *site) preview(w http.ResponseWriter, r *http.Request, p *page, fd postFormData, kind postKind, d board.Draft, reason string) {
	ctx := r.Context()
	checks := board.DraftChecks{Author: kind != kindEdit, Title: kind == kindTopic}
	if fd.edit != nil {
		checks.Title, checks.Reason = fd.edit.First, fd.edit.Moderated
	}
	if err := board.CheckDraft(ctx, s.pool, p.Viewer, d, reason, checks, s.opt.Now()); err != nil {
		s.writeFailed(w, r, p, fd, kind, err)
		return
	}
	var (
		body board.Body
		err  error
	)
	if fd.edit != nil {
		body, err = board.ParseBodyKeeping(ctx, s.pool, p.Viewer, fd.forumID, d.Source, &fd.edit.Flags, fd.edit.Stored)
	} else {
		body, err = board.ParseBody(ctx, s.pool, p.Viewer, fd.forumID, d.Source, nil)
	}
	if err != nil {
		s.writeFailed(w, r, p, fd, kind, err)
		return
	}
	rc, err := s.renderContext(ctx, p.Viewer, fd.forumID)
	if err != nil {
		s.writeFailed(w, r, p, fd, kind, err)
		return
	}
	rendered := markup.Render(body.Tree, rc)
	p.addRendered(rendered)
	pv := postView{HTML: rendered.HTML, Loc: p.Viewer.Loc, Preview: true}
	pv.Post = board.Post{At: s.opt.Now(), Visibility: "approved"}
	if v := p.Viewer; v.UserID != 0 {
		card, err := board.Card(ctx, s.pool, v.UserID)
		if err != nil {
			s.writeFailed(w, r, p, fd, kind, err)
			return
		}
		pv.Author = board.Author{ID: v.UserID, Name: v.Name}
		pv.Card = &card
	} else {
		name := strings.TrimSpace(d.GuestName)
		if name == "" {
			name = "Guest"
		}
		pv.Author = board.Author{Name: name}
	}
	fd.Preview = &pv
	s.renderForm(w, http.StatusOK, p, fd)
}

// renderContext returns the context that markup.Render needs for a viewer in a
// forum: the smilies, the base path, and the note for an attachment image that
// the viewer may not load.
func (s *site) renderContext(ctx context.Context, v board.Viewer, forumID int64) (markup.RenderContext, error) {
	smileys, err := board.SmileyLookup(ctx, s.pool)
	if err != nil {
		return markup.RenderContext{}, err
	}
	rc := markup.RenderContext{Smiley: smileys, BasePath: s.base}
	if !board.MayDownload(v, forumID) {
		note := "not available to you"
		if v.UserID == 0 {
			note = "sign in to view"
		}
		rc.ImageNote = func(src string) string {
			if strings.HasPrefix(src, "/a/") {
				return note
			}
			return ""
		}
	}
	return rc, nil
}

// deletePost serves POST /p/{id}/delete. A POST without confirm=1 changes
// nothing: it answers with a page that asks "Delete this post?" and holds the
// second form. A POST with confirm=1 deletes.
func (s *site) deletePost(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	ctx := r.Context()
	p, err := s.begin(w, r, "", "index")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	idText := strconv.FormatInt(id, 10)
	dt, err := board.LoadDelete(ctx, s.pool, p.Viewer, id)
	if s.deleteFailed(w, r, p, err) {
		return
	}
	if r.PostForm.Get("confirm") != "1" {
		p.Title = "Delete this post?"
		p.Data = confirmData{Action: s.path("p", idText, "delete"), Cancel: s.path("p", idText), First: dt.First}
		s.render(w, http.StatusOK, "confirm.html", p)
		return
	}
	if s.deleteFailed(w, r, p, board.DeletePost(ctx, s.pool, p.Viewer, id, s.opt.Now())) {
		return
	}
	// The delete is done. Whatever happens next, the answer is a 303.
	http.Redirect(w, r, s.afterDeleteTarget(ctx, p.Viewer, dt), http.StatusSeeOther)
}

// afterDeleteTarget is where a viewer goes after a delete that committed: the
// topic; when the topic is gone for the viewer (the first post held it) or cannot
// be read, the forum; when the forum cannot be read either, the index. It never
// fails: the delete is done.
func (s *site) afterDeleteTarget(ctx context.Context, v board.Viewer, dt board.DeleteForm) string {
	_, _, _, err := board.TopicHead(ctx, s.pool, v, dt.TopicID)
	if err == nil {
		return s.path("t", strconv.FormatInt(dt.TopicID, 10))
	}
	if !errors.Is(err, board.ErrNotFound) {
		slog.Warn("delete: locate the topic", "topic", dt.TopicID, "err", err)
	}
	if dt.ForumID != 0 {
		if _, _, err := board.ForumHead(ctx, s.pool, v, dt.ForumID); err == nil {
			return s.path("f", strconv.FormatInt(dt.ForumID, 10))
		} else if !errors.Is(err, board.ErrNotFound) {
			slog.Warn("delete: locate the forum", "forum", dt.ForumID, "err", err)
		}
	}
	return s.path()
}

// deleteFailed answers the error of a delete step and reports whether it did.
func (s *site) deleteFailed(w http.ResponseWriter, r *http.Request, p *page, err error) bool {
	rights, hard := splitErr(err)
	switch {
	case hard != nil:
		s.serverError(w, r, hard)
	case rights != nil:
		s.refuse(w, r, p, rights, "delete this post")
	default:
		return false
	}
	return true
}

// postItemData is the data of the "post" template: a post as the topic page and
// the preview show it.
type postItemData struct {
	postView
	// Topic is the title of the topic. TopicID is its ID, 0 for a preview.
	Topic   string
	TopicID int64
	CSRF    string
}

// postItem builds the data of the "post" template.
func postItem(pv postView, p *page, topic string, topicID int64) postItemData {
	return postItemData{postView: pv, Topic: topic, TopicID: topicID, CSRF: p.CSRF}
}
