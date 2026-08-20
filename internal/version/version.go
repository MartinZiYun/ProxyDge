// Package version holds the build's version metadata. The four vars below are
// injected at build time via ldflags (e.g. by CI/GoReleaser); when unset they
// fall back so a plain `go build` still produces a meaningful `proxydge version`.
//
// CI is the single owner of version + build-number computation (see
// .github/workflows and .github/scripts); push and release workflows share it
// via the reusable CI workflow rather than re-deriving it.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

var (
	// Version is the full version string: "vX.Y.Z[-dev.<build>>]" for CI builds,
	// "vX.Y.Z" for tagged releases, "dev" for a plain local build.
	Version = "dev"
	// BuildNumber is the global, monotonic, cross-workflow build counter.
	BuildNumber = "0"
	// Commit is the git revision (short sha) the binary was built from.
	Commit = "unknown"
	// BuildTime is the build timestamp (RFC3339).
	BuildTime = "unknown"
)

// Short returns the SemVer core, stripping a "-dev.<n>" prerelease suffix.
// e.g. "v0.1.0-dev.1112" -> "v0.1.0"; "v0.1.0" -> "v0.1.0"; "dev" -> "dev".
func Short() string {
	if i := strings.Index(Version, "-dev."); i >= 0 {
		return Version[:i]
	}
	return Version
}

// IsRelease reports whether this is a tagged release (no -dev suffix and not
// the local "dev" fallback).
func IsRelease() bool {
	return Version != "dev" && !strings.Contains(Version, "-dev.")
}

// String returns the detailed, multi-line version banner. Fields not injected
// via ldflags fall back to runtime/debug.BuildInfo (VCS revision/time/modified)
// so local builds still show useful info. Modified/Platform/Go always come
// from runtime (CI builds from a clean checkout are Modified=false).
func String() string {
	commit, built, modified := Commit, BuildTime, false
	if bi, ok := debug.ReadBuildInfo(); ok {
		if commit == "" || commit == "unknown" {
			if r := vcsSetting(bi, "vcs.revision"); r != "" {
				commit = r
			}
		}
		if built == "" || built == "unknown" {
			built = vcsSetting(bi, "vcs.time")
		}
		modified = vcsSetting(bi, "vcs.modified") == "true"
	}
	commit = shortSha(commit)
	platform := runtime.GOOS + "/" + runtime.GOARCH
	return fmt.Sprintf("ProxyDge %s\nBuild: %s\nCommit: %s\nBuilt: %s\nModified: %v\nPlatform: %s\nGo: %s",
		Version, BuildNumber, commit, built, modified, platform, runtime.Version())
}

func vcsSetting(bi *debug.BuildInfo, key string) string {
	for _, s := range bi.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}

func shortSha(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}
