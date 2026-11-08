package config_test

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/config"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFromEnvRequiresDSN(t *testing.T) {
	_, err := config.FromEnv(env(nil))
	if err == nil || !strings.Contains(err.Error(), "GOBB_DSN") {
		t.Fatalf("want an error that names GOBB_DSN, got %v", err)
	}
}

func TestFromEnvDefaults(t *testing.T) {
	c, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.DSN != "postgres://x" {
		t.Errorf("DSN = %q", c.DSN)
	}
	if c.Listen != "127.0.0.1:8080" {
		t.Errorf("Listen = %q", c.Listen)
	}
	if c.DataDir != "/var/lib/gobb" {
		t.Errorf("DataDir = %q", c.DataDir)
	}
	if c.BasePath != "/" {
		t.Errorf("BasePath = %q", c.BasePath)
	}
}

// GOBB_METRICS_LISTEN is off by default. Set, it must be host:port with a port
// from 1 to 65535, and not the address of GOBB_LISTEN.
func TestFromEnvMetricsListen(t *testing.T) {
	c, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "postgres://x"}))
	if err != nil || c.MetricsListen != "" {
		t.Fatalf("default MetricsListen = %q, %v; want empty", c.MetricsListen, err)
	}
	for _, ok := range []string{"127.0.0.1:9187", " 127.0.0.1:9187 ", ":9187", "0.0.0.0:9187", "[::1]:9187", "localhost:9187"} {
		c, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "postgres://x", "GOBB_METRICS_LISTEN": ok}))
		if err != nil || c.MetricsListen != strings.TrimSpace(ok) {
			t.Errorf("%q: MetricsListen = %q, %v", ok, c.MetricsListen, err)
		}
	}
	for _, bad := range []string{"9187", "127.0.0.1", "127.0.0.1:", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:http", "127.0.0.1:8080"} {
		_, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "postgres://x", "GOBB_METRICS_LISTEN": bad}))
		if err == nil || !strings.Contains(err.Error(), "GOBB_METRICS_LISTEN") {
			t.Errorf("%q: err = %v, want an error that names GOBB_METRICS_LISTEN", bad, err)
		}
	}
}

func TestFromEnvNormalizesBasePath(t *testing.T) {
	for in, want := range map[string]string{
		"forums":   "/forums/",
		"/forums":  "/forums/",
		"/forums/": "/forums/",
		"/":        "/",
		"/a/b":     "/a/b/",
	} {
		c, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "x", "GOBB_BASE_PATH": in}))
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if c.BasePath != want {
			t.Errorf("%q: BasePath = %q, want %q", in, c.BasePath, want)
		}
	}
}

func TestFromEnvTrustedProxies(t *testing.T) {
	c, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")}
	if len(c.TrustedProxies) != 2 || c.TrustedProxies[0] != want[0] || c.TrustedProxies[1] != want[1] {
		t.Errorf("default TrustedProxies = %v", c.TrustedProxies)
	}

	c, err = config.FromEnv(env(map[string]string{
		"GOBB_DSN": "x", "GOBB_TRUSTED_PROXIES": "10.231.0.1/32, 10.0.0.0/8",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.TrustedProxies) != 2 ||
		c.TrustedProxies[0] != netip.MustParsePrefix("10.231.0.1/32") ||
		c.TrustedProxies[1] != netip.MustParsePrefix("10.0.0.0/8") {
		t.Errorf("TrustedProxies = %v", c.TrustedProxies)
	}

	_, err = config.FromEnv(env(map[string]string{"GOBB_DSN": "x", "GOBB_TRUSTED_PROXIES": "not-a-cidr"}))
	if err == nil || !strings.Contains(err.Error(), "GOBB_TRUSTED_PROXIES") {
		t.Fatalf("want an error that names GOBB_TRUSTED_PROXIES, got %v", err)
	}
}

// A list with no entries after trimming is an error, not an empty list. An empty
// list would trust no proxy, and a typo would then pass without a word.
func TestFromEnvTrustedProxiesNoEntries(t *testing.T) {
	for _, in := range []string{",", " , ", ",,", ", ,\t,"} {
		_, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "x", "GOBB_TRUSTED_PROXIES": in}))
		if err == nil || !strings.Contains(err.Error(), "GOBB_TRUSTED_PROXIES") ||
			!strings.Contains(err.Error(), "no entries") {
			t.Errorf("%q: want an error that names GOBB_TRUSTED_PROXIES and says no entries, got %v", in, err)
		}
	}
	// An unset, empty or blank variable still takes the default.
	for _, in := range []string{"", "   "} {
		c, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "x", "GOBB_TRUSTED_PROXIES": in}))
		if err != nil || len(c.TrustedProxies) != 2 {
			t.Errorf("%q: want the 2 default proxies, got %v, %v", in, c.TrustedProxies, err)
		}
	}
	// Empty items beside real ones are fine.
	c, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "x", "GOBB_TRUSTED_PROXIES": ",10.0.0.0/8,"}))
	if err != nil || len(c.TrustedProxies) != 1 {
		t.Errorf("want 1 proxy, got %v, %v", c.TrustedProxies, err)
	}
}

