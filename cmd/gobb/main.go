// Command gobb is the gobb forum server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	// The timezone database is compiled in, so that timezone checks work in the
	// static image, which has no zoneinfo files.
	_ "time/tzdata"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/acldb"
	"github.com/evanwtf/gobb/internal/batch"
	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/config"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/importer"
	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/mail"
	"github.com/evanwtf/gobb/internal/memlimit"
	"github.com/evanwtf/gobb/internal/session"
	"github.com/evanwtf/gobb/internal/version"
	"github.com/evanwtf/gobb/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// shutdownTimeout is the window for a clean stop: it covers the end of the
// open requests and the close of the database pool. Together with poolGrace
// (the extra time after a shutdown that ran out of time) it must stay under the
// 30 s stop_grace_period in compose.yaml: 20 s and 5 s leave 5 s before Docker
// sends SIGKILL. The tests shorten it.
var shutdownTimeout = 20 * time.Second

// poolGrace is how long closePool gets after a shutdown that ran out of time.
// The tests shorten it.
var poolGrace = 5 * time.Second

const (
	// healthcheckTimeout bounds the request that the healthcheck command sends.
	healthcheckTimeout = 2 * time.Second
	// defaultListen matches the default in package config.
	defaultListen = "127.0.0.1:8080"
)

type command func(ctx context.Context, args []string) error

// errUsage is the error of a command that was called wrongly. It has printed
// its own usage line, so run adds none and exits 2.
var errUsage = errors.New("usage")

// stdout receives the results of commands. The tests replace it.
var stdout io.Writer = os.Stdout

// stderr receives the messages of the user commands, which keep stdout for the
// one thing they print. The tests replace it.
var stderr io.Writer = os.Stderr

// errReported is the error of a command that has told the operator what went
// wrong on its own. run exits 1 and adds no log line.
var errReported = errors.New("reported")

// commands maps a subcommand name to its handler.
var commands = map[string]command{
	"version": func(_ context.Context, _ []string) error {
		fmt.Println("gobb " + version.Version)
		return nil
	},
	"ban":         banCommand,
	"import":      importBundle,
	"mail":        mailCommand,
	"messages":    messagesCommand,
	"migrate":     migrate,
	"perms":       perms,
	"serve":       serve,
	"user":        user,
	"healthcheck": healthcheck,
}

// migrate applies the pending schema migrations and logs each file it applied.
func migrate(ctx context.Context, _ []string) error {
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	return applyMigrations(ctx, pool)
}

// importBundle loads a bundle into the database and the data directory. It
// applies the pending migrations first. On success it prints the number of rows
// it loaded from each data file, in bundle.Files order, then for each optional
// data file (0 when the bundle does not hold it), then the number of files.
func importBundle(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, "usage: gobb import [--replace] [--thread-messages] <bundle>") }
	replace := fs.Bool("replace", false, "empty the database and move the old files aside first")
	thread := fs.Bool("thread-messages", false,
		"join private messages that the bundle links to no conversation, by people and subject")
	if err := fs.Parse(args); err != nil {
		// The flag package has printed the message and the usage.
		return errUsage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errUsage
	}
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := applyMigrations(ctx, pool); err != nil {
		return err
	}
	counts, err := importer.Run(ctx, pool, fs.Arg(0), importer.Options{
		DataDir: cfg.DataDir, Replace: *replace, ThreadMessages: *thread,
	})
	if err != nil {
		return err
	}
	bumpPageEpoch(ctx, slog.Default(), pool)
	for _, name := range slices.Concat(bundle.Files, bundle.OptionalFiles, []string{"files"}) {
		if _, err := fmt.Fprintf(stdout, "%-16s %d\n", name, counts[name]); err != nil {
			return err
		}
	}
	return nil
}

// bumpPageEpoch tells a running server that this command changed the board:
// the server empties its guest page cache when it next reads the epoch (every
// few seconds). The write is done already, so a failure here does not fail the
// command; it says what to do.
func bumpPageEpoch(ctx context.Context, log *slog.Logger, q db.Querier) {
	if err := board.BumpPageEpoch(ctx, q); err != nil {
		log.Warn("the change is saved, but a running server may show old guest pages until it restarts", "err", err)
	}
}

// perms runs a permissions subcommand. The only one is "export".
func perms(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] != "export" {
		fmt.Fprintln(os.Stderr, "usage: gobb perms export")
		return errUsage
	}
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	// A read-only snapshot, so that the export is one view of the board. The
	// command does not apply migrations: an unmigrated database is an error.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var migrated bool
	if err := tx.QueryRow(ctx, `select to_regclass('schema_migrations') is not null`).Scan(&migrated); err != nil {
		return err
	}
	if !migrated {
		return errors.New("perms export: the database has no schema; run gobb migrate first")
	}
	return acldb.Export(ctx, tx, stdout)
}

