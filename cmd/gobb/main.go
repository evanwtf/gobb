// Command gobb is the gobb forum server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
	// The timezone database is compiled in, so that timezone checks work in the
	// static image, which has no zoneinfo files.
	_ "time/tzdata"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/acldb"
	"github.com/evanwtf/gobb/internal/config"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/importer"
	"github.com/evanwtf/gobb/internal/version"
	"github.com/evanwtf/gobb/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// shutdownTimeout is the window for a clean stop: it covers the end of the
// open requests and the close of the database pool. It stays under the 30 s
// stop grace period in compose.yaml. The tests shorten it.
var shutdownTimeout = 25 * time.Second

const (
	// healthcheckTimeout bounds the request that the healthcheck command sends.
	healthcheckTimeout = 2 * time.Second
	// defaultListen matches the default in package config.
	defaultListen = "127.0.0.1:8080"
)

type command func(ctx context.Context, args []string) error

// errUsage is the error of a command that was called wrongly. It has printed
// its own usage line, so run adds none and exits 2.
var errUsage = errors.New("usage")

// stdout receives the results of commands. The tests replace it.
var stdout io.Writer = os.Stdout

// commands maps a subcommand name to its handler.
var commands = map[string]command{
	"version": func(_ context.Context, _ []string) error {
		fmt.Println("gobb " + version.Version)
		return nil
	},
	"import":      importBundle,
	"migrate":     migrate,
	"perms":       perms,
	"serve":       serve,
	"healthcheck": healthcheck,
}

// migrate applies the pending schema migrations and logs each file it applied.
func migrate(ctx context.Context, _ []string) error {
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	return applyMigrations(ctx, pool)
}

// importBundle loads a bundle into the database and the data directory. It
// applies the pending migrations first. On success it prints the number of rows
// it loaded from each data file, in bundle.Files order, then the number of files.
func importBundle(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, "usage: gobb import [--replace] <bundle>") }
	replace := fs.Bool("replace", false, "empty the database and move the old files aside first")
	if err := fs.Parse(args); err != nil {
		// The flag package has printed the message and the usage.
		return errUsage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errUsage
	}
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := applyMigrations(ctx, pool); err != nil {
		return err
	}
	counts, err := importer.Run(ctx, pool, fs.Arg(0), importer.Options{DataDir: cfg.DataDir, Replace: *replace})
	if err != nil {
		return err
	}
	for _, name := range append(slices.Clone(bundle.Files), "files") {
		if _, err := fmt.Fprintf(stdout, "%-14s %d\n", name, counts[name]); err != nil {
			return err
		}
	}
	return nil
}

// perms runs a permissions subcommand. The only one is "export".
func perms(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] != "export" {
		fmt.Fprintln(os.Stderr, "usage: gobb perms export")
		return errUsage
	}
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	// A read-only snapshot, so that the export is one view of the board. The
	// command does not apply migrations: an unmigrated database is an error.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var migrated bool
	if err := tx.QueryRow(ctx, `select to_regclass('schema_migrations') is not null`).Scan(&migrated); err != nil {
		return err
	}
	if !migrated {
		return errors.New("perms export: the database has no schema; run gobb migrate first")
	}
	return acldb.Export(ctx, tx, stdout)
}

// applyMigrations runs the pending migrations and logs each file it applied.
func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	applied, err := db.Migrate(ctx, pool)
	for _, name := range applied {
		slog.Info("applied migration", "file", name)
	}
	if err != nil {
		return err
	}
	slog.Info("migrations done", "applied", len(applied))
	return nil
}

// newHTTPServer returns the server for h, with the limits that protect a small
// host from slow and oversized requests.
func newHTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

// serve migrates the schema, then serves HTTP until SIGTERM or SIGINT.
// If a migration fails, it returns before the server listens.
func serve(ctx context.Context, _ []string) error {
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DSN)
	if err != nil {
		return err
	}
	if err := applyMigrations(ctx, pool); err != nil {
		pool.Close()
		return err
	}
	h, err := web.New(ctx, pool, web.Options{
		BasePath:       cfg.BasePath,
		DataDir:        cfg.DataDir,
		TrustedProxies: cfg.TrustedProxies,
	})
	if err != nil {
		pool.Close()
		return err
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		pool.Close()
		return err
	}
	slog.Info("listening", "addr", ln.Addr().String(), "base_path", cfg.BasePath)

	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)
	return serveUntil(ctx, newHTTPServer(h), ln, pool.Close, sigs, os.Exit)
}

// serveUntil serves srv on ln until ctx ends, a signal arrives on sigs, or the
// server fails. It then stops the server and calls closePool, all inside the
// shutdownTimeout window. When closePool has not returned at the end of the
// window, it returns an error that says so. A second signal during the stop
// calls exit(1) at once.
func serveUntil(ctx context.Context, srv *http.Server, ln net.Listener, closePool func(), sigs <-chan os.Signal, exit func(int)) error {
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		// The server stopped by itself, so no request is open.
		closePool()
		return err
	case <-ctx.Done():
	case sig := <-sigs:
		slog.Info("signal received", "signal", sig.String())
	}
	slog.Info("shutting down")

	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-sigs:
			slog.Warn("second signal: exiting now")
			exit(1)
		case <-stopped:
		}
	}()

	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutErr := srv.Shutdown(shutCtx)
	if shutErr != nil {
		// Drop the connections that stayed open, so the pool can close.
		_ = srv.Close()
		shutErr = fmt.Errorf("shutdown: %w", shutErr)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	closed := make(chan struct{})
	go func() {
		closePool()
		close(closed)
	}()
	select {
	case <-closed:
	case <-shutCtx.Done():
		return fmt.Errorf("closing the database pool did not finish within %s", shutdownTimeout)
	}
	return shutErr
}

// healthcheck requests /healthz on the local listen address. It exits 0 on
// 200 and fails otherwise. The static image has no shell or curl, so the
// binary checks itself. It reads only GOBB_LISTEN, not GOBB_DSN.
func healthcheck(ctx context.Context, _ []string) error {
	listen := strings.TrimSpace(os.Getenv("GOBB_LISTEN"))
	if listen == "" {
		listen = defaultListen
	}
	url, err := web.HealthURL(listen)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, healthcheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: %s answered %s", url, resp.Status)
	}
	return nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	os.Exit(run(context.Background(), os.Args[1:]))
}

// run dispatches on the first argument and returns the process exit code.
func run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "gobb: unknown command %q\n", args[0])
		usage()
		return 2
	}
	if err := cmd(ctx, args[1:]); err != nil {
		if errors.Is(err, errUsage) {
			return 2
		}
		slog.Error("command failed", "command", args[0], "err", err)
		return 1
	}
	return 0
}

func usage() {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Fprintf(os.Stderr, "usage: gobb <command>\ncommands: %s\n", strings.Join(names, ", "))
}
