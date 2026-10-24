// Package memlimit sets the memory limit of the Go runtime at start.
//
// Without a limit, the Go runtime lets the heap grow to about twice the live
// heap before it collects. Under a container's memory cap, a burst of password
// checks then passes the cap, and the kernel kills the process. A memory limit
// below the cap makes the runtime collect sooner.
package memlimit

import (
	"math"
	"os"
	"path"
	"runtime/debug"
	"strconv"
	"strings"
)

// The sources of a limit.
const (
	// SourceEnv is a limit from the GOMEMLIMIT variable, which the Go runtime
	// reads itself at start.
	SourceEnv = "GOMEMLIMIT"
	// SourceCgroup is a limit that gobb sets from the memory limit of its
	// cgroup.
	SourceCgroup = "cgroup"
	// SourceNone means no limit.
	SourceNone = "none"
)

// cgroupPercent is the share of a cgroup memory limit that gobb gives the Go
// runtime. The rest covers what the runtime does not count: the stacks of
// threads, the binary, and pages that the runtime has freed but the kernel has
// not taken back yet.
const cgroupPercent = 70

// v1Unlimited is the least value of a cgroup v1 memory.limit_in_bytes that
// means "no limit". The kernel writes the largest page-aligned int64 there.
const v1Unlimited = 1 << 62

// Result is the memory limit of the process and where it came from.
type Result struct {
	// Limit is the limit in bytes. Zero means no limit.
	Limit int64
	// Source is SourceEnv, SourceCgroup or SourceNone.
	Source string
}

// Apply sets the memory limit of the Go runtime and returns it:
//   - When GOMEMLIMIT is set, the Go runtime has read it, and that value wins.
//     Apply changes nothing. GOMEMLIMIT=off gives a limit of 0 (none).
//   - Otherwise, when the process runs under a cgroup memory limit (cgroup v2
//     memory.max, or v1 memory.limit_in_bytes), Apply sets the limit to 70% of
//     it.
//   - Otherwise it sets no limit.
func Apply() Result {
	return apply(os.Getenv, os.ReadFile, debug.SetMemoryLimit)
}

// apply is [Apply] with its inputs given. setLimit is debug.SetMemoryLimit: a
// negative value reads the limit and changes nothing.
func apply(getenv func(string) string, readFile func(string) ([]byte, error), setLimit func(int64) int64) Result {
	r := Decide(getenv, readFile, setLimit(-1))
	if r.Source == SourceCgroup {
		setLimit(r.Limit)
	}
	return r
}

// Decide returns the limit that [Apply] sets, and its source, without a
// change. current is the limit of the runtime now.
func Decide(getenv func(string) string, readFile func(string) ([]byte, error), current int64) Result {
	if getenv("GOMEMLIMIT") != "" {
		if current == math.MaxInt64 {
			return Result{Source: SourceEnv}
		}
		return Result{Limit: current, Source: SourceEnv}
	}
	if limit, ok := CgroupLimit(readFile); ok {
		// limit × 70 / 100, in two parts so that no product can overflow.
		share := limit/100*cgroupPercent + limit%100*cgroupPercent/100
		return Result{Limit: share, Source: SourceCgroup}
	}
	return Result{Source: SourceNone}
}

// CgroupLimit returns the memory limit, in bytes, of the cgroup of this
// process, and false when it has none or it cannot be read. It reads the path
// of the cgroup from /proc/self/cgroup, then walks from that cgroup up to the
// root of the mount, and returns the least limit on the way. A v1 path is the
// line of the memory controller, and its files are memory.limit_in_bytes under
// /sys/fs/cgroup/memory. With no such line, the cgroup v2 path is the "0::"
// line, and its files are memory.max under /sys/fs/cgroup. (A host that mounts
// both runs the memory controller under v1.)
func CgroupLimit(readFile func(string) ([]byte, error)) (int64, bool) {
	raw, err := readFile("/proc/self/cgroup")
	if err != nil {
		return 0, false
	}
	var v1, v2 string
	hasV1, hasV2 := false, false
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		switch {
		case parts[0] == "0" && parts[1] == "":
			v2, hasV2 = parts[2], true
		case hasController(parts[1], "memory"):
			v1, hasV1 = parts[2], true
		}
	}
	var root, file, rel string
	switch {
	case hasV1:
		root, file, rel = "/sys/fs/cgroup/memory", "memory.limit_in_bytes", v1
	case hasV2:
		root, file, rel = "/sys/fs/cgroup", "memory.max", v2
	default:
		return 0, false
	}
	// A path outside the namespace of the process starts with "/..": Clean
	// keeps it inside the mount.
	dir := path.Clean("/" + rel)
	var least int64
	found := false
	for {
		if n, ok := readLimit(readFile, path.Join(root, dir, file)); ok && (!found || n < least) {
			least, found = n, true
		}
		if dir == "/" {
			break
		}
		dir = path.Dir(dir)
	}
	return least, found
}

// hasController reports whether the comma-separated list holds name.
func hasController(list, name string) bool {
	for _, c := range strings.Split(list, ",") {
		if c == name {
			return true
		}
	}
	return false
}

// readLimit reads one limit file. "max", a v1 value that means no limit, a
// missing file and anything that is not a positive number give false.
func readLimit(readFile func(string) ([]byte, error), name string) (int64, bool) {
	raw, err := readFile(name)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || n <= 0 || n >= v1Unlimited {
		return 0, false
	}
	return n, true
}
