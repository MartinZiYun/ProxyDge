package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartEmptyUpstreamFails(t *testing.T) {
	// Explicitly empty upstream triggers validation error (exit 2).
	if code := run([]string{"start", "-listen", "127.0.0.1:0", "-upstream", ""}); code != 2 {
		t.Fatalf("empty -upstream: want exit 2, got %d", code)
	}
}

func TestStartInvalidPolicy(t *testing.T) {
	if code := run([]string{"start", "-upstream", "127.0.0.1:1", "-policy", "bogus"}); code != 2 {
		t.Fatalf("invalid -policy: want exit 2, got %d", code)
	}
}

func TestStartValidPolicyAccepted(t *testing.T) {
	// Valid flags reach listen/serve; bad listen forces runtime exit 1.
	if code := run([]string{"start", "-upstream", "127.0.0.1:1", "-policy", "use", "-listen", "bad-listen"}); code != 1 {
		t.Fatalf("bad listen: want exit 1 (runtime), got %d", code)
	}
}

func TestStartEnvProvidesUpstream(t *testing.T) {
	t.Setenv("PROXYDGE_UPSTREAM", "127.0.0.1:1")
	if code := run([]string{"start", "-listen", "bad-listen"}); code != 1 {
		t.Fatalf("env upstream + bad listen: want exit 1, got %d", code)
	}
}

func captureOut(t *testing.T, fn func()) string {
	t.Helper()
	var sb strings.Builder
	orig := out
	out = &sb
	fn()
	out = orig
	return sb.String()
}

func TestBareShowsHelp(t *testing.T) {
	var code int
	got := captureOut(t, func() { code = run(nil) })
	if code != 0 {
		t.Fatalf("bare: want exit 0, got %d", code)
	}
	if !strings.Contains(got, "Usage:") || !strings.Contains(got, "Commands:") {
		t.Fatalf("bare output should be help; got %q", got)
	}
}

func TestHelpCommand(t *testing.T) {
	var code int
	got := captureOut(t, func() { code = run([]string{"help"}) })
	if code != 0 {
		t.Fatalf("help: want exit 0, got %d", code)
	}
	if !strings.Contains(got, "proxydge <command>") {
		t.Fatalf("help output missing usage; got %q", got)
	}
}

func TestVersion(t *testing.T) {
	var code int
	got := captureOut(t, func() { code = run([]string{"version"}) })
	if code != 0 {
		t.Fatalf("version: want exit 0, got %d", code)
	}
	if !strings.HasPrefix(got, "ProxyDge ") {
		t.Fatalf("version output: %q", got)
	}
}

func TestVersionShort(t *testing.T) {
	var code int
	got := captureOut(t, func() { code = run([]string{"version", "--short"}) })
	if code != 0 {
		t.Fatalf("version --short: want exit 0, got %d", code)
	}
	// Local build fallback Version="dev" -> Short()="dev".
	if got = strings.TrimSpace(got); got != "dev" {
		t.Fatalf("version --short: want %q (fallback), got %q", "dev", got)
	}
}

func TestInitWritesSample(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.yaml")
	if code := run([]string{"init", "-config", path}); code != 0 {
		t.Fatalf("init: want exit 0, got %d", code)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("sample not written: %v", err)
	}
}

func TestInitRefusesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.yaml")
	// First init succeeds.
	if code := run([]string{"init", "-config", path}); code != 0 {
		t.Fatalf("first init: want exit 0, got %d", code)
	}
	// Second init without -force should refuse (exit 2).
	if code := run([]string{"init", "-config", path}); code != 2 {
		t.Fatalf("second init without -force: want exit 2, got %d", code)
	}
}

func TestInitForceOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.yaml")
	// First init.
	if code := run([]string{"init", "-config", path}); code != 0 {
		t.Fatalf("first init: want exit 0, got %d", code)
	}
	// Second init with -force should succeed.
	if code := run([]string{"init", "-config", path, "-force"}); code != 0 {
		t.Fatalf("init -force: want exit 0, got %d", code)
	}
}

func TestUnknownCommand(t *testing.T) {
	if code := run([]string{"bogus"}); code != 2 {
		t.Fatalf("unknown command: want exit 2, got %d", code)
	}
}

func TestStartInvalidUntrustedProxyAction(t *testing.T) {
	if code := run([]string{"start", "-upstream", "127.0.0.1:1", "-untrusted-proxy-action", "bogus", "-listen", "bad-listen"}); code != 2 {
		t.Fatalf("invalid -untrusted-proxy-action: want exit 2, got %d", code)
	}
}

func TestStartValidUntrustedProxyAction(t *testing.T) {
	// Valid flag reaches listen/serve; bad listen forces runtime exit 1.
	if code := run([]string{"start", "-upstream", "127.0.0.1:1", "-untrusted-proxy-action", "strip", "-listen", "bad-listen"}); code != 1 {
		t.Fatalf("valid -untrusted-proxy-action=strip: want exit 1 (runtime), got %d", code)
	}
}

func TestStartValidTrustedNetworks(t *testing.T) {
	if code := run([]string{"start", "-upstream", "127.0.0.1:1", "-trusted-networks", "10.0.0.0/8", "-listen", "bad-listen"}); code != 1 {
		t.Fatalf("valid -trusted-networks: want exit 1 (runtime), got %d", code)
	}
}

func TestStartValidLang(t *testing.T) {
	if code := run([]string{"start", "-upstream", "127.0.0.1:1", "-lang", "zh-CN", "-listen", "bad-listen"}); code != 1 {
		t.Fatalf("valid -lang: want exit 1 (runtime), got %d", code)
	}
}

func TestStartInvalidLang(t *testing.T) {
	if code := run([]string{"start", "-upstream", "127.0.0.1:1", "-lang", "bogus", "-listen", "bad-listen"}); code != 2 {
		t.Fatalf("invalid -lang: want exit 2, got %d", code)
	}
}
