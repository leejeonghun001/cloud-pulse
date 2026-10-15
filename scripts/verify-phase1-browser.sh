#!/usr/bin/env bash
# Gated UI-stage browser verification. This is intentionally pinned to this
# checkout, loopback :18190, and fresh ignored .verification data. It owns and
# stops only the hub process it starts; it never uses live/default hub data.
set -euo pipefail
umask 077
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
source "$REPO_ROOT/scripts/verify-round28-env.sh" || exit 2
cd "$REPO_ROOT"
ALLOWED_STAGES=(phase1 card-grid detail activity round28)
STAGE="${CP_VERIFY_STAGE:-phase1}"
ARTIFACT_ROOT="${CP_VERIFY_ARTIFACT_ROOT:-}"
PREBUILT_BIN=""
PREBUILT_SHA256=""
BINARY_KIND="release"
BINARY_VALIDATOR=--binary
PREFLIGHT_ONLY=0
AUDIT_ACCEPTED=0
FIRSTIMPRESSION=0
FOCUSED_PROOF=0
while (( $# )); do
  case "$1" in
    --stage)
      (( $# >= 2 )) || { echo "--stage requires phase1|card-grid|detail|activity|round28" >&2; exit 2; }
      STAGE="$2"
      shift 2
      ;;
    --artifact-root|--prebuilt-binary|--audit-binary|--binary-sha256)
      (( $# >= 2 )) || { echo "$1 requires a value" >&2; exit 2; }
      case "$1" in
        --artifact-root) ARTIFACT_ROOT="$2" ;;
        --prebuilt-binary|--audit-binary)
          [[ -z "$PREBUILT_BIN" ]] || { echo "select only one prebuilt binary purpose" >&2; exit 2; }
          PREBUILT_BIN="$2"
          if [[ "$1" == --audit-binary ]]; then BINARY_KIND=development-audit; BINARY_VALIDATOR=--audit-binary; fi
          ;;
        --binary-sha256) PREBUILT_SHA256="$2" ;;
      esac
      shift 2
      ;;
    --preflight-only) PREFLIGHT_ONLY=1; shift ;;
    --audit-accepted) AUDIT_ACCEPTED=1; shift ;;
    --firstimpression) FIRSTIMPRESSION=1; shift ;;
    --focused-proof) FIRSTIMPRESSION=1; FOCUSED_PROOF=1; shift ;;
    -h|--help)
      echo "usage: scripts/verify-phase1-browser.sh [--stage phase1|card-grid|detail|activity|round28]"
      echo "  [--artifact-root DIR] [--prebuilt-binary FILE --binary-sha256 SHA256] [--preflight-only] [--audit-accepted]"
      echo "  or --audit-binary FILE --binary-sha256 SHA256 for preliminary development QA only."
      echo "Round28 refuses to build: release candidates belong under releases/; development audit binaries under audit-bin/."
      echo "CP_VERIFY_STAGE may also select the stage; the runner is pinned to loopback :18190."
      echo '  --focused-proof runs only normal/reduced same-context native proof, not login/full matrix.'
      exit 0
      ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
STAGE_OK=0
for allowed in "${ALLOWED_STAGES[@]}"; do
  [[ "$STAGE" == "$allowed" ]] && STAGE_OK=1
done
(( STAGE_OK )) || { echo "invalid verification stage: ${STAGE}" >&2; exit 2; }
if (( FIRSTIMPRESSION )) && [[ "$STAGE" != round28 ]]; then echo '--firstimpression requires --stage round28' >&2; exit 2; fi
if (( FOCUSED_PROOF )); then
  node --input-type=module -e 'import{readFileSync}from"node:fs";const p=JSON.parse(readFileSync(".verification/round28/capture-plan.json"));if(p.focusedProofGateAccepted!==true)throw new Error("Focused proof gate closed: parent must accept terminal trap source and fresh prebuild");' || exit 2
fi

PORT=18190
HUB_URL="http://127.0.0.1:${PORT}"
VERIFY_ROOT="${REPO_ROOT}/.verification"
STAGE_ROOT="${ARTIFACT_ROOT:-${VERIFY_ROOT}/${STAGE}}"
STAGE_ROOT="$(node scripts/verify-round28-safety.mjs --path "$STAGE_ROOT")" || exit 2
if [[ "$STAGE" == round28 && "$STAGE_ROOT" != "${VERIFY_ROOT}/round28" && "$STAGE_ROOT" != "${VERIFY_ROOT}/round28/"* ]]; then
  echo "Round28 artifact root must remain beneath .verification/round28" >&2; exit 2
fi
CACHE_ROOT="${VERIFY_ROOT}/round28/cache"
if [[ -n "$PREBUILT_BIN" || -n "$PREBUILT_SHA256" ]]; then
  PREBUILT_BIN="$(node scripts/verify-round28-safety.mjs "$BINARY_VALIDATOR" "$PREBUILT_BIN" "$PREBUILT_SHA256")" || exit 2
elif [[ "$STAGE" == round28 && "$PREFLIGHT_ONLY" != 1 ]]; then
  echo "Round28 requires --prebuilt-binary or --audit-binary, plus --binary-sha256; no binary build is permitted here" >&2; exit 2
