#!/usr/bin/env bash
#
# scripts/test-contributing.sh — verifies CONTRIBUTING.md's own
# documented commands actually work, by parsing every fenced ```bash
# block immediately preceded by an HTML comment marker
# `<!-- contributing:run -->` and running each one, in document order,
# inside a FRESH CLONE of the current worktree (not a copy — a real
# `git clone`, so untracked/ignored files never leak in) with an empty
# GOMODCACHE/GOCACHE/npm cache, simulating a brand new contributor's
# machine.
#
# This never touches the real user's Go module cache, GOCACHE, or the
# real hub (:8090) — every environment variable pointing at a cache or
# data directory is redirected under a throwaway sandbox root, and no
# command in CONTRIBUTING.md's current marked blocks starts a
# long-running server.
#
# Usage:
#   scripts/test-contributing.sh
#
# Exits non-zero if any marked command block fails.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONTRIBUTING_FILE="${REPO_ROOT}/CONTRIBUTING.md"
MARKER='<!-- contributing:run -->'

PASS_COUNT=0
FAIL_COUNT=0
SANDBOX=""

log() {
  echo "[test-contributing] $*"
}

cleanup() {
  local status=$?
  if [ -n "$SANDBOX" ] && [ -d "$SANDBOX" ]; then
    # Go's module cache marks extracted module files read-only
    # (deliberately, to prevent accidental edits) — chmod recursively
    # before rm -rf, or the removal fails halfway through with
    # "Permission denied" and leaks the sandbox.
    chmod -R u+rwX "$SANDBOX" 2>/dev/null || true
    rm -rf "$SANDBOX"
  fi
  exit "$status"
}
trap cleanup EXIT INT TERM

# extract_blocks reads CONTRIBUTING.md and prints each marked fenced
# bash block's content, one block per invocation of the callback via a
# NUL-separated stream on fd 3 (so embedded blank lines inside a block
# survive intact). Blocks are written to numbered files under
# "$1" (a directory) as block-0001.sh, block-0002.sh, ... in document
# order.
extract_blocks() {
  local out_dir="$1"
  python3 - "$CONTRIBUTING_FILE" "$out_dir" "$MARKER" <<'PYEOF'
import re
import sys
from pathlib import Path

md_path, out_dir, marker = sys.argv[1], sys.argv[2], sys.argv[3]
text = Path(md_path).read_text(encoding="utf-8")
lines = text.splitlines()

out = Path(out_dir)
out.mkdir(parents=True, exist_ok=True)

count = 0
i = 0
n = len(lines)
while i < n:
    if lines[i].strip() == marker:
        # Skip blank lines between the marker and the fence.
        j = i + 1
        while j < n and lines[j].strip() == "":
            j += 1
        if j < n and lines[j].strip().startswith("```bash"):
            j += 1
            body = []
            while j < n and lines[j].strip() != "```":
                body.append(lines[j])
                j += 1
            count += 1
            block_path = out / f"block-{count:04d}.sh"
            block_path.write_text("\n".join(body) + "\n", encoding="utf-8")
            i = j + 1
            continue
    i += 1

print(count)
PYEOF
}

# make_sandbox_clone clones the current worktree's HEAD (including any
# uncommitted-but-tracked changes are NOT included — a real `git
# clone` only ever sees committed history, which is the correct
# simulation of "a new contributor clones the repo") into a fresh temp
# directory and returns its path on stdout. --no-hardlinks (via
# --no-local's file-copy behavior) is required because $TMPDIR and the
# repository are frequently on different filesystems/devices in CI and
# sandboxed environments, where git's default --local hardlink
# optimization fails outright with "Invalid cross-device link".
make_sandbox_clone() {
  local dest="$1"
  git clone --quiet --no-hardlinks "$REPO_ROOT" "$dest"
}

main() {
  if [ ! -f "$CONTRIBUTING_FILE" ]; then
    echo "test-contributing.sh: CONTRIBUTING.md not found at ${CONTRIBUTING_FILE}" >&2
    exit 1
  fi

  SANDBOX="$(mktemp -d "${TMPDIR:-/tmp}/cloud-pulse-test-contributing.XXXXXX")"
  local blocks_dir="${SANDBOX}/blocks"
  local clone_dir="${SANDBOX}/clone"
  local gomodcache_dir="${SANDBOX}/gomodcache"
  local gocache_dir="${SANDBOX}/gocache"
  local home_dir="${SANDBOX}/home"
  mkdir -p "$blocks_dir" "$gomodcache_dir" "$gocache_dir" "$home_dir"

  log "extracting marked command blocks from CONTRIBUTING.md"
  local block_count
  block_count="$(extract_blocks "$blocks_dir")"
  if [ "$block_count" -eq 0 ]; then
    echo "test-contributing.sh: no '${MARKER}' blocks found in CONTRIBUTING.md" >&2
    exit 1
  fi
  log "found ${block_count} marked command block(s)"

  log "cloning ${REPO_ROOT} (HEAD only, committed tree) -> ${clone_dir}"
  make_sandbox_clone "$clone_dir"

  # Point every cache this repo's own tooling might consult at a fresh,
  # empty location under the sandbox — this is the "empty module
  # cache" requirement. HOME is also redirected so `git config
  # --global`/npm's own cache dir default lookups can't see the real
  # user's environment, and so nothing this test runs can accidentally
  # write into the real $HOME.
  export GOMODCACHE="$gomodcache_dir"
  export GOCACHE="$gocache_dir"
  export HOME="$home_dir"
  export CGO_ENABLED=0
  # Real git identity isn't needed (no commits are made by any marked
  # block today), but set one defensively in case a future block adds
  # a `git commit` — never the real user's identity.
  export GIT_AUTHOR_NAME="test-contributing"
  export GIT_AUTHOR_EMAIL="test-contributing@example.invalid"
  export GIT_COMMITTER_NAME="test-contributing"
  export GIT_COMMITTER_EMAIL="test-contributing@example.invalid"

  local i
  for i in $(seq -f '%04g' 1 "$block_count"); do
    local block_file="${blocks_dir}/block-${i}.sh"
    [ -f "$block_file" ] || continue
    log "--- running block ${i} ---"
    sed 's/^/    /' "$block_file"
    if (cd "$clone_dir" && bash "$block_file"); then
      log "PASS block ${i}"
      PASS_COUNT=$((PASS_COUNT + 1))
    else
      log "FAIL block ${i}"
      FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
  done

  echo
  echo "===================================================="
  echo "test-contributing results: ${PASS_COUNT} passed, ${FAIL_COUNT} failed (of ${block_count} block(s))"
  echo "===================================================="
  if [ "$FAIL_COUNT" -gt 0 ]; then
    exit 1
  fi
}

main "$@"
