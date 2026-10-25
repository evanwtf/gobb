package web

import (
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/ratelimit"
	"github.com/evanwtf/gobb/markup"
)

// The message pages. Every right is checked in internal/board: a page calls the
// same function as the write, and the board gives ErrNotFound to a member who is
// not a party to a message, before any right is read. A button that the page
// shows is never the check.
//
// GET and HEAD change nothing. The one exception is the read mark: a person who
// opens a message marks it read, as opening a topic marks it read. Only a
// top-level navigation does that (see [isNavigation]); an image load or a
// prefetch of the same address reads the message and writes nothing.

// The limit on sending. A send costs a parse of up to 60,000 bytes and fills the
// inboxes of other members, so each member gets a count of sends in a sliding
// window. A refused send is given back, since it stored nothing. A preview or a
// send also counts against the posting limit. The count lives in memory and
// starts over with the server.
const (
	messageSendWindow   = 10 * time.Minute
	defaultMessageSends = 10
	messageLimitMaxKeys = 10_000
)

// The texts of the message pages.
const (
	msgMessagesTooFast   = "You are sending messages too fast. Wait a few minutes and try again."
	noticeMessageFailed  = "Something went wrong, and your message was not sent. Your text is below. Try again in a moment."
	noticeMessageBanned  = "You cannot send messages because you are banned. Your text is below."
	noticeMessageNoRight = "You do not have the right to send messages. Your text is below."
	noticeMessageRefused = "This message cannot be sent. Your text is below."
)

// newMessageSends returns the limiter of the sends.
func newMessageSends(opt Options) *ratelimit.Limiter {
	n := defaultMessageSends
	if opt.MessageSends > 0 {
		n = opt.MessageSends
	}
	return ratelimit.New(n, messageSendWindow, messageLimitMaxKeys)
}

// messagesData is the data of messages.html.
type messagesData struct {
	// Inbox is true for the inbox and false for the sent list.
	Inbox bool
	List  board.MessageList
	Pager *pager
	// UnreadText says how many messages wait in the inbox, or is empty.
	UnreadText                   string
	InboxHref, SentHref, NewHref string
	CanSend                      bool
}

// messageData is the data of message.html.
type messageData struct {
	Msg board.Message
	// Card is the card of the author, or nil when the author is gone.
	Card          *board.UserCard
	HTML, SigHTML template.HTML
	Crumbs        []crumb
	// CanReply shows Reply and Quote. CanDelete shows Delete.
	CanReply, CanDelete              bool
	ReplyHref, QuoteHref, DeleteHref string
}

// composeErrors are the messages beside the fields of the compose form.
type composeErrors struct{ To, Subject, Body string }

// composeData is the data of message-new.html.
type composeData struct {
	Action, Cancel string
	Crumbs         []crumb
	// To, Subject and Source hold what the member sent, byte for byte.
	To, Subject, Source string
	Err                 composeErrors
	// Notice is a message for the whole form.
	Notice  string
	Preview *postView
}

// messagesMember starts a message page. A guest is sent to sign in and comes back
// (GET), or gets 403 (POST). It reports false when it has answered.
func (s *site) messagesMember(w http.ResponseWriter, r *http.Request, title string) (*page, bool) {
	p, err := s.begin(w, r, title, "messages")
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	if p.Viewer.UserID != 0 {
		return p, true
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		// The redirect is not a page, so the robots meta element has no place on
		// it; the header says the same.
		w.Header().Set("X-Robots-Tag", "noindex")
		http.Redirect(w, r, p.LoginHref, http.StatusFound)
		return nil, false
	}
	s.deniedTo(w, p, "use messages")
	return nil, false
}

// messageFailed answers a rights failure of a message page and reports whether it
// did: 404 for a message that the viewer is no party to, 403 for a right that the
// viewer lacks, 500 for anything else.
func (s *site) messageFailed(w http.ResponseWriter, r *http.Request, p *page, err error, action string) bool {
	return s.actionFailed(w, r, p, err, action)
}

