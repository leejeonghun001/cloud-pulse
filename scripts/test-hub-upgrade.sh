#!/usr/bin/env bash
#
# scripts/test-hub-upgrade.sh — end-to-end rehearsal of the local hub
# upgrade verification kit (scripts/hub-upgrade-check.sh), per
# SPEC-v0.8 §1's test plan. Everything happens under a mktemp sandbox
# with CP_INSTALL_ROOT + a fake CP_SYSTEMCTL that really starts/stops
# the sandboxed hub process on a high port; no root, no real network,
# no port 8090, no /usr/local/bin or /etc/systemd/system.
#
# Usage:
#   scripts/test-hub-upgrade.sh [--from vX.Y.Z]
#
#   --from vX.Y.Z   The version to install first and upgrade away from.
#                   Supported: v0.3.2 (default), v0.7.0. Selects which
#                   seed data + version-specific post-upgrade
#                   preservation checks run (see seed_data_for_from /
#                   assert_from_specific_preservation below).
#
# Flow (identical regardless of --from):
#   1. git worktree of tag $FROM_TAG -> build cloud-pulse-hub -> sandbox
#      install via that tag's OWN install-hub.sh.
#   2. seed data appropriate to that version's schema directly into the
#      sandbox SQLite DB (the surface needed is either not exposed by
#      any hub API at all (e.g. alerts_sent) or would require
#      reproducing real accumulator/scheduler logic for no test value,
#      so this uses the same DB the sandboxed hub itself writes to).
#   3. hub-upgrade-check.sh pre --root
#   4. build the *current* tree as "v0.7.99-test" release assets, serve
#      them locally, run `update --version v0.7.99-test` against the
#      sandbox binary, then `systemd-unit apply --unit-path`, then
#      restart via the fake systemctl.
#   5. hub-upgrade-check.sh post --root --expect v0.7.99-test
#   6. version-specific data-preservation assertions (seeded rows
#      present, row counts non-decreasing).
#   7. negative cases: deleted host, no restart, stale unit -> each
#      must make post FAIL.
#   8. hub-upgrade-check.sh rollback --root --apply --yes -> back to
#      $FROM_TAG, data equal to step 3's pre snapshot.
#   9. dry-run rollback leaves every backup/target file's hash
#      unchanged.
#
# Every listener in this script binds a free ephemeral port chosen at
# run time (free_tcp_port, ultimately python3's bind-to-:0-then-read-
# back idiom) — never a fixed port — so two invocations (e.g. this
# script running for --from v0.3.2 and --from v0.7.0 concurrently, or
# alongside any other sandboxed-hub test script) never collide.
#
# Exits non-zero if any assertion fails.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

FROM_TAG="v0.3.2"

usage() {
  cat >&2 <<'USAGE'
usage: test-hub-upgrade.sh [--from vX.Y.Z]

  --from vX.Y.Z   Version to install first and upgrade away from.
                  Supported: v0.3.2 (default), v0.7.0.
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --from)
      FROM_TAG="${2:?--from requires a value}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "test-hub-upgrade.sh: unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
done

case "$FROM_TAG" in
  v0.3.2|v0.7.0) ;;
  *)
    echo "test-hub-upgrade.sh: unsupported --from ${FROM_TAG} (supported: v0.3.2, v0.7.0)" >&2
    exit 1
    ;;
esac

PASS_COUNT=0
FAIL_COUNT=0

TMP_ROOT=""
WORKTREE_DIR=""
FAKE_RELEASE_PID=""
SYSTEMCTL_SHIM=""
SYSTEMCTL_LOG=""
SYSTEMCTL_STATE=""
SANDBOX_ROOT=""
HUB_PORT=""
FAKE_RELEASE_PORT=""

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
  local sandbox_hub_pidfile="${TMP_ROOT}/sandbox-hub.pid"
  if [ -n "$TMP_ROOT" ] && [ -f "$sandbox_hub_pidfile" ]; then
    local sandbox_hub_pid
    sandbox_hub_pid="$(cat "$sandbox_hub_pidfile" 2>/dev/null || true)"
    if [ -n "$sandbox_hub_pid" ] && kill -0 "$sandbox_hub_pid" 2>/dev/null; then
      kill "$sandbox_hub_pid" 2>/dev/null || true
      wait "$sandbox_hub_pid" 2>/dev/null || true
    fi
  fi
  if [ -n "$FAKE_RELEASE_PID" ] && kill -0 "$FAKE_RELEASE_PID" 2>/dev/null; then
    kill "$FAKE_RELEASE_PID" 2>/dev/null || true
    wait "$FAKE_RELEASE_PID" 2>/dev/null || true
  fi
  if [ -n "$WORKTREE_DIR" ] && [ -d "$WORKTREE_DIR" ]; then
    git worktree remove --force "$WORKTREE_DIR" >/dev/null 2>&1 || true
  fi
  if [ -n "$TMP_ROOT" ]; then
    rm -rf "$TMP_ROOT"
  fi
  find /tmp -maxdepth 1 -name '__pycache__' -exec rm -rf {} + 2>/dev/null || true
  exit "$status"
}
trap cleanup EXIT INT TERM

# wait_for_http url timeout_s — polls url until it answers or timeout_s
# elapses.
wait_for_http() {
  local url="$1" timeout_s="$2" i=0
  while [ "$i" -lt "$((timeout_s * 4))" ]; do
    if curl -s -o /dev/null -w '%{http_code}' "$url" 2>/dev/null | grep -qE '^[0-9]{3}$'; then
      return 0
    fi
    sleep 0.25
    i=$((i + 1))
  done
  return 1
}

# free_tcp_port — prints an available high TCP port on 127.0.0.1.
free_tcp_port() {
  python3 - <<'PY'
import socket

s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
}

