package main

import (
	"os"
	"regexp"
	"testing"
	"time"
)

// TestShutdownFitsStopGracePeriod holds the stop window of the server inside
// the stop_grace_period that compose.yaml gives the gobb service. Docker sends
// SIGKILL when the grace period ends, so shutdownTimeout plus poolGrace must be
// shorter. The test reads the period from the file: a change to either side
// fails it.
func TestShutdownFitsStopGracePeriod(t *testing.T) {
	raw, err := os.ReadFile("../../compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// The gobb service is the block that starts at "  gobb:" and runs to the
	// next top-level key or service.
	block := regexp.MustCompile(`(?ms)^  gobb:\n(.*?)(?:^\S|^  \S)`).FindSubmatch(raw)
	if block == nil {
		t.Fatal("compose.yaml has no gobb service")
	}
	m := regexp.MustCompile(`(?m)^    stop_grace_period:\s*(\S+)\s*$`).FindSubmatch(block[1])
	if m == nil {
		t.Fatal("the gobb service in compose.yaml sets no stop_grace_period")
	}
	grace, err := time.ParseDuration(string(m[1]))
	if err != nil {
		t.Fatalf("stop_grace_period %q: %v", m[1], err)
	}
	if got := shutdownTimeout + poolGrace; got >= grace {
		t.Errorf("shutdownTimeout %v + poolGrace %v = %v, want less than the stop_grace_period %v",
			shutdownTimeout, poolGrace, got, grace)
	}
}
