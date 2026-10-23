package main

import (
	"testing"

	"github.com/evanwtf/gobb/internal/config"
)

// The healthcheck command cannot call config.FromEnv, because it must not
// need GOBB_DSN. This test keeps its default in step with the config package.
func TestDefaultListenMatchesConfig(t *testing.T) {
	env := map[string]string{"GOBB_DSN": "postgres://example"}
	cfg, err := config.FromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != defaultListen {
		t.Fatalf("config default = %q, healthcheck default = %q", cfg.Listen, defaultListen)
	}
}
