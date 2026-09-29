#!/usr/bin/env bash
#
# scripts/test-install.sh — automated test for install-hub.sh /
# install-agent.sh. Builds real linux/<host-arch> release assets,
# serves them over local HTTP, and exercises both installers in
# sandbox mode (CP_INSTALL_ROOT), asserting on the resulting files,
# systemd units, and printed output. Runs as non-root on Linux
# (locally and in CI, where systemd-analyze is available).
#
# Usage:
#   scripts/test-install.sh
#
# All logic is invoked from main() at the bottom of the file.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# ---------------------------------------------------------------------------
# Globals
# ---------------------------------------------------------------------------

PASS_COUNT=0
FAIL_COUNT=0

TMP_ROOT=""
DIST_DIR=""
TAMPERED_DIR=""
SANDBOX_HUB=""
SANDBOX_AGENT=""
SERVER_PID=""
SERVER_PORT=""
TAMPERED_SERVER_PID=""
TAMPERED_SERVER_PORT=""

HOST_ARCH=""

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

pass() {
  echo "PASS: $*"
  PASS_COUNT=$((PASS_COUNT + 1))
}

fail() {
  echo "FAIL: $*" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
}

# assert_true DESCRIPTION -- CMD...   runs CMD, pass/fail based on exit code
assert_exit0() {
  local desc="$1"
  shift
  local out_file
  out_file="$(mktemp "${TMP_ROOT}/assert_out.XXXXXX")"
  if "$@" >"$out_file" 2>&1; then
    pass "$desc"
  else
    fail "$desc (command failed: $*)"
    sed 's/^/    /' "$out_file" >&2 || true
  fi
  rm -f "$out_file"
}

assert_nonzero_exit() {
  local desc="$1"
  shift
  local out_file
  out_file="$(mktemp "${TMP_ROOT}/assert_out.XXXXXX")"
  if "$@" >"$out_file" 2>&1; then
    fail "$desc (command unexpectedly succeeded: $*)"
    sed 's/^/    /' "$out_file" >&2 || true
  else
    pass "$desc"
  fi
  rm -f "$out_file"
}

assert_file_exists() {
  local desc="$1" path="$2"
  if [ -e "$path" ]; then
    pass "$desc"
  else
    fail "$desc (missing: $path)"
  fi
}

assert_file_absent() {
  local desc="$1" path="$2"
  if [ ! -e "$path" ]; then
    pass "$desc"
  else
    fail "$desc (should be absent: $path)"
  fi
}

assert_executable() {
  local desc="$1" path="$2"
  if [ -x "$path" ]; then
    pass "$desc"
  else
    fail "$desc (not executable: $path)"
  fi
}

assert_perm() {
  local desc="$1" path="$2" want="$3"
  local got
  got="$(stat -c '%a' "$path" 2>/dev/null || stat -f '%Lp' "$path" 2>/dev/null || echo "?")"
  if [ "$got" = "$want" ]; then
    pass "$desc"
  else
    fail "$desc (perm=$got, want=$want, path=$path)"
  fi
}

assert_contains() {
  local desc="$1" haystack="$2" needle="$3"
  if [[ "$haystack" == *"$needle"* ]]; then
    pass "$desc"
  else
    fail "$desc (expected to contain '$needle')"
  fi
}

assert_not_contains() {
  local desc="$1" haystack="$2" needle="$3"
  if [[ "$haystack" != *"$needle"* ]]; then
    pass "$desc"
  else
    fail "$desc (expected NOT to contain '$needle')"
  fi
}

assert_file_contains() {
  local desc="$1" path="$2" pattern="$3"
  if grep -q -- "$pattern" "$path" 2>/dev/null; then
    pass "$desc"
  else
    fail "$desc (pattern '$pattern' not found in $path)"
  fi
}

find_free_port() {
  python3 - <<'PY'
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
}

wait_for_http() {
  local url="$1" tries=30
  while [ "$tries" -gt 0 ]; do
    if curl -fsS -o /dev/null "$url" 2>/dev/null; then
      return 0
    fi
    tries=$((tries - 1))
    sleep 0.2
  done
  return 1
}

host_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    armv7l|armv7*) echo "armv7" ;;
    *) echo "unsupported" ;;
  esac
}

