// Package mailtest is a fake SMTP server for tests. It listens on 127.0.0.1 and
// speaks TLS with a certificate that it makes in memory for that address. It
// records every command it gets, and every message it accepts.
package mailtest

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// Options sets how the server behaves.
type Options struct {
	// TLS is "starttls" (the default) or "implicit".
	TLS string
	// NoStartTLS leaves STARTTLS out of the EHLO reply. It has no effect with
	// implicit TLS.
	NoStartTLS bool
	// User and Password are the credentials that AUTH PLAIN accepts. When User
	// is empty, the server does not offer AUTH.
	User, Password string
	// Reply maps a command (MAIL, RCPT or DATA) to the code of its reply. A
	// code from 400 refuses the command: for DATA the server sends it in place
	// of 354 and reads no data. A command with no entry gets the usual reply.
	Reply map[string]int
	// Silent accepts a connection and never answers.
	Silent bool
}

// Received is one message that the server accepted.
type Received struct {
	// From is the envelope sender, and To the envelope recipients, joined with
	// commas. Data is the message as it came on the wire, after the removal of
	// the dot-stuffing, with its CRLF line ends.
	From, To, Data string
}

// Server is a running fake SMTP server.
type Server struct {
	o    Options
	ln   net.Listener
	cert tls.Certificate
	pool *x509.CertPool

	mu    sync.Mutex
	msgs  []Received
	log   []string
	conns map[net.Conn]struct{}
	wg    sync.WaitGroup
	once  sync.Once
}

// Start starts a server and stops it when the test ends.
func Start(t testing.TB, o Options) *Server {
	t.Helper()
	if o.TLS == "" {
		o.TLS = "starttls"
	}
	if o.TLS != "starttls" && o.TLS != "implicit" {
		t.Fatalf("mailtest: TLS = %q, want starttls or implicit", o.TLS)
	}
	cert, pool, err := certificate()
	if err != nil {
		t.Fatalf("mailtest: certificate: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mailtest: listen: %v", err)
	}
	s := &Server{o: o, ln: ln, cert: cert, pool: pool, conns: map[net.Conn]struct{}{}}
	t.Cleanup(s.Close)
	s.wg.Add(1)
	go s.accept()
	return s
}

// certificate makes a self-signed certificate for 127.0.0.1, and a pool that
// trusts it.
func certificate() (tls.Certificate, *x509.CertPool, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mailtest"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool, nil
}

// Port returns the port the server listens on.
func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// CertPool returns a pool that trusts the certificate of the server.
func (s *Server) CertPool() *x509.CertPool { return s.pool }

// Messages returns the messages that the server accepted, oldest first.
func (s *Server) Messages() []Received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Received(nil), s.msgs...)
}

// Log returns each command that the server got, oldest first, with " tls=on" or
// " tls=off" at the end for the state of the connection when the command came.
// A command that carries a secret shows as "AUTH PLAIN [hidden]".
func (s *Server) Log() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}

// Close stops the server and ends its connections. Start arranges a Close at the
// end of the test; a test may call it earlier.
func (s *Server) Close() {
	s.once.Do(func() {
		_ = s.ln.Close()
		s.mu.Lock()
		for c := range s.conns {
			_ = c.Close()
		}
		s.mu.Unlock()
	})
	s.wg.Wait()
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				_ = c.Close()
				s.mu.Lock()
				delete(s.conns, c)
				s.mu.Unlock()
			}()
			s.handle(c)
		}()
	}
}

func (s *Server) record(entry string, tlsOn bool) {
	state := " tls=off"
	if tlsOn {
		state = " tls=on"
	}
	s.mu.Lock()
	s.log = append(s.log, entry+state)
	s.mu.Unlock()
}

// session is the state of one connection.
type session struct {
	s     *Server
	conn  net.Conn
	br    *bufio.Reader
	tlsOn bool
}

func (ss *session) say(format string, args ...any) error {
	_, err := fmt.Fprintf(ss.conn, format+"\r\n", args...)
	return err
}