// The error for a bad entry wraps the parse error from net/netip.
func TestFromEnvTrustedProxiesWrapsParseError(t *testing.T) {
	_, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "x", "GOBB_TRUSTED_PROXIES": "10.0.0.0/8,300.1.1.1/8"}))
	if err == nil {
		t.Fatal("want an error")
	}
	inner := errors.Unwrap(errors.Unwrap(err))
	if inner == nil {
		t.Fatalf("the chain ends too early: %v", err)
	}
	if _, perr := netip.ParsePrefix("300.1.1.1/8"); perr == nil || inner.Error() != perr.Error() {
		t.Errorf("innermost error = %q, want the netip error %q", inner, perr)
	}
}

func TestFromEnvCookieSecure(t *testing.T) {
	for _, c := range []struct {
		value string
		set   bool
		want  bool
	}{
		{set: false, want: true},
		{value: "", set: true, want: true},
		{value: "true", set: true, want: true},
		{value: "false", set: true, want: false},
		{value: " false ", set: true, want: false},
	} {
		m := map[string]string{"GOBB_DSN": "x"}
		if c.set {
			m["GOBB_COOKIE_SECURE"] = c.value
		}
		got, err := config.FromEnv(env(m))
		if err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		if got.CookieSecure != c.want {
			t.Errorf("%+v: CookieSecure = %v, want %v", c, got.CookieSecure, c.want)
		}
	}
	for _, bad := range []string{"nope", "0", "1", "yes", "TRUE"} {
		_, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "x", "GOBB_COOKIE_SECURE": bad}))
		if err == nil || !strings.Contains(err.Error(), "GOBB_COOKIE_SECURE") {
			t.Errorf("%q: want an error that names GOBB_COOKIE_SECURE, got %v", bad, err)
		}
	}
}

const testPassword = "s3cr3t-pw"

