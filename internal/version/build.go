package version

import (
	"runtime/debug"
	"strings"
	"sync"
)

// Commit is the commit that built the binary. The Dockerfile and the release
// workflow set it at build time:
//
//	go build -ldflags "-X github.com/evanwtf/gobb/internal/version.Commit=<sha>"
//
// It is empty for a plain `go build`. Build then reads the revision that the Go
// tool stamps into a binary built inside a git checkout.
var Commit string

var build = sync.OnceValue(func() string {
	rev := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				rev = s.Value
			}
		}
	}
	return buildFrom(Commit, rev)
})

// Build returns the first 8 hex characters of the commit that built the binary,
// or "unknown". The page footer shows it, so it never holds anything but those
// 8 characters or the word "unknown".
func Build() string { return build() }

// buildFrom picks the commit: the value given at build time, or the VCS revision
// when there is none. A value that is not 7 to 40 hex characters gives
// "unknown". A build argument can never put markup or text into a page.
func buildFrom(commit, revision string) string {
	if commit == "" {
		commit = revision
	}
	if len(commit) < 7 || len(commit) > 40 {
		return "unknown"
	}
	commit = strings.ToLower(commit)
	for _, c := range commit {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "unknown"
		}
	}
	return commit[:min(len(commit), 8)]
}
