package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// --- defaults source ---

func TestDefaultsSource(t *testing.T) {
	var c Config
	if err := (defaultsSource{}).Apply(&c); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if c.Listen != ":9000" {
		t.Fatalf("listen default: want :9000, got %q", c.Listen)
	}
	if c.Policy != "use" {
		t.Fatalf("policy default: want use, got %q", c.Policy)
	}
	if c.DetectTimeout != time.Second {
		t.Fatalf("detect-timeout default: want 1s, got %v", c.DetectTimeout)
	}
	if c.Upstream != "127.0.0.1:9001" {
		t.Fatalf("upstream default: want 127.0.0.1:9001, got %q", c.Upstream)
	}
}

// --- file source (YAML) ---

func TestFileSourceYAML(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "version: 1\nlisten: 1.2.3.4:8000\nupstream: 10.0.0.1:5000\npolicy: require\ndetect-timeout: 250ms\n")
	var c Config
	if err := (fileSource{path: p}).Apply(&c); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.Listen != "1.2.3.4:8000" || c.Upstream != "10.0.0.1:5000" || c.Policy != "require" || c.DetectTimeout != 250*time.Millisecond {
		t.Fatalf("file source mismatch: %+v", c)
	}
}

func TestFileSourcePartialOnlySetsPresent(t *testing.T) {
	// Only upstream present in the file; other fields must be untouched.
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "version: 1\nupstream: 10.0.0.1:5000\n")
	var c Config
	c.Listen = "preset"
	if err := (fileSource{path: p}).Apply(&c); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.Upstream != "10.0.0.1:5000" {
		t.Fatalf("upstream not set: %q", c.Upstream)
	}
	if c.Listen != "preset" {
		t.Fatalf("absent field should be untouched: got %q", c.Listen)
	}
}

func TestFileSourceBadYAML(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "listen: [unterminated\n")
	var c Config
	if err := (fileSource{path: p}).Apply(&c); err == nil {
		t.Fatal("bad YAML should error")
	}
}

func TestFileSourceMissingFileErrors(t *testing.T) {
	// An explicitly-requested config file that does not exist is an error.
	var c Config
	err := (fileSource{path: filepath.Join(t.TempDir(), "nope.yaml")}).Apply(&c)
	if err == nil {
		t.Fatal("missing file should error when explicitly requested")
	}
}

func TestFileSourceOptionalMissingNoOp(t *testing.T) {
	// The auto-discovered default path is optional: if absent, silently skip.
	var c Config
	c.Listen = "preset"
	err := (fileSource{path: filepath.Join(t.TempDir(), "nope.yaml"), optional: true}).Apply(&c)
	if err != nil {
		t.Fatalf("optional missing file should not error: %v", err)
	}
	if c.Listen != "preset" {
		t.Fatalf("optional missing file should leave config untouched: %q", c.Listen)
	}
}

// --- env source ---

func TestEnvSource(t *testing.T) {
	t.Setenv("PROXYDGE_LISTEN", "9.9.9.9:7000")
	t.Setenv("PROXYDGE_UPSTREAM", "8.8.8.8:6000")
	t.Setenv("PROXYDGE_POLICY", "reject")
	t.Setenv("PROXYDGE_DETECT_TIMEOUT", "500ms")
	var c Config
	if err := (envSource{}).Apply(&c); err != nil {
		t.Fatalf("env: %v", err)
	}
	if c.Listen != "9.9.9.9:7000" || c.Upstream != "8.8.8.8:6000" || c.Policy != "reject" || c.DetectTimeout != 500*time.Millisecond {
		t.Fatalf("env source mismatch: %+v", c)
	}
}

func TestEnvSourceUnsetLeavesZero(t *testing.T) {
	var c Config
	if err := (envSource{}).Apply(&c); err != nil {
		t.Fatalf("env: %v", err)
	}
	if c.Listen != "" || c.Upstream != "" || c.Policy != "" {
		t.Fatalf("empty env should leave zero config, got %+v", c)
	}
}

