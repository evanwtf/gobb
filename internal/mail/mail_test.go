package mail_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	netmail "net/mail"
	"net/textproto"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/config"
	"github.com/evanwtf/gobb/internal/mail"
	"github.com/evanwtf/gobb/internal/mail/mailtest"
)

const (
	secret    = "s3cr3t-pw"
	recipient = "someone@example.org"
)

var now = time.Date(2026, 10, 25, 12, 30, 0, 0, time.UTC)

// sender starts a fake server with o and returns it with a sender that points
// at it and trusts its certificate. user and password are for the client; the
// server checks them against o.
func sender(t *testing.T, o mailtest.Options, user, password string) (*mailtest.Server, *mail.SMTP) {
	t.Helper()
	srv := mailtest.Start(t, o)
	mode := o.TLS
	if mode == "" {
		mode = "starttls"
	}
	s := mail.NewSMTP(config.Mail{
		Host:      "127.0.0.1",
		Port:      srv.Port(),
		TLS:       mode,
		User:      user,
		Password:  password,
		From:      "board@example.org",
		PublicURL: "https://board.example.org/",
	})
	s.RootCAs = srv.CertPool()
	return srv, s
}

func send(s *mail.SMTP, m mail.Message) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.Send(ctx, m)
}

func TestBuild(t *testing.T) {
	m := mail.Message{To: recipient, Subject: "Reset your password", Body: "Hello.\r\nBye.\r\n"}
	raw, err := mail.Build("board@example.org", m, now)
	if err != nil {
		t.Fatal(err)
	}
	if n, crlf := strings.Count(string(raw), "\n"), strings.Count(string(raw), "\r\n"); n != crlf {
		t.Errorf("%d line feeds but %d CRLF: a line ends in a bare LF", n, crlf)
	}
	msg, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	h := msg.Header
	for k, want := range map[string]string{
		"From":                      "board@example.org",
		"To":                        recipient,
		"Subject":                   "Reset your password",
		"Mime-Version":              "1.0",
		"Content-Type":              "text/plain; charset=utf-8",
		"Content-Transfer-Encoding": "quoted-printable",
		"Auto-Submitted":            "auto-generated",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if d, err := h.Date(); err != nil || !d.Equal(now) {
		t.Errorf("Date = %v, %v; want %v", d, err, now)
	}
	if id := h.Get("Message-Id"); !regexp.MustCompile(`^<[0-9a-f]{32}@example\.org>$`).MatchString(id) {
		t.Errorf("Message-ID = %q", id)
	}
	other, err := mail.Build("board@example.org", m, now)
	if err != nil {
		t.Fatal(err)
	}
	msg2, _ := netmail.ReadMessage(bytes.NewReader(other))
	if msg2.Header.Get("Message-Id") == h.Get("Message-Id") {
		t.Error("two messages share a Message-ID")
	}
}

func TestBuildEncodesTheSubject(t *testing.T) {
	raw, err := mail.Build("board@example.org", mail.Message{To: recipient, Subject: "Réinitialiser", Body: "x\r\n"}, now)
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := strings.Cut(string(raw), "\r\n\r\n")
	if !strings.Contains(strings.ToLower(head), "subject: =?utf-8?q?r=c3=a9initialiser?=") {
		t.Errorf("the subject is not Q-encoded:\n%s", head)
	}
	msg, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	got, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || got != "Réinitialiser" {
		t.Errorf("decoded subject = %q, %v", got, err)
	}
	// A long subject with non-ASCII text keeps every header line short.
	long := strings.Repeat("Réinitialiser ", 12)
	raw, err = mail.Build("board@example.org", mail.Message{To: recipient, Subject: long, Body: "x\r\n"}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 78 {
			t.Errorf("a line of %d octets: %q", len(line), line)
		}
	}
	msg, err = netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	got, err = new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || got != long {
		t.Errorf("decoded long subject = %q, %v", got, err)
	}
}

func TestBuildRefusesInjection(t *testing.T) {
	for name, m := range map[string]mail.Message{
		"a Bcc line in To":      {To: recipient + "\r\nBcc: b@example.org", Subject: "s", Body: "b"},
		"an LF in To":           {To: recipient + "\nBcc: b@example.org", Subject: "s", Body: "b"},
		"a CR in To":            {To: recipient + "\rx", Subject: "s", Body: "b"},
		"an LF in the subject":  {To: recipient, Subject: "a\nBcc: b@example.org", Body: "b"},
		"a CRLF in the subject": {To: recipient, Subject: "a\r\nb", Body: "b"},
		"two addresses":         {To: "a@example.org, b@example.org", Subject: "s", Body: "b"},
		"a display name":        {To: "Someone <someone@example.org>", Subject: "s", Body: "b"},
		"angle brackets":        {To: "<someone@example.org>", Subject: "s", Body: "b"},
		"no address":            {To: "", Subject: "s", Body: "b"},
		"no domain":             {To: "someone", Subject: "s", Body: "b"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := mail.Check(m); err == nil {
				t.Error("Check accepted it")
			}
			raw, err := mail.Build("board@example.org", m, now)
			if err == nil {
				t.Errorf("Build accepted it:\n%s", raw)
			} else if strings.Contains(err.Error(), "example.org") {
				t.Errorf("the error holds the address: %v", err)
			}
		})
	}
	if err := mail.Check(mail.Message{To: recipient, Subject: "Réinitialiser", Body: "b"}); err != nil {
		t.Errorf("Check refused a good message: %v", err)
	}
	if _, err := mail.Build("Board <board@example.org>", mail.Message{To: recipient, Subject: "s", Body: "b"}, now); err == nil {
		t.Error("Build accepted a sender with a display name")
	}
}