// applyMigrations runs the pending migrations and logs each file it applied.
func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	applied, err := db.Migrate(ctx, pool)
	for _, name := range applied {
		slog.Info("applied migration", "file", name)
	}
	if err != nil {
		return err
	}
	slog.Info("migrations done", "applied", len(applied))
	return nil
}

// newHTTPServer returns the server for h, with the limits that protect a small
// host from slow and oversized requests.
func newHTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

// serve sets the Go memory limit, migrates the schema, then serves HTTP until
// SIGTERM or SIGINT.
// If a migration fails, it returns before the server listens. The signal
// handler is set up first, so a signal during the connect or the migrations
// stops the command too.
func serve(ctx context.Context, _ []string) error {
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)
	return serveWith(ctx, sigs, os.Exit)
}

// serveWith is serve with its signal channel and its exit function given. A
// signal that arrives before the server listens cancels the setup and makes
// serveWith return nil; a second one calls exit(1).
func serveWith(ctx context.Context, sigs <-chan os.Signal, exit func(int)) error {
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		return err
	}
	mem := memlimit.Apply()
	gate := gateFor(mem)
	cfg.Load.PageCacheBytes = capPageCache(cfg.Load.PageCacheBytes, mem.Limit)
	setupCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var interrupted atomic.Bool
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case <-sigs:
			interrupted.Store(true)
			cancel()
			// A second signal during the setup ends the process.
			select {
			case <-sigs:
				exit(1)
			case <-done:
			}
		case <-done:
			// A signal that came with the end of the setup still counts.
			select {
			case <-sigs:
				interrupted.Store(true)
			default:
			}
		}
	}()
	var waker mailWaker
	defense := login.NewDefense(defenseConfig(cfg.Signin))
	sv, err := setup(setupCtx, cfg, gate, defense, waker.wake)
	pool, h := sv.pool, sv.handler
	close(done)
	<-exited
	if interrupted.Load() {
		slog.Info("signal received during startup; stopping")
		if pool != nil {
			pool.Close()
		}
		return nil
	}
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		pool.Close()
		return err
	}
	slog.Info("listening", "addr", ln.Addr().String(), "base_path", cfg.BasePath)
	// The metrics listener starts with the server, and stops before the pool
	// closes: some of its gauges read the pool.
	registerServeMetrics(pool, gate, sv.guest, sv.writes)
	stopMetrics, _, err := startMetrics(cfg.MetricsListen)
	if err != nil {
		_ = ln.Close()
		pool.Close()
		return err
	}
	// The cleanup stops with the server. The pool closes after it has stopped.
	cleanCtx, stopClean := context.WithCancel(ctx)
	cleaned := make(chan struct{})
	go func() {
		defer close(cleaned)
		ticker := time.NewTicker(cleanupEvery)
		defer ticker.Stop()
		cleanupLoop(cleanCtx, ticker.C, func(c context.Context) { cleanup(c, pool, time.Now, cfg.Signin.AddressKeep) })
	}()
	// Defensive Mode turns off on a timer too, so its "off" line comes on time
	// when no sign-in arrives. It stops with the cleanup.
	refreshed := make(chan struct{})
	go func() {
		defer close(refreshed)
		ticker := time.NewTicker(defenseRefreshEvery)
		defer ticker.Stop()
		for {
			select {
			case <-cleanCtx.Done():
				return
			case <-ticker.C:
				defense.Refresh(time.Now())
			}
		}
	}()
	// The batched writes flush on a timer, and the guest page cache renders and
	// watches the epoch, until the cleanup stops. The last flush comes after the
	// server has stopped, so the writes of the last requests are in it.
	workers := make(chan struct{})
	go func() {
		defer close(workers)
		cacheDone := make(chan struct{})
		go func() {
			defer close(cacheDone)
			if sv.guest != nil {
				sv.guest.Run(cleanCtx)
			}
		}()
		sv.writes.Run(cleanCtx, pool, cfg.Load.FlushEvery)
		<-cacheDone
	}()
	// The email worker stops with the server, before the pool closes. The web layer
	// wakes it through waker, with a send that never blocks. The server does not
	// serve before this point, so no request finds the worker missing.
	wake, stopMail := startMail(ctx, pool, cfg.Mail)
	waker.set(wake)
	closePool := func() {
		metricsCtx, cancelMetrics := context.WithTimeout(context.Background(), metricsStopTimeout)
		if err := stopMetrics(metricsCtx); err != nil {
			slog.Warn("metrics listener did not stop cleanly", "err", err)
		}
		cancelMetrics()
		stopMail()
		stopClean()
		<-cleaned
		<-refreshed
		<-workers
		finalFlush(sv.writes, pool)
		pool.Close()
	}
	return serveUntil(ctx, newHTTPServer(h), ln, closePool, sigs, exit)
}

