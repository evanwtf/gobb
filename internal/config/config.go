// Package config reads the server settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// Config holds the settings for one gobb process.
type Config struct {
	// DSN is the PostgreSQL connection string.
	DSN string
	// Listen is the address the HTTP server binds to.
	Listen string
	// DataDir is the directory for files that the database does not hold.
	DataDir string
	// BasePath is the URL path prefix. It always starts and ends with "/".
	BasePath string
	// TrustedProxies lists the proxies whose forwarding headers the server trusts.
	TrustedProxies []netip.Prefix
	// CookieSecure marks the cookies of the site Secure, so that a browser sends
	// them only over HTTPS. It is true unless GOBB_COOKIE_SECURE is "false".
	CookieSecure bool
	// Mail holds the email settings. It is nil when email is off, which is when
	// GOBB_SMTP_HOST is unset.
	Mail *Mail
}

const (
	defaultListen         = "127.0.0.1:8080"
	defaultDataDir        = "/var/lib/gobb"
	defaultBasePath       = "/"
	defaultTrustedProxies = "127.0.0.1/32,::1/128"
)

// FromEnv builds a Config from getenv. An unset or empty variable takes its
// default, except GOBB_DSN, which is required. A GOBB_TRUSTED_PROXIES that is set
// but holds no entry after trimming, such as ",", is an error. GOBB_COOKIE_SECURE
// is "true" (the default) or "false"; any other value is an error. The email
// settings are read by [MailFromEnv].
func FromEnv(getenv func(string) string) (Config, error) {
	or := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}

	c := Config{
		DSN:      strings.TrimSpace(getenv("GOBB_DSN")),
		Listen:   or("GOBB_LISTEN", defaultListen),
		DataDir:  or("GOBB_DATA_DIR", defaultDataDir),
		BasePath: normalizeBasePath(or("GOBB_BASE_PATH", defaultBasePath)),
	}
	if c.DSN == "" {
		return Config{}, errors.New("config: GOBB_DSN is required")
	}

	proxies, err := parsePrefixes(or("GOBB_TRUSTED_PROXIES", defaultTrustedProxies))
	if err != nil {
		return Config{}, fmt.Errorf("config: GOBB_TRUSTED_PROXIES: %w", err)
	}
	c.TrustedProxies = proxies

	secure, err := cookieSecure(getenv)
	if err != nil {
		return Config{}, err
	}
	c.CookieSecure = secure

	c.Mail, err = mailFromEnv(getenv, c.BasePath, c.CookieSecure)
	if err != nil {
		return Config{}, err
	}
	return c, nil
}

// cookieSecure reads GOBB_COOKIE_SECURE: "true" (the default) or "false".
func cookieSecure(getenv func(string) string) (bool, error) {
	switch v := strings.TrimSpace(getenv("GOBB_COOKIE_SECURE")); v {
	case "", "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("config: GOBB_COOKIE_SECURE: %q is not %q or %q", v, "true", "false")
	}
}

// normalizeBasePath returns p with one leading and one trailing "/".
func normalizeBasePath(p string) string {
	p = path.Clean("/" + p)
	if p == "/" {
		return p
	}
	return p + "/"
}

func parsePrefixes(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		p, err := netip.ParsePrefix(item)
		if err != nil {
			return nil, fmt.Errorf("%q is not a CIDR prefix: %w", item, err)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errors.New("the list has no entries")
	}
	return out, nil
}

// Mail holds the email settings. Its String and LogValue methods print every
// field except Password, so a log line or an error never holds the password.
type Mail struct {
	// Host and Port are the SMTP server.
	Host string
	Port int
	// TLS is "starttls" or "implicit". There is no plain mode.
	TLS string
	// User and Password are for AUTH PLAIN. Both are empty, or both are set.
	User, Password string
	// From is the plain address in the From header and the envelope.
	From string
	// PublicURL is the address of the site for links in email. It starts with
	// "http://" or "https://" and ends with the base path, which ends with "/".
	PublicURL string
	// PerHour and PerDay are the most emails the board queues in any hour and in
	// any 24 hours. Each is at least 1, and PerHour is not more than PerDay.
	PerHour, PerDay int
}

