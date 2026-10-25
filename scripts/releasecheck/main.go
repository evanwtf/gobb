// Command releasecheck decides whether a release may go out. The release workflow
// runs it before any build, and nothing is published unless it exits 0.
//
//	go run ./scripts/releasecheck [-notes FILE] vX.Y.Z
//	go run ./scripts/releasecheck [-notes FILE] -dry-run
//
// With a tag, the command refuses unless all of these hold:
//   - the tag is vX.Y.Z, with no suffix;
//   - the first "## " section of docs/changelog.md is exactly "X.Y.Z (YYYY-MM-DD)",
//     for the version of the tag and a real date;
//   - internal/version holds the version of the tag;
//   - the section is not empty, and not over 125000 characters (the limit of GitHub
//     for the body of a release).
//
// With -dry-run there is no tag. The version comes from internal/version, and the
// date of the heading may be "(unreleased)". Every other check runs. The dry run
// lets the workflow prove its build before the changelog has a date.
//
// On success the command prints the version alone on stdout. With -notes it also
// writes the body of the section to FILE: those are the release notes. The notes
// come from the changelog, never from the message of a tag.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/evanwtf/gobb/internal/version"
)

// maxNotes is the longest release body in characters. GitHub refuses a longer one,
// and it refuses it after the image is public, so the check runs first.
const maxNotes = 125000

var (
	tagRE     = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	versionRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	headingRE = regexp.MustCompile(`^([0-9]+\.[0-9]+\.[0-9]+) \((unreleased|[0-9]{4}-[0-9]{2}-[0-9]{2})\)$`)
)

// check applies every rule. tag is "vX.Y.Z", or empty in the dry mode. codeVer is
// internal/version. It returns the version and the notes (the trimmed body of the
// first section).
func check(tag string, dry bool, codeVer, changelog string) (ver, notes string, err error) {
	if dry {
		if !versionRE.MatchString(codeVer) {
			return "", "", fmt.Errorf("internal/version is %q, want X.Y.Z", codeVer)
		}
		ver = codeVer
	} else {
		if !tagRE.MatchString(tag) {
			return "", "", fmt.Errorf("the tag %q is not vX.Y.Z", tag)
		}
		ver = strings.TrimPrefix(tag, "v")
		if codeVer != ver {
			return "", "", fmt.Errorf("internal/version is %q, the tag is %q", codeVer, tag)
		}
	}

	heading, body, ok := firstSection(changelog)
	if !ok {
		return "", "", errors.New("the changelog has no ## section")
	}
	m := headingRE.FindStringSubmatch(heading)
	if m == nil {
		return "", "", fmt.Errorf(`the first section heading %q is wrong; want "X.Y.Z (YYYY-MM-DD)"`, heading)
	}
	if m[2] == "unreleased" {
		if !dry {
			return "", "", errors.New("the first section is dated (unreleased); set its date before the tag")
		}
	} else if _, err := time.Parse("2006-01-02", m[2]); err != nil {
		return "", "", fmt.Errorf("%q is not a date", m[2])
	}
	if m[1] != ver {
		return "", "", fmt.Errorf("the first section is %s, the release is %s", m[1], ver)
	}
	notes = strings.TrimSpace(body)
	if notes == "" {
		return "", "", fmt.Errorf("the section %s is empty", ver)
	}
	if n := utf8.RuneCountInString(notes); n > maxNotes {
		return "", "", fmt.Errorf("the notes of %s are %d characters; GitHub accepts at most %d", ver, n, maxNotes)
	}
	return ver, notes, nil
}

// firstSection returns the text of the first "## " heading, and the lines that
// follow it up to the next "## " heading. A "### " line stays in the section.
func firstSection(changelog string) (heading, body string, ok bool) {
	lines := strings.Split(strings.ReplaceAll(changelog, "\r\n", "\n"), "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "## ") {
			start = i
			break
		}
	}
	if start < 0 {
		return "", "", false
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	return strings.TrimRight(strings.TrimPrefix(lines[start], "## "), " \t"),
		strings.Join(lines[start+1:end], "\n"), true
}

// run is main without the process: it returns the exit code. 0 is success, 1 is a
// refusal or a failure, and 2 is a wrong call.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("releasecheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dry := fs.Bool("dry-run", false, "no tag: take the version from internal/version and accept (unreleased)")
	notes := fs.String("notes", "", "write the release notes to `FILE`")
	root := fs.String("root", ".", "the repo `dir`, which holds docs/changelog.md")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	tag := ""
	switch {
	case *dry && fs.NArg() != 0:
		fmt.Fprintln(stderr, "releasecheck: the dry run takes no tag")
		return 2
	case !*dry && fs.NArg() != 1:
		fmt.Fprintln(stderr, "usage: releasecheck [-notes FILE] vX.Y.Z | -dry-run")
		return 2
	case !*dry:
		tag = fs.Arg(0)
	}

	data, err := os.ReadFile(filepath.Join(*root, "docs", "changelog.md"))
	if err != nil {
		fmt.Fprintf(stderr, "releasecheck: %v\n", err)
		return 1
	}
	ver, body, err := check(tag, *dry, version.Version, string(data))
	if err != nil {
		fmt.Fprintf(stderr, "releasecheck: refused: %v\n", err)
		return 1
	}
	if *notes != "" {
		if err := os.WriteFile(*notes, []byte(body+"\n"), 0o644); err != nil {
			fmt.Fprintf(stderr, "releasecheck: %v\n", err)
			return 1
		}
	}
	fmt.Fprintln(stdout, ver)
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