// mailEnv returns an environment with email on, then applies set to it. A value
// of "-" in set removes the variable.
func mailEnv(set map[string]string) map[string]string {
	m := map[string]string{
		"GOBB_DSN":        "x",
		"GOBB_SMTP_HOST":  "smtp.example.org",
		"GOBB_MAIL_FROM":  "board@example.org",
		"GOBB_PUBLIC_URL": "https://board.example.org/",
	}
	for k, v := range set {
		if v == "-" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return m
}

func TestMailFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string // a substring of the error; empty means no error
		check   func(t *testing.T, m *config.Mail)
	}{
		{
			name: "no host means email is off",
			env:  map[string]string{"GOBB_DSN": "x"},
			check: func(t *testing.T, m *config.Mail) {
				if m != nil {
					t.Errorf("Mail = %+v, want nil", m)
				}
			},
		},
		{
			name: "an empty host counts as unset",
			env:  map[string]string{"GOBB_DSN": "x", "GOBB_SMTP_HOST": "", "GOBB_SMTP_PORT": ""},
			check: func(t *testing.T, m *config.Mail) {
				if m != nil {
					t.Errorf("Mail = %+v, want nil", m)
				}
			},
		},
		{
			name: "a whitespace-only port without a host counts as unset",
			env:  map[string]string{"GOBB_DSN": "x", "GOBB_SMTP_PORT": " "},
			check: func(t *testing.T, m *config.Mail) {
				if m != nil {
					t.Errorf("Mail = %+v, want nil", m)
				}
			},
		},
		{
			name:    "the sender without a host",
			env:     map[string]string{"GOBB_DSN": "x", "GOBB_MAIL_FROM": "board@example.org"},
			wantErr: "GOBB_MAIL_FROM",
		},
		{
			name:    "a user without a host",
			env:     map[string]string{"GOBB_DSN": "x", "GOBB_SMTP_USER": "u"},
			wantErr: "GOBB_SMTP_USER",
		},
		{
			name: "starttls is the default, with port 587",
			env:  mailEnv(nil),
			check: func(t *testing.T, m *config.Mail) {
				if m == nil || m.Host != "smtp.example.org" || m.TLS != "starttls" || m.Port != 587 {
					t.Errorf("Mail = %+v", m)
				}
				if m.From != "board@example.org" || m.PublicURL != "https://board.example.org/" {
					t.Errorf("Mail = %+v", m)
				}
			},
		},
		{
			name: "the email limits default to 10 an hour and 20 a day",
			env:  mailEnv(nil),
			check: func(t *testing.T, m *config.Mail) {
				if m.PerHour != 10 || m.PerDay != 20 {
					t.Errorf("PerHour, PerDay = %d, %d; want 10, 20", m.PerHour, m.PerDay)
				}
			},
		},
		{
			name: "explicit email limits",
			env:  mailEnv(map[string]string{"GOBB_MAIL_PER_HOUR": "3", "GOBB_MAIL_PER_DAY": " 12 "}),
			check: func(t *testing.T, m *config.Mail) {
				if m.PerHour != 3 || m.PerDay != 12 {
					t.Errorf("PerHour, PerDay = %d, %d; want 3, 12", m.PerHour, m.PerDay)
				}
			},
		},
		{
			name:    "a daily limit that is not a number",
			env:     mailEnv(map[string]string{"GOBB_MAIL_PER_DAY": "lots"}),
			wantErr: "GOBB_MAIL_PER_DAY",
		},
		{
			name:    "an hourly limit of zero",
			env:     mailEnv(map[string]string{"GOBB_MAIL_PER_HOUR": "0"}),
			wantErr: "GOBB_MAIL_PER_HOUR",
		},
		{
			name:    "a daily limit over 100,000",
			env:     mailEnv(map[string]string{"GOBB_MAIL_PER_DAY": "100001"}),
			wantErr: "GOBB_MAIL_PER_DAY",
		},
		{
			name:    "a negative hourly limit",
			env:     mailEnv(map[string]string{"GOBB_MAIL_PER_HOUR": "-1"}),
			wantErr: "GOBB_MAIL_PER_HOUR",
		},
		{
			name: "an hourly limit equal to the daily limit",
			env:  mailEnv(map[string]string{"GOBB_MAIL_PER_HOUR": "100000", "GOBB_MAIL_PER_DAY": "100000"}),
			check: func(t *testing.T, m *config.Mail) {
				if m.PerHour != 100000 || m.PerDay != 100000 {
					t.Errorf("PerHour, PerDay = %d, %d; want 100000, 100000", m.PerHour, m.PerDay)
				}
			},
		},
		{
			name:    "an hourly limit over the daily limit",
			env:     mailEnv(map[string]string{"GOBB_MAIL_PER_HOUR": "30", "GOBB_MAIL_PER_DAY": "20"}),
			wantErr: "GOBB_MAIL_PER_HOUR",
		},
		{
			name:    "a daily limit without a host",
			env:     map[string]string{"GOBB_DSN": "x", "GOBB_MAIL_PER_DAY": "5"},
			wantErr: "GOBB_MAIL_PER_DAY",
		},
		{
			name: "implicit defaults to port 465",
			env:  mailEnv(map[string]string{"GOBB_SMTP_TLS": "implicit"}),
			check: func(t *testing.T, m *config.Mail) {
				if m.TLS != "implicit" || m.Port != 465 {
					t.Errorf("Mail = %+v", m)
				}
			},
		},
		{
			name: "an explicit port",
			env:  mailEnv(map[string]string{"GOBB_SMTP_PORT": "2525"}),
			check: func(t *testing.T, m *config.Mail) {
				if m.Port != 2525 {
					t.Errorf("Port = %d", m.Port)
				}
			},
		},
		{name: "port 0", env: mailEnv(map[string]string{"GOBB_SMTP_PORT": "0"}), wantErr: "GOBB_SMTP_PORT"},
		{name: "port 65536", env: mailEnv(map[string]string{"GOBB_SMTP_PORT": "65536"}), wantErr: "GOBB_SMTP_PORT"},
		{name: "port not a number", env: mailEnv(map[string]string{"GOBB_SMTP_PORT": "smtp"}), wantErr: "GOBB_SMTP_PORT"},
		{name: "plain mode does not exist", env: mailEnv(map[string]string{"GOBB_SMTP_TLS": "none"}), wantErr: "GOBB_SMTP_TLS"},
		{name: "a host with a port in it", env: mailEnv(map[string]string{"GOBB_SMTP_HOST": "smtp.example.org:587"}), wantErr: "GOBB_SMTP_HOST"},
		{name: "a user without a password", env: mailEnv(map[string]string{"GOBB_SMTP_USER": "u"}), wantErr: "GOBB_SMTP_PASSWORD"},
		{name: "a password without a user", env: mailEnv(map[string]string{"GOBB_SMTP_PASSWORD": testPassword}), wantErr: "GOBB_SMTP_USER"},
		{
			name: "a user and a password",
			env:  mailEnv(map[string]string{"GOBB_SMTP_USER": "u", "GOBB_SMTP_PASSWORD": testPassword}),
			check: func(t *testing.T, m *config.Mail) {
				if m.User != "u" || m.Password != testPassword {
					t.Errorf("Mail = %+v", m)
				}
			},
		},
		{name: "no sender", env: mailEnv(map[string]string{"GOBB_MAIL_FROM": "-"}), wantErr: "GOBB_MAIL_FROM"},
		{name: "a sender with a display name", env: mailEnv(map[string]string{"GOBB_MAIL_FROM": "Board <board@example.org>"}), wantErr: "GOBB_MAIL_FROM"},
		{name: "a sender in angle brackets", env: mailEnv(map[string]string{"GOBB_MAIL_FROM": "<board@example.org>"}), wantErr: "GOBB_MAIL_FROM"},
		{name: "a sender that is not an address", env: mailEnv(map[string]string{"GOBB_MAIL_FROM": "board"}), wantErr: "GOBB_MAIL_FROM"},
		{name: "no public URL", env: mailEnv(map[string]string{"GOBB_PUBLIC_URL": "-"}), wantErr: "GOBB_PUBLIC_URL"},
		{
			name:    "http with secure cookies",
			env:     mailEnv(map[string]string{"GOBB_PUBLIC_URL": "http://board.example.org/"}),
			wantErr: "GOBB_PUBLIC_URL",
		},
		{
			name: "http with insecure cookies",
			env:  mailEnv(map[string]string{"GOBB_PUBLIC_URL": "http://board.example.org/", "GOBB_COOKIE_SECURE": "false"}),
			check: func(t *testing.T, m *config.Mail) {
				if m.PublicURL != "http://board.example.org/" {
					t.Errorf("PublicURL = %q", m.PublicURL)
				}
			},
		},
		{name: "another scheme", env: mailEnv(map[string]string{"GOBB_PUBLIC_URL": "ftp://board.example.org/"}), wantErr: "GOBB_PUBLIC_URL"},
		{name: "no host in the URL", env: mailEnv(map[string]string{"GOBB_PUBLIC_URL": "https:///"}), wantErr: "GOBB_PUBLIC_URL"},
		{
			name:    "a path that is not the base path",
			env:     mailEnv(map[string]string{"GOBB_BASE_PATH": "/forums/", "GOBB_PUBLIC_URL": "https://board.example.org/x/"}),
			wantErr: "GOBB_PUBLIC_URL",
		},
		{
			name:    "no path with a base path",
			env:     mailEnv(map[string]string{"GOBB_BASE_PATH": "/forums/", "GOBB_PUBLIC_URL": "https://board.example.org/"}),
			wantErr: "GOBB_PUBLIC_URL",
		},
		{
			name: "the base path without a trailing slash",
			env:  mailEnv(map[string]string{"GOBB_BASE_PATH": "/forums/", "GOBB_PUBLIC_URL": "https://board.example.org/forums"}),
			check: func(t *testing.T, m *config.Mail) {
				if m.PublicURL != "https://board.example.org/forums/" {
					t.Errorf("PublicURL = %q", m.PublicURL)
				}
			},
		},
		{
			name: "no path with the root base path",
			env:  mailEnv(map[string]string{"GOBB_PUBLIC_URL": "https://board.example.org"}),
			check: func(t *testing.T, m *config.Mail) {
				if m.PublicURL != "https://board.example.org/" {
					t.Errorf("PublicURL = %q", m.PublicURL)
				}
			},
		},
		{name: "a query", env: mailEnv(map[string]string{"GOBB_PUBLIC_URL": "https://board.example.org/?a=1"}), wantErr: "GOBB_PUBLIC_URL"},
		{name: "an empty query", env: mailEnv(map[string]string{"GOBB_PUBLIC_URL": "https://board.example.org/?"}), wantErr: "GOBB_PUBLIC_URL"},
		{name: "a fragment", env: mailEnv(map[string]string{"GOBB_PUBLIC_URL": "https://board.example.org/#f"}), wantErr: "GOBB_PUBLIC_URL"},
		{name: "user information", env: mailEnv(map[string]string{"GOBB_PUBLIC_URL": "https://user@board.example.org/"}), wantErr: "GOBB_PUBLIC_URL"},
		{name: "user information with a password", env: mailEnv(map[string]string{"GOBB_PUBLIC_URL": "https://user:pw@board.example.org/"}), wantErr: "GOBB_PUBLIC_URL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.env
			c, err := config.FromEnv(env(m))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want an error that names %s, got %v", tc.wantErr, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if err != nil && strings.Contains(err.Error(), testPassword) {
				t.Errorf("the error holds the password: %v", err)
			}
			if tc.check != nil {
				tc.check(t, c.Mail)
			}
		})
	}
}

