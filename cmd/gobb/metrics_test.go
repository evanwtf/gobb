package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/internal/config"
	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/metrics"
	"github.com/evanwtf/gobb/internal/version"
)

// With no address, no metrics listener starts.
func TestStartMetricsOff(t *testing.T) {
	stop, bound, err := startMetrics("")
	if err != nil || bound != "" {
		t.Fatalf("startMetrics(\"\") = %q, %v; want no listener", bound, err)
	}
	if err := stop(context.Background()); err != nil {
		t.Errorf("stop = %v", err)
	}
}

func TestStartMetricsBadAddress(t *testing.T) {
	if _, _, err := startMetrics("127.0.0.1:not-a-port"); err == nil || !strings.Contains(err.Error(), "metrics listener") {
		t.Errorf("err = %v, want a metrics listener error", err)
	}
}

// The listener serves the registry of the process: the request counters of the
// board, the build info, the gauges that read the database, the pool and the
// gate, and the runtime. It serves nothing else. When GOBB_METRICS_SAMPLE names
// a file, the test writes the scrape there, and CI runs promtool on it.
func TestMetricsEndpoint(t *testing.T) {
	userBoard(t)
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	gate := login.NewGate(1 << 17)
	pool, h, err := setup(t.Context(), cfg, gate, func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	registerServeMetrics(pool, gate)
	before := metrics.HTTPRequests.Value("/{$}", "GET", "2xx")
	for _, path := range []string{"/", "/t/999999"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if path == "/" && rec.Code != 200 {
			t.Fatalf("GET / = %d", rec.Code)
		}
	}
	if got := metrics.HTTPRequests.Value("/{$}", "GET", "2xx"); got != before+1 {
		t.Errorf("index requests = %d, want %d", got, before+1)
	}

	stop, bound, err := startMetrics("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stop(context.Background()) })
	resp, err := http.Get("http://" + bound + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != metrics.ContentType {
		t.Fatalf("GET /metrics = %d, %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	out := string(body)
	for _, want := range []string{
		`gobb_http_requests_total{route="/{$}",method="GET",code="2xx"} `,
		`gobb_http_requests_total{route="/t/{id}",method="GET",code="4xx"} `,
		`gobb_http_request_duration_seconds_bucket{route="/{$}",le="+Inf"} `,
		`gobb_build_info{version="` + version.Version + `",commit="`,
		"\ngobb_email_queue_depth 0\n",
		"\ngobb_sessions_active 0\n",
		"\ngobb_memory_gate_budget_bytes 134217728\n",
		"\ngobb_memory_gate_holders 0\n",
		"\ngobb_db_pool_conns ",
		"\ngobb_db_pool_acquires_total ",
		"\ngobb_signin_floor_seconds ",
		"\ngo_goroutines ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the scrape lacks %q", want)
		}
	}
	if strings.Contains(out, "/t/999999") {
		t.Error("a path value reached a label")
	}
	if f := os.Getenv("GOBB_METRICS_SAMPLE"); f != "" {
		if err := os.WriteFile(f, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		method, path string
		code         int
	}{{"POST", "/metrics", 405}, {"GET", "/", 404}, {"GET", "/healthz", 404}} {
		req, _ := http.NewRequest(c.method, "http://"+bound+c.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.code {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, resp.StatusCode, c.code)
		}
	}
}

// The address line: INFO for loopback, private and every-interface addresses,
// WARN for a public one.
func TestLogMetricsAddr(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	for addr, want := range map[string]string{
		"127.0.0.1:9187":   "level=INFO",
		"10.0.0.5:9187":    "level=INFO",
		"[::]:9187":        "every interface",
		"0.0.0.0:9187":     "every interface",
		"localhost:9187":   "level=INFO",
		"203.0.113.9:9187": "level=WARN",
	} {
		buf.Reset()
		logMetricsAddr(addr)
		out := strings.TrimSpace(buf.String())
		if strings.Count(out, "\n") != 0 || !strings.Contains(out, want) || !strings.Contains(out, addr) {
			t.Errorf("%s: log = %q, want one line with %q", addr, out, want)
		}
	}
}
