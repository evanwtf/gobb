package version

import (
	"os"
	"strings"
	"testing"
)

func TestVersionMatchesChangelog(t *testing.T) {
	data, err := os.ReadFile("../../docs/changelog.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "## "))
		if len(fields) == 0 {
			t.Fatalf("empty heading in changelog: %q", line)
		}
		if fields[0] != Version {
			t.Fatalf("changelog says %q, Version is %q", fields[0], Version)
		}
		return
	}
	t.Fatal("no version heading in docs/changelog.md")
}
