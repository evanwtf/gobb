// Package importer loads an import bundle into an empty gobb database.
//
// [Run] holds an advisory lock, stages the bundle's files, loads every data file
// in one transaction, and moves the data directories into place after the
// commit. A failure before the commit leaves the database and the data directory
// as they were.
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/counters"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/markup"
)

// lockID is the key of the advisory lock that keeps two imports from running at
// the same time. The migration lock uses the key before it.
const lockID = 7212027

// Options control a run of [Run].
type Options struct {
	// DataDir is the data directory. The importer makes it when it is missing.
	DataDir string
	// Replace allows an import into a database that holds data. It truncates
	// every data table, and it moves the old data directories to
	// DataDir/.replaced-<UTC time>/.
	Replace bool
}

// now returns the time that names the .replaced-<stamp> directory. The tests
// replace it.
var now = time.Now

// parseBody turns a Markdown source into a tree. The tests replace it.
var parseBody = markup.Parse

// source is what a loader reads: the bundle and the smiley list that parses
// every body. Every loader takes it.
type source struct {
	*bundle.Reader
	smilies []markup.Smiley
	// topicForum maps a topic ID to its forum ID. The topics loader fills it,
	// and the posts loader reads it. It holds IDs only.
	topicForum map[int64]int64
}

// loader loads one data file inside the import transaction and returns the
// number of rows it loaded.
type loader struct {
	file string
	load func(ctx context.Context, tx pgx.Tx, r *source) (int, error)
}

// loaders lists the loaders in bundle.Files order. Every data file has one.
var loaders = []loader{
	{"ranks", loadRanks},
	{"users", loadUsers},
	{"gone_users", loadGoneUsers},
	{"groups", loadGroups},
	{"group_members", loadGroupMembers},
	{"roles", loadRoles},
	{"grants", loadGrants},
	{"forums", loadForums},
	{"topics", loadTopics},
	{"posts", loadPosts},
	{"polls", loadPolls},
	{"poll_votes", loadPollVotes},
	{"attachments", loadAttachments},
	{"pms", loadPMs},
	{"topic_watches", loadTopicWatches},
	{"bookmarks", loadBookmarks},
	{"topic_marks", loadTopicMarks},
	{"forum_marks", loadForumMarks},
	{"bans", loadBans},
	{"smilies", loadSmilies},
	{"legacy_routes", loadLegacyRoutes},
	{"legacy_files", loadLegacyFiles},
	{"static_pages", loadStaticPages},
}

// Run imports the bundle at src into the database of pool. src is a bundle
// directory or a .tar.gz or .tgz archive. Run returns the number of rows it
// loaded for each data file it loads, and under "files" the number of files it
// put in the data directory.
//
// Without opt.Replace, Run refuses a database with a row in any data table and
// a data directory with an entry. The error names the table or the directory.
func Run(ctx context.Context, pool *pgxpool.Pool, src string, opt Options) (counts map[string]int, err error) {
	if opt.DataDir == "" {
		return nil, errors.New("importer: the data directory is not set")
	}
	if err := os.MkdirAll(opt.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("importer: make the data directory: %w", err)
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("importer: acquire a connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "select pg_advisory_lock($1)", lockID); err != nil {
		return nil, fmt.Errorf("importer: take the import lock: %w", err)
	}
	defer func() {
		// The context may be cancelled already. A connection that cannot
		// unlock goes back to the pool closed, so the lock dies with it.
		uctx := context.WithoutCancel(ctx)
		if _, uerr := conn.Exec(uctx, "select pg_advisory_unlock($1)", lockID); uerr != nil {
			_ = conn.Conn().Close(uctx)
		}
	}()

	staging, err := newStagingDir(opt.DataDir)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(staging)
		}
	}()

	dir := src
	archive := isArchive(src)
	if archive {
		dir = filepath.Join(staging, "bundle")
		if err := bundle.Extract(src, dir); err != nil {
			return nil, err
		}
	}
	r, err := bundle.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := bundle.Check(r); err != nil {
		return nil, err
	}

	tables, err := db.DataTables(ctx, conn)
	if err != nil {
		return nil, err
	}
	if !opt.Replace {
		if err := requireEmpty(ctx, conn, tables); err != nil {
			return nil, err
		}
		if err := checkDataDirs(opt.DataDir); err != nil {
			return nil, err
		}
	}

	smilies, err := readSmilies(r)
	if err != nil {
		return nil, err
	}
	files, err := stageFiles(r, staging, archive)
	if err != nil {
		return nil, err
	}

	counts, err = load(ctx, conn, tables, &source{Reader: r, smilies: smilies}, opt.Replace)
	if err != nil {
		return nil, err
	}
	counts["files"] = files

	if err := installFiles(opt.DataDir, staging, opt.Replace, now()); err != nil {
		keep = true
		return nil, fmt.Errorf("importer: the rows are committed, but moving the files failed: %w; "+
			"the staged files are in %s; run the import again with --replace to repair it", err, staging)
	}
	m := r.Manifest()
	slog.Info("import done", "files", files, "source", m.Source, "created_at", m.CreatedAt)
	return counts, nil
}