// mailWaker passes a wake-up from the web layer to the email worker. The channel
// is set once the worker runs; a wake before that, or with email off, does
// nothing.
type mailWaker struct {
	ch atomic.Pointer[chan<- struct{}]
}

func (m *mailWaker) set(ch chan<- struct{}) {
	if ch != nil {
		m.ch.Store(&ch)
	}
}

// wake sends on the channel without blocking: a value already waiting is
// enough, because one pass of the worker claims every due row.
func (m *mailWaker) wake() {
	if ch := m.ch.Load(); ch != nil {
		select {
		case *ch <- struct{}{}:
		default:
		}
	}
}

// startMail starts the email worker when m is not nil. It returns the channel
// that wakes the worker, which has room for one value, and a function that stops
// the worker and waits for it to end. A send that is in progress is not a
// failure of its message: the worker leaves the row, and its lease runs out.
// With m nil, no worker runs, and the channel is nil, so a non-blocking send on
// it always falls to the default case.
func startMail(ctx context.Context, pool *pgxpool.Pool, m *config.Mail) (chan<- struct{}, func()) {
	if m == nil {
		return nil, func() {}
	}
	wake := make(chan struct{}, 1)
	sender := mail.NewSMTP(*m)
	sender.RootCAs = mailRoots
	q := &mail.Queue{Pool: pool, Sender: sender, Wake: wake}
	mailCtx, stop := context.WithCancel(ctx)
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		q.Run(mailCtx)
	}()
	slog.Info("email is on", "host", m.Host, "port", m.Port, "tls", m.TLS)
	return wake, func() {
		stop()
		<-ended
	}
}

// metricsStopTimeout is how long the metrics listener has to stop at shutdown.
const metricsStopTimeout = 2 * time.Second

// cleanupEvery is the time between two runs of the session cleanup.
const cleanupEvery = time.Hour

// defenseRefreshEvery is the time between two checks of whether Defensive Mode
// may turn off.
const defenseRefreshEvery = 10 * time.Second

// cleanupLoop calls run once at once, then once for each value on tick, until
// ctx ends. A run that has started finishes; none starts after ctx ends.
func cleanupLoop(ctx context.Context, tick <-chan time.Time, run func(context.Context)) {
	for {
		if ctx.Err() != nil {
			return
		}
		run(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick:
		}
	}
}

// cleanup deletes the expired sessions, the old user_ips rows, the old email rows
// and the expired reset links. It logs one count line for each step. A failure
// is logged and does not stop the server or the later steps.
//
// keep is how long a member's address stays in user_ips after its last use
// (GOBB_ADDRESS_KEEP_DAYS); zero or less is the default of one year.
func cleanup(ctx context.Context, q db.Querier, now func() time.Time, keep time.Duration) {
	sessions, ips, err := session.Cleanup(ctx, q, now(), keep)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("session cleanup failed", "err", err)
		}
	} else {
		slog.Info("session cleanup", "sessions", sessions, "ips", ips)
	}
	rows, err := mail.Cleanup(ctx, q, now())
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("email cleanup failed", "err", err)
		}
	} else {
		slog.Info("email cleanup", "rows", rows)
	}
	links, err := board.CleanupResets(ctx, q, now())
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("reset link cleanup failed", "err", err)
		}
	} else {
		slog.Info("reset link cleanup", "links", links)
	}
	undo, err := board.CleanupUndo(ctx, q, now())
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("undo cleanup failed", "err", err)
		}
		return
	}
	slog.Info("undo cleanup", "rows", undo)
}

// gateFor logs the Go memory limit of the process (see [memlimit.Apply]), its
// source and the gate budget that follows from it ([login.BudgetKiB]), in one
// INFO line. It returns the gate of the password checks, with that budget.
func gateFor(m memlimit.Result) *login.Gate {
	budget := login.BudgetKiB(m.Limit)
	slog.Info("memory limit", "limit_bytes", m.Limit, "source", m.Source, "gate_budget_kib", budget)
	return login.NewGate(budget)
}