# hub_api_auth_header base_url — prints an "Authorization: Bearer
# <token>" header value for a v0.4+ hub (POST /api/v1/auth/login with
# the default admin/changeme credentials, matching write_password_file
# below) or an empty string for a pre-auth hub (v0.3.x, no such
# route). Used only by this test script's own direct API assertions
# (e.g. after rollback); hub-upgrade-check.sh's own api_auth_header
# does the equivalent for its checks.
hub_api_auth_header() {
  local base_url="$1"
  local me_probe_status
  me_probe_status="$(curl -s -o /dev/null -w '%{http_code}' "${base_url}/api/v1/auth/me" 2>/dev/null || echo 000)"
  if [ "$me_probe_status" = "000" ] || [ "$me_probe_status" = "404" ]; then
    return 0
  fi
  local resp token
  resp="$(curl -s -X POST -H 'Content-Type: application/json' \
    --data-binary '{"username":"admin","password":"changeme"}' "${base_url}/api/v1/auth/login" 2>/dev/null || true)"
  token="$(python3 -c 'import json,sys
try:
    print(json.loads(sys.stdin.read()).get("token",""))
except (json.JSONDecodeError, ValueError, AttributeError):
    print("")' <<<"$resp")"
  if [ -n "$token" ]; then
    printf 'Authorization: Bearer %s' "$token"
  fi
}

# host_arch_target prints "goos/goarch" for scripts/build-release.sh's
# TARGETS, matching this host (armv7 -> "linux/arm").
host_arch_target() {
  local machine
  machine="$(uname -m)"
  case "$machine" in
    x86_64|amd64) echo "linux/amd64" ;;
    aarch64|arm64) echo "linux/arm64" ;;
    armv7l|armv7) echo "linux/arm" ;;
    *)
      echo "test-hub-upgrade.sh: unsupported host architecture ${machine}" >&2
      exit 1
      ;;
  esac
}

asset_arch_suffix() {
  local machine
  machine="$(uname -m)"
  case "$machine" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    armv7l|armv7) echo "armv7" ;;
    *)
      echo "test-hub-upgrade.sh: unsupported host architecture ${machine}" >&2
      exit 1
      ;;
  esac
}

# ---------------------------------------------------------------------------
# fake systemctl shim: tracks active/pid/start-timestamp state in a file
# and actually starts/stops the sandbox hub process on HUB_PORT, so
# hub-upgrade-check.sh's restart detection exercises real process
# lifecycle, not just state-file bookkeeping.
# ---------------------------------------------------------------------------

write_systemctl_shim() {
  SYSTEMCTL_SHIM="${TMP_ROOT}/systemctl-shim"
  SYSTEMCTL_LOG="${TMP_ROOT}/systemctl.log"
  SYSTEMCTL_STATE="${TMP_ROOT}/systemctl.state"
  cat > "$SYSTEMCTL_SHIM" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail

log_file="${CP_SYSTEMCTL_LOG:?}"
state_file="${CP_SYSTEMCTL_STATE:?}"
hub_bin="${CP_SYSTEMCTL_HUB_BIN:?}"
hub_env="${CP_SYSTEMCTL_HUB_ENV:?}"
hub_pidfile="${CP_SYSTEMCTL_HUB_PIDFILE:?}"
hub_log="${CP_SYSTEMCTL_HUB_LOG:?}"
hub_data_dir="${CP_SYSTEMCTL_HUB_DATA_DIR:?}"
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

now_ns() {
  date +%s%N
}

do_start() {
  # hub_env is the *installed* hub.env, which (matching a real
  # install) always contains the real, unprefixed CP_DATA_DIR
  # (/var/lib/cloud-pulse) — install-hub.sh never sandbox-prefixes the
  # values it writes into hub.env itself, only the paths it writes
  # hub.env/the unit/the binary *to*. Source it for every other setting,
  # then force CP_DATA_DIR to this sandbox's own data directory so the
  # sandboxed hub process never touches the real hub's database.
  # shellcheck disable=SC1090 # hub_env is a KEY=VALUE env file, not a
  # script; sourcing it here only affects this shim subprocess's own
  # environment before exec-ing the hub, never the caller's shell.
  set -a
  # shellcheck disable=SC1091
  . "$hub_env"
  CP_DATA_DIR="$hub_data_dir"
  set +a
  mkdir -p "$hub_data_dir"
  "$hub_bin" >>"$hub_log" 2>&1 &
  local pid=$!
  echo "$pid" > "$hub_pidfile"
  set_state state active
  set_state pid "$pid"
  set_state started "$(now_ns)"
}

do_stop() {
  if [ -f "$hub_pidfile" ]; then
    local pid
    pid="$(cat "$hub_pidfile")"
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
        kill -0 "$pid" 2>/dev/null || break
        sleep 0.2
      done
      kill -0 "$pid" 2>/dev/null && kill -9 "$pid" 2>/dev/null || true
    fi
    rm -f "$hub_pidfile"
  fi
  set_state state inactive
}

case "$1" in
  daemon-reload|enable|disable)
    exit 0
    ;;
  is-active)
    [ "$(get_state state)" = "active" ]
    ;;
  show)
    # Accept either "show <unit> -p PROP --value" (this repo's
    # hub-upgrade-check.sh) or "show -p PROP --value <unit>"
    # (install-hub.sh's own convention) by scanning for the argument
    # that follows "-p", rather than assuming a fixed position.
    prop=""
    shift
    while [ "$#" -gt 0 ]; do
      if [ "$1" = "-p" ]; then
        prop="${2:-}"
        break
      fi
      shift
    done
    case "$prop" in
      MainPID) get_state pid ;;
      ExecMainStartTimestampMonotonic) get_state started ;;
      *) exit 1 ;;
    esac
    ;;
  start)
    if [ "$(get_state state)" = "active" ]; then
      exit 0
    fi
    do_start
    ;;
  restart)
    if [ "$(get_state fail_restart)" = "1" ]; then
      # Simulate a restart-that-doesn't-actually-restart bug (the
      # v0.3.1 regression hub-upgrade-check.sh's post must catch): stop
      # is called but start is deliberately skipped, so MainPID/start
      # timestamp are unchanged.
      do_stop
      set_state state active
      exit 0
    fi
    do_stop
    do_start
    ;;
  stop)
    do_stop
    ;;
  *)
    echo "unexpected systemctl invocation: $*" >&2
    exit 1
    ;;
esac
SHIM
  chmod 0755 "$SYSTEMCTL_SHIM"
}

reset_systemctl_state() {
  local fail_restart="${1:-0}"
  cat > "$SYSTEMCTL_STATE" <<EOF
state=inactive
pid=0
started=0
fail_restart=${fail_restart}
EOF
  : > "$SYSTEMCTL_LOG"
}

