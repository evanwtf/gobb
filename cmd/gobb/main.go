// Command gobb is the gobb forum server.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/evanwtf/gobb/internal/config"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/version"
)

type command func(ctx context.Context, args []string) error

// commands maps a subcommand name to its handler. Later tasks add serve and
// healthcheck.
var commands = map[string]command{
	"version": func(_ context.Context, _ []string) error {
		fmt.Println("gobb " + version.Version)
		return nil
	},
	"migrate": migrate,
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
