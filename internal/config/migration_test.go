package config

import (
	"os"
	"strings"
	"testing"
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
