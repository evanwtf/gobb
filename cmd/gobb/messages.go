package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/config"
	"github.com/evanwtf/gobb/internal/db"
)

const messagesUsage = `usage: gobb messages thread [-dry-run]`

// messagesCommand runs a command on the private messages. thread joins old
// replies to the conversations they answer (see [board.ThreadMessages]). With
// -dry-run it counts them and changes nothing. Messages go to stderr; the exit
// code is 0 on success, 1 on a failure and 2 on a usage error.
func messagesCommand(ctx context.Context, args []string) error {
	log := slog.New(slog.NewTextHandler(stderr, nil))
	if len(args) == 0 || args[0] != "thread" {
		fmt.Fprintln(stderr, messagesUsage)
		return errUsage
	}
	fs := flag.NewFlagSet("gobb messages thread", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, messagesUsage) }
	dryRun := fs.Bool("dry-run", false, "count the messages to join and change nothing")
	if err := fs.Parse(args[1:]); err != nil {
		return errUsage
	}
	if fs.NArg() > 0 {
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
	n, err := board.ThreadMessages(ctx, pool, *dryRun)
	if err != nil {
		log.Error("messages thread failed", "err", err)
		return errReported
	}
	if *dryRun {
		log.Info("dry run; nothing changed", "would_join", n)
	} else {
		log.Info("replies joined to their conversations", "joined", n)
	}
	return nil
}
