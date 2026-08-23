package config

import (
	"strings"
	"testing"
)

// Regression guards for the TCP field wiring: parseFlags/flagValues
// registration and flagSource/envSource Apply branches are four separate code
// points that must stay in lockstep for every new field. tcp-max-connections
// shipped with registration but WITHOUT its two Apply branches — the compiler
// was satisfied and every existing test stayed green while both -tcp-max-
// connections and PROXYDGE_TCP_MAX_CONNECTIONS were silently ignored. These
// tests exercise each transport end-to-end so a missing branch fails here.

// TestTCPFieldsFlagPropagation: each -tcp-* flag value must reach the Config.
func TestTCPFieldsFlagPropagation(t *testing.T) {
	fv, set, err := parseFlags([]string{
		"-tcp-header-version", "v1",
		"-tcp-family-mismatch", "unknown",
		"-tcp-max-connections", "100",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	var c Config
	if err := (defaultsSource{}).Apply(&c); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if err := (flagSource{fv: fv, set: set}).Apply(&c); err != nil {
		t.Fatalf("flagSource: %v", err)
	}
	if c.TCPHeaderVersion != "v1" {
		t.Errorf("flag tcp-header-version not applied: got %q", c.TCPHeaderVersion)
	}
	if c.TCPFamilyMismatch != "unknown" {
		t.Errorf("flag tcp-family-mismatch not applied: got %q", c.TCPFamilyMismatch)
	}
	if c.TCPMaxConnections != 100 {
		t.Errorf("flag tcp-max-connections not applied: got %d", c.TCPMaxConnections)
	}
}

// TestTCPFieldsEnvPropagation: each PROXYDGE_TCP_* variable must reach the
// Config.
func TestTCPFieldsEnvPropagation(t *testing.T) {
	t.Setenv("PROXYDGE_TCP_HEADER_VERSION", "v1")
	t.Setenv("PROXYDGE_TCP_FAMILY_MISMATCH", "legacy")
	t.Setenv("PROXYDGE_TCP_MAX_CONNECTIONS", "200")

	var c Config
	if err := (envSource{}).Apply(&c); err != nil {
		t.Fatalf("envSource: %v", err)
	}
	if c.TCPHeaderVersion != "v1" {
		t.Errorf("env TCP_HEADER_VERSION not applied: got %q", c.TCPHeaderVersion)
	}
	if c.TCPFamilyMismatch != "legacy" {
		t.Errorf("env TCP_FAMILY_MISMATCH not applied: got %q", c.TCPFamilyMismatch)
	}
	if c.TCPMaxConnections != 200 {
		t.Errorf("env TCP_MAX_CONNECTIONS not applied: got %d", c.TCPMaxConnections)
	}
}

// TestTCPMaxConnectionsEnvInvalid: a non-numeric env value is a hard error,
// not a silent skip.
func TestTCPMaxConnectionsEnvInvalid(t *testing.T) {
	t.Setenv("PROXYDGE_TCP_MAX_CONNECTIONS", "abc")
	err := (envSource{}).Apply(&Config{})
	if err == nil || !strings.Contains(err.Error(), "TCP_MAX_CONNECTIONS") {
		t.Fatalf("invalid env value should error naming the variable, got %v", err)
	}
}
