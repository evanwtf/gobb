// Package config reads the server settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"path"
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
}

const (
	defaultListen         = "127.0.0.1:8080"
	defaultDataDir        = "/var/lib/gobb"
	defaultBasePath       = "/"
	defaultTrustedProxies = "127.0.0.1/32,::1/128"
)

// FromEnv builds a Config from getenv. An unset or empty variable takes its
// default, except GOBB_DSN, which is required.
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
	return c, nil
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
			return nil, fmt.Errorf("%q is not a CIDR prefix", item)
		}
		out = append(out, p)
	}
	return out, nil
}
