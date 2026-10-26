package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/mail"
	"github.com/evanwtf/gobb/internal/metrics"
	"github.com/evanwtf/gobb/internal/session"
	"github.com/evanwtf/gobb/internal/version"
)

const (
	// metricsEvery is how long a gauge that runs a query keeps its value, so a
	// scrape costs the database one query per gauge in that time at most.
	metricsEvery = 15 * time.Second
	// metricsQueryTimeout is the limit on one such query.
	metricsQueryTimeout = 5 * time.Second
)

// registerServeMetrics adds to metrics.Default the build info and the gauges
// that read the pool, the memory gate or the database of this process.
func registerServeMetrics(pool *pgxpool.Pool, gate *login.Gate) {
	r := metrics.Default
	r.Info("gobb_build_info", "The version and the commit of the build.",
		[]string{"version", "commit"}, []string{version.Version, version.Build()})

	r.GaugeFunc("gobb_email_queue_depth", "Emails that wait for their first try or for a retry.",
		metrics.Cached("gobb_email_queue_depth", metricsEvery, metricsQueryTimeout, func(ctx context.Context) (float64, error) {
			n, err := mail.Pending(ctx, pool)
			return float64(n), err
		}))
	r.GaugeFunc("gobb_sessions_active", "Sessions that have not expired.",
		metrics.Cached("gobb_sessions_active", metricsEvery, metricsQueryTimeout, func(ctx context.Context) (float64, error) {
			n, err := session.CountLive(ctx, pool, time.Now())
			return float64(n), err
		}))

	gateStat := func(f func(holders int, usedKiB uint64, waiting int) float64) func() (float64, bool) {
		return func() (float64, bool) { return f(gate.Stats()), true }
	}
	r.GaugeFunc("gobb_memory_gate_holders", "Password checks and image decodes that hold a share of the memory gate.",
		gateStat(func(h int, _ uint64, _ int) float64 { return float64(h) }))
	r.GaugeFunc("gobb_memory_gate_waiting", "Password checks and image decodes that wait for a share of the memory gate.",
		gateStat(func(_ int, _ uint64, w int) float64 { return float64(w) }))
	r.GaugeFunc("gobb_memory_gate_used_bytes", "Memory that the holders of the gate hold.",
		gateStat(func(_ int, used uint64, _ int) float64 { return float64(used) * 1024 }))
	r.GaugeFunc("gobb_memory_gate_budget_bytes", "Memory that the gate lets its holders use at one time.",
		func() (float64, bool) { return float64(gate.Budget()) * 1024, true })

	stat := func(f func(*pgxpool.Stat) float64) func() (float64, bool) {
		return func() (float64, bool) { return f(pool.Stat()), true }
	}
	r.GaugeFunc("gobb_db_pool_conns", "Database connections in the pool, open or opening.",
		stat(func(s *pgxpool.Stat) float64 { return float64(s.TotalConns()) }))
	r.GaugeFunc("gobb_db_pool_acquired_conns", "Database connections in use.",
		stat(func(s *pgxpool.Stat) float64 { return float64(s.AcquiredConns()) }))
	r.GaugeFunc("gobb_db_pool_idle_conns", "Database connections open and not in use.",
		stat(func(s *pgxpool.Stat) float64 { return float64(s.IdleConns()) }))
	r.GaugeFunc("gobb_db_pool_max_conns", "The most connections that the pool opens.",
		stat(func(s *pgxpool.Stat) float64 { return float64(s.MaxConns()) }))
	r.CounterFunc("gobb_db_pool_acquires_total", "Connections taken from the pool.",
		stat(func(s *pgxpool.Stat) float64 { return float64(s.AcquireCount()) }))
	r.CounterFunc("gobb_db_pool_empty_acquires_total", "Takes that found no idle connection, and waited or opened one.",
		stat(func(s *pgxpool.Stat) float64 { return float64(s.EmptyAcquireCount()) }))
	r.CounterFunc("gobb_db_pool_canceled_acquires_total", "Takes that ended before they got a connection.",
		stat(func(s *pgxpool.Stat) float64 { return float64(s.CanceledAcquireCount()) }))
	r.CounterFunc("gobb_db_pool_acquire_seconds_total", "Time that takes from the pool waited, in all.",
		stat(func(s *pgxpool.Stat) float64 { return s.AcquireDuration().Seconds() }))
	r.CounterFunc("gobb_db_pool_new_conns_total", "Database connections opened.",
		stat(func(s *pgxpool.Stat) float64 { return float64(s.NewConnsCount()) }))
}

// startMetrics starts the metrics listener on addr, which serves only GET
// /metrics. It returns the function that stops the listener, and the address it
// bound. With addr empty, nothing listens: the stop function does nothing, and
// the address is empty.
func startMetrics(addr string) (stop func(context.Context) error, bound string, err error) {
	if addr == "" {
		return func(context.Context) error { return nil }, "", nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("metrics listener: %w", err)
	}
	srv := &http.Server{
		Handler:           metrics.Handler(metrics.Default),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("metrics listener stopped", "err", err)
		}
	}()
	bound = ln.Addr().String()
	logMetricsAddr(bound)
	stop = func(ctx context.Context) error {
		err := srv.Shutdown(ctx)
		if err != nil {
			// A slow scrape keeps its connection past the window: drop it.
			_ = srv.Close()
		}
		return err
	}
	return stop, bound, nil
}

// logMetricsAddr logs the address of the metrics listener in one line. The
// endpoint has no auth, so an address that is not loopback or private gets a WARN
// line. Every interface is one of those: inside a container it is the usual
// address, and the line says to publish the port on loopback only.
func logMetricsAddr(addr string) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		slog.Info("metrics listening", "addr", addr)
		return
	}
	ip, err := netip.ParseAddr(host)
	switch {
	case err != nil:
		// A name, such as localhost.
		slog.Info("metrics listening", "addr", addr)
	case ip.IsUnspecified():
		slog.Warn("metrics listening on every interface; the endpoint has no auth", "addr", addr,
			"note", "in a container this is usual: publish the port on loopback only")
	case ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast():
		slog.Info("metrics listening", "addr", addr)
	default:
		slog.Warn("metrics listening on a public address; the endpoint has no auth", "addr", addr)
	}
}
