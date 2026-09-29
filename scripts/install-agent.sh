#!/usr/bin/env bash
#
# install-agent.sh — one-click installer for cloud-pulse-agent.
#
# Downloads a pre-compiled cloud-pulse-agent release binary from GitHub
# Releases, verifies its sha256 checksum, installs it as a systemd
# service running as an unprivileged system user, and writes its
# configuration to /etc/cloud-pulse/agent.env.
#
# Usage (typical, piped from curl):
#   curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.sh \
#     | sudo bash -s -- --hub-url http://100.x.y.z:8090 --token TOKEN
#
# Flags:
#   --hub-url URL          cloud-pulse hub base URL. Required on first
#                         install; on upgrade, reuses the existing value
#                         from agent.env unless given again.
#   --token TOKEN          CP_AGENT_TOKEN. Required on first install; on
#                         upgrade, reuses the existing value unless given
#                         again.
#   --host-id ID           Set CP_HOST_ID (default: system hostname).
#   --interval DURATION    Set CP_INTERVAL (default: 15s).
#   --provider auto|aws|oci|other
#                         Set CP_PROVIDER (default: auto).
#   --egress-limit-gb N    Set CP_EGRESS_LIMIT_GB.
#   --version vX.Y.Z        Install a specific release (default: latest).
#   --prefix DIR            Binary install prefix (default: /usr/local).
#   --uninstall              Stop/disable the service, remove the unit
#                         and binary. Config is kept unless --purge.
#   --purge                  With --uninstall, also remove
#                         /etc/cloud-pulse/agent.env.
#   --dry-run                Print the actions that would be taken and
#                         exit without changing anything.
#   -h, --help                Show this help and exit.
#
# Re-running this script (without --uninstall) upgrades an existing
# install in place: the binary and systemd unit are replaced, but
# agent.env is preserved unless an explicit flag supplies a new value.
#
# Environment overrides:
#   CP_RELEASE_BASE_URL   Override the base URL assets are downloaded
#                         from (default: GitHub release URLs). Assets are
#                         expected at "$CP_RELEASE_BASE_URL/<asset-name>".
#   CP_INSTALL_ROOT       Sandbox mode. When set, every system path this
#                         script touches (binary prefix, /etc/cloud-pulse,
#                         /etc/systemd/system) is prefixed with this
#                         directory instead of the real root filesystem.
#                         The root check, `useradd`, `chown`, and
#                         `systemctl` calls are skipped and replaced with
#                         "sandbox: would run ..." messages. Downloading,
#                         checksum verification, file installation, unit
#                         rendering, and `systemd-analyze verify` (if
#                         available) still run for real against the
#                         sandboxed paths.
#   CP_TEST_UNAME_M       Override the value used in place of `uname -m`
#                         (test hook for the unknown-arch path).
#
# All logic lives inside main(), invoked at the very end of the file, so
# that a connection that is cut off mid-download (when this script itself
# is piped through `curl | bash`) never executes a truncated fragment of
# the script.

set -euo pipefail

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

CP_REPO="leejeonghun001/cloud-pulse"
CP_GITHUB_BASE_URL="https://github.com/${CP_REPO}"
CP_ASSET_NAME_AGENT_PREFIX="cloud-pulse-agent"
CP_SERVICE_USER="cloud-pulse"
CP_SERVICE_GROUP="cloud-pulse"

# ---------------------------------------------------------------------------
# Globals populated by argument parsing / detection.
# ---------------------------------------------------------------------------

OPT_VERSION=""
OPT_HUB_URL=""
OPT_TOKEN=""
OPT_HOST_ID=""
OPT_INTERVAL=""
OPT_PROVIDER=""
OPT_EGRESS_LIMIT_GB=""
OPT_PREFIX="/usr/local"
OPT_UNINSTALL=0
OPT_PURGE=0
OPT_DRY_RUN=0

SANDBOX_ROOT="${CP_INSTALL_ROOT:-}"
IS_SANDBOX=0

BIN_DIR=""
ETC_DIR=""
SYSTEMD_DIR=""
ENV_FILE=""
UNIT_FILE=""
BIN_PATH=""

ARCH=""
OS_NAME=""
ASSET_NAME=""

TMP_DIR=""

PREVIOUS_VERSION=""
IS_UPGRADE=0

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

log() {
  echo "[install-agent] $*"
}

