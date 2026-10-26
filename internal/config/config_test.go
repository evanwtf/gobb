package config_test

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"testing"

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
			name:    "the sender without a host",
			env:     map[string]string{"GOBB_DSN": "x", "GOBB_MAIL_FROM": "board@example.org"},
			wantErr: "GOBB_MAIL_FROM",
		},
		{
			name:    "the public URL without a host",
			env:     map[string]string{"GOBB_DSN": "x", "GOBB_PUBLIC_URL": "https://board.example.org/"},
			wantErr: "GOBB_PUBLIC_URL",
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