// readLine returns one line without its line end.
func (ss *session) readLine() (string, error) {
	line, err := ss.br.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

// readData reads a message up to the line that holds only ".", and removes the
// dot-stuffing. It keeps the CRLF line ends.
func (ss *session) readData() (string, error) {
	var b strings.Builder
	for {
		line, err := ss.br.ReadString('\n')
		if err != nil {
			return "", err
		}
		if line == ".\r\n" || line == ".\n" {
			return b.String(), nil
		}
		b.WriteString(strings.TrimPrefix(line, "."))
	}
}

// refusal formats the reply for a refused command, with an enhanced status code.
func refusal(code int, detail string) string {
	enh := fmt.Sprintf("%d.0.0", code/100)
	switch code {
	case 550:
		enh = "5.1.1"
	case 451:
		enh = "4.3.0"
	}
	return fmt.Sprintf("%d %s %s", code, enh, detail)
}

func (s *Server) handle(raw net.Conn) {
	if s.o.Silent {
		_, _ = io.Copy(io.Discard, raw)
		return
	}
	_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
	ss := &session{s: s, conn: raw}
	if s.o.TLS == "implicit" {
		ss.conn = tls.Server(raw, s.tlsConfig())
		ss.tlsOn = true
	}
	ss.br = bufio.NewReader(ss.conn)
	if ss.say("220 mailtest ESMTP") != nil {
		return
	}
	var from string
	var rcpts []string
	for {
		line, err := ss.readLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		verb = strings.ToUpper(verb)
		if verb == "AUTH" {
			s.record("AUTH PLAIN [hidden]", ss.tlsOn)
		} else {
			s.record(line, ss.tlsOn)
		}
		var werr error
		switch verb {
		case "EHLO":
			werr = ss.ehlo()
		case "HELO":
			werr = ss.say("250 mailtest")
		case "STARTTLS":
			if s.o.TLS == "implicit" || s.o.NoStartTLS || ss.tlsOn {
				werr = ss.say("502 5.5.1 STARTTLS is not offered")
				break
			}
			if err := ss.say("220 2.0.0 ready to start TLS"); err != nil {
				return
			}
			ss.conn = tls.Server(raw, s.tlsConfig())
			ss.br = bufio.NewReader(ss.conn)
			ss.tlsOn = true
			from, rcpts = "", nil
		case "AUTH":
			werr = ss.auth(arg)
		case "MAIL":
			from = address(arg)
			werr = ss.reply("MAIL", "<"+from+">: sender refused")
		case "RCPT":
			if code, ok := s.o.Reply["RCPT"]; ok && code >= 400 {
				werr = ss.say("%s", refusal(code, "<"+address(arg)+">: recipient refused"))
				break
			}
			rcpts = append(rcpts, address(arg))
			werr = ss.say("250 2.1.5 ok")
		case "DATA":
			if code, ok := s.o.Reply["DATA"]; ok && code >= 400 {
				werr = ss.say("%s", refusal(code, "message refused"))
				break
			}
			if err := ss.say("354 end data with <CR><LF>.<CR><LF>"); err != nil {
				return
			}
			data, err := ss.readData()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.msgs = append(s.msgs, Received{From: from, To: strings.Join(rcpts, ","), Data: data})
			s.mu.Unlock()
			from, rcpts = "", nil
			werr = ss.say("250 2.0.0 queued")
		case "RSET":
			from, rcpts = "", nil
			werr = ss.say("250 2.0.0 ok")
		case "NOOP":
			werr = ss.say("250 2.0.0 ok")
		case "QUIT":
			_ = ss.say("221 2.0.0 bye")
			return
		default:
			werr = ss.say("502 5.5.2 command not recognized")
		}
		if werr != nil {
			return
		}
	}
}

func (s *Server) tlsConfig() *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS12}
}

// reply answers a command with the code in Options.Reply, or 250.
func (ss *session) reply(cmd, detail string) error {
	if code, ok := ss.s.o.Reply[cmd]; ok && code >= 400 {
		return ss.say("%s", refusal(code, detail))
	}
	return ss.say("250 2.1.0 ok")
}

func (ss *session) ehlo() error {
	var exts []string
	if ss.s.o.TLS != "implicit" && !ss.s.o.NoStartTLS && !ss.tlsOn {
		exts = append(exts, "STARTTLS")
	}
	if ss.s.o.User != "" {
		exts = append(exts, "AUTH PLAIN")
	}
	if len(exts) == 0 {
		return ss.say("250 mailtest")
	}
	if err := ss.say("250-mailtest"); err != nil {
		return err
	}
	for i, e := range exts {
		sep := "-"
		if i == len(exts)-1 {
			sep = " "
		}
		if err := ss.say("250%s%s", sep, e); err != nil {
			return err
		}
	}
	return nil
}

func (ss *session) auth(arg string) error {
	mech, initial, _ := strings.Cut(arg, " ")
	if ss.s.o.User == "" || !strings.EqualFold(mech, "PLAIN") {
		return ss.say("504 5.5.4 mechanism not available")
	}
	if initial == "" {
		if err := ss.say("334 "); err != nil {
			return err
		}
		line, err := ss.readLine()
		if err != nil {
			return err
		}
		initial = line
	}
	raw, err := base64.StdEncoding.DecodeString(initial)
	if err != nil {
		return ss.say("501 5.5.2 bad encoding")
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) == 3 && parts[1] == ss.s.o.User && parts[2] == ss.s.o.Password {
		return ss.say("235 2.7.0 authenticated")
	}
	return ss.say("535 5.7.8 authentication failed")
}

// address returns the address in "FROM:<a@example.org> ..." or "TO:<a@example.org>".
func address(arg string) string {
	_, rest, _ := strings.Cut(arg, ":")
	rest = strings.TrimSpace(rest)
	start := strings.IndexByte(rest, '<')
	end := strings.IndexByte(rest, '>')
	if start < 0 || end < start {
		return rest
	}
	return rest[start+1 : end]
}
