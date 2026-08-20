#!/usr/bin/env bash
# Computes the version string + metadata. This is the single source of truth
# for version logic — push and release both go through the CI reusable workflow,
# which calls this, so they never re-derive it.
#
# Inputs (env):
#   RELEASE      "true" for tagged releases, "false" for dev push builds
#   BUILD_NUMBER required (from alloc-build-number.sh)
#   COMMIT       optional override (default: git rev-parse --short HEAD)
#   BUILD_TIME   optional override (default: now, UTC RFC3339)
#
# Outputs (stdout + GITHUB_ENV + GITHUB_OUTPUT):
#   version=  build_number=  commit=  build_time=
set -euo pipefail

RELEASE="${RELEASE:-false}"
BN="${BUILD_NUMBER:?BUILD_NUMBER is required}"

latest_tag_core() {
  local t
  t="$(git describe --tags --abbrev=0 2>/dev/null || true)"
  if [ -n "$t" ]; then echo "${t#v}"; else echo "0.1.0"; fi
}

COMMIT="${COMMIT:-$(git rev-parse --short HEAD)}"
BUILD_TIME="${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

if [ "$RELEASE" = "true" ]; then
  # Release: the SemVer comes from the git tag (the only source of SemVer).
  VERSION="${GITHUB_REF_NAME:?GITHUB_REF_NAME required for release}"
  case "$VERSION" in
    v[0-9]*.[0-9]*.[0-9]*) ;;
    *) echo "compute-version: release ref '$VERSION' is not a vX.Y.Z tag" >&2; exit 1 ;;
  esac
else
  # Dev: core from the latest tag, plus -dev.<build number>.
  core="$(latest_tag_core)"
  VERSION="v${core}-dev.${BN}"
fi

emit() { # emit "key=value"
  echo "$1"
  [ -n "${GITHUB_ENV:-}" ]    && printf '%s\n' "$1" >> "$GITHUB_ENV"
  [ -n "${GITHUB_OUTPUT:-}" ] && printf '%s\n' "$1" >> "$GITHUB_OUTPUT"
}
emit "version=$VERSION"
emit "build_number=$BN"
emit "commit=$COMMIT"
emit "build_time=$BUILD_TIME"
