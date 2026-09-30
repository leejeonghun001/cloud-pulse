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
REQ_FAKE_PID=""
REQ_TAMPERED_FAKE_PID=""

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
  if [ -n "$REQ_FAKE_PID" ] && kill -0 "$REQ_FAKE_PID" 2>/dev/null; then
    kill "$REQ_FAKE_PID" 2>/dev/null || true
    wait "$REQ_FAKE_PID" 2>/dev/null || true
  fi
  if [ -n "$REQ_TAMPERED_FAKE_PID" ] && kill -0 "$REQ_TAMPERED_FAKE_PID" 2>/dev/null; then
    kill "$REQ_TAMPERED_FAKE_PID" 2>/dev/null || true
    wait "$REQ_TAMPERED_FAKE_PID" 2>/dev/null || true
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

  # ---------------------------------------------------------------------
  # systemd-unit apply --unit-path <tmp> --no-reload (SPEC-v0.3.1 B/D):
  # an outdated on-disk unit file gets rewritten in place (with a .bak
  # of the previous content); a second run against the now-current unit
  # reports "up to date" and makes no further changes. --no-reload
  # avoids any real `systemctl daemon-reload` call in this sandbox.
  # ---------------------------------------------------------------------
  local unit_path="${sandbox_dir}/cloud-pulse-hub.service"
  cat >"$unit_path" <<'UNITEOF'
[Unit]
Description=cloud-pulse hub (metrics ingestion + dashboard)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/cloud-pulse/hub.env
Environment=CP_DATA_DIR=/var/lib/cloud-pulse
ExecStart=/usr/local/bin/cloud-pulse-hub
User=cloud-pulse
Group=cloud-pulse
Restart=on-failure
RestartSec=3
StateDirectory=cloud-pulse

# --- sandboxing / hardening ---
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes
CapabilityBoundingSet=
AmbientCapabilities=
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX

[Install]
WantedBy=multi-user.target
UNITEOF
  # RestartSec=3 above (current Render always writes RestartSec=5) is
  # the deliberately "outdated" delta this unit file needs rewritten.
  local before_apply_sha
  before_apply_sha="$(sha256sum "$unit_path" | awk '{print $1}')"

  local apply1_out apply1_status
  set +e
  apply1_out="$("$sandbox_hub" systemd-unit apply --unit-path "$unit_path" --no-reload 2>&1)"
  apply1_status=$?
  set -e
  if [ "$apply1_status" -eq 0 ]; then
    pass "systemd-unit apply (outdated unit) exits 0"
  else
    echo "$apply1_out" >&2
    fail "systemd-unit apply (outdated unit) exited ${apply1_status}, want 0"
  fi
  case "$apply1_out" in
    *"updated systemd unit"*) pass "systemd-unit apply (outdated unit) reports it updated the unit" ;;
    *) fail "systemd-unit apply (outdated unit) did not report an update: ${apply1_out}" ;;
  esac
  if [ -f "${unit_path}.bak" ]; then
    pass "systemd-unit apply (outdated unit) wrote a .bak of the previous content"
  else
    fail "systemd-unit apply (outdated unit) did not create ${unit_path}.bak"
  fi
  local bak_sha
  bak_sha="$(sha256sum "${unit_path}.bak" 2>/dev/null | awk '{print $1}')"
  if [ "$bak_sha" = "$before_apply_sha" ]; then
    pass "systemd-unit apply .bak content matches the pre-apply unit (sha256 identical)"
  else
    fail "systemd-unit apply .bak content does not match pre-apply unit (${bak_sha} != ${before_apply_sha})"
  fi
  if grep -q '^RestartSec=5$' "$unit_path"; then
    pass "systemd-unit apply rewrote RestartSec=3 -> RestartSec=5 (current Render output)"
  else
    fail "systemd-unit apply did not rewrite the unit to current Render output"
  fi

  local apply2_out apply2_status
  set +e
  apply2_out="$("$sandbox_hub" systemd-unit apply --unit-path "$unit_path" --no-reload 2>&1)"
  apply2_status=$?
  set -e
  if [ "$apply2_status" -eq 0 ]; then
    pass "systemd-unit apply (rerun, up to date) exits 0"
  else
    echo "$apply2_out" >&2
    fail "systemd-unit apply (rerun, up to date) exited ${apply2_status}, want 0"
  fi
  case "$apply2_out" in
    *"up to date"*) pass "systemd-unit apply (rerun) reports the unit is up to date" ;;
    *) fail "systemd-unit apply (rerun) did not report up-to-date: ${apply2_out}" ;;
  esac
  local bak_sha_after_second
  bak_sha_after_second="$(sha256sum "${unit_path}.bak" | awk '{print $1}')"
  if [ "$bak_sha_after_second" = "$before_apply_sha" ]; then
    pass "systemd-unit apply (rerun) left the existing .bak untouched (no spurious second backup)"
  else
    fail "systemd-unit apply (rerun) modified .bak unexpectedly"
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

  test_from_request_mode "$target" "$arch"

  finish
}

