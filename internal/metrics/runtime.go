package metrics

import (
	"os"
	rm "runtime/metrics"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// startTime is the time the process started, near enough: the time this
// package was initialized.
var startTime = time.Now()

func init() { registerRuntime(Default) }

// registerRuntime adds the Go runtime and process metrics to r. The names follow
// the ones that Prometheus users know from other Go programs.
func registerRuntime(r *Registry) {
	gauge := func(name, help, metric string) {
		r.GaugeFunc(name, help, func() (float64, bool) { return readRuntime(metric) })
	}
	gauge("go_goroutines", "Goroutines that exist now.", "/sched/goroutines:goroutines")
	gauge("go_memstats_heap_alloc_bytes", "Bytes of live and not yet collected heap objects.", "/memory/classes/heap/objects:bytes")
	gauge("go_memstats_sys_bytes", "Bytes of memory that the Go runtime got from the system.", "/memory/classes/total:bytes")
	gauge("go_gc_heap_goal_bytes", "The heap size that the next collection aims for.", "/gc/heap/goal:bytes")
	gauge("go_memory_limit_bytes", "The Go memory limit (GOMEMLIMIT or the limit gobb sets at start).", "/gc/gomemlimit:bytes")
	r.CounterFunc("go_gc_cycles_total", "Garbage collections that completed.", func() (float64, bool) {
		return readRuntime("/gc/cycles/total:gc-cycles")
	})
	r.GaugeFunc("process_start_time_seconds", "Start time of the process, in seconds since the Unix epoch.", func() (float64, bool) {
		return float64(startTime.UnixNano()) / 1e9, true
	})
	r.CounterFunc("process_cpu_seconds_total", "User and system CPU time of the process, in seconds.", func() (float64, bool) {
		var u syscall.Rusage
		if err := syscall.Getrusage(syscall.RUSAGE_SELF, &u); err != nil {
			return 0, false
		}
		return tv(u.Utime) + tv(u.Stime), true
	})
	r.GaugeFunc("process_resident_memory_bytes", "Resident memory of the process, in bytes (Linux only).", residentBytes)
}

func tv(t syscall.Timeval) float64 { return float64(t.Sec) + float64(t.Usec)/1e6 }

// readRuntime reads one runtime/metrics value as a float. A name that the runtime
// does not know gives no sample.
func readRuntime(name string) (float64, bool) {
	s := []rm.Sample{{Name: name}}
	rm.Read(s)
	switch s[0].Value.Kind() {
	case rm.KindUint64:
		return float64(s[0].Value.Uint64()), true
	case rm.KindFloat64:
		return s[0].Value.Float64(), true
	}
	return 0, false
}

// residentBytes reads the resident set from /proc/self/statm: its second field,
// in pages. Where that file does not exist, there is no sample.
func residentBytes() (float64, bool) {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseUint(f[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return float64(pages) * float64(os.Getpagesize()), true
}
