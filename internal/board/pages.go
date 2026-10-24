package board

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/markup"
)

// StaticPage is a page of text that the owner wrote, such as the rules.
type StaticPage struct {
	Slug, Title string
	// Tree is the stored tree of the body. [PageLinks] leaves it nil.
	Tree      *markup.Node
	UpdatedAt time.Time
}

// Page returns the static page with the given slug, or [ErrNotFound].
func Page(ctx context.Context, q db.Querier, slug string) (StaticPage, error) {
	var (
		p   StaticPage
		raw []byte
	)
	err := q.QueryRow(ctx, `select slug, title, body_tree, updated_at from static_pages where slug = $1`, slug).
		Scan(&p.Slug, &p.Title, &raw, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return StaticPage{}, ErrNotFound
	}
	if err != nil {
		return StaticPage{}, fmt.Errorf("board: read page %q: %w", slug, err)
	}
	p.Tree = decodeTree(raw, "static page", 0)
	return p, nil
}

// PageLinks lists the static pages in slug order, without their trees, for the
// footer. It runs one statement.
func PageLinks(ctx context.Context, q db.Querier) ([]StaticPage, error) {
	rows, err := q.Query(ctx, `select slug, title, updated_at from static_pages order by slug`)
	if err != nil {
		return nil, fmt.Errorf("board: list pages: %w", err)
	}
	defer rows.Close()
	var out []StaticPage
	for rows.Next() {
		var p StaticPage
		if err := rows.Scan(&p.Slug, &p.Title, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("board: list pages: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("board: list pages: %w", err)
	}
	return out, nil
}
