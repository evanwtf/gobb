package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/internal/version"
)

const goodLog = `# Changelog

## 0.1.0 (2026-11-01)

Intro line.

- First bullet.

### A sub-heading stays in the section

- Second bullet.

## 0.0.9 (2026-01-01)

- Older bullet.
`

// TestReleaseCheck covers each refusal of the check with its own case, and the
// passing cases. A refusal that stops firing would let a bad tag publish.
func TestReleaseCheck(t *testing.T) {
	tests := []struct {
		name      string
		tag       string
		dry       bool
		codeVer   string
		changelog string
		wantErr   string // a substring of the error; empty means success
		wantVer   string
		wantNotes []string // substrings of the notes
		notNotes  []string // substrings the notes must not hold
	}{
		{
			name: "tag passes", tag: "v0.1.0", codeVer: "0.1.0", changelog: goodLog,
			wantVer:   "0.1.0",
			wantNotes: []string{"Intro line.", "First bullet.", "A sub-heading stays", "Second bullet."},
			notNotes:  []string{"Older bullet.", "## 0.1.0", "0.0.9"},
		},
		{
			name: "tag with larger numbers passes", tag: "v10.20.30", codeVer: "10.20.30",
			changelog: "## 10.20.30 (2030-02-28)\n\n- x\n", wantVer: "10.20.30", wantNotes: []string{"- x"},
		},

		// The tag is not vX.Y.Z.
		{name: "tag with no v", tag: "0.1.0", codeVer: "0.1.0", changelog: goodLog, wantErr: "not vX.Y.Z"},
		{name: "tag with two parts", tag: "v0.1", codeVer: "0.1.0", changelog: goodLog, wantErr: "not vX.Y.Z"},
		{name: "tag with four parts", tag: "v0.1.0.1", codeVer: "0.1.0", changelog: goodLog, wantErr: "not vX.Y.Z"},
		{name: "release candidate tag", tag: "v0.0.0-rc.1", codeVer: "0.1.0", changelog: goodLog, wantErr: "not vX.Y.Z"},
		{name: "tag with a build suffix", tag: "v0.1.0+x", codeVer: "0.1.0", changelog: goodLog, wantErr: "not vX.Y.Z"},
		{name: "tag with a leading zero", tag: "v00.1.0", codeVer: "0.1.0", changelog: goodLog, wantErr: "not vX.Y.Z"},
		{name: "tag with a trailing newline", tag: "v0.1.0\n", codeVer: "0.1.0", changelog: goodLog, wantErr: "not vX.Y.Z"},
		{name: "branch name as tag", tag: "main", codeVer: "0.1.0", changelog: goodLog, wantErr: "not vX.Y.Z"},
		{name: "empty tag", tag: "", codeVer: "0.1.0", changelog: goodLog, wantErr: "not vX.Y.Z"},

		// The first section of the changelog is not exactly "X.Y.Z (date)".
		{
			name: "unreleased section", tag: "v0.1.0", codeVer: "0.1.0",
			changelog: "## 0.1.0 (unreleased)\n\n- x\n", wantErr: "(unreleased)",
		},
		{
			name: "heading with no date", tag: "v0.1.0", codeVer: "0.1.0",
			changelog: "## 0.1.0\n\n- x\n", wantErr: "want",
		},
		{
			name: "date that does not exist", tag: "v0.1.0", codeVer: "0.1.0",
			changelog: "## 0.1.0 (2026-13-45)\n\n- x\n", wantErr: "not a date",
		},
		{
			name: "date in the wrong form", tag: "v0.1.0", codeVer: "0.1.0",
			changelog: "## 0.1.0 (1 November 2026)\n\n- x\n", wantErr: "want",
		},
		{
			name: "text after the date", tag: "v0.1.0", codeVer: "0.1.0",
			changelog: "## 0.1.0 (2026-11-01) draft\n\n- x\n", wantErr: "want",
		},
		{
			name: "heading with a v", tag: "v0.1.0", codeVer: "0.1.0",
			changelog: "## v0.1.0 (2026-11-01)\n\n- x\n", wantErr: "want",
		},
		{
			name: "no section at all", tag: "v0.1.0", codeVer: "0.1.0",
			changelog: "# Changelog\n\nNothing yet.\n", wantErr: "no ## section",
		},
		{
			name: "the section of another version comes first", tag: "v0.1.0", codeVer: "0.1.0",
			changelog: "## 0.2.0 (2026-12-01)\n\n- y\n\n## 0.1.0 (2026-11-01)\n\n- x\n",
			wantErr:   "first section is 0.2.0",
		},
		{
			name: "the first section is older than the tag", tag: "v0.1.0", codeVer: "0.1.0",
			changelog: "## 0.0.9 (2026-01-01)\n\n- x\n", wantErr: "first section is 0.0.9",
		},

		// internal/version disagrees with the tag.
		{
			name: "code version differs from the tag", tag: "v0.1.0", codeVer: "0.1.1",
			changelog: goodLog, wantErr: "internal/version",
		},

		// The section is empty.
		{
			name: "empty section before the next one", tag: "v0.1.0", codeVer: "0.1.0",
			changelog: "## 0.1.0 (2026-11-01)\n\n\n## 0.0.9 (2026-01-01)\n\n- old\n", wantErr: "empty",
		},
		{
			name: "empty section at the end of the file", tag: "v0.1.0", codeVer: "0.1.0",
			changelog: "## 0.1.0 (2026-11-01)\n   \n\t\n", wantErr: "empty",
		},
		{
			name: "heading is the last line", tag: "v0.1.0", codeVer: "0.1.0",
			changelog: "## 0.1.0 (2026-11-01)", wantErr: "empty",
		},

		// The dry mode takes the version from the code and accepts "(unreleased)".
		{
			name: "dry run accepts unreleased", dry: true, codeVer: "0.1.0",
			changelog: "## 0.1.0 (unreleased)\n\n- x\n", wantVer: "0.1.0", wantNotes: []string{"- x"},
		},
		{
			name: "dry run accepts a date", dry: true, codeVer: "0.1.0",
			changelog: goodLog, wantVer: "0.1.0", wantNotes: []string{"First bullet."},
		},
		{
			name: "dry run still refuses a version mismatch", dry: true, codeVer: "0.1.0",
			changelog: "## 0.2.0 (unreleased)\n\n- x\n", wantErr: "first section is 0.2.0",
		},
		{
			name: "dry run still refuses an empty section", dry: true, codeVer: "0.1.0",
			changelog: "## 0.1.0 (unreleased)\n\n## 0.0.9 (2026-01-01)\n\n- old\n", wantErr: "empty",
		},
		{
			name: "dry run still refuses a bad heading", dry: true, codeVer: "0.1.0",
			changelog: "## 0.1.0 (soon)\n\n- x\n", wantErr: "want",
		},
		{
			name: "dry run still refuses a bad date", dry: true, codeVer: "0.1.0",
			changelog: "## 0.1.0 (2026-02-30)\n\n- x\n", wantErr: "not a date",
		},
		{
			name: "dry run still refuses a code version that is not X.Y.Z", dry: true, codeVer: "0.1",
			changelog: "## 0.1 (unreleased)\n\n- x\n", wantErr: "internal/version",
		},
		{
			name: "dry run still refuses a missing section", dry: true, codeVer: "0.1.0",
			changelog: "# Changelog\n", wantErr: "no ## section",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ver, notes, err := check(tt.tag, tt.dry, tt.codeVer, tt.changelog)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("check passed (version %q), want an error with %q", ver, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want it to hold %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("check refused: %v", err)
			}
			if ver != tt.wantVer {
				t.Errorf("version = %q, want %q", ver, tt.wantVer)
			}
			for _, s := range tt.wantNotes {
				if !strings.Contains(notes, s) {
					t.Errorf("notes lack %q:\n%s", s, notes)
				}
			}
			for _, s := range tt.notNotes {
				if strings.Contains(notes, s) {
					t.Errorf("notes hold %q:\n%s", s, notes)
				}
			}
			if notes != strings.TrimSpace(notes) {
				t.Errorf("notes are not trimmed: %q", notes)
			}
		})
	}
}

