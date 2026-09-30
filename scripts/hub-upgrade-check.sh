#!/usr/bin/env bash
#
# scripts/hub-upgrade-check.sh — local hub upgrade verification kit
# (SPEC-v0.8 §1). Works against any Linux+systemd cloud-pulse-hub
# install, old or new, real or sandboxed:
#
#   pre       collect service/binary/unit/env facts, take an online
#             SQLite backup + integrity check, snapshot schema/table
#             row counts and API state, save rollback assets.
#   post      re-collect the same facts after an upgrade and diff
#             against pre.json: service restarted, expected version,
#             migrations are a superset (up to the current binary's own
#             max), no data loss, default egress rules present, auth
#             reachable, dashboard/API 200s.
#   rollback  (dry-run by default) stop the service, move aside the
#             current DB, restore the backed-up DB/binary/unit, reload
#             systemd, start, and verify version + host count.
#
# Usage:
#   sudo scripts/hub-upgrade-check.sh pre    [options]
#   sudo scripts/hub-upgrade-check.sh post   --expect vX.Y.Z [options]
#   sudo scripts/hub-upgrade-check.sh rollback [--apply [--yes]] [options]
#
# Common options:
#   --root DIR          Sandbox prefix; every system path below is
#                        joined under DIR instead of the real
#                        filesystem root (also skips the "must be root"
#                        check). Used by scripts/test-hub-upgrade.sh;
#                        never set this against the real hub.
#   --data-dir DIR       Override the hub's data directory
#                        (default: /var/lib/cloud-pulse, under --root).
#   --env-file FILE      Override hub.env path
#                        (default: /etc/cloud-pulse/hub.env, under --root).
#   --unit-path FILE     Override the systemd unit path
#                        (default: /etc/systemd/system/cloud-pulse-hub.service,
#                        under --root).
#   --bin-path FILE      Override the installed binary path
#                        (default: /usr/local/bin/cloud-pulse-hub,
#                        under --root).
#   --hub-url URL        Override the base URL used for API/dashboard
#                        checks (default: derived from hub.env's
#                        CP_LISTEN; a wildcard host resolves to
#                        127.0.0.1).
#   --systemctl CMD      Override the systemctl command (test hook).
#   --password-file FILE Read the dashboard admin password from FILE
#                        (must be mode 0600). Never pass the password on
#                        the command line.
#   --out DIR            pre: where to write the backup bundle. Default
#                        /var/lib/cloud-pulse/upgrade-backups/<UTC>-<ver>/
#                        (under --root), created root:root 0700.
#                        post/rollback: where to read pre.json/backup
#                        from (default: the most recent directory under
#                        the default --out parent).
#
# pre-only:
#   (no extra flags)
#
# post-only:
#   --expect vX.Y.Z      Required. The version the hub must report after
#                        the upgrade.
#
# rollback-only:
#   --apply               Actually perform the rollback (default is a
#                        dry-run that only prints the planned commands).
#   --yes                 Skip the interactive confirmation prompt when
#                        --apply is given (needed for non-interactive/CI
#                        use).
#
# Secrets: hub.env's token values (CP_AGENT_TOKEN/CP_UI_TOKEN) are never
# printed, only whether they are set. The dashboard password is read
# only from --password-file (0600-checked) or an interactive /dev/tty
# prompt, never from argv or an environment variable.
#
# Exit codes: 0 all checks passed, 1 one or more checks failed or a
# usage/environment error occurred.

set -euo pipefail

# ---------------------------------------------------------------------------
# globals populated by parse_common_args / resolved by resolve_paths
# ---------------------------------------------------------------------------

SANDBOX_ROOT=""
DATA_DIR_OVERRIDE=""
ENV_FILE_OVERRIDE=""
UNIT_PATH_OVERRIDE=""
BIN_PATH_OVERRIDE=""
HUB_URL_OVERRIDE=""
SYSTEMCTL="${CP_SYSTEMCTL:-systemctl}"
PASSWORD_FILE=""
OUT_DIR_OVERRIDE=""
EXPECT_VERSION=""
ROLLBACK_APPLY=0
ROLLBACK_YES=0

DATA_DIR=""
ENV_FILE=""
UNIT_PATH=""
BIN_PATH=""

PASS_COUNT=0
FAIL_COUNT=0
WARN_COUNT=0

pass() {
  echo "PASS: $*"
  PASS_COUNT=$((PASS_COUNT + 1))
}

fail() {
  echo "FAIL: $*" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
}

warn() {
  echo "WARN: $*" >&2
  WARN_COUNT=$((WARN_COUNT + 1))
}

log() {
  echo "$*"
}

err() {
  echo "hub-upgrade-check: $*" >&2
}

# join_root prefix suffix — joins suffix (an absolute path) under
# prefix when prefix is non-empty (sandbox mode), otherwise returns
# suffix unchanged.
join_root() {
  local prefix="$1" suffix="$2"
  if [ -z "$prefix" ]; then
    printf '%s' "$suffix"
    return
  fi
  printf '%s%s' "${prefix%/}" "$suffix"
}

usage() {
  cat >&2 <<'USAGE'
usage: hub-upgrade-check.sh pre [--root DIR] [--data-dir DIR] [--env-file FILE]
         [--unit-path FILE] [--bin-path FILE] [--hub-url URL]
         [--systemctl CMD] [--password-file FILE] [--out DIR]
       hub-upgrade-check.sh post --expect vX.Y.Z [same options as pre]
       hub-upgrade-check.sh rollback [--apply [--yes]] [same options as pre]
USAGE
}

