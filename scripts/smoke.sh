#!/usr/bin/env bash
# scripts/smoke.sh — end-to-end smoke test for cloud-pulse-hub and
# cloud-pulse-agent. Builds both binaries, runs a hub + agent pair
# against real (loopback) network sockets, and asserts on the observed
# behavior of the HTTP API and CLI flags. Uses only bash, curl, and
# python3 (for JSON parsing) so it runs unmodified on Ubuntu CI and
# locally.
set -euo pipefail

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------

SMOKE_PORT="${SMOKE_PORT:-18090}"
HUB_ADDR="127.0.0.1:${SMOKE_PORT}"
HUB_BASE_URL="http://${HUB_ADDR}"

AGENT_TOKEN="smoke-agent-token-0123456789"
UI_TOKEN="smoke-ui-token"
HOST_ID="smoke-host"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

TMP_DIR="$(mktemp -d)"
DATA_DIR="${TMP_DIR}/data"
mkdir -p "$DATA_DIR"

HUB_BIN="${TMP_DIR}/cloud-pulse-hub"
AGENT_BIN="${TMP_DIR}/cloud-pulse-agent"

HUB_LOG="${TMP_DIR}/hub.log"
AGENT_LOG="${TMP_DIR}/agent.log"

HUB_PID=""
AGENT_PID=""
FAKE_PID=""
WEBHOOK_PID=""

PASS_COUNT=0

pass() {
  echo "PASS $*"
  PASS_COUNT=$((PASS_COUNT + 1))
}

fail() {
  echo "FAIL $*" >&2
  dump_logs
  exit 1
}

dump_logs() {
  echo "----- hub log (${HUB_LOG}) -----" >&2
  cat "$HUB_LOG" >&2 2>/dev/null || true
  echo "----- agent log (${AGENT_LOG}) -----" >&2
  cat "$AGENT_LOG" >&2 2>/dev/null || true
}

cleanup() {
  local status=$?
  if [ -n "$AGENT_PID" ] && kill -0 "$AGENT_PID" 2>/dev/null; then
    kill "$AGENT_PID" 2>/dev/null || true
    wait "$AGENT_PID" 2>/dev/null || true
  fi
  if [ -n "$HUB_PID" ] && kill -0 "$HUB_PID" 2>/dev/null; then
    kill "$HUB_PID" 2>/dev/null || true
    wait "$HUB_PID" 2>/dev/null || true
  fi
  if [ -n "$FAKE_PID" ] && kill -0 "$FAKE_PID" 2>/dev/null; then
    kill "$FAKE_PID" 2>/dev/null || true
    wait "$FAKE_PID" 2>/dev/null || true
  fi
  if [ -n "$WEBHOOK_PID" ] && kill -0 "$WEBHOOK_PID" 2>/dev/null; then
    kill "$WEBHOOK_PID" 2>/dev/null || true
    wait "$WEBHOOK_PID" 2>/dev/null || true
  fi
  if [ "$status" -ne 0 ]; then
    dump_logs
  fi
  rm -rf "$TMP_DIR"
  exit "$status"
}
trap cleanup EXIT INT TERM

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

echo "==> building binaries"
CGO_ENABLED=0 go build -o "$HUB_BIN" ./cmd/hub
CGO_ENABLED=0 go build -o "$AGENT_BIN" ./cmd/agent
pass "build both binaries"

# A second pair of binaries stamped with a real release version (instead
# of the "dev" placeholder above) is needed for the `update --check`
# exercises below: selfupdate.Run refuses to compare a dev build against
# a resolved latest version at all (see internal/selfupdate/run.go), so
# the default smoke binaries can never exercise the exit-10 contract.
SMOKE_MODULE="github.com/leejeonghun001/cloud-pulse"
SMOKE_STAMPED_VERSION="v0.3.0"
HUB_STAMPED_BIN="${TMP_DIR}/cloud-pulse-hub-stamped"
AGENT_STAMPED_BIN="${TMP_DIR}/cloud-pulse-agent-stamped"
CGO_ENABLED=0 go build \
  -ldflags "-X ${SMOKE_MODULE}/internal/version.Version=${SMOKE_STAMPED_VERSION}" \
  -o "$HUB_STAMPED_BIN" ./cmd/hub
CGO_ENABLED=0 go build \
  -ldflags "-X ${SMOKE_MODULE}/internal/version.Version=${SMOKE_STAMPED_VERSION}" \
  -o "$AGENT_STAMPED_BIN" ./cmd/agent
pass "build ${SMOKE_STAMPED_VERSION}-stamped hub/agent binaries for update --check"

# ---------------------------------------------------------------------------
# -version / -gen-token (no server needed)
# ---------------------------------------------------------------------------

"$HUB_BIN" -version >/dev/null
pass "hub -version exits 0"

"$AGENT_BIN" -version >/dev/null
pass "agent -version exits 0"

GEN_TOKEN_OUT="$("$HUB_BIN" -gen-token)"
GEN_TOKEN_LEN="${#GEN_TOKEN_OUT}"
if [ "$GEN_TOKEN_LEN" -ne 64 ]; then
  fail "hub -gen-token printed ${GEN_TOKEN_LEN} chars, want 64: ${GEN_TOKEN_OUT}"
fi
case "$GEN_TOKEN_OUT" in
  *[!0-9a-f]*) fail "hub -gen-token output is not lowercase hex: ${GEN_TOKEN_OUT}" ;;
esac
pass "hub -gen-token prints 64 hex chars"

# agent -once prints valid JSON with a ts field.
ONCE_OUT="$("$AGENT_BIN" -once)"
echo "$ONCE_OUT" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
assert 'ts' in doc, 'missing ts field'
assert isinstance(doc['ts'], int), 'ts is not an integer'
"
pass "agent -once prints valid JSON with ts"

# ---------------------------------------------------------------------------
# Start hub
# ---------------------------------------------------------------------------

echo "==> starting hub on ${HUB_ADDR}"
# CP_UPDATE_CHECK is explicitly false: CI must never depend on reaching
# GitHub for this test to pass (see the dedicated update-check section
# below, which uses a local fake release server instead).
# CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS is explicitly "1": the v0.5 alerting
# section below points a webhook notify channel at a local plain-http
# python receiver, which the SSRF guard's default policy (https-only,
# fixed-host allowlist for Discord/Telegram/WhatsApp) would otherwise
# reject — this mirrors the task's live-check instruction to use the
# same env var for the same reason against real fakes.
CP_LISTEN="${HUB_ADDR}" \
CP_AGENT_TOKEN="${AGENT_TOKEN}" \
CP_UI_TOKEN="${UI_TOKEN}" \
CP_ALLOWED_CIDRS="127.0.0.0/8,::1/128" \
CP_DATA_DIR="${DATA_DIR}" \
CP_UPDATE_CHECK="false" \
CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS="1" \
  "$HUB_BIN" >"$HUB_LOG" 2>&1 &
