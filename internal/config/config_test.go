package config_test

import (
	"errors"
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
