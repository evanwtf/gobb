package board

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/markup"
)

// The page editor of the admin area. A page saved here has the columns that an
// import gives it: the text goes through the parser of posts with the three
// flags of the page, so a page renders the same whichever way it came in.

// maxPageTitleBytes is the longest title of a page. It is the limit of a name in
// a bundle.
const maxPageTitleBytes = 255

// reservedSlug is the slug that a new page may not take: /admin/pages/new is the
// form of a new page.
const reservedSlug = "new"

// PageInput is what the page editor sends: the slug, the title, the Markdown
// source and the three flags of the body.
type PageInput struct {
	Slug, Title, Source         string
	Markdown, Smilies, Autolink bool
}

// PageForm is a stored page as the editor shows it.
type PageForm struct {
	PageInput
	UpdatedAt time.Time
	// OldLinks is the count of old addresses (legacy routes) that lead to the
	// page.
	OldLinks int
}

// AdminPage returns the page with the given slug for the editor, or
// [ErrNotFound].
func AdminPage(ctx context.Context, q db.Querier, slug string) (PageForm, error) {
	var f PageForm
	err := q.QueryRow(ctx, `
select slug, title, body_source, enable_markdown, enable_smilies, enable_autolink, updated_at,
       (select count(*) from legacy_routes where target_kind = 'page' and target_slug = static_pages.slug)
  from static_pages where slug = $1`, slug).
		Scan(&f.Slug, &f.Title, &f.Source, &f.Markdown, &f.Smilies, &f.Autolink, &f.UpdatedAt, &f.OldLinks)
	if errors.Is(err, pgx.ErrNoRows) {
		return PageForm{}, ErrNotFound
	}
	if err != nil {
		return PageForm{}, fmt.Errorf("board: read page %q: %w", slug, err)
	}
	return f, nil
}

// cleanPageTitle trims a page title and applies the rules of a name in a bundle:
// valid UTF-8, no control character, at most 255 bytes, not empty.
func cleanPageTitle(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case !utf8.ValidString(s) || strings.IndexFunc(s, unicode.IsControl) >= 0:
		return "", inputError("title", "The title may not hold control characters or line breaks.")
	case s == "":
		return "", inputError("title", "Enter a title.")
	case len(s) > maxPageTitleBytes:
		return "", inputError("title", fmt.Sprintf("The title is longer than %d bytes.", maxPageTitleBytes))
	}
	return s, nil
}

// plainNewlines turns each line break of s into "\n". A browser sends "\r\n"
// from a text box; the parser reads both the same way, and a page stores one.
func plainNewlines(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// parsePage parses the source of a page with the flags of in, as an import does,
// and returns the stored forms. A fault is an [*InputError] on "body".
func parsePage(ctx context.Context, q db.Querier, v Viewer, in PageInput) (Body, error) {
	flags := markup.Options{Markdown: in.Markdown, Smilies: in.Smilies, Autolink: in.Autolink}
	return parseText(ctx, q, v, textPage, 0, in.Source, &flags, nil)
}

// AdminCreatePage adds the page in. The slug follows the rule of a bundle
// ([bundle.ValidSlug]) and no page holds it yet; the title is 1 to 255 bytes;
// the source is 1 to 100,000 bytes. A fault is an [*InputError]; v needs the
// admin right. The slug "new" is reserved. The log row is page.create, with the slug as a detail (a page has
// no number, so its target ID is 0).
func AdminCreatePage(ctx context.Context, pool *pgxpool.Pool, v Viewer, in PageInput, now time.Time) error {
	return AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		in.Slug = strings.TrimSpace(in.Slug)
		if !bundle.ValidSlug(in.Slug) {
			return Change{}, inputError("slug", "Use 1 to 64 lower-case letters, digits and hyphens, and start with a letter or a digit.")
		}
		if in.Slug == reservedSlug {
			return Change{}, inputError("slug", "The slug \""+reservedSlug+"\" is reserved for the form of a new page. Pick another.")
		}
		title, err := cleanPageTitle(in.Title)
		if err != nil {
			return Change{}, err
		}
		in.Title, in.Source = title, plainNewlines(in.Source)
		body, err := parsePage(ctx, tx, v, in)
		if err != nil {
			return Change{}, err
		}
		tag, err := tx.Exec(ctx, `
insert into static_pages (slug, title, body_source, body_tree, enable_markdown, enable_smilies, enable_autolink, updated_at)
values ($1, $2, $3, $4::jsonb, $5, $6, $7, $8)
on conflict (slug) do nothing`,
			in.Slug, in.Title, in.Source, string(body.JSON), in.Markdown, in.Smilies, in.Autolink, now)
		if err != nil {
			return Change{}, fmt.Errorf("board: add page %q: %w", in.Slug, err)
		}
		if tag.RowsAffected() == 0 {
			return Change{}, inputError("slug", "A page with this slug exists. Pick another slug.")
		}
		return Change{Action: "page.create", TargetType: "page", Details: map[string]any{"slug": in.Slug, "title": in.Title}}, nil
	})
}

