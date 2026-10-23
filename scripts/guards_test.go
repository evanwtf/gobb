package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// run executes a script from dir and returns its combined output and exit code.
func run(t *testing.T, dir string, env []string, script string, args ...string) (string, int) {
	t.Helper()
	abs, err := filepath.Abs(script)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", append([]string{abs}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	t.Fatal(err)
	return "", -1
}

// repo makes a temp git repo holding the given files, all added to the index.
func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		git("add", "-f", name)
	}
	return dir
}

// noDenylist points the guard at a missing denylist so the owner's real
// list in the working tree never reaches the test.
var noDenylist = []string{"DENYLIST_FILE=/nonexistent/denylist"}

func TestCheckPublicFlagsLANAddress(t *testing.T) {
	addr := "192." + "168." + "7.42"
	dir := repo(t, map[string]string{"notes.txt": "host is " + addr + "\n"})
	out, code := run(t, dir, noDenylist, "check-public.sh")
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, out)
	}
	if !strings.Contains(out, "notes.txt:1") {
		t.Errorf("output lacks notes.txt:1: %q", out)
	}
	if strings.Contains(out, addr) {
		t.Errorf("output leaks the address: %q", out)
	}
}

func TestCheckPublicFlags172Private(t *testing.T) {
	dir := repo(t, map[string]string{"notes.txt": "host is " + "172." + "20." + "1." + "9\n"})
	out, code := run(t, dir, noDenylist, "check-public.sh")
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, out)
	}
}

func TestCheckPublicAllows10Net(t *testing.T) {
	dir := repo(t, map[string]string{"notes.txt": "host is " + "10." + "1." + "2." + "3\n"})
	out, code := run(t, dir, noDenylist, "check-public.sh")
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
}

func TestCheckPublicFlagsHomePath(t *testing.T) {
	for name, body := range map[string]string{
		"mac":   "see " + "/Users" + "/someone/code\n",
		"linux": "see " + "/home" + "/someone/code\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := repo(t, map[string]string{"notes.txt": body})
			out, code := run(t, dir, noDenylist, "check-public.sh")
			if code != 1 {
				t.Fatalf("exit %d, want 1\n%s", code, out)
			}
		})
	}
}

func TestCheckPublicFlagsPrivateDNS(t *testing.T) {
	for _, suffix := range []string{"internal", "lan", "home.arpa", "localdomain"} {
		t.Run(suffix, func(t *testing.T) {
			dir := repo(t, map[string]string{"notes.txt": "host box." + suffix + " here\n"})
			out, code := run(t, dir, noDenylist, "check-public.sh")
			if code != 1 {
				t.Fatalf("exit %d, want 1\n%s", code, out)
			}
		})
	}
}

func TestCheckPublicUsesDenylist(t *testing.T) {
	word := "examplecorp"
	list := filepath.Join(t.TempDir(), "deny")
	if err := os.WriteFile(list, []byte("# comment\n\n"+word+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := repo(t, map[string]string{"notes.txt": "We work at ExampleCorp today\n"})
	out, code := run(t, dir, []string{"DENYLIST_FILE=" + list}, "check-public.sh")
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, out)
	}
	if !strings.Contains(out, "notes.txt:1") {
		t.Errorf("output lacks notes.txt:1: %q", out)
	}
	if strings.Contains(strings.ToLower(out), word) {
		t.Errorf("output leaks the word: %q", out)
	}

	// The default denylist file in the repo works too, and is never scanned.
	dir = repo(t, map[string]string{
		".public-denylist": word + "\n",
		"notes.txt":        "We work at ExampleCorp today\n",
	})
	out, code = run(t, dir, nil, "check-public.sh")
	if code != 1 {
		t.Fatalf("default list: exit %d, want 1\n%s", code, out)
	}
	if strings.Contains(out, ".public-denylist") {
		t.Errorf("denylist file was scanned: %q", out)
	}
	if !strings.Contains(out, "notes.txt:1") {
		t.Errorf("output lacks notes.txt:1: %q", out)
	}

	// A repo whose only match sits in the denylist itself is clean.
	dir = repo(t, map[string]string{
		".public-denylist": word + "\n",
		"notes.txt":        "ordinary text\n",
	})
	if out, code = run(t, dir, nil, "check-public.sh"); code != 0 {
		t.Fatalf("clean with default list: exit %d\n%s", code, out)
	}
}

func TestCheckPublicChecksOnlyNamedFiles(t *testing.T) {
	dir := repo(t, map[string]string{
		"bad.txt":  "host is " + "192." + "168." + "0.1\n",
		"good.txt": "ordinary text\n",
	})
	if out, code := run(t, dir, noDenylist, "check-public.sh", "good.txt"); code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
	out, code := run(t, dir, noDenylist, "check-public.sh", "good.txt", "bad.txt")
	if code != 1 || !strings.Contains(out, "bad.txt:1") {
		t.Fatalf("exit %d, output %q", code, out)
	}
}

func TestCheckPublicCleanRepo(t *testing.T) {
	dir := repo(t, map[string]string{"notes.txt": "Just ordinary text.\nNothing private.\n"})
	out, code := run(t, dir, noDenylist, "check-public.sh")
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
}

func TestRefuseSessionIDs(t *testing.T) {
	id := strings.Repeat("a1B2", 6) // 24 made-up characters
	cases := []struct {
		name string
		msg  string
		want int
	}{
		{"url", "repo: x\n\nhttps://claude." + "ai/code/session_" + id + "\n", 1},
		{"trailer", "repo: x\n\nClaude-" + "Session: abc\n", 1},
		{"coauthor", "repo: x\n\nCo-Authored-" + "By: Claude <noreply@example.invalid>\n", 1},
		{"bare token", "repo: x\n\nsession" + "_" + id + "\n", 1},
		{"short word", "fix session_store\n", 0},
		{"person", "repo: x\n\nCo-Authored-" + "By: Pat Example <pat@example.invalid>\n", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := filepath.Join(t.TempDir(), "MSG")
			if err := os.WriteFile(msg, []byte(c.msg), 0o600); err != nil {
				t.Fatal(err)
			}
			out, code := run(t, t.TempDir(), nil, "refuse-session-ids.sh", msg)
			if code != c.want {
				t.Fatalf("exit %d, want %d\n%s", code, c.want, out)
			}
		})
	}
}
