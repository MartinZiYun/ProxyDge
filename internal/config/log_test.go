package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- defaults ---

func TestDefaultsLog(t *testing.T) {
	var c Config
	if err := (defaultsSource{}).Apply(&c); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if c.LogConsoleLevel != "info" || c.LogConsoleFormat != "text" {
		t.Fatalf("console defaults: want info/text, got %s/%s", c.LogConsoleLevel, c.LogConsoleFormat)
	}
	if c.LogFilePath != "" {
		t.Fatalf("file path default: want empty, got %q", c.LogFilePath)
	}
	if c.LogFileLevel != "info" || c.LogFileFormat != "text" {
		t.Fatalf("file defaults: want info/text, got %s/%s", c.LogFileLevel, c.LogFileFormat)
	}
}

// --- nested YAML log section ---

func TestFileSourceLogNested(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "version: 1\nupstream: 1.2.3.4:80\nlog:\n  console:\n    level: debug\n    format: json\n  file:\n    path: /var/log/p.log\n    level: warn\n    format: json\n")
	var c Config
	if err := (fileSource{path: p}).Apply(&c); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.LogConsoleLevel != "debug" || c.LogConsoleFormat != "json" {
		t.Fatalf("console: got %s/%s", c.LogConsoleLevel, c.LogConsoleFormat)
	}
	if c.LogFilePath != "/var/log/p.log" || c.LogFileLevel != "warn" || c.LogFileFormat != "json" {
		t.Fatalf("file: got path=%q level=%s fmt=%s", c.LogFilePath, c.LogFileLevel, c.LogFileFormat)
	}
}

func TestFileSourceLogPartialConsoleOnly(t *testing.T) {
	// Only log.console.level present; console.format and file fields untouched.
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "version: 1\nupstream: 1.2.3.4:80\nlog:\n  console:\n    level: debug\n")
	var c Config
	c.LogConsoleFormat = "preset-fmt" // absent in file => must survive
	c.LogFileLevel = "preset-lvl"     // absent => must survive
	if err := (fileSource{path: p}).Apply(&c); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.LogConsoleLevel != "debug" {
		t.Fatalf("console level not set: %q", c.LogConsoleLevel)
	}
	if c.LogConsoleFormat != "preset-fmt" {
		t.Fatalf("absent console format should be untouched: %q", c.LogConsoleFormat)
	}
	if c.LogFileLevel != "preset-lvl" {
		t.Fatalf("absent file level should be untouched: %q", c.LogFileLevel)
	}
}

// --- env source ---

func TestEnvSourceLog(t *testing.T) {
	t.Setenv("PROXYDGE_LOG_CONSOLE_LEVEL", "warn")
	t.Setenv("PROXYDGE_LOG_CONSOLE_FORMAT", "json")
	t.Setenv("PROXYDGE_LOG_FILE", "/tmp/x.log")
	t.Setenv("PROXYDGE_LOG_FILE_LEVEL", "error")
	t.Setenv("PROXYDGE_LOG_FILE_FORMAT", "json")
	var c Config
	if err := (envSource{}).Apply(&c); err != nil {
		t.Fatalf("env: %v", err)
	}
	if c.LogConsoleLevel != "warn" || c.LogConsoleFormat != "json" {
		t.Fatalf("console: got %s/%s", c.LogConsoleLevel, c.LogConsoleFormat)
	}
	if c.LogFilePath != "/tmp/x.log" || c.LogFileLevel != "error" || c.LogFileFormat != "json" {
		t.Fatalf("file: got path=%q level=%s fmt=%s", c.LogFilePath, c.LogFileLevel, c.LogFileFormat)
	}
}

// --- precedence: flag > env > file ---

func TestLoadLogPrecedenceFlagEnv(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "c.yaml", "version: 1\nupstream: 1.2.3.4:80\nlog:\n  console:\n    level: debug\n")
	t.Setenv("PROXYDGE_LOG_CONSOLE_LEVEL", "warn") // env beats file
	// flag beats env
	c, err := Load([]string{"-config", filepath.Join(dir, "c.yaml"), "-log-console-level", "info"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.LogConsoleLevel != "info" {
		t.Fatalf("flag should beat env, got %q", c.LogConsoleLevel)
	}
	// env-only: no -log-console-level, env beats file
	c2, err := Load([]string{"-config", filepath.Join(dir, "c.yaml")})
	if err != nil {
		t.Fatalf("load2: %v", err)
	}
	if c2.LogConsoleLevel != "warn" {
		t.Fatalf("env should beat file, got %q", c2.LogConsoleLevel)
	}
}

// --- validation ---

func TestValidateBadConsoleLevel(t *testing.T) {
	c := Config{Upstream: "1.2.3.4:80", Policy: "use", DetectTimeout: time.Second, LogConsoleLevel: "bogus"}
	if err := c.Validate(); err == nil {
		t.Fatal("bad console level should fail")
	}
}

func TestValidateBadConsoleFormat(t *testing.T) {
	c := Config{Upstream: "1.2.3.4:80", Policy: "use", DetectTimeout: time.Second, LogConsoleFormat: "yaml"}
	if err := c.Validate(); err == nil {
		t.Fatal("bad console format should fail")
	}
}

func TestValidateFileLevelOnlyWhenPathSet(t *testing.T) {
	// file path empty => file level/format not validated even if bogus
	c := Config{Upstream: "1.2.3.4:80", Policy: "use", DetectTimeout: time.Second,
		LogConsoleLevel: "info", LogConsoleFormat: "text", LogFileLevel: "bogus", UntrustedProxyAction: "reject",
		Protocol: "tcp", IdleTimeout: 30 * time.Second, MaxSessions: 1024, MaxDatagramSize: 65535, UDPOutput: "every_datagram"}
	if err := c.Validate(); err != nil {
		t.Fatalf("file off + bogus file level should not fail: %v", err)
	}
	// file path set => file level validated
	c.LogFilePath = "/tmp/x.log"
	if err := c.Validate(); err == nil {
		t.Fatal("file on + bogus file level should fail")
	}
}

// --- WriteSample round-trip ---

func TestWriteSampleRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := WriteSample(p); err != nil {
		t.Fatalf("WriteSample: %v", err)
	}
	// The sample has a default upstream (127.0.0.1:9001); loading it succeeds.
	c, err := Load([]string{"-config", p})
	if err != nil {
		t.Fatalf("sample should load with default upstream: %v", err)
	}
	if c.Upstream != "127.0.0.1:9001" {
		t.Fatalf("sample upstream: want 127.0.0.1:9001, got %q", c.Upstream)
	}
	// With -upstream flag, the flag overrides the sample's default.
	c, err = Load([]string{"-config", p, "-upstream", "1.2.3.4:80"})
	if err != nil {
		t.Fatalf("load sample with flag: %v", err)
	}
	if c.Upstream != "1.2.3.4:80" {
		t.Fatalf("upstream flag: want 1.2.3.4:80, got %q", c.Upstream)
	}
	if c.LogConsoleLevel != "info" || c.LogConsoleFormat != "text" {
		t.Fatalf("sample console: got %s/%s", c.LogConsoleLevel, c.LogConsoleFormat)
	}
	if c.LogFilePath != "" {
		t.Fatalf("sample file path should be empty, got %q", c.LogFilePath)
	}
}

func TestWriteSampleCreatesDir(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "dir", "config.yaml")
	if err := WriteSample(p); err != nil {
		t.Fatalf("WriteSample nested: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("sample not written: %v", err)
	}
}