HUB_PID=$!

HEALTHZ_OK=0
for _ in $(seq 1 40); do
  if curl -fsS "${HUB_BASE_URL}/healthz" >/dev/null 2>&1; then
    HEALTHZ_OK=1
    break
  fi
  if ! kill -0 "$HUB_PID" 2>/dev/null; then
    fail "hub process exited before becoming healthy"
  fi
  sleep 0.5
done
if [ "$HEALTHZ_OK" -ne 1 ]; then
  fail "hub did not become healthy within 20s"
fi
pass "hub /healthz reachable within 20s"

# ---------------------------------------------------------------------------
# Start agent
# ---------------------------------------------------------------------------

echo "==> starting agent"
CP_HUB_URL="${HUB_BASE_URL}" \
CP_AGENT_TOKEN="${AGENT_TOKEN}" \
CP_INTERVAL="5s" \
CP_HOST_ID="${HOST_ID}" \
  "$AGENT_BIN" >"$AGENT_LOG" 2>&1 &
AGENT_PID=$!

# ---------------------------------------------------------------------------
# Poll /api/v1/hosts until the smoke host has a latest sample
# ---------------------------------------------------------------------------

HOSTS_JSON=""
HAVE_LATEST=0
for _ in $(seq 1 90); do
  if HOSTS_JSON="$(curl -fsS -H "Authorization: Bearer ${UI_TOKEN}" "${HUB_BASE_URL}/api/v1/hosts" 2>/dev/null)"; then
    if echo "$HOSTS_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
hosts = doc.get('hosts', [])
sys.exit(0 if hosts and hosts[0].get('latest') is not None else 1)
" 2>/dev/null; then
      HAVE_LATEST=1
      break
    fi
  fi
  if ! kill -0 "$AGENT_PID" 2>/dev/null; then
    fail "agent process exited before reporting"
  fi
  sleep 0.5
done
if [ "$HAVE_LATEST" -ne 1 ]; then
  fail "host ${HOST_ID} did not report a latest sample within 45s"
fi
pass "GET /api/v1/hosts shows a latest sample for ${HOST_ID} within 45s"

# ---------------------------------------------------------------------------
# Assertions on /api/v1/hosts
# ---------------------------------------------------------------------------

echo "$HOSTS_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
host = doc['hosts'][0]
assert host['host']['id'] == '${HOST_ID}', f\"host id {host['host']['id']!r} != ${HOST_ID}\"
assert host['status'] == 'up', f\"status {host['status']!r} != 'up'\"
latest = host['latest']
assert latest['mem_total'] > 0, 'mem_total not > 0'
cpu = latest['cpu_percent']
assert 0 <= cpu <= 100, f'cpu_percent {cpu} not in [0,100]'
"
pass "host id == ${HOST_ID}, status == up, mem_total > 0, cpu_percent in [0,100]"

# ---------------------------------------------------------------------------
# /api/v1/hosts/{id}/metrics
# ---------------------------------------------------------------------------

METRICS_JSON="$(curl -fsS -H "Authorization: Bearer ${UI_TOKEN}" "${HUB_BASE_URL}/api/v1/hosts/${HOST_ID}/metrics?range=1h")"
echo "$METRICS_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
ts = doc['ts']
assert len(ts) >= 1, f'len(ts)={len(ts)} not >= 1'
arrays = ['cpu', 'mem', 'disk', 'net_rx', 'net_tx', 'disk_read', 'disk_write', 'load1']
lengths = {k: len(doc[k]) for k in arrays}
lengths['ts'] = len(ts)
uniq = set(lengths.values())
assert len(uniq) == 1, f'array lengths differ: {lengths}'
"
pass "GET /api/v1/hosts/${HOST_ID}/metrics?range=1h has len(ts)>=1 and equal-length arrays"

# ---------------------------------------------------------------------------
# /api/v1/egress
# ---------------------------------------------------------------------------

EGRESS_JSON="$(curl -fsS -H "Authorization: Bearer ${UI_TOKEN}" "${HUB_BASE_URL}/api/v1/egress")"
echo "$EGRESS_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
assert 'month' in doc and doc['month'], 'missing month'
assert len(doc['hosts']) == 1, f\"expected 1 host, got {len(doc['hosts'])}\"
"
pass "GET /api/v1/egress returns month and 1 host"

# ---------------------------------------------------------------------------
# /api/v1/buckets
# ---------------------------------------------------------------------------

BUCKETS_JSON="$(curl -fsS -H "Authorization: Bearer ${UI_TOKEN}" "${HUB_BASE_URL}/api/v1/buckets")"
echo "$BUCKETS_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
assert doc['buckets'] == [], f\"expected empty buckets, got {doc['buckets']}\"
assert doc['collectors'] == [], f\"expected empty collectors, got {doc['collectors']}\"
"
pass "GET /api/v1/buckets returns buckets [] and collectors []"

# ---------------------------------------------------------------------------
# v0.3: GET /api/v1/version — update checking disabled (CP_UPDATE_CHECK=false)
# ---------------------------------------------------------------------------

VERSION_JSON="$(curl -fsS -H "Authorization: Bearer ${UI_TOKEN}" "${HUB_BASE_URL}/api/v1/version")"
echo "$VERSION_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
assert doc.get('update_check_enabled') is False, f\"update_check_enabled {doc.get('update_check_enabled')!r} != False\"
assert doc.get('update_available') is False, f\"update_available {doc.get('update_available')!r} != False\"
assert 'update_command' in doc and doc['update_command'], 'missing update_command'
"
pass "GET /api/v1/version has update_check_enabled=false (CP_UPDATE_CHECK=false, CI must not depend on GitHub)"

# ---------------------------------------------------------------------------
# Auth failures
# ---------------------------------------------------------------------------

NOAUTH_STATUS="$(curl -s -o /dev/null -w '%{http_code}' "${HUB_BASE_URL}/api/v1/hosts")"
if [ "$NOAUTH_STATUS" != "401" ]; then
  fail "GET /api/v1/hosts without token returned ${NOAUTH_STATUS}, want 401"
fi
pass "GET /api/v1/hosts without token -> 401"

