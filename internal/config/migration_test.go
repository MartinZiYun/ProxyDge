package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestMigrationMissingVersionErrors: a config file without a version field
// is an error — we don't silently migrate from unversioned configs.
func TestMigrationMissingVersionErrors(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "upstream: 1.2.3.4:80\n")
	var c Config
	err := (fileSource{path: p}).Apply(&c)
	if err == nil {
		t.Fatal("missing version should error")
	}
}

// TestMigrationFutureVersionErrors: a config file with a version higher than
// currentVersion means the user downgraded ProxyDge — must error.
func TestMigrationFutureVersionErrors(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "version: 999\nupstream: 1.2.3.4:80\n")
	var c Config
	err := (fileSource{path: p}).Apply(&c)
	if err == nil {
		t.Fatal("future version should error")
	}
}

// TestMigrationCurrentVersionNoOp: a config at the current version triggers
// no migration — file is untouched, Migrated is false.
func TestMigrationCurrentVersionNoOp(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "version: 2\nupstream: 1.2.3.4:80\n")
	orig, _ := os.ReadFile(p)
	var c Config
	if err := (fileSource{path: p}).Apply(&c); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.Migrated {
		t.Fatal("current version should not trigger migration")
	}
	got, _ := os.ReadFile(p)
	if string(got) != string(orig) {
		t.Fatal("file should be untouched at current version")
	}
}

// TestMigrationOldVersion: a config with an old version is migrated —
// backup created, file updated, Migrated flag set.
func TestMigrationOldVersion(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "version: 0\nupstream: 1.2.3.4:80\n")
	orig, _ := os.ReadFile(p)

	var c Config
	if err := (fileSource{path: p}).Apply(&c); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !c.Migrated {
		t.Fatal("old version should trigger migration")
	}
	// Backup file created with original content.
	bak, err := os.ReadFile(p + ".bak")
	if err != nil {
		t.Fatalf("backup file not created: %v", err)
	}
	if string(bak) != string(orig) {
		t.Fatal("backup file content != original")
	}
	// File now has current version.
	migrated, _ := os.ReadFile(p)
	if !strings.Contains(string(migrated), "version: 2") {
		t.Fatalf("migrated file missing version: 2:\n%s", migrated)
	}
}

// TestMigrationPreservesUserValues: fields the user already set are
// preserved after migration.
func TestMigrationPreservesUserValues(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "version: 0\nupstream: 1.2.3.4:80\npolicy: require\n")
	var c Config
	if err := (fileSource{path: p}).Apply(&c); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.Upstream != "1.2.3.4:80" {
		t.Fatalf("upstream not preserved: %q", c.Upstream)
	}
	if c.Policy != "require" {
		t.Fatalf("policy not preserved: %q", c.Policy)
	}
}

// TestMigrationPreservesUnknownFields: fields ProxyDge doesn't know about
// are preserved in the migrated file, not dropped.
func TestMigrationPreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "version: 0\nupstream: 1.2.3.4:80\nfuture-field: hello\n")
	var c Config
	if err := (fileSource{path: p}).Apply(&c); err != nil {
		t.Fatalf("apply: %v", err)
	}
	migrated, _ := os.ReadFile(p)
	if !strings.Contains(string(migrated), "future-field") {
		t.Fatalf("unknown field not preserved:\n%s", migrated)
	}
}

// TestMigrationAddsMissingFields: fields missing from the old config are
// added with defaults + comments in the migrated file.
func TestMigrationAddsMissingFields(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "version: 0\nupstream: 1.2.3.4:80\n")
	var c Config
	if err := (fileSource{path: p}).Apply(&c); err != nil {
		t.Fatalf("apply: %v", err)
	}
	migrated, _ := os.ReadFile(p)
	s := string(migrated)
	if !strings.Contains(s, "trusted-networks") {
		t.Fatalf("migrated file missing trusted-networks:\n%s", s)
	}
	if !strings.Contains(s, "untrusted-proxy-action") {
		t.Fatalf("migrated file missing untrusted-proxy-action:\n%s", s)
	}
	if !strings.Contains(s, "listen") {
		t.Fatalf("migrated file missing listen:\n%s", s)
	}
}

// TestMigrationV1FlatFields: v1 flat fields (detect-timeout, idle-timeout,
// max-sessions, max-datagram-size, udp-output) are mapped to v2 nested
// fields — both in the migrated file and in the in-memory Config.
func TestMigrationV1FlatFields(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "version: 1\nupstream: 1.2.3.4:80\ndetect-timeout: 250ms\nidle-timeout: 60s\nmax-sessions: 256\nmax-datagram-size: 4096\nudp-output: first_datagram\n")
	var c Config
	if err := (fileSource{path: p}).Apply(&c); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !c.Migrated {
		t.Fatal("v1 config should trigger migration")
	}
	// In-memory values must reflect the v1 flat field values.
	if c.DetectTimeout != 250*time.Millisecond {
		t.Fatalf("detect-timeout: want 250ms, got %v", c.DetectTimeout)
	}
	if c.IdleTimeout != 60*time.Second {
		t.Fatalf("idle-timeout: want 60s, got %v", c.IdleTimeout)
	}
	if c.MaxSessions != 256 {
		t.Fatalf("max-sessions: want 256, got %d", c.MaxSessions)
	}
	if c.MaxDatagramSize != 4096 {
		t.Fatalf("max-datagram-size: want 4096, got %d", c.MaxDatagramSize)
	}
	if c.UDPHeaderMode != "first_datagram" {
		t.Fatalf("header-mode: want first_datagram, got %q", c.UDPHeaderMode)
	}
	// Migrated file must have v2 nested fields with the user's values.
	migrated, _ := os.ReadFile(p)
	s := string(migrated)
	for _, want := range []string{
		`tcp:`,
		`detect-timeout: "250ms"`,
		`udp:`,
		`idle-timeout: "60s"`,
		`max-sessions: 256`,
		`max-datagram-size: 4096`,
		`header-mode: "first_datagram"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("migrated file missing %q:\n%s", want, s)
		}
	}
	// v1 flat fields must NOT appear as unknown fields at the bottom.
	for _, bad := range []string{"\ndetect-timeout:", "\nidle-timeout:", "\nmax-sessions:", "\nmax-datagram-size:", "\nudp-output:"} {
		if strings.Contains(s, bad) {
			t.Fatalf("v1 flat field leaked as unknown:\n%s", s)
		}
	}
}