parse_common_args() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --root)
        SANDBOX_ROOT="${2:?--root requires a value}"
        shift 2
        ;;
      --data-dir)
        DATA_DIR_OVERRIDE="${2:?--data-dir requires a value}"
        shift 2
        ;;
      --env-file)
        ENV_FILE_OVERRIDE="${2:?--env-file requires a value}"
        shift 2
        ;;
      --unit-path)
        UNIT_PATH_OVERRIDE="${2:?--unit-path requires a value}"
        shift 2
        ;;
      --bin-path)
        BIN_PATH_OVERRIDE="${2:?--bin-path requires a value}"
        shift 2
        ;;
      --hub-url)
        HUB_URL_OVERRIDE="${2:?--hub-url requires a value}"
        shift 2
        ;;
      --systemctl)
        SYSTEMCTL="${2:?--systemctl requires a value}"
        shift 2
        ;;
      --password-file)
        PASSWORD_FILE="${2:?--password-file requires a value}"
        shift 2
        ;;
      --out)
        OUT_DIR_OVERRIDE="${2:?--out requires a value}"
        shift 2
        ;;
      --expect)
        EXPECT_VERSION="${2:?--expect requires a value}"
        shift 2
        ;;
      --apply)
        ROLLBACK_APPLY=1
        shift
        ;;
      --yes)
        ROLLBACK_YES=1
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        err "unknown argument: $1"
        usage
        exit 1
        ;;
    esac
  done
}

# resolve_paths fills DATA_DIR/ENV_FILE/UNIT_PATH/BIN_PATH from overrides
# or defaults, joined under SANDBOX_ROOT when set.
resolve_paths() {
  DATA_DIR="${DATA_DIR_OVERRIDE:-$(join_root "$SANDBOX_ROOT" /var/lib/cloud-pulse)}"
  ENV_FILE="${ENV_FILE_OVERRIDE:-$(join_root "$SANDBOX_ROOT" /etc/cloud-pulse/hub.env)}"
  UNIT_PATH="${UNIT_PATH_OVERRIDE:-$(join_root "$SANDBOX_ROOT" /etc/systemd/system/cloud-pulse-hub.service)}"
  BIN_PATH="${BIN_PATH_OVERRIDE:-$(join_root "$SANDBOX_ROOT" /usr/local/bin/cloud-pulse-hub)}"
}

require_root_or_sandbox() {
  if [ -n "$SANDBOX_ROOT" ]; then
    return 0
  fi
  if [ "$(id -u)" -ne 0 ]; then
    err "this script must be run as root (use sudo). For local testing without root, use --root DIR to enable sandbox mode."
    exit 1
  fi
}

# env_value_of key file — prints the value assigned to an *active*
# (non-commented) KEY=VALUE line in file, or "" if absent. Only the
# last active assignment wins, matching hub config-loading precedent.
env_value_of() {
  local key="$1" file="$2"
  if [ ! -f "$file" ]; then
    return 0
  fi
  awk -F= -v key="$key" '
    $0 ~ "^" key "=" { val = substr($0, length(key) + 2); found = 1 }
    END { if (found) print val }
  ' "$file"
}

# env_key_is_set key file — reports (exit 0/1) whether an active
# KEY=VALUE line with a non-empty value exists in file, without ever
# printing the value.
env_key_is_set() {
  local key="$1" file="$2"
  local val
  val="$(env_value_of "$key" "$file")"
  [ -n "$val" ]
}

# env_active_keys file — lists the key names of every active
# (non-commented, non-blank) KEY=VALUE line in file, one per line,
# never their values.
env_active_keys() {
  local file="$1"
  if [ ! -f "$file" ]; then
    return 0
  fi
  grep -E '^[A-Za-z_][A-Za-z0-9_]*=' "$file" | cut -d= -f1 | sort -u
}

# hub_url_from_env — derives the base URL for API/dashboard checks from
# CP_LISTEN in ENV_FILE, or the --hub-url override if given. A
# wildcard/empty host resolves to 127.0.0.1. Defaults to :8090 when
# CP_LISTEN is entirely absent.
hub_url_from_env() {
  if [ -n "$HUB_URL_OVERRIDE" ]; then
    printf '%s' "$HUB_URL_OVERRIDE"
    return
  fi
  local listen host port
  listen="$(env_value_of CP_LISTEN "$ENV_FILE")"
  if [ -z "$listen" ]; then
    listen=":8090"
  fi
  # listen is "[host]:port", "host:port", or ":port"; take the first
  # comma-separated entry only (multi-listen hubs are checked via their
  # first address).
  listen="${listen%%,*}"
  port="${listen##*:}"
  host="${listen%:*}"
  case "$host" in
    ""|"*"|"0.0.0.0"|"[::]")
      host="127.0.0.1"
      ;;
  esac
  printf 'http://%s:%s' "$host" "$port"
}

# ---------------------------------------------------------------------------
# systemctl helpers (test-hookable via --systemctl / CP_SYSTEMCTL)
# ---------------------------------------------------------------------------

systemctl_is_active() {
  "$SYSTEMCTL" is-active cloud-pulse-hub.service >/dev/null 2>&1
}

systemctl_show() {
  local prop="$1"
  "$SYSTEMCTL" show cloud-pulse-hub.service -p "$prop" --value 2>/dev/null || true
}

systemctl_main_pid() {
  systemctl_show MainPID
}

systemctl_start_timestamp() {
  # ExecMainStartTimestampMonotonic is immune to wall-clock jumps and
  # changes on every real restart, matching the fake shim in
  # scripts/test-install.sh's setup_systemctl_shim.
  systemctl_show ExecMainStartTimestampMonotonic
}

# ---------------------------------------------------------------------------
# secrets: password acquisition (never argv, never printed)
# ---------------------------------------------------------------------------

# read_password — reads the dashboard admin password from
# PASSWORD_FILE (0600-checked) if set, else prompts on /dev/tty. Prints
# nothing but the password itself to stdout; callers must not echo it.
read_password() {
  if [ -n "$PASSWORD_FILE" ]; then
    local mode
    if [ ! -f "$PASSWORD_FILE" ]; then
      err "--password-file ${PASSWORD_FILE} does not exist"
      return 1
    fi
    mode="$(stat -c '%a' "$PASSWORD_FILE" 2>/dev/null || stat -f '%Lp' "$PASSWORD_FILE" 2>/dev/null || true)"
    if [ "$mode" != "600" ]; then
      err "--password-file ${PASSWORD_FILE} must be mode 0600 (found ${mode:-unknown})"
      return 1
    fi
    head -n1 "$PASSWORD_FILE"
    return 0
  fi
  if ! : 2>/dev/null < /dev/tty > /dev/tty; then
    err "no --password-file given and /dev/tty is unavailable for an interactive prompt"
    return 1
  fi
  local pw=""
  if ! read -r -s -p "Dashboard admin password: " pw < /dev/tty > /dev/tty 2>&1; then
    err "could not read the dashboard admin password from /dev/tty"
    return 1
  fi
  printf '\n' > /dev/tty
  printf '%s' "$pw"
}

