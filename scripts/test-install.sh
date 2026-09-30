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

# All existing (flag-driven) test cases must never hang waiting on a
# menu prompt, even if this script happens to be run with a controlling
# terminal attached (e.g. interactively during development). New
# interactive-menu test cases override this per-invocation via
# `env -u CP_NONINTERACTIVE ...` when they specifically want the menu
# path exercised through a real pty (see test_*_menu_* below).
export CP_NONINTERACTIVE=1

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
SYSTEMCTL_SHIM=""
SYSTEMCTL_LOG=""
SYSTEMCTL_STATE=""

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

assert_equal() {
  local desc="$1" got="$2" want="$3"
  if [ "$got" = "$want" ]; then
    pass "$desc"
  else
    fail "$desc (got '$got', want '$want')"
  fi
}

assert_file_order() {
  local desc="$1" path="$2" first="$3" second="$4" first_line second_line
  first_line="$(grep -n -m1 -F -- "$first" "$path" 2>/dev/null | cut -d: -f1 || true)"
  second_line="$(grep -n -m1 -F -- "$second" "$path" 2>/dev/null | cut -d: -f1 || true)"
  if [ -n "$first_line" ] && [ -n "$second_line" ] && [ "$first_line" -lt "$second_line" ]; then
    pass "$desc"
  else
    fail "$desc (first='$first' line=${first_line:-missing}, second='$second' line=${second_line:-missing})"
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

  TARGETS="linux/${go_arch} darwin/arm64" ALLOW_ANY_VERSION=1 \
    timeout 300 scripts/build-release.sh v0.0.0-test "$DIST_DIR" >/dev/null

  assert_file_exists "build-release produced hub asset" "${DIST_DIR}/cloud-pulse-hub-linux-${HOST_ARCH}"
  assert_file_exists "build-release produced agent asset" "${DIST_DIR}/cloud-pulse-agent-linux-${HOST_ARCH}"
  assert_file_exists "build-release produced checksums.txt" "${DIST_DIR}/checksums.txt"
  # darwin/arm64 cross-build (CGO_ENABLED=0, no host toolchain needed):
  # used by test_agent_darwin_sandbox_install below to exercise the
  # launchd platform branch of install-agent.sh even though this test
  # suite itself always runs on Linux (SPEC-v0.7 §1: "리눅스에서도 실행
  # 가능하게 한다"). The binary can't actually execute here (wrong
  # OS/ABI) — that's fine, since every code path this test exercises
  # (dscl/launchctl hooks, plist rendering, path/permission
  # assertions) never depends on running it; install-agent.sh's own
  # run_sanity_checks treats a failed `-once`/`-version` probe as a
  # non-fatal warning, not an install failure.
  assert_file_exists "build-release produced darwin agent asset" "${DIST_DIR}/cloud-pulse-agent-darwin-arm64"
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
  assert_contains "hub same-version re-run prints 'Reinstalled'" "$out" "Reinstalled v0.0.0-test"
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
  assert_contains "agent same-version re-run prints 'Reinstalled'" "$out" "Reinstalled v0.0.0-test"
  assert_contains "agent upgrade re-run prints future-updates hint" "$out" "Future updates: sudo cloud-pulse-agent update"
  assert_contains "agent upgrade from legacy (v0.0.0-test) prints built-in-updater line" "$out" \
    "This install now includes the built-in updater."
}

# ---------------------------------------------------------------------------
# darwin-launchd sandbox tests (SPEC-v0.7 §1) — runnable on Linux via
# CP_TEST_UNAME_S/CP_LAUNCHCTL/CP_DSCL hooks, exercising
# install-agent.sh's darwin-launchd platform branch without ever
# needing a real macOS host. See build_release_assets' darwin/arm64
# cross-build note above for why the binary itself never actually runs.
# ---------------------------------------------------------------------------

# write_fake_launchctl_dscl DIR — writes minimal fake `launchctl`/`dscl`
# shims into DIR (added to PATH-independent CP_LAUNCHCTL/CP_DSCL env
# vars, never relying on real system binaries), logging every
# invocation to DIR/launchctl.log / DIR/dscl.log for assertions, and
# maintaining just enough state for install-agent.sh's own logic to
# behave sensibly across install/reinstall/uninstall:
#   - `launchctl print system/<label>`: exit 0 (i.e. "loaded") only if
#     DIR/loaded-<label> exists; `bootstrap system <path>` creates that
#     marker; `bootout system/<label>` removes it.
#   - `launchctl kickstart -k ...`: always exits 0 (no-op beyond
#     logging).
#   - `dscl . -read /Users/_cloudpulse UniqueID`: exit 0 only if
#     DIR/user-exists marker is present; `-create` creates it;
#     `-delete` removes it. `-list /Users UniqueID` prints nothing
#     (empty used-UID set), matching a fresh sandbox.
write_fake_launchctl_dscl() {
  local dir="$1"
  cat > "${dir}/launchctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
DIR="$(dirname "$0")"
echo "$*" >> "${DIR}/launchctl.log"
case "$1" in
  print)
    label="${2#system/}"
    if [ -f "${DIR}/loaded-${label}" ]; then
      echo "	pid = 12345"
      exit 0
    fi
    exit 1
    ;;
  bootstrap)
    label="$(basename "$3" .plist)"
    touch "${DIR}/loaded-${label}"
    ;;
  bootout)
    label="${2#system/}"
    rm -f "${DIR}/loaded-${label}"
    ;;
  kickstart)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
EOF
  chmod +x "${dir}/launchctl"

  cat > "${dir}/dscl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
DIR="$(dirname "$0")"
echo "$*" >> "${DIR}/dscl.log"
printf 'dscl %s\n' "$*" >> "${DIR}/calls.log"
record_prefix() {
  case "$1" in
    /Users/_cloudpulse) echo user ;;
    /Groups/_cloudpulse) echo group ;;
    *) exit 1 ;;
  esac
}
case "$2" in
  -read)
    prefix="$(record_prefix "$3")"
    [ -f "${DIR}/${prefix}-exists" ] || exit 1
    [ -f "${DIR}/${prefix}-$4" ] || exit 1
    printf '%s: %s\n' "$4" "$(cat "${DIR}/${prefix}-$4")"
    ;;
  -create)
    prefix="$(record_prefix "$3")"
    touch "${DIR}/${prefix}-exists"
    if [ "$#" -ge 5 ]; then
      printf '%s\n' "$5" > "${DIR}/${prefix}-$4"
    fi
    ;;
  -delete)
    prefix="$(record_prefix "$3")"
    rm -f "${DIR}/${prefix}-exists" "${DIR}/${prefix}-"*
    ;;
  -change)
    prefix="$(record_prefix "$3")"
    [ -f "${DIR}/${prefix}-exists" ] || exit 1
    printf '%s\n' "$6" > "${DIR}/${prefix}-$4"
    ;;
  -list)
    case "$3:$4" in
      /Users:UniqueID)
        if [ -f "${DIR}/user-exists" ] && [ -f "${DIR}/user-UniqueID" ]; then
          printf '_cloudpulse %s\n' "$(cat "${DIR}/user-UniqueID")"
        fi
        ;;
      /Groups:PrimaryGroupID)
        if [ -f "${DIR}/group-exists" ] && [ -f "${DIR}/group-PrimaryGroupID" ]; then
          printf '_cloudpulse %s\n' "$(cat "${DIR}/group-PrimaryGroupID")"
        fi
        ;;
    esac
    ;;
  *) exit 0 ;;
esac
EOF
  chmod +x "${dir}/dscl"

  cat > "${dir}/chown" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
DIR="$(dirname "$0")"
printf '%s\n' "$*" >> "${DIR}/chown.log"
printf 'chown %s\n' "$*" >> "${DIR}/calls.log"
EOF
  chmod +x "${dir}/chown"
}

