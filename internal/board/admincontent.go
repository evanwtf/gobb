package board

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/evanwtf/gobb/internal/account"
	"github.com/evanwtf/gobb/internal/db"
)

// The content editors of the admin area: static pages, smilies and ranks. Each
// write goes through [AdminWrite], so it needs an admin and writes one mod_log
// row with the admin as the actor.
//
// A write that replaces or removes an image file does not touch the file. It
// returns the path of the file that no row names any more, and the caller
// deletes that file after the commit.

// The image directories that the editors write into. They are directories that
// /files/ serves.
const (
	SmileyDir = "smilies"
	RankDir   = "ranks"
)

// maxPageBytes is the limit of a static page. It is the limit of the body of a
// bundle (bundle.MaxSourceBytes), so every page that an import brings can be
// saved again.
const maxPageBytes = 100_000

// maxLabelRunes is the longest title of a rank or description of a smiley, in
// characters.
const maxLabelRunes = 255

// maxCount is the largest value of a count that lands in a 32-bit database
// column.
const maxCount = 1<<31 - 1

// ValidContentPath reports whether p is a path that gobb may write or delete
// under the data directory dir: slash-separated, clean, at most 255 bytes, under
// dir, and with no element that starts with a dot or holds a backslash.
func ValidContentPath(dir, p string) bool {
	rest, ok := strings.CutPrefix(p, dir+"/")
	if !ok || rest == "" || len(p) > 255 || !fs.ValidPath(p) || strings.ContainsRune(p, '\\') {
		return false
	}
	for _, elem := range strings.Split(rest, "/") {
		if strings.HasPrefix(elem, ".") {
			return false
		}
	}
	return true
}

// cleanLabel trims a one-line field and applies the rules of labels: valid
// UTF-8, at most limit characters, no control character, no line or paragraph
// separator and no hidden formatting character. An empty value is an error when
// required is true. field is the name of the form field; noun is the word in
// the messages.
func cleanLabel(s, field, noun string, limit int, required bool) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case !utf8.ValidString(s) || account.HasControl(s):
		return "", inputError(field, "The "+noun+" may not hold control characters or line breaks.")
	case account.HasHiddenFormat(s):
		return "", inputError(field, "Remove the hidden formatting character.")
	case required && s == "":
		return "", inputError(field, "Enter a "+noun+".")
	case utf8.RuneCountInString(s) > limit:
		return "", inputError(field, fmt.Sprintf("The %s is longer than %d characters.", noun, limit))
	}
	return s, nil
}

// isUniqueViolation reports whether err is the refusal of a unique index.
func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// freeImage returns path when no smiley and no rank names it, and "" otherwise.
// It runs after the write that dropped the last name, in the same transaction.
// An empty path is no file.
func freeImage(ctx context.Context, q db.Querier, path string) (string, error) {
	if path == "" {
		return "", nil
	}
	var used bool
	err := q.QueryRow(ctx, `select exists (select 1 from smilies where image_path = $1)
    or exists (select 1 from ranks where image_path = $1)`, path).Scan(&used)
	if err != nil {
		return "", fmt.Errorf("board: look for users of an image: %w", err)
	}
	if used {
		return "", nil
	}
	return path, nil
}

// checkImagePath refuses a path that the caller should not store as the image of
// a row of dir. The web layer makes the path itself, so a fault here is a bug.
func checkImagePath(dir, path string) error {
	if !ValidContentPath(dir, path) {
		return fmt.Errorf("board: %q is not an image path under %s", path, dir)
	}
	return nil
}

// changed returns the names of the fields whose flag is true, in order.
func changed(fields []string, flags ...bool) []string {
	var out []string
	for i, f := range flags {
		if f {
			out = append(out, fields[i])
		}
	}
	return out
}