BADTOKEN_STATUS="$(curl -s -o /dev/null -w '%{http_code}' \
  -X POST -H "Authorization: Bearer wrong-token" \
  -H "Content-Type: application/json" \
  -d '{"host":{"id":"'"${HOST_ID}"'"},"samples":[]}' \
  "${HUB_BASE_URL}/api/v1/agent/report")"
if [ "$BADTOKEN_STATUS" != "401" ]; then
  fail "POST report with wrong token returned ${BADTOKEN_STATUS}, want 401"
fi
pass "POST report with wrong token -> 401"

# ---------------------------------------------------------------------------
# Static assets + security headers
# ---------------------------------------------------------------------------

INDEX_HEADERS="$(curl -s -D - -o /tmp/cp-smoke-index.$$ "${HUB_BASE_URL}/")"
INDEX_STATUS="$(echo "$INDEX_HEADERS" | head -1 | tr -d '\r' | awk '{print $2}')"
rm -f "/tmp/cp-smoke-index.$$"
if [ "$INDEX_STATUS" != "200" ]; then
  fail "GET / returned ${INDEX_STATUS}, want 200"
fi
if ! echo "$INDEX_HEADERS" | grep -qi '^content-type: *text/html'; then
  fail "GET / did not return text/html content-type"
fi
if ! echo "$INDEX_HEADERS" | grep -qi '^content-security-policy:'; then
  fail "GET / did not return a Content-Security-Policy header"
fi
pass "GET / -> 200 text/html with Content-Security-Policy header"

# ---------------------------------------------------------------------------
# Unknown API path -> 404 JSON
# ---------------------------------------------------------------------------

NOPE_BODY="$(curl -s -H "Authorization: Bearer ${UI_TOKEN}" -w '\n%{http_code}' "${HUB_BASE_URL}/api/v1/nope")"
NOPE_STATUS="$(echo "$NOPE_BODY" | tail -1)"
NOPE_JSON="$(echo "$NOPE_BODY" | sed '$d')"
if [ "$NOPE_STATUS" != "404" ]; then
  fail "GET /api/v1/nope returned ${NOPE_STATUS}, want 404"
fi
echo "$NOPE_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
assert 'error' in doc, 'missing error field in 404 body'
"
pass "GET /api/v1/nope -> 404 JSON"

# ---------------------------------------------------------------------------
# v0.2: ingest response server_time_ms is within 5s of local time
# ---------------------------------------------------------------------------

REPORT_RESP="$(curl -fsS -X POST \
  -H "Authorization: Bearer ${AGENT_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"host":{"id":"'"${HOST_ID}"'"},"samples":[]}' \
  "${HUB_BASE_URL}/api/v1/agent/report")"
NOW_MS="$(($(date +%s%N) / 1000000))"
echo "$REPORT_RESP" | NOW_MS="$NOW_MS" python3 -c "
import json, os, sys
doc = json.load(sys.stdin)
now_ms = int(os.environ['NOW_MS'])
server_ms = doc.get('server_time_ms')
assert isinstance(server_ms, int) and server_ms > 0, f'missing/invalid server_time_ms: {server_ms!r}'
diff = abs(now_ms - server_ms)
assert diff <= 5000, f'server_time_ms differs from local time by {diff}ms, want <=5000ms'
"
pass "POST /api/v1/agent/report response server_time_ms within 5s of local time"

# ---------------------------------------------------------------------------
# v0.2: GET /api/v1/agent/time — agent-token gated
# ---------------------------------------------------------------------------

AGENT_TIME_STATUS="$(curl -s -o /dev/null -w '%{http_code}' \
  -H "Authorization: Bearer ${AGENT_TOKEN}" \
  "${HUB_BASE_URL}/api/v1/agent/time")"
if [ "$AGENT_TIME_STATUS" != "200" ]; then
  fail "GET /api/v1/agent/time with agent token returned ${AGENT_TIME_STATUS}, want 200"
fi
pass "GET /api/v1/agent/time with agent token -> 200"

AGENT_TIME_NOAUTH_STATUS="$(curl -s -o /dev/null -w '%{http_code}' \
  "${HUB_BASE_URL}/api/v1/agent/time")"
if [ "$AGENT_TIME_NOAUTH_STATUS" != "401" ]; then
  fail "GET /api/v1/agent/time without token returned ${AGENT_TIME_NOAUTH_STATUS}, want 401"
fi
pass "GET /api/v1/agent/time without token -> 401"

# ---------------------------------------------------------------------------
# v0.2: admin settings endpoints (hub runs WITH CP_UI_TOKEN)
# ---------------------------------------------------------------------------

SETTINGS_STATUS="$(curl -s -o /dev/null -w '%{http_code}' \
  -H "Authorization: Bearer ${UI_TOKEN}" \
  "${HUB_BASE_URL}/api/v1/settings")"
if [ "$SETTINGS_STATUS" != "200" ]; then
  fail "GET /api/v1/settings with ui token returned ${SETTINGS_STATUS}, want 200"
fi
pass "GET /api/v1/settings with ui token -> 200"

SETTINGS_NOAUTH_STATUS="$(curl -s -o /dev/null -w '%{http_code}' \
  "${HUB_BASE_URL}/api/v1/settings")"
if [ "$SETTINGS_NOAUTH_STATUS" != "401" ]; then
  fail "GET /api/v1/settings without token returned ${SETTINGS_NOAUTH_STATUS}, want 401"
fi
pass "GET /api/v1/settings without token -> 401"

AGENT_TOKEN_VIEW="$(curl -fsS -H "Authorization: Bearer ${UI_TOKEN}" "${HUB_BASE_URL}/api/v1/settings/agent-token")"
echo "$AGENT_TOKEN_VIEW" | AGENT_TOKEN="$AGENT_TOKEN" python3 -c "
import json, os, sys
doc = json.load(sys.stdin)
want = os.environ['AGENT_TOKEN']
assert doc.get('agent_token') == want, f\"agent_token {doc.get('agent_token')!r} != {want!r}\"
assert doc.get('install_command'), 'missing install_command'
"
pass "GET /api/v1/settings/agent-token returns the agent token"

# ---------------------------------------------------------------------------
# v0.2: PUT /api/v1/hosts/{id}/limits — hub override applied
# ---------------------------------------------------------------------------

LIMITS_PUT_STATUS="$(curl -s -o /dev/null -w '%{http_code}' \
  -X PUT \
  -H "Authorization: Bearer ${UI_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"egress_limit_bytes": 1073741824, "ingress_limit_bytes": 2147483648}' \
  "${HUB_BASE_URL}/api/v1/hosts/${HOST_ID}/limits")"
