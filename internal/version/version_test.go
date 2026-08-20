package version

import (
	"strings"
	"testing"
)

// withVars temporarily overrides the ldflags-injectable vars for a test.
func withVars(t *testing.T, v, bn, commit, built string) {
	t.Helper()
	oldV, oldBN, oldC, oldB := Version, BuildNumber, Commit, BuildTime
	Version, BuildNumber, Commit, BuildTime = v, bn, commit, built
	t.Cleanup(func() { Version, BuildNumber, Commit, BuildTime = oldV, oldBN, oldC, oldB })
}

func TestShortStripsDev(t *testing.T) {
	withVars(t, "v0.1.0-dev.1112", "1112", "abc", "x")
	if got := Short(); got != "v0.1.0" {
		t.Fatalf("Short: want v0.1.0, got %q", got)
	}
}

func TestShortNoDev(t *testing.T) {
	withVars(t, "v0.1.0", "1113", "abc", "x")
	if got := Short(); got != "v0.1.0" {
		t.Fatalf("Short: want v0.1.0, got %q", got)
	}
}

func TestShortFallbackDev(t *testing.T) {
	// Default "dev" has no -dev. suffix; Short returns it as-is.
	withVars(t, "dev", "0", "unknown", "unknown")
	if got := Short(); got != "dev" {
		t.Fatalf("Short fallback: want dev, got %q", got)
	}
}

func TestIsRelease(t *testing.T) {
	withVars(t, "v1.1.2", "1113", "abc", "x")
	if !IsRelease() {
		t.Fatal("tag build should be release")
	}
	withVars(t, "v1.1.2-dev.1112", "1112", "abc", "x")
	if IsRelease() {
		t.Fatal("dev build should not be release")
	}
}

func TestStringDevBuild(t *testing.T) {
	withVars(t, "v0.1.0-dev.1112", "1112", "abcdef1234567890", "2026-08-21T00:58:12Z")
	s := String()
	for _, want := range []string{
		"ProxyDge v0.1.0-dev.1112",
		"Build: 1112",
		"Commit: abcdef1", // shortened to 7
		"Built: 2026-08-21T00:58:12Z",
		"Modified:",
		"Platform:",
		"Go: go",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("String missing %q:\n%s", want, s)
		}
	}
}

func TestStringReleaseBuild(t *testing.T) {
	withVars(t, "v0.1.0", "1113", "91a42b7", "2026-08-21T01:12:08Z")
	s := String()
	if !strings.Contains(s, "ProxyDge v0.1.0") || !strings.Contains(s, "Build: 1113") || !strings.Contains(s, "Commit: 91a42b7") {
		t.Errorf("release String mismatch:\n%s", s)
	}
}
