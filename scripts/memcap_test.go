package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDocker puts a docker command on PATH that answers inspect with state
// (restarts and the OOM flag) or the id "abc", and stats with one line. It
// returns the env for run, with CGROUP_ROOT at a new, empty directory.
func fakeDocker(t *testing.T, state string) (env []string, root string) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
case "$1 $3" in
"inspect {{.RestartCount}} {{.State.OOMKilled}}") echo "` + state + `" ;;
"inspect {{.Id}}") echo abc ;;
stats*) echo "docker stats now: 1MiB / 256MiB" ;;
*) echo "fake docker: $*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	root = t.TempDir()
	return []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "CGROUP_ROOT=" + root}, root
}

// cgroup writes memory.peak and memory.events for container abc under root,
// at the path of the cgroupfs driver or the systemd one.
func cgroup(t *testing.T, root string, systemd bool, peak, oom string) {
	t.Helper()
	dir := filepath.Join(root, "docker", "abc")
	if systemd {
		dir = filepath.Join(root, "system.slice", "docker-abc.scope")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	events := "low 0\nhigh 0\nmax 3\noom 0\noom_kill " + oom + "\noom_group_kill 0\n"
	for name, body := range map[string]string{"memory.peak": peak + "\n", "memory.events": events} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMemcapCheckPasses(t *testing.T) {
	for _, systemd := range []bool{false, true} {
		env, root := fakeDocker(t, "0 false")
		cgroup(t, root, systemd, "104857600", "0")
		out, code := run(t, t.TempDir(), env, "memcap-check.sh", "cid", "268435456")
		if code != 0 {
			t.Fatalf("systemd=%v: exit %d, want 0:\n%s", systemd, code, out)
		}
		if !strings.Contains(out, "memory.peak: 104857600 bytes (100 MiB)") {
			t.Errorf("systemd=%v: no peak line:\n%s", systemd, out)
		}
	}
}

func TestMemcapCheckFails(t *testing.T) {
	for _, c := range []struct {
		name, state, peak, oom, want string
		noCgroup                     bool
	}{
		{name: "a restart", state: "1 false", peak: "1", oom: "0", want: "restarted"},
		{name: "an OOM flag", state: "0 true", peak: "1", oom: "0", want: "restarted"},
		{name: "a kill in the cgroup", state: "0 false", peak: "1", oom: "1", want: "the kernel killed"},
		{name: "the peak at the cap", state: "0 false", peak: "268435456", oom: "0", want: "reached the cap"},
		{name: "no cgroup", state: "0 false", noCgroup: true, want: "not readable"},
	} {
		env, root := fakeDocker(t, c.state)
		if !c.noCgroup {
			cgroup(t, root, false, c.peak, c.oom)
		}
		out, code := run(t, t.TempDir(), env, "memcap-check.sh", "cid", "268435456")
		if code != 1 || !strings.Contains(out, c.want) {
			t.Errorf("%s: exit %d, want 1 and %q:\n%s", c.name, code, c.want, out)
		}
	}
}

func TestMemcapCheckUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"cid"}, {"", "1"}, {"cid", "256m"}, {"cid", "1", "x"}} {
		if out, code := run(t, t.TempDir(), nil, "memcap-check.sh", args...); code != 2 {
			t.Errorf("%q: exit %d, want 2:\n%s", args, code, out)
		}
	}
}