cleanup() {
  local status=$?
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  if [ -n "$TAMPERED_SERVER_PID" ] && kill -0 "$TAMPERED_SERVER_PID" 2>/dev/null; then
    kill "$TAMPERED_SERVER_PID" 2>/dev/null || true
    wait "$TAMPERED_SERVER_PID" 2>/dev/null || true
  fi
  if [ -n "$TMP_ROOT" ]; then
    rm -rf "$TMP_ROOT"
  fi
  exit "$status"
}
trap cleanup EXIT INT TERM

# ---------------------------------------------------------------------------
# Setup: build release assets for host arch, serve over HTTP
# ---------------------------------------------------------------------------

build_release_assets() {
  HOST_ARCH="$(host_arch)"
  if [ "$HOST_ARCH" = "unsupported" ]; then
    echo "test-install: host arch $(uname -m) is not a supported release target" >&2
    exit 1
  fi

  echo "==> building release assets for linux/${HOST_ARCH}"
  local go_arch="$HOST_ARCH"
  if [ "$HOST_ARCH" = "armv7" ]; then
    go_arch="arm"
  fi

  TARGETS="linux/${go_arch}" ALLOW_ANY_VERSION=1 \
    timeout 300 scripts/build-release.sh v0.0.0-test "$DIST_DIR" >/dev/null

  assert_file_exists "build-release produced hub asset" "${DIST_DIR}/cloud-pulse-hub-linux-${HOST_ARCH}"
  assert_file_exists "build-release produced agent asset" "${DIST_DIR}/cloud-pulse-agent-linux-${HOST_ARCH}"
  assert_file_exists "build-release produced checksums.txt" "${DIST_DIR}/checksums.txt"
}

start_asset_server() {
  SERVER_PORT="$(find_free_port)"
  (
    cd "$DIST_DIR"
    exec timeout 280 python3 -m http.server --bind 127.0.0.1 "$SERVER_PORT"
  ) >"${TMP_ROOT}/server.log" 2>&1 &
  SERVER_PID=$!

  if ! wait_for_http "http://127.0.0.1:${SERVER_PORT}/checksums.txt"; then
    fail "asset HTTP server came up"
    cat "${TMP_ROOT}/server.log" >&2 || true
    exit 1
  fi
  pass "asset HTTP server serving ${DIST_DIR}"
}

start_tampered_server() {
  TAMPERED_DIR="${TMP_ROOT}/tampered"
  mkdir -p "$TAMPERED_DIR"
  cp "${DIST_DIR}/cloud-pulse-agent-linux-${HOST_ARCH}" "$TAMPERED_DIR/"
  cp "${DIST_DIR}/cloud-pulse-hub-linux-${HOST_ARCH}" "$TAMPERED_DIR/"
  # Tamper: checksums.txt lists a bogus hash for the agent binary.
  {
    echo "0000000000000000000000000000000000000000000000000000000000000  cloud-pulse-agent-linux-${HOST_ARCH}"
    grep "cloud-pulse-hub-linux-${HOST_ARCH}" "${DIST_DIR}/checksums.txt"
  } > "${TAMPERED_DIR}/checksums.txt"

  TAMPERED_SERVER_PORT="$(find_free_port)"
  (
    cd "$TAMPERED_DIR"
    exec timeout 280 python3 -m http.server --bind 127.0.0.1 "$TAMPERED_SERVER_PORT"
  ) >"${TMP_ROOT}/tampered-server.log" 2>&1 &
  TAMPERED_SERVER_PID=$!

  if ! wait_for_http "http://127.0.0.1:${TAMPERED_SERVER_PORT}/checksums.txt"; then
    fail "tampered asset HTTP server came up"
    cat "${TMP_ROOT}/tampered-server.log" >&2 || true
    exit 1
  fi
  pass "tampered asset HTTP server serving ${TAMPERED_DIR}"
}

# ---------------------------------------------------------------------------
# Hub install assertions
# ---------------------------------------------------------------------------