// writeRepo makes a folder with docs/changelog.md, and returns its path.
func writeRepo(t *testing.T, changelog string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "changelog.md"), []byte(changelog), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReleaseCheckCommand(t *testing.T) {
	cur := version.Version
	log := "## " + cur + " (2026-11-01)\n\n- shipped\n"
	unreleased := "## " + cur + " (unreleased)\n\n- shipped\n"

	t.Run("tag prints the version and writes the notes", func(t *testing.T) {
		dir := writeRepo(t, log)
		notes := filepath.Join(t.TempDir(), "notes.md")
		var out, errb bytes.Buffer
		code := run([]string{"-root", dir, "-notes", notes, "v" + cur}, &out, &errb)
		if code != 0 {
			t.Fatalf("exit %d, stderr: %s", code, errb.String())
		}
		if out.String() != cur+"\n" {
			t.Errorf("stdout = %q, want only the version", out.String())
		}
		got, err := os.ReadFile(notes)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "- shipped\n" {
			t.Errorf("notes file = %q", got)
		}
	})
	t.Run("a refusal exits 1 and writes no notes file", func(t *testing.T) {
		dir := writeRepo(t, unreleased)
		notes := filepath.Join(t.TempDir(), "notes.md")
		var out, errb bytes.Buffer
		code := run([]string{"-root", dir, "-notes", notes, "v" + cur}, &out, &errb)
		if code != 1 {
			t.Fatalf("exit %d, want 1", code)
		}
		if out.Len() != 0 {
			t.Errorf("stdout = %q, want nothing", out.String())
		}
		if _, err := os.Stat(notes); err == nil {
			t.Error("the notes file exists after a refusal")
		}
		if !strings.Contains(errb.String(), "unreleased") {
			t.Errorf("stderr = %q", errb.String())
		}
	})
	t.Run("the dry run passes on an unreleased section", func(t *testing.T) {
		dir := writeRepo(t, unreleased)
		var out, errb bytes.Buffer
		code := run([]string{"-root", dir, "-dry-run"}, &out, &errb)
		if code != 0 || out.String() != cur+"\n" {
			t.Fatalf("exit %d, stdout %q, stderr %s", code, out.String(), errb.String())
		}
	})
	t.Run("a missing changelog exits 1", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := run([]string{"-root", t.TempDir(), "-dry-run"}, &out, &errb)
		if code != 1 {
			t.Fatalf("exit %d, want 1", code)
		}
	})
	for name, args := range map[string][]string{
		"no tag and no dry run":    {},
		"a tag with the dry run":   {"-dry-run", "v" + cur},
		"two tags":                 {"v" + cur, "v" + cur},
		"an unknown flag":          {"-nope", "v" + cur},
		"the dry run with a notes": {"-dry-run", "-notes"},
	} {
		t.Run("usage error: "+name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if code := run(args, &out, &errb); code != 2 {
				t.Fatalf("exit %d, want 2; stderr: %s", code, errb.String())
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", out.String())
			}
		})
	}
}

// TestCurrentChangelogPasses runs the dry check against the real changelog, so a
// heading that the release would refuse shows up before the tag does.
func TestCurrentChangelogPasses(t *testing.T) {
	data, err := os.ReadFile("../../docs/changelog.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := check("", true, version.Version, string(data)); err != nil {
		t.Fatal(err)
	}
}
