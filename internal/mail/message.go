// Package mail builds email messages and sends them over SMTP with TLS.
//
// An error from this package never holds the SMTP password, the recipient
// address or a message body. It holds no text of a server's reply either, since
// that text can name the recipient.
package mail

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	netmail "net/mail"
	"strings"
	"time"
)

// Message is one email to one recipient. Body is plain text.
type Message struct {
	To, Subject, Body string
}

// plainAddress parses s as exactly one ASCII address, with no display name and
// no angle brackets. It returns the part after the last "@".
func plainAddress(s string) (domain string, ok bool) {
	a, err := netmail.ParseAddress(s)
	if err != nil || a.Name != "" || a.Address != s {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return "", false
		}
	}
	return s[strings.LastIndexByte(s, '@')+1:], true
}

// Check reports an error when m cannot be sent safely: To is not one plain
// address, or To or Subject holds a CR or an LF. The errors do not hold the
// address.
func Check(m Message) error {
	if strings.ContainsAny(m.To, "\r\n") {
		return errors.New("mail: the recipient holds a line break")
	}
	if strings.ContainsAny(m.Subject, "\r\n") {
		return errors.New("mail: the subject holds a line break")
	}
	if _, ok := plainAddress(m.To); !ok {
		return errors.New("mail: the recipient is not one plain address")
	}
	return nil
}

// Build returns the whole message, headers and body, as bytes. Lines end in CRLF
// and the body is quoted-printable, so no line is over 78 octets. It runs Check
// first. from is the plain sender address; its domain is the domain of the
// Message-ID.
func Build(from string, m Message, now time.Time) ([]byte, error) {
	if err := Check(m); err != nil {
		return nil, err
	}
	domain, ok := plainAddress(from)
	if !ok || strings.ContainsAny(from, "\r\n") {
		return nil, errors.New("mail: the sender is not one plain address")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, fmt.Errorf("mail: message id: %w", err)
	}

	var b bytes.Buffer
	header := func(name, value string) {
		sep := " "
		if strings.HasPrefix(value, "\r\n") {
			sep = "" // a folded value starts on its own line
		}
		b.WriteString(name + ":" + sep + value + "\r\n")
	}
	header("From", from)
	header("To", m.To)
	header("Subject", encodeSubject(m.Subject))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(id[:])+"@"+domain+">")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	header("Auto-Submitted", "auto-generated")
	b.WriteString("\r\n")

	w := quotedprintable.NewWriter(&b)
	if _, err := w.Write([]byte(m.Body)); err != nil {
		return nil, fmt.Errorf("mail: encode body: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("mail: encode body: %w", err)
	}
	return b.Bytes(), nil
}

// encodeSubject returns s as a header value. Text that is not plain ASCII becomes
// RFC 2047 encoded words. The encoder joins several words with spaces; they go on
// their own lines here, so that no line is over 78 octets.
func encodeSubject(s string) string {
	enc := mime.QEncoding.Encode("utf-8", s)
	if !strings.Contains(enc, "?= =?") {
		return enc
	}
	return "\r\n " + strings.ReplaceAll(enc, "?= =?", "?=\r\n =?")
}