test_hub_install() {
  echo "==> testing install-hub.sh (sandbox install)"
  local out
  out="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$SANDBOX_HUB" \
    timeout 60 bash scripts/install-hub.sh --listen :18090 2>&1)" || {
    fail "install-hub.sh sandbox install exited 0"
    echo "$out" >&2
    return
  }
  pass "install-hub.sh sandbox install exited 0"

  local bin="${SANDBOX_HUB}/usr/local/bin/cloud-pulse-hub"
  local env_file="${SANDBOX_HUB}/etc/cloud-pulse/hub.env"
  local unit_file="${SANDBOX_HUB}/etc/systemd/system/cloud-pulse-hub.service"

  assert_executable "hub binary installed and executable" "$bin"
  assert_exit0 "hub binary -version works" timeout 10 "$bin" -version

  assert_file_exists "hub.env created" "$env_file"
  assert_perm "hub.env mode 0640" "$env_file" "640"
  assert_file_contains "hub.env contains CP_DATA_DIR" "$env_file" "CP_DATA_DIR="

  local token
  token="$(grep '^CP_AGENT_TOKEN=' "$env_file" | cut -d= -f2)"
  if [ "${#token}" -eq 64 ] && [[ "$token" =~ ^[0-9a-f]{64}$ ]]; then
    pass "hub.env CP_AGENT_TOKEN is 64 hex chars"
  else
    fail "hub.env CP_AGENT_TOKEN is 64 hex chars (got: '${token}', len=${#token})"
  fi

  if [ -f "$unit_file" ] && command -v systemd-analyze >/dev/null 2>&1; then
    assert_exit0 "hub unit passes systemd-analyze verify" \
      timeout 30 bash -c "sed 's#^ExecStart=.*#ExecStart=${bin}#' '$unit_file' > '${TMP_ROOT}/verify-hub.service' && systemd-analyze verify '${TMP_ROOT}/verify-hub.service'"
  else
    echo "SKIP: systemd-analyze not available or unit missing; skipping hub unit verify"
  fi

  assert_contains "printed agent one-liner contains the token" "$out" "$token"
  assert_contains "first install prints future-updates hint" "$out" "Future updates: sudo cloud-pulse-hub update"
  assert_not_contains "first install does not print 'Upgraded' (no previous binary)" "$out" "Upgraded"
  HUB_TOKEN="$token"
}
HUB_TOKEN=""

test_hub_explicit_listen_host_in_one_liner() {
  echo "==> testing install-hub.sh prints explicit --listen host in agent one-liner"
  local sandbox="${TMP_ROOT}/sandbox-hub-listen" out
  out="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-hub.sh --listen 127.0.0.1:18091 2>&1)" || {
    fail "install-hub.sh --listen 127.0.0.1:18091 exited 0"
    echo "$out" >&2
    return
  }
  assert_contains "one-liner uses explicit bind address" "$out" "--hub-url http://127.0.0.1:18091 "
}

test_hub_upgrade_keeps_token() {
  echo "==> testing install-hub.sh re-run (upgrade) keeps existing token"
  local out
  out="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$SANDBOX_HUB" \
    timeout 60 bash scripts/install-hub.sh --listen :18090 2>&1)" || {
    fail "install-hub.sh upgrade re-run exited 0"
    echo "$out" >&2
    return
  }
  local env_file="${SANDBOX_HUB}/etc/cloud-pulse/hub.env"
  local token
  token="$(grep '^CP_AGENT_TOKEN=' "$env_file" | cut -d= -f2)"
  if [ "$token" = "$HUB_TOKEN" ]; then
    pass "hub upgrade re-run preserves CP_AGENT_TOKEN"
  else
    fail "hub upgrade re-run preserves CP_AGENT_TOKEN (was ${HUB_TOKEN}, now ${token})"
  fi
  assert_contains "hub upgrade re-run prints 'Upgraded'" "$out" "Upgraded"
  assert_contains "hub upgrade re-run prints future-updates hint" "$out" "Future updates: sudo cloud-pulse-hub update"
  assert_contains "hub upgrade from legacy (v0.0.0-test) prints built-in-updater line" "$out" \
    "This install now includes the built-in updater."
}

test_hub_dry_run() {
  echo "==> testing install-hub.sh --dry-run changes nothing"
  local sandbox="${TMP_ROOT}/sandbox-hub-dryrun"
  mkdir -p "$sandbox"
  CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 30 bash scripts/install-hub.sh --dry-run >/dev/null 2>&1
  local bin="${sandbox}/usr/local/bin/cloud-pulse-hub"
  local env_file="${sandbox}/etc/cloud-pulse/hub.env"
  assert_file_absent "hub --dry-run does not install binary" "$bin"
  assert_file_absent "hub --dry-run does not write hub.env" "$env_file"
}