// maxPageCacheNoLimit is the most memory of the guest page cache when the
// process has no memory limit.
const maxPageCacheNoLimit = 64 << 20

// capPageCache returns the memory of the guest page cache: want
// (GOBB_PAGE_CACHE_MIB), at most a quarter of the Go memory limit limit, or at
// most 64 MiB when there is no limit. The rest of the memory is for the
// requests, the password checks and the runtime. A cut is logged.
func capPageCache(want, limit int64) int64 {
	most := int64(maxPageCacheNoLimit)
	if limit > 0 {
		most = limit / 4
	}
	if want > most {
		slog.Warn("GOBB_PAGE_CACHE_MIB is more than the memory allows; using less",
			"want_mib", want>>20, "using_mib", most>>20, "memory_limit_bytes", limit)
		return most
	}
	return want
}

// flushTimeout bounds the last flush of the batched writes at shutdown. It fits
// in the shutdown window with the other steps of closePool.
const flushTimeout = 5 * time.Second

// finalFlush writes what the batched writes still hold, before the pool closes.
func finalFlush(w *batch.Writer, pool *pgxpool.Pool) {
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	if err := w.Flush(ctx, pool); err != nil {
		slog.Error("batched writes: the last flush failed; these view counts and visit times are lost", "err", err)
		return
	}
	slog.Info("batched writes: flushed at shutdown")
}

// served is what setup builds: the pool, the handler, the batched writes and
// the guest page cache (nil when it is off).
type served struct {
	pool    *pgxpool.Pool
	handler http.Handler
	writes  *batch.Writer
	guest   *web.GuestCache
}

// setup opens the pool, applies the migrations and builds the handler, with
// gate as the memory gate of the password checks. On an error it closes the
// pool and returns a zero served.
func setup(ctx context.Context, cfg config.Config, gate *login.Gate, defense *login.Defense, wake func()) (served, error) {
	pool, err := db.OpenSized(ctx, cfg.DSN, cfg.Load.DBMaxConns)
	if err != nil {
		return served{}, err
	}
	if err := applyMigrations(ctx, pool); err != nil {
		pool.Close()
		return served{}, err
	}
	sv := served{pool: pool, writes: batch.New()}
	if cfg.Load.PageCacheBytes > 0 {
		sv.guest = web.NewGuestCache(cfg.Load.PageCacheBytes, cfg.Load.PageCacheWarm)
	}
	opt := web.Options{
		BasePath:       cfg.BasePath,
		DataDir:        cfg.DataDir,
		TrustedProxies: cfg.TrustedProxies,
		CookieSecure:   cfg.CookieSecure,
		LoginGate:      gate,
		PublicURL:      cfg.PublicURL,

		PoolWait:         cfg.Load.DBPoolWait,
		SearchPerSecond:  cfg.Load.SearchPerSecond,
		SearchMaxRunning: cfg.Load.SearchMaxRunning,
		LoginGateWait:    cfg.Load.SigninGateWait,
		MaxInFlight:      cfg.Load.MaxInFlight,

		Signin:  scheduleConfig(cfg.Signin),
		Defense: defense,

		GuestCache: sv.guest,
		Writes:     sv.writes,
	}
	slog.Info("sign-in schedule", "free_failures", cfg.Signin.FreeFailures, "waits", durations(cfg.Signin.Waits),
		"last_wait", cfg.Signin.LastWait.String(), "reset_after", cfg.Signin.ResetAfter.String(),
		"address_keep_days", int(cfg.Signin.AddressKeep/(24*time.Hour)),
		"defensive_failures", cfg.Signin.DefensiveFailures, "defensive_window", cfg.Signin.DefensiveWindow.String(),
		"defensive_quiet", cfg.Signin.DefensiveQuiet.String(), "defensive_gap", cfg.Signin.DefensiveGap.String(),
		"table_keys", cfg.Signin.TableKeys)
	slog.Info("load limits", "db_max_conns", cfg.Load.DBMaxConns, "db_pool_wait", cfg.Load.DBPoolWait.String(),
		"search_per_second", cfg.Load.SearchPerSecond, "search_max_running", cfg.Load.SearchMaxRunning,
		"page_cache_mib", cfg.Load.PageCacheBytes>>20, "page_cache_warm", cfg.Load.PageCacheWarm,
		"write_flush_interval", cfg.Load.FlushEvery.String(),
		"signin_gate_wait", cfg.Load.SigninGateWait.String(), "max_in_flight", cfg.Load.MaxInFlight)
	// With no email settings, Mail stays nil and the reset pages are off.
	if cfg.Mail != nil {
		sender := mail.NewSMTP(*cfg.Mail)
		sender.RootCAs = mailRoots
		opt.Mail = &web.MailOptions{PublicURL: cfg.Mail.PublicURL, Wake: wake, PerHour: cfg.Mail.PerHour, PerDay: cfg.Mail.PerDay, Sender: sender}
	}
	h, err := web.New(ctx, pool, opt)
	if err != nil {
		pool.Close()
		return served{}, err
	}
	sv.handler = h
	return sv, nil
}

