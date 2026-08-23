package config

import (
	"fmt"
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
	p := writeFile(t, dir, "c.yaml", fmt.Sprintf("version: %d\nupstream: 1.2.3.4:80\n", currentConfigVersion))
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
	if !strings.Contains(string(migrated), fmt.Sprintf("version: %d", currentConfigVersion)) {
		t.Fatalf("migrated file missing version: %d:\n%s", currentConfigVersion, migrated)
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

// TestMigrationV2InjectsLegacyFamilyMismatch: configs written by pre-v3
// releases (version: 2) predate tcp.family-mismatch entirely — they were
// served by the historical auto-conversion behavior, where mixed
// address-family headers were re-encoded as-is (including ::ffff:-mapped
// IPv4 under AF_INET6). Migration must inject "legacy" explicitly rather
// than let the fresh-config default ("reject") apply, so upgrading never
// changes downstream wire behavior silently. The same pass back-fills
// tcp.idle-timeout (added in v0.3.2 without a version bump, so old files
// never gained the line) and materializes header-version with its default.
func TestMigrationV2InjectsLegacyFamilyMismatch(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "version: 2\nupstream: 1.2.3.4:80\n")
	var c Config
	if err := (fileSource{path: p}).Apply(&c); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !c.Migrated {
		t.Fatal("v2 config should trigger migration")
	}
	if c.TCPFamilyMismatch != "legacy" {
		t.Fatalf("migrated family-mismatch: want legacy (historical behavior), got %q", c.TCPFamilyMismatch)
	}
	if c.TCPHeaderVersion != "v2" {
		t.Fatalf("migrated header-version default: want v2, got %q", c.TCPHeaderVersion)
	}
	migrated, _ := os.ReadFile(p)
	s := string(migrated)
	for _, want := range []string{
		`family-mismatch: "legacy"`,
		`header-version: "v2"`,
		`idle-timeout: "5m"`, // back-filled into pre-v0.3.2-era files
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("migrated file missing %q:\n%s", want, s)
		}
	}
}

// TestMigrationPreservesTCPUserValues: if the user already wrote explicit
// values for the new fields (or for idle-timeout), migration keeps them —
// injection only fires for absent keys.
func TestMigrationPreservesTCPUserValues(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "version: 2\nupstream: 1.2.3.4:80\ntcp:\n  idle-timeout: \"90s\"\n  family-mismatch: \"unknown\"\n")
	var c Config
	if err := (fileSource{path: p}).Apply(&c); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.TCPFamilyMismatch != "unknown" {
		t.Fatalf("user family-mismatch not preserved: %q", c.TCPFamilyMismatch)
	}
	if c.TCPIdleTimeout != 90*time.Second {
		t.Fatalf("user idle-timeout not preserved: %v", c.TCPIdleTimeout)
	}
	migrated, _ := os.ReadFile(p)
	s := string(migrated)
	if !strings.Contains(s, `family-mismatch: "unknown"`) || strings.Contains(s, `"legacy"`) {
		t.Fatalf("migration must keep the user's value and not inject legacy:\n%s", s)
	}
}

// TestMigrationTCPOrderMatchesSampleTemplate: the migrated TCP section must
// list fields in the same order as the -init sample template, so both paths
// produce identical layouts and diffs stay readable.
func TestMigrationTCPOrderMatchesSampleTemplate(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "version: 2\nupstream: 1.2.3.4:80\n")
	var c Config
	if err := (fileSource{path: p}).Apply(&c); err != nil {
		t.Fatalf("apply: %v", err)
	}
	migrated, _ := os.ReadFile(p)
	fields := []string{"detect-timeout:", "idle-timeout:", "header-version:", "family-mismatch:", "max-connections:"}
	orderIn := func(s string) []int {
		out := make([]int, len(fields))
		for i, f := range fields {
			out[i] = strings.Index(s, f)
		}
		return out
	}
	got, want := orderIn(string(migrated)), orderIn(sampleConfig)
	for i := range fields {
		if got[i] < 0 {
			t.Fatalf("migrated file missing %q:\n%s", fields[i], migrated)
		}
		if want[i] < 0 {
			t.Fatalf("sampleConfig missing %q (registry/sample drift)", fields[i])
		}
	}
	for i := 1; i < len(fields); i++ {
		if (got[i-1] < got[i]) != (want[i-1] < want[i]) {
			t.Fatalf("TCP field order diverges from sample template at %q:\nmigrated:\n%s", fields[i], migrated)
		}
	}
}