if [ "$LIMITS_PUT_STATUS" != "200" ]; then
  fail "PUT /api/v1/hosts/${HOST_ID}/limits returned ${LIMITS_PUT_STATUS}, want 200"
fi
pass "PUT /api/v1/hosts/${HOST_ID}/limits -> 200"

HOSTS_AFTER_LIMITS_JSON="$(curl -fsS -H "Authorization: Bearer ${UI_TOKEN}" "${HUB_BASE_URL}/api/v1/hosts")"
echo "$HOSTS_AFTER_LIMITS_JSON" | HOST_ID="$HOST_ID" python3 -c "
import json, os, sys
doc = json.load(sys.stdin)
host_id = os.environ['HOST_ID']
hosts = [h for h in doc['hosts'] if h['host']['id'] == host_id]
assert len(hosts) == 1, f'expected exactly 1 host {host_id!r}, got {len(hosts)}'
egress = hosts[0]['egress']
assert egress.get('limit_bytes') == 1073741824, f\"limit_bytes {egress.get('limit_bytes')!r} != 1073741824\"
assert egress.get('rx_limit_bytes') == 2147483648, f\"rx_limit_bytes {egress.get('rx_limit_bytes')!r} != 2147483648\"
assert egress.get('limit_source') == 'hub', f\"limit_source {egress.get('limit_source')!r} != 'hub'\"
"
pass "GET /api/v1/hosts shows egress.limit_bytes=1073741824, rx_limit_bytes=2147483648, limit_source=hub"

# ---------------------------------------------------------------------------
# v0.2: agent sample ts values are multiples of the interval (5s)
# ---------------------------------------------------------------------------

echo "$METRICS_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
ts = doc['ts']
assert len(ts) >= 1, f'len(ts)={len(ts)} not >= 1'
bad = [t for t in ts if t % 5 != 0]
assert not bad, f'ts values not multiples of 5s interval: {bad}'
"
pass "agent sample ts values in metrics are all multiples of 5s"

# ---------------------------------------------------------------------------
# v0.3: `update --check` against a local fake release server
#
# Runs both binaries' `update --check` subcommand against a throwaway
# python3 http.server standing in for GitHub Releases (never real
# network), asserting the documented exit-10 "update available"
# contract and that the resolved latest tag is printed.
# ---------------------------------------------------------------------------

echo "==> starting fake release server for update --check"
FAKE_PORT="${FAKE_PORT:-18099}"
FAKE_BASE_URL="http://127.0.0.1:${FAKE_PORT}"
FAKE_TAG="v9.9.9"
FAKE_ASSETS_DIR="${TMP_DIR}/fake-assets"
mkdir -p "$FAKE_ASSETS_DIR"
FAKE_LOG="${TMP_DIR}/fake-release-server.log"

python3 "${REPO_ROOT}/scripts/fake_release_server.py" "$FAKE_PORT" "$FAKE_ASSETS_DIR" "$FAKE_TAG" \
  >"$FAKE_LOG" 2>&1 &
FAKE_PID=$!

FAKE_UP=0
for _ in $(seq 1 40); do
  if curl -fsS -o /dev/null "${FAKE_BASE_URL}/releases/latest" 2>/dev/null; then
    FAKE_UP=1
    break
  fi
  # A 302 makes curl -f treat it as success only with -L; without -L,
  # curl -fsS on a 3xx still exits 0 for HTTP 2xx/3xx by default only
  # with -f considering 4xx/5xx as errors, so this check alone confirms
  # the server is listening and answering.
  if ! kill -0 "$FAKE_PID" 2>/dev/null; then
    fail "fake release server exited before becoming reachable"
  fi
  sleep 0.25
done
if [ "$FAKE_UP" -ne 1 ]; then
  fail "fake release server did not become reachable within 10s"
fi
pass "fake release server reachable on ${FAKE_BASE_URL}"

HUB_CHECK_OUT=""
set +e
HUB_CHECK_OUT="$(CP_UPDATE_LATEST_URL="${FAKE_BASE_URL}/releases/latest" \
  CP_RELEASE_BASE_URL="${FAKE_BASE_URL}/releases/download/${FAKE_TAG}" \
  "$HUB_STAMPED_BIN" update --check 2>&1)"
HUB_CHECK_STATUS=$?
set -e
if [ "$HUB_CHECK_STATUS" -ne 10 ]; then
  echo "$HUB_CHECK_OUT" >&2
  fail "cloud-pulse-hub update --check exited ${HUB_CHECK_STATUS}, want 10"
fi
case "$HUB_CHECK_OUT" in
  *"$FAKE_TAG"*) ;;
  *) fail "cloud-pulse-hub update --check output did not mention ${FAKE_TAG}: ${HUB_CHECK_OUT}" ;;
esac
pass "cloud-pulse-hub update --check against fake release server -> exit 10, prints ${FAKE_TAG}"

AGENT_CHECK_OUT=""
set +e
AGENT_CHECK_OUT="$(CP_UPDATE_LATEST_URL="${FAKE_BASE_URL}/releases/latest" \
  CP_RELEASE_BASE_URL="${FAKE_BASE_URL}/releases/download/${FAKE_TAG}" \
  "$AGENT_STAMPED_BIN" update --check 2>&1)"
AGENT_CHECK_STATUS=$?
set -e
if [ "$AGENT_CHECK_STATUS" -ne 10 ]; then
  echo "$AGENT_CHECK_OUT" >&2
  fail "cloud-pulse-agent update --check exited ${AGENT_CHECK_STATUS}, want 10"
fi
case "$AGENT_CHECK_OUT" in
  *"$FAKE_TAG"*) ;;
  *) fail "cloud-pulse-agent update --check output did not mention ${FAKE_TAG}: ${AGENT_CHECK_OUT}" ;;
esac
pass "cloud-pulse-agent update --check against fake release server -> exit 10, prints ${FAKE_TAG}"

if kill -0 "$FAKE_PID" 2>/dev/null; then
  kill "$FAKE_PID" 2>/dev/null || true
  wait "$FAKE_PID" 2>/dev/null || true
fi
FAKE_PID=""

# ---------------------------------------------------------------------------
# v0.4: dashboard auth (login/must-change/logout/rate limit) and
# Network settings (interfaces/listeners, add-a-listener, lock-out) —
# SPEC-v0.4 §1/§2/§5.
# ---------------------------------------------------------------------------

echo "==> v0.4 auth flow"