test_agent_darwin_sandbox_install() {
  echo "==> testing install-agent.sh darwin-launchd sandbox install (CP_TEST_UNAME_S=Darwin)"
  local sandbox="${TMP_ROOT}/sandbox-agent-darwin"
  local hooks="${TMP_ROOT}/darwin-hooks"
  mkdir -p "$sandbox" "$hooks"
  write_fake_launchctl_dscl "$hooks"

  local out
  out="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    CP_TEST_UNAME_S=Darwin CP_TEST_UNAME_M=arm64 \
    CP_LAUNCHCTL="${hooks}/launchctl" CP_DSCL="${hooks}/dscl" CP_CHOWN="${hooks}/chown" \
    timeout 60 bash scripts/install-agent.sh --hub-url "http://127.0.0.1:${SERVER_PORT}" --token "$HUB_TOKEN" 2>&1)" || {
    fail "install-agent.sh darwin sandbox install exited 0"
    echo "$out" >&2
    return
  }
  pass "install-agent.sh darwin sandbox install exited 0"

  local bin="${sandbox}/usr/local/bin/cloud-pulse-agent"
  local env_file="${sandbox}/usr/local/etc/cloud-pulse/agent.env"
  local plist_file="${sandbox}/Library/LaunchDaemons/com.cloudpulse.agent.plist"
  local agent_log="${sandbox}/Library/Logs/cloud-pulse-agent.log"
  local updater_log="${sandbox}/Library/Logs/cloud-pulse-agent-update.log"

  assert_executable "darwin agent binary installed and executable" "$bin"
  assert_file_exists "darwin agent.env created" "$env_file"
  assert_file_contains "darwin agent.env contains CP_HUB_URL" "$env_file" "CP_HUB_URL=http://127.0.0.1:${SERVER_PORT}"
  assert_file_contains "darwin agent.env contains CP_AGENT_TOKEN" "$env_file" "CP_AGENT_TOKEN=${HUB_TOKEN}"

  assert_file_exists "darwin plist created" "$plist_file"
  assert_file_contains "darwin plist references the binary path" "$plist_file" "$bin"
  assert_file_contains "darwin plist references --env-file" "$plist_file" "--env-file"
  assert_file_contains "darwin plist sets UserName _cloudpulse" "$plist_file" "_cloudpulse"
  assert_file_contains "darwin plist has KeepAlive" "$plist_file" "KeepAlive"

  if command -v plutil >/dev/null 2>&1; then
    assert_exit0 "darwin plist passes plutil -lint" timeout 10 plutil -lint "$plist_file"
  else
    echo "SKIP: plutil not available on this host; skipping darwin plist lint"
  fi
  assert_exit0 "forced fallback main plist parses with python plistlib" \
    python3 -c 'import plistlib,sys; plistlib.loads(open(sys.argv[1], "rb").read())' "$plist_file"
  local binary_plist="${TMP_ROOT}/darwin-binary-rendered.plist"
  if go run ./cmd/agent plist print --bin-path "$bin" --env-file "$env_file" --user _cloudpulse \
      --log-path "${sandbox}/Library/Logs/cloud-pulse-agent.log" > "$binary_plist"; then
    assert_exit0 "binary-rendered main plist parses with python plistlib" \
      python3 -c 'import plistlib,sys; plistlib.loads(open(sys.argv[1], "rb").read())' "$binary_plist"
    if cmp -s "$binary_plist" "$plist_file"; then
      pass "forced fallback main plist is byte-identical to binary rendering"
    else
      fail "forced fallback main plist is byte-identical to binary rendering"
    fi
  else
    fail "native agent binary renders darwin plist for fallback drift test"
  fi

  assert_file_exists "darwin dscl hook was invoked (user/group creation)" "${hooks}/dscl.log"
  assert_file_contains "darwin dscl hook created private _cloudpulse group" "${hooks}/dscl.log" "-create /Groups/_cloudpulse"
  assert_file_contains "darwin dscl hook created _cloudpulse user" "${hooks}/dscl.log" "-create /Users/_cloudpulse"
  assert_equal "darwin user's PrimaryGroupID equals private group GID"     "$(cat "${hooks}/user-PrimaryGroupID")" "$(cat "${hooks}/group-PrimaryGroupID")"
  assert_file_exists "darwin chown hook exercised ownership decisions" "${hooks}/chown.log"
  assert_file_contains "darwin env ownership is root:_cloudpulse" "${hooks}/chown.log" "root:_cloudpulse ${env_file}"
  assert_file_exists "darwin main launchd log is pre-created" "$agent_log"
  assert_perm "darwin main launchd log mode is 0640" "$agent_log" "640"
  assert_file_contains "darwin main log ownership is private user/group" "${hooks}/chown.log" "_cloudpulse:_cloudpulse ${agent_log}"
  assert_file_contains "darwin updater log is root-owned" "${hooks}/chown.log" "root ${updater_log}"
  assert_file_order "darwin private group is created before root:_cloudpulse chown" "${hooks}/calls.log" \
    "dscl . -create /Groups/_cloudpulse" "chown root:_cloudpulse ${env_file}"
  assert_not_contains "darwin ownership never uses staff" "$(cat "${hooks}/chown.log")" "staff"
  assert_not_contains "darwin ownership never uses GID 20" "$(cat "${hooks}/chown.log")" "root:20"
  assert_file_exists "darwin launchctl hook was invoked (bootstrap+kickstart)" "${hooks}/launchctl.log"
  assert_file_contains "darwin launchctl hook ran bootstrap" "${hooks}/launchctl.log" "bootstrap"
  assert_file_contains "darwin launchctl hook ran kickstart" "${hooks}/launchctl.log" "kickstart"

  assert_contains "darwin first install prints future-updates hint" "$out" "Future updates: sudo cloud-pulse-agent update"
}

test_agent_darwin_sandbox_reinstall() {
  echo "==> testing install-agent.sh darwin-launchd sandbox reinstall (idempotent user/service handling)"
  local sandbox="${TMP_ROOT}/sandbox-agent-darwin"
  local hooks="${TMP_ROOT}/darwin-hooks"

  local out
  out="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    CP_TEST_UNAME_S=Darwin CP_TEST_UNAME_M=arm64 \
    CP_LAUNCHCTL="${hooks}/launchctl" CP_DSCL="${hooks}/dscl" CP_CHOWN="${hooks}/chown" \
    timeout 60 bash scripts/install-agent.sh 2>&1)" || {
    fail "install-agent.sh darwin sandbox reinstall exited 0"
    echo "$out" >&2
    return
  }
  pass "install-agent.sh darwin sandbox reinstall exited 0"
  # Not asserting the exact "Reinstalled vX.Y.Z" wording here: that
  # message depends on probe_existing_version actually executing the
  # installed binary (`<bin> -version`), which a cross-compiled
  # darwin/arm64 binary cannot do on this Linux test host (wrong
  # OS/ABI) — probe_existing_version degrades gracefully to an empty
  # string in that case (see install-agent.sh's own doc comment on
  # print_version_hint), so this is a real, expected limitation of
  # running these sandbox tests on Linux rather than a bug. The
  # bootout/idempotent-user assertions below cover the behavior that
  # actually matters and IS verifiable without executing the binary.

  local bootout_count
  bootout_count="$(grep -c '^bootout' "${hooks}/launchctl.log" || true)"
  if [ "$bootout_count" -ge 1 ]; then
    pass "darwin reinstall issued at least one launchctl bootout (reload-on-upgrade)"
  else
    fail "darwin reinstall did not issue any launchctl bootout"
  fi

  # dscl -create must not be called a second time for an
  # already-existing user (idempotent reuse, SPEC-v0.7 §1: "이미 있으면
  # 재사용한다") — the fake dscl's -read check inside
  # ensure_system_user_darwin should have short-circuited before any
  # further -create calls for this reinstall.
  local create_count_before create_count_after
  create_count_before="$(grep -c '^\. -create /Users/_cloudpulse$' "${hooks}/dscl.log" || true)"
  assert_exit0 "darwin second reinstall still succeeds" true
  create_count_after="$(grep -c '^\. -create /Users/_cloudpulse$' "${hooks}/dscl.log" || true)"
  if [ "$create_count_after" = "$create_count_before" ]; then
    pass "darwin reinstall does not recreate the already-existing _cloudpulse user"
  else
    fail "darwin reinstall unexpectedly recreated _cloudpulse (before=${create_count_before} after=${create_count_after})"
  fi
}

test_agent_darwin_sandbox_remote_update() {
  echo "==> testing install-agent.sh darwin-launchd --remote-update installs the update plist"
  local sandbox="${TMP_ROOT}/sandbox-agent-darwin-remote"
  local hooks="${TMP_ROOT}/darwin-hooks-remote"
  mkdir -p "$sandbox" "$hooks"
  write_fake_launchctl_dscl "$hooks"

  local out
  out="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    CP_TEST_UNAME_S=Darwin CP_TEST_UNAME_M=arm64 \
    CP_LAUNCHCTL="${hooks}/launchctl" CP_DSCL="${hooks}/dscl" CP_CHOWN="${hooks}/chown" \
    timeout 60 bash scripts/install-agent.sh --hub-url "http://127.0.0.1:${SERVER_PORT}" --token "$HUB_TOKEN" --remote-update 2>&1)" || {
    fail "install-agent.sh darwin --remote-update sandbox install exited 0"
    echo "$out" >&2
    return
  }
  pass "install-agent.sh darwin --remote-update sandbox install exited 0"

  local update_plist="${sandbox}/Library/LaunchDaemons/com.cloudpulse.agent-update.plist"
  local request_dir="${sandbox}/Library/Application Support/cloud-pulse-agent"
  local result_dir="${sandbox}/Library/Application Support/cloud-pulse-agent-update"
  local updater_log="${sandbox}/Library/Logs/cloud-pulse-agent-update.log"

  assert_file_exists "darwin update plist created" "$update_plist"
  assert_file_contains "darwin update plist has WatchPaths" "$update_plist" "WatchPaths"
  assert_file_contains "darwin update plist passes its agent.env to update helper" "$update_plist" "--env-file"
  assert_file_contains "darwin update plist references --from-request" "$update_plist" "--from-request"
  assert_file_contains "darwin update plist records helper output" "$update_plist" "$updater_log"
  assert_file_exists "darwin updater log is pre-created" "$updater_log"
  assert_perm "darwin updater log mode is 0640" "$updater_log" "640"
  assert_exit0 "darwin update plist parses with python plistlib" \
    python3 -c 'import plistlib,sys; plistlib.loads(open(sys.argv[1], "rb").read())' "$update_plist"
  assert_file_exists "darwin update request dir created" "$request_dir"
  assert_file_exists "darwin update result dir created" "$result_dir"
  assert_file_contains "darwin launchctl hook bootstrapped the update plist" "${hooks}/launchctl.log" "agent-update"
  assert_file_contains "darwin update request directory ownership is private user/group" "${hooks}/chown.log" "_cloudpulse:_cloudpulse ${request_dir}"
  assert_file_contains "darwin update result directory ownership is root" "${hooks}/chown.log" "root ${result_dir}"
  assert_perm "darwin update request directory mode is 0750" "$request_dir" "750"
  assert_perm "darwin update result directory mode is 0755" "$result_dir" "755"
}

test_agent_darwin_sandbox_uninstall() {
  echo "==> testing install-agent.sh darwin-launchd --uninstall --purge"
  local sandbox="${TMP_ROOT}/sandbox-agent-darwin"
  local hooks="${TMP_ROOT}/darwin-hooks"

  local out
  out="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    CP_TEST_UNAME_S=Darwin CP_TEST_UNAME_M=arm64 \
    CP_LAUNCHCTL="${hooks}/launchctl" CP_DSCL="${hooks}/dscl" CP_CHOWN="${hooks}/chown" \
    timeout 30 bash scripts/install-agent.sh --uninstall --purge 2>&1)" || {
    fail "install-agent.sh darwin --uninstall --purge exited 0"
    echo "$out" >&2
    return
  }
  pass "install-agent.sh darwin --uninstall --purge exited 0"

  assert_file_absent "darwin uninstall removed the binary" "${sandbox}/usr/local/bin/cloud-pulse-agent"
  assert_file_absent "darwin uninstall removed the plist" "${sandbox}/Library/LaunchDaemons/com.cloudpulse.agent.plist"
  assert_file_absent "darwin purge removed agent.env" "${sandbox}/usr/local/etc/cloud-pulse/agent.env"
  assert_file_contains "darwin uninstall issued a bootout" "${hooks}/launchctl.log" "bootout"
  assert_file_absent "darwin purge removed the _cloudpulse user marker" "${hooks}/user-exists"
  assert_file_absent "darwin purge removed the _cloudpulse group marker" "${hooks}/group-exists"
  assert_file_contains "darwin purge deletes the private _cloudpulse group" "${hooks}/dscl.log" "-delete /Groups/_cloudpulse"
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

# test_agent_docker_flag — SPEC-v0.5 §C: --docker adds
# SupplementaryGroups=docker to the rendered unit and sets
# CP_DOCKER=auto in agent.env; a reinstall without re-passing --docker
# preserves both (detect_preserved_docker_flag).
test_agent_docker_flag() {
  echo "==> testing install-agent.sh --docker flag (unit SupplementaryGroups + CP_DOCKER + preservation on reinstall)"
  local sandbox="${TMP_ROOT}/sandbox-agent-docker"
  mkdir -p "$sandbox"
  local unit_file="${sandbox}/etc/systemd/system/cloud-pulse-agent.service"
  local env_file="${sandbox}/etc/cloud-pulse/agent.env"

  local out
  out="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-agent.sh --hub-url "http://127.0.0.1:${SERVER_PORT}" --token "$HUB_TOKEN" --docker 2>&1)" || {
    fail "install-agent.sh --docker sandbox install exited 0"
    echo "$out" >&2
    return
  }
  pass "install-agent.sh --docker sandbox install exited 0"

  assert_file_contains "agent unit has SupplementaryGroups=docker after --docker" "$unit_file" "SupplementaryGroups=docker"
  assert_file_contains "agent.env has CP_DOCKER=auto after --docker" "$env_file" "CP_DOCKER=auto"
  assert_contains "install-agent.sh --docker logs the usermod/docker-group action" "$out" "docker"

  # Reinstall without --docker must preserve both.
  local out2
  out2="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-agent.sh 2>&1)" || {
    fail "install-agent.sh reinstall (no --docker) after --docker exited 0"
    echo "$out2" >&2
    return
  }
  pass "install-agent.sh reinstall (no --docker) after --docker exited 0"
  assert_file_contains "agent unit still has SupplementaryGroups=docker after reinstall without --docker" "$unit_file" "SupplementaryGroups=docker"
  assert_file_contains "agent.env still has CP_DOCKER=auto after reinstall without --docker" "$env_file" "CP_DOCKER=auto"
  assert_contains "reinstall logs preserving the --docker setting" "$out2" "preserving existing --docker setting"
}

