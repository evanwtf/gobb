package board

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/db"
)

// The rank editor of the admin area. A member gets the rank they hold, or else
// the highest ordinary rank their post count reaches (see cardJoins). A special
// rank is one that an admin gives to a member by hand.

// AdminRank is one rank as the editor shows it. Image is a path under the data
// directory, such as "ranks/newcomer.png", or empty.
type AdminRank struct {
	ID       int64
	Title    string
	MinPosts int
	Special  bool
	Image    string
	// Members is the count of members who hold the rank by hand: those whose
	// rank_id is this rank.
	Members int
}

// RankInput is what the rank editor sends: the title, the post-count threshold
// and the special flag. MinPosts is 64 bits wide so that a value over the limit
// of the database column is a fault the editor can name.
type RankInput struct {
	Title    string
	MinPosts int64
	Special  bool
}

const rankColumns = `r.id, r.title, r.min_posts, r.special, coalesce(r.image_path, ''),
       (select count(*) from users u where u.rank_id = r.id)`

func scanRank(row pgx.Row, r *AdminRank) error {
	return row.Scan(&r.ID, &r.Title, &r.MinPosts, &r.Special, &r.Image, &r.Members)
}

// AdminRanks returns the ranks: the ordinary ones by their threshold, then the
// special ones.
func AdminRanks(ctx context.Context, q db.Querier) ([]AdminRank, error) {
	rows, err := q.Query(ctx, `select `+rankColumns+` from ranks r order by r.special, r.min_posts, r.id`)
	if err != nil {
		return nil, fmt.Errorf("board: list ranks: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (r AdminRank, err error) {
		err = scanRank(row, &r)
		return r, err
	})
	if err != nil {
		return nil, fmt.Errorf("board: list ranks: %w", err)
	}
	return out, nil
}

// AdminRankByID returns rank id, or [ErrNotFound].
func AdminRankByID(ctx context.Context, q db.Querier, id int64) (AdminRank, error) {
	var r AdminRank
	err := scanRank(q.QueryRow(ctx, `select `+rankColumns+` from ranks r where r.id = $1`, id), &r)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdminRank{}, ErrNotFound
	}
	if err != nil {
		return AdminRank{}, fmt.Errorf("board: read rank %d: %w", id, err)
	}
	return r, nil
}

// cleanRank trims and checks the title (1 to 255 characters) and the threshold
// (0 to 2,147,483,647).
func cleanRank(in RankInput) (RankInput, error) {
	title, err := cleanLabel(in.Title, "title", "title", maxLabelRunes, true)
	if err != nil {
		return in, err
	}
	if in.MinPosts < 0 || in.MinPosts > maxCount {
		return in, inputError("min_posts", "Enter a whole number from 0 to 2,147,483,647.")
	}
	in.Title = title
	return in, nil
}

