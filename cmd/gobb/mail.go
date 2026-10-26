package main

import (
	"context"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"

	"github.com/evanwtf/gobb/internal/config"
	"github.com/evanwtf/gobb/internal/mail"
	"github.com/evanwtf/gobb/internal/version"
)

const mailUsage = "usage: gobb mail test <address>"

// mailRoots, when not nil, replaces the system roots that gobb mail test trusts
// for the TLS connection to the SMTP server. It is nil in production; the tests
// set it to the certificate of their fake server.
var mailRoots *x509.CertPool

// mailCommand runs an email command. The only one is "test": it sends one test
// email to the address at once, through the SMTP settings and not through the
// queue, so that the administrator sees at once whether email works.
//
// Like the user commands, it keeps stdout empty and sends every message to
// stderr through slog. No message holds the SMTP password or the address. The
// exit code is 0 when the server accepted the message, 1 on a missing setting or
// a failed send, and 2 on a usage error. It reads the email settings only, so it
// needs no GOBB_DSN.
func mailCommand(ctx context.Context, args []string) error {
	if len(args) != 2 || args[0] != "test" {
		fmt.Fprintln(stderr, mailUsage)
		return errUsage
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	cfg, err := config.MailFromEnv(os.Getenv)
	if err != nil {
		log.Error("mail test failed", "err", err)
		return errReported
	}
	if cfg == nil {
		log.Error("email is not configured: GOBB_SMTP_HOST is unset")
		return errReported
	}
	sender := mail.NewSMTP(*cfg)
	sender.RootCAs = mailRoots
	err = sender.Send(ctx, mail.Message{
		To:      args[1],
		Subject: "Test email from gobb",
		Body:    "This is a test email from gobb " + version.Version + ". Email works.\n",
	})
	if err != nil {
		log.Error("test email failed", "reason", mail.Summary(err), "err", err)
		return errReported
	}
	log.Info("test email sent", "server", cfg.Host, "port", cfg.Port)
	return nil
}