# ---------------------------------------------------------------------------
# python3 helpers — sqlite backup/integrity/snapshot/compare. Every
# heredoc receives its paths as argv (sys.argv), never string-
# interpolated into the script body, per CODING_CONVENTIONS.md.
# ---------------------------------------------------------------------------

# py_sqlite_backup src_db dest_db — online backup via
# sqlite3.Connection.backup, safe to run against a live WAL-mode
# database.
py_sqlite_backup() {
  local src="$1" dest="$2"
  python3 - "$src" "$dest" <<'PY'
import sqlite3
import sys

src_path, dest_path = sys.argv[1], sys.argv[2]
src = sqlite3.connect(src_path)
dest = sqlite3.connect(dest_path)
try:
    src.backup(dest)
finally:
    dest.close()
    src.close()
PY
}

# py_sqlite_integrity_check db — prints "ok" on stdout iff PRAGMA
# integrity_check reports exactly "ok", else prints the first problem
# line and exits 1.
py_sqlite_integrity_check() {
  local db="$1"
  python3 - "$db" <<'PY'
import sqlite3
import sys

db_path = sys.argv[1]
conn = sqlite3.connect(db_path)
try:
    rows = conn.execute("PRAGMA integrity_check").fetchall()
finally:
    conn.close()
if len(rows) == 1 and rows[0][0] == "ok":
    print("ok")
    sys.exit(0)
for row in rows:
    print(row[0])
sys.exit(1)
PY
}

# py_sqlite_snapshot db out_json — writes a JSON snapshot of
# schema_migrations versions, per-table row counts (existing tables
# only), host IDs, and settings keys to out_json.
py_sqlite_snapshot() {
  local db="$1" out_json="$2"
  python3 - "$db" "$out_json" <<'PY'
import json
import sqlite3
import sys

db_path, out_path = sys.argv[1], sys.argv[2]
conn = sqlite3.connect(db_path)
conn.row_factory = sqlite3.Row

CANDIDATE_TABLES = [
    "hosts", "metrics_raw", "metrics_5m", "metrics_1h", "egress_monthly",
    "bucket_stats", "alerts_sent", "host_limits", "settings", "sessions",
    "alert_rules", "notify_channels", "alert_state", "alert_events",
    "host_inventory", "update_jobs", "pricing_plans", "host_pricing",
    "cloud_cost_snapshots", "audit_log", "storage_accounts", "storage_snapshots",
]


def existing_tables(conn: sqlite3.Connection) -> set[str]:
    rows = conn.execute("SELECT name FROM sqlite_master WHERE type='table'").fetchall()
    return {row[0] for row in rows}


def main() -> int:
    present = existing_tables(conn)

    migrations: list[str] = []
    if "schema_migrations" in present:
        migrations = [
            str(row[0])
            for row in conn.execute("SELECT version FROM schema_migrations ORDER BY version").fetchall()
        ]

    counts: dict[str, int] = {}
    for table in CANDIDATE_TABLES:
        if table not in present:
            continue
        (count,) = conn.execute(f"SELECT COUNT(*) FROM {table}").fetchone()  # noqa: S608 - table from static allowlist
        counts[table] = int(count)

    host_ids: list[str] = []
    if "hosts" in present:
        host_ids = sorted(str(row[0]) for row in conn.execute("SELECT id FROM hosts").fetchall())

    setting_keys: list[str] = []
    if "settings" in present:
        setting_keys = sorted(str(row[0]) for row in conn.execute("SELECT key FROM settings").fetchall())

    egress_rule_names: list[str] = []
    if "alert_rules" in present:
        egress_rule_names = sorted(
            str(row[0])
            for row in conn.execute(
                "SELECT name FROM alert_rules WHERE metric = 'egress_out_pct'"
            ).fetchall()
        )

    snapshot = {
        "tables_present": sorted(present),
        "migrations": migrations,
        "row_counts": counts,
        "host_ids": host_ids,
        "setting_keys": setting_keys,
        "egress_rule_names": egress_rule_names,
    }
    with open(out_path, "w", encoding="utf-8") as f:
        json.dump(snapshot, f, indent=2, sort_keys=True)
        f.write("\n")
    return 0


sys.exit(main())
PY
}

# ---------------------------------------------------------------------------
# HTTP/API helpers
# ---------------------------------------------------------------------------

# curl_status url [auth_header] — prints the numeric HTTP status only.
curl_status() {
  local url="$1" auth_header="${2:-}"
  if [ -n "$auth_header" ]; then
    curl -s -o /dev/null -w '%{http_code}' -H "$auth_header" "$url" 2>/dev/null || echo "000"
  else
    curl -s -o /dev/null -w '%{http_code}' "$url" 2>/dev/null || echo "000"
  fi
}

# curl_body url [auth_header] — prints the response body only.
curl_body() {
  local url="$1" auth_header="${2:-}"
  if [ -n "$auth_header" ]; then
    curl -s -H "$auth_header" "$url" 2>/dev/null || true
  else
    curl -s "$url" 2>/dev/null || true
  fi
}

# json_field body key — extracts a top-level string/number/bool field
# from a small flat JSON body using python3's stdlib json module,
# avoiding a jq dependency. Prints "" if the key is absent or the body
# does not parse.
json_field() {
  local body="$1" key="$2"
  python3 - "$key" "$body" <<'PY' 2>/dev/null || true
import json
import sys

key, raw = sys.argv[1], sys.argv[2]
try:
    data = json.loads(raw)
except (json.JSONDecodeError, ValueError):
    print("")
    sys.exit(0)
val = data.get(key, "")
if isinstance(val, bool):
    print("true" if val else "false")
else:
    print(val)
PY
}