// messageList serves GET /messages and GET /messages/sent.
func (s *site) messageList(inbox bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		title := "Messages: Sent"
		if inbox {
			title = "Messages: Inbox"
		}
		p, ok := s.messagesMember(w, r, title)
		if !ok {
			return
		}
		n, err := board.ParsePage(r.URL.Query().Get("page"))
		if err != nil {
			s.badPage(w, r)
			return
		}
		ctx := r.Context()
		var list board.MessageList
		if inbox {
			list, err = board.Inbox(ctx, s.pool, p.Viewer, n)
		} else {
			list, err = board.Sent(ctx, s.pool, p.Viewer, n)
		}
		if s.messageFailed(w, r, p, err, "read messages") {
			return
		}
		d := messagesData{Inbox: inbox, List: list, CanSend: p.Viewer.Perms.Has("pm.send", 0),
			InboxHref: s.path("messages"), SentHref: s.path("messages", "sent"), NewHref: s.path("messages", "new")}
		base := d.InboxHref
		if !inbox {
			base = d.SentHref
		}
		d.Pager = newPager(base, list.Page, list.Pages)
		if inbox {
			switch unread, err := board.UnreadMessages(ctx, s.pool, p.Viewer); {
			case err != nil:
				slog.Warn("cannot count the unread messages", "err", err)
			case unread > 0:
				d.UnreadText = num(unread) + " unread."
			}
		}
		p.Data = d
		s.render(w, http.StatusOK, "messages.html", p)
	}
}

// messageShow serves GET /messages/{id}.
func (s *site) messageShow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, ok := s.messagesMember(w, r, "")
	if !ok {
		return
	}
	ctx := r.Context()
	m, err := board.PeekMessage(ctx, s.pool, p.Viewer, id)
	if s.messageFailed(w, r, p, err, "read messages") {
		return
	}
	rc, err := s.renderContext(ctx, p.Viewer, 0)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	idText := strconv.FormatInt(id, 10)
	d := messageData{Msg: m, CanReply: p.Viewer.Perms.Has("pm.send", 0), CanDelete: p.Viewer.Perms.Has("pm.delete", 0),
		ReplyHref: s.path("messages", idText, "reply"), QuoteHref: s.path("messages", idText, "reply") + "?quote=1",
		DeleteHref: s.path("messages", idText, "delete")}
	folder, href := "Inbox", s.path("messages")
	if m.Mine {
		folder, href = "Sent", s.path("messages", "sent")
	}
	d.Crumbs = []crumb{{Label: "Messages", Href: s.path("messages")}, {Label: folder, Href: href}}
	body := markup.Render(m.Tree, rc)
	p.addRendered(body)
	d.HTML = body.HTML
	if m.Signature != nil {
		// A signature never embeds: an embed link is a plain link.
		sigRC := rc
		sigRC.NoEmbeds = true
		sig := markup.Render(m.Signature, sigRC)
		p.addRendered(sig)
		d.SigHTML = sig.HTML
	}
	if m.From.ID != 0 {
		card, err := board.Card(ctx, s.pool, m.From.ID)
		switch {
		case err == nil:
			d.Card = &card
		case !errors.Is(err, board.ErrNotFound):
			s.serverError(w, r, err)
			return
		}
	}
	p.Title = m.Subject
	p.Data = d
	// Only a person who opens the page marks the message read, and only once the
	// page is built. The nav count comes after the mark.
	var after func()
	if isNavigation(r) {
		after = func() {
			if err := board.MarkMessageRead(ctx, s.pool, p.Viewer, id); err != nil {
				slog.Warn("cannot mark a message read", "message", id, "err", err)
			}
		}
	}
	s.renderThen(w, http.StatusOK, "message.html", p, after)
}

// composeFrame returns the compose form with the action and the cancel link.
func (s *site) composeFrame(cancel string) composeData {
	return composeData{
		Action: s.path("messages", "new"), Cancel: cancel,
		Crumbs: []crumb{{Label: "Messages", Href: s.path("messages")}},
	}
}

// renderCompose writes the compose form with status.
func (s *site) renderCompose(w http.ResponseWriter, status int, p *page, d composeData) {
	p.Title = "New message"
	p.Return = s.path("messages")
	p.Data = d
	s.render(w, status, "message-new.html", p)
}

