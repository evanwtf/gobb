package main

import (
	"io"
	netmail "net/mail"
	"strconv"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/internal/mail/mailtest"
	"mime/quotedprintable"
)

const (
	mailPassword = "s3cr3t-pw"
	mailAddress  = "someone@example.org"
)

// mailEnvFor points the GOBB_SMTP_* variables at srv, and makes gobb trust its
// certificate. It clears every other variable that FromEnv reads.
func mailEnvFor(t *testing.T, srv *mailtest.Server, mode string) {
	t.Helper()
	for _, k := range []string{"GOBB_DSN", "GOBB_BASE_PATH", "GOBB_COOKIE_SECURE"} {
		t.Setenv(k, "")
	}
	t.Setenv("GOBB_SMTP_HOST", "127.0.0.1")
	t.Setenv("GOBB_SMTP_PORT", strconv.Itoa(srv.Port()))
	t.Setenv("GOBB_SMTP_TLS", mode)
	t.Setenv("GOBB_SMTP_USER", "board")
	t.Setenv("GOBB_SMTP_PASSWORD", mailPassword)
	t.Setenv("GOBB_MAIL_FROM", "board@example.org")
	t.Setenv("GOBB_PUBLIC_URL", "https://board.example.org/")
	old := mailRoots
	mailRoots = srv.CertPool()
	t.Cleanup(func() { mailRoots = old })
}

func TestMailTestCommand(t *testing.T) {
	for _, mode := range []string{"starttls", "implicit"} {
		t.Run(mode, func(t *testing.T) {
			srv := mailtest.Start(t, mailtest.Options{TLS: mode, User: "board", Password: mailPassword})
			mailEnvFor(t, srv, mode)
			code, out, errOut := runUser(t, "mail", "test", mailAddress)
			if code != 0 {
				t.Fatalf("exit = %d, want 0; stderr:\n%s", code, errOut)
			}
			if out != "" {
				t.Errorf("stdout = %q, want nothing", out)
			}
			for _, bad := range []string{mailPassword, mailAddress} {
				if strings.Contains(errOut, bad) {
					t.Errorf("stderr holds %q:\n%s", bad, errOut)
				}
			}
			msgs := srv.Messages()
			if len(msgs) != 1 {
				t.Fatalf("%d messages, want 1", len(msgs))
			}
			if msgs[0].To != mailAddress || msgs[0].From != "board@example.org" {
				t.Errorf("From = %q, To = %q", msgs[0].From, msgs[0].To)
			}
			m, err := netmail.ReadMessage(strings.NewReader(msgs[0].Data))
			if err != nil {
				t.Fatal(err)
			}
			if got := m.Header.Get("Subject"); got != "Test email from gobb" {
				t.Errorf("Subject = %q", got)
			}
			body, err := io.ReadAll(quotedprintable.NewReader(m.Body))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(body), "This is a test email from gobb ") || !strings.Contains(string(body), ". Email works.") {
				t.Errorf("body = %q", body)
			}
		})
	}
}

func TestMailTestCommandReportsAFailure(t *testing.T) {
	srv := mailtest.Start(t, mailtest.Options{User: "board", Password: mailPassword, Reply: map[string]int{"RCPT": 550}})
	mailEnvFor(t, srv, "starttls")
	code, out, errOut := runUser(t, "mail", "test", mailAddress)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !strings.Contains(errOut, "smtp 550") {
		t.Errorf("stderr lacks the summary:\n%s", errOut)
	}
	for _, bad := range []string{mailPassword, mailAddress} {
		if strings.Contains(errOut, bad) {
			t.Errorf("stderr holds %q:\n%s", bad, errOut)
		}
	}
}

func TestMailTestCommandNeedsConfig(t *testing.T) {
	for _, k := range []string{"GOBB_DSN", "GOBB_SMTP_HOST", "GOBB_SMTP_PORT", "GOBB_SMTP_TLS", "GOBB_SMTP_USER",
		"GOBB_SMTP_PASSWORD", "GOBB_MAIL_FROM", "GOBB_PUBLIC_URL"} {
		t.Setenv(k, "")
	}
	code, _, errOut := runUser(t, "mail", "test", mailAddress)
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut, "email is not configured: GOBB_SMTP_HOST is unset") {
		t.Errorf("stderr = %q", errOut)
	}
	if strings.Contains(errOut, mailAddress) {
		t.Errorf("stderr holds the address: %s", errOut)
	}
}

func TestMailTestCommandUsage(t *testing.T) {
	const usage = "usage: gobb mail test <address>"
	for _, args := range [][]string{
		{"mail"},
		{"mail", "test"},
		{"mail", "test", "a@example.org", "b@example.org"},
		{"mail", "send", "a@example.org"},
	} {
		code, _, errOut := runUser(t, args...)
		if code != 2 {
			t.Errorf("%v: exit = %d, want 2", args, code)
		}
		if !strings.Contains(errOut, usage) {
			t.Errorf("%v: stderr = %q, want the usage line", args, errOut)
		}
	}
}

// TestMailTestCommandKeepsServerTextOut: the server answers RCPT with a malformed
// line that holds the address in upper case. Stderr holds none of it, only the
// summary and the stage.
func TestMailTestCommandKeepsServerTextOut(t *testing.T) {
	srv := mailtest.Start(t, mailtest.Options{
		User: "board", Password: mailPassword,
		Raw: map[string]string{"RCPT": "XYZ " + strings.ToUpper(mailAddress)},
	})
	mailEnvFor(t, srv, "starttls")
	code, out, errOut := runUser(t, "mail", "test", mailAddress)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	for _, bad := range []string{mailPassword, mailAddress, strings.ToUpper(mailAddress), "EXAMPLE.ORG", "XYZ"} {
		if strings.Contains(errOut, bad) {
			t.Errorf("stderr holds %q:\n%s", bad, errOut)
		}
	}
	if !strings.Contains(errOut, "reason=network") || !strings.Contains(errOut, "stage=RCPT") {
		t.Errorf("stderr lacks the reason and the stage:\n%s", errOut)
	}
}