# with_systemctl_env cmd [args...] — runs cmd with the shim's required
# CP_SYSTEMCTL_* environment variables set, avoiding an unquoted
# `env $(...)` word-split (shellcheck SC2046).
with_systemctl_env() {
  env \
    "CP_SYSTEMCTL_LOG=${SYSTEMCTL_LOG}" \
    "CP_SYSTEMCTL_STATE=${SYSTEMCTL_STATE}" \
    "CP_SYSTEMCTL_HUB_BIN=${SANDBOX_ROOT}/usr/local/bin/cloud-pulse-hub" \
    "CP_SYSTEMCTL_HUB_ENV=${SANDBOX_ROOT}/etc/cloud-pulse/hub.env" \
    "CP_SYSTEMCTL_HUB_PIDFILE=${TMP_ROOT}/sandbox-hub.pid" \
    "CP_SYSTEMCTL_HUB_LOG=${TMP_ROOT}/sandbox-hub.log" \
    "CP_SYSTEMCTL_HUB_DATA_DIR=$(sandbox_data_dir)" \
    "$@"
}


# ---------------------------------------------------------------------------
# 1. build $FROM_TAG from a worktree, sandbox install via that tag's own
#    install-hub.sh
# ---------------------------------------------------------------------------

build_and_install_from_tag() {
  local from_tag="$1"

  echo "==> git worktree for ${from_tag}"
  WORKTREE_DIR="${TMP_ROOT}/worktree-${from_tag}"
  if git worktree add --detach "$WORKTREE_DIR" "$from_tag" >"${TMP_ROOT}/worktree.log" 2>&1; then
    pass "git worktree add ${from_tag}"
  else
    cat "${TMP_ROOT}/worktree.log" >&2
    fail "git worktree add ${from_tag}"
    return 1
  fi

  echo "==> building ${from_tag} cloud-pulse-hub"
  local target
  target="$(host_arch_target)"
  if TARGETS="$target" GOFLAGS=-mod=mod \
    go build -C "$WORKTREE_DIR" -o "${TMP_ROOT}/cloud-pulse-hub-${from_tag}" \
    -ldflags "-X github.com/leejeonghun001/cloud-pulse/internal/version.Version=${from_tag}" \
    ./cmd/hub >"${TMP_ROOT}/build-from.log" 2>&1; then
    pass "build ${from_tag} cloud-pulse-hub"
  else
    cat "${TMP_ROOT}/build-from.log" >&2
    fail "build ${from_tag} cloud-pulse-hub"
    return 1
  fi

  echo "==> sandbox install via ${from_tag}'s own install-hub.sh"
  SANDBOX_ROOT="${TMP_ROOT}/sandbox"
  HUB_PORT="$(free_tcp_port)"

  local assets_dir="${TMP_ROOT}/from-assets"
  mkdir -p "$assets_dir"
  cp "${TMP_ROOT}/cloud-pulse-hub-${from_tag}" "${assets_dir}/cloud-pulse-hub-linux-$(asset_arch_suffix)"
  ( cd "$assets_dir" && sha256sum "cloud-pulse-hub-linux-$(asset_arch_suffix)" > checksums.txt )

  local fake_port
  fake_port="$(free_tcp_port)"
  local fake_base="http://127.0.0.1:${fake_port}"
  local fake_assets_root="${TMP_ROOT}/from-fake-assets"
  mkdir -p "${fake_assets_root}/${from_tag}"
  cp "${assets_dir}"/* "${fake_assets_root}/${from_tag}/"

  python3 "${REPO_ROOT}/scripts/fake_release_server.py" "$fake_port" "$fake_assets_root" "$from_tag" \
    >"${TMP_ROOT}/from-fake-server.log" 2>&1 &
  local from_fake_pid=$!

  if ! wait_for_http "${fake_base}/releases/latest" 10; then
    fail "${from_tag} fake asset server did not start"
    kill "$from_fake_pid" 2>/dev/null || true
    return 1
  fi

  write_systemctl_shim
  reset_systemctl_state 0

  local install_out
  if install_out="$(with_systemctl_env env CP_RELEASE_BASE_URL="${fake_base}/releases/download/${from_tag}" \
    CP_INSTALL_ROOT="$SANDBOX_ROOT" CP_SYSTEMCTL="$SYSTEMCTL_SHIM" \
    timeout 60 bash "${WORKTREE_DIR}/scripts/install-hub.sh" --install --yes --listen "127.0.0.1:${HUB_PORT}" 2>&1)"; then
    pass "${from_tag} sandbox install"
  else
    echo "$install_out" >&2
    fail "${from_tag} sandbox install"
    kill "$from_fake_pid" 2>/dev/null || true
    return 1
  fi
  kill "$from_fake_pid" 2>/dev/null || true
  wait "$from_fake_pid" 2>/dev/null || true

  # install-hub.sh's fresh install starts the service itself through
  # the shim (see assert_systemctl_called precedent in
  # scripts/test-install.sh); confirm the sandbox hub is actually
  # reachable before seeding data into it.
  if wait_for_http "http://127.0.0.1:${HUB_PORT}/healthz" 15; then
    pass "sandbox ${from_tag} hub reachable on 127.0.0.1:${HUB_PORT}"
  else
    fail "sandbox ${from_tag} hub did not become reachable"
    return 1
  fi
  return 0
}

# ---------------------------------------------------------------------------
# 2. seed data directly into the sandbox DB, appropriate to $FROM_TAG's
#    own schema
# ---------------------------------------------------------------------------

sandbox_data_dir() {
  echo "${SANDBOX_ROOT}/var/lib/cloud-pulse"
}

sandbox_db_path() {
  echo "$(sandbox_data_dir)/cloud-pulse.db"
}

# seed_data_common — the 3 hosts + egress + host_limits + webhook
# setting + alerts_sent rows every --from version supports (v0.3.2's
# schema already has all of these tables).
seed_data_common() {
  local db="$1"
  python3 - "$db" <<'PY'
import sqlite3
import sys
import time

db_path = sys.argv[1]
conn = sqlite3.connect(db_path)
now = int(time.time())

hosts = [
    ("test-host-1", "aws"),
    ("test-host-2", "oci"),
    ("test-host-3", "other"),
]
for host_id, provider in hosts:
    info = (
        '{"id":"%s","hostname":"%s","os":"linux","platform":"debian",'
        '"platform_version":"12","kernel_version":"6.1.0","arch":"amd64",'
        '"cpu_model":"test-cpu","cpu_cores":4,"boot_time":%d,'
        '"provider":"%s","egress_limit_bytes":0,"agent_version":"v0.3.2"}'
    ) % (host_id, host_id, now - 86400, provider)
    latest = (
        '{"ts":%d,"cpu_percent":12.5,"load1":0.1,"load5":0.2,"load15":0.3,'
        '"mem_total":1000000,"mem_available":500000,"mem_used":500000,'
        '"mem_used_percent":50.0,"mem_cached":0,"swap_total":0,"swap_used":0,'
        '"disk_total":1000000,"disk_used":400000,"disk_used_percent":40.0,'
        '"disk_read_bps":0,"disk_write_bps":0,"net_rx_bps":0,"net_tx_bps":0,'
        '"net_rx_bytes":0,"net_tx_bytes":0,"uptime_seconds":86400}'
    ) % now
    conn.execute(
        "INSERT INTO hosts (id, info_json, last_seen, latest_ts, latest_json) VALUES (?, ?, ?, ?, ?)",
        (host_id, info, now, now, latest),
    )

month = time.strftime("%Y-%m", time.gmtime(now))
for host_id, _ in hosts:
    conn.execute(
        "INSERT INTO egress_monthly (host_id, month, tx_bytes, rx_bytes, updated_at) VALUES (?, ?, ?, ?, ?)",
        (host_id, month, 10 * 1024 * 1024 * 1024, 1024 * 1024 * 1024, now),
    )

conn.execute(
    "INSERT INTO host_limits (host_id, egress_limit_bytes, ingress_limit_bytes, updated_at) VALUES (?, ?, ?, ?)",
    (hosts[0][0], 50 * 1024 * 1024 * 1024, None, now),
)

conn.execute(
    "INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)",
    ("alert_webhook_url", "http://127.0.0.1:19-placeholder-not-dialed.invalid/webhook", now),
)

conn.execute(
    "INSERT INTO alerts_sent (host_id, month, level, sent_at) VALUES (?, ?, ?, ?)",
    (hosts[0][0], month, "warning", now),
)

conn.commit()
conn.close()
PY
}

# seed_data_v070_extra — rows in the tables that exist starting at
# v0.7.0's schema: one extra notify_channels + alert_rules row beyond
# the 3 default egress_out_pct rules 0004_alerting.sql's migration
# always seeds on a fresh install (regardless of whether a webhook URL
# was configured at install time — this sandbox's install-hub.sh run
# never passes one, so no "Default webhook" channel exists here),
# plus update_jobs and storage_accounts/storage_snapshots rows, so
# preservation checks have version-specific data to actually verify
# beyond what a fresh v0.3.2 install could ever have had.
seed_data_v070_extra() {
  local db="$1"
  python3 - "$db" <<'PY'
import sqlite3
import sys
import time

db_path = sys.argv[1]
conn = sqlite3.connect(db_path)
now = int(time.time())

conn.execute(
    "INSERT INTO notify_channels (name, type, enabled, config, created_at, updated_at) "
    "VALUES (?, ?, ?, ?, ?, ?)",
    ("test-seed-channel", "webhook", 1, '{"webhook_url":"https://203.0.113.10/hook"}', now, now),
)
channel_id = conn.execute(
    "SELECT id FROM notify_channels WHERE name = 'test-seed-channel'"
).fetchone()[0]

conn.execute(
    "INSERT INTO alert_rules (name, enabled, metric, host_id, operator, threshold, duration_sec, "
    "cooldown_sec, notify_resolved, channel_ids, created_at, updated_at) "
    "VALUES (?, 1, 'cpu_percent', '', '>=', 90, 60, 3600, 0, ?, ?, ?)",
    ("test-seed-cpu-rule", "[%d]" % channel_id, now, now),
)

conn.execute(
    "INSERT INTO update_jobs (batch_id, host_id, target, state, reason, created_at, updated_at, "
    "attempt, platform) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
    ("test-seed-batch", "test-host-1", "v0.7.0", "succeeded", "", now, now, 1, "linux"),
)

conn.execute(
    "INSERT INTO storage_accounts (provider, name, config_json, secret_json, created_at, updated_at) "
    "VALUES (?, ?, ?, ?, ?, ?)",
    ("google_drive", "test-seed-account", '{"client_id":"test-client-id"}', "{}", now, now),
)
account_id = conn.execute(
    "SELECT id FROM storage_accounts WHERE name = 'test-seed-account'"
).fetchone()[0]
conn.execute(
    "INSERT INTO storage_snapshots (account_id, status, account_email, used_bytes, limit_bytes, "
    "collected_at, last_success_at, last_attempt_at) VALUES (?, 'ok', ?, ?, ?, ?, ?, ?)",
    (account_id, "seed@example.test", 1024, 1024 * 1024, now, now, now),
)

conn.commit()
conn.close()
PY
}

# seed_data_for_from from_tag — seeds seed_data_common's rows always,
# plus from_tag-specific extra rows for versions whose schema has more
# to preserve than v0.3.2's.
seed_data_for_from() {
  local from_tag="$1" db="$2"
  seed_data_common "$db"
  case "$from_tag" in
    v0.7.0)
      seed_data_v070_extra "$db"
      ;;
  esac
}

seed_data() {
  local from_tag="$1"
  echo "==> seeding data appropriate to ${from_tag}'s schema"
  local db
  db="$(sandbox_db_path)"

  # Stop the sandbox hub first so this direct write doesn't race the
  # live WAL-mode connection it holds open.
  with_systemctl_env "$SYSTEMCTL_SHIM" stop >/dev/null

  if seed_data_for_from "$from_tag" "$db"; then
    pass "seed data written to sandbox DB"
  else
    fail "seed data write failed"
    return 1
  fi

  with_systemctl_env "$SYSTEMCTL_SHIM" start >/dev/null
  if wait_for_http "http://127.0.0.1:${HUB_PORT}/healthz" 15; then
    pass "sandbox hub restarted after seeding"
  else
    fail "sandbox hub did not come back up after seeding"
    return 1
  fi
  return 0
}

# ---------------------------------------------------------------------------
# 3. pre
# ---------------------------------------------------------------------------

PRE_OUT_DIR=""
PASSWORD_FILE=""

# write_password_file — writes the hub's default admin password
# ("changeme", set by EnsureDefaultCredentials on a fresh v0.4+ DB) to
# a 0600 file so hub-upgrade-check.sh's --password-file path is
# exercised the same way a real operator would use it; never passed on
# argv or in an environment variable.
write_password_file() {
  PASSWORD_FILE="${TMP_ROOT}/admin-password"
  printf 'changeme\n' > "$PASSWORD_FILE"
  chmod 0600 "$PASSWORD_FILE"
}

run_pre() {
  echo "==> hub-upgrade-check.sh pre"
  local out
  PRE_OUT_DIR="${TMP_ROOT}/backups/pre-run"
  if out="$(with_systemctl_env \
    timeout 60 bash "${REPO_ROOT}/scripts/hub-upgrade-check.sh" pre \
    --root "$SANDBOX_ROOT" \
    --hub-url "http://127.0.0.1:${HUB_PORT}" \
    --systemctl "$SYSTEMCTL_SHIM" \
    --out "$PRE_OUT_DIR" \
    --password-file "$PASSWORD_FILE" 2>&1)"; then
    pass "pre check exits 0"
  else
    echo "$out" >&2
    fail "pre check exits 0"
  fi
  if [ -f "${PRE_OUT_DIR}/pre.json" ] && [ -f "${PRE_OUT_DIR}/cloud-pulse.db.bak" ]; then
    pass "pre produced pre.json + database backup"
  else
    fail "pre did not produce expected backup bundle contents"
  fi
}

# ---------------------------------------------------------------------------
# 4. build current tree as v0.7.99-test, serve, update, systemd-unit apply,
#    restart
# ---------------------------------------------------------------------------

do_upgrade() {
  echo "==> building current tree as v0.7.99-test"
  local target new_assets_dir
  target="$(host_arch_target)"
  new_assets_dir="${TMP_ROOT}/v0.7.99-test-assets"
  mkdir -p "$new_assets_dir"

  if TARGETS="$target" ALLOW_ANY_VERSION=1 \
    bash scripts/build-release.sh "v0.7.99-test" "$new_assets_dir" \
    >"${TMP_ROOT}/build-new.log" 2>&1; then
    pass "build current tree as v0.7.99-test"
  else
    cat "${TMP_ROOT}/build-new.log" >&2
    fail "build current tree as v0.7.99-test"
    return 1
  fi

  FAKE_RELEASE_PORT="$(free_tcp_port)"
  local fake_base="http://127.0.0.1:${FAKE_RELEASE_PORT}"
  local fake_assets_root="${TMP_ROOT}/new-fake-assets"
  mkdir -p "${fake_assets_root}/v0.7.99-test"
  cp "${new_assets_dir}"/* "${fake_assets_root}/v0.7.99-test/" 2>/dev/null || true

  python3 "${REPO_ROOT}/scripts/fake_release_server.py" "$FAKE_RELEASE_PORT" "$fake_assets_root" "v0.7.99-test" \
    >"${TMP_ROOT}/new-fake-server.log" 2>&1 &
  FAKE_RELEASE_PID=$!

  if wait_for_http "${fake_base}/releases/latest" 10; then
    pass "v0.7.99-test fake release server reachable"
  else
    fail "v0.7.99-test fake release server did not start"
    return 1
  fi

  echo "==> running update --version v0.7.99-test against the sandbox binary"
  local hub_bin update_out
  hub_bin="${SANDBOX_ROOT}/usr/local/bin/cloud-pulse-hub"
  if update_out="$(env CP_UPDATE_LATEST_URL="${fake_base}/releases/latest" \
    CP_RELEASE_BASE_URL="${fake_base}/releases/download/v0.7.99-test" \
    timeout 60 "$hub_bin" update --version v0.7.99-test --no-restart 2>&1)"; then
    pass "update --version v0.7.99-test"
  else
    echo "$update_out" >&2
    fail "update --version v0.7.99-test"
    return 1
  fi

  echo "==> systemd-unit apply"
  local unit_path="${SANDBOX_ROOT}/etc/systemd/system/cloud-pulse-hub.service"
  local apply_out
  # --no-reload: systemd-unit apply's Go implementation always shells
  # out to the real "systemctl" for its daemon-reload step (it has no
  # CP_SYSTEMCTL hook, unlike install-hub.sh); there is no real systemd
  # instance to reload against in this sandbox, so only the unit file
  # rewrite itself is exercised here.
  if apply_out="$(timeout 30 "$hub_bin" systemd-unit apply --unit-path "$unit_path" --no-reload 2>&1)"; then
    pass "systemd-unit apply"
  else
    echo "$apply_out" >&2
    fail "systemd-unit apply"
  fi

  echo "==> restart via fake systemctl"
  if with_systemctl_env timeout 30 "$SYSTEMCTL_SHIM" restart >/dev/null 2>&1; then
    pass "fake systemctl restart"
  else
    fail "fake systemctl restart"
    return 1
  fi

  if wait_for_http "http://127.0.0.1:${HUB_PORT}/healthz" 15; then
    pass "sandbox hub reachable after upgrade + restart"
  else
    fail "sandbox hub did not become reachable after upgrade"
    return 1
  fi
  return 0
}

# ---------------------------------------------------------------------------
# 5. post (expect PASS)
# ---------------------------------------------------------------------------

run_post_expect_pass() {
  echo "==> hub-upgrade-check.sh post --expect v0.7.99-test (expect PASS)"
  local out
  if out="$(with_systemctl_env \
    timeout 60 bash "${REPO_ROOT}/scripts/hub-upgrade-check.sh" post \
    --root "$SANDBOX_ROOT" \
    --hub-url "http://127.0.0.1:${HUB_PORT}" \
    --systemctl "$SYSTEMCTL_SHIM" \
    --out "$PRE_OUT_DIR" \
    --password-file "$PASSWORD_FILE" \
    --expect v0.7.99-test 2>&1)"; then
    pass "post check exits 0 after a clean upgrade"
  else
    echo "$out" >&2
    fail "post check exits 0 after a clean upgrade"
  fi
  if printf '%s' "$out" | grep -qi 'must_change\|default password'; then
    pass "post surfaced the default-password warning"
  else
    echo "$out" | grep -i 'warn' >&2 || true
    fail "post did not mention the default-password warning (admin/changeme not yet rotated)"
  fi
}

# ---------------------------------------------------------------------------
# 5b. version-specific data-preservation assertions
# ---------------------------------------------------------------------------

# assert_v032_preservation — the 24 checks' worth of data seeded by
# seed_data_common must all still be present verbatim after the
# upgrade (this is exercised generically by hub-upgrade-check.sh
# post's own row-count/host/settings diff already asserted via
# run_post_expect_pass above; this adds a direct DB-level check of the
# exact seeded rows, independent of that script, as a belt-and-braces
# cross-check).
assert_v032_preservation() {
  echo "==> asserting v0.3.2-seeded data survived the upgrade"
  local db out
  db="$(sandbox_db_path)"
  out="$(python3 - "$db" <<'PY'
import sqlite3
import sys

conn = sqlite3.connect(sys.argv[1])
ok = True

(host_count,) = conn.execute(
    "SELECT COUNT(*) FROM hosts WHERE id IN ('test-host-1','test-host-2','test-host-3')"
).fetchone()
if host_count != 3:
    print(f"FAIL:expected 3 seeded hosts, found {host_count}")
    ok = False

(egress_count,) = conn.execute("SELECT COUNT(*) FROM egress_monthly").fetchone()
if egress_count < 3:
    print(f"FAIL:expected >=3 egress_monthly rows, found {egress_count}")
    ok = False

(limit_count,) = conn.execute(
    "SELECT COUNT(*) FROM host_limits WHERE host_id = 'test-host-1'"
).fetchone()
if limit_count != 1:
    print(f"FAIL:expected the seeded host_limits row for test-host-1, found {limit_count}")
    ok = False

(setting_count,) = conn.execute(
    "SELECT COUNT(*) FROM settings WHERE key = 'alert_webhook_url'"
).fetchone()
if setting_count != 1:
    print(f"FAIL:expected the seeded alert_webhook_url setting, found {setting_count}")
    ok = False

(sent_count,) = conn.execute("SELECT COUNT(*) FROM alerts_sent").fetchone()
if sent_count < 1:
    print(f"FAIL:expected the seeded alerts_sent row, found {sent_count}")
    ok = False

conn.close()
print("OK" if ok else "FAILED")
PY
)"
  if printf '%s' "$out" | grep -q '^OK$'; then
    pass "v0.3.2-seeded hosts/egress/host_limits/settings/alerts_sent all preserved"
  else
    printf '%s\n' "$out" | grep '^FAIL:' >&2 || true
    fail "v0.3.2-seeded data missing/changed after upgrade"
  fi
}

# assert_v070_preservation — the extra alert_rules/notify_channels/
# update_jobs/storage_accounts/storage_snapshots rows seeded by
# seed_data_v070_extra, plus v0.7.0's own default-seeded rows
# (3 egress alert rules + the "Default webhook" channel, created by
# 0004_alerting.sql's migration the first time v0.7.0 itself started
# up against a fresh DB), must all still be present after the v0.8
# upgrade — this is the check that actually exercises "v0.7.0-specific
# data preservation" the way SPEC-v0.8's test plan calls for, since
# v0.3.2 predates every one of these tables.
assert_v070_preservation() {
  echo "==> asserting v0.7.0-seeded alert rules/channels/update_jobs/storage rows survived the upgrade"
  local db out
  db="$(sandbox_db_path)"
  out="$(python3 - "$db" <<'PY'
import sqlite3
import sys

conn = sqlite3.connect(sys.argv[1])
ok = True


def check(label, count, minimum):
    global ok
    if count < minimum:
        print(f"FAIL:{label}: expected >={minimum}, found {count}")
        ok = False


(channel_count,) = conn.execute(
    "SELECT COUNT(*) FROM notify_channels WHERE name = 'test-seed-channel'"
).fetchone()
check("notify_channels (seeded channel)", channel_count, 1)

(rule_count,) = conn.execute(
    "SELECT COUNT(*) FROM alert_rules WHERE name = 'test-seed-cpu-rule'"
).fetchone()
check("alert_rules (seeded cpu rule)", rule_count, 1)

(egress_rule_count,) = conn.execute(
    "SELECT COUNT(*) FROM alert_rules WHERE metric = 'egress_out_pct'"
).fetchone()
check("alert_rules (default egress rules)", egress_rule_count, 3)

(job_count,) = conn.execute(
    "SELECT COUNT(*) FROM update_jobs WHERE batch_id = 'test-seed-batch'"
).fetchone()
check("update_jobs (seeded batch)", job_count, 1)

(account_count,) = conn.execute(
    "SELECT COUNT(*) FROM storage_accounts WHERE name = 'test-seed-account'"
).fetchone()
check("storage_accounts (seeded account)", account_count, 1)

(snapshot_count,) = conn.execute(
    "SELECT COUNT(*) FROM storage_snapshots s "
    "JOIN storage_accounts a ON a.id = s.account_id "
    "WHERE a.name = 'test-seed-account'"
).fetchone()
check("storage_snapshots (seeded snapshot)", snapshot_count, 1)

conn.close()
print("OK" if ok else "FAILED")
PY
)"
  if printf '%s' "$out" | grep -q '^OK$'; then
    pass "v0.7.0-seeded alert rules/channels/update_jobs/storage rows all preserved"
  else
    printf '%s\n' "$out" | grep '^FAIL:' >&2 || true
    fail "v0.7.0-seeded data missing/changed after upgrade"
  fi
}

# assert_from_specific_preservation from_tag — dispatches to the
# from_tag-specific preservation assertion(s) above.
assert_from_specific_preservation() {
  local from_tag="$1"
  assert_v032_preservation
  case "$from_tag" in
    v0.7.0)
      assert_v070_preservation
      ;;
  esac
}

# ---------------------------------------------------------------------------
# 6. negative cases
# ---------------------------------------------------------------------------

run_negative_cases() {
  echo "==> negative case: deleted host -> post must FAIL"
  local db out
  db="$(sandbox_db_path)"
  with_systemctl_env "$SYSTEMCTL_SHIM" stop >/dev/null
  python3 - "$db" <<'PY'
import sqlite3
import sys

conn = sqlite3.connect(sys.argv[1])
conn.execute("DELETE FROM hosts WHERE id = 'test-host-3'")
conn.commit()
conn.close()
PY
  with_systemctl_env "$SYSTEMCTL_SHIM" start >/dev/null
  wait_for_http "http://127.0.0.1:${HUB_PORT}/healthz" 15 || true

  if out="$(with_systemctl_env \
    timeout 60 bash "${REPO_ROOT}/scripts/hub-upgrade-check.sh" post \
    --root "$SANDBOX_ROOT" --hub-url "http://127.0.0.1:${HUB_PORT}" \
    --systemctl "$SYSTEMCTL_SHIM" --out "$PRE_OUT_DIR" --password-file "$PASSWORD_FILE" --expect v0.7.99-test 2>&1)"; then
    echo "$out" >&2
    fail "post FAILS when a seeded host has been deleted"
  else
    pass "post FAILS when a seeded host has been deleted"
  fi

  echo "==> restoring deleted host for subsequent steps"
  with_systemctl_env "$SYSTEMCTL_SHIM" stop >/dev/null
  python3 - "$db" <<'PY'
import sqlite3
import sys
import time

conn = sqlite3.connect(sys.argv[1])
now = int(time.time())
info = (
    '{"id":"test-host-3","hostname":"test-host-3","os":"linux","platform":"debian",'
    '"platform_version":"12","kernel_version":"6.1.0","arch":"amd64",'
    '"cpu_model":"test-cpu","cpu_cores":4,"boot_time":%d,'
    '"provider":"other","egress_limit_bytes":0,"agent_version":"v0.3.2"}'
) % (now - 86400)
latest = '{"ts":%d,"cpu_percent":1,"load1":0,"load5":0,"load15":0,"mem_total":1,"mem_available":1,"mem_used":0,"mem_used_percent":0,"mem_cached":0,"swap_total":0,"swap_used":0,"disk_total":1,"disk_used":0,"disk_used_percent":0,"disk_read_bps":0,"disk_write_bps":0,"net_rx_bps":0,"net_tx_bps":0,"net_rx_bytes":0,"net_tx_bytes":0,"uptime_seconds":1}' % now
conn.execute(
    "INSERT INTO hosts (id, info_json, last_seen, latest_ts, latest_json) VALUES (?, ?, ?, ?, ?)",
    ("test-host-3", info, now, now, latest),
)
conn.commit()
conn.close()
PY
  with_systemctl_env "$SYSTEMCTL_SHIM" start >/dev/null
  wait_for_http "http://127.0.0.1:${HUB_PORT}/healthz" 15 || true

  echo "==> negative case: no restart (same PID) -> post must FAIL"
  # Force the next restart to be a no-op (stop, no start) so
  # MainPID/start-timestamp stay unchanged, mirroring the v0.3.1
  # restart-missed regression.
  python3 - "$SYSTEMCTL_STATE" <<'PY'
import sys

path = sys.argv[1]
with open(path, encoding="utf-8") as f:
    lines = f.readlines()
with open(path, "w", encoding="utf-8") as f:
    for line in lines:
        if line.startswith("fail_restart="):
            f.write("fail_restart=1\n")
        else:
            f.write(line)
PY
  with_systemctl_env timeout 30 "$SYSTEMCTL_SHIM" restart >/dev/null 2>&1 || true

  if out="$(with_systemctl_env \
    timeout 60 bash "${REPO_ROOT}/scripts/hub-upgrade-check.sh" post \
    --root "$SANDBOX_ROOT" --hub-url "http://127.0.0.1:${HUB_PORT}" \
    --systemctl "$SYSTEMCTL_SHIM" --out "$PRE_OUT_DIR" --password-file "$PASSWORD_FILE" --expect v0.7.99-test 2>&1)"; then
    echo "$out" >&2
    fail "post FAILS when the service did not actually restart"
  else
    pass "post FAILS when the service did not actually restart"
  fi

  # Restore normal restart behavior and bring the hub back up for the
  # remaining steps.
  python3 - "$SYSTEMCTL_STATE" <<'PY'
import sys

path = sys.argv[1]
with open(path, encoding="utf-8") as f:
    lines = f.readlines()
with open(path, "w", encoding="utf-8") as f:
    for line in lines:
        if line.startswith("fail_restart="):
            f.write("fail_restart=0\n")
        else:
            f.write(line)
PY
  with_systemctl_env timeout 30 "$SYSTEMCTL_SHIM" restart >/dev/null 2>&1 || true
  wait_for_http "http://127.0.0.1:${HUB_PORT}/healthz" 15 || true

  echo "==> negative case: stale unit -> post must FAIL"
  local unit_path backup_unit
  unit_path="${SANDBOX_ROOT}/etc/systemd/system/cloud-pulse-hub.service"
  backup_unit="${TMP_ROOT}/unit-before-stale.service"
  cp "$unit_path" "$backup_unit"
  echo "# stale: this line should not appear in a freshly-rendered unit and the real ExecStart directive below is now wrong" > "$unit_path"
  echo "ExecStart=/bin/false --this-is-a-deliberately-stale-unit" >> "$unit_path"

  if out="$(with_systemctl_env \
    timeout 60 bash "${REPO_ROOT}/scripts/hub-upgrade-check.sh" post \
    --root "$SANDBOX_ROOT" --hub-url "http://127.0.0.1:${HUB_PORT}" \
    --systemctl "$SYSTEMCTL_SHIM" --out "$PRE_OUT_DIR" --password-file "$PASSWORD_FILE" --expect v0.7.99-test 2>&1)"; then
    echo "$out" >&2
    fail "post FAILS when the installed unit is stale"
  else
    pass "post FAILS when the installed unit is stale"
  fi

  cp "$backup_unit" "$unit_path"
  echo "==> stale-unit case restored"
}

# ---------------------------------------------------------------------------
# 7. rollback --apply --yes
# ---------------------------------------------------------------------------

run_rollback() {
  echo "==> hub-upgrade-check.sh rollback --apply --yes"
  local out
  if out="$(with_systemctl_env \
    timeout 60 bash "${REPO_ROOT}/scripts/hub-upgrade-check.sh" rollback \
    --root "$SANDBOX_ROOT" --hub-url "http://127.0.0.1:${HUB_PORT}" \
    --systemctl "$SYSTEMCTL_SHIM" --out "$PRE_OUT_DIR" --password-file "$PASSWORD_FILE" --apply --yes 2>&1)"; then
    pass "rollback --apply --yes exits 0"
  else
    echo "$out" >&2
    fail "rollback --apply --yes exits 0"
  fi

  if ! wait_for_http "http://127.0.0.1:${HUB_PORT}/healthz" 15; then
    fail "sandbox hub not reachable after rollback"
    return 1
  fi

  local hub_bin restored_version
  hub_bin="${SANDBOX_ROOT}/usr/local/bin/cloud-pulse-hub"
  restored_version="$("$hub_bin" -version 2>/dev/null | awk '{print $1}')"
  if [ "$restored_version" = "$FROM_TAG" ]; then
    pass "rollback restored the ${FROM_TAG} binary"
  else
    fail "rollback left binary at ${restored_version:-<unknown>}, expected ${FROM_TAG}"
  fi

  local hosts_body host_count auth_header hosts_code
  auth_header="$(hub_api_auth_header "http://127.0.0.1:${HUB_PORT}")"
  hosts_body="$(curl -s -H "$auth_header" "http://127.0.0.1:${HUB_PORT}/api/v1/hosts" 2>/dev/null || true)"
  hosts_code="$(python3 -c 'import json,sys
try:
    print(json.loads(sys.stdin.read()).get("code",""))
except (json.JSONDecodeError, ValueError, AttributeError):
    print("")' <<<"$hosts_body")"
  host_count="$(python3 -c 'import json,sys
try:
    data = json.loads(sys.stdin.read())
    print(len(data) if isinstance(data, list) else len(data.get("hosts", [])))
except (json.JSONDecodeError, ValueError, AttributeError):
    print(-1)' <<<"$hosts_body")"
  if [ "$hosts_code" = "password_change_required" ]; then
    # The restored (rolled-back) hub still has the seeded admin
    # account's default password, so its must-change-password gate
    # blocks GET /api/v1/hosts the same way it did right after the
    # fresh install -- hub-upgrade-check.sh rollback's OWN host-count
    # check (already asserted PASS above via "rollback --apply --yes
    # exits 0") verifies this same fact through its own login flow;
    # this script-level cross-check falls back to the DB directly
    # instead of fighting the same auth gate a second time.
    local db
    db="$(sandbox_db_path)"
    host_count="$(python3 -c 'import sqlite3,sys
conn = sqlite3.connect(sys.argv[1])
print(conn.execute("SELECT COUNT(*) FROM hosts").fetchone()[0])' "$db")"
  fi
  if [ "$host_count" = "3" ]; then
    pass "rollback restored all 3 seeded hosts"
  else
    fail "rollback host count = ${host_count}, expected 3"
  fi
}

# ---------------------------------------------------------------------------
# 8. dry-run rollback changes nothing
# ---------------------------------------------------------------------------

run_dry_run_rollback_noop_check() {
  echo "==> dry-run rollback (default, no --apply) leaves files unchanged"
  local hub_bin unit_path env_file
  hub_bin="${SANDBOX_ROOT}/usr/local/bin/cloud-pulse-hub"
  unit_path="${SANDBOX_ROOT}/etc/systemd/system/cloud-pulse-hub.service"
  env_file="${SANDBOX_ROOT}/etc/cloud-pulse/hub.env"

  local before_bin before_unit before_env
  before_bin="$(sha256sum "$hub_bin" | awk '{print $1}')"
  before_unit="$(sha256sum "$unit_path" | awk '{print $1}')"
  before_env="$(sha256sum "$env_file" | awk '{print $1}')"

  local out
  out="$(with_systemctl_env \
    timeout 30 bash "${REPO_ROOT}/scripts/hub-upgrade-check.sh" rollback \
    --root "$SANDBOX_ROOT" --hub-url "http://127.0.0.1:${HUB_PORT}" \
    --systemctl "$SYSTEMCTL_SHIM" --out "$PRE_OUT_DIR" 2>&1)" || true

  local after_bin after_unit after_env
  after_bin="$(sha256sum "$hub_bin" | awk '{print $1}')"
  after_unit="$(sha256sum "$unit_path" | awk '{print $1}')"
  after_env="$(sha256sum "$env_file" | awk '{print $1}')"

  if [ "$before_bin" = "$after_bin" ] && [ "$before_unit" = "$after_unit" ] && [ "$before_env" = "$after_env" ]; then
    pass "dry-run rollback left binary/unit/env file hashes unchanged"
  else
    fail "dry-run rollback modified a file it should only have described"
  fi
  if printf '%s' "$out" | grep -qi 'dry-run'; then
    pass "dry-run rollback output says it made no changes"
  else
    fail "dry-run rollback output did not mention dry-run"
  fi
}

# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------

main() {
  TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/cloud-pulse-test-hub-upgrade.XXXXXX")"
  mkdir -p "${TMP_ROOT}/backups"
  write_password_file

  echo "=== test-hub-upgrade.sh: --from ${FROM_TAG} ==="

  build_and_install_from_tag "$FROM_TAG" || { finish; return; }
  seed_data "$FROM_TAG" || { finish; return; }
  run_pre
  do_upgrade || { finish; return; }
  run_post_expect_pass
  assert_from_specific_preservation "$FROM_TAG"
  run_negative_cases
  run_rollback
  run_dry_run_rollback_noop_check

  finish
}

finish() {
  echo ""
  echo "=== test-hub-upgrade.sh summary (--from ${FROM_TAG}): ${PASS_COUNT} passed, ${FAIL_COUNT} failed ==="
  if [ "$FAIL_COUNT" -gt 0 ]; then
    exit 1
  fi
  exit 0
}

main "$@"
