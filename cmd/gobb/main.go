// Command gobb is the gobb forum server.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/evanwtf/gobb/internal/acldb"
	"github.com/evanwtf/gobb/internal/config"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/version"
	"github.com/evanwtf/gobb/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// shutdownTimeout stays under the 30 s stop grace period in compose.yaml.
	shutdownTimeout = 25 * time.Second
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

// serve migrates the schema, then serves HTTP until SIGTERM or SIGINT.
// If a migration fails, it returns before the server listens.
func serve(ctx context.Context, _ []string) error {
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	pool, err := db.Open(ctx, cfg.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := applyMigrations(ctx, pool); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("/healthz", web.Health(pool))
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	slog.Info("listening", "addr", ln.Addr().String())

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
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