func TestBuildShortLines(t *testing.T) {
	body := strings.Repeat("a", 2000) + "\r\n" + strings.Repeat("é", 300) + "\r\n"
	raw, err := mail.Build("board@example.org", mail.Message{To: recipient, Subject: "s", Body: body}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 78 {
			t.Errorf("a line of %d octets", len(line))
		}
	}
	_, rest, _ := strings.Cut(string(raw), "\r\n\r\n")
	got, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(rest)))
	if err != nil || string(got) != body {
		t.Errorf("the body does not decode to the original: %v", err)
	}
}

// decode returns the body of a received message.
func decode(t *testing.T, data string) string {
	t.Helper()
	msg, err := netmail.ReadMessage(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSendDotLine(t *testing.T) {
	srv, s := sender(t, mailtest.Options{}, "", "")
	body := "before\r\n.\r\n..\r\n.after\r\nlast\r\n"
	if err := send(s, mail.Message{To: recipient, Subject: "dots", Body: body}); err != nil {
		t.Fatal(err)
	}
	got := srv.Messages()
	if len(got) != 1 {
		t.Fatalf("%d messages, want 1", len(got))
	}
	if b := decode(t, got[0].Data); b != body {
		t.Errorf("body = %q, want %q", b, body)
	}
}

func hasPrefix(log []string, prefix string) bool {
	for _, l := range log {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

func index(log []string, prefix string) int {
	for i, l := range log {
		if strings.HasPrefix(l, prefix) {
			return i
		}
	}
	return -1
}

func TestSendStartTLS(t *testing.T) {
	srv, s := sender(t, mailtest.Options{User: "board", Password: secret}, "board", secret)
	if err := send(s, mail.Message{To: recipient, Subject: "hi", Body: "hello\r\n"}); err != nil {
		t.Fatal(err)
	}
	log := srv.Log()
	st, au := index(log, "STARTTLS"), index(log, "AUTH")
	if st < 0 || au < 0 || st > au {
		t.Fatalf("want STARTTLS before AUTH, log:\n%s", strings.Join(log, "\n"))
	}
	for _, cmd := range []string{"AUTH", "MAIL", "RCPT", "DATA"} {
		i := index(log, cmd)
		if i < 0 {
			t.Errorf("no %s in the log", cmd)
			continue
		}
		if !strings.HasSuffix(log[i], " tls=on") {
			t.Errorf("%s was not under TLS: %q", cmd, log[i])
		}
	}
	if !hasPrefix(log, "AUTH PLAIN [hidden]") {
		t.Errorf("want AUTH PLAIN [hidden] in the log:\n%s", strings.Join(log, "\n"))
	}
	if strings.Contains(strings.Join(log, "\n"), secret) {
		t.Error("the log holds the password")
	}
	// The client says the host of the public URL in EHLO.
	if !hasPrefix(log, "EHLO board.example.org") {
		t.Errorf("want EHLO board.example.org, log:\n%s", strings.Join(log, "\n"))
	}
	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("%d messages, want 1", len(msgs))
	}
	if msgs[0].From != "board@example.org" || msgs[0].To != recipient {
		t.Errorf("From = %q, To = %q", msgs[0].From, msgs[0].To)
	}
	if b := decode(t, msgs[0].Data); b != "hello\r\n" {
		t.Errorf("body = %q", b)
	}
}

func TestSendImplicitTLS(t *testing.T) {
	srv, s := sender(t, mailtest.Options{TLS: "implicit", User: "board", Password: secret}, "board", secret)
	if err := send(s, mail.Message{To: recipient, Subject: "hi", Body: "hello\r\n"}); err != nil {
		t.Fatal(err)
	}
	if n := len(srv.Messages()); n != 1 {
		t.Fatalf("%d messages, want 1", n)
	}
	log := srv.Log()
	if hasPrefix(log, "STARTTLS") {
		t.Errorf("implicit TLS sent STARTTLS:\n%s", strings.Join(log, "\n"))
	}
	for _, l := range log {
		if !strings.HasSuffix(l, " tls=on") {
			t.Errorf("a command was not under TLS: %q", l)
		}
	}
}

func TestSendRefusesNoStartTLS(t *testing.T) {
	srv, s := sender(t, mailtest.Options{NoStartTLS: true, User: "board", Password: secret}, "board", secret)
	err := send(s, mail.Message{To: recipient, Subject: "hi", Body: "hello\r\n"})
	if err == nil {
		t.Fatal("Send worked without STARTTLS")
	}
	if got := mail.Summary(err); got != "no starttls" {
		t.Errorf("Summary = %q, want no starttls", got)
	}
	if mail.Permanent(err) {
		t.Error("a missing STARTTLS counts as permanent")
	}
	log := srv.Log()
	for _, cmd := range []string{"AUTH", "MAIL", "RCPT", "DATA"} {
		if hasPrefix(log, cmd) {
			t.Errorf("the client sent %s:\n%s", cmd, strings.Join(log, "\n"))
		}
	}
	if len(srv.Messages()) != 0 {
		t.Error("a message arrived")
	}
}

func TestSendUntrustedCert(t *testing.T) {
	for _, mode := range []string{"starttls", "implicit"} {
		t.Run(mode, func(t *testing.T) {
			srv, s := sender(t, mailtest.Options{TLS: mode, User: "board", Password: secret}, "board", secret)
			s.RootCAs = nil // the system roots do not know the fake certificate
			err := send(s, mail.Message{To: recipient, Subject: "hi", Body: "hello\r\n"})
			if err == nil {
				t.Fatal("Send trusted a certificate that no root signed")
			}
			if got := mail.Summary(err); got != "tls" {
				t.Errorf("Summary = %q, want tls (error: %v)", got, err)
			}
			if hasPrefix(srv.Log(), "AUTH") {
				t.Error("the client sent AUTH over an untrusted connection")
			}
			if len(srv.Messages()) != 0 {
				t.Error("a message arrived")
			}
		})
	}
}

func TestSendPermanent(t *testing.T) {
	srv, s := sender(t, mailtest.Options{Reply: map[string]int{"RCPT": 550}}, "", "")
	err := send(s, mail.Message{To: recipient, Subject: "hi", Body: "hello\r\n"})
	if err == nil {
		t.Fatal("Send worked after a 550")
	}
	if !mail.Permanent(err) {
		t.Errorf("Permanent(%v) = false", err)
	}
	if got := mail.Summary(err); got != "smtp 550 5.1.1" {
		t.Errorf("Summary = %q, want smtp 550 5.1.1", got)
	}
	// The server's reply text names the address. No error text may hold it.
	if strings.Contains(err.Error(), "example.org") {
		t.Errorf("the error holds the address or a host name from the reply: %v", err)
	}
	if len(srv.Messages()) != 0 {
		t.Error("a message arrived")
	}

	_, s = sender(t, mailtest.Options{Reply: map[string]int{"RCPT": 451}}, "", "")
	err = send(s, mail.Message{To: recipient, Subject: "hi", Body: "hello\r\n"})
	if err == nil {
		t.Fatal("Send worked after a 451")
	}
	if mail.Permanent(err) {
		t.Errorf("Permanent(%v) = true for a 451", err)
	}
	if got := mail.Summary(err); !strings.HasPrefix(got, "smtp 451") {
		t.Errorf("Summary = %q, want smtp 451", got)
	}

	_, s = sender(t, mailtest.Options{Reply: map[string]int{"DATA": 554}}, "", "")
	err = send(s, mail.Message{To: recipient, Subject: "hi", Body: "hello\r\n"})
	if !mail.Permanent(err) || !strings.HasPrefix(mail.Summary(err), "smtp 554") {
		t.Errorf("DATA 554: Permanent = %v, Summary = %q", mail.Permanent(err), mail.Summary(err))
	}
}

func TestSendAuthFailureKeepsSecrets(t *testing.T) {
	_, s := sender(t, mailtest.Options{User: "board", Password: "right-pw"}, "board", secret)
	err := send(s, mail.Message{To: recipient, Subject: "hi", Body: "hello\r\n"})
	if err == nil {
		t.Fatal("Send worked with a wrong password")
	}
	if got := mail.Summary(err); !strings.HasPrefix(got, "smtp 535") {
		t.Errorf("Summary = %q, want smtp 535", got)
	}
	for _, bad := range []string{secret, recipient} {
		if strings.Contains(err.Error(), bad) {
			t.Errorf("the error holds %q: %v", bad, err)
		}
	}
}

func TestSendTimeout(t *testing.T) {
	for _, mode := range []string{"starttls", "implicit"} {
		t.Run(mode, func(t *testing.T) {
			_, s := sender(t, mailtest.Options{TLS: mode, Silent: true}, "", "")
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			start := time.Now()
			err := s.Send(ctx, mail.Message{To: recipient, Subject: "hi", Body: "hello\r\n"})
			if took := time.Since(start); took > time.Second {
				t.Errorf("Send took %v", took)
			}
			if err == nil {
				t.Fatal("Send worked against a silent server")
			}
			if got := mail.Summary(err); got != "timeout" {
				t.Errorf("Summary = %q, want timeout (error: %v)", got, err)
			}
			if mail.Permanent(err) {
				t.Error("a timeout counts as permanent")
			}
		})
	}
}

func TestSendNetworkError(t *testing.T) {
	srv, s := sender(t, mailtest.Options{}, "", "")
	srv.Close()
	err := send(s, mail.Message{To: recipient, Subject: "hi", Body: "hello\r\n"})
	if err == nil {
		t.Fatal("Send worked with no server")
	}
	if got := mail.Summary(err); got != "network" {
		t.Errorf("Summary = %q, want network (error: %v)", got, err)
	}
	if mail.Permanent(err) {
		t.Error("a network error counts as permanent")
	}
}

func TestSendRefusesABadMessageBeforeItConnects(t *testing.T) {
	srv, s := sender(t, mailtest.Options{}, "", "")
	err := send(s, mail.Message{To: recipient + "\r\nBcc: x@example.org", Subject: "hi", Body: "b"})
	if err == nil {
		t.Fatal("Send accepted a header injection")
	}
	if len(srv.Log()) != 0 {
		t.Errorf("the client connected:\n%s", strings.Join(srv.Log(), "\n"))
	}
}

func TestPermanentAndSummaryOfOtherErrors(t *testing.T) {
	if mail.Permanent(nil) || mail.Permanent(errors.New("x")) {
		t.Error("Permanent is true for a non-reply")
	}
	if !mail.Permanent(&textproto.Error{Code: 550, Msg: "5.1.1 no"}) {
		t.Error("Permanent is false for a raw 550")
	}
	if mail.Permanent(&textproto.Error{Code: 421, Msg: "go away"}) {
		t.Error("Permanent is true for a raw 421")
	}
	if got := mail.Summary(&textproto.Error{Code: 550, Msg: "5.7.1 no"}); got != "smtp 550 5.7.1" {
		t.Errorf("Summary = %q", got)
	}
	if got := mail.Summary(&textproto.Error{Code: 554, Msg: "no code here"}); got != "smtp 554" {
		t.Errorf("Summary = %q", got)
	}
	if got := mail.Summary(errors.New("boom")); got != "network" {
		t.Errorf("Summary = %q", got)
	}
	if got := mail.Summary(context.DeadlineExceeded); got != "timeout" {
		t.Errorf("Summary = %q", got)
	}
}

// TestSendMalformedReplyKeepsNoServerText: a reply that is not valid SMTP holds
// the recipient in upper case. No part of it reaches the error text.
func TestSendMalformedReplyKeepsNoServerText(t *testing.T) {
	for name, line := range map[string]string{
		"bad code":       "XYZ SOMEONE@EXAMPLE.ORG",
		"short response": "SOMEONE@EXAMPLE.ORG",
	} {
		t.Run(name, func(t *testing.T) {
			_, s := sender(t, mailtest.Options{Raw: map[string]string{"RCPT": line}}, "", "")
			err := send(s, mail.Message{To: recipient, Subject: "hi", Body: "hello\r\n"})
			if err == nil {
				t.Fatal("Send worked after a malformed reply")
			}
			if strings.Contains(strings.ToLower(err.Error()), "example.org") ||
				strings.Contains(strings.ToLower(fmt.Sprintf("%+v %#v", err, err)), "someone") {
				t.Errorf("the error holds text of the reply: %v", err)
			}
			if got := mail.Summary(err); got != "network" {
				t.Errorf("Summary = %q, want network", got)
			}
			if mail.Permanent(err) {
				t.Error("a malformed reply counts as permanent")
			}
		})
	}
}