err() {
  echo "[install-agent] error: $*" >&2
}

usage() {
  sed -n '2,60p' "$0" | sed 's/^# \{0,1\}//'
}

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    err "required command '$1' not found in PATH"
    exit 1
  fi
}

fetch() {
  local url="$1" out="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 --retry-delay 1 -o "$out" "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$out" "$url"
  else
    err "neither curl nor wget is available to download files"
    exit 1
  fi
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    err "neither sha256sum nor shasum is available to verify checksums"
    exit 1
  fi
}

# validate_env_value VALUE FLAGNAME — reject values that would corrupt or
# escape a KEY=VALUE line when later written into agent.env and parsed by
# systemd's EnvironmentFile=. Without this check, a value containing a
# newline injects a second, arbitrary "KEY=VALUE" line into the file
# (e.g. --host-id "$(printf 'x\nCP_HUB_URL=http://evil/\n#')" would append
# a rogue CP_HUB_URL line that wins under systemd's last-line-wins
# parsing); a value containing unquoted whitespace silently corrupts the
# config with no clear error. Reject each case explicitly, naming the
# offending flag. A literal backslash is also rejected: systemd's real
# EnvironmentFile= parser applies POSIX-style unquoted backslash-escape
# rules to unquoted values, silently dropping/altering the backslash on
# load, so the value written by this installer could differ from what
# the running service actually receives with no visible error.
validate_env_value() {
  local value="$1" flag_name="$2"
  case "$value" in
    *$'\n'*)
      err "${flag_name} value must not contain a newline"
      exit 1
      ;;
    *$'\r'*)
      err "${flag_name} value must not contain a carriage return"
      exit 1
      ;;
    '#'*|'export '*)
      err "${flag_name} value must not begin with '#' or 'export '"
      exit 1
      ;;
  esac
  case "$value" in
    *' '*|*$'\t'*)
      err "${flag_name} value must not contain whitespace (got: ${value})"
      exit 1
      ;;
    *'\'*)
      err "${flag_name} value must not contain a backslash (got: ${value})"
      exit 1
      ;;
  esac
}

# semver_lt A B — small bash-only semver comparator, "vX.Y.Z[-pre]" only
# (no build-metadata handling; good enough for the installer's upgrade
# hint, not a general semver library — see internal/version for the real
# one used by the Go binaries). Returns 0 (true) iff A < B. Any input
# that doesn't match `vNUM.NUM.NUM` is treated as not-less-than anything
# (the caller treats an unparsable previous version as "legacy" via a
# separate helper, not via this comparator being wrong).
semver_lt() {
  local a="$1" b="$2"
  local a_core b_core
  a_core="${a#v}"
  b_core="${b#v}"
  # Strip any -pre/+build suffix for the purposes of this coarse
  # major.minor.patch comparison; pre-releases of the same core version
  # are rare in practice for this installer's use (comparing an
  # installed binary's version against v0.3.0) and are not
  # security-relevant here.
  a_core="${a_core%%-*}"
  b_core="${b_core%%-*}"
  a_core="${a_core%%+*}"
  b_core="${b_core%%+*}"

  case "$a_core" in
    [0-9]*.[0-9]*.[0-9]*) ;;
    *) return 1 ;;
  esac
  case "$b_core" in
    [0-9]*.[0-9]*.[0-9]*) ;;
    *) return 1 ;;
  esac

  local a_maj="${a_core%%.*}" b_maj="${b_core%%.*}"
  local a_rest="${a_core#*.}" b_rest="${b_core#*.}"
  local a_min="${a_rest%%.*}" b_min="${b_rest%%.*}"
  local a_pat="${a_rest#*.}" b_pat="${b_rest#*.}"

  case "$a_maj$a_min$a_pat$b_maj$b_min$b_pat" in
    *[!0-9]*) return 1 ;;
  esac

  if [ "$a_maj" -ne "$b_maj" ]; then
    [ "$a_maj" -lt "$b_maj" ]
    return
  fi
  if [ "$a_min" -ne "$b_min" ]; then
    [ "$a_min" -lt "$b_min" ]
    return
  fi
  [ "$a_pat" -lt "$b_pat" ]
}