fi
if (( FOCUSED_PROOF )) && [[ -n "$PREBUILT_BIN" ]]; then
  node --input-type=module -e 'import{validateFrozenProofSource}from"./scripts/verify-round28-focused-proof.mjs";console.log(JSON.stringify(validateFrozenProofSource(process.argv[1])))' "$(dirname "$PREBUILT_BIN")/source-manifest.json" || exit 2
fi
for required in firefox curl ss flock; do
  command -v "$required" >/dev/null || { echo "$required is required; install nothing during this gate" >&2; exit 2; }
done
# Lock only our own runner, never a live/default instance. Refuse occupied
# test ports before any database/binary execution, including --preflight-only.
mkdir -p "${VERIFY_ROOT}/round28"
node scripts/verify-round28-safety.mjs --path "${VERIFY_ROOT}/round28/browser.lock" >/dev/null || exit 2
exec 9>"${VERIFY_ROOT}/round28/browser.lock"
flock -n 9 || { echo "another verification runner owns the test-port gate" >&2; exit 2; }
LISTENERS="$(ss -H -ltn "sport = :${PORT}")" || { echo "could not inspect test-port listeners; refusing to proceed" >&2; exit 2; }
if [[ -n "$LISTENERS" ]]; then
  echo "127.0.0.1:${PORT} is already in use; refusing to connect or replace it" >&2; exit 2
fi
if (( PREFLIGHT_ONLY )); then
  echo "PASS: preflight only; loopback :${PORT} unused; artifact root ${STAGE_ROOT}; binary ${PREBUILT_BIN:-not-yet-prepared}; no hub started"
  exit 0
fi
if [[ "$STAGE" == round28 && "$AUDIT_ACCEPTED" != 1 ]]; then
  echo "Round28 hub start is gated: parent must accept audit before --audit-accepted" >&2; exit 2
fi
mkdir -p "$STAGE_ROOT" "${CACHE_ROOT}/go-build" "${CACHE_ROOT}/python"
RUN_ROOT="$(mktemp -d "${STAGE_ROOT}/browser.XXXXXX")"
TMP_ROOT="${RUN_ROOT}/tmp"
LOG_ROOT="${RUN_ROOT}/logs"
DATA_DIR="${RUN_ROOT}/hub-data"
BIN_DIR="${RUN_ROOT}/bin"
OUT_DIR="${RUN_ROOT}/browser-output"
PASSWORD_DIR="${RUN_ROOT}/credentials"
PASSWORD_FILE="${PASSWORD_DIR}/admin.password"
HUB_BIN="${PREBUILT_BIN:-${BIN_DIR}/cloud-pulse-hub}"
HUB_LOG="${LOG_ROOT}/hub.log"
TEST_AGENT_TOKEN="phase1-local-agent-token-0123456789"
TEST_UI_TOKEN="phase1-local-ui-token-0123456789"
HUB_PID=""
RUN_FAILURE=0

mkdir -p "$TMP_ROOT" "$LOG_ROOT" "$DATA_DIR" "$BIN_DIR" "$OUT_DIR" "$PASSWORD_DIR" "${RUN_ROOT}/home"
chmod 700 "$STAGE_ROOT" "$RUN_ROOT" "$TMP_ROOT" "$LOG_ROOT" "$DATA_DIR" "$BIN_DIR" "$OUT_DIR" "$PASSWORD_DIR" "${RUN_ROOT}/home" "$CACHE_ROOT" "${CACHE_ROOT}/go-build" "${CACHE_ROOT}/python"
export TMPDIR="$TMP_ROOT" TMP="$TMP_ROOT" TEMP="$TMP_ROOT"
export GOCACHE="${CACHE_ROOT}/go-build"
export PYTHONPYCACHEPREFIX="${CACHE_ROOT}/python"
export CGO_ENABLED=0

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

port_is_listening() {
  local listeners
  listeners="$(ss -H -ltn "sport = :${PORT}")" || { echo "could not inspect test-port listeners" >&2; exit 1; }
  [[ -n "$listeners" ]]
}

curl() { command curl --noproxy '*' --connect-timeout 2 --max-time 15 "$@"; }

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [[ -n "$HUB_PID" ]] && jobs -pr | grep -Fx "$HUB_PID" >/dev/null; then
    echo "stopping owned hub pid ${HUB_PID}" | tee "${LOG_ROOT}/cleanup.log"
    kill -TERM "$HUB_PID" 2>/dev/null || true
    for _ in {1..40}; do
      jobs -pr | grep -Fx "$HUB_PID" >/dev/null || break
      sleep 0.1
    done
    if jobs -pr | grep -Fx "$HUB_PID" >/dev/null; then kill -KILL "$HUB_PID" 2>/dev/null || true; fi
    wait "$HUB_PID" 2>/dev/null || true
  else
    echo "owned hub process already stopped (pid ${HUB_PID:-not-started})" | tee "${LOG_ROOT}/cleanup.log"
  fi
  HUB_PID=""
  sleep 0.2
  if port_is_listening; then
    echo "FAIL: port ${PORT} still has a listener after owned-process cleanup" | tee -a "${LOG_ROOT}/cleanup.log" >&2
    status=1
  else
    echo "PASS: port ${PORT} is free after cleanup" | tee -a "${LOG_ROOT}/cleanup.log"
  fi
  echo "evidence: ${RUN_ROOT}" | tee -a "${LOG_ROOT}/cleanup.log"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cd "$REPO_ROOT"
