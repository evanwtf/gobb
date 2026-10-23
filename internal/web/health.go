// Package web holds the HTTP handlers of the gobb server.
package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"
)

// pingTimeout is how long the health check waits for the database.
const pingTimeout = 2 * time.Second

// Pinger checks that a dependency answers. *pgxpool.Pool satisfies it.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Health returns a handler for GET /healthz. It answers 200 "ok" when p
// answers within two seconds, and 503 "db unavailable" when it does not.
// Other methods get 405.
func Health(p Pinger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), pingTimeout)
		defer cancel()
		// Ping runs in its own goroutine, so a driver that ignores the
		// context cannot hold the response past the timeout. The channel
		// has room for the result, so the goroutine never blocks.
		done := make(chan error, 1)
		go func() { done <- p.Ping(ctx) }()
		select {
		case err := <-done:
			if err != nil {
				http.Error(w, "db unavailable", http.StatusServiceUnavailable)
				return
			}
		case <-ctx.Done():
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, "ok")
	})
}

// HealthURL returns the URL that the healthcheck command requests for the
// listen address. A wildcard or empty host becomes 127.0.0.1.
func HealthURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("web: listen address %q: %w", listen, err)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("web: listen address %q: bad port %q", listen, port)
	}
	if host == "" {
		host = "127.0.0.1"
	} else if ip, err := netip.ParseAddr(host); err == nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
}