# version_is_legacy VERSION — true if VERSION is unparsable by
# semver_lt's `vNUM.NUM.NUM` shape, or if it parses and is < v0.3.0 (the
# first release with the built-in `update` subcommand). An unparsable
# version (e.g. "dev", empty, a git-describe string) is conservatively
# treated as legacy: every real release tag before v0.3.0 parses fine,
# so the only way to reach "unparsable" here is a dev build or a
# previous-binary probe that failed, neither of which has the updater.
version_is_legacy() {
  local v="$1"
  if [ -z "$v" ]; then
    return 0
  fi
  local core="${v#v}"
  core="${core%%-*}"
  core="${core%%+*}"
  case "$core" in
    [0-9]*.[0-9]*.[0-9]*) ;;
    *) return 0 ;;
  esac
  semver_lt "$v" "v0.3.0"
}

# probe_existing_version BIN_PATH — run "<bin> -version" with a timeout
# and print its first whitespace-separated token (e.g. "v0.2.0" out of
# "v0.2.0 (abc1234, 2024-01-01)"), or print nothing if BIN_PATH doesn't
# exist, isn't executable, or the probe fails/times out. Never fails the
# script: this is purely informational for the upgrade-hint message.
probe_existing_version() {
  local bin_path="$1"
  if [ ! -x "$bin_path" ]; then
    return
  fi
  local out
  out="$(timeout 10 "$bin_path" -version 2>/dev/null || true)"
  # First word of the first line.
  printf '%s\n' "$out" | head -n1 | awk '{print $1}'
}

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------

parse_args() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --hub-url)
        OPT_HUB_URL="${2:?--hub-url requires an argument}"
        validate_env_value "$OPT_HUB_URL" --hub-url
        shift 2
        ;;
      --token)
        OPT_TOKEN="${2:?--token requires an argument}"
        validate_env_value "$OPT_TOKEN" --token
        shift 2
        ;;
      --host-id)
        OPT_HOST_ID="${2:?--host-id requires an argument}"
        validate_env_value "$OPT_HOST_ID" --host-id
        shift 2
        ;;
      --interval)
        OPT_INTERVAL="${2:?--interval requires an argument}"
        validate_env_value "$OPT_INTERVAL" --interval
        shift 2
        ;;
      --provider)
        OPT_PROVIDER="${2:?--provider requires an argument}"
        validate_env_value "$OPT_PROVIDER" --provider
        shift 2
        ;;
      --egress-limit-gb)
        OPT_EGRESS_LIMIT_GB="${2:?--egress-limit-gb requires an argument}"
        validate_env_value "$OPT_EGRESS_LIMIT_GB" --egress-limit-gb
        shift 2
        ;;
      --version)
        OPT_VERSION="${2:?--version requires an argument}"
        shift 2
        ;;
      --prefix)
        OPT_PREFIX="${2:?--prefix requires an argument}"
        shift 2
        ;;
      --uninstall)
        OPT_UNINSTALL=1
        shift
        ;;
      --purge)
        OPT_PURGE=1
        shift
        ;;
      --dry-run)
        OPT_DRY_RUN=1
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

# ---------------------------------------------------------------------------
# Environment detection
# ---------------------------------------------------------------------------

detect_sandbox() {
  if [ -n "$SANDBOX_ROOT" ]; then
    IS_SANDBOX=1
    mkdir -p "$SANDBOX_ROOT"
    log "sandbox mode: CP_INSTALL_ROOT=${SANDBOX_ROOT} (all system paths prefixed, root check skipped)"
  fi
}

detect_os() {
  local uname_s
  uname_s="$(uname -s)"
  if [ "$uname_s" != "Linux" ]; then
    err "cloud-pulse-agent's installer only supports Linux (systemd). Detected: ${uname_s}"
    exit 1
  fi
  OS_NAME="linux"
}

detect_arch() {
  local uname_m
  uname_m="${CP_TEST_UNAME_M:-$(uname -m)}"
  case "$uname_m" in
    x86_64|amd64)
      ARCH="amd64"
      ;;
    aarch64|arm64)
      ARCH="arm64"
      ;;
    armv7l|armv7*)
      ARCH="armv7"
      ;;
    *)
      err "unsupported CPU architecture: ${uname_m}"
      exit 1
      ;;
  esac
}

require_root_unless_sandbox() {
  if [ "$IS_SANDBOX" -eq 1 ]; then
    return
  fi
  if [ "$(id -u)" -ne 0 ]; then
    err "this script must be run as root (use sudo). For local testing without root, set CP_INSTALL_ROOT=<dir> to enable sandbox mode."
    exit 1
  fi
}

