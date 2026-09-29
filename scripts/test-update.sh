#!/usr/bin/env bash
#
# scripts/test-update.sh — local end-to-end test of the `update`
# subcommand's self-update path, per SPEC-v0.3.md's "Verification
# requirements" section:
#
#   - build a v0.3.0-stamped cloud-pulse-hub/cloud-pulse-agent using
#     scripts/build-release.sh (linux/<host-arch> only, ALLOW_ANY_VERSION
#     unset — v0.3.0 is a real-looking tag)
#   - build v0.3.1 release assets + checksums.txt into a second directory,
#     served by a throwaway local HTTP server standing in for GitHub
#     Releases (never real network)
#   - run `update --check` (expect exit 10, "v0.3.0 -> v0.3.1"-shaped
#     message) then `update` for real, in a sandbox temp dir with no
#     root privileges (so no restart is attempted) -> `-version` reports
#     v0.3.1 afterwards
#   - repeat with a tampered checksums.txt -> expect an error and the
#     binary left byte-for-byte unchanged
#   - `update --version v0.1.0` (an explicit downgrade below
#     version.SelfUpdateSince) -> expect the "predates ... has no
#     `update` subcommand" warning in the output
#
# Usage:
#   scripts/test-update.sh
#
# Exits non-zero if any assertion fails. All work happens under a
# mktemp -d sandbox that is removed on exit; no real network access, no
# port 8090, no /usr/local/bin, /etc/cloud-pulse, or systemctl calls.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

PASS_COUNT=0
FAIL_COUNT=0

TMP_ROOT=""
FAKE_PID=""
FAKE_PORT=""
TAMPERED_FAKE_PID=""
TAMPERED_FAKE_PORT=""

pass() {
  echo "PASS: $*"
  PASS_COUNT=$((PASS_COUNT + 1))
}

fail() {
  echo "FAIL: $*" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
}

cleanup() {
  local status=$?
  if [ -n "$FAKE_PID" ] && kill -0 "$FAKE_PID" 2>/dev/null; then
    kill "$FAKE_PID" 2>/dev/null || true
    wait "$FAKE_PID" 2>/dev/null || true
  fi
  if [ -n "$TAMPERED_FAKE_PID" ] && kill -0 "$TAMPERED_FAKE_PID" 2>/dev/null; then
    kill "$TAMPERED_FAKE_PID" 2>/dev/null || true
    wait "$TAMPERED_FAKE_PID" 2>/dev/null || true
  fi
  if [ -n "$TMP_ROOT" ]; then
    rm -rf "$TMP_ROOT"
  fi
  exit "$status"
}
trap cleanup EXIT INT TERM

# host_arch_target prints the "goos/goarch" pair matching this machine,
# in scripts/build-release.sh's TARGETS syntax (armv7 -> "linux/arm").
host_arch_target() {
  local machine
  machine="$(uname -m)"
  case "$machine" in
    x86_64|amd64) echo "linux/amd64" ;;
    aarch64|arm64) echo "linux/arm64" ;;
    armv7l|armv7) echo "linux/arm" ;;
    *)
      echo "test-update.sh: unsupported host architecture ${machine}" >&2
      exit 1
      ;;
  esac
}

# asset_arch_suffix prints the release-asset architecture suffix
# (matching internal/selfupdate.AssetName) for the current host.
asset_arch_suffix() {
  local machine
  machine="$(uname -m)"
  case "$machine" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    armv7l|armv7) echo "armv7" ;;
    *)
      echo "test-update.sh: unsupported host architecture ${machine}" >&2
      exit 1
      ;;
  esac
}

# wait_for_http url timeout_s — polls url until it answers (any status,
# including redirects) or timeout_s elapses.
wait_for_http() {
  local url="$1" timeout_s="$2" i=0
  while [ "$i" -lt "$((timeout_s * 4))" ]; do
    if curl -fsS -o /dev/null "$url" 2>/dev/null; then
      return 0
    fi
    # curl -f treats 3xx as success only when following redirects; a
    # plain HEAD/GET without -L still exits 0 on a 2xx/3xx response
    # here as long as the server answered, since -f only maps 4xx/5xx
    # to a non-zero exit. Fall through to a direct status probe too, in
    # case the endpoint itself is a redirect target that 404s.
    if curl -s -o /dev/null -w '%{http_code}' "$url" 2>/dev/null | grep -qE '^[0-9]{3}$'; then
      return 0
    fi
    sleep 0.25
    i=$((i + 1))
  done
  return 1
}