# Fresh admin/changeme login must-change-gate 403 on a normal read
# endpoint, then GET /auth/me still works (exempt path), then the
# password change succeeds and yields a fresh, immediately-usable
# session with must_change_password=false.
LOGIN_JSON="$(curl -fsS -X POST -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"changeme"}' \
  "${HUB_BASE_URL}/api/v1/auth/login")"
echo "$LOGIN_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
assert doc.get('must_change_password') is True, f\"must_change_password {doc.get('must_change_password')!r} != True\"
assert doc.get('username') == 'admin', f\"username {doc.get('username')!r} != 'admin'\"
assert doc.get('token'), 'missing token'
assert isinstance(doc.get('expires_at'), int) and doc['expires_at'] > 0, 'missing/invalid expires_at'
"
SESSION_TOKEN="$(echo "$LOGIN_JSON" | python3 -c "import json,sys; print(json.load(sys.stdin)['token'])")"
pass "POST /api/v1/auth/login admin/changeme -> 200, must_change_password=true"

MUSTCHANGE_HOSTS_STATUS="$(curl -s -o /dev/null -w '%{http_code}' \
  -H "Authorization: Bearer ${SESSION_TOKEN}" "${HUB_BASE_URL}/api/v1/hosts")"
if [ "$MUSTCHANGE_HOSTS_STATUS" != "403" ]; then
  fail "GET /api/v1/hosts with must-change session returned ${MUSTCHANGE_HOSTS_STATUS}, want 403"
fi
pass "GET /api/v1/hosts with must-change-pending session -> 403"

MUSTCHANGE_ME_STATUS="$(curl -s -o /dev/null -w '%{http_code}' \
  -H "Authorization: Bearer ${SESSION_TOKEN}" "${HUB_BASE_URL}/api/v1/auth/me")"
if [ "$MUSTCHANGE_ME_STATUS" != "200" ]; then
  fail "GET /api/v1/auth/me with must-change session returned ${MUSTCHANGE_ME_STATUS}, want 200 (exempt path)"
fi
pass "GET /api/v1/auth/me with must-change-pending session -> 200 (exempt path)"

CHANGE_JSON="$(curl -fsS -X POST -H "Authorization: Bearer ${SESSION_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"current_password":"changeme","new_password":"smoke-new-password-1"}' \
  "${HUB_BASE_URL}/api/v1/auth/password")"
echo "$CHANGE_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
assert doc.get('must_change_password') is False, f\"must_change_password {doc.get('must_change_password')!r} != False\"
assert doc.get('token'), 'missing token'
"
NEW_SESSION_TOKEN="$(echo "$CHANGE_JSON" | python3 -c "import json,sys; print(json.load(sys.stdin)['token'])")"
pass "POST /api/v1/auth/password -> 200, fresh session, must_change_password=false"

# The pre-change session must be revoked by the password change.
OLD_SESSION_STATUS="$(curl -s -o /dev/null -w '%{http_code}' \
  -H "Authorization: Bearer ${SESSION_TOKEN}" "${HUB_BASE_URL}/api/v1/auth/me")"
if [ "$OLD_SESSION_STATUS" != "401" ]; then
  fail "GET /api/v1/auth/me with pre-change session returned ${OLD_SESSION_STATUS}, want 401 (revoked)"
fi
pass "pre-change session revoked by password change -> 401"

NEW_HOSTS_STATUS="$(curl -s -o /dev/null -w '%{http_code}' \
  -H "Authorization: Bearer ${NEW_SESSION_TOKEN}" "${HUB_BASE_URL}/api/v1/hosts")"
if [ "$NEW_HOSTS_STATUS" != "200" ]; then
  fail "GET /api/v1/hosts with post-change session returned ${NEW_HOSTS_STATUS}, want 200"
fi
pass "GET /api/v1/hosts with post-change session -> 200"

# CP_UI_TOKEN bearer still works as a full-access, never-must-change
# static API token, unaffected by the dashboard's session-based auth.
UI_TOKEN_STATUS="$(curl -s -o /dev/null -w '%{http_code}' \
  -H "Authorization: Bearer ${UI_TOKEN}" "${HUB_BASE_URL}/api/v1/hosts")"
if [ "$UI_TOKEN_STATUS" != "200" ]; then
  fail "GET /api/v1/hosts with CP_UI_TOKEN bearer returned ${UI_TOKEN_STATUS}, want 200"
fi
UI_TOKEN_ME_JSON="$(curl -fsS -H "Authorization: Bearer ${UI_TOKEN}" "${HUB_BASE_URL}/api/v1/auth/me")"
echo "$UI_TOKEN_ME_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
assert doc.get('auth_method') == 'api_token', f\"auth_method {doc.get('auth_method')!r} != 'api_token'\"
assert doc.get('must_change_password') is False, f\"must_change_password {doc.get('must_change_password')!r} != False (api_token is never must-change-gated)\"
"
pass "CP_UI_TOKEN bearer -> 200, auth_method=api_token, never must-change-gated"

# Logout deletes the current session.
LOGOUT_JSON="$(curl -fsS -X POST -H "Authorization: Bearer ${NEW_SESSION_TOKEN}" \
  "${HUB_BASE_URL}/api/v1/auth/logout")"
case "$LOGOUT_JSON" in
  *'"ok":true'*) ;;
  *) fail "POST /api/v1/auth/logout body did not contain ok:true: ${LOGOUT_JSON}" ;;
esac
LOGGEDOUT_STATUS="$(curl -s -o /dev/null -w '%{http_code}' \
  -H "Authorization: Bearer ${NEW_SESSION_TOKEN}" "${HUB_BASE_URL}/api/v1/auth/me")"
if [ "$LOGGEDOUT_STATUS" != "401" ]; then
  fail "GET /api/v1/auth/me after logout returned ${LOGGEDOUT_STATUS}, want 401"
fi
pass "POST /api/v1/auth/logout -> {ok:true}, session unusable afterward -> 401"

# Wrong password x5 from the same client -> 6th attempt is rate-limited
# (429, Retry-After header + retry_after_seconds field). Uses a
# dedicated python3 http.client loop (not curl) so every attempt reuses
# one TCP connection's local port deterministically is irrelevant here
# since the limiter buckets by RemoteAddr HOST only — this is itself a
# regression check that the limiter strips the ephemeral port rather
# than keying on the full "ip:port" string (a bug that would make the
# limiter a no-op against a real client whose OS picks a fresh source
# port per connection, which curl always does).
RATE_LIMIT_OUT="$(python3 -c "
import json, urllib.request, urllib.error