// TestMailErrorsKeepThePassword: with a password in the environment, no error
// text holds it, whatever else is wrong.
func TestMailErrorsKeepThePassword(t *testing.T) {
	bad := []map[string]string{
		{"GOBB_SMTP_PORT": "0"},
		{"GOBB_SMTP_TLS": "none"},
		{"GOBB_MAIL_FROM": "Board <board@example.org>"},
		{"GOBB_MAIL_FROM": "-"},
		{"GOBB_PUBLIC_URL": "http://board.example.org/"},
		{"GOBB_PUBLIC_URL": "https://user:" + testPassword + "@board.example.org/"},
		{"GOBB_PUBLIC_URL": "https://board.example.org/?" + testPassword},
		{"GOBB_SMTP_USER": "-"},
		{"GOBB_SMTP_HOST": "-"},
		{"GOBB_SMTP_HOST": "-", "GOBB_MAIL_FROM": "x"},
	}
	for _, set := range bad {
		m := mailEnv(set)
		m["GOBB_SMTP_PASSWORD"] = testPassword
		if _, ok := set["GOBB_SMTP_USER"]; !ok {
			m["GOBB_SMTP_USER"] = "u"
		}
		_, err := config.FromEnv(env(m))
		if err == nil {
			t.Errorf("%v: want an error", set)
			continue
		}
		if strings.Contains(err.Error(), testPassword) {
			t.Errorf("%v: the error holds the password: %v", set, err)
		}
	}
}

