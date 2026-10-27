// Package metrics keeps the counters, gauges and histograms of gobb and writes
// them in the Prometheus text format, version 0.0.4. It uses no client library:
// the format is small, and the spec keeps the list of dependencies short.
//
// A label holds a fixed value only: a route pattern, a method, a status class, a
// result name. Never a name, an ID, an address or text that a member typed. The
// number of series stays bounded.
package metrics

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ContentType is the media type of the text format.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// Registry holds metric families by name. A Registry is safe for concurrent use.
type Registry struct {
	mu   sync.Mutex
	fams map[string]family
}

// family is one metric name with its HELP, its TYPE and its samples.
type family interface {
	write(w *bufio.Writer)
	kind() string
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry { return &Registry{fams: map[string]family{}} }

// Default is the registry of the process. `gobb serve` serves it on the metrics
// listener.
var Default = NewRegistry()

// register adds f under name. A second family of the same type replaces the
// first, so a test that builds the server twice registers again with no error. A
// family of another type under a name in use is a bug, and panics.
func (r *Registry) register(name string, f family) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.fams[name]; ok && old.kind() != f.kind() {
		panic("metrics: " + name + " is a " + old.kind() + ", not a " + f.kind())
	}
	r.fams[name] = f
}

// WriteTo writes every family in the text format, sorted by name.
func (r *Registry) WriteTo(w io.Writer) (int64, error) {
	r.mu.Lock()
	names := make([]string, 0, len(r.fams))
	for n := range r.fams {
		names = append(names, n)
	}
	fams := make([]family, 0, len(names))
	slices.Sort(names)
	for _, n := range names {
		fams = append(fams, r.fams[n])
	}
	r.mu.Unlock()
	cw := &countWriter{w: w}
	bw := bufio.NewWriter(cw)
	for _, f := range fams {
		f.write(bw)
	}
	err := bw.Flush()
	return cw.n, err
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// Handler serves the registry at GET and HEAD /metrics. Every other path is 404,
// and every other method on /metrics is 405.
func Handler(r *Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/metrics" {
			http.NotFound(w, req)
			return
		}
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", ContentType)
		w.Header().Set("Cache-Control", "no-store")
		if req.Method == http.MethodHead {
			return
		}
		_, _ = r.WriteTo(w)
	})
}

// meta is the name and the help text of a family.
type meta struct {
	name, help, typ string
}

func (m meta) kind() string { return m.typ }

func (m meta) header(w *bufio.Writer) {
	w.WriteString("# HELP ")
	w.WriteString(m.name)
	w.WriteByte(' ')
	w.WriteString(escapeHelp(m.help))
	w.WriteString("\n# TYPE ")
	w.WriteString(m.name)
	w.WriteByte(' ')
	w.WriteString(m.typ)
	w.WriteByte('\n')
}

// sample writes one line: name, the labels, the value.
func sample(w *bufio.Writer, name string, names, values []string, extra string, v float64) {
	w.WriteString(name)
	if len(names) > 0 || extra != "" {
		w.WriteByte('{')
		for i, n := range names {
			if i > 0 {
				w.WriteByte(',')
			}
			w.WriteString(n)
			w.WriteString(`="`)
			w.WriteString(escapeLabel(values[i]))
			w.WriteByte('"')
		}
		if extra != "" {
			if len(names) > 0 {
				w.WriteByte(',')
			}
			w.WriteString(extra)
		}
		w.WriteByte('}')
	}
	w.WriteByte(' ')
	w.WriteString(formatFloat(v))
	w.WriteByte('\n')
}

var (
	helpEscaper  = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	labelEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)
)

func escapeHelp(s string) string  { return helpEscaper.Replace(s) }
func escapeLabel(s string) string { return labelEscaper.Replace(s) }

func formatFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	case v == math.Trunc(v) && math.Abs(v) < 1e15:
		// A whole number prints as one: 134217728, not 1.34217728e+08.
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// key joins label values into a map key. 0xff is not valid UTF-8, so no label
// value holds it.
func key(values []string) string { return strings.Join(values, "\xff") }

