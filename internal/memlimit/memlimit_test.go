package memlimit

import (
	"errors"
	"io/fs"
	"math"
	"testing"
)

// files returns a readFile over a map of paths to contents. A path that is not
// in the map does not exist.
func files(m map[string]string) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		s, ok := m[name]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return []byte(s), nil
	}
}

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

const mib = 1 << 20

func TestCgroupLimit(t *testing.T) {
	for _, c := range []struct {
		name  string
		files map[string]string
		want  int64
		ok    bool
	}{
		{"v2 in a container: the namespace root", map[string]string{
			"/proc/self/cgroup":         "0::/\n",
			"/sys/fs/cgroup/memory.max": "268435456\n",
		}, 256 * mib, true},
		{"v2 with no limit", map[string]string{
			"/proc/self/cgroup":         "0::/\n",
			"/sys/fs/cgroup/memory.max": "max\n",
		}, 0, false},
		{"v2 nested: the least limit on the path wins", map[string]string{
			"/proc/self/cgroup": "0::/system.slice/docker-abc.scope\n",
			"/sys/fs/cgroup/system.slice/docker-abc.scope/memory.max": "536870912\n",
			"/sys/fs/cgroup/system.slice/memory.max":                  "268435456\n",
		}, 256 * mib, true},
		{"v2 nested: no limit on the path", map[string]string{
			"/proc/self/cgroup": "0::/user.slice/session-1.scope\n",
			"/sys/fs/cgroup/user.slice/session-1.scope/memory.max": "max\n",
			"/sys/fs/cgroup/user.slice/memory.max":                 "max\n",
		}, 0, false},
		{"v1 with a limit", map[string]string{
			"/proc/self/cgroup": "12:pids:/docker/abc\n4:memory:/docker/abc\n1:name=systemd:/docker/abc\n",
			"/sys/fs/cgroup/memory/docker/abc/memory.limit_in_bytes": "268435456\n",
		}, 256 * mib, true},
		{"v1 in a container: the namespace root", map[string]string{
			"/proc/self/cgroup":                           "4:cpu,memory:/\n",
			"/sys/fs/cgroup/memory/memory.limit_in_bytes": "134217728\n",
		}, 128 * mib, true},
		{"v1 with no limit", map[string]string{
			"/proc/self/cgroup":                           "4:memory:/\n",
			"/sys/fs/cgroup/memory/memory.limit_in_bytes": "9223372036854771712\n",
		}, 0, false},
		{"hybrid: the memory controller is under v1", map[string]string{
			"/proc/self/cgroup": "4:memory:/docker/abc\n0::/docker/abc\n",
			"/sys/fs/cgroup/memory/docker/abc/memory.limit_in_bytes": "268435456\n",
		}, 256 * mib, true},
		{"no cgroup file (not Linux)", map[string]string{}, 0, false},
		{"a value that is not a number", map[string]string{
			"/proc/self/cgroup":         "0::/\n",
			"/sys/fs/cgroup/memory.max": "lots\n",
		}, 0, false},
		{"a path that tries to climb out", map[string]string{
			"/proc/self/cgroup":         "0::/../../etc\n",
			"/sys/fs/cgroup/memory.max": "268435456\n",
			"/sys/fs/memory.max":        "1\n",
			"/etc/memory.max":           "1\n",
		}, 256 * mib, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := CgroupLimit(files(c.files))
			if got != c.want || ok != c.ok {
				t.Errorf("CgroupLimit = %d, %v; want %d, %v", got, ok, c.want, c.ok)
			}
		})
	}
}

// The memory limit: GOMEMLIMIT wins when it is set, and gobb changes nothing.
// Otherwise 70% of a cgroup limit. Otherwise no limit.
func TestDecide(t *testing.T) {
	cgroup := map[string]string{
		"/proc/self/cgroup":         "0::/\n",
		"/sys/fs/cgroup/memory.max": "268435456\n",
	}
	for _, c := range []struct {
		name    string
		env     map[string]string
		files   map[string]string
		current int64
		want    Result
	}{
		{"GOMEMLIMIT set, under a cgroup limit", map[string]string{"GOMEMLIMIT": "180MiB"}, cgroup,
			180 * mib, Result{Limit: 180 * mib, Source: SourceEnv}},
		{"GOMEMLIMIT=off", map[string]string{"GOMEMLIMIT": "off"}, cgroup,
			math.MaxInt64, Result{Limit: 0, Source: SourceEnv}},
		{"a cgroup limit of 256 MiB", nil, cgroup,
			math.MaxInt64, Result{Limit: 187904819, Source: SourceCgroup}},
		{"no limit anywhere", nil, nil,
			math.MaxInt64, Result{Limit: 0, Source: SourceNone}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Decide(env(c.env), files(c.files), c.current); got != c.want {
				t.Errorf("Decide = %+v, want %+v", got, c.want)
			}
		})
	}
}

// apply sets the runtime limit only for a cgroup limit.
func TestApply(t *testing.T) {
	cgroup := files(map[string]string{
		"/proc/self/cgroup":         "0::/\n",
		"/sys/fs/cgroup/memory.max": "268435456\n",
	})
	for _, c := range []struct {
		name string
		env  map[string]string
		read func(string) ([]byte, error)
		set  []int64
		want Result
	}{
		{"cgroup", nil, cgroup, []int64{187904819}, Result{Limit: 187904819, Source: SourceCgroup}},
		{"GOMEMLIMIT", map[string]string{"GOMEMLIMIT": "100MiB"}, cgroup, nil, Result{Limit: 100 * mib, Source: SourceEnv}},
		{"none", nil, func(string) ([]byte, error) { return nil, errors.New("no") }, nil, Result{Source: SourceNone}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var set []int64
			setLimit := func(n int64) int64 {
				if n >= 0 {
					set = append(set, n)
					return 0
				}
				if c.env["GOMEMLIMIT"] != "" {
					return 100 * mib
				}
				return math.MaxInt64
			}
			got := apply(env(c.env), c.read, setLimit)
			if got != c.want {
				t.Errorf("apply = %+v, want %+v", got, c.want)
			}
			if len(set) != len(c.set) || (len(set) == 1 && set[0] != c.set[0]) {
				t.Errorf("the runtime limit was set to %v, want %v", set, c.set)
			}
		})
	}
}