func TestMailFromEnvOnly(t *testing.T) {
	// MailFromEnv needs no GOBB_DSN, for commands that touch no database.
	m, err := config.MailFromEnv(env(map[string]string{
		"GOBB_SMTP_HOST":  "smtp.example.org",
		"GOBB_MAIL_FROM":  "board@example.org",
		"GOBB_PUBLIC_URL": "https://board.example.org/",
	}))
	if err != nil || m == nil || m.Port != 587 {
		t.Fatalf("got %+v, %v", m, err)
	}
	m, err = config.MailFromEnv(env(nil))
	if err != nil || m != nil {
		t.Fatalf("got %+v, %v; want nil and no error", m, err)
	}
}

func TestMailConfigRedacts(t *testing.T) {
	m := mailEnv(map[string]string{"GOBB_SMTP_USER": "u", "GOBB_SMTP_PASSWORD": testPassword})
	c, err := config.FromEnv(env(m))
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	log.Info("value", "mail", *c.Mail)
	log.Info("pointer", "mail", c.Mail)
	log.Info("config", "config", c)
	out := []string{
		fmt.Sprint(c.Mail),
		fmt.Sprint(*c.Mail),
		fmt.Sprintf("%v", c.Mail),
		fmt.Sprintf("%+v", *c.Mail),
		fmt.Sprintf("%+v", c.Mail),
		fmt.Sprintf("%#v", *c.Mail),
		fmt.Sprintf("[%s]", *c.Mail),
		buf.String(),
	}
	for i, s := range out {
		if strings.Contains(s, testPassword) {
			t.Errorf("output %d holds the password: %s", i, s)
		}
	}
	for _, want := range []string{"smtp.example.org", "587", "board@example.org", "[set]"} {
		if !strings.Contains(out[3], want) {
			t.Errorf("%%+v output lacks %q: %s", want, out[3])
		}
	}
	if !strings.Contains(buf.String(), "[set]") {
		t.Errorf("slog output lacks [set]: %s", buf.String())
	}
	// With no password, the field says so.
	c, err = config.FromEnv(env(mailEnv(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Mail.String(), "[unset]") {
		t.Errorf("String() = %s, want [unset]", c.Mail.String())
	}
}

// GOBB_PUBLIC_URL works without email: the sitemap, robots.txt and the canonical
// links need it too. It follows the same checks as with email.
func TestFromEnvPublicURL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     map[string]string
		want    string
		wantErr string
	}{
		{name: "unset", env: map[string]string{"GOBB_DSN": "x"}, want: ""},
		{name: "without email", env: map[string]string{"GOBB_DSN": "x", "GOBB_PUBLIC_URL": "https://board.example.org"}, want: "https://board.example.org/"},
		{name: "with a base path", env: map[string]string{"GOBB_DSN": "x", "GOBB_BASE_PATH": "/forum", "GOBB_PUBLIC_URL": "https://board.example.org/forum"}, want: "https://board.example.org/forum/"},
		{name: "path not the base path", env: map[string]string{"GOBB_DSN": "x", "GOBB_BASE_PATH": "/forum/", "GOBB_PUBLIC_URL": "https://board.example.org/"}, wantErr: "GOBB_PUBLIC_URL"},
		{name: "http with secure cookies", env: map[string]string{"GOBB_DSN": "x", "GOBB_PUBLIC_URL": "http://board.example.org/"}, wantErr: "GOBB_PUBLIC_URL"},
		{name: "a query", env: map[string]string{"GOBB_DSN": "x", "GOBB_PUBLIC_URL": "https://board.example.org/?a=1"}, wantErr: "GOBB_PUBLIC_URL"},
		{name: "with email", env: mailEnv(nil), want: "https://board.example.org/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := config.FromEnv(env(tc.env))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one that names %s", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.PublicURL != tc.want {
				t.Errorf("PublicURL = %q, want %q", c.PublicURL, tc.want)
			}
		})
	}
}