# api_auth_header prints an "Authorization: Bearer <token>" header
# value to use for authenticated GETs: CP_UI_TOKEN from hub.env if set,
# else a freshly logged-in session token via --password-file/prompt
# (v0.4+ hubs), else "" (pre-auth v0.3.x hub with no CP_UI_TOKEN).
# Prints diagnostics (never the token) to stderr.
api_auth_header() {
  local base_url="$1"
  local ui_token
  ui_token="$(env_value_of CP_UI_TOKEN "$ENV_FILE")"
  if [ -n "$ui_token" ]; then
    echo "using CP_UI_TOKEN from hub.env for API auth" >&2
    printf 'Authorization: Bearer %s' "$ui_token"
    return 0
  fi

  # POST /api/v1/auth/login only exists (as a route) from v0.4+; a GET
  # probe against it would always 404 (method mismatch) regardless of
  # version, so probe GET /api/v1/auth/me instead: absent entirely on
  # v0.3.x (404, no such route), 401 on v0.4+ (route exists, no
  # session yet).
  local me_probe_status
  me_probe_status="$(curl_status "${base_url}/api/v1/auth/me")"
  if [ "$me_probe_status" = "000" ] || [ "$me_probe_status" = "404" ]; then
    echo "no dashboard auth routes reachable; assuming a pre-auth hub (v0.3.x)" >&2
    return 0
  fi

  local password
  if ! password="$(read_password)"; then
    echo "could not obtain a dashboard password; API checks will be unauthenticated" >&2
    return 0
  fi

  local resp token must_change login_body
  login_body="$(python3 -c 'import json,sys; print(json.dumps({"username":"admin","password":sys.stdin.read().rstrip(chr(10))}))' <<<"$password")"
  resp="$(curl -s -X POST -H 'Content-Type: application/json' \
    --data-binary "$login_body" "${base_url}/api/v1/auth/login")"
  token="$(json_field "$resp" token)"
  must_change="$(json_field "$resp" must_change_password)"
  if [ -z "$token" ]; then
    echo "dashboard login failed; API checks will be unauthenticated" >&2
    return 0
  fi
  if [ "$must_change" = "true" ]; then
    echo "WARNING: admin account still has the default password (must_change_password=true) -- change it now in the dashboard" >&2
  fi
  printf 'Authorization: Bearer %s' "$token"
}

# ---------------------------------------------------------------------------
# pre
# ---------------------------------------------------------------------------

# default_out_dir prints the default --out directory for the given
# current-version string.
default_out_dir() {
  local ver="$1" ts
  ts="$(date -u +%Y%m%dT%H%M%SZ)"
  join_root "$SANDBOX_ROOT" "/var/lib/cloud-pulse/upgrade-backups/${ts}-${ver}"
}