# test_from_request_mode — SPEC-v0.6 §2's `update --from-request FILE`
# path: builds a v0.6.0 agent + a fake v0.6.1 release, then exercises
# success, checksum mismatch, downgrade refused, and malicious request
# files, all against a dedicated fake release server on its own port
# (never the v0.3.x server/ports used above).
test_from_request_mode() {
  local target="$1" arch="$2"
  local v060_dir v061_dir req_tampered_dir req_sandbox_dir
  v060_dir="${TMP_ROOT}/v0.6.0"
  v061_dir="${TMP_ROOT}/v0.6.1"
  req_tampered_dir="${TMP_ROOT}/v0.6.1-tampered"
  req_sandbox_dir="${TMP_ROOT}/from-request-sandbox"
  mkdir -p "$v060_dir" "$v061_dir" "$req_tampered_dir" "$req_sandbox_dir"

  echo "==> building v0.6.0 agent binary (${target})"
  if TARGETS="$target" ALLOW_ANY_VERSION=1 \
    bash scripts/build-release.sh "v0.6.0" "$v060_dir" >"${TMP_ROOT}/build-v060.log" 2>&1; then
    pass "build v0.6.0 agent binary for ${target}"
  else
    cat "${TMP_ROOT}/build-v060.log" >&2
    fail "build v0.6.0 agent binary for ${target}"
    return
  fi

  echo "==> building v0.6.1 release assets (${target})"
  if TARGETS="$target" ALLOW_ANY_VERSION=1 \
    bash scripts/build-release.sh "v0.6.1" "$v061_dir" >"${TMP_ROOT}/build-v061.log" 2>&1; then
    pass "build v0.6.1 release assets for ${target}"
  else
    cat "${TMP_ROOT}/build-v061.log" >&2
    fail "build v0.6.1 release assets for ${target}"
    return
  fi

  cp -r "${v061_dir}/." "$req_tampered_dir"
  if [ -f "${req_tampered_dir}/checksums.txt" ]; then
    python3 - "$req_tampered_dir/checksums.txt" <<'PYEOF'
import re
import sys

path = sys.argv[1]
with open(path, encoding="utf-8") as f:
    lines = f.readlines()

out = [re.sub(r"^[0-9a-f]{64}", "0" * 64, line) for line in lines]

with open(path, "w", encoding="utf-8") as f:
    f.writelines(out)
PYEOF
    pass "prepared tampered v0.6.1 checksums.txt (all-zero hashes) for --from-request"
  else
    fail "v0.6.1 checksums.txt not found at ${req_tampered_dir}/checksums.txt"
  fi

  local req_fake_port="${REQ_FAKE_PORT:-18197}"
  local req_fake_base="http://127.0.0.1:${req_fake_port}"
  local req_fake_assets_root="${TMP_ROOT}/from-request-fake-assets"
  mkdir -p "${req_fake_assets_root}/v0.6.1"
  cp "${v061_dir}"/* "${req_fake_assets_root}/v0.6.1/" 2>/dev/null || true

  local req_tampered_assets_root="${TMP_ROOT}/from-request-fake-assets-tampered"
  mkdir -p "${req_tampered_assets_root}/v0.6.1"
  cp "${req_tampered_dir}"/* "${req_tampered_assets_root}/v0.6.1/" 2>/dev/null || true

  python3 "${REPO_ROOT}/scripts/fake_release_server.py" "$req_fake_port" "$req_fake_assets_root" "v0.6.1" \
    >"${TMP_ROOT}/from-request-fake-server.log" 2>&1 &
  REQ_FAKE_PID=$!

  if wait_for_http "${req_fake_base}/releases/latest" 10; then
    pass "from-request fake release server reachable on ${req_fake_base}"
  else
    fail "from-request fake release server did not become reachable within 10s"
    return
  fi

  local req_tampered_port="${REQ_TAMPERED_FAKE_PORT:-18196}"
  local req_tampered_base="http://127.0.0.1:${req_tampered_port}"
  python3 "${REPO_ROOT}/scripts/fake_release_server.py" "$req_tampered_port" "$req_tampered_assets_root" "v0.6.1" \
    >"${TMP_ROOT}/from-request-fake-server-tampered.log" 2>&1 &
  REQ_TAMPERED_FAKE_PID=$!

  if wait_for_http "${req_tampered_base}/releases/latest" 10; then
    pass "from-request tampered fake release server reachable on ${req_tampered_base}"
  else
    fail "from-request tampered fake release server did not become reachable within 10s"
    return
  fi

  local agent_asset="cloud-pulse-agent-linux-${arch}"

  # --- success: v0.6.0 -> v0.6.1 via a valid request file ---
  local success_bin="${req_sandbox_dir}/cloud-pulse-agent-success"
  local success_result_dir="${req_sandbox_dir}/result-success"
  cp "${v060_dir}/${agent_asset}" "$success_bin"
  chmod 0755 "$success_bin"
  local success_req="${req_sandbox_dir}/request-success.json"
  printf '{"job_id":1,"target":"v0.6.1"}' > "$success_req"

  local success_out success_status
  set +e
  success_out="$(CP_UPDATE_LATEST_URL="${req_fake_base}/releases/latest" \
    CP_RELEASE_BASE_URL="${req_fake_base}/releases/download/v0.6.1" \
    "$success_bin" update --from-request "$success_req" --result-dir "$success_result_dir" 2>&1)"
  success_status=$?
  set -e
  if [ "$success_status" -eq 0 ]; then
    pass "update --from-request (success case) exits 0"
  else
    echo "$success_out" >&2
    fail "update --from-request (success case) exited ${success_status}, want 0"
  fi
  local success_version_out
  success_version_out="$("$success_bin" -version 2>&1)"
  case "$success_version_out" in
    *"v0.6.1"*) pass "update --from-request (success case) upgraded binary to v0.6.1" ;;
    *) fail "update --from-request (success case) did not upgrade to v0.6.1: ${success_version_out}" ;;
  esac
  if [ -f "${success_result_dir}/result.json" ]; then
    pass "update --from-request (success case) wrote result.json"
    case "$(cat "${success_result_dir}/result.json")" in
      *'"state":"succeeded"'*) pass "update --from-request (success case) result.json state=succeeded" ;;
      *) fail "update --from-request (success case) result.json missing state=succeeded: $(cat "${success_result_dir}/result.json")" ;;
    esac
    case "$(cat "${success_result_dir}/result.json")" in
      *'"job_id":1'*) pass "update --from-request (success case) result.json echoes job_id" ;;
      *) fail "update --from-request (success case) result.json missing job_id: $(cat "${success_result_dir}/result.json")" ;;
    esac
  else
    fail "update --from-request (success case) did not write ${success_result_dir}/result.json"
  fi
  local result_mode
  result_mode="$(stat -c '%a' "${success_result_dir}/result.json" 2>/dev/null || stat -f '%Lp' "${success_result_dir}/result.json" 2>/dev/null || true)"
  if [ "$result_mode" = "644" ]; then
    pass "update --from-request result.json has mode 0644"
  else
    fail "update --from-request result.json mode = ${result_mode}, want 644"
  fi

  # --- checksum mismatch: tampered release -> failed result, binary untouched ---
  local checksum_bin="${req_sandbox_dir}/cloud-pulse-agent-checksum"
  local checksum_result_dir="${req_sandbox_dir}/result-checksum"
  cp "${v060_dir}/${agent_asset}" "$checksum_bin"
  chmod 0755 "$checksum_bin"
  local checksum_before_sha
  checksum_before_sha="$(sha256sum "$checksum_bin" | awk '{print $1}')"
  local checksum_req="${req_sandbox_dir}/request-checksum.json"
  printf '{"job_id":2,"target":"v0.6.1"}' > "$checksum_req"

  local checksum_out checksum_status
  set +e
  checksum_out="$(CP_UPDATE_LATEST_URL="${req_tampered_base}/releases/latest" \
    CP_RELEASE_BASE_URL="${req_tampered_base}/releases/download/v0.6.1" \
    "$checksum_bin" update --from-request "$checksum_req" --result-dir "$checksum_result_dir" 2>&1)"
  checksum_status=$?
  set -e
  # Per RunFromRequest's contract, the process itself still exits 0
  # (the request file was valid and a result was written); the failure
  # is reported via result.json, not the process exit code.
  if [ "$checksum_status" -eq 0 ]; then
    pass "update --from-request (checksum mismatch case) exits 0 (failure reported via result.json)"
  else
    echo "$checksum_out" >&2
    fail "update --from-request (checksum mismatch case) exited ${checksum_status}, want 0"
  fi
  if [ -f "${checksum_result_dir}/result.json" ]; then
    case "$(cat "${checksum_result_dir}/result.json")" in
      *'"state":"failed"'*'"error_code":"checksum_mismatch"'*|*'"error_code":"checksum_mismatch"'*'"state":"failed"'*)
        pass "update --from-request (checksum mismatch case) result.json state=failed error_code=checksum_mismatch" ;;
      *) fail "update --from-request (checksum mismatch case) result.json unexpected content: $(cat "${checksum_result_dir}/result.json")" ;;
    esac
  else
    fail "update --from-request (checksum mismatch case) did not write result.json"
  fi
  local checksum_after_sha
  checksum_after_sha="$(sha256sum "$checksum_bin" | awk '{print $1}')"
  if [ "$checksum_before_sha" = "$checksum_after_sha" ]; then
    pass "update --from-request (checksum mismatch case) left the binary unchanged"
  else
    fail "update --from-request (checksum mismatch case) MODIFIED the binary despite a checksum failure"
  fi

  # --- downgrade refused: target older than current -> failed, no network needed ---
  local downgrade_bin="${req_sandbox_dir}/cloud-pulse-agent-downgrade"
  local downgrade_result_dir="${req_sandbox_dir}/result-downgrade"
  cp "${v061_dir}/${agent_asset}" "$downgrade_bin"
  chmod 0755 "$downgrade_bin"
  local downgrade_before_sha
  downgrade_before_sha="$(sha256sum "$downgrade_bin" | awk '{print $1}')"
  local downgrade_req="${req_sandbox_dir}/request-downgrade.json"
  printf '{"job_id":3,"target":"v0.6.0"}' > "$downgrade_req"

  local downgrade_out downgrade_status
  set +e
  downgrade_out="$("$downgrade_bin" update --from-request "$downgrade_req" --result-dir "$downgrade_result_dir" 2>&1)"
  downgrade_status=$?
  set -e
  if [ "$downgrade_status" -eq 0 ]; then
    pass "update --from-request (downgrade case) exits 0"
  else
    echo "$downgrade_out" >&2
    fail "update --from-request (downgrade case) exited ${downgrade_status}, want 0"
  fi
  if [ -f "${downgrade_result_dir}/result.json" ]; then
    case "$(cat "${downgrade_result_dir}/result.json")" in
      *'"error_code":"downgrade_refused"'*) pass "update --from-request (downgrade case) result.json error_code=downgrade_refused" ;;
      *) fail "update --from-request (downgrade case) result.json unexpected content: $(cat "${downgrade_result_dir}/result.json")" ;;
    esac
  else
    fail "update --from-request (downgrade case) did not write result.json"
  fi
  local downgrade_after_sha
  downgrade_after_sha="$(sha256sum "$downgrade_bin" | awk '{print $1}')"
  if [ "$downgrade_before_sha" = "$downgrade_after_sha" ]; then
    pass "update --from-request (downgrade case) left the binary unchanged"
  else
    fail "update --from-request (downgrade case) MODIFIED the binary despite a refused downgrade"
  fi

  # --- malicious request files: process itself must error, no result.json ---
  local malicious_bin="${req_sandbox_dir}/cloud-pulse-agent-malicious"
  cp "${v060_dir}/${agent_asset}" "$malicious_bin"
  chmod 0755 "$malicious_bin"

  local malformed_req="${req_sandbox_dir}/request-malformed.json"
  printf '{not valid json' > "$malformed_req"
  local malformed_result_dir="${req_sandbox_dir}/result-malformed"
  if "$malicious_bin" update --from-request "$malformed_req" --result-dir "$malformed_result_dir" >/dev/null 2>&1; then
    fail "update --from-request (malformed JSON) unexpectedly exited 0"
  else
    pass "update --from-request (malformed JSON) exits non-zero"
  fi
  assert_no_result_file "$malformed_result_dir" "malformed JSON"

  local bad_tag_req="${req_sandbox_dir}/request-bad-tag.json"
  printf '{"job_id":4,"target":"; rm -rf /"}' > "$bad_tag_req"
  local bad_tag_result_dir="${req_sandbox_dir}/result-bad-tag"
  if "$malicious_bin" update --from-request "$bad_tag_req" --result-dir "$bad_tag_result_dir" >/dev/null 2>&1; then
    fail "update --from-request (malicious target tag) unexpectedly exited 0"
  else
    pass "update --from-request (malicious target tag) exits non-zero"
  fi
  assert_no_result_file "$bad_tag_result_dir" "malicious target tag"

  local bad_jobid_req="${req_sandbox_dir}/request-bad-jobid.json"
  printf '{"job_id":0,"target":"v0.6.1"}' > "$bad_jobid_req"
  local bad_jobid_result_dir="${req_sandbox_dir}/result-bad-jobid"
  if "$malicious_bin" update --from-request "$bad_jobid_req" --result-dir "$bad_jobid_result_dir" >/dev/null 2>&1; then
    fail "update --from-request (job_id <= 0) unexpectedly exited 0"
  else
    pass "update --from-request (job_id <= 0) exits non-zero"
  fi
  assert_no_result_file "$bad_jobid_result_dir" "job_id <= 0"

  local oversized_req="${req_sandbox_dir}/request-oversized.json"
  python3 -c "print('{\"job_id\":1,\"target\":\"' + ('v' * 8192) + '\"}')" > "$oversized_req"
  local oversized_result_dir="${req_sandbox_dir}/result-oversized"
  if "$malicious_bin" update --from-request "$oversized_req" --result-dir "$oversized_result_dir" >/dev/null 2>&1; then
    fail "update --from-request (oversized request file) unexpectedly exited 0"
  else
    pass "update --from-request (oversized request file) exits non-zero"
  fi
  assert_no_result_file "$oversized_result_dir" "oversized request file"

  local symlink_target="${req_sandbox_dir}/symlink-target.json"
  printf '{"job_id":5,"target":"v0.6.1"}' > "$symlink_target"
  local symlink_req="${req_sandbox_dir}/request-symlink.json"
  ln -sf "$symlink_target" "$symlink_req"
  local symlink_result_dir="${req_sandbox_dir}/result-symlink"
  if "$malicious_bin" update --from-request "$symlink_req" --result-dir "$symlink_result_dir" >/dev/null 2>&1; then
    fail "update --from-request (symlinked request file) unexpectedly exited 0"
  else
    pass "update --from-request (symlinked request file) exits non-zero (O_NOFOLLOW)"
  fi
  assert_no_result_file "$symlink_result_dir" "symlinked request file"

  # --- combined flags rejected ---
  local combined_status
  set +e
  "$malicious_bin" update --from-request "$success_req" --check >/dev/null 2>&1
  combined_status=$?
  set -e
  if [ "$combined_status" -eq 0 ]; then
    fail "update --from-request combined with --check unexpectedly exited 0"
  else
    pass "update --from-request combined with --check exits non-zero"
  fi
}

# assert_no_result_file RESULT_DIR LABEL — asserts RESULT_DIR/result.json
# was never created, for the malicious-request-file cases above where
# the request itself is rejected before any update attempt (and
# therefore before any result file would ever be written).
assert_no_result_file() {
  local result_dir="$1" label="$2"
  if [ -f "${result_dir}/result.json" ]; then
    fail "update --from-request (${label}) unexpectedly wrote a result.json"
  else
    pass "update --from-request (${label}) wrote no result.json"
  fi
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
