package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
)

// The moderator actions on the topic page. Each is one POST through site.protect
// and answers 303 to a page. A button on the topic page is only a hint: the board
// write checks the right again, and answers [board.ErrForbidden] or
// [board.ErrNotFound] for a viewer who may not. Delete is in post.go, with its
// confirm step.

// postWrite is a board write on one post.
type postWrite func(ctx context.Context, pool *pgxpool.Pool, v board.Viewer, postID int64, now time.Time) error

// postAction serves POST /p/{id}/restore and POST /p/{id}/approve. verb names the
// action in a refusal ("restore this post"). A step from the approval queue
// (from=queue) goes back to the queue.
func (s *site) postAction(verb string, write postWrite) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		// A post that the viewer cannot see is a missing one, whatever the action.
		// Its topic and forum are where the answer goes if the post is hidden
		// afterwards.
		topicID, forumID, err := board.PostPlace(ctx, s.pool, p.Viewer, id)
		if s.actionFailed(w, r, p, err, verb) {
			return
		}
		scope := s.postScopeOf(ctx, id)
		err = write(ctx, s.pool, p.Viewer, id, s.opt.Now())
		s.afterWrite(err, func() { s.changedScope(scope) })
		if s.actionFailed(w, r, p, err, verb) {
			return
		}
		// The write is done. Whatever happens next, the answer is a 303: to the post
		// when the viewer still sees it. A restore can put a post back as held, for a
		// restorer who cannot see held posts: the answer then falls back as the
		// answer to a delete does, to the topic, the forum, the index.
		if fromQueue(r) {
			http.Redirect(w, r, s.path("queue"), http.StatusSeeOther)
			return
		}
		target := s.fallbackTarget(ctx, p.Viewer, topicID, forumID)
		switch _, _, err := board.PostLocation(ctx, s.pool, p.Viewer, id); {
		case err == nil:
			target = s.path("p", strconv.FormatInt(id, 10))
		case !errors.Is(err, board.ErrNotFound):
			slog.Warn("moderation: locate the post", "post", id, "err", err)
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
}

// topicLock serves POST /t/{id}/lock and POST /t/{id}/unlock.
func (s *site) topicLock(locked bool) http.HandlerFunc {
	verb := "unlock this topic"
	if locked {
		verb = "lock this topic"
	}
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
		err = board.SetTopicLock(r.Context(), s.pool, p.Viewer, id, locked, s.opt.Now())
		s.afterWrite(err, func() { s.changedTopic(id) })
		if s.actionFailed(w, r, p, err, verb) {
			return
		}
		http.Redirect(w, r, s.path("t", strconv.FormatInt(id, 10)), http.StatusSeeOther)
	}
}

// kindLabels are the words that the "Topic type" select shows for each type.
var kindLabels = map[string]string{
	"normal":   "Normal",
	"sticky":   "Sticky",
	"announce": "Announcement",
	"global":   "Global",
}

// kindOption is one option of a "Topic type" select.
type kindOption struct {
	Value, Label string
	Selected     bool
}

// kindOptions builds the options of a "Topic type" select from the types that the
// board allows, with selected marked. It is nil when kinds is empty.
func kindOptions(kinds []string, selected string) []kindOption {
	var out []kindOption
	for _, k := range kinds {
		out = append(out, kindOption{Value: k, Label: kindLabels[k], Selected: k == selected})
	}
	return out
}

// topicKind serves POST /t/{id}/type: it sets the type of the topic to the
// value of the field "kind", and answers 303 to the topic. The board checks the
// topic and the rights first: a missing or hidden topic answers 404, and a
// viewer who may not change the type answers 403. Then a value that is not a
// type answers 400. A refusal changes nothing.
func (s *site) topicKind(w http.ResponseWriter, r *http.Request) {
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
	err = board.SetTopicKind(r.Context(), s.pool, p.Viewer, id, r.PostForm.Get("kind"), s.opt.Now())
	s.afterWrite(err, func() { s.changedTopic(id) })
	if errors.Is(err, board.ErrBadKind) {
		s.fail(w, r, http.StatusBadRequest, "Bad request", "The topic type is not valid.")
		return
	}
	if s.actionFailed(w, r, p, err, "change the type of this topic") {
		return
	}
	http.Redirect(w, r, s.path("t", strconv.FormatInt(id, 10)), http.StatusSeeOther)
}

// actionFailed answers the error of a moderator step and reports whether it did:
// 404 for what the viewer may not see, 403 for what the viewer may not do, 500
// (503 for a timeout) for a failure of the database.
func (s *site) actionFailed(w http.ResponseWriter, r *http.Request, p *page, err error, verb string) bool {
	rights, hard := splitErr(err)
	switch {
	case hard != nil:
		s.serverError(w, r, hard)
	case rights != nil:
		s.refuse(w, r, p, rights, verb)
	default:
		return false
	}
	return true
}