if port_is_listening; then fail "127.0.0.1:${PORT} is already in use; refusing to connect or replace it"; fi
if ! command -v firefox >/dev/null || ! command -v curl >/dev/null || ! command -v ss >/dev/null; then
  fail "Firefox, curl, and ss are required; install nothing during this gate"
fi

SOURCE_CSS_SHA256="$(sha256sum web/assets/app.css | cut -d' ' -f1)"
# A later UI stage legitimately changes CSS; verify this run's source snapshot,
# with an optional caller-pinned digest, rather than a historical Phase 1 build.
EXPECTED_CSS_SHA256="${CP_VERIFY_EXPECTED_CSS_SHA256:-$SOURCE_CSS_SHA256}"
[[ "$EXPECTED_CSS_SHA256" =~ ^[0-9a-f]{64}$ ]] || fail "CP_VERIFY_EXPECTED_CSS_SHA256 must be 64 lowercase hex characters"
if [[ -n "$EXPECTED_CSS_SHA256" ]]; then
  [[ "$SOURCE_CSS_SHA256" == "$EXPECTED_CSS_SHA256" ]] || fail "source CSS digest $SOURCE_CSS_SHA256 did not match expected $EXPECTED_CSS_SHA256"
fi
printf 'stage: %s\nsource web/assets/app.css sha256: %s\n' "$STAGE" "$SOURCE_CSS_SHA256" > "${RUN_ROOT}/css-digests.txt"
[[ -z "$EXPECTED_CSS_SHA256" ]] || printf 'expected CSS sha256: %s\n' "$EXPECTED_CSS_SHA256" >> "${RUN_ROOT}/css-digests.txt"

if [[ -n "$PREBUILT_BIN" ]]; then
  echo "==> using validated exact ${BINARY_KIND} prebuilt binary (no rebuild or copy)"
  printf 'binary_kind=%s\nrelease_verification=%s\n' "$BINARY_KIND" "$([[ "$BINARY_KIND" == release ]] && printf candidate-only || printf not-performed-development-QA-only)" > "${RUN_ROOT}/binary-purpose.txt"
  node scripts/verify-round28-safety.mjs "$BINARY_VALIDATOR" "$HUB_BIN" "$PREBUILT_SHA256" > "${RUN_ROOT}/prebuilt-path.txt" || fail "prebuilt validation failed"
else
  echo "==> building isolated hub binary (legacy stages only)"
  go build -o "$HUB_BIN" ./cmd/hub >"${LOG_ROOT}/go-build-hub.log" 2>&1 || {
    cat "${LOG_ROOT}/go-build-hub.log" >&2
    fail "go build failed"
  }
fi
sha256sum "$HUB_BIN" > "${RUN_ROOT}/binary-digest-before.txt"
go version -m "$HUB_BIN" > "${RUN_ROOT}/binary-buildinfo.txt"

printf '%s\n' 'phase1-test-password' > "$PASSWORD_FILE"
chmod 600 "$PASSWORD_FILE"

echo "==> migrating fresh isolated database and setting stdin-only test password before hub launch"
if ! "$HUB_BIN" reset-password --data-dir "$DATA_DIR" --password-stdin < "$PASSWORD_FILE" >"${LOG_ROOT}/reset-password.log" 2>&1; then
  cat "${LOG_ROOT}/reset-password.log" >&2
  fail "isolated reset-password/database migration failed"
fi

echo "==> inserting deterministic long-name, no-sample visual fixture before hub launch"
if ! python3 - "${DATA_DIR}/cloud-pulse.db" >"${LOG_ROOT}/long-host-fixture.log" 2>&1 <<'PY'
import json
import sqlite3
import sys
import time

db_path = sys.argv[1]
host = {
    "id": "verify-long-name-01",
    "hostname": "verify-grid-long-hostname-eu-west-1-database-replica-secondary-02",
    "os": "linux",
    "platform": "ubuntu",
    "platform_version": "24.04",
    "kernel_version": "6.8.0-test",
    "arch": "amd64",
    "cpu_model": "Isolated fixture only",
    "cpu_cores": 4,
    "boot_time": int(time.time()) - 86400,
    "provider": "other",
    "egress_limit_bytes": 0,
    "agent_version": "v0.3.1",
}
with sqlite3.connect(db_path) as db:
    db.execute(
        "INSERT INTO hosts (id, info_json, last_seen, latest_ts, latest_json) VALUES (?, ?, ?, NULL, NULL)",
        (host["id"], json.dumps(host, separators=(",", ":")), int(time.time())),
    )
    db.commit()
print(f"seeded identity-only host {host['id']} ({len(host['hostname'])} hostname characters); no samples or measurements")
PY
then
  cat "${LOG_ROOT}/long-host-fixture.log" >&2
  fail "isolated long-host fixture insertion failed"
fi

