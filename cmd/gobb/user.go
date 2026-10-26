package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/config"
	"github.com/evanwtf/gobb/internal/db"
)

const userUsage = `usage: gobb user reset-password <name>
       gobb user ban <name> [-reason TEXT]
       gobb user unban <name>
       gobb user activate <name>
       gobb user deactivate <name>`

// user runs a member command: reset-password, ban, unban, activate or deactivate.
// The administrator runs it on the host that holds the database, with GOBB_DSN set.
//
// Output has two channels. Stdout carries one thing only: the new password of
// reset-password, as one line. Every message goes to stderr through slog, and no
// message, error or log row holds the password. The exit code is 0 on success,
// 1 on an unknown name or any failure, and 2 on a usage error. A failure changes
// nothing, with one exception: when reset-password cannot write the password to
// stdout, the reset has committed and stays. The command exits 1, and the
// administrator runs it again to get a new password. Every message names the
// stored username and the ID of the member that the typed name reached.
func user(ctx context.Context, args []string) error {
	log := slog.New(slog.NewTextHandler(stderr, nil))
	if len(args) == 0 {
		fmt.Fprintln(stderr, userUsage)
		return errUsage
	}
	sub := args[0]
	switch sub {
	case "reset-password", "ban", "unban", "activate", "deactivate":
	default:
		fmt.Fprintf(stderr, "gobb user: unknown command %q\n%s\n", sub, userUsage)
		return errUsage
	}
	name, reason, err := parseUserArgs(sub, args[1:])
	if err != nil {
		return err
	}
	if err := execUser(ctx, log, sub, name, reason); err != nil {
		if errors.Is(err, errUsage) || errors.Is(err, errReported) {
			return err
		}
		log.Error("user "+sub+" failed", "err", err)
		return errReported
	}
	return nil
}

// parseUserArgs reads the arguments after the subcommand: one name, and for ban
// the flag -reason, before or after the name. A wrong call prints the usage and
// returns errUsage.
func parseUserArgs(sub string, args []string) (name, reason string, err error) {
	fs := flag.NewFlagSet("gobb user "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, userUsage) }
	if sub == "ban" {
		fs.StringVar(&reason, "reason", "", "the reason, for staff")
	}
	if err := fs.Parse(args); err != nil {
		// The flag package has printed the message and the usage.
		return "", "", errUsage
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return "", "", errUsage
	}
	name = fs.Arg(0)
	// The flag package stops at the first argument that is not a flag, so a flag
	// after the name needs a second pass.
	if rest := fs.Args()[1:]; len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return "", "", errUsage
		}
		if fs.NArg() > 0 {
			fs.Usage()
			return "", "", errUsage
		}
	}
	if strings.TrimSpace(name) == "" {
		fs.Usage()
		return "", "", errUsage
	}
	return name, reason, nil
}

// execUser opens the database and runs the subcommand sub for the member name.
func execUser(ctx context.Context, log *slog.Logger, sub, name, reason string) error {
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	id, matched, err := board.UserByName(ctx, pool, name)
	if errors.Is(err, board.ErrNotFound) {
		log.Error(fmt.Sprintf("no member named %s", name))
		return errReported
	}
	if err != nil {
		return err
	}
	// The name that was typed is folded, so the member it reached may not be the one
	// in mind: every message names the stored username and the ID.
	log = log.With("username", matched, "user", id)
	now := time.Now()
	switch sub {
	case "reset-password":
		pw, err := board.ResetPassword(ctx, pool, id, now)
		if err != nil {
			return err
		}
		log.Info("password reset; the member's sessions are ended")
		// The one line on stdout. If the write fails, the password is changed and
		// lost: say so, and the administrator runs the command again.
		if _, err := fmt.Fprintln(stdout, pw); err != nil {
			return fmt.Errorf("the password was changed, but it could not be printed (run the command again): %w", err)
		}
	case "ban":
		err := board.BanUser(ctx, pool, id, reason, now)
		if errors.Is(err, board.ErrAlreadyBanned) {
			log.Info("the member is already banned; nothing changed")
			return nil
		}
		if err != nil {
			return err
		}
		log.Info("member banned; their sessions are ended")
	case "unban":
		err := board.UnbanUser(ctx, pool, id, now)
		if errors.Is(err, board.ErrNotBanned) {
			log.Info("the member has no ban; nothing changed")
			return nil
		}
		if err != nil {
			return err
		}
		log.Info("member unbanned")
	case "activate":
		err := board.ActivateUser(ctx, pool, id, now)
		if errors.Is(err, board.ErrAlreadyActive) {
			log.Info("the account is already active; nothing changed")
			return nil
		}
		if err != nil {
			return err
		}
		log.Info("account activated; the member can sign in")
	case "deactivate":
		err := board.DeactivateUser(ctx, pool, id, now)
		if errors.Is(err, board.ErrAlreadyInactive) {
			log.Info("the account is already inactive; nothing changed")
			return nil
		}
		if err != nil {
			return err
		}
		log.Info("account deactivated; their sessions are ended")
	}
	return nil
}