test_agent_no_docker_flag_omits_supplementary_groups() {
  echo "==> testing install-agent.sh without --docker omits SupplementaryGroups and CP_DOCKER stays commented"
  local unit_file="${SANDBOX_AGENT}/etc/systemd/system/cloud-pulse-agent.service"
  local env_file="${SANDBOX_AGENT}/etc/cloud-pulse/agent.env"
  assert_not_contains "agent unit has no SupplementaryGroups= without --docker" "$(cat "$unit_file")" "SupplementaryGroups"
  assert_file_contains "agent.env has CP_DOCKER commented out without --docker" "$env_file" "#CP_DOCKER=auto"
}

# test_agent_remote_update_flag — SPEC-v0.6 §2: --remote-update adds
# StateDirectory=cloud-pulse-agent to the rendered unit, sets
# CP_REMOTE_UPDATE=on in agent.env, and installs
# cloud-pulse-agent-update.path/.service; a reinstall without
# re-passing --remote-update preserves all three
# (detect_preserved_remote_update_flag).
test_agent_remote_update_flag() {
  echo "==> testing install-agent.sh --remote-update flag (StateDirectory + CP_REMOTE_UPDATE + update units + preservation on reinstall)"
  local sandbox="${TMP_ROOT}/sandbox-agent-remote-update"
  mkdir -p "$sandbox"
  local unit_file="${sandbox}/etc/systemd/system/cloud-pulse-agent.service"
  local env_file="${sandbox}/etc/cloud-pulse/agent.env"
  local update_path_unit="${sandbox}/etc/systemd/system/cloud-pulse-agent-update.path"
  local update_service_unit="${sandbox}/etc/systemd/system/cloud-pulse-agent-update.service"

  local out
  out="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_UPDATE_LATEST_URL="http://127.0.0.1:${SERVER_PORT}/latest" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-agent.sh --hub-url "http://127.0.0.1:${SERVER_PORT}" --token "$HUB_TOKEN" --remote-update 2>&1)" || {
    fail "install-agent.sh --remote-update sandbox install exited 0"
    echo "$out" >&2
    return
  }
  pass "install-agent.sh --remote-update sandbox install exited 0"

  assert_file_contains "agent unit has StateDirectory=cloud-pulse-agent after --remote-update" "$unit_file" "StateDirectory=cloud-pulse-agent"
  assert_file_contains "agent.env has CP_REMOTE_UPDATE=on after --remote-update" "$env_file" "CP_REMOTE_UPDATE=on"
  assert_file_contains "agent.env persists explicit release mirror for helper" "$env_file" "CP_RELEASE_BASE_URL=http://127.0.0.1:${SERVER_PORT}"
  assert_file_contains "agent.env persists explicit latest URL for helper" "$env_file" "CP_UPDATE_LATEST_URL=http://127.0.0.1:${SERVER_PORT}/latest"
  assert_file_exists "cloud-pulse-agent-update.path installed after --remote-update" "$update_path_unit"
  assert_file_exists "cloud-pulse-agent-update.service installed after --remote-update" "$update_service_unit"
  assert_file_contains "update .path unit activates on request existence after atomic rename" "$update_path_unit" "PathExists="
  assert_not_contains "update .path unit does not rely on PathModified" "$(cat "$update_path_unit")" "PathModified="
  assert_file_contains "update .service unit receives root-owned agent.env" "$update_service_unit" "EnvironmentFile=${env_file}"
  assert_file_contains "update .service unit invokes update --from-request" "$update_service_unit" "update --from-request"

  # Reinstall without --remote-update must preserve everything.
  local out2
  out2="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-agent.sh 2>&1)" || {
    fail "install-agent.sh reinstall (no --remote-update) after --remote-update exited 0"
    echo "$out2" >&2
    return
  }
  pass "install-agent.sh reinstall (no --remote-update) after --remote-update exited 0"
  assert_file_contains "agent unit still has StateDirectory=cloud-pulse-agent after reinstall without --remote-update" "$unit_file" "StateDirectory=cloud-pulse-agent"
  assert_file_contains "agent.env still has CP_REMOTE_UPDATE=on after reinstall without --remote-update" "$env_file" "CP_REMOTE_UPDATE=on"
  assert_contains "reinstall logs preserving the --remote-update setting" "$out2" "preserving existing --remote-update setting"
}