if [[ "$STAGE" == activity || "$FIRSTIMPRESSION" == 1 ]]; then
  echo "==> seeding deterministic alert history into the isolated SQLite schema before hub launch"
  if ! python3 - "${DATA_DIR}/cloud-pulse.db" "${RUN_ROOT}/alerts-fixture.json" >"${LOG_ROOT}/alerts-fixture-seed.log" 2>&1 <<'PY'
import json
import sqlite3
import sys
import time

db_path, manifest_path = sys.argv[1:]
now = int(time.time())
hosts = [
    ("demo-aws-web-01", "demo-aws-web-01"),
    ("demo-aws-db-01", "demo-aws-db-01"),
    ("demo-oci-app-01", "demo-oci-app-01"),
    ("demo-other-nas-01", "demo-other-nas-01"),
    ("demo-aws-cache-01", "demo-aws-cache-01"),
    ("demo-oci-offline-01", "demo-oci-offline-01"),
    ("verify-long-name-01", "verify-grid-long-hostname-eu-west-1-database-replica-secondary-02"),
]
deleted_host = ("deleted-demo-host-01", "retired-gateway-west-europe-legacy-cluster-archive-node-09")

with sqlite3.connect(db_path) as db:
    rules = db.execute(
        "SELECT id, name, metric, threshold FROM alert_rules WHERE metric='egress_out_pct' ORDER BY threshold"
    ).fetchall()
    if len(rules) != 3:
        raise SystemExit(f"expected three migrated default egress rules, found {len(rules)}")
    channel_count = db.execute("SELECT COUNT(*) FROM notify_channels").fetchone()[0]
    if channel_count != 0:
        raise SystemExit(f"refusing alert fixture seed because notify_channels is not empty ({channel_count})")
    event_count = db.execute("SELECT COUNT(*) FROM alert_events").fetchone()[0]
    if event_count != 0:
        raise SystemExit(f"refusing alert fixture seed into a nonempty alert_events table ({event_count})")
    # Disable migrated defaults before the hub starts: the fixture consists
    # only of historical event rows, with no notification channels/sends.
    db.execute("UPDATE alert_rules SET enabled=0")
    rows = []
    firing = resolved = with_deliveries = 0
    for index in range(55):
        rule_id, rule_name, metric, threshold = rules[index % len(rules)]
        if index == 54:
            host_id, hostname = deleted_host  # alert_events has no host foreign key; preserve its historical host snapshot.
        elif index == 1:
            host_id, hostname = hosts[-1]     # long real test host label, linked through the normal UI route.
        else:
            host_id, hostname = hosts[(index * 3) % len(hosts)]
        if index == 3:
            # alert_events is a denormalized history snapshot with no host or rule FK.
            # Reuse a positive migrated rule ID; the metric/host snapshot is a
            # storage-account occurrence and must not be routed as a host.
            rule_id = rules[0][0]
            metric = "storage_usage_pct"
            threshold = 80.0
            host_id = "42"
            hostname = "archive-vault-account-42-production-usage-snapshot"
        state = "firing" if index % 4 == 0 else "resolved"
        started_at = now - 60 - index * 86_400  # unique newest-first cursors spread across multiple UTC dates.
        resolved_at = 0 if state == "firing" else started_at + 120 + (index % 6) * 30
        deliveries = []
        if index % 3 == 1:
            deliveries = [{"channel_id":0,"channel_name":"fixture-only delivered record","ok":True,"at":started_at + 20,"duration_ms":11}]
        elif index % 3 == 2:
            deliveries = [{"channel_id":0,"channel_name":"fixture-only failed record","ok":False,"error":"fixture-only simulated connection refused","at":started_at + 20,"duration_ms":23}]
        if index == 3:
            rule_snapshot = "Storage account usage 80% (historical fixture snapshot)"
            value = 87.5
        else:
            rule_snapshot = rule_name if index != 1 else f"Historical snapshot · {rule_name} · legacy threshold label"
            value = float(threshold) + 5.0 + (index % 3)
        rows.append((rule_id, rule_snapshot, host_id, hostname, metric, state, value, threshold,
                     started_at, started_at + 20 if deliveries else 0, resolved_at, json.dumps(deliveries,separators=(",",":"))))
        firing += state == "firing"
        resolved += state == "resolved"
        with_deliveries += bool(deliveries)
    db.executemany(
        """INSERT INTO alert_events
           (rule_id,rule_name,host_id,hostname,metric,state,value,threshold,started_at,notified_at,resolved_at,deliveries)
           VALUES (?,?,?,?,?,?,?,?,?,?,?,?)""",
        rows,
    )
    db.commit()

