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
// navigation that the member started does that (see [isOwnNavigation]); an image
// load or a prefetch of the same address reads the message and writes nothing.

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
	msgOneAtATime        = "You can send a message to one member at a time."
)

// newMessageSends returns the limiter of the sends.
func newMessageSends(opt Options) *ratelimit.Limiter {
	n := defaultMessageSends
	if opt.MessageSends > 0 {
		n = opt.MessageSends
	}
	return ratelimit.New(n, messageSendWindow, messageLimitMaxKeys).Named("message_sends")
}

// messagesData is the data of messages.html.
type messagesData struct {
	// Inbox is true for the list of conversations and false for the sent list.
	Inbox bool
	// Talks is the list of conversations; List is the sent list.
	Talks                        board.ConversationList
	List                         board.MessageList
	Pager                        *pager
	InboxHref, SentHref, NewHref string
	CanSend                      bool
}

// messageView is one message of a conversation page.
type messageView struct {
	Msg board.Message
	// Card is the card of the author, or nil when the author is gone.
	Card                  *board.UserCard
	HTML, SigHTML         template.HTML
	QuoteHref, DeleteHref string
}

// messageData is the data of message.html: one page of a conversation.
type messageData struct {
	Subject  string
	Messages []messageView
	Pager    *pager
	Crumbs   []crumb
	// CanReply shows Quote and the reply form. CanDelete shows Delete.
	CanReply, CanDelete bool
	// The reply form: it answers the last message the viewer sees.
	ReplyAction, ReplyTo, ReplyToNames, ReplySubject, ReplyHref string
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
	// ReplyTo is the ID of the message that this one answers, or empty.
	ReplyTo string
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

// messageList serves GET /messages, the conversations, and GET /messages/sent.
func (s *site) messageList(inbox bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		title := "Messages: Sent"
		if inbox {
			title = "Messages"
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
		d := messagesData{Inbox: inbox, CanSend: p.Viewer.Perms.Has("pm.send", 0),
			InboxHref: s.path("messages"), SentHref: s.path("messages", "sent"), NewHref: s.path("messages", "new")}
		if inbox {
			d.Talks, err = board.Conversations(ctx, s.pool, p.Viewer, n)
		} else {
			d.List, err = board.Sent(ctx, s.pool, p.Viewer, n)
		}
		if s.actionFailed(w, r, p, err, "read messages") {
			return
		}
		if inbox {
			d.Pager = newPager(d.InboxHref, d.Talks.Page, d.Talks.Pages)
		} else {
			d.Pager = newPager(d.SentHref, d.List.Page, d.List.Pages)
		}
		p.Data = d
		s.render(w, http.StatusOK, "messages.html", p)
	}
}

// messageShow serves GET /messages/{id}: the conversation of the message, at
// the page that holds it, or at ?page=.
func (s *site) messageShow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	page := 0
	if q := r.URL.Query().Get("page"); q != "" {
		n, err := board.ParsePage(q)
		if err != nil {
			s.badPage(w, r)
			return
		}
		page = n
	}
	p, ok := s.messagesMember(w, r, "")
	if !ok {
		return
	}
	ctx := r.Context()
	c, err := board.ReadConversation(ctx, s.pool, p.Viewer, id, page)
	if s.actionFailed(w, r, p, err, "read messages") {
		return
	}
	rc, err := s.renderContext(ctx, p.Viewer, 0)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// A message and a signature never embed: an embed is a plain link, with no
	// frame and no provider script on the most private page (ruling R43).
	rc.NoEmbeds = true
	d := messageData{Subject: c.Subject, CanReply: p.Viewer.Perms.Has("pm.send", 0), CanDelete: p.Viewer.Perms.Has("pm.delete", 0),
		Crumbs: []crumb{{Label: "Messages", Href: s.path("messages")}}}
	// The pages hang off the first message: page 1 of /messages/{id} is the page
	// that holds id, and for the first message that is page 1.
	d.Pager = newPager(s.path("messages", strconv.FormatInt(c.FirstID, 10)), c.Page, c.Pages)
	cards := map[int64]*board.UserCard{}
	for _, m := range c.Messages {
		mText := strconv.FormatInt(m.ID, 10)
		mv := messageView{Msg: m, QuoteHref: s.path("messages", mText, "reply") + "?quote=1",
			DeleteHref: s.path("messages", mText, "delete")}
		body := markup.Render(m.Tree, rc)
		p.addRendered(body)
		mv.HTML = body.HTML
		if m.Signature != nil {
			sig := markup.Render(m.Signature, rc)
			p.addRendered(sig)
			mv.SigHTML = sig.HTML
		}
		if m.From.ID != 0 {
			card, seen := cards[m.From.ID]
			if !seen {
				got, err := board.Card(ctx, s.pool, m.From.ID)
				switch {
				case err == nil:
					card = &got
				case !errors.Is(err, board.ErrNotFound):
					s.serverError(w, r, err)
					return
				}
				cards[m.From.ID] = card
			}
			mv.Card = card
		}
		d.Messages = append(d.Messages, mv)
	}
	if d.CanReply {
		draft, err := board.ReplyDraft(ctx, s.pool, p.Viewer, c.LastID, false)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		last := strconv.FormatInt(c.LastID, 10)
		d.ReplyAction, d.ReplyTo, d.ReplyToNames, d.ReplySubject = s.path("messages", "new"), last, draft.To, draft.Subject
		d.ReplyHref = s.path("messages", last, "reply")
	}
	p.Title = c.Subject
	p.Data = d
	// Only a person who opens the page marks the messages on it read, and only
	// once the page is built. The nav count comes after the mark.
	var after func()
	if isOwnNavigation(r) && len(c.Unread) > 0 {
		after = func() {
			if err := board.MarkMessagesRead(ctx, s.pool, p.Viewer, c.Unread); err != nil {
				slog.Warn("cannot mark messages read", "conversation", c.ID, "err", err)
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
	if s.actionFailed(w, r, p, err, "send messages") {
		return
	}
	idText := strconv.FormatInt(id, 10)
	d := s.composeFrame(s.path("messages", idText))
	d.To, d.Subject, d.Source, d.ReplyTo = draft.To, draft.Subject, draft.Source, idText
	s.renderCompose(w, http.StatusOK, p, d)
}

// messageSubmit serves POST /messages/new: a preview, or the send. Whatever
// happens after the csrf check, the text of the member comes back.
func (s *site) messageSubmit(w http.ResponseWriter, r *http.Request) {
	form := r.PostForm
	draft := board.MessageDraft{To: form.Get("to"), Subject: form.Get("subject"), Source: form.Get("body"), IP: s.clientIP(r)}
	// A reply_to that is not an ID is dropped: the message starts its own
	// conversation, as a reply to a message of someone else does.
	if n, err := strconv.ParseInt(form.Get("reply_to"), 10, 64); err == nil && n > 0 {
		draft.ReplyTo = n
	}
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
	if draft.ReplyTo != 0 {
		d.ReplyTo = strconv.FormatInt(draft.ReplyTo, 10)
		d.Cancel = s.path("messages", d.ReplyTo)
	}
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
	idText := strconv.FormatInt(id, 10)
	http.Redirect(w, r, s.path("messages", idText)+"#m"+idText, http.StatusSeeOther)
}

// messageTooFast answers 429 with the form and the text of the member.
func (s *site) messageTooFast(w http.ResponseWriter, p *page, d composeData, wait time.Duration, msg string) {
	w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(wait)))
	d.Notice = msg
	s.renderCompose(w, http.StatusTooManyRequests, p, d)
}

// messageSendFailed answers the failure of a preview or a send with the form:
//   - an [*board.InputError]: 400 and the message beside its field;
//   - [board.ErrSendMany]: 400 and a message beside the To field;
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
	case errors.Is(err, board.ErrSendMany):
		d.Err.To = msgOneAtATime
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
	// The preview shows what the message page shows: an embed is a plain link
	// (ruling R43).
	rc.NoEmbeds = true
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
	_, err := board.PeekMessage(ctx, s.pool, p.Viewer, id)
	if s.actionFailed(w, r, p, err, "delete this message") {
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
	if s.actionFailed(w, r, p, board.DeleteMessage(ctx, s.pool, p.Viewer, id), "delete this message") {
		return
	}
	http.Redirect(w, r, s.path("messages"), http.StatusSeeOther)
}

// messageHref is the link to the compose form with the To field filled.
func (s *site) messageHref(name string) string {
	return s.path("messages", "new") + "?" + url.Values{"to": {name}}.Encode()
}

// addMessagesLink adds "Messages" to the nav of a member who may read messages,
// with the count of unread ones as a note. It keeps the count in
// [page.UnreadMessages], so the inbox page reuses it and counts once. A failure to
// count is logged and the link shows no note.
func (s *site) addMessagesLink(p *page) {
	if p.Viewer.UserID == 0 || !p.Viewer.Perms.Has("pm.read", 0) {
		return
	}
	link := navLink{Label: "Messages", Href: s.path("messages"), Current: p.navKey == "messages"}
	switch n, err := board.UnreadMessages(p.ctx, s.pool, p.Viewer); {
	case err != nil:
		slog.Warn("cannot count the unread messages", "user", p.Viewer.UserID, "err", err)
	case n > 0:
		link.Badge = num(n) + " new"
		p.UnreadMessages = n
	}
	p.Nav = append(p.Nav, link)
}