// The defaults of GOBB_MAIL_PER_HOUR and GOBB_MAIL_PER_DAY, and the largest value
// of each.
const (
	defaultMailPerHour = 10
	defaultMailPerDay  = 20
	maxMailLimit       = 100_000
)

func (m Mail) passwordState() string {
	if m.Password == "" {
		return "[unset]"
	}
	return "[set]"
}

// String lists every field, with the password as [set] or [unset].
func (m Mail) String() string {
	return fmt.Sprintf("{Host:%s Port:%d TLS:%s User:%s Password:%s From:%s PublicURL:%s PerHour:%d PerDay:%d}",
		m.Host, m.Port, m.TLS, m.User, m.passwordState(), m.From, m.PublicURL, m.PerHour, m.PerDay)
}

// GoString makes %#v print what String prints.
func (m Mail) GoString() string { return "config.Mail" + m.String() }

// LogValue makes slog print the fields as a group, with the password as [set] or
// [unset].
func (m Mail) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("host", m.Host),
		slog.Int("port", m.Port),
		slog.String("tls", m.TLS),
		slog.String("user", m.User),
		slog.String("password", m.passwordState()),
		slog.String("from", m.From),
		slog.String("public_url", m.PublicURL),
		slog.Int("per_hour", m.PerHour),
		slog.Int("per_day", m.PerDay),
	)
}

// mailVars lists the variables that only make sense with GOBB_SMTP_HOST.
var mailVars = []string{
	"GOBB_SMTP_PORT", "GOBB_SMTP_TLS", "GOBB_SMTP_USER", "GOBB_SMTP_PASSWORD",
	"GOBB_MAIL_FROM", "GOBB_PUBLIC_URL", "GOBB_MAIL_PER_HOUR", "GOBB_MAIL_PER_DAY",
}

// MailFromEnv reads only the email settings, with GOBB_BASE_PATH and
// GOBB_COOKIE_SECURE, which they depend on. It needs no GOBB_DSN, for commands
// that touch no database. It returns nil when GOBB_SMTP_HOST is unset.
func MailFromEnv(getenv func(string) string) (*Mail, error) {
	base := strings.TrimSpace(getenv("GOBB_BASE_PATH"))
	if base == "" {
		base = defaultBasePath
	}
	secure, err := cookieSecure(getenv)
	if err != nil {
		return nil, err
	}
	return mailFromEnv(getenv, normalizeBasePath(base), secure)
}

