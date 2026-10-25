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
	// Drop any inherited DENYLIST_FILE so a test reads only the list it names.
	var base []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "DENYLIST_FILE=") {
			base = append(base, kv)
		}
	}
	cmd.Env = append(base, env...)
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

// noDenylist points the guard at an empty denylist so the owner's real
// list in the working tree never reaches the test.
func noDenylist(t *testing.T) []string {
	t.Helper()
	list := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(list, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{"DENYLIST_FILE=" + list}
}

func TestCheckPublicFlagsLANAddress(t *testing.T) {
	addr := "192." + "168." + "7.42"
	dir := repo(t, map[string]string{"notes.txt": "host is " + addr + "\n"})
	out, code := run(t, dir, noDenylist(t), "check-public.sh")
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
	out, code := run(t, dir, noDenylist(t), "check-public.sh")
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, out)
	}
}

func TestCheckPublicAllows10Net(t *testing.T) {
	dir := repo(t, map[string]string{"notes.txt": "host is " + "10." + "1." + "2." + "3\n"})
	out, code := run(t, dir, noDenylist(t), "check-public.sh")
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
}

// The carrier-grade NAT range 100.64.0.0/10 holds tailnet addresses. Each case is
// built from pieces, so this file does not hold a literal address.
func TestCheckPublicFlagsCGNAT(t *testing.T) {
	for name, addr := range map[string]string{
		"low edge":   "100." + "64." + "0." + "1",
		"middle":     "100." + "100." + "100." + "100",
		"high edge":  "100." + "127." + "255." + "254",
		"in a URL":   "http://" + "100." + "72." + "3." + "4:8080/x",
		"after text": "ssh to" + "100." + "90." + "1." + "2",
	} {
		t.Run(name, func(t *testing.T) {
			dir := repo(t, map[string]string{"notes.txt": "host is " + addr + "\n"})
			out, code := run(t, dir, noDenylist(t), "check-public.sh")
			if code != 1 {
				t.Fatalf("exit %d, want 1\n%s", code, out)
			}
			if !strings.Contains(out, "notes.txt:1") {
				t.Errorf("output lacks notes.txt:1: %q", out)
			}
			if strings.Contains(out, "100.") {
				t.Errorf("output leaks the address: %q", out)
			}
		})
	}
}

// Addresses just outside 100.64.0.0/10, and numbers that only contain one, pass.
func TestCheckPublicAllowsOutsideCGNAT(t *testing.T) {
	for name, text := range map[string]string{
		"below":          "100." + "63." + "255." + "255",
		"above":          "100." + "128." + "0." + "1",
		"public 100":     "100." + "200." + "1." + "1",
		"longer prefix":  "1100." + "64." + "0." + "1",
		"inside dotted":  "9." + "100." + "64." + "0." + "1",
		"version string": "v1.100.64",
	} {
		t.Run(name, func(t *testing.T) {
			dir := repo(t, map[string]string{"notes.txt": "value " + text + "\n"})
			out, code := run(t, dir, noDenylist(t), "check-public.sh")
			if code != 0 {
				t.Fatalf("exit %d, want 0\n%s", code, out)
			}
		})
	}
}

