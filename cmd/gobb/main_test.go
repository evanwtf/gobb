package main

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/acldb"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/config"
	"github.com/evanwtf/gobb/internal/dbtest"
	"github.com/evanwtf/gobb/internal/importer"
	"github.com/jackc/pgx/v5/pgxpool"
)

// dsnOf returns a connection string for the database of pool. The pool's own
// ConnString names the admin database, so this replaces the database name.
func dsnOf(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	u, err := url.Parse(os.Getenv(dbtest.EnvVar))
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + pool.Config().ConnConfig.Database
	return u.String()
}

// The healthcheck command cannot call config.FromEnv, because it must not
// need GOBB_DSN. This test keeps its default in step with the config package.
func TestDefaultListenMatchesConfig(t *testing.T) {
	env := map[string]string{"GOBB_DSN": "postgres://example"}
	cfg, err := config.FromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != defaultListen {
		t.Fatalf("config default = %q, healthcheck default = %q", cfg.Listen, defaultListen)
	}
}

func TestPermsCommand(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	dir := bundletest.Write(t, bundletest.Small())
	if _, err := importer.Run(ctx, pool, dir, importer.Options{DataDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	if err := acldb.Export(ctx, pool, &want); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOBB_DSN", dsnOf(t, pool))

	var got bytes.Buffer
	old := stdout
	stdout = &got
	t.Cleanup(func() { stdout = old })

	if code := run(ctx, []string{"perms", "export"}); code != 0 {
		t.Fatalf("perms export exit = %d, want 0", code)
	}
	if got.String() != want.String() {
		t.Errorf("perms export output differs from acldb.Export:\n got %q\nwant %q", got.String(), want.String())
	}
	if want.Len() == 0 {
		t.Fatal("the export is empty")
	}
}

func TestPermsUsage(t *testing.T) {
	ctx := context.Background()
	for _, args := range [][]string{
		{"perms"},
		{"perms", "bogus"},
		{"perms", "export", "extra"},
	} {
		if code := run(ctx, args); code != 2 {
			t.Errorf("run(%v) = %d, want 2", args, code)
		}
	}
}

func TestPermsExportRefusesWithoutSchema(t *testing.T) {
	pool := dbtest.NewEmpty(t)
	t.Setenv("GOBB_DSN", dsnOf(t, pool))
	var got bytes.Buffer
	old := stdout
	stdout = &got
	t.Cleanup(func() { stdout = old })
	if code := run(context.Background(), []string{"perms", "export"}); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if got.Len() != 0 {
		t.Errorf("wrote %q before it failed", got.String())
	}
}

func TestImportCommand(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.NewEmpty(t)
	dir := bundletest.Write(t, bundletest.Small())
	data := t.TempDir()
	t.Setenv("GOBB_DSN", dsnOf(t, pool))
	t.Setenv("GOBB_DATA_DIR", data)

	var got bytes.Buffer
	old := stdout
	stdout = &got
	t.Cleanup(func() { stdout = old })

	if code := run(ctx, []string{"import", dir}); code != 0 {
		t.Fatalf("import exit = %d, want 0", code)
	}
	lines := strings.Split(strings.TrimRight(got.String(), "\n"), "\n")
	if want := len(bundle.Files) + 1; len(lines) != want {
		t.Fatalf("import printed %d lines, want %d:\n%s", len(lines), want, got.String())
	}
	if !regexp.MustCompile(`^posts +6$`).MatchString(lines[9]) {
		t.Errorf("line 10 = %q, want posts and 6", lines[9])
	}
	for i, name := range bundle.Files {
		if !strings.HasPrefix(lines[i], name+" ") {
			t.Errorf("line %d = %q, want it to start with %q", i+1, lines[i], name)
		}
	}
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "files") {
		t.Errorf("last line = %q, want it to start with files", last)
	}

	// A second import into the same database stops without --replace and
	// works with it.
	got.Reset()
	if code := run(ctx, []string{"import", dir}); code != 1 {
		t.Errorf("second import exit = %d, want 1", code)
	}
	if got.Len() != 0 {
		t.Errorf("a failed import wrote %q to stdout", got.String())
	}
	if code := run(ctx, []string{"import", "--replace", dir}); code != 0 {
		t.Errorf("import --replace exit = %d, want 0", code)
	}
}

func TestImportCommandUsage(t *testing.T) {
	ctx := context.Background()
	for _, args := range [][]string{
		{"import"},
		{"import", "--replace"},
		{"import", "a", "b"},
		{"import", "--bogus", "a"},
	} {
		if code := run(ctx, args); code != 2 {
			t.Errorf("run(%v) = %d, want 2", args, code)
		}
	}
}