url = '${HUB_BASE_URL}/api/v1/auth/login'
body = json.dumps({'username': 'admin', 'password': 'wrong-password'}).encode()
codes = []
last_body = ''
for _ in range(6):
    req = urllib.request.Request(url, data=body, headers={'Content-Type': 'application/json'}, method='POST')
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            codes.append(resp.status)
            last_body = resp.read().decode()
    except urllib.error.HTTPError as e:
        codes.append(e.code)
        last_body = e.read().decode()
        last_headers = dict(e.headers)
print(json.dumps({'codes': codes, 'last_body': last_body, 'retry_after_header': last_headers.get('Retry-After')}))
")"
echo "$RATE_LIMIT_OUT" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
codes = doc['codes']
assert codes[:5] == [401]*5, f'first 5 attempts were {codes[:5]}, want five 401s'
assert codes[5] == 429, f'6th attempt was {codes[5]}, want 429'
last = json.loads(doc['last_body'])
assert last.get('code') == 'rate_limited', f\"code {last.get('code')!r} != 'rate_limited'\"
assert isinstance(last.get('retry_after_seconds'), int) and last['retry_after_seconds'] > 0, 'missing/invalid retry_after_seconds'
assert doc.get('retry_after_header'), 'missing Retry-After header'
"
pass "5x wrong password then 6th attempt -> 429 rate_limited with Retry-After"

echo "==> v0.4 network settings"

# Re-authenticate (the smoke test's own client IP may now be locked out
# of *failed* login attempts, but a *correct* login is a separate check
# — recordFailure/recordSuccess are keyed the same way, and a correct
# password during an active lockout is still rejected per spec, so wait
# for the 1-minute base lockout to clear before continuing).
NET_LOGIN_DEADLINE=$(( $(date +%s) + 90 ))
NET_SESSION_TOKEN=""
while [ "$(date +%s)" -lt "$NET_LOGIN_DEADLINE" ]; do
  ATTEMPT_JSON="$(curl -s -X POST -H "Content-Type: application/json" \
    -d '{"username":"admin","password":"smoke-new-password-1"}' \
    "${HUB_BASE_URL}/api/v1/auth/login")"
  NET_SESSION_TOKEN="$(echo "$ATTEMPT_JSON" | python3 -c "
import json, sys
try:
    print(json.load(sys.stdin).get('token', ''))
except Exception:
    print('')
")"
  if [ -n "$NET_SESSION_TOKEN" ]; then
    break
  fi
  sleep 2
done
if [ -z "$NET_SESSION_TOKEN" ]; then
  fail "could not log in again after rate-limit test within 90s (lockout never cleared?)"
fi
pass "re-authenticated after rate-limit lockout cleared"

NETWORK_GET_JSON="$(curl -fsS -H "Authorization: Bearer ${NET_SESSION_TOKEN}" \
  "${HUB_BASE_URL}/api/v1/settings/network")"
echo "$NETWORK_GET_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
assert isinstance(doc.get('interfaces'), list) and len(doc['interfaces']) >= 1, f\"interfaces {doc.get('interfaces')!r} not a non-empty list\"
names = {i['name'] for i in doc['interfaces']}
assert 'lo' in names, f'loopback interface \"lo\" not found in {names}'
lo = next(i for i in doc['interfaces'] if i['name'] == 'lo')
assert lo['kind'] == 'loopback', f\"lo kind {lo['kind']!r} != 'loopback'\"
assert isinstance(doc.get('listeners'), list) and len(doc['listeners']) >= 1, f\"listeners {doc.get('listeners')!r} not a non-empty list\"
assert any(l['status'] == 'listening' for l in doc['listeners']), f\"no listener with status=listening in {doc['listeners']}\"
assert doc.get('source') in ('hub', 'env'), f\"source {doc.get('source')!r} not 'hub' or 'env'\"
assert 'client' in doc and doc['client'].get('ip'), 'missing client.ip'
"
pass "GET /api/v1/settings/network lists interfaces (incl. loopback) and listeners (>=1 listening)"

# PUT adding a second listener (127.0.0.2, a bindable loopback alias on
# Linux) alongside the existing one: the client (127.0.0.1) stays
# served by the unchanged listener, so this persists immediately
# (pending=null) rather than entering the pending-confirmation path.
NETWORK_PUT_JSON="$(curl -s -X PUT -H "Authorization: Bearer ${NET_SESSION_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"mode":"custom","addresses":["127.0.0.1","127.0.0.2"],"port":'"${SMOKE_PORT}"',"allowed_cidrs":["127.0.0.0/8","::1/128"]}' \
  "${HUB_BASE_URL}/api/v1/settings/network")"
echo "$NETWORK_PUT_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
assert doc.get('pending') is None, f\"pending {doc.get('pending')!r} is not null (client should still be served on 127.0.0.1)\"
assert doc.get('source') == 'hub', f\"source {doc.get('source')!r} != 'hub' after a PUT\"
addrs = set(doc['config']['addresses'])
assert addrs == {'127.0.0.1', '127.0.0.2'}, f'config.addresses {addrs!r} != {{127.0.0.1, 127.0.0.2}}'
statuses = {l['addr']: l['status'] for l in doc['listeners']}
assert statuses.get('127.0.0.2:'+str(${SMOKE_PORT})) == 'listening', f'127.0.0.2 listener status: {statuses!r}'
"
pass "PUT /api/v1/settings/network adding 127.0.0.2 -> persists immediately, 127.0.0.2 listening"

# The new listener must actually serve a request, not just report
# "listening" in the status view.
NEW_LISTENER_STATUS="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.2:${SMOKE_PORT}/healthz")"
if [ "$NEW_LISTENER_STATUS" != "200" ]; then
  fail "GET http://127.0.0.2:${SMOKE_PORT}/healthz returned ${NEW_LISTENER_STATUS}, want 200 (new listener not actually serving)"
fi
pass "GET http://127.0.0.2:${SMOKE_PORT}/healthz -> 200 (new listener actually serves requests)"

# would_lock_out: an allowlist that excludes the requesting client's own
# address must be rejected with 409, and must NOT be applied.
LOCKOUT_STATUS="$(curl -s -o /dev/null -w '%{http_code}' -X PUT \
  -H "Authorization: Bearer ${NET_SESSION_TOKEN}" -H "Content-Type: application/json" \
  -d '{"mode":"custom","addresses":["127.0.0.1","127.0.0.2"],"port":'"${SMOKE_PORT}"',"allowed_cidrs":["203.0.113.0/24"]}' \
  "${HUB_BASE_URL}/api/v1/settings/network")"
if [ "$LOCKOUT_STATUS" != "409" ]; then
  fail "PUT /api/v1/settings/network with a self-excluding allowlist returned ${LOCKOUT_STATUS}, want 409"