// scheduleConfig returns the sign-in schedule of the settings s.
func scheduleConfig(s config.Signin) *login.ScheduleConfig {
	return &login.ScheduleConfig{FreeFailures: s.FreeFailures, Waits: s.Waits, LastWait: s.LastWait,
		ResetAfter: s.ResetAfter, MaxKeys: s.TableKeys}
}

// defenseConfig returns the settings of Defensive Mode of the settings s.
func defenseConfig(s config.Signin) login.DefenseConfig {
	return login.DefenseConfig{Failures: s.DefensiveFailures, Window: s.DefensiveWindow, Quiet: s.DefensiveQuiet, Gap: s.DefensiveGap}
}

// durations returns ds as one text, such as "15s,15s,1m0s", for a log line.
func durations(ds []time.Duration) string {
	parts := make([]string, len(ds))
	for i, d := range ds {
		parts[i] = d.String()
	}
	return strings.Join(parts, ",")
}

// serveUntil serves srv on ln until ctx ends, a signal arrives on sigs, or the
// server fails. It then stops the server and calls closePool, all inside the
// shutdownTimeout window. A second signal during the stop calls exit(1) at
// once.
//
// When the server does not stop inside the window, serveUntil closes its
// connections, gives closePool the short poolGrace, and returns the shutdown
// error. It adds the pool error only when the pool also fails to close. When
// the server stops in time but closePool does not return by the end of the
// window, the error names the pool.
func serveUntil(ctx context.Context, srv *http.Server, ln net.Listener, closePool func(), sigs <-chan os.Signal, exit func(int)) error {
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		// The server stopped by itself, so no request is open.
		closePool()
		return err
	case <-ctx.Done():
	case sig := <-sigs:
		slog.Info("signal received", "signal", sig.String())
	}
	slog.Info("shutting down")

	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-sigs:
			slog.Warn("second signal: exiting now")
			exit(1)
		case <-stopped:
		}
	}()

	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutErr := srv.Shutdown(shutCtx)
	poolCtx := shutCtx
	if shutErr != nil {
		// Drop the connections that stayed open, so the pool can close. The
		// window is spent, so the pool gets a short grace of its own.
		_ = srv.Close()
		shutErr = fmt.Errorf("shutdown: %w", shutErr)
		var cancelPool context.CancelFunc
		poolCtx, cancelPool = context.WithTimeout(context.Background(), poolGrace)
		defer cancelPool()
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	closed := make(chan struct{})
	go func() {
		closePool()
		close(closed)
	}()
	select {
	case <-closed:
		return shutErr
	case <-poolCtx.Done():
		poolErr := fmt.Errorf("closing the database pool did not finish within %s", shutdownTimeout)
		if shutErr != nil {
			poolErr = fmt.Errorf("closing the database pool did not finish within %s", poolGrace)
		}
		return errors.Join(shutErr, poolErr)
	}
}

// healthcheck requests /healthz on the local listen address. It exits 0 on
// 200 and fails otherwise. The static image has no shell or curl, so the
// binary checks itself. It reads only GOBB_LISTEN, not GOBB_DSN.
func healthcheck(ctx context.Context, _ []string) error {
	listen := strings.TrimSpace(os.Getenv("GOBB_LISTEN"))
	if listen == "" {
		listen = defaultListen
	}
	url, err := web.HealthURL(listen)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, healthcheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: %s answered %s", url, resp.Status)
	}
	return nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	os.Exit(run(context.Background(), os.Args[1:]))
}

// run dispatches on the first argument and returns the process exit code.
func run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "gobb: unknown command %q\n", args[0])
		usage()
		return 2
	}
	if err := cmd(ctx, args[1:]); err != nil {
		if errors.Is(err, errUsage) {
			return 2
		}
		if errors.Is(err, errReported) {
			return 1
		}
		slog.Error("command failed", "command", args[0], "err", err)
		return 1
	}
	return 0
}

func usage() {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Fprintf(os.Stderr, "usage: gobb <command>\ncommands: %s\n", strings.Join(names, ", "))
}