func checkValues(m meta, labels, values []string) {
	if len(values) != len(labels) {
		panic("metrics: " + m.name + " takes " + strconv.Itoa(len(labels)) + " label values, got " + strconv.Itoa(len(values)))
	}
}

// CounterVec is a counter with a fixed list of label names. With no label
// names, it is one plain counter.
type CounterVec struct {
	meta
	labels []string
	mu     sync.RWMutex
	series map[string]*counterSeries
}

type counterSeries struct {
	values []string
	n      atomic.Uint64
}

// CounterVec registers a counter family. The name must end in _total.
func (r *Registry) CounterVec(name, help string, labels ...string) *CounterVec {
	c := &CounterVec{meta: meta{name, help, "counter"}, labels: labels, series: map[string]*counterSeries{}}
	if len(labels) == 0 {
		c.get(nil)
	}
	r.register(name, c)
	return c
}

func (c *CounterVec) get(values []string) *counterSeries {
	checkValues(c.meta, c.labels, values)
	k := key(values)
	c.mu.RLock()
	s := c.series[k]
	c.mu.RUnlock()
	if s != nil {
		return s
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if s = c.series[k]; s == nil {
		s = &counterSeries{values: slices.Clone(values)}
		c.series[k] = s
	}
	return s
}

// Init creates the series of values at 0, so that it shows before its first
// event and a rate over it starts from a known point.
func (c *CounterVec) Init(values ...string) { c.get(values) }

// Inc adds 1 to the series of values.
func (c *CounterVec) Inc(values ...string) { c.get(values).n.Add(1) }

// Add adds n to the series of values.
func (c *CounterVec) Add(n uint64, values ...string) { c.get(values).n.Add(n) }

// Value returns the count of the series of values.
func (c *CounterVec) Value(values ...string) uint64 { return c.get(values).n.Load() }

func (c *CounterVec) write(w *bufio.Writer) {
	c.meta.header(w)
	c.mu.RLock()
	all := make([]*counterSeries, 0, len(c.series))
	for _, s := range c.series {
		all = append(all, s)
	}
	c.mu.RUnlock()
	slices.SortFunc(all, func(a, b *counterSeries) int { return strings.Compare(key(a.values), key(b.values)) })
	for _, s := range all {
		sample(w, c.name, c.labels, s.values, "", float64(s.n.Load()))
	}
}

// Gauge is a value that goes up and down. It has no labels.
type Gauge struct {
	meta
	bits atomic.Uint64
}

// Gauge registers a gauge.
func (r *Registry) Gauge(name, help string) *Gauge {
	g := &Gauge{meta: meta{name, help, "gauge"}}
	r.register(name, g)
	return g
}

// Set sets the gauge to v.
func (g *Gauge) Set(v float64) { g.bits.Store(math.Float64bits(v)) }

// Value returns the value of the gauge.
func (g *Gauge) Value() float64 { return math.Float64frombits(g.bits.Load()) }

func (g *Gauge) write(w *bufio.Writer) {
	g.meta.header(w)
	sample(w, g.name, nil, nil, "", g.Value())
}

// funcFamily reads its one value when the registry is written. When ok is false,
// the family writes its HELP and TYPE lines and no sample.
type funcFamily struct {
	meta
	labels, values []string
	f              func() (float64, bool)
}

func (f *funcFamily) write(w *bufio.Writer) {
	f.meta.header(w)
	if v, ok := f.f(); ok {
		sample(w, f.name, f.labels, f.values, "", v)
	}
}

// GaugeFunc registers a gauge whose value f gives at each scrape.
func (r *Registry) GaugeFunc(name, help string, f func() (float64, bool)) {
	r.register(name, &funcFamily{meta: meta{name, help, "gauge"}, f: f})
}

// CounterFunc registers a counter whose value f gives at each scrape. The name
// must end in _total, and the value must never go down.
func (r *Registry) CounterFunc(name, help string, f func() (float64, bool)) {
	r.register(name, &funcFamily{meta: meta{name, help, "counter"}, f: f})
}

// Sample is one series of a family that [Registry.GaugeSamples] reads: the label
// values, in the order of the label names, and the value.
type Sample struct {
	Values []string
	Value  float64
}

// GaugeSpec is the name and the help text of one gauge in a group.
type GaugeSpec struct{ Name, Help string }

// samplesGroup is one or more gauge families with the same label names. One call
// of f reads the series of all of them, so the families of a scrape agree.
type samplesGroup struct {
	meta   // the first family: the group is registered under its name
	labels []string
	specs  []GaugeSpec // sorted by name
	f      func() [][]Sample
}

func (g *samplesGroup) write(w *bufio.Writer) {
	rows := g.f()
	for i, sp := range g.specs {
		meta{sp.Name, sp.Help, "gauge"}.header(w)
		if i >= len(rows) {
			continue
		}
		// f owns the slice that it returned, so sort a copy.
		all := slices.DeleteFunc(slices.Clone(rows[i]), func(x Sample) bool { return len(x.Values) != len(g.labels) })
		slices.SortStableFunc(all, func(a, b Sample) int { return strings.Compare(key(a.Values), key(b.Values)) })
		for _, x := range all {
			sample(w, sp.Name, g.labels, x.Values, "", x.Value)
		}
	}
}

// GaugeSamples registers a gauge with labels, whose series f gives at each
// scrape. The series appear sorted by label values. f returns no series when
// there is none, and the family then writes its HELP and TYPE lines only. A
// series whose number of values differs from the number of label names is left
// out.
func (r *Registry) GaugeSamples(name, help string, labels []string, f func() []Sample) {
	r.GaugeGroup(labels, []GaugeSpec{{name, help}}, func() [][]Sample { return [][]Sample{f()} })
}

// GaugeGroup registers several gauges with the same labels that one call of f
// fills at each scrape: f returns the series of each spec, in the order of
// specs. The gauges of a group come from one read, so a scrape cannot show two
// of them at two moments. The group writes its families sorted by name, one
// after the other, and r holds it under the lowest name. Each name must be new
// or belong to an earlier group of the same lowest name.
func (r *Registry) GaugeGroup(labels []string, specs []GaugeSpec, f func() [][]Sample) {
	if len(specs) == 0 {
		panic("metrics: a gauge group has no gauge")
	}
	order := make([]int, len(specs))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return strings.Compare(specs[a].Name, specs[b].Name) })
	sorted := make([]GaugeSpec, len(specs))
	for i, j := range order {
		sorted[i] = specs[j]
	}
	g := &samplesGroup{meta: meta{sorted[0].Name, sorted[0].Help, "gauge"}, labels: slices.Clone(labels), specs: sorted,
		f: func() [][]Sample {
			// Hand the rows back in the order of the sorted specs.
			rows := f()
			out := make([][]Sample, len(rows))
			for i, j := range order {
				if j < len(rows) {
					out[i] = rows[j]
				}
			}
			return out
		}}
	r.register(sorted[0].Name, g)
}