fi
pass "PUT /api/v1/settings/network with a self-excluding allowlist -> 409 would_lock_out"

# Confirm the lock-out attempt was never applied: the original allowlist
# ("127.0.0.0/8,::1/128") should still be in effect, i.e. this same
# loopback client can still reach the API.
STILL_ALLOWED_STATUS="$(curl -s -o /dev/null -w '%{http_code}' \
  -H "Authorization: Bearer ${NET_SESSION_TOKEN}" "${HUB_BASE_URL}/api/v1/settings/network")"
if [ "$STILL_ALLOWED_STATUS" != "200" ]; then
  fail "GET /api/v1/settings/network after a rejected lock-out attempt returned ${STILL_ALLOWED_STATUS}, want 200 (allowlist must not have changed)"
fi
pass "rejected would_lock_out PUT left the allowlist unchanged (still reachable)"

# Basic confirm/revert: DELETE drops the hub override back to the env
# config (also exercises the same lock-out safety checks on a
# non-PUT endpoint), which the still-loopback-allowed client can do
# safely here.
NETWORK_DELETE_JSON="$(curl -fsS -X DELETE -H "Authorization: Bearer ${NET_SESSION_TOKEN}" \
  "${HUB_BASE_URL}/api/v1/settings/network")"
echo "$NETWORK_DELETE_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
assert doc.get('source') == 'env', f\"source {doc.get('source')!r} != 'env' after DELETE\"
"
pass "DELETE /api/v1/settings/network -> drops hub override, source=env"

# No pending change exists at this point (the DELETE above didn't
# create one): confirm/revert must both report 409 no_pending.
NOPENDING_CONFIRM_STATUS="$(curl -s -o /dev/null -w '%{http_code}' -X POST \
  -H "Authorization: Bearer ${NET_SESSION_TOKEN}" "${HUB_BASE_URL}/api/v1/settings/network/confirm")"
if [ "$NOPENDING_CONFIRM_STATUS" != "409" ]; then
  fail "POST /api/v1/settings/network/confirm with no pending change returned ${NOPENDING_CONFIRM_STATUS}, want 409"
fi
NOPENDING_REVERT_STATUS="$(curl -s -o /dev/null -w '%{http_code}' -X POST \
  -H "Authorization: Bearer ${NET_SESSION_TOKEN}" "${HUB_BASE_URL}/api/v1/settings/network/revert")"
if [ "$NOPENDING_REVERT_STATUS" != "409" ]; then
  fail "POST /api/v1/settings/network/revert with no pending change returned ${NOPENDING_REVERT_STATUS}, want 409"
fi
pass "POST .../network/confirm and .../revert with no pending change -> both 409 no_pending"

# ---------------------------------------------------------------------------
# v0.5: alerting + notify + chart — SPEC-v0.5 §E
#
# Creates a generic webhook notify channel (include_image=true) pointed
# at a local python webhook receiver, an alert rule with threshold 0/
# duration 0 (fires immediately on the smoke agent's first sample), and
# asserts the receiver got a firing message carrying a PNG chart image
# (magic-byte + full decode check). Also exercises the channel test
# endpoint, the events list, and the inventory endpoint.
# ---------------------------------------------------------------------------

echo "==> v0.5 alerting + notify + chart"

WEBHOOK_PORT="${WEBHOOK_PORT:-18098}"
WEBHOOK_BASE_URL="http://127.0.0.1:${WEBHOOK_PORT}"
WEBHOOK_LOG="${TMP_DIR}/webhook-receiver.log"
WEBHOOK_RECEIVED="${TMP_DIR}/webhook-received.jsonl"
: >"$WEBHOOK_RECEIVED"

python3 "${REPO_ROOT}/scripts/webhook_receiver.py" "$WEBHOOK_PORT" "$WEBHOOK_RECEIVED" \
  >"$WEBHOOK_LOG" 2>&1 &
WEBHOOK_PID=$!

WEBHOOK_UP=0
for _ in $(seq 1 40); do
  if curl -fsS -o /dev/null "${WEBHOOK_BASE_URL}/healthz" 2>/dev/null; then
    WEBHOOK_UP=1
    break
  fi
  if ! kill -0 "$WEBHOOK_PID" 2>/dev/null; then
    fail "webhook receiver exited before becoming reachable"
  fi
  sleep 0.25
done
if [ "$WEBHOOK_UP" -ne 1 ]; then
  fail "webhook receiver did not become reachable within 10s"
fi
pass "local webhook receiver reachable on ${WEBHOOK_BASE_URL}"

# We reuse the already-authenticated NET_SESSION_TOKEN (admin, past the
# must-change gate) for every alerts.* admin call below.

CHANNEL_JSON="$(curl -fsS -X POST -H "Authorization: Bearer ${NET_SESSION_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"name":"smoke webhook","type":"webhook","enabled":true,"config":{"url":"'"${WEBHOOK_BASE_URL}"'/webhook","include_image":"true"}}' \
  "${HUB_BASE_URL}/api/v1/alerts/channels")"
CHANNEL_ID="$(echo "$CHANNEL_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
assert doc.get('id'), f'missing channel id: {doc!r}'
assert doc.get('config', {}).get('url') != '***', 'url should not be redacted (not a secret field for webhook)'
print(doc['id'])
")"
pass "POST /api/v1/alerts/channels creates a webhook channel (id=${CHANNEL_ID})"

TEST_JSON="$(curl -fsS -X POST -H "Authorization: Bearer ${NET_SESSION_TOKEN}" \
  "${HUB_BASE_URL}/api/v1/alerts/channels/${CHANNEL_ID}/test")"
echo "$TEST_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
assert doc.get('ok') is True, f\"test delivery ok={doc.get('ok')!r}, want True: {doc!r}\"
"
pass "POST /api/v1/alerts/channels/${CHANNEL_ID}/test -> ok:true"

RULE_JSON="$(curl -fsS -X POST -H "Authorization: Bearer ${NET_SESSION_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"name":"smoke cpu rule","enabled":true,"metric":"cpu","host_id":"","operator":">=","threshold":0,"duration_sec":0,"cooldown_sec":0,"notify_resolved":false,"channel_ids":['"${CHANNEL_ID}"']}' \
  "${HUB_BASE_URL}/api/v1/alerts/rules")"
RULE_ID="$(echo "$RULE_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
assert doc.get('id'), f'missing rule id: {doc!r}'
print(doc['id'])
")"
pass "POST /api/v1/alerts/rules creates a CPU>=0/duration=0 rule (id=${RULE_ID})"