// requireEmpty refuses a data table that holds a row.
func requireEmpty(ctx context.Context, q db.Querier, tables []string) error {
	for _, t := range tables {
		var has bool
		// The names come from the catalog and are quoted.
		stmt := "select exists (select 1 from " + pgx.Identifier{t}.Sanitize() + ")"
		if err := q.QueryRow(ctx, stmt).Scan(&has); err != nil {
			return fmt.Errorf("importer: check table %s: %w", t, err)
		}
		if has {
			return fmt.Errorf("importer: table %s has rows; import into an empty database or use --replace", t)
		}
	}
	return nil
}

// readSmilies reads smilies.jsonl into the list that parses every body. Parsing
// needs only the code. Image is the file path, for completeness.
func readSmilies(r *bundle.Reader) ([]markup.Smiley, error) {
	var list []markup.Smiley
	err := bundle.Each(r, func(_ int, s bundle.Smiley) error {
		list = append(list, markup.Smiley{Code: s.Code, Image: s.Image.Path, Alt: s.Description})
		return nil
	})
	return list, err
}

// load runs the whole database part of the import on conn in one transaction.
func load(ctx context.Context, conn *pgxpool.Conn, tables []string, r *source, replace bool) (counts map[string]int, err error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("importer: begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	if replace {
		quoted := make([]string, len(tables))
		for i, t := range tables {
			quoted[i] = pgx.Identifier{t}.Sanitize()
		}
		if _, err := tx.Exec(ctx, "truncate "+strings.Join(quoted, ", ")+" restart identity cascade"); err != nil {
			return nil, fmt.Errorf("importer: truncate: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, "set constraints all deferred"); err != nil {
		return nil, fmt.Errorf("importer: defer constraints: %w", err)
	}

	counts = map[string]int{}
	if err := loadSettings(ctx, tx, r); err != nil {
		return nil, err
	}
	for _, l := range loaders {
		n, err := l.load(ctx, tx, r)
		if err != nil {
			return nil, err
		}
		counts[l.file] = n
		slog.Info("loaded", "file", l.file, "rows", n)
	}
	if err := counters.All(ctx, tx); err != nil {
		return nil, fmt.Errorf("importer: %w", err)
	}
	if err := db.ResetSequences(ctx, tx); err != nil {
		return nil, err
	}
	if err := reserveGoneUserIDs(ctx, tx); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("importer: commit: %w", err)
	}
	return counts, nil
}

// reserveGoneUserIDs moves the users sequence past the largest ID in users and
// gone_users. A gone user's URL answers 410 for good, so a new member never
// gets that ID. It runs after db.ResetSequences, which looks at users only.
func reserveGoneUserIDs(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx,
		`select setval(pg_get_serial_sequence('users', 'id'),
		        greatest((select coalesce(max(id), 0) from users),
		                 (select coalesce(max(id), 0) from gone_users)) + 1,
		        false)`)
	if err != nil {
		return fmt.Errorf("importer: reserve the IDs of gone users: %w", err)
	}
	return nil
}

// settingRow is one row of the settings table.
type settingRow struct {
	key   string
	value string // JSON
}

// settingRows makes one row for each JSON field of s. The key is the JSON name
// and the value is the field's JSON. header_image holds the path of the file, or
// null.
func settingRows(s bundle.Settings) ([]settingRow, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if s.HeaderImage != nil {
		p, err := json.Marshal(s.HeaderImage.Path)
		if err != nil {
			return nil, err
		}
		fields["header_image"] = p
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	rows := make([]settingRow, len(keys))
	for i, k := range keys {
		rows[i] = settingRow{key: k, value: string(fields[k])}
	}
	return rows, nil
}

func loadSettings(ctx context.Context, tx pgx.Tx, r *source) error {
	rows, err := settingRows(r.Settings())
	if err != nil {
		return fmt.Errorf("importer: settings.json: %w", err)
	}
	for _, s := range rows {
		if _, err := tx.Exec(ctx, `insert into settings (key, value) values ($1, $2::jsonb)`, s.key, s.value); err != nil {
			return fmt.Errorf("importer: settings.json: %s: %w", s.key, err)
		}
	}
	return nil
}