# ---------------------------------------------------------------------------
# Path setup (sandbox-aware)
# ---------------------------------------------------------------------------

setup_paths() {
  local root="${SANDBOX_ROOT}"
  BIN_DIR="${root}${OPT_PREFIX}/bin"
  ETC_DIR="${root}/etc/cloud-pulse"
  SYSTEMD_DIR="${root}/etc/systemd/system"
  ENV_FILE="${ETC_DIR}/agent.env"
  UNIT_FILE="${SYSTEMD_DIR}/cloud-pulse-agent.service"
  BIN_PATH="${BIN_DIR}/cloud-pulse-agent"
}

# ---------------------------------------------------------------------------
# Download + verify
# ---------------------------------------------------------------------------

release_base_url() {
  if [ -n "${CP_RELEASE_BASE_URL:-}" ]; then
    echo "${CP_RELEASE_BASE_URL}"
    return
  fi
  if [ -n "$OPT_VERSION" ]; then
    echo "${CP_GITHUB_BASE_URL}/releases/download/${OPT_VERSION}"
  else
    echo "${CP_GITHUB_BASE_URL}/releases/latest/download"
  fi
}

download_and_verify() {
  ASSET_NAME="${CP_ASSET_NAME_AGENT_PREFIX}-${OS_NAME}-${ARCH}"
  local base_url asset_url checksums_url
  base_url="$(release_base_url)"
  asset_url="${base_url}/${ASSET_NAME}"
  checksums_url="${base_url}/checksums.txt"

  log "downloading ${ASSET_NAME} from ${base_url}"

  fetch "$asset_url" "${TMP_DIR}/${ASSET_NAME}"
  fetch "$checksums_url" "${TMP_DIR}/checksums.txt"

  local expected actual
  expected="$(grep -F " ${ASSET_NAME}" "${TMP_DIR}/checksums.txt" | awk '{print $1}' | head -n1)"
  if [ -z "$expected" ]; then
    err "no checksum entry found for ${ASSET_NAME} in checksums.txt"
    exit 1
  fi

  actual="$(sha256_file "${TMP_DIR}/${ASSET_NAME}")"
  if [ "$expected" != "$actual" ]; then
    err "checksum mismatch for ${ASSET_NAME}: expected ${expected}, got ${actual}"
    exit 1
  fi

  log "checksum verified for ${ASSET_NAME}"
  chmod 0755 "${TMP_DIR}/${ASSET_NAME}"
}

# ---------------------------------------------------------------------------
# System user / group
# ---------------------------------------------------------------------------

ensure_system_user() {
  if [ "$IS_SANDBOX" -eq 1 ]; then
    log "sandbox: would run useradd --system --no-create-home --shell /usr/sbin/nologin -U ${CP_SERVICE_USER}"
    return
  fi
  if id "$CP_SERVICE_USER" >/dev/null 2>&1; then
    log "system user '${CP_SERVICE_USER}' already exists"
    return
  fi
  useradd --system --no-create-home --shell /usr/sbin/nologin --user-group "$CP_SERVICE_USER"
  log "created system user/group '${CP_SERVICE_USER}'"
}

# ---------------------------------------------------------------------------
# Install binary atomically
# ---------------------------------------------------------------------------

install_binary() {
  if [ -x "$BIN_PATH" ]; then
    IS_UPGRADE=1
    PREVIOUS_VERSION="$(probe_existing_version "$BIN_PATH")"
  fi
  mkdir -p "$BIN_DIR"
  # install(1) unlinks the destination and recreates it (O_CREAT|O_EXCL)
  # rather than write-then-rename, leaving a brief window where
  # BIN_PATH is absent or partially written. Install to a sibling
  # ".new" path first, then `mv -f` it over BIN_PATH so the final step
  # is a single atomic rename within the same filesystem.
  install -m 0755 "${TMP_DIR}/${ASSET_NAME}" "${BIN_PATH}.new"
  mv -f "${BIN_PATH}.new" "$BIN_PATH"
  log "installed binary to ${BIN_PATH}"
}

# ---------------------------------------------------------------------------
# agent.env rendering (preserve existing values on upgrade unless overridden)
# ---------------------------------------------------------------------------

