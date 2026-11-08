// Package web holds the HTTP handlers of the gobb server.
package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

// pingTimeout is how long the health check waits for the database.
const pingTimeout = 2 * time.Second

// Pinger checks that a dependency answers. *pgxpool.Pool satisfies it.
type Pinger interface {
	Ping(ctx context.Context) error
}

// healthEvery is how long the answer of one ping serves later health checks.
// /healthz skips the in-flight limit, so a flood of it must not reach the
// database more than about once a second.
const healthEvery = time.Second

// Health returns a handler for GET /healthz. It answers 200 "ok" when p
// answers within two seconds, and 503 "db unavailable" when it does not. In
// the server, the ping takes a connection from the pool under the wait limit of
// the request (GOBB_DB_POOL_WAIT), so a full pool answers 503 after that limit.
// The result of a ping serves every check for healthEvery after it, and one
// ping runs at a time: a check that comes during a ping waits for its result.
// Other methods get 405.
func Health(p Pinger) http.Handler {
	var (
		mu   sync.Mutex
		at   time.Time
		good bool
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		mu.Lock()
		if at.IsZero() || time.Since(at) >= healthEvery {
			good = ping(r.Context(), p)
			at = time.Now()
		}
		ok := good
		mu.Unlock()
		if !ok {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, "ok")
	})
}

// ping reports whether p answers within pingTimeout. Ping runs in its own
// goroutine, so a driver that ignores the context cannot hold the response
// past the timeout. The channel has room for the result, so the goroutine
// never blocks.
func ping(ctx context.Context, p Pinger) bool {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.Ping(ctx) }()
	select {
	case err := <-done:
		return err == nil
	case <-ctx.Done():
		return false
	}
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