// AdminCreateRank adds a rank. image is a path under [RankDir], or empty for a
// rank with no image. It returns the ID of the new rank. The log row is
// rank.create.
func AdminCreateRank(ctx context.Context, pool *pgxpool.Pool, v Viewer, in RankInput, image string, now time.Time) (int64, error) {
	var id int64
	err := AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		in, err := cleanRank(in)
		if err != nil {
			return Change{}, err
		}
		if image != "" {
			if err := checkImagePath(RankDir, image); err != nil {
				return Change{}, err
			}
		}
		err = tx.QueryRow(ctx, `
insert into ranks (title, min_posts, special, image_path) values ($1, $2, $3, nullif($4, '')) returning id`,
			in.Title, in.MinPosts, in.Special, image).Scan(&id)
		if err != nil {
			return Change{}, fmt.Errorf("board: add rank: %w", err)
		}
		return Change{Action: "rank.create", TargetType: "rank", TargetID: id,
			Details: map[string]any{"title": in.Title, "min_posts": in.MinPosts, "special": in.Special, "image": image}}, nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// AdminUpdateRank sets the title, the threshold and the special flag of rank id
// to those of in. A newImage that is not empty replaces the image, a path under
// [RankDir]. With removeImage the rank has no image after the write; newImage
// wins when both are set. A save that changes nothing is [ErrNoChange], and a
// missing rank is [ErrNotFound]. It returns the path of the replaced or removed
// image when no smiley and no rank names it any more, for the caller to delete
// after the commit. The log row is rank.update with the names of the changed
// fields.
func AdminUpdateRank(ctx context.Context, pool *pgxpool.Pool, v Viewer, id int64, in RankInput, newImage string, removeImage bool, now time.Time) (freed string, err error) {
	err = AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		in, err := cleanRank(in)
		if err != nil {
			return Change{}, err
		}
		if newImage != "" {
			if err := checkImagePath(RankDir, newImage); err != nil {
				return Change{}, err
			}
		}
		var old AdminRank
		err = tx.QueryRow(ctx, `select title, min_posts, special, coalesce(image_path, '') from ranks where id = $1 for update`, id).
			Scan(&old.Title, &old.MinPosts, &old.Special, &old.Image)
		if errors.Is(err, pgx.ErrNoRows) {
			return Change{}, ErrNotFound
		}
		if err != nil {
			return Change{}, fmt.Errorf("board: read rank %d: %w", id, err)
		}
		image := old.Image
		switch {
		case newImage != "":
			image = newImage
		case removeImage:
			image = ""
		}
		fields := changed([]string{"title", "min_posts", "special", "image"},
			old.Title != in.Title, int64(old.MinPosts) != in.MinPosts, old.Special != in.Special, image != old.Image)
		if len(fields) == 0 {
			return Change{}, ErrNoChange
		}
		if _, err := tx.Exec(ctx, `update ranks set title = $2, min_posts = $3, special = $4, image_path = nullif($5, '') where id = $1`,
			id, in.Title, in.MinPosts, in.Special, image); err != nil {
			return Change{}, fmt.Errorf("board: update rank %d: %w", id, err)
		}
		if image != old.Image {
			if freed, err = freeImage(ctx, tx, old.Image); err != nil {
				return Change{}, err
			}
		}
		return Change{Action: "rank.update", TargetType: "rank", TargetID: id,
			Details: map[string]any{"title": in.Title, "fields": fields}}, nil
	})
	if err != nil {
		return "", err
	}
	return freed, nil
}

// AdminDeleteRank deletes rank id. Members who hold it by hand lose it: their
// rank_id becomes null, and they get the rank their post count reaches. A
// missing rank is [ErrNotFound]. It returns the path of its image when no
// smiley and no other rank names it, for the caller to delete after the commit.
// The log row is rank.delete, with the count of members who held the rank.
func AdminDeleteRank(ctx context.Context, pool *pgxpool.Pool, v Viewer, id int64, now time.Time) (freed string, err error) {
	err = AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		var r AdminRank
		err := scanRank(tx.QueryRow(ctx, `select `+rankColumns+` from ranks r where r.id = $1 for update`, id), &r)
		if errors.Is(err, pgx.ErrNoRows) {
			return Change{}, ErrNotFound
		}
		if err != nil {
			return Change{}, fmt.Errorf("board: read rank %d: %w", id, err)
		}
		// The foreign key sets users.rank_id to null.
		if _, err := tx.Exec(ctx, `delete from ranks where id = $1`, id); err != nil {
			return Change{}, fmt.Errorf("board: delete rank %d: %w", id, err)
		}
		if freed, err = freeImage(ctx, tx, r.Image); err != nil {
			return Change{}, err
		}
		return Change{Action: "rank.delete", TargetType: "rank", TargetID: id,
			Details: map[string]any{"title": r.Title, "members": r.Members, "image": r.Image}}, nil
	})
	if err != nil {
		return "", err
	}
	return freed, nil
}