// The load settings default to a small board on a 1 GB host.
func TestFromEnvLoadDefaults(t *testing.T) {
	c, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	want := config.Load{DBMaxConns: 4, DBPoolWait: time.Second, SearchPerSecond: 5, SearchMaxRunning: 2,
		PageCacheBytes: 16 << 20, PageCacheWarm: true, FlushEvery: 30 * time.Second}
	if c.Load != want {
		t.Errorf("Load = %+v, want %+v", c.Load, want)
	}
}

func TestFromEnvLoadValues(t *testing.T) {
	c, err := config.FromEnv(env(map[string]string{
		"GOBB_DSN": "postgres://x", "GOBB_DB_MAX_CONNS": " 8 ", "GOBB_DB_POOL_WAIT": "250ms",
		"GOBB_SEARCH_PER_SECOND": "20", "GOBB_SEARCH_MAX_RUNNING": "7",
		"GOBB_PAGE_CACHE_MIB": "64", "GOBB_PAGE_CACHE_WARM": "false", "GOBB_WRITE_FLUSH_INTERVAL": "1m",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := config.Load{DBMaxConns: 8, DBPoolWait: 250 * time.Millisecond, SearchPerSecond: 20, SearchMaxRunning: 7,
		PageCacheBytes: 64 << 20, PageCacheWarm: false, FlushEvery: time.Minute}
	if c.Load != want {
		t.Errorf("Load = %+v, want %+v", c.Load, want)
	}
	// Searches leave one connection for the pages: with a pool of two, the
	// default shrinks to 1. A pool of one still allows one search.
	for conns, want := range map[string]int{"1": 1, "2": 1, "3": 2} {
		c, err = config.FromEnv(env(map[string]string{"GOBB_DSN": "postgres://x", "GOBB_DB_MAX_CONNS": conns}))
		if err != nil || c.Load.SearchMaxRunning != want {
			t.Errorf("pool of %s: SearchMaxRunning = %d, %v; want %d", conns, c.Load.SearchMaxRunning, err, want)
		}
	}
	// The bound is GOBB_DB_MAX_CONNS - 1, and 1 for a pool of one.
	for conns, running := range map[string]string{"4": "3", "1": "1", "2": "1"} {
		c, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "postgres://x", "GOBB_DB_MAX_CONNS": conns, "GOBB_SEARCH_MAX_RUNNING": running}))
		if err != nil || strconv.Itoa(c.Load.SearchMaxRunning) != running {
			t.Errorf("pool of %s, %s running: %d, %v", conns, running, c.Load.SearchMaxRunning, err)
		}
	}
	// 0 turns the page cache off.
	if c, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "postgres://x", "GOBB_PAGE_CACHE_MIB": "0"})); err != nil || c.Load.PageCacheBytes != 0 {
		t.Errorf("GOBB_PAGE_CACHE_MIB=0: %d, %v", c.Load.PageCacheBytes, err)
	}
	// The bounds themselves are allowed.
	for k, v := range map[string]string{"GOBB_DB_MAX_CONNS": "100", "GOBB_DB_POOL_WAIT": "10ms", "GOBB_SEARCH_PER_SECOND": "1000",
		"GOBB_PAGE_CACHE_MIB": "1024", "GOBB_WRITE_FLUSH_INTERVAL": "1s", "GOBB_PAGE_CACHE_WARM": "true"} {
		if _, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "postgres://x", k: v})); err != nil {
			t.Errorf("%s=%s: %v", k, v, err)
		}
	}
	if c, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "postgres://x", "GOBB_DB_POOL_WAIT": "5s"})); err != nil || c.Load.DBPoolWait != 5*time.Second {
		t.Errorf("GOBB_DB_POOL_WAIT=5s: %v, %v", c.Load.DBPoolWait, err)
	}
}

