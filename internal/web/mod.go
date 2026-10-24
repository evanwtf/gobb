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
// action in a refusal ("restore this post").
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
		// The topic is also where the answer goes if the post is hidden afterwards.
		topicID, _, err := board.PostLocation(ctx, s.pool, p.Viewer, id)
		if s.actionFailed(w, r, p, err, verb) {
			return
		}
		if s.actionFailed(w, r, p, write(ctx, s.pool, p.Viewer, id, s.opt.Now()), verb) {
			return
		}
		// The write is done. Whatever happens next, the answer is a 303: to the post,
		// or to its topic when the post is not visible to the viewer any more (a
		// restore that puts a post back as held, for a viewer who cannot see held
		// posts).
		target := s.path("t", strconv.FormatInt(topicID, 10))
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
		if s.actionFailed(w, r, p, board.SetTopicLock(r.Context(), s.pool, p.Viewer, id, locked, s.opt.Now()), verb) {
			return
		}
		http.Redirect(w, r, s.path("t", strconv.FormatInt(id, 10)), http.StatusSeeOther)
	}
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
