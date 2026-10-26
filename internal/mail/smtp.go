package mail

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/textproto"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/evanwtf/gobb/internal/config"
)

// sendTimeout bounds the dial, and the whole send.
const sendTimeout = 30 * time.Second

// Sender sends one message.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// SMTP sends messages to one SMTP server, over TLS only: from the first byte
// ("implicit") or after STARTTLS ("starttls"). It sends no AUTH before TLS runs,
// and it sends nothing but QUIT to a server that does not offer STARTTLS.
type SMTP struct {
	// RootCAs, when not nil, replaces the system roots. The tests use it.
	RootCAs *x509.CertPool

	c config.Mail
}

var _ Sender = (*SMTP)(nil)

// NewSMTP returns a sender for the server in c.
func NewSMTP(c config.Mail) *SMTP { return &SMTP{c: c} }

// Error is a failed send. Its text holds the stage and a reason, never the
// password, the recipient or any text from the server.
type Error struct {
	// Stage names the step that failed, such as "RCPT".
	Stage string
	// Code and Enhanced are the SMTP reply code and enhanced status code of a
	// refusal. Code is 0 when the failure was not a reply.
	Code     int
	Enhanced string

	kind   string // "tls" or "no starttls", or empty
	reason string // the text of a failure that was not a reply
	cause  error
}

func (e *Error) Error() string {
	switch {
	case e.Code != 0:
		return "mail: " + e.Stage + ": " + replySummary(e.Code, e.Enhanced)
	case e.kind == "no starttls":
		return "mail: " + e.Stage + ": the server does not offer STARTTLS"
	default:
		return "mail: " + e.Stage + ": " + e.reason
	}
}

func (e *Error) Unwrap() error { return e.cause }

// enhancedRE matches an enhanced status code at the start of a reply text.
var enhancedRE = regexp.MustCompile(`^([245])\.\d{1,3}\.\d{1,3}(\s|$)`)

func replySummary(code int, enhanced string) string {
	s := "smtp " + strconv.Itoa(code)
	if enhanced != "" {
		s += " " + enhanced
	}
	return s
}

// reply returns the code and enhanced status code of an SMTP reply in err.
func reply(err error) (code int, enhanced string, ok bool) {
	var e *Error
	if errors.As(err, &e) && e.Code != 0 {
		return e.Code, e.Enhanced, true
	}
	var te *textproto.Error
	if errors.As(err, &te) {
		if m := enhancedRE.FindStringSubmatch(te.Msg); m != nil {
			enhanced = strings.TrimSpace(m[0])
		}
		return te.Code, enhanced, true
	}
	return 0, "", false
}

// Permanent reports whether err holds a 5xx SMTP reply, which a retry cannot fix.
func Permanent(err error) bool {
	code, _, ok := reply(err)
	return ok && code >= 500 && code < 600
}

// Summary describes err in a few words that are safe to store and to log:
// "smtp 550 5.1.1" (the code, and the enhanced status code when the reply has
// one), "timeout", "tls", "no starttls" or "network". It holds no text of the
// server's reply. It returns "" for nil.
func Summary(err error) string {
	if err == nil {
		return ""
	}
	if code, enh, ok := reply(err); ok {
		return replySummary(code, enh)
	}
	var te net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &te) && te.Timeout()) {
		return "timeout"
	}
	var e *Error
	if errors.As(err, &e) && e.kind != "" {
		return e.kind
	}
	return "network"
}

// send holds the state of one Send call.
type send struct {
	ctx      context.Context
	redactor *strings.Replacer
}

// fail turns err from stage into an *Error. A reply keeps its codes and loses
// its text. The text of any other failure loses the password and the recipient.
func (s *send) fail(stage, kind string, err error) error {
	e := &Error{Stage: stage, cause: err}
	if code, enh, ok := reply(err); ok {
		e.Code, e.Enhanced, e.cause = code, enh, nil
		return e
	}
	// A malformed reply carries the raw line of the server in its text, and that
	// line can hold the recipient in any case or encoding. Keep none of it.
	var pe textproto.ProtocolError
	if errors.As(err, &pe) {
		e.cause, e.reason = nil, "malformed reply"
		return e
	}
	// A deadline or a cancel closes the connection, so the error that the
	// connection returns is not the cause.
	if ctxErr := s.ctx.Err(); ctxErr != nil {
		e.cause = fmt.Errorf("%w: %w", ctxErr, err)
		e.reason = ctxErr.Error()
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			e.reason = "timeout"
		}
		return e
	}
	var te net.Error
	if kind != "" && !(errors.As(err, &te) && te.Timeout()) {
		e.kind = kind
	}
	e.reason = s.redactor.Replace(err.Error())
	return e
}

// Send builds m and sends it. It gives up after 30 s, or when ctx ends, if that
// comes first.
func (s *SMTP) Send(ctx context.Context, m Message) error {
	c := s.c
	body, err := Build(c.From, m, time.Now())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	st := &send{ctx: ctx, redactor: redactor(c.Password, m.To)}

	tlsConf := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12, RootCAs: s.RootCAs}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	dialer := &net.Dialer{Timeout: sendTimeout}

	var conn net.Conn
	if c.TLS == "implicit" {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConf}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		// A dial that reached the server and failed the handshake is a TLS
		// failure; one that never connected is a network failure.
		kind := ""
		if c.TLS == "implicit" && !isDialError(err) {
			kind = "tls"
		}
		return st.fail("connect", kind, err)
	}
	// A deadline on the connection ends a read that waits on a silent server.
	// The close ends one when ctx is canceled.
	if d, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(d)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	client, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		_ = conn.Close()
		return st.fail("greeting", "", err)
	}
	defer func() { _ = client.Close() }()

	if err := client.Hello(helloName(c.PublicURL)); err != nil {
		return st.fail("EHLO", "", err)
	}
	if c.TLS != "implicit" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			_ = client.Quit()
			return &Error{Stage: "STARTTLS", kind: "no starttls"}
		}
		if err := client.StartTLS(tlsConf); err != nil {
			return st.fail("STARTTLS", "tls", err)
		}
	}
	if c.User != "" {
		if err := client.Auth(smtp.PlainAuth("", c.User, c.Password, c.Host)); err != nil {
			return st.fail("AUTH", "", err)
		}
	}
	if err := client.Mail(c.From); err != nil {
		return st.fail("MAIL", "", err)
	}
	if err := client.Rcpt(m.To); err != nil {
		return st.fail("RCPT", "", err)
	}
	w, err := client.Data()
	if err != nil {
		return st.fail("DATA", "", err)
	}
	if _, err := w.Write(body); err != nil {
		return st.fail("DATA", "", err)
	}
	if err := w.Close(); err != nil {
		return st.fail("DATA", "", err)
	}
	// The server has accepted the message. A failed QUIT must not make the
	// caller send it again.
	_ = client.Quit()
	return nil
}

// isDialError reports whether err came from the TCP connect, before any TLS.
func isDialError(err error) bool {
	var oe *net.OpError
	return errors.As(err, &oe) && oe.Op == "dial"
}

// helloName returns the host of the public URL, for EHLO.
func helloName(publicURL string) string {
	if u, err := url.Parse(publicURL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "localhost"
}

// redactor returns a Replacer that hides each non-empty secret.
func redactor(secrets ...string) *strings.Replacer {
	var pairs []string
	for _, s := range secrets {
		if s != "" {
			pairs = append(pairs, s, "[hidden]")
		}
	}
	return strings.NewReplacer(pairs...)
}
