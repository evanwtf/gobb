package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/config"
	"github.com/evanwtf/gobb/internal/db"
)

const banUsage = `usage: gobb ban list
       gobb ban lift <id>`

// banCommand runs a ban command: list or lift. The administrator runs it on the
// host that holds the database, with GOBB_DSN set. It reaches bans of every kind
// (user, email and ip), so an operator whom a ban locks out of the admin pages
// can recover without SQL.
//
// list prints the bans in force on stdout, newest first, as a table with the
// columns ID, KIND, TARGET and ENDS. ENDS is the end in UTC (RFC 3339), or
// "never". With no bans in force it prints nothing on stdout and says so on
// stderr.
//
// lift removes one ban by its ID and writes one mod_log row ("ban.lift", null
// actor, the kind and the target, "via": "cli"). It writes nothing on stdout.
//
// Every message goes to stderr through slog. The exit code is 0 on success, 1
// on an ID with no ban in force or any failure, and 2 on a usage error. A
// failure changes nothing.
func banCommand(ctx context.Context, args []string) error {
	log := slog.New(slog.NewTextHandler(stderr, nil))
	var (
		sub string
		id  int64
	)
	switch {
	case len(args) == 1 && args[0] == "list":
		sub = "list"
	case len(args) == 2 && args[0] == "lift":
		sub = "lift"
		n, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || n < 1 {
			fmt.Fprintf(stderr, "gobb ban lift: the ID must be a whole number above 0, not %q\n%s\n", args[1], banUsage)
			return errUsage
		}
		id = n
	default:
		fmt.Fprintln(stderr, banUsage)
		return errUsage
	}
	if err := execBan(ctx, log, sub, id); err != nil {
		if errors.Is(err, errReported) {
			return err
		}
		log.Error("ban "+sub+" failed", "err", err)
		return errReported
	}
	return nil
}

// execBan opens the database and runs the subcommand sub; id is the ban of lift.
func execBan(ctx context.Context, log *slog.Logger, sub string, id int64) error {
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	now := time.Now()
	if sub == "list" {
		bans, err := board.AllBans(ctx, pool, now)
		if err != nil {
			return err
		}
		if len(bans) == 0 {
			log.Info("no bans in force")
			return nil
		}
		return printBans(bans)
	}
	kind, target, err := board.LiftBanCLI(ctx, pool, id, now)
	if errors.Is(err, board.ErrNotFound) {
		log.Error(fmt.Sprintf("no ban in force with ID %d (it does not exist, has ended or is lifted already); nothing changed", id))
		return errReported
	}
	if err != nil {
		return err
	}
	log.Info("ban lifted", "ban", id, "kind", kind, "target", target)
	return nil
}

// printBans writes the table of list to stdout.
func printBans(bans []board.BanEntry) error {
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tKIND\tTARGET\tENDS")
	for _, b := range bans {
		ends := "never"
		if b.EndsAt != nil {
			ends = b.EndsAt.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", b.ID, b.Kind, b.Target(), ends)
	}
	return w.Flush()
}