cmd_pre() {
  resolve_paths
  require_root_or_sandbox

  if [ ! -f "$ENV_FILE" ]; then
    err "hub.env not found at ${ENV_FILE}"
    exit 1
  fi

  local db_path current_version base_url out_dir
  db_path="${DATA_DIR}/cloud-pulse.db"
  if [ ! -f "$db_path" ]; then
    err "hub database not found at ${db_path}"
    exit 1
  fi

  current_version="unknown"
  if [ -x "$BIN_PATH" ]; then
    current_version="$("$BIN_PATH" -version 2>/dev/null | awk '{print $1}')"
    [ -n "$current_version" ] || current_version="unknown"
  fi
  base_url="$(hub_url_from_env)"
  out_dir="${OUT_DIR_OVERRIDE:-$(default_out_dir "$current_version")}"

  mkdir -p "$out_dir"
  chmod 0700 "$out_dir"

  log "=== pre-upgrade check: current version ${current_version}, out=${out_dir} ==="

  # --- service state -------------------------------------------------
  local service_active=0
  if systemctl_is_active; then
    service_active=1
    pass "service cloud-pulse-hub.service is active"
  else
    warn "service cloud-pulse-hub.service is not active (pre-upgrade snapshot will still proceed)"
  fi
  local pre_pid pre_started
  pre_pid="$(systemctl_main_pid)"
  pre_started="$(systemctl_start_timestamp)"

  # --- unit file + env keys (values excluded) -------------------------
  if [ -f "$UNIT_PATH" ]; then
    cp -p "$UNIT_PATH" "${out_dir}/unit.service"
    chmod 0600 "${out_dir}/unit.service"
    pass "unit file collected from ${UNIT_PATH}"
  else
    fail "unit file not found at ${UNIT_PATH}"
  fi
  env_active_keys "$ENV_FILE" > "${out_dir}/env-keys.txt"
  pass "hub.env key list collected ($(wc -l < "${out_dir}/env-keys.txt" | tr -d ' ') keys, values excluded)"

  # --- online DB backup + integrity ------------------------------------
  local backup_db="${out_dir}/cloud-pulse.db.bak"
  py_sqlite_backup "$db_path" "$backup_db"
  chmod 0600 "$backup_db"
  local integrity
  if integrity="$(py_sqlite_integrity_check "$backup_db")" && [ "$integrity" = "ok" ]; then
    pass "backup database integrity_check = ok"
  else
    fail "backup database integrity_check failed: ${integrity}"
  fi
  local backup_size backup_sha256
  backup_size="$(stat -c '%s' "$backup_db" 2>/dev/null || stat -f '%z' "$backup_db" 2>/dev/null || echo 0)"
  backup_sha256="$(sha256sum "$backup_db" 2>/dev/null | awk '{print $1}')"
  log "backup db: size=${backup_size} bytes sha256=${backup_sha256}"

  # --- rollback assets (binary, unit already copied above) ------------
  if [ -x "$BIN_PATH" ]; then
    cp -p "$BIN_PATH" "${out_dir}/cloud-pulse-hub.bak"
    chmod 0700 "${out_dir}/cloud-pulse-hub.bak"
    pass "current binary copied for rollback"
  else
    fail "current binary not found/executable at ${BIN_PATH}"
  fi
  if [ -f "$ENV_FILE" ]; then
    local env_mode
    env_mode="$(stat -c '%a' "$ENV_FILE" 2>/dev/null || stat -f '%Lp' "$ENV_FILE" 2>/dev/null || echo 600)"
    cp -p "$ENV_FILE" "${out_dir}/hub.env.bak"
    chmod "$env_mode" "${out_dir}/hub.env.bak" 2>/dev/null || chmod 0600 "${out_dir}/hub.env.bak"
    pass "hub.env copied for rollback (mode preserved: ${env_mode})"
  fi

  # --- DB snapshot ------------------------------------------------------
  py_sqlite_snapshot "$backup_db" "${out_dir}/db-snapshot.json"
  pass "database snapshot written to db-snapshot.json"

  # --- API snapshot -------------------------------------------------------
  local auth_header health_status version_status hosts_status hosts_body host_count
  auth_header="$(api_auth_header "$base_url")"
  health_status="$(curl_status "${base_url}/healthz")"
  [ "$health_status" = "200" ] && pass "GET /healthz = 200" || fail "GET /healthz = ${health_status}"

  version_status="$(curl_status "${base_url}/api/v1/version" "$auth_header")"
  [ "$version_status" = "200" ] && pass "GET /api/v1/version = 200" || warn "GET /api/v1/version = ${version_status}"

  hosts_status="$(curl_status "${base_url}/api/v1/hosts" "$auth_header")"
  hosts_body="$(curl_body "${base_url}/api/v1/hosts" "$auth_header")"
  host_count="$(python3 -c 'import json,sys
try:
    data = json.loads(sys.stdin.read())
    print(len(data) if isinstance(data, list) else len(data.get("hosts", [])))
except (json.JSONDecodeError, ValueError, AttributeError):
    print(-1)' <<<"$hosts_body")"
  if [ "$hosts_status" = "200" ] && [ "$host_count" -ge 0 ]; then
    pass "GET /api/v1/hosts = 200 (${host_count} hosts)"
  else
    fail "GET /api/v1/hosts = ${hosts_status}"
    host_count=-1
  fi

  # --- dashboard smoke ---------------------------------------------------
  local root_status css_status js_status csp_header
  root_status="$(curl_status "${base_url}/")"
  [ "$root_status" = "200" ] && pass "GET / = 200" || fail "GET / = ${root_status}"
  css_status="$(curl_status "${base_url}/assets/app.css")"
  [ "$css_status" = "200" ] && pass "GET /assets/app.css = 200" || fail "GET /assets/app.css = ${css_status}"
  js_status="$(curl_status "${base_url}/assets/js/main.js")"
  if [ "$js_status" != "200" ]; then
    js_status="$(curl_status "${base_url}/assets/app.js")"
  fi
  [ "$js_status" = "200" ] && pass "GET main JS asset = 200" || fail "GET main JS asset = ${js_status}"
  csp_header="$(curl -s -D - -o /dev/null "${base_url}/" 2>/dev/null | grep -i '^content-security-policy:' || true)"
  [ -n "$csp_header" ] && pass "GET / has Content-Security-Policy header" || warn "GET / has no Content-Security-Policy header"

  # --- pre.json summary ---------------------------------------------------
  python3 - "${out_dir}/pre.json" "$current_version" "$service_active" "$pre_pid" "$pre_started" \
    "$backup_size" "$backup_sha256" "$host_count" "$base_url" <<'PY'
import json
import sys

(out_path, version, service_active, pid, started, size, sha256, host_count, base_url) = sys.argv[1:10]
snapshot = {
    "version": version,
    "service_active": bool(int(service_active)),
    "main_pid": pid,
    "start_timestamp_monotonic": started,
    "backup_size_bytes": int(size),
    "backup_sha256": sha256,
    "host_count": int(host_count),
    "base_url": base_url,
}
with open(out_path, "w", encoding="utf-8") as f:
    json.dump(snapshot, f, indent=2, sort_keys=True)
    f.write("\n")
PY

  log ""
  log "=== pre-upgrade summary: ${PASS_COUNT} passed, ${WARN_COUNT} warnings, ${FAIL_COUNT} failed ==="
  log "backup bundle: ${out_dir}"
  log "next step: sudo cloud-pulse-hub update --version v0.7.0"
  log "then run: sudo scripts/hub-upgrade-check.sh post --expect v0.7.0 --out ${out_dir}"

  [ "$FAIL_COUNT" -eq 0 ]
}

# ---------------------------------------------------------------------------
# post
# ---------------------------------------------------------------------------

# find_latest_backup_dir — prints the most recently modified directory
# under the default upgrade-backups parent, for use when --out is
# omitted on post/rollback.
find_latest_backup_dir() {
  local parent
  parent="$(join_root "$SANDBOX_ROOT" /var/lib/cloud-pulse/upgrade-backups)"
  if [ ! -d "$parent" ]; then
    return 1
  fi
  # shellcheck disable=SC2012 # ls -t is adequate here: backup dir names
  # are UTC-timestamp-prefixed and contain no adversarial input.
  ls -t "$parent" 2>/dev/null | head -n1
}

