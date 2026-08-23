package config

import (
	"fmt"
	"os"
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

// TestTCPFieldsYAMLPropagation: the yamlTCP struct fields are useless without
// a matching application branch in fileSource.Apply — a missing branch parses
// cleanly and silently drops the value. Walk a real config file through the
// file source for all three new TCP fields.
func TestTCPFieldsYAMLPropagation(t *testing.T) {
	dir := t.TempDir()
	body := fmt.Sprintf("version: %d\nupstream: 1.2.3.4:80\ntcp:\n  detect-timeout: 250ms\n  idle-timeout: 90s\n  header-version: v1\n  family-mismatch: unknown\n  max-connections: 100\n", currentConfigVersion)
	p := writeFile(t, dir, "c.yaml", body)

	var c Config
	if err := (fileSource{path: p}).Apply(&c); err != nil {
		t.Fatalf("fileSource: %v", err)
	}
	if c.TCPHeaderVersion != "v1" {
		t.Errorf("yaml header-version not applied: got %q", c.TCPHeaderVersion)
	}
	if c.TCPFamilyMismatch != "unknown" {
		t.Errorf("yaml family-mismatch not applied: got %q", c.TCPFamilyMismatch)
	}
	if c.TCPMaxConnections != 100 {
		t.Errorf("yaml max-connections not applied: got %d", c.TCPMaxConnections)
	}
}

// TestMigrationPreservesExplicitZeroTCPMaxConnections: an explicit
// max-connections: 0 in a migrated config means unlimited and must survive —
// both in the rewritten file and in memory. The migration generator defaults
// ABSENT keys to 4096 but must never coerce an explicit 0 into that default.
func TestMigrationPreservesExplicitZeroTCPMaxConnections(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "version: 2\nupstream: 1.2.3.4:80\ntcp:\n  max-connections: 0\n")

	var c Config
	if err := (fileSource{path: p}).Apply(&c); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !c.Migrated {
		t.Fatal("v2 config should trigger migration")
	}
	if c.TCPMaxConnections != 0 {
		t.Fatalf("explicit 0 (unlimited) coerced during migration: got %d", c.TCPMaxConnections)
	}
	migrated, _ := os.ReadFile(p)
	if !strings.Contains(string(migrated), "max-connections: 0") {
		t.Fatalf("migrated file lost explicit max-connections: 0:\n%s", migrated)
	}
}