func TestEnvSourceBadDuration(t *testing.T) {
	t.Setenv("PROXYDGE_DETECT_TIMEOUT", "not-a-duration")
	var c Config
	if err := (envSource{}).Apply(&c); err == nil {
		t.Fatal("bad duration env should error")
	}
}

// --- Load priority: flags > env > file > defaults ---

func TestLoadPriorityFlagsOverEnvOverFileOverDefaults(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "c.yaml", "version: 1\nlisten: 1.1.1.1:1000\nupstream: 2.2.2.2:2000\npolicy: require\ndetect-timeout: 100ms\n")
	t.Setenv("PROXYDGE_UPSTREAM", "3.3.3.3:3000") // env beats file
	t.Setenv("PROXYDGE_POLICY", "reject")         // env beats file
	// flags beat env on listen + policy
	args := []string{"-listen", "4.4.4.4:4000", "-policy", "use", "-config", filepath.Join(dir, "c.yaml")}

	c, err := Load(args)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Listen != "4.4.4.4:4000" {
		t.Fatalf("listen: flag should win, got %q", c.Listen)
	}
	if c.Upstream != "3.3.3.3:3000" {
		t.Fatalf("upstream: env should beat file, got %q", c.Upstream)
	}
	if c.Policy != "use" {
		t.Fatalf("policy: flag should beat env, got %q", c.Policy)
	}
	if c.DetectTimeout != 100*time.Millisecond {
		t.Fatalf("detect-timeout: file should win (no env/flag), got %v", c.DetectTimeout)
	}
}

// --- path resolution: -config overrides the exe-dir default ---

func TestResolveConfigPathExplicitFlag(t *testing.T) {
	path, optional := resolveConfigPath(map[string]bool{"config": true}, "/etc/p.yaml", "/exe/config.yaml")
	if path != "/etc/p.yaml" || optional {
		t.Fatalf("explicit -config: want (/etc/p.yaml,false), got (%q,%v)", path, optional)
	}
}

func TestResolveConfigPathDefaultOptional(t *testing.T) {
	path, optional := resolveConfigPath(map[string]bool{}, "", "/exe/config.yaml")
	if path != "/exe/config.yaml" || !optional {
		t.Fatalf("default: want (/exe/config.yaml,true), got (%q,%v)", path, optional)
	}
}

// --- Validate ---

func TestValidateMissingUpstream(t *testing.T) {
	c := Config{Listen: ":9000", Policy: "use", DetectTimeout: time.Second}
	if err := c.Validate(); err == nil {
		t.Fatal("missing upstream should fail validation")
	}
}

func TestValidateBadPolicy(t *testing.T) {
	c := Config{Upstream: "1.2.3.4:80", Policy: "bogus", DetectTimeout: time.Second}
	if err := c.Validate(); err == nil {
		t.Fatal("bad policy should fail validation")
	}
}

func TestValidateGood(t *testing.T) {
	c := Config{
		Listen:               ":9000",
		Upstream:             "1.2.3.4:80",
		Policy:               "use",
		DetectTimeout:        time.Second,
		LogConsoleLevel:      "info",
		LogConsoleFormat:     "text",
		UntrustedProxyAction: "reject",
		Protocol:             "tcp",
		IdleTimeout:          30 * time.Second,
		MaxSessions:          1024,
		MaxDatagramSize:      65535,
		UDPOutput:            "every_datagram",
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
}

// --- Load propagates validation ---

func TestLoadValidationFails(t *testing.T) {
	// Explicit empty upstream → Load must return a validation error.
	_, err := Load([]string{"-listen", ":0", "-upstream", ""})
	if err == nil {
		t.Fatal("Load should fail validation with empty upstream")
	}
}

// --- trust control config ---

func TestValidateBadUntrustedProxyAction(t *testing.T) {
	c := Config{Upstream: "1.2.3.4:80", Policy: "use", DetectTimeout: time.Second, UntrustedProxyAction: "bogus"}
	if err := c.Validate(); err == nil {
		t.Fatal("bad untrusted-proxy-action should fail validation")
	}
}

func TestValidateBadTrustedNetworkCIDR(t *testing.T) {
	c := Config{Upstream: "1.2.3.4:80", Policy: "use", DetectTimeout: time.Second, UntrustedProxyAction: "reject", TrustedNetworks: []string{"not-a-cidr"}}
	if err := c.Validate(); err == nil {
		t.Fatal("invalid CIDR should fail validation")
	}
}

func TestValidateTrustDefaults(t *testing.T) {
	var c Config
	if err := (defaultsSource{}).Apply(&c); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	c.Upstream = "1.2.3.4:80" // required
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults should validate: %v", err)
	}
	if c.UntrustedProxyAction != "reject" {
		t.Fatalf("untrusted-proxy-action default: want reject, got %q", c.UntrustedProxyAction)
	}
}