cmd_post() {
  resolve_paths
  require_root_or_sandbox

  if [ -z "$EXPECT_VERSION" ]; then
    err "post requires --expect vX.Y.Z"
    exit 1
  fi

  local out_dir
  if [ -n "$OUT_DIR_OVERRIDE" ]; then
    out_dir="$OUT_DIR_OVERRIDE"
  else
    local parent name
    parent="$(join_root "$SANDBOX_ROOT" /var/lib/cloud-pulse/upgrade-backups)"
    name="$(find_latest_backup_dir)" || { err "no --out given and no backup bundle found under ${parent}"; exit 1; }
    out_dir="${parent}/${name}"
  fi

  local pre_json="${out_dir}/pre.json"
  if [ ! -f "$pre_json" ]; then
    err "pre.json not found at ${pre_json} (run 'pre' first, or pass the matching --out)"
    exit 1
  fi

  local db_path base_url
  db_path="${DATA_DIR}/cloud-pulse.db"
  base_url="$(hub_url_from_env)"

  log "=== post-upgrade check: expecting ${EXPECT_VERSION}, comparing against ${out_dir} ==="

  # --- service restarted? ------------------------------------------------
  local pre_pid pre_started post_active post_pid post_started
  pre_pid="$(json_field "$(cat "$pre_json")" main_pid)"
  pre_started="$(json_field "$(cat "$pre_json")" start_timestamp_monotonic)"

  if systemctl_is_active; then
    post_active=1
    pass "service cloud-pulse-hub.service is active"
  else
    post_active=0
    fail "service cloud-pulse-hub.service is not active after upgrade"
  fi
  post_pid="$(systemctl_main_pid)"
  post_started="$(systemctl_start_timestamp)"

  if [ "$post_active" -eq 1 ] && [ -n "$post_started" ] && [ "$post_started" != "$pre_started" ]; then
    pass "service restarted (start timestamp changed: ${pre_started} -> ${post_started})"
  else
    fail "service did not restart (start timestamp unchanged: ${pre_started} -> ${post_started:-<unknown>}; pid ${pre_pid} -> ${post_pid:-<unknown>})"
  fi

  # --- binary/API version ------------------------------------------------
  local bin_version
  bin_version="unknown"
  if [ -x "$BIN_PATH" ]; then
    bin_version="$("$BIN_PATH" -version 2>/dev/null | awk '{print $1}')"
  fi
  if [ "$bin_version" = "$EXPECT_VERSION" ]; then
    pass "installed binary reports ${EXPECT_VERSION}"
  else
    fail "installed binary reports ${bin_version:-<none>}, expected ${EXPECT_VERSION}"
  fi

  local auth_header api_version_body api_version api_version_code
  auth_header="$(api_auth_header "$base_url")"
  api_version_body="$(curl_body "${base_url}/api/v1/version" "$auth_header")"
  api_version="$(json_field "$api_version_body" version)"
  api_version_code="$(json_field "$api_version_body" code)"
  if [ "$api_version" = "$EXPECT_VERSION" ]; then
    pass "GET /api/v1/version reports ${EXPECT_VERSION}"
  elif [ "$api_version_code" = "password_change_required" ]; then
    warn "GET /api/v1/version blocked by the must-change-password gate; verified via the binary's own -version instead"
  else
    fail "GET /api/v1/version reports ${api_version:-<none>}, expected ${EXPECT_VERSION}"
  fi

  # --- unit directives: compare with the new binary's own render, not
  # a hard-coded list --------------------------------------------------
  if [ -x "$BIN_PATH" ] && [ -f "$UNIT_PATH" ]; then
    local rendered_unit
    # In sandbox mode (--root set), install-hub.sh itself renders the
    # unit with --read-write-path DATA_DIR (ReadWritePaths=DATA_DIR
    # instead of a real install's StateDirectory=cloud-pulse) — mirror
    # that here so the comparison is apples-to-apples; a real install
    # passes neither flag.
    if [ -n "$SANDBOX_ROOT" ]; then
      rendered_unit="$("$BIN_PATH" systemd-unit print --bin-path "$BIN_PATH" --env-file "$ENV_FILE" --read-write-path "$DATA_DIR" 2>/dev/null || true)"
    else
      rendered_unit="$("$BIN_PATH" systemd-unit print --bin-path "$BIN_PATH" --env-file "$ENV_FILE" 2>/dev/null || true)"
    fi
    if [ -n "$rendered_unit" ]; then
      local missing=0 line
      while IFS= read -r line; do
        [ -n "$line" ] || continue
        if ! grep -qF -- "$line" "$UNIT_PATH"; then
          missing=1
          echo "unit missing directive: ${line}" >&2
        fi
      done <<<"$rendered_unit"
      if [ "$missing" -eq 0 ]; then
        pass "installed unit contains every directive the new binary renders"
      else
        fail "installed unit is missing directives the new binary renders (run systemd-unit apply)"
      fi
    else
      warn "could not render the expected unit via systemd-unit print; skipping directive comparison"
    fi
  fi

  # --- DB integrity + migrations + preservation --------------------------
  local backup_db="${out_dir}/post-cloud-pulse.db.bak"
  py_sqlite_backup "$db_path" "$backup_db"
  local integrity
  if integrity="$(py_sqlite_integrity_check "$backup_db")" && [ "$integrity" = "ok" ]; then
    pass "post-upgrade database integrity_check = ok"
  else
    fail "post-upgrade database integrity_check failed: ${integrity}"
  fi

  local post_snapshot="${out_dir}/post-db-snapshot.json"
  py_sqlite_snapshot "$backup_db" "$post_snapshot"

  # Compares pre vs. post DB snapshots and prints "FAIL:msg"/"WARN:msg"
  # lines, translated below into this script's own pass/fail/warn
  # counters (bash's `set -e` makes branching on "did this print any
  # lines" awkward inline, so the comparison and the counting are two
  # separate steps).
  local diff_output
  diff_output="$(python3 - "$pre_json" "${out_dir}/db-snapshot.json" "$post_snapshot" <<'PY'
import json
import sys

pre_snap_path, post_snap_path = sys.argv[2], sys.argv[3]

with open(pre_snap_path, encoding="utf-8") as f:
    pre = json.load(f)
with open(post_snap_path, encoding="utf-8") as f:
    post = json.load(f)

failures: list[str] = []
warnings: list[str] = []

pre_migrations = set(pre.get("migrations", []))
post_migrations = set(post.get("migrations", []))
if not pre_migrations.issubset(post_migrations):
    failures.append(f"migrations regressed: pre had {sorted(pre_migrations)}, post has {sorted(post_migrations)}")

pre_counts = pre.get("row_counts", {})
post_counts = post.get("row_counts", {})
for table, pre_count in pre_counts.items():
    post_count = post_counts.get(table)
    if post_count is None:
        warnings.append(f"table {table!r} present pre-upgrade is absent post-upgrade")
        continue
    if post_count < pre_count:
        failures.append(f"table {table!r} row count decreased: {pre_count} -> {post_count}")

pre_hosts = set(pre.get("host_ids", []))
post_hosts = set(post.get("host_ids", []))
missing_hosts = pre_hosts - post_hosts
if missing_hosts:
    failures.append(f"hosts missing post-upgrade: {sorted(missing_hosts)}")

pre_settings = set(pre.get("setting_keys", []))
post_settings = set(post.get("setting_keys", []))
missing_settings = pre_settings - post_settings
if missing_settings:
    failures.append(f"settings keys missing post-upgrade: {sorted(missing_settings)}")

if not post.get("egress_rule_names"):
    warnings.append("no default egress alert rules found post-upgrade (expected 3 egress_out_pct rules)")
elif len(post.get("egress_rule_names", [])) < 3:
    warnings.append(f"expected at least 3 default egress_out_pct rules post-upgrade, found {len(post['egress_rule_names'])}")

for msg in failures:
    print(f"FAIL:{msg}")
for msg in warnings:
    print(f"WARN:{msg}")
PY
)"
  if [ -n "$diff_output" ]; then
    while IFS= read -r line; do
      [ -n "$line" ] || continue
      case "$line" in
        FAIL:*) fail "${line#FAIL:}" ;;
        WARN:*) warn "${line#WARN:}" ;;
      esac
    done <<<"$diff_output"
  fi
  if [ -z "$diff_output" ]; then
    pass "no data-preservation regressions detected (migrations superset, row counts non-decreasing, hosts/settings preserved)"
  fi

  # --- auth path -----------------------------------------------------------
  local me_status
  me_status="$(curl_status "${base_url}/api/v1/auth/me" "$auth_header")"
  case "$me_status" in
    200) pass "GET /api/v1/auth/me = 200 (authenticated)" ;;
    404) warn "GET /api/v1/auth/me = 404 (pre-auth hub; no dashboard login on this version)" ;;
    *) fail "GET /api/v1/auth/me = ${me_status}" ;;
  esac

  # --- API + dashboard smoke ------------------------------------------------
  # A freshly-upgraded hub whose admin session still has the default
  # password gets 403 password_change_required on every route besides
  # /auth/me|/auth/password|/auth/logout (see requireUser's must-change
  # gate) — expected right after an upgrade per SPEC-v0.8 §1 ("경고와
  # 함께... 대신 변경해 주지는 않는다"), so it is reported as a warning
  # here rather than a hard failure. Any other non-200/404 is a real
  # failure.
  local endpoint status body code
  for endpoint in /api/v1/hosts /api/v1/alerts/rules /api/v1/billing /api/v1/settings/network /api/v1/storage/accounts /api/v1/agents/updates; do
    status="$(curl_status "${base_url}${endpoint}" "$auth_header")"
    case "$status" in
      200) pass "GET ${endpoint} = 200" ;;
      404) warn "GET ${endpoint} = 404 (not present on this version)" ;;
      403)
        body="$(curl_body "${base_url}${endpoint}" "$auth_header")"
        code="$(json_field "$body" code)"
        if [ "$code" = "password_change_required" ]; then
          warn "GET ${endpoint} = 403 (admin must change the default password first; sign in and change it now)"
        else
          fail "GET ${endpoint} = 403 (code: ${code:-none})"
        fi
        ;;
      *) fail "GET ${endpoint} = ${status}" ;;
    esac
  done

  local pre_host_count post_hosts_body post_host_count post_hosts_code
  pre_host_count="$(json_field "$(cat "$pre_json")" host_count)"
  post_hosts_body="$(curl_body "${base_url}/api/v1/hosts" "$auth_header")"
  post_host_count="$(python3 -c 'import json,sys