// AdminUpdatePage replaces the title, the source and the flags of the page with
// the slug slug by those of in. The slug does not change: old addresses name it.
// The rules of the title and the source are those of [AdminCreatePage]. The text
// is parsed again, with the smilies of the board now, so a save without a change
// of the text still gives a page the smilies that were added since. A save that
// changes no column, the stored tree included, is [ErrNoChange]. A missing page
// is [ErrNotFound]. The log row is page.update with the names of the fields that
// changed.
func AdminUpdatePage(ctx context.Context, pool *pgxpool.Pool, v Viewer, slug string, in PageInput, now time.Time) error {
	return AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		title, err := cleanPageTitle(in.Title)
		if err != nil {
			return Change{}, err
		}
		in.Title, in.Source = title, plainNewlines(in.Source)
		body, err := parsePage(ctx, tx, v, in)
		if err != nil {
			return Change{}, err
		}
		var old PageInput
		var sameTree bool
		err = tx.QueryRow(ctx, `
select title, body_source, enable_markdown, enable_smilies, enable_autolink, body_tree = $2::jsonb
  from static_pages where slug = $1 for update`, slug, string(body.JSON)).
			Scan(&old.Title, &old.Source, &old.Markdown, &old.Smilies, &old.Autolink, &sameTree)
		if errors.Is(err, pgx.ErrNoRows) {
			return Change{}, ErrNotFound
		}
		if err != nil {
			return Change{}, fmt.Errorf("board: read page %q: %w", slug, err)
		}
		fields := changed([]string{"title", "source", "markdown", "smilies", "autolink"},
			old.Title != in.Title, old.Source != in.Source, old.Markdown != in.Markdown, old.Smilies != in.Smilies, old.Autolink != in.Autolink)
		if len(fields) == 0 && sameTree {
			return Change{}, ErrNoChange
		}
		if len(fields) == 0 {
			fields = []string{"tree"}
		}
		if _, err := tx.Exec(ctx, `
update static_pages set title = $2, body_source = $3, body_tree = $4::jsonb,
       enable_markdown = $5, enable_smilies = $6, enable_autolink = $7, updated_at = $8
 where slug = $1`, slug, in.Title, in.Source, string(body.JSON), in.Markdown, in.Smilies, in.Autolink, now); err != nil {
			return Change{}, fmt.Errorf("board: update page %q: %w", slug, err)
		}
		return Change{Action: "page.update", TargetType: "page", Details: map[string]any{"slug": slug, "title": in.Title, "fields": fields}}, nil
	})
}

// AdminDeletePage deletes the page with the slug slug. A missing page is
// [ErrNotFound]. Old addresses that lead to the page then answer 404. The log
// row is page.delete.
func AdminDeletePage(ctx context.Context, pool *pgxpool.Pool, v Viewer, slug string, now time.Time) error {
	return AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		var title string
		err := tx.QueryRow(ctx, `delete from static_pages where slug = $1 returning title`, slug).Scan(&title)
		if errors.Is(err, pgx.ErrNoRows) {
			return Change{}, ErrNotFound
		}
		if err != nil {
			return Change{}, fmt.Errorf("board: delete page %q: %w", slug, err)
		}
		return Change{Action: "page.delete", TargetType: "page", Details: map[string]any{"slug": slug, "title": title}}, nil
	})
}
