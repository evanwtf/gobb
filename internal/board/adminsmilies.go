package board

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/markup"
)

// The smiley editor of the admin area. The parser and the renderer read the
// smilies from the database on each use, so a smiley added here is in the next
// post that is parsed, and a smiley removed here shows as its code in every
// post at the next render. A stored post keeps the code, not the image.

// AdminSmiley is one smiley as the editor shows it. Image is a path under the
// data directory, such as "smilies/smile.gif".
type AdminSmiley struct {
	ID          int64
	Code        string
	Image       string
	Description string
	Position    int
}

// SmileyInput is what the smiley editor sends: the code and the description.
// The image is a file that the caller has checked and stored.
type SmileyInput struct {
	Code, Description string
}

// AdminSmilies returns the smilies in the order that the parser reads them:
// by position, then by ID.
func AdminSmilies(ctx context.Context, q db.Querier) ([]AdminSmiley, error) {
	rows, err := q.Query(ctx, `select id, code, image_path, description, position from smilies order by position, id`)
	if err != nil {
		return nil, fmt.Errorf("board: list smilies: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (s AdminSmiley, err error) {
		err = row.Scan(&s.ID, &s.Code, &s.Image, &s.Description, &s.Position)
		return s, err
	})
	if err != nil {
		return nil, fmt.Errorf("board: list smilies: %w", err)
	}
	return out, nil
}

// AdminSmileyByID returns smiley id, or [ErrNotFound].
func AdminSmileyByID(ctx context.Context, q db.Querier, id int64) (AdminSmiley, error) {
	var s AdminSmiley
	err := q.QueryRow(ctx, `select id, code, image_path, description, position from smilies where id = $1`, id).
		Scan(&s.ID, &s.Code, &s.Image, &s.Description, &s.Position)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdminSmiley{}, ErrNotFound
	}
	if err != nil {
		return AdminSmiley{}, fmt.Errorf("board: read smiley %d: %w", id, err)
	}
	return s, nil
}

// cleanSmiley trims and checks the code and the description. The code is valid
// as the code of a smiley element of a tree, which is the rule of a bundle: 1 to
// 32 characters, none a control character. The description is the alt text of the
// image: at most 255 characters, and it may be empty.
func cleanSmiley(in SmileyInput) (SmileyInput, error) {
	code := strings.TrimSpace(in.Code)
	switch {
	case code == "":
		return in, inputError("code", "Enter a code.")
	case !utf8.ValidString(code):
		return in, inputError("code", "The code is not valid text.")
	}
	tree := &markup.Node{Kind: markup.Root, Children: []*markup.Node{{
		Kind: markup.Element, Name: "p", Children: []*markup.Node{{
			Kind: markup.Element, Name: "smiley", Attrs: map[string]string{"code": code},
		}},
	}}}
	if markup.Validate(tree) != nil {
		return in, inputError("code", "Use 1 to 32 characters, with no control character.")
	}
	desc, err := cleanLabel(in.Description, "description", "description", maxLabelRunes, false)
	if err != nil {
		return in, err
	}
	return SmileyInput{Code: code, Description: desc}, nil
}

const msgCodeTaken = "Another smiley has this code."

// AdminCreateSmiley adds a smiley with the code and description of in and the
// image at image, a path under [SmileyDir] (see [ValidContentPath]). The smiley
// goes last in the list. A code that another smiley holds is an [*InputError] on
// "code". It returns the ID of the new smiley. The log row is smiley.create.
func AdminCreateSmiley(ctx context.Context, pool *pgxpool.Pool, v Viewer, in SmileyInput, image string, now time.Time) (int64, error) {
	var id int64
	err := AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		in, err := cleanSmiley(in)
		if err != nil {
			return Change{}, err
		}
		if err := checkImagePath(SmileyDir, image); err != nil {
			return Change{}, err
		}
		err = tx.QueryRow(ctx, `
insert into smilies (code, image_path, description, position)
values ($1, $2, $3, (select coalesce(max(position), 0) + 1 from smilies))
on conflict (code) do nothing returning id`, in.Code, image, in.Description).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return Change{}, inputError("code", msgCodeTaken)
		}
		if err != nil {
			return Change{}, fmt.Errorf("board: add smiley: %w", err)
		}
		return Change{Action: "smiley.create", TargetType: "smiley", TargetID: id,
			Details: map[string]any{"code": in.Code, "image": image}}, nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// AdminUpdateSmiley sets the code and the description of smiley id to those of