// mailFromEnv reads and checks the email settings. An error names the variable
// and never holds the password, or a URL, which may hold one.
func mailFromEnv(getenv func(string) string, basePath string, secure bool) (*Mail, error) {
	get := func(key string) string { return strings.TrimSpace(getenv(key)) }
	host := get("GOBB_SMTP_HOST")
	if host == "" {
		for _, key := range mailVars {
			// A value of spaces counts as unset here, as it does below.
			if get(key) != "" {
				return nil, fmt.Errorf("config: %s is set, but GOBB_SMTP_HOST is not: email is off", key)
			}
		}
		return nil, nil
	}
	if !validHost(host) {
		return nil, errors.New("config: GOBB_SMTP_HOST: must be a host name or an IP address, with no port")
	}
	m := &Mail{Host: host}

	switch v := get("GOBB_SMTP_TLS"); v {
	case "", "starttls":
		m.TLS = "starttls"
	case "implicit":
		m.TLS = "implicit"
	default:
		return nil, fmt.Errorf("config: GOBB_SMTP_TLS: %q is not %q or %q", v, "starttls", "implicit")
	}

	m.Port = 587
	if m.TLS == "implicit" {
		m.Port = 465
	}
	if v := get("GOBB_SMTP_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("config: GOBB_SMTP_PORT: must be a number from 1 to 65535")
		}
		m.Port = n
	}

	// The password keeps its spaces. An empty value counts as unset.
	m.User, m.Password = get("GOBB_SMTP_USER"), getenv("GOBB_SMTP_PASSWORD")
	switch {
	case m.User != "" && m.Password == "":
		return nil, errors.New("config: GOBB_SMTP_PASSWORD: required when GOBB_SMTP_USER is set")
	case m.User == "" && m.Password != "":
		return nil, errors.New("config: GOBB_SMTP_USER: required when GOBB_SMTP_PASSWORD is set")
	}

	m.From = get("GOBB_MAIL_FROM")
	if m.From == "" {
		return nil, errors.New("config: GOBB_MAIL_FROM: required when GOBB_SMTP_HOST is set")
	}
	if !plainAddress(m.From) {
		return nil, errors.New("config: GOBB_MAIL_FROM: must be one plain address, such as board@example.org")
	}

	pub, err := publicURL(get("GOBB_PUBLIC_URL"), basePath, secure)
	if err != nil {
		return nil, err
	}
	m.PublicURL = pub

	if m.PerHour, err = mailLimit("GOBB_MAIL_PER_HOUR", get("GOBB_MAIL_PER_HOUR"), defaultMailPerHour); err != nil {
		return nil, err
	}
	if m.PerDay, err = mailLimit("GOBB_MAIL_PER_DAY", get("GOBB_MAIL_PER_DAY"), defaultMailPerDay); err != nil {
		return nil, err
	}
	if m.PerHour > m.PerDay {
		return nil, fmt.Errorf("config: GOBB_MAIL_PER_HOUR: %d is more than GOBB_MAIL_PER_DAY (%d)", m.PerHour, m.PerDay)
	}
	return m, nil
}

// mailLimit reads one limit on the emails of the board: def when v is empty,
// otherwise a number from 1 to maxMailLimit.
func mailLimit(key, v string, def int) (int, error) {
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > maxMailLimit {
		return 0, fmt.Errorf("config: %s: must be a number from 1 to %d", key, maxMailLimit)
	}
	return n, nil
}

// validHost reports whether s can be the host of a dial address: an IP address,
// or a name with no space, slash, at sign or port.
func validHost(s string) bool {
	if _, err := netip.ParseAddr(s); err == nil {
		return true
	}
	return !strings.ContainsAny(s, " \t/\\:@[]")
}

// plainAddress reports whether s is exactly one ASCII address, with no display
// name and no angle brackets. internal/mail has a twin of it, because mail
// imports config and config cannot import mail. Change both together.
func plainAddress(s string) bool {
	a, err := mail.ParseAddress(s)
	if err != nil || a.Name != "" || a.Address != s {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// publicURL checks GOBB_PUBLIC_URL and returns it with a trailing "/". The
// errors do not hold raw: it may hold a password.
func publicURL(raw, basePath string, secure bool) (string, error) {
	const name = "config: GOBB_PUBLIC_URL: "
	if raw == "" {
		return "", errors.New(name + "required when GOBB_SMTP_HOST is set")
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return "", errors.New(name + "is not a URL")
	case u.Scheme != "https" && u.Scheme != "http":
		return "", errors.New(name + "must start with https://")
	case u.Scheme == "http" && secure:
		return "", errors.New(name + "must start with https://, unless GOBB_COOKIE_SECURE is false")
	case u.Host == "" || u.Hostname() == "" || u.Opaque != "":
		return "", errors.New(name + "needs a host")
	case u.User != nil || strings.Contains(raw, "@"):
		return "", errors.New(name + "must not hold user information")
	case strings.ContainsAny(raw, "?#"):
		return "", errors.New(name + "must not hold a query or a fragment")
	}
	p := u.Path
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	if p != basePath {
		return "", fmt.Errorf(name+"the path must be the base path %s", basePath)
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: basePath}).String(), nil
}