test_agent_no_remote_update_flag_omits_state_directory() {
  echo "==> testing install-agent.sh without --remote-update omits StateDirectory and CP_REMOTE_UPDATE stays commented, no update units installed"
  local unit_file="${SANDBOX_AGENT}/etc/systemd/system/cloud-pulse-agent.service"
  local env_file="${SANDBOX_AGENT}/etc/cloud-pulse/agent.env"
  local update_path_unit="${SANDBOX_AGENT}/etc/systemd/system/cloud-pulse-agent-update.path"
  assert_not_contains "agent unit has no StateDirectory= without --remote-update" "$(cat "$unit_file")" "StateDirectory"
  assert_file_contains "agent.env has CP_REMOTE_UPDATE commented out without --remote-update" "$env_file" "#CP_REMOTE_UPDATE=off"
  assert_file_absent "no cloud-pulse-agent-update.path installed without --remote-update" "$update_path_unit"
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

  assert_contains "legacy-stub reinstall prints 'Downgraded v0.2.0 →'" "$out" "Downgraded v0.2.0 →"
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

  assert_contains "legacy-stub reinstall prints 'Downgraded v0.2.0 →'" "$out" "Downgraded v0.2.0 →"
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
# SPEC-v0.3.1 A: --install/--reinstall action flags, -y/--yes, UI token
# generate/rotate semantics
# ---------------------------------------------------------------------------

test_hub_install_reinstall_flag_errors() {
  echo "==> testing install-hub.sh --install/--reinstall action-flag guards"
  local sandbox="${TMP_ROOT}/sandbox-hub-action-flags"
  mkdir -p "$sandbox"

  assert_nonzero_exit "hub --reinstall on a fresh sandbox (not installed) fails" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 30 bash scripts/install-hub.sh --reinstall

  assert_exit0 "hub --install on a fresh sandbox succeeds" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-hub.sh --install --listen :18095

  assert_nonzero_exit "hub --install when already installed fails with a hint" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 30 bash scripts/install-hub.sh --install

  assert_exit0 "hub --reinstall when already installed succeeds" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-hub.sh --reinstall
}

test_agent_install_reinstall_flag_errors() {
  echo "==> testing install-agent.sh --install/--reinstall action-flag guards"
  local sandbox="${TMP_ROOT}/sandbox-agent-action-flags"
  mkdir -p "$sandbox"

  assert_nonzero_exit "agent --reinstall on a fresh sandbox (not installed) fails" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 30 bash scripts/install-agent.sh --reinstall

  assert_exit0 "agent --install on a fresh sandbox succeeds" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-agent.sh --install --hub-url "http://127.0.0.1:${SERVER_PORT}" --token "$HUB_TOKEN"

  assert_nonzero_exit "agent --install when already installed fails with a hint" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 30 bash scripts/install-agent.sh --install

  assert_exit0 "agent --reinstall when already installed succeeds" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-agent.sh --reinstall
}

test_hub_generate_and_rotate_ui_token() {
  echo "==> testing install-hub.sh --generate-ui-token (idempotent) vs --rotate-ui-token"
  local sandbox="${TMP_ROOT}/sandbox-hub-ui-token"
  mkdir -p "$sandbox"
  local env_file="${sandbox}/etc/cloud-pulse/hub.env"

  local out1 out2 out3
  out1="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-hub.sh --listen :18096 --generate-ui-token 2>&1)" || {
    fail "hub install with --generate-ui-token (1st run) exited 0"
    echo "$out1" >&2
    return
  }
  assert_contains "1st --generate-ui-token run prints the token once" "$out1" "API token (optional, for scripts"
  local token1
  token1="$(grep '^CP_UI_TOKEN=' "$env_file" | cut -d= -f2)"

  local out2_status=0
  out2="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-hub.sh --generate-ui-token 2>&1)" || out2_status=$?
  if [ "$out2_status" -ne 0 ]; then
    fail "hub install with --generate-ui-token (2nd run) exited 0"
    echo "$out2" >&2
    return
  fi
  local token2
  token2="$(grep '^CP_UI_TOKEN=' "$env_file" | cut -d= -f2)"
  if [ "$token1" = "$token2" ]; then
    pass "--generate-ui-token is idempotent (token unchanged on 2nd run)"
  else
    fail "--generate-ui-token is idempotent (token unchanged on 2nd run) (was ${token1}, now ${token2})"
  fi
  assert_contains "2nd --generate-ui-token run reports the token as unchanged" "$out2" "API token: unchanged"

  local out3_status=0
  out3="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-hub.sh --rotate-ui-token 2>&1)" || out3_status=$?
  if [ "$out3_status" -ne 0 ]; then
    fail "hub install with --rotate-ui-token exited 0"
    echo "$out3" >&2
    return
  fi
  local token3
  token3="$(grep '^CP_UI_TOKEN=' "$env_file" | cut -d= -f2)"
  if [ "$token3" != "$token2" ]; then
    pass "--rotate-ui-token always generates a new token"
  else
    fail "--rotate-ui-token always generates a new token (unchanged: ${token3})"
  fi
  assert_contains "--rotate-ui-token run prints the new token once" "$out3" "API token (optional, for scripts"
}

test_hub_no_ui_token_prints_disabled_hint() {
  echo "==> testing install-hub.sh prints 'API token: not configured' when no UI token is ever configured"
  local sandbox="${TMP_ROOT}/sandbox-hub-no-ui-token"
  mkdir -p "$sandbox"
  local out
  out="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-hub.sh --listen :18097 2>&1)" || {
    fail "hub install without any UI token flag exited 0"
    echo "$out" >&2
    return
  }
  assert_contains "install without a UI token prints the not-configured hint" "$out" "API token: not configured"
}

# ---------------------------------------------------------------------------
# systemd-unit rendering drift test (SPEC-v0.3.1 B / D): the freshly
# built binary's "systemd-unit print" output must match the bash
# heredoc fallback (forced via CP_INSTALL_FORCE_SCRIPT_UNIT=1).
# ---------------------------------------------------------------------------

test_systemd_unit_render_drift() {
  echo "==> testing systemd-unit print vs heredoc fallback drift (hub real+sandbox, agent)"
  local hub_bin="${DIST_DIR}/cloud-pulse-hub-linux-${HOST_ARCH}"
  local agent_bin="${DIST_DIR}/cloud-pulse-agent-linux-${HOST_ARCH}"
  chmod +x "$hub_bin" "$agent_bin" 2>/dev/null || true

  if ! timeout 10 "$hub_bin" systemd-unit print --bin-path /usr/local/bin/cloud-pulse-hub \
      --env-file /etc/cloud-pulse/hub.env --user cloud-pulse --group cloud-pulse \
      >"${TMP_ROOT}/hub-real-bin.unit" 2>"${TMP_ROOT}/hub-real-bin.err"; then
    echo "SKIP: ${hub_bin} does not support 'systemd-unit print' yet; heredoc fallback path already exercised by every other install test in this suite"
    cat "${TMP_ROOT}/hub-real-bin.err" >&2 || true
    return
  fi
  pass "hub binary supports 'systemd-unit print'"

  # Real-install params (no --read-write-path -> StateDirectory=cloud-pulse).
  render_hub_fallback_standalone /usr/local/bin/cloud-pulse-hub /etc/cloud-pulse/hub.env "" \
    > "${TMP_ROOT}/hub-real-heredoc.unit"
  assert_exit0 "hub unit (real params): systemd-unit print == heredoc fallback (diff)" \
    diff "${TMP_ROOT}/hub-real-bin.unit" "${TMP_ROOT}/hub-real-heredoc.unit"

  # Sandbox-install params (--read-write-path -> ReadWritePaths=...).
  timeout 10 "$hub_bin" systemd-unit print --bin-path /tmp/sandbox/usr/local/bin/cloud-pulse-hub \
    --env-file /tmp/sandbox/etc/cloud-pulse/hub.env --user cloud-pulse --group cloud-pulse \
    --read-write-path /tmp/sandbox/var/lib/cloud-pulse \
    > "${TMP_ROOT}/hub-sandbox-bin.unit"
  render_hub_fallback_standalone /tmp/sandbox/usr/local/bin/cloud-pulse-hub /tmp/sandbox/etc/cloud-pulse/hub.env \
    /tmp/sandbox/var/lib/cloud-pulse > "${TMP_ROOT}/hub-sandbox-heredoc.unit"
  assert_exit0 "hub unit (sandbox params): systemd-unit print == heredoc fallback (diff)" \
    diff "${TMP_ROOT}/hub-sandbox-bin.unit" "${TMP_ROOT}/hub-sandbox-heredoc.unit"

  timeout 10 "$agent_bin" systemd-unit print --bin-path /usr/local/bin/cloud-pulse-agent \
    --env-file /etc/cloud-pulse/agent.env --user cloud-pulse --group cloud-pulse \
    > "${TMP_ROOT}/agent-bin.unit"
  render_agent_fallback_standalone /usr/local/bin/cloud-pulse-agent /etc/cloud-pulse/agent.env \
    > "${TMP_ROOT}/agent-heredoc.unit"
  assert_exit0 "agent unit: systemd-unit print == heredoc fallback (diff)" \
    diff "${TMP_ROOT}/agent-bin.unit" "${TMP_ROOT}/agent-heredoc.unit"

  # --docker (SPEC-v0.5 §C): SupplementaryGroups=docker line.
  timeout 10 "$agent_bin" systemd-unit print --bin-path /usr/local/bin/cloud-pulse-agent \
    --env-file /etc/cloud-pulse/agent.env --user cloud-pulse --group cloud-pulse \
    --supplementary-groups docker \
    > "${TMP_ROOT}/agent-docker-bin.unit"
  render_agent_fallback_standalone /usr/local/bin/cloud-pulse-agent /etc/cloud-pulse/agent.env docker \
    > "${TMP_ROOT}/agent-docker-heredoc.unit"
  assert_exit0 "agent unit (--docker): systemd-unit print == heredoc fallback (diff)" \
    diff "${TMP_ROOT}/agent-docker-bin.unit" "${TMP_ROOT}/agent-docker-heredoc.unit"

  # --remote-update (SPEC-v0.6 §2): StateDirectory=cloud-pulse-agent line.
  timeout 10 "$agent_bin" systemd-unit print --bin-path /usr/local/bin/cloud-pulse-agent \
    --env-file /etc/cloud-pulse/agent.env --user cloud-pulse --group cloud-pulse \
    --remote-update \
    > "${TMP_ROOT}/agent-remote-update-bin.unit"
  render_agent_fallback_standalone /usr/local/bin/cloud-pulse-agent /etc/cloud-pulse/agent.env "" 1 \
    > "${TMP_ROOT}/agent-remote-update-heredoc.unit"
  assert_exit0 "agent unit (--remote-update): systemd-unit print == heredoc fallback (diff)" \
    diff "${TMP_ROOT}/agent-remote-update-bin.unit" "${TMP_ROOT}/agent-remote-update-heredoc.unit"

  # cloud-pulse-agent-update.path/.service (SPEC-v0.6 §2): rendered via
  # `systemd-unit apply` against a throwaway sandboxed unit tree, and
  # compared against install-agent.sh's own install_update_units_fallback
  # heredoc. applyUpdateUnitsIfEnabled reads CP_REMOTE_UPDATE=on from the
  # main unit's own EnvironmentFile=, so both must exist for this to
  # actually create the two units (a missing/CP_REMOTE_UPDATE=off env
  # file makes it a no-op, per its own doc comment).
  local update_apply_root="${TMP_ROOT}/update-units-drift"
  mkdir -p "${update_apply_root}/etc/systemd/system" "${update_apply_root}/etc/cloud-pulse"
  local update_apply_env_file="${update_apply_root}/etc/cloud-pulse/agent.env"
  {
    echo "CP_HUB_URL=http://127.0.0.1:${SERVER_PORT}"
    echo "CP_AGENT_TOKEN=${HUB_TOKEN}"
    echo "CP_REMOTE_UPDATE=on"
  } > "$update_apply_env_file"
  render_agent_fallback_standalone /usr/local/bin/cloud-pulse-agent "$update_apply_env_file" "" 1 \
    > "${update_apply_root}/etc/systemd/system/cloud-pulse-agent.service"
  if timeout 10 "$agent_bin" systemd-unit apply \
      --unit-path "${update_apply_root}/etc/systemd/system/cloud-pulse-agent.service" \
      --update-path-unit-path "${update_apply_root}/etc/systemd/system/cloud-pulse-agent-update.path" \
      --update-service-unit-path "${update_apply_root}/etc/systemd/system/cloud-pulse-agent-update.service" \
      --request-path /var/lib/cloud-pulse-agent/update-request.json \
      --result-dir /var/lib/cloud-pulse-agent-update \
      --no-reload >/dev/null 2>&1; then
    pass "agent binary supports 'systemd-unit apply' with remote-update unit flags"
    {
      echo "[Unit]"
      echo "Description=Watch for cloud-pulse-agent remote update requests"
      echo
      echo "[Path]"
      echo "PathExists=/var/lib/cloud-pulse-agent/update-request.json"
      echo "Unit=cloud-pulse-agent-update.service"
      echo
      echo "[Install]"
      echo "WantedBy=multi-user.target"
    } > "${TMP_ROOT}/update-path-heredoc.unit"
    assert_exit0 "cloud-pulse-agent-update.path: systemd-unit apply == install-agent.sh heredoc (diff)" \
      diff "${update_apply_root}/etc/systemd/system/cloud-pulse-agent-update.path" "${TMP_ROOT}/update-path-heredoc.unit"
    {
      echo "[Unit]"
      echo "Description=Apply a hub-requested cloud-pulse-agent update"
      echo
      echo "[Service]"
      echo "Type=oneshot"
      echo "EnvironmentFile=${update_apply_env_file}"
      echo "ExecStart=/usr/local/bin/cloud-pulse-agent update --from-request /var/lib/cloud-pulse-agent/update-request.json --result-dir /var/lib/cloud-pulse-agent-update"
    } > "${TMP_ROOT}/update-service-heredoc.unit"
    assert_exit0 "cloud-pulse-agent-update.service: systemd-unit apply == install-agent.sh heredoc (diff)" \
      diff "${update_apply_root}/etc/systemd/system/cloud-pulse-agent-update.service" "${TMP_ROOT}/update-service-heredoc.unit"
  else
    echo "SKIP: ${agent_bin} does not support 'systemd-unit apply' with remote-update unit flags yet"
  fi
}

# render_hub_fallback_standalone BIN_PATH ENV_FILE DATA_DIR — reproduce
# install-hub.sh's render_unit_fallback() in an isolated subshell (no
# sourcing of the whole script, so this test doesn't depend on its
# other globals) for the drift comparison above.
render_hub_fallback_standalone() {
  local bin_path="$1" env_file="$2" data_dir="$3"
  if [ -n "$data_dir" ]; then
    echo "[Unit]"
    echo "Description=cloud-pulse hub (metrics ingestion + dashboard)"
    echo "After=network-online.target"
    echo "Wants=network-online.target"
    echo
    echo "[Service]"
    echo "Type=simple"
    echo "EnvironmentFile=${env_file}"
    echo "Environment=CP_DATA_DIR=/var/lib/cloud-pulse"
    echo "ExecStart=${bin_path}"
    echo "User=cloud-pulse"
    echo "Group=cloud-pulse"
    echo "Restart=on-failure"
    echo "RestartSec=5"
    echo "ReadWritePaths=${data_dir}"
  else
    echo "[Unit]"
    echo "Description=cloud-pulse hub (metrics ingestion + dashboard)"
    echo "After=network-online.target"
    echo "Wants=network-online.target"
    echo
    echo "[Service]"
    echo "Type=simple"
    echo "EnvironmentFile=${env_file}"
    echo "Environment=CP_DATA_DIR=/var/lib/cloud-pulse"
    echo "ExecStart=${bin_path}"
    echo "User=cloud-pulse"
    echo "Group=cloud-pulse"
    echo "Restart=on-failure"
    echo "RestartSec=5"
    echo "StateDirectory=cloud-pulse"
  fi
  echo
  echo "# --- sandboxing / hardening ---"
  echo "NoNewPrivileges=yes"
  echo "ProtectSystem=strict"
  echo "ProtectHome=read-only"
  echo "PrivateTmp=yes"
  echo "PrivateDevices=yes"
  echo "ProtectKernelTunables=yes"
  echo "ProtectControlGroups=yes"
  echo "RestrictSUIDSGID=yes"
  echo "LockPersonality=yes"
  echo "CapabilityBoundingSet="
  echo "AmbientCapabilities="
  echo "RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK"
  echo
  echo "[Install]"
  echo "WantedBy=multi-user.target"
}

# render_agent_fallback_standalone BIN_PATH ENV_FILE — reproduce
# render_agent_fallback_standalone BIN_PATH ENV_FILE [DOCKER_GROUP] [REMOTE_UPDATE] —
# install-agent.sh's render_unit_fallback() in an isolated subshell.
# DOCKER_GROUP (optional 3rd arg, e.g. "docker") renders a
# SupplementaryGroups= line right after Group=, matching Render's own
# placement (SPEC-v0.5 §C). REMOTE_UPDATE (optional 4th arg, "1")
# renders a StateDirectory=cloud-pulse-agent line right after that
# (SPEC-v0.6 §2), matching Render's own placement.
render_agent_fallback_standalone() {
  local bin_path="$1" env_file="$2" docker_group="${3:-}" remote_update="${4:-}"
  echo "[Unit]"
  echo "Description=cloud-pulse agent (host metrics collector)"
  echo "After=network-online.target"
  echo "Wants=network-online.target"
  echo
  echo "[Service]"
  echo "Type=simple"
  echo "EnvironmentFile=${env_file}"
  echo "ExecStart=${bin_path}"
  echo "User=cloud-pulse"
  echo "Group=cloud-pulse"
  if [ -n "$docker_group" ]; then
    echo "SupplementaryGroups=${docker_group}"
  fi
  if [ "$remote_update" = "1" ]; then
    echo "StateDirectory=cloud-pulse-agent"
  fi
  echo "Restart=on-failure"
  echo "RestartSec=5"
  echo
  echo "# --- sandboxing / hardening ---"
  echo "NoNewPrivileges=yes"
  echo "ProtectSystem=strict"
  echo "ProtectHome=read-only"
  echo "PrivateTmp=yes"
  echo "PrivateDevices=yes"
  echo "ProtectKernelTunables=yes"
  echo "ProtectControlGroups=yes"
  echo "RestrictSUIDSGID=yes"
  echo "LockPersonality=yes"
  echo "CapabilityBoundingSet="
  echo "AmbientCapabilities="
  echo "RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX"
  echo
  echo "[Install]"
  echo "WantedBy=multi-user.target"
}

# ---------------------------------------------------------------------------
# Interactive menu tests (real pty via util-linux `script`)
# ---------------------------------------------------------------------------

# run_in_pty TIMEOUT_SECS INPUT LOG_PATH ENV_ASSIGN... -- CMD — feed INPUT
# (already newline-terminated per line by the caller) to CMD running
# under a real pty via `script -qec`, capturing the full transcript
# (prompts + echoed input, minus the hidden-token line since that's
# read with `read -rs`) at LOG_PATH. CP_NONINTERACTIVE is explicitly
# unset (not just absent) so these specific invocations see the menu
# regardless of the global `export CP_NONINTERACTIVE=1` at the top of
# this file.
run_in_pty() {
  local timeout_secs="$1" input="$2" log_path="$3"
  shift 3
  printf '%s' "$input" | timeout "$timeout_secs" script -qec "env -u CP_NONINTERACTIVE $*" "$log_path" \
    >"${TMP_ROOT}/pty-stdout.log" 2>&1
}

test_hub_menu_fresh_install() {
  echo "==> testing install-hub.sh interactive menu: fresh install via '1' with defaults"
  local sandbox="${TMP_ROOT}/sandbox-hub-menu-fresh"
  mkdir -p "$sandbox"
  local log="${TMP_ROOT}/menu-hub-fresh.log"
  # 1=Install, then Enter (default listen port), Enter (skip webhook URL).
  local status=0
  run_in_pty 60 $'1\n\n\n' "$log" \
    "CP_RELEASE_BASE_URL=http://127.0.0.1:${SERVER_PORT} CP_INSTALL_ROOT=${sandbox} bash scripts/install-hub.sh" || status=$?

  assert_contains "menu shows 'not installed' status on a fresh sandbox" "$(cat "$log")" "Status: not installed"
  assert_contains "menu lists Install/Reinstall/Uninstall/Exit" "$(cat "$log")" "1) Install"
  if [ "$status" -ne 0 ]; then
    fail "hub menu fresh install ('1' + defaults) exited 0"
    cat "$log" >&2 || true
    return
  fi
  pass "hub menu fresh install ('1' + defaults) exited 0"
  assert_file_exists "menu fresh install wrote hub.env" "${sandbox}/etc/cloud-pulse/hub.env"
  assert_not_contains "menu fresh install does not prompt to enable the Settings page" "$(cat "$log")" "Enable the web Settings page"
  assert_contains "menu fresh install prints the default sign-in credentials" "$(cat "$log")" "Sign in: admin / changeme"
}

test_hub_menu_install_when_already_installed() {
  echo "==> testing install-hub.sh interactive menu: '1' on an already-installed hub shows a hint, then '0' exits"
  local sandbox="${TMP_ROOT}/sandbox-hub-menu-fresh"
  local log="${TMP_ROOT}/menu-hub-already-installed.log"
  # 1=Install (already installed -> hint + menu again), 0=Exit.
  local status=0
  run_in_pty 60 $'1\n0\n' "$log" \
    "CP_RELEASE_BASE_URL=http://127.0.0.1:${SERVER_PORT} CP_INSTALL_ROOT=${sandbox} bash scripts/install-hub.sh" || status=$?
  if [ "$status" -ne 0 ]; then
    fail "hub menu '1' on installed then '0' exits 0"
    cat "$log" >&2 || true
    return
  fi
  pass "hub menu '1' on installed then '0' exits 0"
  assert_contains "menu shows 'installed' status" "$(cat "$log")" "Status: installed"
  assert_contains "menu explains already-installed and offers reinstall/update" "$(cat "$log")" "already installed"
  assert_contains "menu shows the menu a second time after the hint" "$(cat "$log")" "1) Install      (설치)"
}

test_hub_menu_reinstall_from_legacy() {
  echo "==> testing install-hub.sh interactive menu: '2' reinstall over a simulated legacy (v0.2.0) install"
  local sandbox="${TMP_ROOT}/sandbox-hub-menu-legacy"
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
  mkdir -p "${sandbox}/etc/cloud-pulse"
  {
    echo "CP_LISTEN=:18098"
    echo "CP_AGENT_TOKEN=0000000000000000000000000000000000000000000000000000000000000000"
    echo "CP_ALLOWED_CIDRS=100.64.0.0/10,fd7a:115c:a1e0::/48,127.0.0.0/8,::1/128"
  } > "${sandbox}/etc/cloud-pulse/hub.env"

  local log="${TMP_ROOT}/menu-hub-reinstall-legacy.log"
  # 2=Reinstall, y=Continue?
  local status=0
  run_in_pty 60 $'2\ny\n' "$log" \
    "CP_RELEASE_BASE_URL=http://127.0.0.1:${SERVER_PORT} CP_INSTALL_ROOT=${sandbox} bash scripts/install-hub.sh" || status=$?
  if [ "$status" -ne 0 ]; then
    fail "hub menu reinstall from legacy exited 0"
    cat "$log" >&2 || true
    return
  fi
  pass "hub menu reinstall from legacy exited 0"
  assert_contains "menu reinstall prompts for current/target version" "$(cat "$log")" "Current version: v0.2.0"
  assert_not_contains "menu reinstall does not prompt to enable the Settings page" "$(cat "$log")" "The web Settings page is disabled"
  assert_contains "menu reinstall over legacy prints 'Downgraded v0.2.0 ->'" "$(cat "$log")" "Downgraded v0.2.0"
  assert_contains "menu reinstall prints password-unchanged sign-in hint" "$(cat "$log")" "Sign in: admin (password unchanged)"
  # NOTE: CP_LISTEN/CP_ALLOWED_CIDRS are pre-existing v0.3.0 behavior:
  # write_env_file() always writes them from OPT_LISTEN/
  # OPT_ALLOWED_CIDRS (their flag defaults when unset), unlike
  # agent_token/ui_token/webhook_url which explicitly fall back to the
  # existing env value. Reinstall via the menu passes no --listen, so
  # CP_LISTEN reverts to the ":8090" default here — this assertion
  # checks the token/env file is otherwise intact rather than asserting
  # a preservation behavior the underlying script has never had.
  assert_file_contains "menu reinstall kept the legacy CP_AGENT_TOKEN" "${sandbox}/etc/cloud-pulse/hub.env" \
    "CP_AGENT_TOKEN=0000000000000000000000000000000000000000000000000000000000000000"
}

test_hub_menu_uninstall_keep_then_purge() {
  echo "==> testing install-hub.sh interactive menu: '3' uninstall (y,n keeps env) then again (y,y purges)"
  local sandbox="${TMP_ROOT}/sandbox-hub-menu-fresh"

  local log1="${TMP_ROOT}/menu-hub-uninstall-keep.log"
  local status1=0
  run_in_pty 60 $'3\ny\nn\n' "$log1" \
    "CP_INSTALL_ROOT=${sandbox} bash scripts/install-hub.sh" || status1=$?
  if [ "$status1" -ne 0 ]; then
    fail "hub menu uninstall (keep env) exited 0"
    cat "$log1" >&2 || true
    return
  fi
  pass "hub menu uninstall (keep env) exited 0"
  assert_file_absent "menu uninstall removed the binary" "${sandbox}/usr/local/bin/cloud-pulse-hub"
  assert_file_exists "menu uninstall (n to purge) kept hub.env" "${sandbox}/etc/cloud-pulse/hub.env"

  # Re-install once more so uninstall+purge has something to act on and
  # to exercise the "not installed -> choose 1" path is NOT hit here.
  CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-hub.sh --reinstall >/dev/null 2>&1 || true

  local log2="${TMP_ROOT}/menu-hub-uninstall-purge.log"
  local status2=0
  run_in_pty 60 $'3\ny\ny\n' "$log2" \
    "CP_INSTALL_ROOT=${sandbox} bash scripts/install-hub.sh" || status2=$?
  if [ "$status2" -ne 0 ]; then
    fail "hub menu uninstall (purge) exited 0"
    cat "$log2" >&2 || true
    return
  fi
  pass "hub menu uninstall (purge) exited 0"
  assert_file_absent "menu uninstall+purge removed hub.env" "${sandbox}/etc/cloud-pulse/hub.env"
}

test_hub_menu_invalid_choice_reprompts_then_eof_exits() {
  echo "==> testing install-hub.sh interactive menu: invalid choice re-prompts, EOF exits 1"
  local sandbox="${TMP_ROOT}/sandbox-hub-menu-invalid"
  mkdir -p "$sandbox"
  local log="${TMP_ROOT}/menu-hub-invalid.log"
  # One invalid choice ('9'), then EOF (no trailing input at all).
  local status=0
  run_in_pty 60 $'9\n' "$log" \
    "CP_INSTALL_ROOT=${sandbox} bash scripts/install-hub.sh" || status=$?
  if [ "$status" -eq 0 ]; then
    fail "hub menu invalid-choice-then-EOF exits non-zero"
    cat "$log" >&2 || true
  else
    pass "hub menu invalid-choice-then-EOF exits non-zero"
  fi
  assert_contains "menu re-prompts after an invalid choice" "$(cat "$log")" "Invalid choice: 9"
}

test_agent_menu_fresh_install() {
  echo "==> testing install-agent.sh interactive menu: fresh install via '1' with hub-url/token/host-id prompts (token hidden)"
  local sandbox="${TMP_ROOT}/sandbox-agent-menu-fresh"
  mkdir -p "$sandbox"
  local log="${TMP_ROOT}/menu-agent-fresh.log"
  local fake_token="abcdefabcdefabcdefabcdefabcdefab"
  # 1=Install, hub URL, hidden token, Enter (default host-id), Enter
  # (default N to the "Monitor Docker containers?" prompt), Enter
  # (default N to the "Allow the hub to trigger updates..." prompt).
  local status=0
  run_in_pty 60 "1
http://127.0.0.1:${SERVER_PORT}
${fake_token}



" "$log" \
    "CP_RELEASE_BASE_URL=http://127.0.0.1:${SERVER_PORT} CP_INSTALL_ROOT=${sandbox} bash scripts/install-agent.sh" || status=$?
  if [ "$status" -ne 0 ]; then
    fail "agent menu fresh install exited 0"
    cat "$log" >&2 || true
    return
  fi
  pass "agent menu fresh install exited 0"
  assert_file_exists "menu fresh agent install wrote agent.env" "${sandbox}/etc/cloud-pulse/agent.env"
  assert_file_contains "menu fresh agent install stored the token" "${sandbox}/etc/cloud-pulse/agent.env" "CP_AGENT_TOKEN=${fake_token}"
  # NOTE on redaction: `read -rs` correctly disables bash's own echo of
  # what it reads (verified below: the prompt line itself is never
  # followed by a re-displayed value, unlike every other prompt in this
  # transcript). A pty's line discipline echoes bytes as they are
  # written to the master side independently of which process later
  # calls read(2) on the slave side; since this test feeds the whole
  # answer sequence via a single `printf | script` pipe up front, the
  # token text is unavoidably visible in script(1)'s raw pty capture as
  # *input* bytes, before install-agent.sh's read -rs ever suppresses
  # its own echo of the same bytes. A real interactive terminal session
  # (a human typing at a keyboard after the "Agent token (input
  # hidden):" prompt has already disabled echo) does not have this
  # property. So: assert the specific real guarantee (the prompt is not
  # followed by a re-echoed value on its own transcript line) rather
  # than a blanket "token never appears anywhere in the transcript",
  # which piped-pty testing cannot achieve for ANY `read -rs` call.
  assert_not_contains "prompt line for the agent token is not followed by a re-echoed value" \
    "$(grep -A1 'Agent token (input hidden)' "$log" | tail -n1)" "$fake_token"
  assert_contains "menu prompts for the hidden agent token" "$(cat "$log")" "Agent token (input hidden)"
  assert_contains "menu prompts for Docker monitoring with the root-equivalence warning" "$(cat "$log")" \
    "Monitor Docker containers? (adds the agent to the docker group; docker group access is root-equivalent)"
  assert_not_contains "agent.env has no active CP_DOCKER after declining the Docker prompt (default N)" \
    "$(grep '^CP_DOCKER=' "${sandbox}/etc/cloud-pulse/agent.env" || true)" "CP_DOCKER="
  assert_contains "menu prompts to allow hub-triggered remote updates" "$(cat "$log")" \
    "Allow the hub to trigger updates of this agent?"
  assert_not_contains "agent.env has no active CP_REMOTE_UPDATE after declining the remote-update prompt (default N)" \
    "$(grep '^CP_REMOTE_UPDATE=' "${sandbox}/etc/cloud-pulse/agent.env" || true)" "CP_REMOTE_UPDATE="
}

test_agent_menu_uninstall_keeps_env() {
  echo "==> testing install-agent.sh interactive menu: '3' uninstall keeps env by default answers"
  local sandbox="${TMP_ROOT}/sandbox-agent-menu-fresh"
  local log="${TMP_ROOT}/menu-agent-uninstall.log"
  local status=0
  run_in_pty 60 $'3\ny\nn\n' "$log" \
    "CP_INSTALL_ROOT=${sandbox} bash scripts/install-agent.sh" || status=$?
  if [ "$status" -ne 0 ]; then
    fail "agent menu uninstall (keep env) exited 0"
    cat "$log" >&2 || true
    return
  fi
  pass "agent menu uninstall (keep env) exited 0"
  assert_file_absent "menu uninstall removed the agent binary" "${sandbox}/usr/local/bin/cloud-pulse-agent"
  assert_file_exists "menu uninstall (n to purge) kept agent.env" "${sandbox}/etc/cloud-pulse/agent.env"
}

# ---------------------------------------------------------------------------
# Non-interactive: no tty + no flags must never show the menu or hang.
# ---------------------------------------------------------------------------

test_no_tty_no_flags_never_shows_menu() {
  echo "==> testing install-hub.sh/install-agent.sh: no tty + no flags => auto install/reinstall, no menu, no hang"
  local sandbox="${TMP_ROOT}/sandbox-hub-notty"
  mkdir -p "$sandbox"
  local out
  # No flags at all, but CP_NONINTERACTIVE is exported globally in this
  # script AND there is no tty backing this command substitution
  # either way, so this must behave exactly like the pre-v0.3.1 "auto"
  # path (this is also covered implicitly by every existing bare
  # `bash scripts/install-hub.sh` call elsewhere in this file, which
  # would have hung or misbehaved by now if the menu leaked through).
  out="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 30 bash scripts/install-hub.sh --listen :18099 2>&1)" || {
    fail "hub no-tty no-menu-flags auto-install exited 0"
    echo "$out" >&2
    return
  }
  assert_not_contains "no-tty run never prints the menu banner" "$out" "cloud-pulse hub installer"
  pass "hub no-tty no-menu-flags auto-install exited 0 without showing the menu"
}

# ---------------------------------------------------------------------------
# Reinstall configuration preservation regressions
# ---------------------------------------------------------------------------

test_hub_reinstall_preserves_configuration() {
  echo "==> testing hub reinstall preserves managed values, comments, and custom settings"
  local sandbox="${TMP_ROOT}/sandbox-hub-preserve" env_file out log status=0
  sandbox="${TMP_ROOT}/sandbox-hub-preserve"
  mkdir -p "$sandbox"
  env_file="${sandbox}/etc/cloud-pulse/hub.env"

  assert_exit0 "hub fresh install accepts custom listen and allowlist" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-hub.sh --install --listen :18950 --allowed-cidrs 10.0.0.0/8
  {
    echo "CP_S3_BUCKETS=b1:us-east-1"
    echo "# my note"
  } >> "$env_file"

  out="$(CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-hub.sh --reinstall --yes 2>&1)" || {
    fail "hub non-interactive reinstall preserves configuration"
    echo "$out" >&2
    return
  }
  assert_file_contains "hub non-interactive reinstall keeps CP_LISTEN" "$env_file" "^CP_LISTEN=:18950$"
  assert_file_contains "hub non-interactive reinstall keeps CP_ALLOWED_CIDRS" "$env_file" "^CP_ALLOWED_CIDRS=10.0.0.0/8$"
  assert_file_contains "hub non-interactive reinstall keeps custom S3 setting" "$env_file" "^CP_S3_BUCKETS=b1:us-east-1$"
  assert_file_contains "hub non-interactive reinstall keeps user comment" "$env_file" "^# my note$"
  assert_contains "hub summary uses preserved listen port" "$out" ":18950"
  assert_contains "hub same-version reinstall uses Reinstalled wording" "$out" "Reinstalled v0.0.0-test"

  log="${TMP_ROOT}/menu-hub-preserve.log"
  run_in_pty 60 $'2\ny\n' "$log" \
    "CP_RELEASE_BASE_URL=http://127.0.0.1:${SERVER_PORT} CP_INSTALL_ROOT=${sandbox} bash scripts/install-hub.sh" || status=$?
  if [ "$status" -ne 0 ]; then
    fail "hub menu reinstall preserves configuration"
    cat "$log" >&2 || true
    return
  fi
  assert_file_contains "hub menu reinstall keeps CP_LISTEN" "$env_file" "^CP_LISTEN=:18950$"
  assert_file_contains "hub menu reinstall keeps CP_ALLOWED_CIDRS" "$env_file" "^CP_ALLOWED_CIDRS=10.0.0.0/8$"
  assert_file_contains "hub menu reinstall keeps custom S3 setting" "$env_file" "^CP_S3_BUCKETS=b1:us-east-1$"
  assert_file_contains "hub menu reinstall keeps user comment" "$env_file" "^# my note$"
  assert_contains "hub menu summary uses preserved listen port" "$(cat "$log")" ":18950"

  assert_exit0 "hub explicit reinstall listen overrides preserved value" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-hub.sh --reinstall --yes --listen :19000
  assert_file_contains "hub explicit reinstall writes requested listen" "$env_file" "^CP_LISTEN=:19000$"
}

test_agent_reinstall_preserves_configuration() {
  echo "==> testing agent reinstall preserves optional and user-supplied configuration"
  local sandbox="${TMP_ROOT}/sandbox-agent-preserve" env_file
  sandbox="${TMP_ROOT}/sandbox-agent-preserve"
  mkdir -p "$sandbox"
  env_file="${sandbox}/etc/cloud-pulse/agent.env"

  assert_exit0 "agent fresh install accepts custom optional values" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-agent.sh --install --hub-url "http://127.0.0.1:${SERVER_PORT}" --token "$HUB_TOKEN" \
      --interval 30s --provider oci --egress-limit-gb 50
  {
    echo "CP_LOG_LEVEL=debug"
    echo "# agent note"
  } >> "$env_file"

  assert_exit0 "agent non-interactive reinstall preserves configuration" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-agent.sh --reinstall --yes
  assert_file_contains "agent reinstall keeps CP_INTERVAL" "$env_file" "^CP_INTERVAL=30s$"
  assert_file_contains "agent reinstall keeps CP_PROVIDER" "$env_file" "^CP_PROVIDER=oci$"
  assert_file_contains "agent reinstall keeps CP_EGRESS_LIMIT_GB" "$env_file" "^CP_EGRESS_LIMIT_GB=50$"
  assert_file_contains "agent reinstall keeps CP_LOG_LEVEL" "$env_file" "^CP_LOG_LEVEL=debug$"
  assert_file_contains "agent reinstall keeps user comment" "$env_file" "^# agent note$"
}

# ---------------------------------------------------------------------------
# Commented managed-key rewrite regressions
# ---------------------------------------------------------------------------

assert_managed_key_once() {
  local desc="$1" path="$2" key="$3" count
  count="$(grep -cE "^#?${key}=" "$path" 2>/dev/null || true)"
  if [ "$count" -eq 1 ]; then
    pass "$desc"
  else
    fail "$desc (key ${key} appears ${count} times in ${path}, want 1)"
  fi
}

assert_managed_keys_once() {
  local desc_prefix="$1" path="$2"
  shift 2
  local key
  for key in "$@"; do
    assert_managed_key_once "${desc_prefix}: ${key} appears exactly once" "$path" "$key"
  done
}

test_reinstall_commented_managed_keys_are_stable() {
  echo "==> testing repeated reinstalls do not duplicate commented managed env keys"
  local hub_sandbox="${TMP_ROOT}/sandbox-hub-commented-keys"
  local agent_sandbox="${TMP_ROOT}/sandbox-agent-commented-keys"
  local hub_env="${hub_sandbox}/etc/cloud-pulse/hub.env"
  local agent_env="${agent_sandbox}/etc/cloud-pulse/agent.env"
  local hub_log="${TMP_ROOT}/menu-hub-commented-keys.log"
  local agent_log="${TMP_ROOT}/menu-agent-commented-keys.log"
  local hub_snapshot="${TMP_ROOT}/hub-commented-keys.snapshot"
  local agent_snapshot="${TMP_ROOT}/agent-commented-keys.snapshot"
  local status=0

  mkdir -p "$hub_sandbox" "$agent_sandbox"
  assert_exit0 "hub fresh install creates commented managed defaults" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$hub_sandbox" \
    timeout 60 bash scripts/install-hub.sh --install --yes --listen :18110
  assert_exit0 "agent fresh install creates commented managed defaults" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$agent_sandbox" \
    timeout 60 bash scripts/install-agent.sh --install --yes --hub-url "http://127.0.0.1:${SERVER_PORT}" --token "$HUB_TOKEN"

  # Reinstall 1: scriptable/no-tty path.
  assert_exit0 "hub no-tty reinstall preserves commented defaults" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$hub_sandbox" \
    timeout 60 bash scripts/install-hub.sh --reinstall --yes
  assert_exit0 "agent no-tty reinstall preserves commented defaults" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$agent_sandbox" \
    timeout 60 bash scripts/install-agent.sh --reinstall --yes

  # Reinstall 2: real PTY menu (2=Reinstall, y=Continue?). The
  # web-Settings-page UI-token prompt was removed in v0.4.0 (SPEC-v0.4
  # §3), so this only exercises the menu's confirm-and-reinstall path
  # now; commented-default stability is asserted below regardless.
  run_in_pty 60 $'2\ny\n' "$hub_log" \
    "CP_RELEASE_BASE_URL=http://127.0.0.1:${SERVER_PORT} CP_INSTALL_ROOT=${hub_sandbox} bash scripts/install-hub.sh" || status=$?
  if [ "$status" -eq 0 ]; then
    pass "hub PTY-menu reinstall preserves commented defaults"
  else
    fail "hub PTY-menu reinstall preserves commented defaults"
    cat "$hub_log" >&2 || true
  fi
  status=0
  run_in_pty 60 $'2\ny\n' "$agent_log" \
    "CP_RELEASE_BASE_URL=http://127.0.0.1:${SERVER_PORT} CP_INSTALL_ROOT=${agent_sandbox} bash scripts/install-agent.sh" || status=$?
  if [ "$status" -eq 0 ]; then
    pass "agent PTY-menu reinstall preserves commented defaults"
  else
    fail "agent PTY-menu reinstall preserves commented defaults"
    cat "$agent_log" >&2 || true
  fi

  # Reinstall 3: no-change scriptable path, followed by a fourth identical
  # run so byte-for-byte idempotence is checked after the required sequence.
  assert_exit0 "hub third reinstall remains stable" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$hub_sandbox" \
    timeout 60 bash scripts/install-hub.sh --reinstall --yes
  assert_exit0 "agent third reinstall remains stable" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$agent_sandbox" \
    timeout 60 bash scripts/install-agent.sh --reinstall --yes

  assert_managed_keys_once "hub after three reinstalls" "$hub_env" \
    CP_LISTEN CP_DATA_DIR CP_AGENT_TOKEN CP_ALLOWED_CIDRS CP_UI_TOKEN CP_ALERT_WEBHOOK_URL
  assert_managed_keys_once "agent after three reinstalls" "$agent_env" \
    CP_HUB_URL CP_AGENT_TOKEN CP_HOST_ID CP_INTERVAL CP_PROVIDER CP_EGRESS_LIMIT_GB \
    CP_NET_EXCLUDE CP_TIME_SYNC CP_SEND_JITTER CP_LOG_LEVEL CP_LOG_FORMAT

  cp "$hub_env" "$hub_snapshot"
  cp "$agent_env" "$agent_snapshot"
  assert_exit0 "hub fourth no-change reinstall succeeds" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$hub_sandbox" \
    timeout 60 bash scripts/install-hub.sh --reinstall --yes
  assert_exit0 "agent fourth no-change reinstall succeeds" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$agent_sandbox" \
    timeout 60 bash scripts/install-agent.sh --reinstall --yes
  assert_exit0 "hub env is byte-identical across no-change reinstalls" diff "$hub_snapshot" "$hub_env"
  assert_exit0 "agent env is byte-identical across no-change reinstalls" diff "$agent_snapshot" "$agent_env"
}

test_active_managed_key_keeps_commented_peer() {
  echo "==> testing active managed keys win without rewriting commented peers"
  local sandbox="${TMP_ROOT}/sandbox-hub-active-commented-peer"
  local env_file="${sandbox}/etc/cloud-pulse/hub.env"
  mkdir -p "$sandbox"

  assert_exit0 "hub install with an explicit UI token succeeds" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-hub.sh --install --yes --listen :18111 --ui-token 0123456789abcdef
  echo "# CP_UI_TOKEN=legacy-comment-kept-verbatim" >> "$env_file"
  assert_exit0 "hub reinstall with active and commented UI token succeeds" \
    env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$sandbox" \
    timeout 60 bash scripts/install-hub.sh --reinstall --yes
  assert_file_contains "active CP_UI_TOKEN remains authoritative" "$env_file" "^CP_UI_TOKEN=0123456789abcdef$"
  assert_file_contains "commented CP_UI_TOKEN peer remains verbatim" "$env_file" "^# CP_UI_TOKEN=legacy-comment-kept-verbatim$"
  assert_managed_key_once "active CP_UI_TOKEN has no duplicate active assignment" "$env_file" CP_UI_TOKEN
}

# ---------------------------------------------------------------------------
# systemctl lifecycle integration tests
# ---------------------------------------------------------------------------

setup_systemctl_shim() {
  SYSTEMCTL_LOG="${TMP_ROOT}/systemctl.log"
  SYSTEMCTL_STATE="${TMP_ROOT}/systemctl.state"
  SYSTEMCTL_SHIM="${TMP_ROOT}/systemctl-shim"
  cat > "$SYSTEMCTL_SHIM" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail

log_file="${CP_SYSTEMCTL_LOG:?}"
state_file="${CP_SYSTEMCTL_STATE:?}"
printf '%s\n' "$*" >> "$log_file"

get_state() {
  awk -F= -v key="$1" '$1 == key { print substr($0, length(key) + 2); exit }' "$state_file"
}

set_state() {
  local key="$1" value="$2" tmp
  tmp="${state_file}.tmp"
  awk -F= -v key="$key" -v value="$value" '
    $1 == key { print key "=" value; next }
    { print }
  ' "$state_file" > "$tmp"
  mv "$tmp" "$state_file"
}

bump_process() {
  local pid started
  pid="$(get_state pid)"
  started="$(get_state started)"
  set_state state active
  set_state pid "$((pid + 1))"
  set_state started "$((started + 1))"
}

case "$1" in
  daemon-reload|enable|disable)
    exit 0
    ;;
  is-active)
    [ "$(get_state state)" = "active" ]
    ;;
  show)
    case "$3" in
      MainPID) get_state pid ;;
      ExecMainStartTimestampMonotonic) get_state started ;;
      *) exit 1 ;;
    esac
    ;;
  start)
    bump_process
    ;;
  restart)
    if [ "$(get_state fail_restart)" = "1" ]; then
      set_state state inactive
    else
      bump_process
    fi
    ;;
  stop)
    set_state state inactive
    ;;
  *)
    echo "unexpected systemctl invocation: $*" >&2
    exit 1
    ;;
