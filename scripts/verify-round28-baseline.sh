#!/usr/bin/env bash
# Baseline test execution only: never starts a hub or builds a release.
set -uo pipefail
umask 077
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
if [[ "${1:-}" == --help && $# == 1 ]]; then
  echo 'usage: verify-round28-baseline.sh [--go-only]'
  exit 0
fi
source "$REPO_ROOT/scripts/verify-round28-env.sh" || exit 2
cd "$REPO_ROOT" || exit 2
ROOT="$PWD/.verification/round28/baseline"
node scripts/verify-round28-safety.mjs --path "$ROOT/logs" >/dev/null || exit 2
mkdir -p "$ROOT/logs"
# Use only the prepopulated, isolated module cache. Missing pinned dependencies
# fail offline; never fall back to a personal cache or download new packages.
go list -m -json all > "$ROOT/module-cache-source.json" || exit 2
export GOFLAGS=-v
printf '%s\n' "TMPDIR=$TMPDIR" "TMP=$TMP" "TEMP=$TEMP" "GOCACHE=$GOCACHE" "GOMODCACHE=$GOMODCACHE" "PYTHONPYCACHEPREFIX=$PYTHONPYCACHEPREFIX" "npm_config_cache=$npm_config_cache" "HOME=$HOME" "XDG_CACHE_HOME=$XDG_CACHE_HOME" "XDG_CONFIG_HOME=$XDG_CONFIG_HOME" "GOPROXY=$GOPROXY" "GOSUMDB=$GOSUMDB" "GOFLAGS=$GOFLAGS" "CGO_ENABLED=$CGO_ENABLED" > "$ROOT/environment.txt"
{ go version; node --version; npm --version; python3 --version; } > "$ROOT/toolchain.txt" 2>&1
touch "$ROOT/commands.txt" "$ROOT/results.tsv"
FAILED=0
run() {
  local name="$1" rc=0
  shift
  printf '%q ' "$@" >> "$ROOT/commands.txt"; printf '\n' >> "$ROOT/commands.txt"
  "$@" > "$ROOT/logs/$name.log" 2>&1 || rc=$?
  printf '%s\texit=%s\n' "$name" "$rc" >> "$ROOT/results.tsv"
  if (( rc )); then FAILED=1; fi
}
run go-build-all go build ./...
run go-vet-all go vet ./...
run go-test-all go test ./...
if [[ "${1:-}" == --go-only ]]; then
  printf 'go_baseline_exit=%s\n' "$FAILED" > "$ROOT/go-completed.txt"
  exit "$FAILED"
fi
if [[ -f web/package.json ]]; then
  run npm-test npm --prefix web test
elif [[ -f package.json ]]; then
  run npm-test npm test
else
  printf 'No root/web package.json: npm test expected ENOENT; actual JS suite is make test-js.\n' > "$ROOT/npm-limitation.txt"
  run npm-test npm test
fi
# Avoid duplicating the JS suite covered by the npm script.
if [[ ! -f package.json && ! -f web/package.json ]]; then run make-test-js make test-js; fi
run make-test-py make test-py
run docs-integrity python3 scripts/check-docs.py --check-flags
printf 'baseline_exit=%s\n' "$FAILED" > "$ROOT/completed.txt"
printf 'Baseline complete: %s (exit %s)\n' "$ROOT/results.tsv" "$FAILED"
exit "$FAILED"
