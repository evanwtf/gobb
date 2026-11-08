package main

import (
	"testing"

	"github.com/evanwtf/gobb/internal/config"
	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/web"
)

// The packages keep their own defaults for a caller that passes zero. They
// must match the defaults of the settings, or a server and a test would run
// with different limits.
func TestLoadDefaultsAgree(t *testing.T) {
	if web.DefaultMaxInFlight != config.DefaultMaxInFlight {
		t.Errorf("web.DefaultMaxInFlight = %d, config.DefaultMaxInFlight = %d", web.DefaultMaxInFlight, config.DefaultMaxInFlight)
	}
	if web.DefaultMaxSigninInFlight != config.DefaultMaxSigninInFlight {
		t.Errorf("web.DefaultMaxSigninInFlight = %d, config.DefaultMaxSigninInFlight = %d", web.DefaultMaxSigninInFlight, config.DefaultMaxSigninInFlight)
	}
	if login.DefaultGateWait != config.DefaultSigninGateWait {
		t.Errorf("login.DefaultGateWait = %s, config.DefaultSigninGateWait = %s", login.DefaultGateWait, config.DefaultSigninGateWait)
	}
}