test_hub_checksum_mismatch() {
  echo "==> testing install-hub.sh checksum mismatch aborts and does not replace binary"
  local sandbox="${TMP_ROOT}/sandbox-hub-tamper"
  mkdir -p "$sandbox"

  # Tamper the hub asset's own checksum entry for this test (the shared
  # tampered server only mismatches the agent asset).
  local hub_tamper_dir="${TMP_ROOT}/tampered-hub"
  mkdir -p "$hub_tamper_dir"
  cp "${DIST_DIR}/cloud-pulse-hub-linux-${HOST_ARCH}" "$hub_tamper_dir/"
  cp "${DIST_DIR}/cloud-pulse-agent-linux-${HOST_ARCH}" "$hub_tamper_dir/"
  {
    echo "0000000000000000000000000000000000000000000000000000000000000  cloud-pulse-hub-linux-${HOST_ARCH}"
    grep "cloud-pulse-agent-linux-${HOST_ARCH}" "${DIST_DIR}/checksums.txt"
  } > "${hub_tamper_dir}/checksums.txt"

  local port
  port="$(find_free_port)"
  (
    cd "$hub_tamper_dir"
    exec timeout 60 python3 -m http.server --bind 127.0.0.1 "$port"
  ) >"${TMP_ROOT}/hub-tamper-server.log" 2>&1 &
  local pid=$!

  if ! wait_for_http "http://127.0.0.1:${port}/checksums.txt"; then
    fail "hub tampered asset HTTP server came up"
    kill "$pid" 2>/dev/null || true
    return
  fi

  assert_nonzero_exit "hub install with tampered checksums.txt fails" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${port}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-hub.sh

  assert_file_absent "hub binary NOT installed after checksum mismatch" \
    "${sandbox}/usr/local/bin/cloud-pulse-hub"

  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# Agent install assertions
# ---------------------------------------------------------------------------

test_agent_missing_required_flags() {
  echo "==> testing install-agent.sh missing --hub-url on first install errors"
  local sandbox="${TMP_ROOT}/sandbox-agent-missing"
  mkdir -p "$sandbox"
  assert_nonzero_exit "agent install without --hub-url/--token fails" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-agent.sh
}

test_agent_install() {
  echo "==> testing install-agent.sh (sandbox install)"
  local out
  out="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$SANDBOX_AGENT" \
    timeout 60 bash scripts/install-agent.sh --hub-url "http://127.0.0.1:${SERVER_PORT}" --token "$HUB_TOKEN" 2>&1)" || {
    fail "install-agent.sh sandbox install exited 0"
    echo "$out" >&2
    return
  }
  pass "install-agent.sh sandbox install exited 0"

  local bin="${SANDBOX_AGENT}/usr/local/bin/cloud-pulse-agent"
  local env_file="${SANDBOX_AGENT}/etc/cloud-pulse/agent.env"
  local unit_file="${SANDBOX_AGENT}/etc/systemd/system/cloud-pulse-agent.service"

  assert_executable "agent binary installed and executable" "$bin"
  assert_exit0 "agent binary -version works" timeout 10 "$bin" -version

  assert_file_exists "agent.env created" "$env_file"
  assert_file_contains "agent.env contains CP_HUB_URL" "$env_file" "CP_HUB_URL=http://127.0.0.1:${SERVER_PORT}"
  assert_file_contains "agent.env contains CP_AGENT_TOKEN" "$env_file" "CP_AGENT_TOKEN=${HUB_TOKEN}"

  if [ -f "$unit_file" ] && command -v systemd-analyze >/dev/null 2>&1; then
    assert_exit0 "agent unit passes systemd-analyze verify" \
      timeout 30 bash -c "sed 's#^ExecStart=.*#ExecStart=${bin}#' '$unit_file' > '${TMP_ROOT}/verify-agent.service' && systemd-analyze verify '${TMP_ROOT}/verify-agent.service'"
  else
    echo "SKIP: systemd-analyze not available or unit missing; skipping agent unit verify"
  fi

  assert_contains "first install prints future-updates hint" "$out" "Future updates: sudo cloud-pulse-agent update"
  assert_not_contains "first install does not print 'Upgraded' (no previous binary)" "$out" "Upgraded"
}
test_agent_upgrade_keeps_values() {
  echo "==> testing install-agent.sh re-run (upgrade) keeps hub-url/token"
  local out
  out="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$SANDBOX_AGENT" \
    timeout 60 bash scripts/install-agent.sh 2>&1)" || {
    fail "install-agent.sh upgrade re-run (no flags) exited 0"
    echo "$out" >&2
    return
  }
  pass "install-agent.sh upgrade re-run (no flags) exited 0"

  local env_file="${SANDBOX_AGENT}/etc/cloud-pulse/agent.env"
  assert_file_contains "agent upgrade preserves CP_HUB_URL" "$env_file" "CP_HUB_URL=http://127.0.0.1:${SERVER_PORT}"
  assert_file_contains "agent upgrade preserves CP_AGENT_TOKEN" "$env_file" "CP_AGENT_TOKEN=${HUB_TOKEN}"
  assert_contains "agent upgrade re-run prints 'Upgraded'" "$out" "Upgraded"
  assert_contains "agent upgrade re-run prints future-updates hint" "$out" "Future updates: sudo cloud-pulse-agent update"
  assert_contains "agent upgrade from legacy (v0.0.0-test) prints built-in-updater line" "$out" \
    "This install now includes the built-in updater."
}