env_get_existing() {
  local key="$1"
  if [ -f "$ENV_FILE" ]; then
    grep -E "^${key}=" "$ENV_FILE" 2>/dev/null | tail -n1 | cut -d= -f2- || true
  fi
}

resolve_required_value() {
  # resolve_required_value FLAG_VALUE ENV_KEY FLAG_NAME
  local flag_value="$1" env_key="$2" flag_name="$3"
  local existing
  existing="$(env_get_existing "$env_key")"

  if [ -n "$flag_value" ]; then
    echo "$flag_value"
    return 0
  fi
  if [ -n "$existing" ]; then
    echo "$existing"
    return 0
  fi
  err "${flag_name} is required on first install (no existing ${ENV_FILE} to read a previous value from)"
  return 1
}

write_env_file() {
  local hub_url token host_id interval provider egress_limit

  if ! hub_url="$(resolve_required_value "$OPT_HUB_URL" CP_HUB_URL --hub-url)"; then
    exit 1
  fi
  if ! token="$(resolve_required_value "$OPT_TOKEN" CP_AGENT_TOKEN --token)"; then
    exit 1
  fi

  host_id="$OPT_HOST_ID"
  if [ -z "$host_id" ]; then
    host_id="$(env_get_existing CP_HOST_ID)"
  fi

  interval="$OPT_INTERVAL"
  if [ -z "$interval" ]; then
    interval="$(env_get_existing CP_INTERVAL)"
  fi

  provider="$OPT_PROVIDER"
  if [ -z "$provider" ]; then
    provider="$(env_get_existing CP_PROVIDER)"
  fi

  egress_limit="$OPT_EGRESS_LIMIT_GB"
  if [ -z "$egress_limit" ]; then
    egress_limit="$(env_get_existing CP_EGRESS_LIMIT_GB)"
  fi

  mkdir -p "$ETC_DIR"

  local tmp_env="${TMP_DIR}/agent.env"
  {
    echo "# cloud-pulse-agent configuration. Managed by install-agent.sh;"
    echo "# manual edits are preserved across re-runs of the installer."
    echo "CP_HUB_URL=${hub_url}"
    echo "CP_AGENT_TOKEN=${token}"
    if [ -n "$host_id" ]; then
      echo "CP_HOST_ID=${host_id}"
    else
      echo "#CP_HOST_ID="
    fi
    if [ -n "$interval" ]; then
      echo "CP_INTERVAL=${interval}"
    else
      echo "#CP_INTERVAL=15s"
    fi
    if [ -n "$provider" ]; then
      echo "CP_PROVIDER=${provider}"
    else
      echo "#CP_PROVIDER=auto"
    fi
    if [ -n "$egress_limit" ]; then
      echo "CP_EGRESS_LIMIT_GB=${egress_limit}"
    else
      echo "#CP_EGRESS_LIMIT_GB="
    fi
    echo "#CP_NET_EXCLUDE=lo,lo0,docker*,veth*,br-*,virbr*,tailscale*,utun*,cni*,flannel*,cali*,kube*,vxlan*,tun*,wg*,zt*"
    echo "#CP_LOG_LEVEL=info"
  } > "$tmp_env"

  install -m 0640 "$tmp_env" "$ENV_FILE"
  if [ "$IS_SANDBOX" -eq 1 ]; then
    log "sandbox: would run chown root:${CP_SERVICE_GROUP} ${ENV_FILE}"
  else
    chown "root:${CP_SERVICE_GROUP}" "$ENV_FILE"
  fi
  log "wrote ${ENV_FILE}"

  RESOLVED_HUB_URL="$hub_url"
}
RESOLVED_HUB_URL=""

# ---------------------------------------------------------------------------
# systemd unit rendering
# ---------------------------------------------------------------------------