# The 30s scheduler tick (or the next ingest, whichever comes first)
# will evaluate this rule and fire immediately (threshold 0, no
# sustained window) for the smoke agent's host, delivering to the
# webhook receiver above with a rendered chart image attached
# (include_image=true).
FIRING_LINE=""
for _ in $(seq 1 90); do
  if [ -s "$WEBHOOK_RECEIVED" ]; then
    FIRING_LINE="$(grep -m1 'smoke cpu rule' "$WEBHOOK_RECEIVED" || true)"
    if [ -n "$FIRING_LINE" ]; then
      break
    fi
  fi
  sleep 1
done
if [ -z "$FIRING_LINE" ]; then
  echo "----- webhook receiver log -----" >&2
  cat "$WEBHOOK_LOG" >&2 2>/dev/null || true
  echo "----- webhook received (raw) -----" >&2
  cat "$WEBHOOK_RECEIVED" >&2 2>/dev/null || true
  echo "----- alert rules -----" >&2
  curl -s -H "Authorization: Bearer ${NET_SESSION_TOKEN}" "${HUB_BASE_URL}/api/v1/alerts/rules" >&2 2>/dev/null || true
  echo >&2
  echo "----- alert events -----" >&2
  curl -s -H "Authorization: Bearer ${NET_SESSION_TOKEN}" "${HUB_BASE_URL}/api/v1/alerts/events" >&2 2>/dev/null || true
  echo >&2
  fail "webhook receiver did not receive a firing message for 'smoke cpu rule' within 90s"
fi
pass "webhook receiver got a firing message referencing 'smoke cpu rule' within 90s"

echo "$FIRING_LINE" | HOST_ID="$HOST_ID" python3 -c "
import base64, json, os, sys

doc = json.loads(sys.stdin.read())
assert doc.get('title'), f'missing title: {doc!r}'
b64 = doc.get('image_png_base64')
assert b64, f'missing image_png_base64 (include_image=true was set): {doc!r}'
png = base64.b64decode(b64)
assert png[:8] == b'\x89PNG\r\n\x1a\n', f'decoded image does not start with PNG magic bytes: {png[:8]!r}'
assert len(png) > 100, f'decoded PNG suspiciously small ({len(png)} bytes)'
"
pass "firing message's image_png_base64 decodes to a PNG (magic bytes + non-trivial size)"

EVENTS_JSON="$(curl -fsS -H "Authorization: Bearer ${NET_SESSION_TOKEN}" \
  "${HUB_BASE_URL}/api/v1/alerts/events?host=${HOST_ID}")"
echo "$EVENTS_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
events = doc.get('events', [])
matching = [e for e in events if e.get('rule_name') == 'smoke cpu rule']
assert matching, f'no event for smoke cpu rule in {events!r}'
deliveries = matching[0].get('deliveries', [])
assert deliveries, f'event has no deliveries recorded: {matching[0]!r}'
assert any(d.get('ok') is True for d in deliveries), f'no delivery with ok=true: {deliveries!r}'
"
pass "GET /api/v1/alerts/events shows the firing event with a delivery ok=true"

ACTIVE_JSON="$(curl -fsS -H "Authorization: Bearer ${NET_SESSION_TOKEN}" \
  "${HUB_BASE_URL}/api/v1/alerts/active")"
echo "$ACTIVE_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
events = doc.get('events', [])
assert any(e.get('rule_name') == 'smoke cpu rule' for e in events), f'smoke cpu rule not in active events: {events!r}'
"
pass "GET /api/v1/alerts/active lists the still-firing smoke cpu rule"

# Clean up the rule so later scheduler ticks (during the remainder of
# this script) stop generating more deliveries against a receiver we're
# about to kill.
curl -fsS -X DELETE -H "Authorization: Bearer ${NET_SESSION_TOKEN}" \
  "${HUB_BASE_URL}/api/v1/alerts/rules/${RULE_ID}" >/dev/null
pass "DELETE /api/v1/alerts/rules/${RULE_ID} -> cleaned up"

if kill -0 "$WEBHOOK_PID" 2>/dev/null; then
  kill "$WEBHOOK_PID" 2>/dev/null || true
  wait "$WEBHOOK_PID" 2>/dev/null || true
fi
WEBHOOK_PID=""

echo "==> v0.5 inventory"

INVENTORY_JSON=""
for _ in $(seq 1 40); do
  if INVENTORY_JSON="$(curl -fsS -H "Authorization: Bearer ${NET_SESSION_TOKEN}" \
      "${HUB_BASE_URL}/api/v1/hosts/${HOST_ID}/inventory" 2>/dev/null)"; then
    break
  fi
  INVENTORY_JSON=""
  sleep 1
done
if [ -z "$INVENTORY_JSON" ]; then
  fail "GET /api/v1/hosts/${HOST_ID}/inventory never returned 200 within 40s"
fi
echo "$INVENTORY_JSON" | python3 -c "
import json, sys
doc = json.load(sys.stdin)
ports = doc.get('ports', [])
assert isinstance(ports, list) and len(ports) >= 1, f'expected >=1 listening port for the smoke agent, got {ports!r}'
assert 'docker' in doc, 'missing docker field'
"
pass "GET /api/v1/hosts/${HOST_ID}/inventory returns >=1 listening port for the smoke agent"

# ---------------------------------------------------------------------------
# SIGTERM hub -> exits within 10s, DB file exists
# ---------------------------------------------------------------------------

kill -TERM "$AGENT_PID" 2>/dev/null || true
wait "$AGENT_PID" 2>/dev/null || true
AGENT_PID=""

kill -TERM "$HUB_PID"
HUB_EXITED=0
for _ in $(seq 1 20); do
  if ! kill -0 "$HUB_PID" 2>/dev/null; then
    HUB_EXITED=1
    break
  fi
  sleep 0.5
done
if [ "$HUB_EXITED" -ne 1 ]; then
  fail "hub did not exit within 10s of SIGTERM"
fi
wait "$HUB_PID" 2>/dev/null
HUB_EXIT_CODE=$?
HUB_PID=""
if [ "$HUB_EXIT_CODE" -ne 0 ]; then
  fail "hub exited with code ${HUB_EXIT_CODE} after SIGTERM, want 0"
fi
if [ ! -f "${DATA_DIR}/cloud-pulse.db" ]; then
  fail "expected db file ${DATA_DIR}/cloud-pulse.db to exist after shutdown"
fi
pass "SIGTERM hub -> exits 0 within 10s and DB file exists"

echo
echo "smoke: all ${PASS_COUNT} assertions passed"