test_agent_unknown_arch() {
  echo "==> testing install-agent.sh unknown arch simulation (CP_TEST_UNAME_M=mips)"
  local sandbox="${TMP_ROOT}/sandbox-agent-mips"
  mkdir -p "$sandbox"
  assert_nonzero_exit "agent install with CP_TEST_UNAME_M=mips fails clearly" \
    env CP_TEST_UNAME_M=mips CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 30 bash scripts/install-agent.sh --hub-url "http://127.0.0.1:${SERVER_PORT}" --token "$HUB_TOKEN"
}

test_hub_rejects_malicious_values() {
  echo "==> testing install-hub.sh rejects shell-injection / newline-injection / whitespace flag values"
  local pwned_marker="${TMP_ROOT}/pwned-hub-marker"
  rm -f "$pwned_marker"

  local sandbox
  sandbox="${TMP_ROOT}/sandbox-hub-cmdsubst"
  mkdir -p "$sandbox"
  assert_nonzero_exit "hub install with \$(...) in --webhook-url is rejected" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 30 bash scripts/install-hub.sh --webhook-url "https://evil.example/\$(touch ${pwned_marker})"
  assert_file_absent "hub \$(...) webhook-url payload did not execute (no marker file)" "$pwned_marker"

  sandbox="${TMP_ROOT}/sandbox-hub-backtick"
  mkdir -p "$sandbox"
  assert_nonzero_exit "hub install with backtick command substitution in --ui-token is rejected" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 30 bash scripts/install-hub.sh --ui-token "x\`touch ${pwned_marker}\`"
  assert_file_absent "hub backtick ui-token payload did not execute (no marker file)" "$pwned_marker"

  sandbox="${TMP_ROOT}/sandbox-hub-newline"
  mkdir -p "$sandbox"
  local injected_value
  injected_value="$(printf 'x\nCP_UI_TOKEN=injected-by-attacker\n#')"
  assert_nonzero_exit "hub install with embedded newline in --webhook-url is rejected" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 30 bash scripts/install-hub.sh --webhook-url "$injected_value"
  assert_file_absent "hub newline-injection did not write hub.env" "${sandbox}/etc/cloud-pulse/hub.env"

  sandbox="${TMP_ROOT}/sandbox-hub-space"
  mkdir -p "$sandbox"
  assert_nonzero_exit "hub install with whitespace in --agent-token is rejected" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 30 bash scripts/install-hub.sh --agent-token "has space"

  # A literal backslash must be rejected too: systemd's real
  # EnvironmentFile= parser applies POSIX-style unquoted backslash-escape
  # rules on load, silently altering/dropping the backslash so the value
  # actually seen by the running service would differ from what was
  # written here with no visible error.
  sandbox="${TMP_ROOT}/sandbox-hub-backslash"
  mkdir -p "$sandbox"
  assert_nonzero_exit "hub install with backslash in --webhook-url is rejected" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 30 bash scripts/install-hub.sh --webhook-url 'https://evil.example/webhook\nid'
  assert_file_absent "hub backslash webhook-url did not write hub.env" "${sandbox}/etc/cloud-pulse/hub.env"
}