// messageNew serves GET /messages/new: the form, with To filled from ?to=.
func (s *site) messageNew(w http.ResponseWriter, r *http.Request) {
	p, ok := s.messagesMember(w, r, "New message")
	if !ok {
		return
	}
	if !p.Viewer.Perms.Has("pm.send", 0) {
		s.deniedTo(w, p, "send messages")
		return
	}
	d := s.composeFrame(s.path("messages"))
	d.To = r.URL.Query().Get("to")
	s.renderCompose(w, http.StatusOK, p, d)
}

// messageReply serves GET /messages/{id}/reply, and with ?quote=1 the form with
// the text of the message quoted.
func (s *site) messageReply(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, ok := s.messagesMember(w, r, "New message")
	if !ok {
		return
	}
	draft, err := board.ReplyDraft(r.Context(), s.pool, p.Viewer, id, r.URL.Query().Get("quote") == "1")
	if s.messageFailed(w, r, p, err, "send messages") {
		return
	}
	d := s.composeFrame(s.path("messages", strconv.FormatInt(id, 10)))
	d.To, d.Subject, d.Source = draft.To, draft.Subject, draft.Source
	s.renderCompose(w, http.StatusOK, p, d)
}

// messageSubmit serves POST /messages/new: a preview, or the send. Whatever
// happens after the csrf check, the text of the member comes back.
func (s *site) messageSubmit(w http.ResponseWriter, r *http.Request) {
	form := r.PostForm
	draft := board.MessageDraft{To: form.Get("to"), Subject: form.Get("subject"), Source: form.Get("body"), IP: s.clientIP(r)}
	p, err := s.begin(w, r, "New message", "messages")
	if err != nil {
		slog.Error("messages: page", "path", r.URL.Path, "err", err)
		s.bareForm(w, r, nil, failStatus(r, err), noticeMessageFailed, draft.Source)
		return
	}
	if p.Viewer.UserID == 0 {
		s.deniedTo(w, p, "send messages")
		return
	}
	d := s.composeFrame(s.path("messages"))
	d.To, d.Subject, d.Source = draft.To, draft.Subject, draft.Source
	// The POST counts against the posting limit before anything is parsed. A
	// refusal still counts: it costs the server the same.
	now := s.opt.Now()
	if ok, wait := s.takePost(p.Viewer, r); !ok {
		s.messageTooFast(w, p, d, wait, msgPostingTooFast)
		return
	}
	if !p.Viewer.Perms.Has("pm.send", 0) {
		d.Notice = noticeMessageNoRight
		s.renderCompose(w, http.StatusForbidden, p, d)
		return
	}
	if form.Get("preview") != "" {
		s.messagePreview(w, r, p, d)
		return
	}
	key := strconv.FormatInt(p.Viewer.UserID, 10)
	if ok, wait := s.messageSends.Take(key, now); !ok {
		s.messageTooFast(w, p, d, wait, msgMessagesTooFast)
		return
	}
	id, err := board.SendMessage(r.Context(), s.pool, p.Viewer, draft, now)
	if err != nil {
		var in *board.InputError
		if errors.As(err, &in) || errors.Is(err, board.ErrForbidden) {
			// Nothing was stored, so the send does not count.
			s.messageSends.Refund(key, now)
		}
		s.messageSendFailed(w, r, p, d, err)
		return
	}
	http.Redirect(w, r, s.path("messages", strconv.FormatInt(id, 10)), http.StatusSeeOther)
}

// messageTooFast answers 429 with the form and the text of the member.
func (s *site) messageTooFast(w http.ResponseWriter, p *page, d composeData, wait time.Duration, msg string) {
	w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(wait)))
	d.Notice = msg
	s.renderCompose(w, http.StatusTooManyRequests, p, d)
}

