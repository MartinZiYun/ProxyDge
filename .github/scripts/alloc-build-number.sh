#!/usr/bin/env bash
# Global, monotonic, cross-workflow build-number allocator.
#
# Why a branch (not refs/builds/proxydge): GitHub only accepts pushes to
# refs/heads/* and refs/tags/*. A dedicated branch is the GitHub-compatible
# equivalent of a standalone ref.
#
# Storage (on branch "build-counter"):
#   number            -> current counter (plain text)
#   runs/<run_id>     -> the number allocated to that workflow run
#
# Concurrency: git push --force-with-lease=<ref>:<oldsha> is the compare-and-
#   set; on rejection (the branch moved) we re-fetch and retry.
# Re-run stability: GITHUB_RUN_ID is constant across attempts of a run, so we
#   look up runs/<run_id> first and reuse its number instead of allocating new.
#
# Outputs: prints "build_number=<n>", writes it to GITHUB_ENV and GITHUB_OUTPUT.
set -euo pipefail

REMOTE="${REMOTE:-origin}"
BRANCH="${BRANCH:-build-counter}"
RUN_ID="${GITHUB_RUN_ID:-local-$$}"
MAX_RETRIES="${MAX_RETRIES:-10}"

git config user.name  "proxydge-ci" 2>/dev/null || true
git config user.email "ci@proxydge" 2>/dev/null || true

# Isolate index operations so we never clobber the checkout's index.
export GIT_INDEX_FILE
GIT_INDEX_FILE="$(mktemp -t bc-index.XXXXXX 2>/dev/null || mktemp)"
trap 'rm -f "$GIT_INDEX_FILE"' EXIT

TRACK="refs/remotes/$REMOTE/$BRANCH"

fetch_counter() {
  # Fetch the counter branch into a tracking ref. Returns 0 if it exists.
  rm -f "$GIT_INDEX_FILE"
  if git fetch "$REMOTE" "$BRANCH:$TRACK" 2>/dev/null; then
    return 0
  fi
  return 1
}

read_file() { # read_file <ref> <path>  -> file contents, or "" if absent
  git show "$1:$2" 2>/dev/null || true
}

current_number() {
  local n
  n="$(read_file "$TRACK" "number")"
  if [ -z "$n" ]; then echo 0; else echo "$n"; fi
}

run_number() { # run_number <run_id> -> number already allocated, or "" 
  read_file "$TRACK" "runs/$1"
}

allocate_once() {
  fetch_counter || true
  local oldsha base n
  oldsha="$(git rev-parse --verify "$TRACK" 2>/dev/null || echo "")"

  # Re-run stability: reuse this run's previously-allocated number.
  n="$(run_number "$RUN_ID")"
  if [ -n "$n" ]; then
    echo "$n"
    return 0
  fi

  base="$(current_number)"
  n=$((base + 1))

  # Build a new tree from the old one (or empty), setting number + runs/<id>.
  rm -f "$GIT_INDEX_FILE"
  if [ -n "$oldsha" ]; then
    git read-tree "$oldsha"
  fi
  local num_blob run_blob newtree newcommit
  num_blob="$(printf '%s' "$n" | git hash-object -w --stdin)"
  run_blob="$(printf '%s' "$n" | git hash-object -w --stdin)"
  git update-index --add --replace --cacheinfo 100644 "$num_blob" number
  git update-index --add --replace --cacheinfo 100644 "$run_blob" "runs/$RUN_ID"
  newtree="$(git write-tree)"
  if [ -n "$oldsha" ]; then
    newcommit="$(git commit-tree "$newtree" -p "$oldsha" -m "build $n (run $RUN_ID)")"
  else
    newcommit="$(git commit-tree "$newtree" -m "build $n (run $RUN_ID)")"
  fi

  # CAS push.
  if [ -n "$oldsha" ]; then
    if git push --force-with-lease="refs/heads/$BRANCH:$oldsha" \
        "$REMOTE" "$newcommit:refs/heads/$BRANCH" >/dev/null 2>&1; then
      echo "$n"; return 0
    fi
    return 1 # conflict → retry
  else
    # Branch absent: try to create it (race: another creator may win).
    if git push "$REMOTE" "$newcommit:refs/heads/$BRANCH" >/dev/null 2>&1; then
      echo "$n"; return 0
    fi
    return 1 # someone created it → retry to read+allocate
  fi
}

for _ in $(seq 1 "$MAX_RETRIES"); do
  if n="$(allocate_once)"; then
    echo "build_number=$n"
    [ -n "${GITHUB_ENV:-}" ]   && echo "build_number=$n" >> "$GITHUB_ENV"
    [ -n "${GITHUB_OUTPUT:-}" ] && echo "build_number=$n" >> "$GITHUB_OUTPUT"
    exit 0
  fi
  sleep 1
done

echo "alloc-build-number: failed after $MAX_RETRIES retries" >&2
exit 1