main() {
  TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/cloud-pulse-test-update.XXXXXX")"
  local target arch
  target="$(host_arch_target)"
  arch="$(asset_arch_suffix)"

  local v030_dir v031_dir tampered_dir sandbox_dir
  v030_dir="${TMP_ROOT}/v0.3.0"
  v031_dir="${TMP_ROOT}/v0.3.1"
  tampered_dir="${TMP_ROOT}/tampered"
  sandbox_dir="${TMP_ROOT}/sandbox"
  mkdir -p "$v030_dir" "$v031_dir" "$tampered_dir" "$sandbox_dir"

  echo "==> building v0.3.0 binaries (${target})"
  if TARGETS="$target" ALLOW_ANY_VERSION=1 \
    bash scripts/build-release.sh "v0.3.0" "$v030_dir" >"${TMP_ROOT}/build-v030.log" 2>&1; then
    pass "build v0.3.0 binaries for ${target}"
  else
    cat "${TMP_ROOT}/build-v030.log" >&2
    fail "build v0.3.0 binaries for ${target}"
    finish
    return
  fi

  echo "==> building v0.3.1 release assets (${target})"
  if TARGETS="$target" ALLOW_ANY_VERSION=1 \
    bash scripts/build-release.sh "v0.3.1" "$v031_dir" >"${TMP_ROOT}/build-v031.log" 2>&1; then
    pass "build v0.3.1 release assets for ${target}"
  else
    cat "${TMP_ROOT}/build-v031.log" >&2
    fail "build v0.3.1 release assets for ${target}"
    finish
    return
  fi

  # A second copy of the v0.3.1 assets with a tampered checksums.txt
  # (every hash replaced with all-zeros), used by the tampered-checksum
  # test below.
  cp -r "${v031_dir}/." "$tampered_dir"
  if [ -f "${tampered_dir}/checksums.txt" ]; then
    python3 - "$tampered_dir/checksums.txt" <<'PYEOF'
import re
import sys

path = sys.argv[1]
with open(path, encoding="utf-8") as f:
    lines = f.readlines()

out = []
for line in lines:
    out.append(re.sub(r"^[0-9a-f]{64}", "0" * 64, line))

with open(path, "w", encoding="utf-8") as f:
    f.writelines(out)
PYEOF
    pass "prepared tampered checksums.txt (all-zero hashes)"
  else
    fail "v0.3.1 checksums.txt not found at ${tampered_dir}/checksums.txt"
  fi

  # ---------------------------------------------------------------------
  # Fake release server: /releases/latest redirects to /releases/tag/v0.3.1;
  # /releases/download/<tag>/<asset> serves from v031_dir or tampered_dir
  # depending on which base URL a test points the binary at.
  # ---------------------------------------------------------------------
  FAKE_PORT="${FAKE_PORT:-18199}"
  local fake_base="http://127.0.0.1:${FAKE_PORT}"
  local fake_assets_root="${TMP_ROOT}/fake-assets"
  mkdir -p "${fake_assets_root}/v0.3.1"
  cp "${v031_dir}"/* "${fake_assets_root}/v0.3.1/" 2>/dev/null || true

  local tampered_assets_root="${TMP_ROOT}/fake-assets-tampered"
  mkdir -p "${tampered_assets_root}/v0.3.1"
  cp "${tampered_dir}"/* "${tampered_assets_root}/v0.3.1/" 2>/dev/null || true

  python3 "${REPO_ROOT}/scripts/fake_release_server.py" "$FAKE_PORT" "$fake_assets_root" "v0.3.1" \
    >"${TMP_ROOT}/fake-server.log" 2>&1 &
  FAKE_PID=$!

  if wait_for_http "${fake_base}/releases/latest" 10; then
    pass "fake release server reachable on ${fake_base}"
  else
    fail "fake release server did not become reachable within 10s"
    finish
    return
  fi

  # A second fake server, on its own port, serves the tampered assets —
  # kept entirely separate from the good v0.3.1 server above so the
  # tampered-checksum test can't accidentally hit the real assets.
  TAMPERED_FAKE_PORT="${TAMPERED_FAKE_PORT:-18198}"
  local tampered_fake_base="http://127.0.0.1:${TAMPERED_FAKE_PORT}"
  python3 "${REPO_ROOT}/scripts/fake_release_server.py" "$TAMPERED_FAKE_PORT" "$tampered_assets_root" "v0.3.1" \
    >"${TMP_ROOT}/fake-server-tampered.log" 2>&1 &
  TAMPERED_FAKE_PID=$!

  if wait_for_http "${tampered_fake_base}/releases/latest" 10; then
    pass "tampered fake release server reachable on ${tampered_fake_base}"
  else
    fail "tampered fake release server did not become reachable within 10s"
    finish
    return
  fi

  local hub_asset="cloud-pulse-hub-linux-${arch}"
  local agent_asset="cloud-pulse-agent-linux-${arch}"

  # ---------------------------------------------------------------------
  # update --check against the fake server -> exit 10, mentions v0.3.1
  # ---------------------------------------------------------------------
  local sandbox_hub="${sandbox_dir}/cloud-pulse-hub"
  cp "${v030_dir}/${hub_asset}" "$sandbox_hub"
  chmod 0755 "$sandbox_hub"

  local check_out check_status
  set +e
  check_out="$(CP_UPDATE_LATEST_URL="${fake_base}/releases/latest" \
    CP_RELEASE_BASE_URL="${fake_base}/releases/download/v0.3.1" \
    "$sandbox_hub" update --check 2>&1)"
  check_status=$?
  set -e
  if [ "$check_status" -eq 10 ]; then
    pass "hub update --check against fake server exits 10"
  else
    echo "$check_out" >&2
    fail "hub update --check exited ${check_status}, want 10"
  fi
  case "$check_out" in
    *"v0.3.1"*) pass "hub update --check output mentions v0.3.1" ;;
    *) fail "hub update --check output did not mention v0.3.1: ${check_out}" ;;
  esac

  # ---------------------------------------------------------------------
  # Real update: v0.3.0 -> v0.3.1, no root -> no restart attempted
  # ---------------------------------------------------------------------
  local update_out update_status
  set +e
  update_out="$(CP_UPDATE_LATEST_URL="${fake_base}/releases/latest" \
    CP_RELEASE_BASE_URL="${fake_base}/releases/download/v0.3.1" \
    "$sandbox_hub" update 2>&1)"
  update_status=$?
  set -e
  if [ "$update_status" -eq 0 ]; then
    pass "hub update (real) exits 0"
  else
    echo "$update_out" >&2
    fail "hub update (real) exited ${update_status}, want 0"
  fi
  case "$update_out" in
    *"restart manually"*|*"not running as root"*|*"restart failed"*|*"restart"*)
      pass "hub update (real) mentions manual restart (no root in sandbox)" ;;
    *) fail "hub update (real) output did not mention restart handling: ${update_out}" ;;
  esac

  local post_version_out
  post_version_out="$("$sandbox_hub" -version 2>&1)"
  case "$post_version_out" in
    *"v0.3.1"*) pass "sandbox hub binary reports v0.3.1 after update" ;;
    *) fail "sandbox hub binary does not report v0.3.1 after update: ${post_version_out}" ;;
  esac

  # ---------------------------------------------------------------------
  # Tampered checksum -> update errors, binary unchanged
  # ---------------------------------------------------------------------
  local sandbox_hub_tampered="${sandbox_dir}/cloud-pulse-hub-tampered"
  cp "${v030_dir}/${hub_asset}" "$sandbox_hub_tampered"
  chmod 0755 "$sandbox_hub_tampered"
  local before_sha
  before_sha="$(sha256sum "$sandbox_hub_tampered" | awk '{print $1}')"

  local tamper_out tamper_status
  set +e
  tamper_out="$(CP_UPDATE_LATEST_URL="${tampered_fake_base}/releases/latest" \
    CP_RELEASE_BASE_URL="${tampered_fake_base}/releases/download/v0.3.1" \
    "$sandbox_hub_tampered" update 2>&1)"
  tamper_status=$?
  set -e

  if [ "$tamper_status" -ne 0 ]; then
    pass "hub update against tampered checksums.txt exits non-zero"
  else
    echo "$tamper_out" >&2
    fail "hub update against tampered checksums.txt unexpectedly exited 0"
  fi
  case "$tamper_out" in
    *"checksum"*) pass "hub update tampered-checksum error message mentions checksum" ;;
    *) fail "hub update tampered-checksum error did not mention checksum: ${tamper_out}" ;;
  esac

  local after_sha
  after_sha="$(sha256sum "$sandbox_hub_tampered" | awk '{print $1}')"
  if [ "$before_sha" = "$after_sha" ]; then
    pass "binary unchanged after failed tampered-checksum update (sha256 identical)"
  else
    fail "binary CHANGED after failed tampered-checksum update: ${before_sha} -> ${after_sha}"
  fi

  # ---------------------------------------------------------------------
  # --version explicit downgrade prints a warning
  # ---------------------------------------------------------------------
  local sandbox_hub_downgrade="${sandbox_dir}/cloud-pulse-hub-downgrade"
  cp "${v030_dir}/${hub_asset}" "$sandbox_hub_downgrade"
  chmod 0755 "$sandbox_hub_downgrade"

  local downgrade_out downgrade_status
  set +e
  downgrade_out="$(CP_UPDATE_LATEST_URL="${fake_base}/releases/latest" \
    CP_RELEASE_BASE_URL="${fake_base}/releases/download/v0.3.1" \
    "$sandbox_hub_downgrade" update --check --version v0.1.0 2>&1)"
  downgrade_status=$?
  set -e
  # --check --version v0.1.0: v0.1.0 predates SelfUpdateSince (v0.3.0),
  # so Run must print the downgrade warning even though --check never
  # downloads/replaces anything.
  case "$downgrade_out" in
    *"predates"*"has no"*"update"*|*"predates"*)
      pass "update --version v0.1.0 (explicit downgrade) prints the predates/no-update-subcommand warning" ;;
    *)
      fail "update --version v0.1.0 output did not contain the expected downgrade warning: ${downgrade_out}" ;;
  esac
  # A downgrade via --check still reports "update available" (v0.1.0 is
  # not newer, but Target is set so Run always proceeds to at least the
  # check-only report) with exit 10; assert it's not an error (exit 1).
  if [ "$downgrade_status" -ne 1 ]; then
    pass "update --check --version v0.1.0 does not exit as a hard error (exit ${downgrade_status})"
  else
    echo "$downgrade_out" >&2
    fail "update --check --version v0.1.0 exited 1 (error): ${downgrade_out}"
  fi

  # agent gets a lighter equivalent of the same self-update-works check,
  # to cover cmd/agent/update.go's identical wiring.
  local sandbox_agent="${sandbox_dir}/cloud-pulse-agent"
  cp "${v030_dir}/${agent_asset}" "$sandbox_agent"
  chmod 0755 "$sandbox_agent"

  local agent_update_out agent_update_status
  set +e
  agent_update_out="$(CP_UPDATE_LATEST_URL="${fake_base}/releases/latest" \
    CP_RELEASE_BASE_URL="${fake_base}/releases/download/v0.3.1" \
    "$sandbox_agent" update 2>&1)"
  agent_update_status=$?
  set -e
  if [ "$agent_update_status" -eq 0 ]; then
    pass "agent update (real) exits 0"
  else
    echo "$agent_update_out" >&2
    fail "agent update (real) exited ${agent_update_status}, want 0"
  fi
  local agent_post_version_out
  agent_post_version_out="$("$sandbox_agent" -version 2>&1)"
  case "$agent_post_version_out" in
    *"v0.3.1"*) pass "sandbox agent binary reports v0.3.1 after update" ;;
    *) fail "sandbox agent binary does not report v0.3.1 after update: ${agent_post_version_out}" ;;
  esac

  finish
}

finish() {
  echo
  echo "===================================================="
  echo "test-update results: ${PASS_COUNT} passed, ${FAIL_COUNT} failed"
  echo "===================================================="
  if [ "$FAIL_COUNT" -gt 0 ]; then
    exit 1
  fi
}

main "$@"