test_agent_rejects_malicious_values() {
  echo "==> testing install-agent.sh rejects newline-injection / whitespace flag values"
  local sandbox
  sandbox="${TMP_ROOT}/sandbox-agent-newline"
  mkdir -p "$sandbox"
  local injected_host_id
  injected_host_id="$(printf 'x\nCP_HUB_URL=http://evil.attacker/\n#')"
  assert_nonzero_exit "agent install with embedded newline in --host-id is rejected" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 30 bash scripts/install-agent.sh --hub-url "http://127.0.0.1:${SERVER_PORT}" --token "$HUB_TOKEN" \
      --host-id "$injected_host_id"
  assert_file_absent "agent newline-injection did not write agent.env" "${sandbox}/etc/cloud-pulse/agent.env"

  sandbox="${TMP_ROOT}/sandbox-agent-space"
  mkdir -p "$sandbox"
  assert_nonzero_exit "agent install with whitespace in --token is rejected" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 30 bash scripts/install-agent.sh --hub-url "http://127.0.0.1:${SERVER_PORT}" --token "has space"

  # A literal backslash must be rejected too: systemd's real
  # EnvironmentFile= parser applies POSIX-style unquoted backslash-escape
  # rules on load, silently altering/dropping the backslash so the value
  # actually seen by the running service would differ from what was
  # written here with no visible error.
  sandbox="${TMP_ROOT}/sandbox-agent-backslash"
  mkdir -p "$sandbox"
  assert_nonzero_exit "agent install with backslash in --host-id is rejected" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 30 bash scripts/install-agent.sh --hub-url "http://127.0.0.1:${SERVER_PORT}" --token "$HUB_TOKEN" \
      --host-id 'host\nname'
  assert_file_absent "agent backslash host-id did not write agent.env" "${sandbox}/etc/cloud-pulse/agent.env"
}