// in. A newImage that is not empty replaces the image; it is a path under
// [SmileyDir]. A change to the code does not reach the posts that use the old
// code: they show it as text. A code that another smiley holds is an
// [*InputError] on "code". A save that changes nothing is [ErrNoChange], and a
// missing smiley is [ErrNotFound]. It returns the path of the replaced image
// when no smiley and no rank names it any more, for the caller to delete after
// the commit. The log row is smiley.update with the names of the changed fields.
func AdminUpdateSmiley(ctx context.Context, pool *pgxpool.Pool, v Viewer, id int64, in SmileyInput, newImage string, now time.Time) (freed string, err error) {
	err = AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		in, err := cleanSmiley(in)
		if err != nil {
			return Change{}, err
		}
		if newImage != "" {
			if err := checkImagePath(SmileyDir, newImage); err != nil {
				return Change{}, err
			}
		}
		var old AdminSmiley
		err = tx.QueryRow(ctx, `select code, image_path, description from smilies where id = $1 for update`, id).
			Scan(&old.Code, &old.Image, &old.Description)
		if errors.Is(err, pgx.ErrNoRows) {
			return Change{}, ErrNotFound
		}
		if err != nil {
			return Change{}, fmt.Errorf("board: read smiley %d: %w", id, err)
		}
		fields := changed([]string{"code", "description", "image"},
			old.Code != in.Code, old.Description != in.Description, newImage != "")
		if len(fields) == 0 {
			return Change{}, ErrNoChange
		}
		image := old.Image
		if newImage != "" {
			image = newImage
		}
		if _, err := tx.Exec(ctx, `update smilies set code = $2, image_path = $3, description = $4 where id = $1`,
			id, in.Code, image, in.Description); err != nil {
			if isUniqueViolation(err) {
				return Change{}, inputError("code", msgCodeTaken)
			}
			return Change{}, fmt.Errorf("board: update smiley %d: %w", id, err)
		}
		if newImage != "" {
			if freed, err = freeImage(ctx, tx, old.Image); err != nil {
				return Change{}, err
			}
		}
		return Change{Action: "smiley.update", TargetType: "smiley", TargetID: id,
			Details: map[string]any{"code": in.Code, "fields": fields}}, nil
	})
	if err != nil {
		return "", err
	}
	return freed, nil
}

// AdminMoveSmiley moves smiley id one place up or down the list. It numbers the
// positions of the whole list from 1 again, so equal positions from an import
// settle. A smiley that is first and moves up, or last and moves down, is
// [ErrNoChange]; a missing smiley is [ErrNotFound]. The log row is
// smiley.reorder.
func AdminMoveSmiley(ctx context.Context, pool *pgxpool.Pool, v Viewer, id int64, up bool, now time.Time) error {
	return AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		rows, err := tx.Query(ctx, `select id, code, position from smilies order by position, id for update`)
		if err != nil {
			return Change{}, fmt.Errorf("board: lock smilies: %w", err)
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (s AdminSmiley, err error) {
			err = row.Scan(&s.ID, &s.Code, &s.Position)
			return s, err
		})
		if err != nil {
			return Change{}, fmt.Errorf("board: lock smilies: %w", err)
		}
		at := -1
		for i, s := range list {
			if s.ID == id {
				at = i
			}
		}
		to := at + 1
		if up {
			to = at - 1
		}
		switch {
		case at < 0:
			return Change{}, ErrNotFound
		case to < 0 || to >= len(list):
			return Change{}, ErrNoChange
		}
		code := list[at].Code
		list[at], list[to] = list[to], list[at]
		for i, s := range list {
			if s.Position == i+1 {
				continue
			}
			if _, err := tx.Exec(ctx, `update smilies set position = $2 where id = $1`, s.ID, i+1); err != nil {
				return Change{}, fmt.Errorf("board: move smiley %d: %w", id, err)
			}
		}
		return Change{Action: "smiley.reorder", TargetType: "smiley", TargetID: id,
			Details: map[string]any{"code": code, "from": at + 1, "to": to + 1}}, nil
	})
}

// AdminDeleteSmiley deletes smiley id. A missing smiley is [ErrNotFound]. The
// posts that use its code show the code as text. It returns the path of its
// image when no other smiley and no rank names it, for the caller to delete
// after the commit. The log row is smiley.delete.
func AdminDeleteSmiley(ctx context.Context, pool *pgxpool.Pool, v Viewer, id int64, now time.Time) (freed string, err error) {
	err = AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		var s AdminSmiley
		err := tx.QueryRow(ctx, `delete from smilies where id = $1 returning code, image_path`, id).Scan(&s.Code, &s.Image)
		if errors.Is(err, pgx.ErrNoRows) {
			return Change{}, ErrNotFound
		}
		if err != nil {
			return Change{}, fmt.Errorf("board: delete smiley %d: %w", id, err)
		}
		if freed, err = freeImage(ctx, tx, s.Image); err != nil {
			return Change{}, err
		}
		return Change{Action: "smiley.delete", TargetType: "smiley", TargetID: id,
			Details: map[string]any{"code": s.Code, "image": s.Image}}, nil
	})
	if err != nil {
		return "", err
	}
	return freed, nil
}