// messageSendFailed answers the failure of a preview or a send with the form:
//   - an [*board.InputError]: 400 and the message beside its field;
//   - [board.ErrForbidden]: 403 and a notice;
//   - any other error: 500 (503 for a timeout) and a fixed notice that carries no
//     detail.
func (s *site) messageSendFailed(w http.ResponseWriter, r *http.Request, p *page, d composeData, err error) {
	var in *board.InputError
	switch {
	case errors.As(err, &in):
		switch in.Field {
		case "to":
			d.Err.To = in.Message
		case "subject":
			d.Err.Subject = in.Message
		default:
			d.Err.Body = in.Message
		}
		s.renderCompose(w, http.StatusBadRequest, p, d)
	case errors.Is(err, board.ErrBanned):
		d.Notice = noticeMessageBanned
		s.renderCompose(w, http.StatusForbidden, p, d)
	case errors.Is(err, board.ErrForbidden):
		d.Notice = noticeMessageRefused
		s.renderCompose(w, http.StatusForbidden, p, d)
	default:
		slog.Error("message failed", "path", r.URL.Path, "err", err)
		d.Notice = noticeMessageFailed
		s.renderCompose(w, failStatus(r, err), p, d)
	}
}

// messagePreview answers 200 with the form and the rendered message above it. It
// saves nothing. It parses the body as the send does ([board.ParseMessage]), so a
// preview never disagrees with the send.
func (s *site) messagePreview(w http.ResponseWriter, r *http.Request, p *page, d composeData) {
	ctx := r.Context()
	body, err := board.ParseMessage(ctx, s.pool, p.Viewer, d.Source)
	if err != nil {
		s.messageSendFailed(w, r, p, d, err)
		return
	}
	rc, err := s.renderContext(ctx, p.Viewer, 0)
	if err != nil {
		s.messageSendFailed(w, r, p, d, err)
		return
	}
	rendered := markup.Render(body.Tree, rc)
	p.addRendered(rendered)
	card, err := board.Card(ctx, s.pool, p.Viewer.UserID)
	if err != nil {
		s.messageSendFailed(w, r, p, d, err)
		return
	}
	pv := postView{HTML: rendered.HTML, Clock: p.Clock, Preview: true}
	pv.Post = board.Post{At: s.opt.Now(), Visibility: "approved", Author: board.Author{ID: p.Viewer.UserID, Name: p.Viewer.Name}, Card: &card}
	d.Preview = &pv
	s.renderCompose(w, http.StatusOK, p, d)
}

// messageDelete serves POST /messages/{id}/delete. A POST without confirm=1
// changes nothing: it answers with a page that asks "Delete this message?" and
// holds the second form. A POST with confirm=1 deletes the message from the view
// of the member. The party check comes before the check of the right, so a
// message of others answers 404.
func (s *site) messageDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, ok := s.messagesMember(w, r, "")
	if !ok {
		return
	}
	ctx := r.Context()
	m, err := board.PeekMessage(ctx, s.pool, p.Viewer, id)
	if s.messageFailed(w, r, p, err, "delete this message") {
		return
	}
	if !p.Viewer.Perms.Has("pm.delete", 0) {
		s.deniedTo(w, p, "delete this message")
		return
	}
	idText := strconv.FormatInt(id, 10)
	if r.PostForm.Get("confirm") != "1" {
		p.Title = "Delete this message?"
		p.Data = confirmData{Action: s.path("messages", idText, "delete"), Cancel: s.path("messages", idText), Message: true}
		s.render(w, http.StatusOK, "confirm.html", p)
		return
	}
	if s.messageFailed(w, r, p, board.DeleteMessage(ctx, s.pool, p.Viewer, id), "delete this message") {
		return
	}
	target := s.path("messages")
	if m.Mine {
		target = s.path("messages", "sent")
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// messageHref is the link to the compose form with the To field filled.
func (s *site) messageHref(name string) string {
	return s.path("messages", "new") + "?" + url.Values{"to": {name}}.Encode()
}

// addMessagesLink adds "Messages" to the nav of a member who may read messages,
// with the count of unread ones as a note. A failure to count is logged and the
// link shows no note.
func (s *site) addMessagesLink(p *page) {
	if p.Viewer.UserID == 0 || !p.Viewer.Perms.Has("pm.read", 0) {
		return
	}
	link := navLink{Label: "Messages", Href: s.path("messages"), Current: p.navKey == "messages"}
	switch n, err := board.UnreadMessages(p.ctx, s.pool, p.Viewer); {
	case err != nil:
		slog.Warn("cannot count the unread messages", "err", err)
	case n > 0:
		link.Badge = num(n) + " new"
	}
	p.Nav = append(p.Nav, link)
}