try:
    data = json.loads(sys.stdin.read())
    print(len(data) if isinstance(data, list) else len(data.get("hosts", [])))
except (json.JSONDecodeError, ValueError, AttributeError):
    print(-1)' <<<"$post_hosts_body")"
  post_hosts_code="$(json_field "$post_hosts_body" code)"
  if [ "$post_hosts_code" = "password_change_required" ]; then
    warn "host count comparison skipped (admin must change the default password before GET /api/v1/hosts works)"
  elif [ "$post_host_count" = "$pre_host_count" ]; then
    pass "host count unchanged (${post_host_count})"
  else
    fail "host count changed: ${pre_host_count} -> ${post_host_count}"
  fi

  local root_status css_status csp_header cache_header
  root_status="$(curl_status "${base_url}/")"
  [ "$root_status" = "200" ] && pass "GET / = 200" || fail "GET / = ${root_status}"
  css_status="$(curl_status "${base_url}/assets/app.css")"
  [ "$css_status" = "200" ] && pass "GET /assets/app.css = 200" || fail "GET /assets/app.css = ${css_status}"
  cache_header="$(curl -s -D - -o /dev/null "${base_url}/" 2>/dev/null | grep -i '^cache-control:' || true)"
  if printf '%s' "$cache_header" | grep -qi 'no-cache'; then
    pass "GET / has Cache-Control: no-cache"
  else
    warn "GET / Cache-Control header: ${cache_header:-<missing>}"
  fi
  csp_header="$(curl -s -D - -o /dev/null "${base_url}/" 2>/dev/null | grep -i '^content-security-policy:' || true)"
  [ -n "$csp_header" ] && pass "GET / has Content-Security-Policy header" || warn "GET / has no Content-Security-Policy header"

  log ""
  log "=== post-upgrade summary: ${PASS_COUNT} passed, ${WARN_COUNT} warnings, ${FAIL_COUNT} failed ==="
  if [ "$FAIL_COUNT" -gt 0 ]; then
    log "one or more checks FAILED. Consider: sudo scripts/hub-upgrade-check.sh rollback --out ${out_dir}"
  fi

  [ "$FAIL_COUNT" -eq 0 ]
}