func TestFromEnvLoadInvalid(t *testing.T) {
	for _, c := range []struct{ key, value string }{
		{"GOBB_DB_MAX_CONNS", "0"},
		{"GOBB_DB_MAX_CONNS", "101"},
		{"GOBB_DB_MAX_CONNS", "-1"},
		{"GOBB_DB_MAX_CONNS", "four"},
		{"GOBB_DB_POOL_WAIT", "1"},
		{"GOBB_DB_POOL_WAIT", "9ms"},
		{"GOBB_DB_POOL_WAIT", "5001ms"},
		{"GOBB_DB_POOL_WAIT", "-1s"},
		{"GOBB_DB_POOL_WAIT", "soon"},
		{"GOBB_SEARCH_PER_SECOND", "0"},
		{"GOBB_SEARCH_PER_SECOND", "1001"},
		{"GOBB_SEARCH_PER_SECOND", "5.5"},
		{"GOBB_SEARCH_MAX_RUNNING", "0"},
		{"GOBB_SEARCH_MAX_RUNNING", "4"}, // the whole pool of 4
		{"GOBB_SEARCH_MAX_RUNNING", "5"},
		{"GOBB_SEARCH_MAX_RUNNING", "x"},
		{"GOBB_PAGE_CACHE_MIB", "-1"},
		{"GOBB_PAGE_CACHE_MIB", "1025"},
		{"GOBB_PAGE_CACHE_MIB", "16MiB"},
		{"GOBB_PAGE_CACHE_WARM", "yes"},
		{"GOBB_WRITE_FLUSH_INTERVAL", "30"},
		{"GOBB_WRITE_FLUSH_INTERVAL", "999ms"},
		{"GOBB_WRITE_FLUSH_INTERVAL", "11m"},
	} {
		_, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "postgres://x", c.key: c.value}))
		if err == nil || !strings.Contains(err.Error(), c.key) {
			t.Errorf("%s=%q: err = %v, want an error that names %s", c.key, c.value, err, c.key)
		}
	} // With a pool of two, two searches at once would hold the whole pool.
	_, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "postgres://x", "GOBB_DB_MAX_CONNS": "2", "GOBB_SEARCH_MAX_RUNNING": "2"}))
	if err == nil || !strings.Contains(err.Error(), "GOBB_SEARCH_MAX_RUNNING") {
		t.Errorf("pool of 2, 2 running: err = %v, want an error that names GOBB_SEARCH_MAX_RUNNING", err)
	}
}

func TestFromEnvSigninDefaults(t *testing.T) {
	c, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	s := c.Signin
	if s.FreeFailures != 2 || !slices.Equal(s.Waits, []time.Duration{15 * time.Second, 15 * time.Second, time.Minute, time.Minute, time.Minute}) ||
		s.LastWait != 900*time.Second || s.ResetAfter != 24*time.Hour || s.AddressKeep != 365*24*time.Hour ||
		s.DefensiveFailures != 100 || s.DefensiveWindow != time.Minute || s.DefensiveQuiet != 10*time.Minute || s.DefensiveGap != 5*time.Second || s.TableKeys != 50000 {
		t.Errorf("Signin = %+v, want the owner's defaults", s)
	}
}