with open(manifest_path, "w", encoding="utf-8") as manifest:
    date_count = len({row[8] // 86_400 for row in rows})
    storage_event = db.execute(
        "SELECT id FROM alert_events WHERE metric='storage_usage_pct' AND host_id='42' AND state='resolved'"
    ).fetchone()
    if storage_event is None:
        raise SystemExit("storage-scoped history snapshot was not inserted")
    json.dump({"total":55,"page_size":50,"firing":firing,"resolved":resolved,"with_deliveries":with_deliveries,
               "deleted_host_id":deleted_host[0],"deleted_hostname":deleted_host[1],
               "long_host_id":hosts[-1][0],"long_hostname":hosts[-1][1],"notification_channels":0,
               "distinct_utc_dates":date_count,"storage_event_id":storage_event[0],"storage_host_id":"42",
               "storage_hostname":"archive-vault-account-42-production-usage-snapshot","storage_metric":"storage_usage_pct",
               "storage_value":87.5,"storage_threshold":80.0}, manifest, separators=(",",":"))
print(f"seeded 55 historical alert events ({firing} firing, {resolved} resolved, {with_deliveries} with delivery records), including one storage-account snapshot")
print("disabled migrated rules; notify_channels remains empty; no deliveries were sent")
PY
  then
    cat "${LOG_ROOT}/alerts-fixture-seed.log" >&2
    fail "isolated alert-event fixture seeding failed"
  fi
fi

cat > "${RUN_ROOT}/runtime-config.txt" <<EOF
listen=${HUB_URL}
data_dir=${DATA_DIR}
allowed_cidrs=127.0.0.0/8,::1/128
update_check=false
billing=off
offline_after=3600s (keeps fresh identity-only visual fixtures online for the short browser run)
admin_username=admin
admin_password_file=${PASSWORD_FILE} (test-only; mode 0600)
agent_token=redacted (test-only dummy token)
ui_token=redacted (test-only dummy token)
EOF

echo "==> starting only the isolated hub at ${HUB_URL}"
if [[ -n "$PREBUILT_BIN" ]]; then
  node scripts/verify-round28-safety.mjs "$BINARY_VALIDATOR" "$HUB_BIN" "$PREBUILT_SHA256" >/dev/null || fail "selected binary changed before launch"
fi
if port_is_listening; then fail "test port became occupied before launch"; fi
env -i \
  PATH="$PATH" \
  HOME="${RUN_ROOT}/home" \
  TMPDIR="$TMP_ROOT" TMP="$TMP_ROOT" TEMP="$TMP_ROOT" \
  GOCACHE="$GOCACHE" \
  GOMODCACHE="$GOMODCACHE" PYTHONPYCACHEPREFIX="$PYTHONPYCACHEPREFIX" \
  CP_LISTEN="127.0.0.1:${PORT}" \
  CP_DATA_DIR="$DATA_DIR" \
  CP_AGENT_TOKEN="$TEST_AGENT_TOKEN" \
  CP_UI_TOKEN="$TEST_UI_TOKEN" \
  CP_ALLOWED_CIDRS='127.0.0.0/8,::1/128' \
  CP_OFFLINE_AFTER=3600s \
  CP_UPDATE_CHECK=false \
  CP_BILLING=off \
  "$HUB_BIN" >"$HUB_LOG" 2>&1 &
HUB_PID=$!
printf '%s\n' "$HUB_PID" > "${RUN_ROOT}/owned-hub.pid"

HEALTH_OK=0
for _ in {1..120}; do
  if curl --silent --show-error --fail "$HUB_URL/healthz" -o "${RUN_ROOT}/http-healthz.body" 2>>"${LOG_ROOT}/http.log"; then
    HEALTH_OK=1
    break
  fi
  if ! kill -0 "$HUB_PID" 2>/dev/null; then break; fi
  sleep 0.25
done
[[ "$HEALTH_OK" == 1 ]] || {
  cat "$HUB_LOG" >&2
  fail "isolated hub failed to become healthy on :${PORT}"
}
[[ -n "$(ss -H -ltnp "sport = :${PORT}" | grep -F "pid=${HUB_PID},")" ]] || fail "health response was not from the owned hub listener"
[[ "$(readlink "/proc/${HUB_PID}/exe")" == "$HUB_BIN" ]] || fail "owned listener is not the exact selected hub binary"
sha256sum "/proc/${HUB_PID}/exe" > "${RUN_ROOT}/running-binary-digest.txt"
if [[ -n "$PREBUILT_BIN" ]]; then
  [[ "$(sha256sum "/proc/${HUB_PID}/exe" | cut -d' ' -f1)" == "$PREBUILT_SHA256" ]] || fail "running selected binary digest mismatch"
fi
echo "PASS: isolated /healthz is reachable on 127.0.0.1:${PORT}"

echo "==> seeding the existing demo fixture into the isolated database"
if ! python3 scripts/demo-seed.py \
  --hub "$HUB_URL" \
  --token "$TEST_AGENT_TOKEN" \
  --ui-token "$TEST_UI_TOKEN" \
  --db "${DATA_DIR}/cloud-pulse.db" >"${LOG_ROOT}/demo-seed.log" 2>&1; then
  cat "${LOG_ROOT}/demo-seed.log" >&2
  fail "isolated demo fixture seed failed"
fi
printf '%s\n' \
  'Fixture: scripts/demo-seed.py fixed demo host IDs and seeded metric samples; timestamps are current-run relative.' \
  'Target: fresh .verification-only SQLite database; no live/default data touched.' \
  'Known detail route: #/host/demo-aws-web-01.' \
  'Pre-hub visual fixture: verify-long-name-01 has a deterministic long hostname and no samples.' > "${RUN_ROOT}/fixture.txt"
if [[ "$STAGE" != phase1 ]]; then
  UNKNOWN_REPORT="${RUN_ROOT}/no-sample-host-report.json"
  printf '%s\n' '{"host":{"id":"verify-unknown-01","hostname":"verify-unknown-01","provider":"other"},"samples":[]}' > "$UNKNOWN_REPORT"
  curl --silent --show-error --fail \
    --header "Authorization: Bearer ${TEST_AGENT_TOKEN}" \
    --header 'Content-Type: application/json' \
    --data-binary "@${UNKNOWN_REPORT}" \
    "${HUB_URL}/api/v1/agent/report" \
    -o "${RUN_ROOT}/unknown-fixture-ingest.json" 2>>"${LOG_ROOT}/http.log" || fail "isolated no-sample fixture ingest failed"
  printf '%s\n' \
    'Card-grid-only fixture: verify-unknown-01 is an isolated host identity with blank OS/arch/agent and no samples.' \
    'Expected UI state: localized unknown metadata and no-samples placeholder; this is test-only, not product data.' >> "${RUN_ROOT}/fixture.txt"
fi
if [[ "$STAGE" == detail || "$STAGE" == activity || "$FIRSTIMPRESSION" == 1 ]]; then
  echo "==> building test-only services inventory fixture from existing schema data, without seeding channels/rules"
  if ! curl --silent --show-error --fail \
    --header "Authorization: Bearer ${TEST_UI_TOKEN}" \
    "${HUB_URL}/api/v1/hosts/demo-aws-web-01" \
    -o "${RUN_ROOT}/detail-host-summary.json" 2>>"${LOG_ROOT}/http.log"; then
    fail "could not read isolated demo host identity for detail inventory fixture"
  fi
  if ! python3 - "${RUN_ROOT}/detail-host-summary.json" "${RUN_ROOT}/detail-inventory-report.json" >"${LOG_ROOT}/detail-inventory-build.log" 2>&1 <<'PY'
import importlib.util
import json
import sys
import time

spec = importlib.util.spec_from_file_location("demo_seed_v05", "scripts/demo_seed_v05.py")
if spec is None or spec.loader is None:
    raise SystemExit("could not load test fixture data definitions")
module = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = module
spec.loader.exec_module(module)
host = next((item for item in module.INVENTORY_HOSTS if item.host_id == "demo-aws-web-01"), None)
if host is None:
    raise SystemExit("test fixture has no demo-aws-web-01 inventory definition")
report = module.InventorySeeder._build_report(host, int(time.time()))
with open(sys.argv[1], encoding="utf-8") as source:
    report["host"] = json.load(source)["host"]
report["samples"] = []  # preserve the existing metric fixture exactly
with open(sys.argv[2], "w", encoding="utf-8") as output:
    json.dump(report, output, separators=(",", ":"))
print(f"built inventory-only AgentReport with {len(report['inventory']['docker']['containers'])} containers and {len(report['inventory']['ports'])} listening ports")
PY
  then
    cat "${LOG_ROOT}/detail-inventory-build.log" >&2
    fail "isolated detail inventory fixture generation failed"
  fi
  if ! curl --silent --show-error --fail \
    --header "Authorization: Bearer ${TEST_AGENT_TOKEN}" \
    --header 'Content-Type: application/json' \
    --data-binary "@${RUN_ROOT}/detail-inventory-report.json" \
    "${HUB_URL}/api/v1/agent/report" \
    -o "${RUN_ROOT}/detail-inventory-ingest.json" 2>>"${LOG_ROOT}/http.log"; then
    fail "isolated detail inventory report was rejected"
  fi
  printf '%s\n' \
    'Detail fixture: inventory-only AgentReport for demo-aws-web-01, built from existing schema-shaped test inventory data.' \
    'No alert channels/rules or external endpoints were seeded; no sample measurements were added.' >> "${RUN_ROOT}/fixture.txt"
  echo "==> waiting for isolated metrics rollups needed by longer detail ranges"
  ROLLUP_READY=0
  for _ in {1..90}; do
    curl --silent --show-error --fail \
      --header "Authorization: Bearer ${TEST_UI_TOKEN}" \
      "${HUB_URL}/api/v1/hosts/demo-aws-web-01/metrics?range=30d" \
      -o "${RUN_ROOT}/detail-30d-rollup-probe.json" 2>>"${LOG_ROOT}/http.log" || fail "isolated 30d metrics probe failed"
    ROLLUP_POINTS="$(python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1], encoding="utf-8")).get("ts", [])))' "${RUN_ROOT}/detail-30d-rollup-probe.json")"
    if [[ "$ROLLUP_POINTS" -gt 0 ]]; then ROLLUP_READY=1; break; fi
    sleep 1
  done
  [[ "$ROLLUP_READY" == 1 ]] || fail "isolated metrics rollup did not populate 30d-series storage within 90s"
  printf 'Long-range fixture readiness: %s 30d metric sample(s) after local rollup.\n' "$ROLLUP_POINTS" >> "${RUN_ROOT}/fixture.txt"
fi
if [[ "$STAGE" == activity || "$FIRSTIMPRESSION" == 1 ]]; then
  printf '%s\n' \
    'Activity fixture: 55 test-only alert_events rows inserted into the fresh migrated DB before hub startup.' \
    'Uses disabled migrated egress rules and no notification channels; delivery values are historical JSON snapshots only.' \
    'The event schema has no host foreign key; one resolved page-two row references deleted-demo-host-01.' \
    'scripts/demo_seed_v05.py notification/channel seeding is not used; no notification send endpoint is called.' >> "${RUN_ROOT}/fixture.txt"
fi

echo "==> checking literal HTTP /login and served CSS digest"
HTTP_STATUS="$(curl --silent --show-error --output "${RUN_ROOT}/http-login.html" \
  --dump-header "${RUN_ROOT}/http-login.headers" --write-out '%{http_code}' \
  "$HUB_URL/login" 2>>"${LOG_ROOT}/http.log")" || fail "HTTP GET /login failed"
[[ "$HTTP_STATUS" == 200 ]] || fail "GET /login returned HTTP ${HTTP_STATUS}, expected 200"
curl --silent --show-error --fail "$HUB_URL/assets/app.css" -o "${RUN_ROOT}/served-app.css" 2>>"${LOG_ROOT}/http.log" || fail "could not fetch served app.css"
SERVED_CSS_SHA256="$(sha256sum "${RUN_ROOT}/served-app.css" | cut -d' ' -f1)"
printf 'HTTP GET /login: %s\nsource web/assets/app.css sha256: %s\nserved /assets/app.css sha256: %s\n' \
  "$HTTP_STATUS" "$SOURCE_CSS_SHA256" "$SERVED_CSS_SHA256" >> "${RUN_ROOT}/css-digests.txt"
[[ "$SERVED_CSS_SHA256" == "$SOURCE_CSS_SHA256" ]] || fail "served CSS digest differs from checked-in built CSS"
if [[ -n "$EXPECTED_CSS_SHA256" ]]; then
  [[ "$SERVED_CSS_SHA256" == "$EXPECTED_CSS_SHA256" ]] || fail "served CSS digest differs from the stage's expected digest"
fi
echo "PASS: literal /login is HTTP ${HTTP_STATUS}; served CSS digest matches source ${SOURCE_CSS_SHA256}"

echo "==> checking served embedded overview JS and locale catalog digests"
while IFS= read -r -d '' source_path; do
   asset="${source_path#web/}"
  source_path="web/${asset}"
  served_path="${RUN_ROOT}/served-${asset//\//_}"
  curl --silent --show-error --fail "${HUB_URL}/${asset}" -o "$served_path" 2>>"${LOG_ROOT}/http.log" || fail "could not fetch served asset ${asset}"
  source_hash="$(sha256sum "$source_path" | cut -d' ' -f1)"
  served_hash="$(sha256sum "$served_path" | cut -d' ' -f1)"
  printf '%s source=%s served=%s\n' "$asset" "$source_hash" "$served_hash" >> "${RUN_ROOT}/asset-digests.txt"
  [[ "$source_hash" == "$served_hash" ]] || fail "served embedded asset differs from source: ${asset}"
done < <(find web/assets -type f -print0)

run_browser_check() {
  local label="$1" status=0
  shift
  echo "==> ${label}"
  if "$@" >"${LOG_ROOT}/${label}.log" 2>&1; then
    status=0
  else
    status=$?
    RUN_FAILURE=1
  fi
  cat "${LOG_ROOT}/${label}.log"
  echo "${label}_exit=${status}" >> "${RUN_ROOT}/results.txt"
}

if (( ! FOCUSED_PROOF )); then
  run_browser_check auth-smoke node scripts/verify-phase1-auth.mjs \
    --hub "$HUB_URL" --password-file "$PASSWORD_FILE" --out "${OUT_DIR}/login"
fi

if [[ "$STAGE" != phase1 && "$STAGE" != round28 ]]; then
  run_browser_check overview-contract node scripts/verify-overview-contract.mjs \
    --stage card-grid --hub "$HUB_URL" --password-file "$PASSWORD_FILE" --out "${OUT_DIR}/overview-contract"
  printf 'overview contract report: %s\n' "${OUT_DIR}/overview-contract/overview-contract-report.json" >> "${RUN_ROOT}/results.txt"
fi
if [[ "$STAGE" == detail || "$STAGE" == activity ]]; then
  run_browser_check detail-contract node scripts/verify-detail-contract.mjs \
    --stage detail --hub "$HUB_URL" --password-file "$PASSWORD_FILE" --out "${OUT_DIR}/detail-contract"
  printf 'detail contract report: %s\n' "${OUT_DIR}/detail-contract/detail-contract-report.json" >> "${RUN_ROOT}/results.txt"
fi
if [[ "$STAGE" == activity ]]; then
  run_browser_check activity-contract node scripts/verify-activity-contract.mjs \
    --stage activity --hub "$HUB_URL" --password-file "$PASSWORD_FILE" \
    --fixture-file "${RUN_ROOT}/alerts-fixture.json" --ui-token "$TEST_UI_TOKEN" --out "${OUT_DIR}/activity-contract"
  printf 'activity contract report: %s\n' "${OUT_DIR}/activity-contract/activity-contract-report.json" >> "${RUN_ROOT}/results.txt"
fi

if [[ "$STAGE" == phase1 ]]; then
  AUDIT_VIEWPORTS=360x800,1440x900
  AUDIT_LANGS=en
  AUDIT_WAIT=800
  AUDIT_TIMEOUT=180000
else
  AUDIT_VIEWPORTS=360x800,768x1024,1440x900
  AUDIT_LANGS=en,ko
  AUDIT_WAIT=600
  AUDIT_TIMEOUT=300000
fi

if [[ "$STAGE" != round28 ]]; then
run_browser_check ui-audit node scripts/ui-audit.mjs \
  --hub "$HUB_URL" \
  --user admin \
  --password-file "$PASSWORD_FILE" \
  --out "${OUT_DIR}/ui-audit" \
  --routes '#/login,#/,#/host/demo-aws-web-01,#/alerts' \
  --viewports "$AUDIT_VIEWPORTS" \
  --themes dark,light \
  --langs "$AUDIT_LANGS" \
  --wait "$AUDIT_WAIT" \
  --timeout "$AUDIT_TIMEOUT"
fi
if [[ "$STAGE" == round28 ]]; then
  if (( FIRSTIMPRESSION )); then
    if (( FOCUSED_PROOF )); then
      run_browser_check focused-proof node scripts/verify-round28-firstimpression.mjs \
        --plan "$REPO_ROOT/.verification/round28/capture-plan.json" \
        --out "${OUT_DIR}/focused-proof" --run --integration-accepted --focused-proof \
        --source-manifest "$(dirname "$HUB_BIN")/source-manifest.json" \
        --hub "$HUB_URL" --password-file "$PASSWORD_FILE" --owned-run-root "$RUN_ROOT"
      printf 'Focused proof report (not full QA): %s\n' "${OUT_DIR}/focused-proof/report.json" >> "${RUN_ROOT}/results.txt"
    else
      run_browser_check firstimpression node scripts/verify-round28-firstimpression.mjs \
        --plan "$REPO_ROOT/.verification/round28/capture-plan.json" \
        --out "${OUT_DIR}/firstimpression" --run --integration-accepted \
        --source-manifest "$(dirname "$HUB_BIN")/source-manifest.json" \
        --hub "$HUB_URL" --password-file "$PASSWORD_FILE" --owned-run-root "$RUN_ROOT"
      printf 'Full feature/capture report: %s\n' "${OUT_DIR}/firstimpression/report.json" >> "${RUN_ROOT}/results.txt"
    fi
    run_browser_check no-dispatch-database python3 - "${DATA_DIR}/cloud-pulse.db" <<'PY'
import json,sqlite3,sys
with sqlite3.connect('file:'+sys.argv[1]+'?mode=ro',uri=True) as db:
    counts={table:db.execute('SELECT COUNT(*) FROM '+table).fetchone()[0] for table in ['notify_channels','channel_synthetic_results']}
    counts['enabled_rules']=db.execute('SELECT COUNT(*) FROM alert_rules WHERE enabled != 0').fetchone()[0]
print(json.dumps(counts))
if any(counts.values()): raise SystemExit('Unexpected dispatch/evaluator fixture state')
PY
  fi
  if (( FOCUSED_PROOF )); then
    printf 'Scope: one normal + one reduced same-context native Tab/ShiftTab/Escape/notification/billing/real-history proof only; full matrix/login-12 not performed; every embedded asset digest.\n' >> "${RUN_ROOT}/results.txt"
  elif (( FIRSTIMPRESSION )); then
    printf 'Scope: login-12 + 72 scoped feature-route cases and A/B/D/range/Activity/native-motion runtime hooks + private captures; every embedded asset digest. Legacy generic ui-audit not invoked; scoped BiDi report is authoritative.\n' >> "${RUN_ROOT}/results.txt"
  else
    printf 'Scope: literal /login EN/KO x dark/light x 360/768/1440, auth lifecycle, missing-resource/CSP/console/JS checks, every embedded asset digest; generic legacy ui-audit deferred to final QA.\n' >> "${RUN_ROOT}/results.txt"
  fi
  node scripts/verify-round28-safety.mjs "$BINARY_VALIDATOR" "$HUB_BIN" "$PREBUILT_SHA256" > "${RUN_ROOT}/prebuilt-path-after.txt" || fail "selected binary changed during test"
  sha256sum "$HUB_BIN" > "${RUN_ROOT}/binary-digest-after.txt"
fi

if (( ! FOCUSED_PROOF )); then printf 'auth report: %s\n' "${OUT_DIR}/login/auth-report.json" >> "${RUN_ROOT}/results.txt"; fi
if [[ "$STAGE" != round28 ]]; then
  printf 'ui audit report: %s\n' "${OUT_DIR}/ui-audit/report.json" >> "${RUN_ROOT}/results.txt"
else
  printf 'legacy generic ui audit: not run; no legacy report claimed (see scoped firstimpression report when enabled)\n' >> "${RUN_ROOT}/results.txt"
fi
if [[ "$STAGE" == activity ]]; then
  printf 'stage-specific assertion coverage: auth + card-grid + detail + Alerts history contract + shared UI audit\n' >> "${RUN_ROOT}/results.txt"
fi

if (( RUN_FAILURE )); then
  echo "Browser gate has one or more failed checks; reports are retained for review." >&2
  exit 1
fi

echo "PASS: ${STAGE} browser checks completed; hub cleanup follows."
echo "evidence: ${RUN_ROOT}"