// Info registers a gauge with the value 1 and fixed labels, such as the version
// of the build.
func (r *Registry) Info(name, help string, labels, values []string) {
	checkValues(meta{name: name}, labels, values)
	r.register(name, &funcFamily{meta: meta{name, help, "gauge"}, labels: labels, values: slices.Clone(values),
		f: func() (float64, bool) { return 1, true }})
}

// HistogramVec counts observations in fixed buckets, per series of label values.
type HistogramVec struct {
	meta
	labels  []string
	buckets []float64
	mu      sync.RWMutex
	series  map[string]*histSeries
}

type histSeries struct {
	values []string
	// counts holds one count per bucket, not cumulative, and a last one for the
	// values above the largest bound.
	counts  []atomic.Uint64
	sumBits atomic.Uint64
	count   atomic.Uint64
}

// HistogramVec registers a histogram family with the upper bounds buckets, in
// increasing order. The +Inf bucket is implied.
func (r *Registry) HistogramVec(name, help string, buckets []float64, labels ...string) *HistogramVec {
	if !slices.IsSorted(buckets) {
		panic("metrics: the buckets of " + name + " are not in order")
	}
	h := &HistogramVec{meta: meta{name, help, "histogram"}, labels: labels, buckets: slices.Clone(buckets), series: map[string]*histSeries{}}
	r.register(name, h)
	return h
}