test_agent_checksum_mismatch() {
  echo "==> testing install-agent.sh checksum mismatch aborts and does not replace binary"
  local sandbox="${TMP_ROOT}/sandbox-agent-tamper"
  mkdir -p "$sandbox"

  assert_nonzero_exit "agent install with tampered checksums.txt fails" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${TAMPERED_SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-agent.sh --hub-url "http://127.0.0.1:${SERVER_PORT}" --token "$HUB_TOKEN"

  assert_file_absent "agent binary NOT installed after checksum mismatch" \
    "${sandbox}/usr/local/bin/cloud-pulse-agent"
}

test_agent_dry_run() {
  echo "==> testing install-agent.sh --dry-run changes nothing"
  local sandbox="${TMP_ROOT}/sandbox-agent-dryrun"
  mkdir -p "$sandbox"
  CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 30 bash scripts/install-agent.sh --dry-run --hub-url "http://127.0.0.1:${SERVER_PORT}" --token "$HUB_TOKEN" >/dev/null 2>&1
  assert_file_absent "agent --dry-run does not install binary" "${sandbox}/usr/local/bin/cloud-pulse-agent"
  assert_file_absent "agent --dry-run does not write agent.env" "${sandbox}/etc/cloud-pulse/agent.env"
}

test_agent_uninstall_and_purge() {
  echo "==> testing install-agent.sh --uninstall (keeps env) and --purge (removes env)"
  local bin="${SANDBOX_AGENT}/usr/local/bin/cloud-pulse-agent"
  local env_file="${SANDBOX_AGENT}/etc/cloud-pulse/agent.env"
  local unit_file="${SANDBOX_AGENT}/etc/systemd/system/cloud-pulse-agent.service"

  CP_INSTALL_ROOT="$SANDBOX_AGENT" timeout 30 bash scripts/install-agent.sh --uninstall >/dev/null 2>&1 || {
    fail "install-agent.sh --uninstall exited 0"
    return
  }
  pass "install-agent.sh --uninstall exited 0"
  assert_file_absent "agent --uninstall removes binary" "$bin"
  assert_file_absent "agent --uninstall removes unit" "$unit_file"
  assert_file_exists "agent --uninstall keeps agent.env" "$env_file"

  CP_INSTALL_ROOT="$SANDBOX_AGENT" timeout 30 bash scripts/install-agent.sh --uninstall --purge >/dev/null 2>&1 || {
    fail "install-agent.sh --uninstall --purge exited 0"
    return
  }
  pass "install-agent.sh --uninstall --purge exited 0"
  assert_file_absent "agent --purge removes agent.env" "$env_file"
}

test_hub_upgrade_from_legacy_stub() {
  echo "==> testing install-hub.sh upgrade over a simulated legacy (v0.2.0) binary"
  local sandbox="${TMP_ROOT}/sandbox-hub-legacy"
  local bin_dir="${sandbox}/usr/local/bin"
  mkdir -p "$bin_dir"

  # Simulate a pre-v0.3.0 installed binary: a tiny executable shell stub
  # at the exact path install-hub.sh will probe with "-version" before
  # replacing it. Real legacy binaries print e.g. "v0.2.0 (abc1234,
  # 2024-06-01)" for -version; this stub reproduces just that first
  # token, which is all install_binary()'s probe_existing_version reads.
  cat > "${bin_dir}/cloud-pulse-hub" <<'STUB'
#!/usr/bin/env bash
if [ "$1" = "-version" ]; then
  echo "v0.2.0 (abc1234, 2024-06-01)"
  exit 0
fi
exit 1
STUB
  chmod 0755 "${bin_dir}/cloud-pulse-hub"

  local out
  out="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-hub.sh --listen :18092 2>&1)" || {
    fail "install-hub.sh upgrade over legacy stub exited 0"
    echo "$out" >&2
    return
  }
  pass "install-hub.sh upgrade over legacy stub exited 0"

  assert_contains "legacy-stub upgrade prints 'Upgraded v0.2.0 →'" "$out" "Upgraded v0.2.0 →"
  assert_contains "legacy-stub upgrade prints built-in-updater line" "$out" \
    "This install now includes the built-in updater."
  assert_contains "legacy-stub upgrade prints future-updates hint" "$out" "Future updates: sudo cloud-pulse-hub update"
}

test_agent_upgrade_from_legacy_stub() {
  echo "==> testing install-agent.sh upgrade over a simulated legacy (v0.2.0) binary"
  local sandbox="${TMP_ROOT}/sandbox-agent-legacy"
  local bin_dir="${sandbox}/usr/local/bin"
  mkdir -p "$bin_dir"

  cat > "${bin_dir}/cloud-pulse-agent" <<'STUB'
#!/usr/bin/env bash
if [ "$1" = "-version" ]; then
  echo "v0.2.0 (abc1234, 2024-06-01)"
  exit 0
fi
exit 1
STUB
  chmod 0755 "${bin_dir}/cloud-pulse-agent"

  local out
  out="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-agent.sh --hub-url "http://127.0.0.1:${SERVER_PORT}" --token "$HUB_TOKEN" 2>&1)" || {
    fail "install-agent.sh upgrade over legacy stub exited 0"
    echo "$out" >&2
    return
  }
  pass "install-agent.sh upgrade over legacy stub exited 0"

  assert_contains "legacy-stub upgrade prints 'Upgraded v0.2.0 →'" "$out" "Upgraded v0.2.0 →"
  assert_contains "legacy-stub upgrade prints built-in-updater line" "$out" \
    "This install now includes the built-in updater."
  assert_contains "legacy-stub upgrade prints future-updates hint" "$out" "Future updates: sudo cloud-pulse-agent update"
}

test_hub_dry_run_upgrade_hint() {
  echo "==> testing install-hub.sh --dry-run mentions upgrade/legacy hints over an existing legacy binary"
  local sandbox="${TMP_ROOT}/sandbox-hub-legacy-dryrun"
  local bin_dir="${sandbox}/usr/local/bin"
  mkdir -p "$bin_dir"
  cat > "${bin_dir}/cloud-pulse-hub" <<'STUB'
#!/usr/bin/env bash
if [ "$1" = "-version" ]; then
  echo "v0.2.0 (abc1234, 2024-06-01)"
  exit 0
fi
exit 1
STUB
  chmod 0755 "${bin_dir}/cloud-pulse-hub"

  local out
  out="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 30 bash scripts/install-hub.sh --dry-run 2>&1)" || {
    fail "install-hub.sh --dry-run over legacy binary exited 0"
    echo "$out" >&2
    return
  }
  pass "install-hub.sh --dry-run over legacy binary exited 0"
  assert_contains "dry-run mentions the built-in updater for a legacy previous install" "$out" \
    "This install now includes the built-in updater."
  assert_contains "dry-run mentions the future-updates hint" "$out" "Future updates: sudo cloud-pulse-hub update"
  assert_file_absent "dry-run over legacy binary does not replace the binary" "${bin_dir}/cloud-pulse-hub.new"
}