render_unit() {
  mkdir -p "$SYSTEMD_DIR"
  local tmp_unit="${TMP_DIR}/cloud-pulse-agent.service"
  {
    echo "[Unit]"
    echo "Description=cloud-pulse agent (host metrics collector)"
    echo "After=network-online.target"
    echo "Wants=network-online.target"
    echo
    echo "[Service]"
    echo "Type=simple"
    echo "EnvironmentFile=${ENV_FILE}"
    echo "ExecStart=${BIN_PATH}"
    echo "User=${CP_SERVICE_USER}"
    echo "Group=${CP_SERVICE_GROUP}"
    echo "Restart=on-failure"
    echo "RestartSec=5"
    echo
    echo "# --- sandboxing / hardening ---"
    echo "NoNewPrivileges=yes"
    echo "ProtectSystem=strict"
    echo "ProtectHome=read-only"
    echo "PrivateTmp=yes"
    # PrivateDevices=yes hides real /dev nodes but leaves /proc and
    # /sys mounted; gopsutil's disk/net/cpu collectors read
    # /proc/diskstats, /proc/net/dev, /proc/stat and statfs(2) on
    # mountpoints, none of which require device node access, so
    # PrivateDevices is safe here and does not break any collected
    # metric.
    echo "PrivateDevices=yes"
    echo "ProtectKernelTunables=yes"
    echo "ProtectControlGroups=yes"
    echo "RestrictSUIDSGID=yes"
    echo "LockPersonality=yes"
    echo "CapabilityBoundingSet="
    echo "AmbientCapabilities="
    # gopsutil's net.IOCounters reads /proc/net/dev directly on Linux
    # (no netlink socket involved), so AF_NETLINK is not required; the
    # agent only needs outbound TCP to the hub.
    echo "RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX"
    echo
    echo "[Install]"
    echo "WantedBy=multi-user.target"
  } > "$tmp_unit"

  install -m 0644 "$tmp_unit" "$UNIT_FILE"
  log "wrote ${UNIT_FILE}"
}

verify_unit() {
  if ! command -v systemd-analyze >/dev/null 2>&1; then
    log "systemd-analyze not available; skipping unit verification"
    return
  fi

  local verify_target="$UNIT_FILE"
  if [ "$IS_SANDBOX" -eq 1 ]; then
    verify_target="${TMP_DIR}/verify-cloud-pulse-agent.service"
    sed "s#^ExecStart=.*#ExecStart=${BIN_PATH}#" "$UNIT_FILE" > "$verify_target"
  fi

  if timeout 30 systemd-analyze verify "$verify_target" 2>&1 | tee "${TMP_DIR}/systemd-analyze.log"; then
    log "systemd-analyze verify: OK"
  else
    if grep -qiE 'unknown (lvalue|section)|failed to parse|invalid syntax' "${TMP_DIR}/systemd-analyze.log"; then
      err "systemd-analyze verify reported a fatal unit syntax error"
      exit 1
    fi
    log "systemd-analyze verify reported warnings (see above); continuing"
  fi
}

# ---------------------------------------------------------------------------
# service start
# ---------------------------------------------------------------------------

start_service() {
  if [ "$IS_SANDBOX" -eq 1 ]; then
    log "sandbox: would run systemctl daemon-reload"
    log "sandbox: would run systemctl enable --now cloud-pulse-agent.service"
    return
  fi
  systemctl daemon-reload
  systemctl enable --now cloud-pulse-agent.service
  if systemctl is-active --quiet cloud-pulse-agent.service; then
    log "cloud-pulse-agent.service is active"
  else
    err "cloud-pulse-agent.service failed to start; check: journalctl -u cloud-pulse-agent"
    exit 1
  fi
}

# ---------------------------------------------------------------------------
# post-install sanity checks (non-fatal)
# ---------------------------------------------------------------------------

run_sanity_checks() {
  if [ -x "$BIN_PATH" ]; then
    log "running sanity check: ${BIN_PATH} -once"
    if timeout 20 "$BIN_PATH" -once >/dev/null 2>"${TMP_DIR}/once.log"; then
      log "sanity check ok"
    else
      log "warning: '${BIN_PATH} -once' failed (non-fatal):"
      sed 's/^/  /' "${TMP_DIR}/once.log" || true
    fi
  fi

  if command -v curl >/dev/null 2>&1 && [ -n "$RESOLVED_HUB_URL" ]; then
    log "checking hub reachability: ${RESOLVED_HUB_URL}/healthz"
    if timeout 10 curl -fsS "${RESOLVED_HUB_URL%/}/healthz" >/dev/null 2>&1; then
      log "hub healthz ok"
    else
      log "warning: could not reach ${RESOLVED_HUB_URL%/}/healthz (non-fatal; check --hub-url and network/firewall)"
    fi
  fi
}

print_summary() {
  echo
  log "cloud-pulse-agent installed successfully."
  echo "  Hub URL:      ${RESOLVED_HUB_URL}"
  echo "  Config file:  ${ENV_FILE}"
  echo "  Service:      cloud-pulse-agent.service"
  echo
  print_version_hint
}