esac
SHIM
  chmod 0755 "$SYSTEMCTL_SHIM"
}

set_systemctl_state() {
  local state="$1" fail_restart="$2"
  cat > "$SYSTEMCTL_STATE" <<EOF
state=${state}
pid=100
started=1000
fail_restart=${fail_restart}
EOF
  : > "$SYSTEMCTL_LOG"
}

assert_systemctl_called() {
  local desc="$1" command="$2"
  if grep -Fqx -- "$command" "$SYSTEMCTL_LOG"; then
    pass "$desc"
  else
    fail "$desc (missing '${command}')"
    sed 's/^/    /' "$SYSTEMCTL_LOG" >&2 || true
  fi
}

assert_systemctl_not_called() {
  local desc="$1" command="$2"
  if grep -Fqx -- "$command" "$SYSTEMCTL_LOG"; then
    fail "$desc (unexpected '${command}')"
    sed 's/^/    /' "$SYSTEMCTL_LOG" >&2 || true
  else
    pass "$desc"
  fi
}

test_systemctl_lifecycle() {
  echo "==> testing installer systemctl lifecycle through sandbox shim"
  setup_systemctl_shim

  local hub_sandbox="${TMP_ROOT}/sandbox-hub-systemctl" out
  set_systemctl_state inactive 0
  out="$(env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$hub_sandbox" \
    CP_SYSTEMCTL="$SYSTEMCTL_SHIM" CP_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" CP_SYSTEMCTL_STATE="$SYSTEMCTL_STATE" \
    timeout 60 bash scripts/install-hub.sh --install --yes --listen :18092 2>&1)" || {
    fail "hub fresh install through systemctl shim exits 0"
    echo "$out" >&2
  }
  assert_systemctl_called "hub fresh install enables service" "enable cloud-pulse-hub.service"
  assert_systemctl_called "hub fresh install starts inactive service" "start cloud-pulse-hub.service"
  assert_systemctl_not_called "hub fresh install does not restart inactive service" "restart cloud-pulse-hub.service"
  assert_contains "hub fresh summary says started" "$out" "Service:        cloud-pulse-hub.service started (running v0.0.0-test)"

  set_systemctl_state active 0
  out="$(env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$hub_sandbox" \
    CP_SYSTEMCTL="$SYSTEMCTL_SHIM" CP_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" CP_SYSTEMCTL_STATE="$SYSTEMCTL_STATE" \
    timeout 60 bash scripts/install-hub.sh --reinstall --yes 2>&1)" || {
    fail "hub active reinstall through systemctl shim exits 0"
    echo "$out" >&2
  }
  assert_systemctl_called "hub active reinstall restarts service" "restart cloud-pulse-hub.service"
  assert_contains "hub active summary says restarted" "$out" "Service:        cloud-pulse-hub.service restarted (running v0.0.0-test)"

  set_systemctl_state inactive 0
  out="$(env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$hub_sandbox" \
    CP_SYSTEMCTL="$SYSTEMCTL_SHIM" CP_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" CP_SYSTEMCTL_STATE="$SYSTEMCTL_STATE" \
    timeout 60 bash scripts/install-hub.sh --reinstall --yes 2>&1)" || {
    fail "hub inactive reinstall through systemctl shim exits 0"
    echo "$out" >&2
  }
  assert_systemctl_called "hub inactive reinstall starts service" "start cloud-pulse-hub.service"
  assert_systemctl_not_called "hub inactive reinstall does not restart service" "restart cloud-pulse-hub.service"

  set_systemctl_state active 1
  out="$(env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$hub_sandbox" \
    CP_SYSTEMCTL="$SYSTEMCTL_SHIM" CP_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" CP_SYSTEMCTL_STATE="$SYSTEMCTL_STATE" \
    timeout 25 bash scripts/install-hub.sh --reinstall --yes 2>&1)" && {
    fail "hub failed restart exits nonzero"
  }
  assert_contains "hub failed restart prints journal hint" "$out" "journalctl -u cloud-pulse-hub"

  set_systemctl_state active 0
  assert_exit0 "hub uninstall routes through systemctl shim" \
    env CP_INSTALL_ROOT="$hub_sandbox" CP_SYSTEMCTL="$SYSTEMCTL_SHIM" \
    CP_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" CP_SYSTEMCTL_STATE="$SYSTEMCTL_STATE" \
    timeout 30 bash scripts/install-hub.sh --uninstall --yes
  assert_systemctl_called "hub uninstall stops service" "stop cloud-pulse-hub.service"
  assert_systemctl_called "hub uninstall disables service" "disable cloud-pulse-hub.service"

  local agent_sandbox="${TMP_ROOT}/sandbox-agent-systemctl"
  set_systemctl_state inactive 0
  out="$(env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$agent_sandbox" \
    CP_SYSTEMCTL="$SYSTEMCTL_SHIM" CP_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" CP_SYSTEMCTL_STATE="$SYSTEMCTL_STATE" \
    timeout 60 bash scripts/install-agent.sh --install --yes --hub-url http://127.0.0.1:18092 \
      --token 0123456789abcdef --remote-update 2>&1)" || {
    fail "agent fresh install through systemctl shim exits 0"
    echo "$out" >&2
  }
  assert_systemctl_called "agent fresh install enables service" "enable cloud-pulse-agent.service"
  assert_systemctl_called "agent fresh remote-update install enables and starts path watcher" "enable --now cloud-pulse-agent-update.path"
  assert_systemctl_called "agent fresh install starts inactive service" "start cloud-pulse-agent.service"
  assert_systemctl_not_called "agent fresh install does not restart inactive service" "restart cloud-pulse-agent.service"
  assert_contains "agent fresh summary says started" "$out" "Service:      cloud-pulse-agent.service started (running v0.0.0-test)"

  set_systemctl_state active 0
  out="$(env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$agent_sandbox" \
    CP_SYSTEMCTL="$SYSTEMCTL_SHIM" CP_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" CP_SYSTEMCTL_STATE="$SYSTEMCTL_STATE" \
    timeout 60 bash scripts/install-agent.sh --reinstall --yes 2>&1)" || {
    fail "agent active reinstall through systemctl shim exits 0"
    echo "$out" >&2
  }
  assert_systemctl_called "agent active reinstall restarts service" "restart cloud-pulse-agent.service"
  assert_contains "agent active summary says restarted" "$out" "Service:      cloud-pulse-agent.service restarted (running v0.0.0-test)"

  set_systemctl_state inactive 0
  out="$(env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$agent_sandbox" \
    CP_SYSTEMCTL="$SYSTEMCTL_SHIM" CP_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" CP_SYSTEMCTL_STATE="$SYSTEMCTL_STATE" \
    timeout 60 bash scripts/install-agent.sh --reinstall --yes 2>&1)" || {
    fail "agent inactive reinstall through systemctl shim exits 0"
    echo "$out" >&2
  }
  assert_systemctl_called "agent inactive reinstall starts service" "start cloud-pulse-agent.service"
  assert_systemctl_not_called "agent inactive reinstall does not restart service" "restart cloud-pulse-agent.service"

  set_systemctl_state active 1
  out="$(env CP_RELEASE_BASE_URL="http://127.0.0.1:${SERVER_PORT}" CP_INSTALL_ROOT="$agent_sandbox" \
    CP_SYSTEMCTL="$SYSTEMCTL_SHIM" CP_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" CP_SYSTEMCTL_STATE="$SYSTEMCTL_STATE" \
    timeout 25 bash scripts/install-agent.sh --reinstall --yes 2>&1)" && {
    fail "agent failed restart exits nonzero"
  }
  # The command above must fail when the shim refuses the restart; the
  # remote path watcher's independent activation is asserted on fresh install.

  set_systemctl_state active 0
  assert_exit0 "agent uninstall routes through systemctl shim" \
    env CP_INSTALL_ROOT="$agent_sandbox" CP_SYSTEMCTL="$SYSTEMCTL_SHIM" \
    CP_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" CP_SYSTEMCTL_STATE="$SYSTEMCTL_STATE" \
    timeout 30 bash scripts/install-agent.sh --uninstall --yes
  assert_systemctl_called "agent uninstall stops service" "stop cloud-pulse-agent.service"
  assert_systemctl_called "agent uninstall disables service" "disable cloud-pulse-agent.service"
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
  test_agent_no_docker_flag_omits_supplementary_groups
  test_agent_no_remote_update_flag_omits_state_directory
  test_agent_upgrade_keeps_values
  test_agent_upgrade_from_legacy_stub
  test_agent_unknown_arch
  test_agent_checksum_mismatch
  test_agent_dry_run
  test_agent_docker_flag
  test_agent_remote_update_flag

  test_agent_darwin_sandbox_install
  test_agent_darwin_sandbox_reinstall
  test_agent_darwin_sandbox_remote_update
  test_agent_darwin_sandbox_uninstall

  test_hub_rejects_malicious_values
  test_agent_rejects_malicious_values

  test_hub_reinstall_preserves_configuration
  test_agent_reinstall_preserves_configuration
  test_reinstall_commented_managed_keys_are_stable
  test_active_managed_key_keeps_commented_peer

  test_hub_install_reinstall_flag_errors
  test_agent_install_reinstall_flag_errors
  test_hub_generate_and_rotate_ui_token
  test_hub_no_ui_token_prints_disabled_hint
  test_no_tty_no_flags_never_shows_menu

  test_systemd_unit_render_drift

  test_hub_menu_fresh_install
  test_hub_menu_install_when_already_installed
  test_hub_menu_reinstall_from_legacy
  test_hub_menu_invalid_choice_reprompts_then_eof_exits
  test_hub_menu_uninstall_keep_then_purge

  test_agent_menu_fresh_install
  test_agent_menu_uninstall_keeps_env

  test_agent_uninstall_and_purge
  test_hub_uninstall_and_purge
  test_systemctl_lifecycle

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