func (h *HistogramVec) get(values []string) *histSeries {
	checkValues(h.meta, h.labels, values)
	k := key(values)
	h.mu.RLock()
	s := h.series[k]
	h.mu.RUnlock()
	if s != nil {
		return s
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if s = h.series[k]; s == nil {
		s = &histSeries{values: slices.Clone(values), counts: make([]atomic.Uint64, len(h.buckets)+1)}
		h.series[k] = s
	}
	return s
}

// Init creates the series of values with no observation, so that it shows before
// its first one.
func (h *HistogramVec) Init(values ...string) { h.get(values) }

// Observe records v in the series of values.
func (h *HistogramVec) Observe(v float64, values ...string) {
	s := h.get(values)
	i, _ := slices.BinarySearch(h.buckets, v)
	s.counts[i].Add(1)
	for {
		old := s.sumBits.Load()
		if s.sumBits.CompareAndSwap(old, math.Float64bits(math.Float64frombits(old)+v)) {
			break
		}
	}
	s.count.Add(1)
}

// Count returns the number of observations in the series of values.
func (h *HistogramVec) Count(values ...string) uint64 { return h.get(values).count.Load() }

func (h *HistogramVec) write(w *bufio.Writer) {
	h.meta.header(w)
	h.mu.RLock()
	all := make([]*histSeries, 0, len(h.series))
	for _, s := range h.series {
		all = append(all, s)
	}
	h.mu.RUnlock()
	slices.SortFunc(all, func(a, b *histSeries) int { return strings.Compare(key(a.values), key(b.values)) })
	for _, s := range all {
		// Read the count first: an Observe that runs during the write adds to a
		// bucket before the count, so no bucket is below the count written here.
		count := s.count.Load()
		var cum uint64
		for i, b := range h.buckets {
			cum += s.counts[i].Load()
			sample(w, h.name+"_bucket", h.labels, s.values, `le="`+formatFloat(b)+`"`, float64(min(cum, count)))
		}
		sample(w, h.name+"_bucket", h.labels, s.values, `le="+Inf"`, float64(count))
		sample(w, h.name+"_sum", h.labels, s.values, "", math.Float64frombits(s.sumBits.Load()))
		sample(w, h.name+"_count", h.labels, s.values, "", float64(count))
	}
}

// Cached returns a value function for [Registry.GaugeFunc] that runs f at most
// once in every, with timeout as its limit, and gives the last result between
// runs. A failed run logs one WARN line that names the gauge, and the gauge has
// no sample until a run succeeds. A gauge that needs a query uses it, so a scrape
// costs the database one query per gauge in each interval at most.
func Cached(name string, every, timeout time.Duration, f func(context.Context) (float64, error)) func() (float64, bool) {
	var (
		mu   sync.Mutex
		last time.Time
		val  float64
		ok   bool
	)
	return func() (float64, bool) {
		mu.Lock()
		defer mu.Unlock()
		if !last.IsZero() && time.Since(last) < every {
			return val, ok
		}
		last = time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		v, err := f(ctx)
		if err != nil {
			slog.Warn("metrics: gauge failed", "gauge", name, "err", err)
			val, ok = 0, false
			return val, ok
		}
		val, ok = v, true
		return val, ok
	}
}