// A tailnet name ends in the .ts.net suffix.
func TestCheckPublicFlagsTailnetName(t *testing.T) {
	suffix := ".ts" + ".net"
	for name, text := range map[string]string{
		"host":       "ssh box.tail1234" + suffix,
		"upper case": "ssh BOX.TAIL1234" + strings.ToUpper(suffix),
		"with port":  "https://box.tail1234" + suffix + ":8443/",
	} {
		t.Run(name, func(t *testing.T) {
			dir := repo(t, map[string]string{"notes.txt": text + "\n"})
			out, code := run(t, dir, noDenylist(t), "check-public.sh")
			if code != 1 {
				t.Fatalf("exit %d, want 1\n%s", code, out)
			}
		})
	}
	// A longer word that starts with the suffix is not a tailnet name.
	dir := repo(t, map[string]string{"notes.txt": "see a.ts" + ".network here\n"})
	if out, code := run(t, dir, noDenylist(t), "check-public.sh"); code != 0 {
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
			out, code := run(t, dir, noDenylist(t), "check-public.sh")
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
			out, code := run(t, dir, noDenylist(t), "check-public.sh")
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

// pre-commit passes the commit message file at the commit-msg stage. The file
// sits outside the index, so the guard must scan a named file that git ignores.
func TestCheckPublicFlagsCommitMessageFile(t *testing.T) {
	word := "examplecorp"
	list := filepath.Join(t.TempDir(), "deny")
	if err := os.WriteFile(list, []byte(word+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := repo(t, nil)
	msg := filepath.Join(dir, "COMMIT_EDITMSG")
	body := "repo: fix thing\n\nMoves the ExampleCorp import path.\n"
	if err := os.WriteFile(msg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"DENYLIST_FILE=" + list}
	out, code := run(t, dir, env, "check-public.sh", "COMMIT_EDITMSG")
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, out)
	}
	if !strings.Contains(out, "COMMIT_EDITMSG:3") {
		t.Errorf("output lacks COMMIT_EDITMSG:3: %q", out)
	}
	if strings.Contains(strings.ToLower(out), word) {
		t.Errorf("output leaks the word: %q", out)
	}

	if err := os.WriteFile(msg, []byte("repo: fix thing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code = run(t, dir, env, "check-public.sh", "COMMIT_EDITMSG"); code != 0 {
		t.Fatalf("clean message: exit %d, want 0\n%s", code, out)
	}
}

func TestCheckPublicChecksOnlyNamedFiles(t *testing.T) {
	dir := repo(t, map[string]string{
		"bad.txt":  "host is " + "192." + "168." + "0.1\n",
		"good.txt": "ordinary text\n",
	})
	if out, code := run(t, dir, noDenylist(t), "check-public.sh", "good.txt"); code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
	out, code := run(t, dir, noDenylist(t), "check-public.sh", "good.txt", "bad.txt")
	if code != 1 || !strings.Contains(out, "bad.txt:1") {
		t.Fatalf("exit %d, output %q", code, out)
	}
}

func TestCheckPublicCleanRepo(t *testing.T) {
	dir := repo(t, map[string]string{"notes.txt": "Just ordinary text.\nNothing private.\n"})
	out, code := run(t, dir, noDenylist(t), "check-public.sh")
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
}

func TestRefuseSessionIDs(t *testing.T) {
	id := strings.Repeat("a1B2", 6) // 24 made-up characters
	scissors := "# " + strings.Repeat("-", 24) + " >8 " + strings.Repeat("-", 24)
	trailer := "Claude-" + "Session: abc\n"
	msgMode := []string{"--commit-msg"}
	cases := []struct {
		name string
		args []string // flags before the file name
		msg  string
		want int
	}{
		{"url", nil, "repo: x\n\nhttps://claude." + "ai/code/session_" + id + "\n", 1},
		{"trailer", nil, "repo: x\n\n" + trailer, 1},
		{"coauthor", nil, "repo: x\n\nCo-Authored-" + "By: Claude <noreply@example.invalid>\n", 1},
		{"bare token", nil, "repo: x\n\nsession" + "_" + id + "\n", 1},
		{"short word", nil, "fix session_store\n", 0},
		{"lower trailer", nil, "repo: x\n\nclaude-" + "session: abc\n", 1},
		{"github casing", nil, "repo: x\n\nCo-authored-" + "by: Claude <noreply@example.invalid>\n", 1},
		{"comment line", nil, "repo: x\n\n# " + trailer, 1},
		{"person", nil, "repo: x\n\nCo-Authored-" + "By: Pat Example <pat@example.invalid>\n", 0},
		// Plain mode scans a whole log. A scissors line must not hide what follows.
		{"plain: scissors then trailer", nil, "abc\nrepo: x\n\n" + scissors + "\n-x\n\ndef\nrepo: y\n\n" + trailer, 1},
		{"plain: trailer then scissors", nil, "abc\nrepo: x\n\n" + trailer + scissors + "\n", 1},
		// --commit-msg mode drops the verbose diff below the scissors line.
		{"msg: trailer only below scissors", msgMode, "repo: x\n\n" + scissors + "\n-" + trailer, 0},
		{"msg: trailer above scissors", msgMode, "repo: x\n\n" + trailer + scissors + "\n", 1},
		{"msg: trailer", msgMode, "repo: x\n\n" + trailer, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := filepath.Join(t.TempDir(), "MSG")
			if err := os.WriteFile(msg, []byte(c.msg), 0o600); err != nil {
				t.Fatal(err)
			}
			args := append(append([]string{}, c.args...), msg)
			out, code := run(t, t.TempDir(), nil, "refuse-session-ids.sh", args...)
			if code != c.want {
				t.Fatalf("exit %d, want %d\n%s", code, c.want, out)
			}
		})
	}
}

func TestCheckPublicFailsClosedOnUnreadableDenylist(t *testing.T) {
	dir := repo(t, map[string]string{"notes.txt": "ordinary text\n"})
	missing := []string{"DENYLIST_FILE=" + filepath.Join(t.TempDir(), "absent")}
	out, code := run(t, dir, missing, "check-public.sh")
	if code != 2 {
		t.Fatalf("missing file: exit %d, want 2\n%s", code, out)
	}
	if !strings.Contains(out, "DENYLIST_FILE") {
		t.Errorf("error does not name the variable: %q", out)
	}
	// An empty value is what an unset CI secret produces.
	if out, code = run(t, dir, []string{"DENYLIST_FILE="}, "check-public.sh"); code != 2 {
		t.Fatalf("empty value: exit %d, want 2\n%s", code, out)
	}
}

func TestCheckPublicScansNonASCIIFileNames(t *testing.T) {
	list := filepath.Join(t.TempDir(), "deny")
	if err := os.WriteFile(list, []byte("examplecorp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := repo(t, map[string]string{"caf\u00e9.txt": "ExampleCorp\n"})
	out, code := run(t, dir, []string{"DENYLIST_FILE=" + list}, "check-public.sh")
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, out)
	}
	if !strings.Contains(out, "caf\u00e9.txt:1") {
		t.Errorf("output lacks the file name: %q", out)
	}
}

// git -v puts a scissors line and the full diff below the message. A commit that
// removes a private word shows the word on a "-" line there; that must pass.
func TestCheckPublicCommitMessageModeSkipsOnlyTheDiff(t *testing.T) {
	list := filepath.Join(t.TempDir(), "deny")
	if err := os.WriteFile(list, []byte("examplecorp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"DENYLIST_FILE=" + list}
	scissors := "# " + strings.Repeat("-", 24) + " >8 " + strings.Repeat("-", 24)
	check := func(body string) (string, int) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "MSG"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return run(t, dir, env, "check-public.sh", "--commit-msg", "MSG")
	}

	verbose := "repo: drop old name\n\n# Please enter the commit message\n# on branch main\n" +
		scissors + "\ndiff --git a/x b/x\n-ExampleCorp\n+ordinary\n"
	if out, code := check(verbose); code != 0 {
		t.Fatalf("word below scissors: exit %d, want 0\n%s", code, out)
	}
	if out, code := check("repo: ExampleCorp\n"); code != 1 || !strings.Contains(out, "MSG:1") {
		t.Fatalf("word in body: exit %d, output %q", code, out)
	}
	// git -m keeps a # line, so the word on one reaches history: refuse it.
	out, code := check("repo: x\n\n# ExampleCorp\n" + scissors + "\n")
	if code != 1 || !strings.Contains(out, "MSG:3") {
		t.Fatalf("word on # line above scissors: exit %d, output %q", code, out)
	}
	// The word on line 4, after a comment line, keeps its original line number.
	out, code = check("repo: x\n# note\n\nExampleCorp in the body\n" + scissors + "\n")
	if code != 1 || !strings.Contains(out, "MSG:4") {
		t.Fatalf("body after comment: exit %d, output %q", code, out)
	}
}

// fuzzScript runs scripts/fuzz.sh from the repo root.
func fuzzScript(t *testing.T, args ...string) (string, int) {
	t.Helper()
	return run(t, "..", nil, "fuzz.sh", args...)
}

// A fuzz target whose name matches nothing makes go test pass with a warning, so
// a renamed target would turn the CI job into a green no-op. The script lists the
// target first and fails when the list is not exactly its name.
func TestFuzzScriptRefusesAMissingTarget(t *testing.T) {
	for name, target := range map[string]string{
		"misspelled name":    "./markup/ FuzzParsee",
		"unanchored prefix":  "./markup/ FuzzPars",
		"name of a test":     "./markup/ TestParseBasic",
		"name in other pkg":  "./internal/password/ FuzzParse",
		"package with none":  "./internal/version/ FuzzVersion",
		"package that fails": "./no/such/package/ FuzzParse",
	} {
		t.Run(name, func(t *testing.T) {
			out, code := fuzzScript(t, "1s", target)
			if code != 1 {
				t.Fatalf("exit %d for %q, want 1:\n%s", code, target, out)
			}
			if strings.Contains(out, "fuzz: elapsed") {
				t.Errorf("the script fuzzed for %q:\n%s", target, out)
			}
		})
	}
	// One bad target does not hide the others: the good one still runs.
	out, code := fuzzScript(t, "1s", "./markup/ FuzzParsee", "./internal/password/ FuzzVerifyLegacy")
	if code != 1 || !strings.Contains(out, "FuzzParsee") || !strings.Contains(out, "fuzz: elapsed") {
		t.Errorf("exit %d, want a failure for the bad target and a run of the good one:\n%s", code, out)
	}
}

func TestFuzzScriptRunsARealTarget(t *testing.T) {
	out, code := fuzzScript(t, "2s", "./internal/password/ FuzzVerifyLegacy")
	if code != 0 || !strings.Contains(out, "fuzz: elapsed") {
		t.Errorf("exit %d, want 0 and a fuzz run:\n%s", code, out)
	}
}

// A cold module cache makes go print "go: downloading ..." lines on stderr before
// the list. They must not count as names. A stub go on the PATH prints that noise
// on stderr, then the name and the ok line on stdout, as the real tool does.
func TestFuzzScriptIgnoresDownloadNoise(t *testing.T) {
	bin := t.TempDir()
	stub := `#!/bin/sh
case "$*" in
*-list*)
	echo "go: downloading example.com/m v1.0.0" >&2
	echo "FuzzThing"
	printf 'ok  \texample.com/pkg\t0.01s\n'
	;;
*-fuzz*)
	echo "go: downloading example.com/m v1.0.0" >&2
	echo "fuzz: elapsed: 1s, execs: 1"
	;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + bin + ":" + os.Getenv("PATH")}
	out, code := run(t, "..", env, "fuzz.sh", "1s", "./pkg/ FuzzThing")
	if code != 0 || !strings.Contains(out, "fuzz: elapsed") {
		t.Errorf("exit %d, want 0 and a fuzz run:\n%s", code, out)
	}
	// And the stub still lets a wrong name fail.
	out, code = run(t, "..", env, "fuzz.sh", "1s", "./pkg/ FuzzOther")
	if code != 1 {
		t.Errorf("a name the list does not hold: exit %d, want 1:\n%s", code, out)
	}
}

const readmeSample = "# Sample\n\n" +
	"<!-- block: first -->\n```sh\necho one\n```\n\n" +
	"1. A step.\n\n   <!-- block: second -->\n   ```sh\n   echo two \\\n     && echo three\n   ```\n\n" +
	"<!-- block: empty -->\n```sh\n```\n\n" +
	"<!-- block: text-only -->\n```\nnot shell\n```\n"

func TestReadmeBlock(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "README.md")
	if err := os.WriteFile(file, []byte(readmeSample), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		want     string
		wantCode int
	}{
		{"first", "echo one\n", 0},
		{"second", "echo two \\\n  && echo three\n", 0},
		{"empty", "", 1},
		{"text-only", "", 1},
		{"missing", "", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, code := run(t, dir, nil, "readme-block.sh", tt.name, file)
			if code != tt.wantCode {
				t.Fatalf("exit %d, want %d; output: %q", code, tt.wantCode, out)
			}
			if tt.wantCode == 0 && out != tt.want {
				t.Fatalf("block = %q, want %q", out, tt.want)
			}
		})
	}
	if _, code := run(t, dir, nil, "readme-block.sh"); code != 2 {
		t.Errorf("no arguments: exit %d, want 2", code)
	}
}