func TestFromEnvSigninValues(t *testing.T) {
	c, err := config.FromEnv(env(map[string]string{
		"GOBB_DSN": "postgres://x", "GOBB_SIGNIN_FREE_FAILURES": "0", "GOBB_SIGNIN_WAITS": " 1s, 2m ",
		"GOBB_SIGNIN_LAST_WAIT": "1h", "GOBB_SIGNIN_RESET_AFTER": "48h", "GOBB_ADDRESS_KEEP_DAYS": "30",
		"GOBB_DEFENSIVE_FAILURES": "50", "GOBB_DEFENSIVE_WINDOW": "30s", "GOBB_DEFENSIVE_QUIET": "1m", "GOBB_DEFENSIVE_GAP": "0s", "GOBB_SIGNIN_TABLE_KEYS": "1000",
	}))
	if err != nil {
		t.Fatal(err)
	}
	s := c.Signin
	if s.FreeFailures != 0 || !slices.Equal(s.Waits, []time.Duration{time.Second, 2 * time.Minute}) ||
		s.LastWait != time.Hour || s.ResetAfter != 48*time.Hour || s.AddressKeep != 30*24*time.Hour ||
		s.DefensiveFailures != 50 || s.DefensiveWindow != 30*time.Second || s.DefensiveQuiet != time.Minute || s.DefensiveGap != 0 || s.TableKeys != 1000 {
		t.Errorf("Signin = %+v", s)
	}
}

func TestFromEnvSigninInvalid(t *testing.T) {
	for _, c := range []struct{ key, value string }{
		{"GOBB_SIGNIN_FREE_FAILURES", "-1"},
		{"GOBB_SIGNIN_FREE_FAILURES", "101"},
		{"GOBB_SIGNIN_WAITS", "15"},
		{"GOBB_SIGNIN_WAITS", "15s,,60s"},
		{"GOBB_SIGNIN_WAITS", "500ms"},
		{"GOBB_SIGNIN_WAITS", "25h"},
		{"GOBB_SIGNIN_WAITS", strings.Repeat("1s,", 20) + "1s"},
		{"GOBB_SIGNIN_LAST_WAIT", "0s"},
		{"GOBB_SIGNIN_LAST_WAIT", "25h"},
		{"GOBB_SIGNIN_RESET_AFTER", "59s"},
		{"GOBB_ADDRESS_KEEP_DAYS", "0"},
		{"GOBB_ADDRESS_KEEP_DAYS", "3651"},
		{"GOBB_ADDRESS_KEEP_DAYS", "1y"},
		{"GOBB_DEFENSIVE_FAILURES", "0"},
		{"GOBB_DEFENSIVE_FAILURES", "10001"},
		{"GOBB_DEFENSIVE_WINDOW", "0s"},
		{"GOBB_DEFENSIVE_WINDOW", "2h"},
		{"GOBB_DEFENSIVE_QUIET", "soon"},
		{"GOBB_DEFENSIVE_GAP", "-1s"},
		{"GOBB_DEFENSIVE_GAP", "2h"},
		{"GOBB_SIGNIN_TABLE_KEYS", "999"},
		{"GOBB_SIGNIN_TABLE_KEYS", "1000001"},
	} {
		_, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "postgres://x", c.key: c.value}))
		if err == nil || !strings.Contains(err.Error(), c.key) {
			t.Errorf("%s=%q: err = %v, want an error that names %s", c.key, c.value, err, c.key)
		}
	}
	// Twenty waits are allowed.
	if _, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "postgres://x", "GOBB_SIGNIN_WAITS": strings.Repeat("1s,", 19) + "1s"})); err != nil {
		t.Errorf("20 waits: %v", err)
	}
}

// A count may not clear before its longest wait ends.
func TestFromEnvSigninResetCoversTheWaits(t *testing.T) {
	for _, c := range []map[string]string{
		{"GOBB_SIGNIN_RESET_AFTER": "10m"}, // under the default 900 s
		{"GOBB_SIGNIN_RESET_AFTER": "2h", "GOBB_SIGNIN_WAITS": "15s,3h", "GOBB_SIGNIN_LAST_WAIT": "1m"},
	} {
		c["GOBB_DSN"] = "postgres://x"
		_, err := config.FromEnv(env(c))
		if err == nil || !strings.Contains(err.Error(), "GOBB_SIGNIN_RESET_AFTER") {
			t.Errorf("%v: err = %v, want an error that names GOBB_SIGNIN_RESET_AFTER", c, err)
		}
	}
	if _, err := config.FromEnv(env(map[string]string{"GOBB_DSN": "postgres://x", "GOBB_SIGNIN_RESET_AFTER": "15m"})); err != nil {
		t.Errorf("a reset equal to the longest wait: %v", err)
	}
}
