package board

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/evanwtf/gobb/internal/db"
)

// Page sizes of the lists.
const (
	topicsPerPage  = 50
	postsPerPage   = 25
	membersPerPage = 50
)

// TopicsPerPage and PostsPerPage are the page sizes of a forum and of a topic.
// A caller that turns an item offset into a page number uses them.
const (
	TopicsPerPage = topicsPerPage
	PostsPerPage  = postsPerPage
)

// maxPage is the highest page number that [ParsePage] accepts.
const maxPage = 1_000_000

// ParsePage reads a ?page= value. The empty string means page 1. Any other value
// must be a decimal integer from 1 to 1,000,000, written with ASCII digits
// only: no sign, no space, no other digits.
func ParsePage(s string) (int, error) {
	if s == "" {
		return 1, nil
	}
	if len(s) > len(strconv.Itoa(maxPage)) {
		return 0, fmt.Errorf("board: page %q is out of range", s)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("board: page %q is not a number", s)
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > maxPage {
		return 0, fmt.Errorf("board: page %q is out of range", s)
	}
	return n, nil
}

// pageCount returns the number of pages for n rows. An empty list has one page.
func pageCount(n, perPage int) int {
	if n <= 0 {
		return 1
	}
	return (n + perPage - 1) / perPage
}

// scope holds the forums in which one viewer may read, approve and delete. The
// queries pass it as arrays, so the visibility rule is one SQL clause.
type scope struct {
	read, approve, del []int64
	userID             int64
}

// newScope computes the scope of v. A viewer who may approve or delete in a
// forum also needs forum.read there, so both lists are subsets of read.
func newScope(ctx context.Context, q db.Querier, v Viewer) (scope, error) {
	rows, err := q.Query(ctx, `select id from forums`)
	if err != nil {
		return scope{}, fmt.Errorf("board: list forum ids: %w", err)
	}
	defer rows.Close()
	sc := scope{read: []int64{}, approve: []int64{}, del: []int64{}, userID: v.UserID}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return scope{}, fmt.Errorf("board: list forum ids: %w", err)
		}
		if !v.Perms.Has("forum.read", id) {
			continue
		}
		sc.read = append(sc.read, id)
		if v.Perms.Has("mod.approve", id) {
			sc.approve = append(sc.approve, id)
		}
		if v.Perms.Has("mod.delete", id) {
			sc.del = append(sc.del, id)
		}
	}
	if err := rows.Err(); err != nil {
		return scope{}, fmt.Errorf("board: list forum ids: %w", err)
	}
	return sc, nil
}

// args returns the four query arguments that [visible] numbers from first.
func (sc scope) args() []any {
	return []any{sc.read, sc.approve, sc.del, sc.userID}
}

// visible returns the SQL clause for the rule "who sees which rows" on a
// table with the columns forum_id, author_id and visibility, under alias. The
// clause reads four arguments, numbered first to first+3, in the order of
// [scope.args]:
//
//   - an approved row shows to everyone who may read its forum;
//   - an unapproved row shows to its author and to viewers who may approve in
//     its forum;
//   - a deleted row shows to viewers who may delete in its forum.
//
// Every case needs forum.read, so a viewer who cannot read a forum sees none of
// its rows.
func visible(alias string, first int) string {
	n := func(i int) string { return "$" + strconv.Itoa(first+i) }
	r := strings.NewReplacer("{a}", alias, "{read}", n(0), "{approve}", n(1), "{del}", n(2), "{uid}", n(3))
	return r.Replace(`({a}.forum_id = any({read}::bigint[]) and (
  {a}.visibility = 'approved'
  or ({a}.visibility = 'unapproved' and ({a}.forum_id = any({approve}::bigint[])
       or ({a}.author_id is not null and {a}.author_id = {uid}::bigint)))
  or ({a}.visibility = 'deleted' and {a}.forum_id = any({del}::bigint[]))))`)
}