# print_version_hint — D-U7: on an upgrade (a binary already existed at
# BIN_PATH before install_binary replaced it), print "Upgraded vA → vB";
# always print the "Future updates" hint for the built-in `update`
# subcommand; and if the previous install predates v0.3.0 (or its
# version couldn't be determined at all), call out that this install now
# includes it.
print_version_hint() {
  local new_version
  new_version="$(probe_existing_version "$BIN_PATH")"

  if [ "$IS_UPGRADE" -eq 1 ]; then
    if [ -n "$PREVIOUS_VERSION" ] && [ -n "$new_version" ]; then
      echo "Upgraded ${PREVIOUS_VERSION} → ${new_version}"
    elif [ -n "$new_version" ]; then
      echo "Upgraded (previous version unknown) → ${new_version}"
    fi
    if version_is_legacy "$PREVIOUS_VERSION"; then
      echo "This install now includes the built-in updater."
    fi
  fi
  echo "Future updates: sudo cloud-pulse-agent update"
  echo
}

# ---------------------------------------------------------------------------
# dry-run
# ---------------------------------------------------------------------------

print_dry_run() {
  echo "[install-agent] dry-run: would perform the following actions:"
  echo "  - detect OS/arch, download ${CP_ASSET_NAME_AGENT_PREFIX}-${OS_NAME}-${ARCH} (version: ${OPT_VERSION:-latest})"
  echo "  - verify sha256 against checksums.txt"
  echo "  - install -m 0755 to ${BIN_PATH}"
  echo "  - ensure system user/group '${CP_SERVICE_USER}' exists"
  echo "  - write ${ENV_FILE} (mode 0640, owner root:${CP_SERVICE_GROUP})"
  echo "  - write ${UNIT_FILE}"
  echo "  - systemd-analyze verify the unit"
  echo "  - systemctl daemon-reload && systemctl enable --now cloud-pulse-agent.service"
  echo "  - run -once sanity check and curl \$HUB_URL/healthz (both non-fatal)"
  if [ -x "$BIN_PATH" ]; then
    local existing_version
    existing_version="$(probe_existing_version "$BIN_PATH")"
    echo "  - this is an upgrade over an existing install${existing_version:+ (currently ${existing_version})}; would print 'Upgraded vA → vB'"
    if version_is_legacy "$existing_version"; then
      echo "  - would print: This install now includes the built-in updater."
    fi
  fi
  echo "  - would print: Future updates: sudo cloud-pulse-agent update"
}

# ---------------------------------------------------------------------------
# uninstall
# ---------------------------------------------------------------------------

do_uninstall() {
  if [ "$IS_SANDBOX" -eq 1 ]; then
    log "sandbox: would run systemctl disable --now cloud-pulse-agent.service"
  else
    if command -v systemctl >/dev/null 2>&1; then
      systemctl disable --now cloud-pulse-agent.service 2>/dev/null || true
    fi
  fi

  rm -f "$UNIT_FILE"
  rm -f "$BIN_PATH"
  log "removed unit and binary"

  if [ "$IS_SANDBOX" -ne 1 ] && command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
  fi

  if [ "$OPT_PURGE" -eq 1 ]; then
    rm -f "$ENV_FILE"
    log "purged config (--purge)"
  else
    log "kept ${ENV_FILE} (pass --purge to remove it)"
  fi
}

# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------

main() {
  parse_args "$@"
  detect_sandbox
  detect_os
  detect_arch
  setup_paths
  require_root_unless_sandbox

  if [ "$OPT_UNINSTALL" -eq 1 ]; then
    if [ "$OPT_DRY_RUN" -eq 1 ]; then
      echo "[install-agent] dry-run: would uninstall (purge=${OPT_PURGE})"
      exit 0
    fi
    do_uninstall
    exit 0
  fi

  if [ "$OPT_DRY_RUN" -eq 1 ]; then
    print_dry_run
    exit 0
  fi

  require_cmd awk
  require_cmd grep

  TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/cloud-pulse-agent-install.XXXXXX")"
  trap 'rm -rf "$TMP_DIR"' EXIT

  download_and_verify
  ensure_system_user
  install_binary
  write_env_file
  render_unit
  verify_unit
  start_service
  run_sanity_checks
  print_summary
}

main "$@"