# ---------------------------------------------------------------------------
# rollback
# ---------------------------------------------------------------------------

cmd_rollback() {
  resolve_paths
  require_root_or_sandbox

  local out_dir
  if [ -n "$OUT_DIR_OVERRIDE" ]; then
    out_dir="$OUT_DIR_OVERRIDE"
  else
    local parent name
    parent="$(join_root "$SANDBOX_ROOT" /var/lib/cloud-pulse/upgrade-backups)"
    name="$(find_latest_backup_dir)" || { err "no --out given and no backup bundle found under ${parent}"; exit 1; }
    out_dir="${parent}/${name}"
  fi

  local backup_db="${out_dir}/cloud-pulse.db.bak"
  local backup_bin="${out_dir}/cloud-pulse-hub.bak"
  local backup_unit="${out_dir}/unit.service"
  local db_path="${DATA_DIR}/cloud-pulse.db"

  for f in "$backup_db" "$backup_bin" "$backup_unit"; do
    if [ ! -f "$f" ]; then
      err "rollback asset missing: ${f} (was 'pre' run with this --out?)"
      exit 1
    fi
  done

  local saved_dir
  saved_dir="${out_dir}/rollback-saved-$(date -u +%Y%m%dT%H%M%SZ)"

  log "=== rollback plan (bundle: ${out_dir}) ==="
  log "  1. ${SYSTEMCTL} stop cloud-pulse-hub.service"
  log "  2. move ${db_path}{,-wal,-shm} -> ${saved_dir}/ (not deleted)"
  log "  3. restore database from ${backup_db}"
  log "  4. restore binary from ${backup_bin} -> ${BIN_PATH}"
  log "  5. restore unit from ${backup_unit} -> ${UNIT_PATH}"
  log "  6. ${SYSTEMCTL} daemon-reload"
  log "  7. ${SYSTEMCTL} start cloud-pulse-hub.service"
  log "  8. verify version + host count"
  log ""
  log "NOTE: any data written since the 'pre' backup (new metrics, hosts,"
  log "settings changes, etc.) will NOT be present after this rollback."
  log "It will be preserved, unremoved, under: ${saved_dir}"

  if [ "$ROLLBACK_APPLY" -ne 1 ]; then
    log ""
    log "dry-run only (no changes made). Pass --apply to execute this plan."
    return 0
  fi

  if [ "$ROLLBACK_YES" -ne 1 ]; then
    if [ ! -e /dev/tty ]; then
      err "--apply given without --yes and no /dev/tty available to confirm; aborting"
      exit 1
    fi
    local confirm
    read -r -p "Apply this rollback? Data written since the backup will be set aside, not merged. [y/N] " confirm < /dev/tty > /dev/tty
    case "$confirm" in
      y|Y|yes|YES) ;;
      *) log "aborted."; return 1 ;;
    esac
  fi

  log ""
  log "=== applying rollback ==="
  "$SYSTEMCTL" stop cloud-pulse-hub.service
  pass "service stopped"

  mkdir -p "$saved_dir"
  for suffix in "" "-wal" "-shm"; do
    if [ -f "${db_path}${suffix}" ]; then
      mv "${db_path}${suffix}" "${saved_dir}/"
    fi
  done
  pass "current database files moved to ${saved_dir}"

  cp -p "$backup_db" "$db_path"
  chmod 0600 "$db_path"
  pass "database restored from backup"

  cp -p "$backup_bin" "$BIN_PATH"
  chmod 0755 "$BIN_PATH"
  pass "binary restored from backup"

  cp -p "$backup_unit" "$UNIT_PATH"
  pass "unit file restored from backup"

  "$SYSTEMCTL" daemon-reload
  pass "daemon-reload run"

  "$SYSTEMCTL" start cloud-pulse-hub.service
  pass "service started"

  local restored_version
  restored_version="unknown"
  if [ -x "$BIN_PATH" ]; then
    restored_version="$("$BIN_PATH" -version 2>/dev/null | awk '{print $1}')"
  fi
  local expected_version
  expected_version="$(json_field "$(cat "${out_dir}/pre.json")" version)"
  if [ "$restored_version" = "$expected_version" ]; then
    pass "restored binary reports ${expected_version}"
  else
    fail "restored binary reports ${restored_version:-<none>}, expected ${expected_version}"
  fi

  local base_url auth_header hosts_body host_count pre_host_count
  base_url="$(hub_url_from_env)"
  auth_header="$(api_auth_header "$base_url")"
  hosts_body="$(curl_body "${base_url}/api/v1/hosts" "$auth_header")"
  host_count="$(python3 -c 'import json,sys
try:
    data = json.loads(sys.stdin.read())
    print(len(data) if isinstance(data, list) else len(data.get("hosts", [])))
except (json.JSONDecodeError, ValueError, AttributeError):
    print(-1)' <<<"$hosts_body")"
  pre_host_count="$(json_field "$(cat "${out_dir}/pre.json")" host_count)"
  if [ "$host_count" = "$pre_host_count" ]; then
    pass "host count matches pre-upgrade snapshot (${host_count})"
  else
    fail "host count mismatch after rollback: expected ${pre_host_count}, got ${host_count}"
  fi

  log ""
  log "=== rollback summary: ${PASS_COUNT} passed, ${WARN_COUNT} warnings, ${FAIL_COUNT} failed ==="
  log "set-aside data from the upgraded state: ${saved_dir}"

  [ "$FAIL_COUNT" -eq 0 ]
}

# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------

main() {
  if [ "$#" -eq 0 ]; then
    usage
    exit 1
  fi
  local subcommand="$1"
  shift
  parse_common_args "$@"

  case "$subcommand" in
    pre)
      cmd_pre
      ;;
    post)
      cmd_post
      ;;
    rollback)
      cmd_rollback
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      err "unknown subcommand: ${subcommand}"
      usage
      exit 1
      ;;
  esac
}

main "$@"