func TestParseCIDRListTrimSpace(t *testing.T) {
	got := parseCIDRList("10.0.0.0/8, 192.168.1.0/24")
	want := []string{"10.0.0.0/8", "192.168.1.0/24"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("parseCIDRList: want %v, got %v", want, got)
	}
}

func TestParseCIDRListEmptyValues(t *testing.T) {
	got := parseCIDRList("")
	if len(got) != 0 {
		t.Fatalf("empty string should produce empty list, got %v", got)
	}
	got = parseCIDRList("10.0.0.0/8,")
	if len(got) != 1 || got[0] != "10.0.0.0/8" {
		t.Fatalf("trailing comma: want [10.0.0.0/8], got %v", got)
	}
}

func TestEnvTrustedNetworks(t *testing.T) {
	t.Setenv("PROXYDGE_TRUSTED_NETWORKS", "10.0.0.0/8, 192.168.0.0/16")
	t.Setenv("PROXYDGE_UNTRUSTED_PROXY_ACTION", "strip")
	var c Config
	if err := (envSource{}).Apply(&c); err != nil {
		t.Fatalf("env: %v", err)
	}
	if len(c.TrustedNetworks) != 2 || c.TrustedNetworks[0] != "10.0.0.0/8" || c.TrustedNetworks[1] != "192.168.0.0/16" {
		t.Fatalf("trusted-networks: got %v", c.TrustedNetworks)
	}
	if c.UntrustedProxyAction != "strip" {
		t.Fatalf("untrusted-proxy-action: want strip, got %q", c.UntrustedProxyAction)
	}
}

func TestWarningsEmptyTrustedNetworks(t *testing.T) {
	c := Config{Upstream: "1.2.3.4:80", Policy: "use", DetectTimeout: time.Second, UntrustedProxyAction: "reject"}
	ws := c.Warnings()
	if len(ws) != 1 {
		t.Fatalf("empty trusted-networks: want 1 warning, got %d", len(ws))
	}
}

func TestWarningsStripAction(t *testing.T) {
	c := Config{Upstream: "1.2.3.4:80", Policy: "use", DetectTimeout: time.Second, UntrustedProxyAction: "strip", TrustedNetworks: []string{"10.0.0.0/8"}}
	ws := c.Warnings()
	if len(ws) != 1 {
		t.Fatalf("strip action: want 1 warning, got %d", len(ws))
	}
}

func TestWarningsBoth(t *testing.T) {
	c := Config{Upstream: "1.2.3.4:80", Policy: "use", DetectTimeout: time.Second, UntrustedProxyAction: "strip"}
	ws := c.Warnings()
	if len(ws) != 2 {
		t.Fatalf("both: want 2 warnings, got %d", len(ws))
	}
}

func TestWarningsNone(t *testing.T) {
	c := Config{Upstream: "1.2.3.4:80", Policy: "use", DetectTimeout: time.Second, UntrustedProxyAction: "reject", TrustedNetworks: []string{"10.0.0.0/8"}}
	ws := c.Warnings()
	if len(ws) != 0 {
		t.Fatalf("secure config: want 0 warnings, got %d: %v", len(ws), ws)
	}
}