test_hub_uninstall_and_purge() {
  echo "==> testing install-hub.sh --uninstall (keeps env) and --purge (removes env)"
  local bin="${SANDBOX_HUB}/usr/local/bin/cloud-pulse-hub"
  local env_file="${SANDBOX_HUB}/etc/cloud-pulse/hub.env"
  local unit_file="${SANDBOX_HUB}/etc/systemd/system/cloud-pulse-hub.service"
  local data_dir="${SANDBOX_HUB}/var/lib/cloud-pulse"

  CP_INSTALL_ROOT="$SANDBOX_HUB" timeout 30 bash scripts/install-hub.sh --uninstall >/dev/null 2>&1 || {
    fail "install-hub.sh --uninstall exited 0"
    return
  }
  pass "install-hub.sh --uninstall exited 0"
  assert_file_absent "hub --uninstall removes binary" "$bin"
  assert_file_absent "hub --uninstall removes unit" "$unit_file"
  assert_file_exists "hub --uninstall keeps hub.env" "$env_file"

  CP_INSTALL_ROOT="$SANDBOX_HUB" timeout 30 bash scripts/install-hub.sh --uninstall --purge >/dev/null 2>&1 || {
    fail "install-hub.sh --uninstall --purge exited 0"
    return
  }
  pass "install-hub.sh --uninstall --purge exited 0"
  assert_file_absent "hub --purge removes hub.env" "$env_file"
  assert_file_absent "hub --purge removes data dir" "$data_dir"
}

# ---------------------------------------------------------------------------
# Static checks
# ---------------------------------------------------------------------------

test_static_checks() {
  echo "==> static checks (bash -n, shellcheck if available)"
  assert_exit0 "install-hub.sh bash -n clean" bash -n scripts/install-hub.sh
  assert_exit0 "install-agent.sh bash -n clean" bash -n scripts/install-agent.sh

  if command -v shellcheck >/dev/null 2>&1; then
    assert_exit0 "install-hub.sh shellcheck -S warning clean" \
      shellcheck -S warning scripts/install-hub.sh
    assert_exit0 "install-agent.sh shellcheck -S warning clean" \
      shellcheck -S warning scripts/install-agent.sh
  else
    echo "SKIP: shellcheck not available"
  fi
}

# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------

main() {
  if [ "$(uname -s)" != "Linux" ]; then
    echo "test-install.sh only runs on Linux" >&2
    exit 1
  fi

  TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/cloud-pulse-test-install.XXXXXX")"
  DIST_DIR="${TMP_ROOT}/dist"
  SANDBOX_HUB="${TMP_ROOT}/sandbox-hub"
  SANDBOX_AGENT="${TMP_ROOT}/sandbox-agent"
  mkdir -p "$DIST_DIR" "$SANDBOX_HUB" "$SANDBOX_AGENT"

  test_static_checks

  build_release_assets
  start_asset_server
  start_tampered_server

  test_hub_install
  test_hub_upgrade_keeps_token
  test_hub_upgrade_from_legacy_stub
  test_hub_dry_run_upgrade_hint
  test_hub_explicit_listen_host_in_one_liner
  test_hub_dry_run
  test_hub_checksum_mismatch

  test_agent_missing_required_flags
  test_agent_install
  test_agent_upgrade_keeps_values
  test_agent_upgrade_from_legacy_stub
  test_agent_unknown_arch
  test_agent_checksum_mismatch
  test_agent_dry_run

  test_hub_rejects_malicious_values
  test_agent_rejects_malicious_values

  test_agent_uninstall_and_purge
  test_hub_uninstall_and_purge

  echo
  echo "===================================================="
  echo "Results: ${PASS_COUNT} passed, ${FAIL_COUNT} failed"
  echo "===================================================="

  if [ "$FAIL_COUNT" -gt 0 ]; then
    exit 1
  fi
  exit 0
}

main "$@"
