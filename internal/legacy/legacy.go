// Package legacy holds the old URLs of the board that a bundle came from, and
// matches a request against them. The table is read once and lives in memory.
package legacy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"slices"

	"github.com/evanwtf/gobb/internal/db"
)

// Route is one row of legacy_routes: an old path and the page it stands for.
type Route struct {
	// Path is the old path under the base path, with no leading slash.
	Path string
	// Match holds query parameters that must have these values.
	Match map[string]string
	// IDParam names the query parameter that holds the target ID, or is nil.
	IDParam *string
	// OffsetParam names the query parameter that holds an item offset, or is nil.
	OffsetParam *string
	// Kind is the target kind: home, forum, topic, post, user, members, team,
	// attachment, avatar, search, account, page, messages or message.
	Kind string
	// TargetID is a fixed target ID, or nil.
	TargetID *int64
	// TargetSlug is the slug of a static page, or nil.
	TargetSlug *string
	// Position orders the routes that apply to one request: the lowest wins.
	Position int
}

// Table is the set of legacy routes and file prefixes of a board. It is
// read-only after [Load], so many requests may use it at once.
type Table struct {
	// byPath maps a path to its routes, in the order they are tried: lowest
	// position first, then lowest row ID.
	byPath map[string][]Route
	// files maps a prefix to its directory. maxPrefix is the longest prefix, in
	// bytes, which bounds the probes of [Table.File].
	files     map[string]string
	maxPrefix int
}

// row is a route with the ID of its row, for the tie-break.
type row struct {
	id int64
	Route
}

// Load reads the whole of legacy_routes and legacy_files. It logs one INFO line
// with the counts.
func Load(ctx context.Context, q db.Querier) (*Table, error) {
	rows, err := q.Query(ctx, `
select id, path, match, id_param, target_kind, target_id, target_slug, offset_param, position
  from legacy_routes order by position, id`)
	if err != nil {
		return nil, fmt.Errorf("legacy: read routes: %w", err)
	}
	defer rows.Close()
	var routes []row
	for rows.Next() {
		var (
			r   row
			raw []byte
		)
		if err := rows.Scan(&r.id, &r.Path, &raw, &r.IDParam, &r.Kind, &r.TargetID, &r.TargetSlug, &r.OffsetParam, &r.Position); err != nil {
			return nil, fmt.Errorf("legacy: read routes: %w", err)
		}
		if err := json.Unmarshal(raw, &r.Match); err != nil {
			return nil, fmt.Errorf("legacy: route %d: match: %w", r.id, err)
		}
		routes = append(routes, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("legacy: read routes: %w", err)
	}
	rows.Close()

	frows, err := q.Query(ctx, `select prefix, dir from legacy_files`)
	if err != nil {
		return nil, fmt.Errorf("legacy: read files: %w", err)
	}
	defer frows.Close()
	files := map[string]string{}
	for frows.Next() {
		var prefix, dir string
		if err := frows.Scan(&prefix, &dir); err != nil {
			return nil, fmt.Errorf("legacy: read files: %w", err)
		}
		files[prefix] = dir
	}
	if err := frows.Err(); err != nil {
		return nil, fmt.Errorf("legacy: read files: %w", err)
	}

	t := newTable(routes, files)
	slog.Info("legacy table loaded", "routes", len(routes), "files", len(files))
	return t, nil
}

// newTable indexes routes by path and orders each path's routes by position,
// then row ID.
func newTable(routes []row, files map[string]string) *Table {
	t := &Table{byPath: map[string][]Route{}, files: files}
	slices.SortStableFunc(routes, func(a, b row) int {
		if a.Position != b.Position {
			return a.Position - b.Position
		}
		switch {
		case a.id < b.id:
			return -1
		case a.id > b.id:
			return 1
		}
		return 0
	})
	for _, r := range routes {
		t.byPath[r.Path] = append(t.byPath[r.Path], r.Route)
	}
	for prefix := range files {
		t.maxPrefix = max(t.maxPrefix, len(prefix))
	}
	return t
}

// Match finds the route for a request. path is the request path under the base
// path, decoded once, with no leading slash. A route applies when its Path
// equals path, every Match pair equals the first value of that query parameter
// (the parameter must be present), and the query has IDParam when the route
// sets one. A parameter with an empty value is present. Of the routes that
// apply, the one with the lowest Position wins, then the lowest row ID.
func (t *Table) Match(path string, query url.Values) (Route, bool) {
	for _, r := range t.byPath[path] {
		if applies(r, query) {
			return r, true
		}
	}
	return Route{}, false
}

// applies reports whether the query satisfies r.
func applies(r Route, query url.Values) bool {
	for k, want := range r.Match {
		vals, ok := query[k]
		if !ok || len(vals) == 0 || vals[0] != want {
			return false
		}
	}
	if r.IDParam != nil {
		if vals, ok := query[*r.IDParam]; !ok || len(vals) == 0 {
			return false
		}
	}
	return true
}

// File maps an old file path, under the base path with no leading slash, to the
// directory and the rest of the path below its prefix. The longest prefix wins.
// A path that equals a prefix has no file in it, so it gives false.
//
// A prefix ends with "/", so only the slashes of the path are candidates, and no
// prefix is longer than the longest in the table. The work does not grow with
// the number of prefixes or with the length of the path.
func (t *Table) File(path string) (dir, rest string, ok bool) {
	limit := min(len(path), t.maxPrefix)
	for i := limit; i > 0; i-- {
		if path[i-1] != '/' {
			continue
		}
		if d, found := t.files[path[:i]]; found {
			if i == len(path) {
				return "", "", false
			}
			return d, path[i:], true
		}
	}
	return "", "", false
}

// Routes lists every route, ordered by path, then by the order in which Match
// tries them. The site uses it to warn about a route that its own pages hide.
func (t *Table) Routes() []Route {
	paths := make([]string, 0, len(t.byPath))
	for p := range t.byPath {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	var out []Route
	for _, p := range paths {
		out = append(out, t.byPath[p]...)
	}
	return out
}

// Prefixes lists the file prefixes in order.
func (t *Table) Prefixes() []string {
	out := make([]string, 0, len(t.files))
	for p := range t.files {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}
