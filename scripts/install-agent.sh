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
# Flags:
#   --install                    Fresh install. Errors (with a hint) if
#                               already installed.
#   --reinstall                   Upgrade/reinstall an existing install in
#                               place, keeping all env values. Errors
#                               (with a hint) if not installed.
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
#   --docker                  Monitor Docker (or Podman) containers: adds
#                         the agent's system user to the "docker" group
#                         (root-equivalent access to the host — see the
#                         warning printed by the interactive menu) and
#                         sets CP_DOCKER=auto. Without this flag, Docker
#                         collection still runs but reports
#                         permission_denied against the default socket
#                         (owned by root:docker) unless CP_DOCKER is set
#                         to a Podman-style rootless socket path.
#   --version vX.Y.Z        Install a specific release (default: latest).
#   --prefix DIR            Binary install prefix (default: /usr/local).
#   --uninstall              Stop/disable the service, remove the unit
#                         and binary. Config is kept unless --purge.
#   --purge                  With --uninstall, also remove
#                         /etc/cloud-pulse/agent.env.
#   -y, --yes                Assume default answers to any confirmation
#                         prompt (Y for install/reinstall, N for purge
#                         unless --purge is also given); never blocks on
#                         a prompt.
#   --dry-run                Print the actions that would be taken and
#                         exit without changing anything.
#   -h, --help                Show this help and exit.
#
# Interactive menu:
#   Running with NO arguments at all, from a real terminal (stdin/stdout
#   attached to a tty, and CP_NONINTERACTIVE is not "1"), shows a menu
#   (Install / Reinstall / Uninstall / Exit) instead of the flag-driven
#   behavior below. All menu prompts (including the hidden agent-token
#   prompt) read from /dev/tty, never stdin, so `curl ... | sudo bash`
#   (whose stdin is the script itself) still shows the menu correctly
#   when run at a terminal. Piping any flag, or running without a tty,
#   always skips the menu.
#
# Re-running this script (without --uninstall, and without a tty/menu)
# upgrades an existing install in place: the binary and systemd unit are
# replaced, but agent.env is preserved unless an explicit flag supplies
# a new value.
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
#                         "sandbox: would run ..." messages, unless
#                         CP_SYSTEMCTL explicitly names a systemctl shim (a
#                         test hook), in which case that shim runs. Downloading,
#                         checksum verification, file installation, unit
#                         rendering, and `systemd-analyze verify` (if
#                         available) still run for real against the
#                         sandboxed paths.
#   CP_TEST_UNAME_M       Override the value used in place of `uname -m`
#                         (test hook for the unknown-arch path).
#   CP_NONINTERACTIVE     Set to "1" to force non-interactive behavior
#                         (skip the menu) even when a tty is attached.
#                         Always set by scripts/test-install.sh's
#                         non-menu test cases so they never block.
#   CP_INSTALL_FORCE_SCRIPT_UNIT
#                         Set to "1" to force the bash heredoc fallback
#                         for systemd unit rendering, skipping the
#                         "$BIN_PATH systemd-unit print" call entirely
#                         (test hook for the unit-rendering drift test;
#                         also useful on a version pinned older than the
#                         binary subcommand's introduction).
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
OPT_DOCKER=0
OPT_PREFIX="/usr/local"
OPT_ACTION=""
OPT_UNINSTALL=0
OPT_PURGE=0
OPT_YES=0
OPT_DRY_RUN=0
OPT_ANY_FLAG_GIVEN=0

SANDBOX_ROOT="${CP_INSTALL_ROOT:-}"
SYSTEMCTL="${CP_SYSTEMCTL:-systemctl}"
IS_SANDBOX=0

# Set by start_service so the final summary reports the actual lifecycle
# action and the version of the binary that was launched.
SERVICE_ACTION=""
SERVICE_VERSION=""

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

# tty_available — true iff /dev/tty can be opened for both read and
# write in this process. See install-hub.sh's identical helper for the
# full rationale (piped `curl | sudo bash` invocations never read menu
# prompts from stdin).
tty_available() {
  if [ "${CP_NONINTERACTIVE:-0}" = "1" ]; then
    return 1
  fi
  { : <"/dev/tty"; } 2>/dev/null && { : >"/dev/tty"; } 2>/dev/null
}

# prompt_tty PROMPT_TEXT — print PROMPT_TEXT to /dev/tty with no
# trailing newline.
prompt_tty() {
  printf '%s' "$1" >/dev/tty
}

# read_line_tty VARNAME — read one line from /dev/tty into VARNAME.
read_line_tty() {
  local __varname="$1"
  # shellcheck disable=SC2229
  IFS= read -r "$__varname" </dev/tty
}

# read_hidden_tty VARNAME PROMPT_TEXT — prompt on /dev/tty and read one
# line from /dev/tty with echo disabled (for the agent token), printing
# a newline afterward since -s suppresses the one the terminal would
# otherwise echo.
read_hidden_tty() {
  local __varname="$1" __prompt="$2"
  prompt_tty "$__prompt"
  local status
  # shellcheck disable=SC2229
  IFS= read -rs "$__varname" </dev/tty
  status=$?
  printf '\n' >/dev/tty
  return "$status"
}

# confirm_tty PROMPT_TEXT DEFAULT_YES — see install-hub.sh's identical
# helper. If OPT_YES is set, returns DEFAULT_YES immediately without
# prompting.
confirm_tty() {
  local text="$1" default_yes="$2"
  if [ "$OPT_YES" -eq 1 ]; then
    return "$((1 - default_yes))"
  fi
  local suffix="[Y/n]"
  if [ "$default_yes" -ne 1 ]; then
    suffix="[y/N]"
  fi
  local answer
  prompt_tty "${text} ${suffix}: "
  if ! read_line_tty answer; then
    err "unexpected EOF on /dev/tty"
    exit 1
  fi
  case "$answer" in
    '') return "$((1 - default_yes))" ;;
    [Yy]|[Yy][Ee][Ss]) return 0 ;;
    [Nn]|[Nn][Oo]) return 1 ;;
    *) return "$((1 - default_yes))" ;;
  esac
}

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------

parse_args() {
  if [ "$#" -gt 0 ]; then
    OPT_ANY_FLAG_GIVEN=1
  fi
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --install)
        OPT_ACTION="install"
        shift
        ;;
      --reinstall)
        OPT_ACTION="reinstall"
        shift
        ;;
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
      --docker)
        OPT_DOCKER=1
        shift
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
      -y|--yes)
        OPT_YES=1
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
    if [ "$OPT_DOCKER" -eq 1 ]; then
      log "sandbox: would run usermod -aG docker ${CP_SERVICE_USER}"
    fi
    return
  fi
  if id "$CP_SERVICE_USER" >/dev/null 2>&1; then
    log "system user '${CP_SERVICE_USER}' already exists"
  else
    useradd --system --no-create-home --shell /usr/sbin/nologin --user-group "$CP_SERVICE_USER"
    log "created system user/group '${CP_SERVICE_USER}'"
  fi
  ensure_docker_group_membership
}

# ensure_docker_group_membership — with --docker, add CP_SERVICE_USER to
# the "docker" group (idempotent: usermod -aG is safe to re-run, and a
# missing "docker" group — Docker/Podman not installed on this host —
# is a non-fatal warning, not an install failure, since Docker
# collection degrades gracefully to permission_denied/unavailable
# either way).
ensure_docker_group_membership() {
  if [ "$OPT_DOCKER" -ne 1 ]; then
    return
  fi
  if ! getent group docker >/dev/null 2>&1; then
    log "warning: --docker given but no 'docker' group exists on this host (Docker/Podman not installed?); CP_DOCKER=auto will report permission_denied/unavailable until one does"
    return
  fi
  usermod -aG docker "$CP_SERVICE_USER"
  log "added '${CP_SERVICE_USER}' to the 'docker' group (root-equivalent access to this host — see README.md's Docker monitoring security note)"
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

resolve_optional_value() {
  # resolve_optional_value FLAG_VALUE ENV_KEY FLAG_NAME — explicit flag
  # wins, followed by the existing active env-file value. An empty result
  # means the setting remains at the binary default.
  local flag_value="$1" env_key="$2" flag_name="$3" value
  value="$flag_value"
  if [ -z "$value" ]; then
    value="$(env_get_existing "$env_key")"
  fi
  if [ -n "$value" ]; then
    validate_env_value "$value" "$flag_name"
  fi
  printf '%s\n' "$value"
}

rewrite_agent_env() {
  # Rewrite managed settings in their original positions while copying
  # comments and every unrecognised line verbatim. Commented managed keys
  # count as present and remain comments unless no active form exists and a
  # resolved non-empty value needs activating. This never sources agent.env.
  local tmp_env="$1" hub_url="$2" token="$3" host_id="$4" interval="$5"
  local provider="$6" egress_limit="$7" net_exclude="$8" time_sync="$9"
  local send_jitter="${10}" log_level="${11}" log_format="${12}" docker="${13}"
  local seen_hub=0 seen_token=0 seen_host=0 seen_interval=0 seen_provider=0
  local seen_egress=0 seen_net=0 seen_time=0 seen_jitter=0 seen_log_level=0 seen_log_format=0 seen_docker=0
  local active_hub=0 active_token=0 active_host=0 active_interval=0 active_provider=0
  local active_egress=0 active_net=0 active_time=0 active_jitter=0 active_log_level=0 active_log_format=0 active_docker=0
  local line normalized is_commented

  if [ -f "$ENV_FILE" ]; then
    # Find active keys first so an active assignment wins over any commented
    # default even if the comment appears before it in the file.
    while IFS= read -r line || [ -n "$line" ]; do
      normalized="${line#"${line%%[![:space:]]*}"}"
      case "$normalized" in
        \#*) ;;
        CP_HUB_URL=*) active_hub=1 ;;
        CP_AGENT_TOKEN=*) active_token=1 ;;
        CP_HOST_ID=*) active_host=1 ;;
        CP_INTERVAL=*) active_interval=1 ;;
        CP_PROVIDER=*) active_provider=1 ;;
        CP_EGRESS_LIMIT_GB=*) active_egress=1 ;;
        CP_NET_EXCLUDE=*) active_net=1 ;;
        CP_TIME_SYNC=*) active_time=1 ;;
        CP_SEND_JITTER=*) active_jitter=1 ;;
        CP_LOG_LEVEL=*) active_log_level=1 ;;
        CP_LOG_FORMAT=*) active_log_format=1 ;;
        CP_DOCKER=*) active_docker=1 ;;
      esac
    done < "$ENV_FILE"

    while IFS= read -r line || [ -n "$line" ]; do
      normalized="${line#"${line%%[![:space:]]*}"}"
      is_commented=0
      if [[ "$normalized" == \#* ]]; then
        is_commented=1
        normalized="${normalized#\#}"
        normalized="${normalized#"${normalized%%[![:space:]]*}"}"
      fi
      case "$normalized" in
        CP_HUB_URL=*)
          seen_hub=1
          if [ "$is_commented" -eq 1 ] && [ "$active_hub" -eq 1 ]; then printf '%s\n' "$line"; else printf 'CP_HUB_URL=%s\n' "$hub_url"; fi
          ;;
        CP_AGENT_TOKEN=*)
          seen_token=1
          if [ "$is_commented" -eq 1 ] && [ "$active_token" -eq 1 ]; then printf '%s\n' "$line"; else printf 'CP_AGENT_TOKEN=%s\n' "$token"; fi
          ;;
        CP_HOST_ID=*)
          seen_host=1
          if [ "$is_commented" -eq 1 ]; then
            if [ "$active_host" -eq 1 ] || [ -z "$host_id" ]; then printf '%s\n' "$line"; else printf 'CP_HOST_ID=%s\n' "$host_id"; fi
          else
            printf 'CP_HOST_ID=%s\n' "$host_id"
          fi
          ;;
        CP_INTERVAL=*)
          seen_interval=1
          if [ "$is_commented" -eq 1 ]; then
            if [ "$active_interval" -eq 1 ] || [ -z "$interval" ]; then printf '%s\n' "$line"; else printf 'CP_INTERVAL=%s\n' "$interval"; fi
          else
            printf 'CP_INTERVAL=%s\n' "$interval"
          fi
          ;;
        CP_PROVIDER=*)
          seen_provider=1
          if [ "$is_commented" -eq 1 ]; then
            if [ "$active_provider" -eq 1 ] || [ -z "$provider" ]; then printf '%s\n' "$line"; else printf 'CP_PROVIDER=%s\n' "$provider"; fi
          else
            printf 'CP_PROVIDER=%s\n' "$provider"
          fi
          ;;
        CP_EGRESS_LIMIT_GB=*)
          seen_egress=1
          if [ "$is_commented" -eq 1 ]; then
            if [ "$active_egress" -eq 1 ] || [ -z "$egress_limit" ]; then printf '%s\n' "$line"; else printf 'CP_EGRESS_LIMIT_GB=%s\n' "$egress_limit"; fi
          else
            printf 'CP_EGRESS_LIMIT_GB=%s\n' "$egress_limit"
          fi
          ;;
        CP_NET_EXCLUDE=*)
          seen_net=1
          if [ "$is_commented" -eq 1 ]; then
            if [ "$active_net" -eq 1 ] || [ -z "$net_exclude" ]; then printf '%s\n' "$line"; else printf 'CP_NET_EXCLUDE=%s\n' "$net_exclude"; fi
          else
            printf 'CP_NET_EXCLUDE=%s\n' "$net_exclude"
          fi
          ;;
        CP_TIME_SYNC=*)
          seen_time=1
          if [ "$is_commented" -eq 1 ]; then
            if [ "$active_time" -eq 1 ] || [ -z "$time_sync" ]; then printf '%s\n' "$line"; else printf 'CP_TIME_SYNC=%s\n' "$time_sync"; fi
          else
            printf 'CP_TIME_SYNC=%s\n' "$time_sync"
          fi
          ;;
        CP_SEND_JITTER=*)
          seen_jitter=1
          if [ "$is_commented" -eq 1 ]; then
            if [ "$active_jitter" -eq 1 ] || [ -z "$send_jitter" ]; then printf '%s\n' "$line"; else printf 'CP_SEND_JITTER=%s\n' "$send_jitter"; fi
          else
            printf 'CP_SEND_JITTER=%s\n' "$send_jitter"
          fi
          ;;
        CP_LOG_LEVEL=*)
          seen_log_level=1
          if [ "$is_commented" -eq 1 ]; then
            if [ "$active_log_level" -eq 1 ] || [ -z "$log_level" ]; then printf '%s\n' "$line"; else printf 'CP_LOG_LEVEL=%s\n' "$log_level"; fi
          else
            printf 'CP_LOG_LEVEL=%s\n' "$log_level"
          fi
          ;;
        CP_LOG_FORMAT=*)
          seen_log_format=1
          if [ "$is_commented" -eq 1 ]; then
            if [ "$active_log_format" -eq 1 ] || [ -z "$log_format" ]; then printf '%s\n' "$line"; else printf 'CP_LOG_FORMAT=%s\n' "$log_format"; fi
          else
            printf 'CP_LOG_FORMAT=%s\n' "$log_format"
          fi
          ;;
        CP_DOCKER=*)
          seen_docker=1
          if [ "$is_commented" -eq 1 ]; then
            if [ "$active_docker" -eq 1 ] || [ -z "$docker" ]; then printf '%s\n' "$line"; else printf 'CP_DOCKER=%s\n' "$docker"; fi
          else
            printf 'CP_DOCKER=%s\n' "$docker"
          fi
          ;;
        *) printf '%s\n' "$line" ;;
      esac
    done < "$ENV_FILE" > "$tmp_env"
  else
    {
      echo "# cloud-pulse-agent configuration. Managed by install-agent.sh;"
      echo "# manual edits are preserved across re-runs of the installer."
    } > "$tmp_env"
  fi

  {
    [ "$seen_hub" -eq 1 ] || printf 'CP_HUB_URL=%s\n' "$hub_url"
    [ "$seen_token" -eq 1 ] || printf 'CP_AGENT_TOKEN=%s\n' "$token"
    if [ "$seen_host" -eq 0 ]; then
      if [ -n "$host_id" ]; then printf 'CP_HOST_ID=%s\n' "$host_id"; else echo "#CP_HOST_ID="; fi
    fi
    if [ "$seen_interval" -eq 0 ]; then
      if [ -n "$interval" ]; then printf 'CP_INTERVAL=%s\n' "$interval"; else echo "#CP_INTERVAL=15s"; fi
    fi
    if [ "$seen_provider" -eq 0 ]; then
      if [ -n "$provider" ]; then printf 'CP_PROVIDER=%s\n' "$provider"; else echo "#CP_PROVIDER=auto"; fi
    fi
    if [ "$seen_egress" -eq 0 ]; then
      if [ -n "$egress_limit" ]; then printf 'CP_EGRESS_LIMIT_GB=%s\n' "$egress_limit"; else echo "#CP_EGRESS_LIMIT_GB="; fi
    fi
    [ "$seen_net" -eq 1 ] || echo "#CP_NET_EXCLUDE=lo,lo0,docker*,veth*,br-*,virbr*,tailscale*,utun*,cni*,flannel*,cali*,kube*,vxlan*,tun*,wg*,zt*"
    [ "$seen_time" -eq 1 ] || echo "#CP_TIME_SYNC=hub"
    [ "$seen_jitter" -eq 1 ] || echo "#CP_SEND_JITTER=0s"
    [ "$seen_log_level" -eq 1 ] || echo "#CP_LOG_LEVEL=info"
    [ "$seen_log_format" -eq 1 ] || echo "#CP_LOG_FORMAT=text"
    if [ "$seen_docker" -eq 0 ]; then
      if [ -n "$docker" ]; then printf 'CP_DOCKER=%s\n' "$docker"; else echo "#CP_DOCKER=auto"; fi
    fi
  } >> "$tmp_env"
}
# resolve_docker_value — --docker wins (always "auto"), else the
# existing active CP_DOCKER value from agent.env is preserved on
# upgrade, else empty (meaning: let the binary default to "auto" via an
# inactive #CP_DOCKER= comment, same convention as every other optional
# setting in this file).
resolve_docker_value() {
  if [ "$OPT_DOCKER" -eq 1 ]; then
    echo "auto"
    return
  fi
  env_get_existing CP_DOCKER
}


write_env_file() {
  local hub_url token host_id interval provider egress_limit net_exclude time_sync send_jitter log_level log_format docker

  if ! hub_url="$(resolve_required_value "$OPT_HUB_URL" CP_HUB_URL --hub-url)"; then exit 1; fi
  if ! token="$(resolve_required_value "$OPT_TOKEN" CP_AGENT_TOKEN --token)"; then exit 1; fi
  validate_env_value "$hub_url" --hub-url
  validate_env_value "$token" --token
  host_id="$(resolve_optional_value "$OPT_HOST_ID" CP_HOST_ID --host-id)"
  interval="$(resolve_optional_value "$OPT_INTERVAL" CP_INTERVAL --interval)"
  provider="$(resolve_optional_value "$OPT_PROVIDER" CP_PROVIDER --provider)"
  egress_limit="$(resolve_optional_value "$OPT_EGRESS_LIMIT_GB" CP_EGRESS_LIMIT_GB --egress-limit-gb)"
  net_exclude="$(resolve_optional_value "" CP_NET_EXCLUDE CP_NET_EXCLUDE)"
  time_sync="$(resolve_optional_value "" CP_TIME_SYNC CP_TIME_SYNC)"
  send_jitter="$(resolve_optional_value "" CP_SEND_JITTER CP_SEND_JITTER)"
  log_level="$(resolve_optional_value "" CP_LOG_LEVEL CP_LOG_LEVEL)"
  log_format="$(resolve_optional_value "" CP_LOG_FORMAT CP_LOG_FORMAT)"
  docker="$(resolve_docker_value)"

  mkdir -p "$ETC_DIR"
  local tmp_env="${TMP_DIR}/agent.env"
  rewrite_agent_env "$tmp_env" "$hub_url" "$token" "$host_id" "$interval" "$provider" "$egress_limit" "$net_exclude" "$time_sync" "$send_jitter" "$log_level" "$log_format" "$docker"

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

  if [ "${CP_INSTALL_FORCE_SCRIPT_UNIT:-0}" != "1" ] && render_unit_via_binary "$tmp_unit"; then
    log "rendered ${UNIT_FILE} via '${BIN_PATH} systemd-unit print'"
  else
    render_unit_fallback "$tmp_unit"
    log "rendered ${UNIT_FILE} via built-in fallback template"
  fi

  install -m 0644 "$tmp_unit" "$UNIT_FILE"
  log "wrote ${UNIT_FILE}"
}

# detect_preserved_docker_flag — if this is a re-run (reinstall/upgrade)
# that doesn't re-pass --docker, but the existing unit file already has
# SupplementaryGroups=docker, treat this run as if --docker had been
# given. Matches this script's "keeps settings" reinstall philosophy
# (README.md's Upgrading section): a one-time --docker is meant to
# persist across upgrades, not need repeating on every future re-run.
# Must run before ensure_system_user so the docker-group membership is
# (re-)applied consistently with the unit file every time, not just on
# the run --docker was first given.
detect_preserved_docker_flag() {
  if [ "$OPT_DOCKER" -eq 1 ]; then
    return
  fi
  if [ -f "$UNIT_FILE" ] && grep -q '^SupplementaryGroups=.*docker' "$UNIT_FILE" 2>/dev/null; then
    OPT_DOCKER=1
    log "preserving existing --docker setting from ${UNIT_FILE} (SupplementaryGroups=docker)"
  fi
}

# render_unit_via_binary OUT_PATH — ask the freshly installed agent
# binary to render its own systemd unit (SPEC-v0.3.1 B,
# internal/systemdunit). See install-hub.sh's identical helper for the
# full rationale. Returns non-zero if the binary doesn't support the
# subcommand yet; the caller falls back to render_unit_fallback.
render_unit_via_binary() {
  local out_path="$1"
  if [ ! -x "$BIN_PATH" ]; then
    return 1
  fi
  if [ "$OPT_DOCKER" -eq 1 ]; then
    timeout 10 "$BIN_PATH" systemd-unit print --bin-path "$BIN_PATH" --env-file "$ENV_FILE" \
      --user "$CP_SERVICE_USER" --group "$CP_SERVICE_GROUP" --supplementary-groups docker >"$out_path" 2>/dev/null
  else
    timeout 10 "$BIN_PATH" systemd-unit print --bin-path "$BIN_PATH" --env-file "$ENV_FILE" \
      --user "$CP_SERVICE_USER" --group "$CP_SERVICE_GROUP" >"$out_path" 2>/dev/null
  fi
}

# render_unit_fallback OUT_PATH — bash heredoc fallback kept
# byte-identical to internal/systemdunit.Render's agent template (see
# SPEC-v0.3.1 B and the drift test in test-install.sh). A
# SupplementaryGroups=docker line is added right after Group= when
# --docker was given (SPEC-v0.5 §C), matching Render's own placement.
render_unit_fallback() {
  local out_path="$1"
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
    if [ "$OPT_DOCKER" -eq 1 ]; then
      echo "SupplementaryGroups=docker"
    fi
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
  } > "$out_path"
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
  local service="cloud-pulse-agent.service"
  local previous_pid="" previous_started="" current_pid="" current_started=""
  local attempts=30

  if [ "$IS_SANDBOX" -eq 1 ] && [ -z "${CP_SYSTEMCTL:-}" ]; then
    log "sandbox: would run ${SYSTEMCTL} daemon-reload"
    log "sandbox: would run ${SYSTEMCTL} enable ${service}"
    log "sandbox: would run ${SYSTEMCTL} start ${service} (or restart if already running)"
    return
  fi

  "$SYSTEMCTL" daemon-reload
  "$SYSTEMCTL" enable "$service"
  if "$SYSTEMCTL" is-active --quiet "$service"; then
    SERVICE_ACTION="restarted"
    previous_pid="$("$SYSTEMCTL" show -p MainPID --value "$service" 2>/dev/null || true)"
    previous_started="$("$SYSTEMCTL" show -p ExecMainStartTimestampMonotonic --value "$service" 2>/dev/null || true)"
    "$SYSTEMCTL" restart "$service"
  else
    SERVICE_ACTION="started"
    "$SYSTEMCTL" start "$service"
  fi

  while [ "$attempts" -gt 0 ]; do
    attempts=$((attempts - 1))
    if "$SYSTEMCTL" is-active --quiet "$service"; then
      if [ "$SERVICE_ACTION" = "started" ]; then
        SERVICE_VERSION="$(probe_existing_version "$BIN_PATH")"
        return
      fi
      current_pid="$("$SYSTEMCTL" show -p MainPID --value "$service" 2>/dev/null || true)"
      current_started="$("$SYSTEMCTL" show -p ExecMainStartTimestampMonotonic --value "$service" 2>/dev/null || true)"
      if [ "$current_pid" != "$previous_pid" ] && [ "$current_started" != "$previous_started" ]; then
        SERVICE_VERSION="$(probe_existing_version "$BIN_PATH")"
        return
      fi
    fi
    sleep 0.5
  done

  err "${service} failed to ${SERVICE_ACTION}; check: journalctl -u cloud-pulse-agent"
  exit 1
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
  if [ -n "$SERVICE_ACTION" ]; then
    echo "  Service:      cloud-pulse-agent.service ${SERVICE_ACTION} (running ${SERVICE_VERSION})"
  else
    echo "  Service:      cloud-pulse-agent.service"
  fi
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
      if [ "$PREVIOUS_VERSION" = "$new_version" ]; then
        echo "Reinstalled ${new_version}"
      elif semver_lt "$new_version" "$PREVIOUS_VERSION"; then
        echo "Downgraded ${PREVIOUS_VERSION} → ${new_version}"
      else
        echo "Upgraded ${PREVIOUS_VERSION} → ${new_version}"
      fi
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
  if [ "$OPT_DOCKER" -eq 1 ]; then
    echo "  - add '${CP_SERVICE_USER}' to the 'docker' group (--docker: root-equivalent access)"
  fi
  echo "  - write ${ENV_FILE} (mode 0640, owner root:${CP_SERVICE_GROUP})"
  echo "  - write ${UNIT_FILE}"
  echo "  - systemd-analyze verify the unit"
  echo "  - ${SYSTEMCTL} daemon-reload && ${SYSTEMCTL} enable cloud-pulse-agent.service"
  echo "  - start cloud-pulse-agent.service if inactive; restart it if already running"
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
# Interactive menu (SPEC-v0.3.1 A)
# ---------------------------------------------------------------------------

# is_installed — true iff an agent binary is currently installed at
# BIN_PATH (setup_paths must have already run).
is_installed() {
  [ -x "$BIN_PATH" ]
}

# service_status_text — human-readable "(service: active|inactive|...)"
# suffix for the menu's status line. Best-effort only.
service_status_text() {
  if [ "$IS_SANDBOX" -eq 1 ]; then
    echo ""
    return
  fi
  if ! command -v "$SYSTEMCTL" >/dev/null 2>&1; then
    echo ""
    return
  fi
  local state
  state="$("$SYSTEMCTL" is-active cloud-pulse-agent.service 2>/dev/null || true)"
  if [ -n "$state" ]; then
    echo " (service: ${state})"
  else
    echo ""
  fi
}

# prompt_fresh_install_values — interactively collect the values a fresh
# agent install needs (SPEC-v0.3.1 A "1) Install" prompts): hub URL,
# hidden agent token, and optional host ID. Same validate_env_value
# checks as the equivalent flags.
prompt_fresh_install_values() {
  local answer sanitized_default

  prompt_tty "Hub URL (e.g. http://100.x.y.z:8090): "
  if ! read_line_tty answer; then
    err "unexpected EOF on /dev/tty"
    exit 1
  fi
  case "$answer" in
    http://*|https://*) ;;
    *)
      err "hub URL must start with http:// or https://"
      exit 1
      ;;
  esac
  validate_env_value "$answer" --hub-url
  OPT_HUB_URL="$answer"

  if ! read_hidden_tty answer "Agent token (input hidden): "; then
    err "unexpected EOF on /dev/tty"
    exit 1
  fi
  if [ "${#answer}" -lt 16 ]; then
    err "agent token must be at least 16 characters"
    exit 1
  fi
  validate_env_value "$answer" --token
  OPT_TOKEN="$answer"

  sanitized_default="$(hostname 2>/dev/null | tr -c 'A-Za-z0-9._-' '-' || true)"
  prompt_tty "Host ID [${sanitized_default:-<hostname>}]: "
  if ! read_line_tty answer; then
    err "unexpected EOF on /dev/tty"
    exit 1
  fi
  if [ -n "$answer" ]; then
    validate_env_value "$answer" --host-id
    OPT_HOST_ID="$answer"
  fi

  prompt_tty "Monitor Docker containers? (adds the agent to the docker group; docker group access is root-equivalent) [y/N]: "
  if ! read_line_tty answer; then
    err "unexpected EOF on /dev/tty"
    exit 1
  fi
  case "$answer" in
    [Yy]|[Yy][Ee][Ss]) OPT_DOCKER=1 ;;
    *) ;;
  esac
}

print_menu() {
  local status_line
  if is_installed; then
    local v
    v="$(probe_existing_version "$BIN_PATH")"
    status_line="installed ${v:-(unknown version)}$(service_status_text)"
  else
    status_line="not installed"
  fi
  {
    echo "cloud-pulse agent installer"
    echo "  Status: ${status_line}"
    echo "  1) Install      (설치)"
    echo "  2) Reinstall    (재설치: latest version, keeps settings, tokens and data)"
    echo "  3) Uninstall    (삭제)"
    echo "  0) Exit"
  } >/dev/tty
}

# run_menu — show the menu and read a single choice from /dev/tty,
# re-prompting on invalid input up to 5 times.
run_menu() {
  local tries=0 choice
  while [ "$tries" -lt 5 ]; do
    print_menu
    prompt_tty "Select [1-3, 0]: "
    if ! read_line_tty choice; then
      err "unexpected EOF on /dev/tty"
      exit 1
    fi
    case "$choice" in
      1) OPT_ACTION="install"; return ;;
      2) OPT_ACTION="reinstall"; return ;;
      3) OPT_ACTION="uninstall"; return ;;
      0) exit 0 ;;
      *)
        tries=$((tries + 1))
        echo "Invalid choice: ${choice}" >/dev/tty
        ;;
    esac
  done
  err "too many invalid menu selections"
  exit 1
}

menu_loop() {
  while :; do
    run_menu
    case "$OPT_ACTION" in
      install)
        if is_installed; then
          echo >/dev/tty
          echo "cloud-pulse-agent is already installed. Choose 2 to reinstall/upgrade, or run 'sudo cloud-pulse-agent update' on v0.3.0+." >/dev/tty
          echo >/dev/tty
          OPT_ACTION=""
          continue
        fi
        prompt_fresh_install_values
        run_install_flow
        exit 0
        ;;
      reinstall)
        if ! is_installed; then
          echo >/dev/tty
          echo "cloud-pulse-agent is not installed. Choose 1 to install." >/dev/tty
          echo >/dev/tty
          OPT_ACTION=""
          continue
        fi
        local cur target
        cur="$(probe_existing_version "$BIN_PATH")"
        target="${OPT_VERSION:-latest}"
        echo >/dev/tty
        echo "Current version: ${cur:-unknown}" >/dev/tty
        echo "Target version:  ${target}" >/dev/tty
        if ! confirm_tty "Continue?" 1; then
          OPT_ACTION=""
          continue
        fi
        run_install_flow
        exit 0
        ;;
      uninstall)
        if ! confirm_tty "Uninstall cloud-pulse-agent?" 0; then
          OPT_ACTION=""
          continue
        fi
        if confirm_tty "Also delete configuration and tokens (${ENV_FILE})?" 0; then
          OPT_PURGE=1
        fi
        do_uninstall
        exit 0
        ;;
    esac
  done
}

# ---------------------------------------------------------------------------
# uninstall
# ---------------------------------------------------------------------------

do_uninstall() {
  if [ "$IS_SANDBOX" -eq 1 ] && [ -z "${CP_SYSTEMCTL:-}" ]; then
    log "sandbox: would run ${SYSTEMCTL} stop cloud-pulse-agent.service"
    log "sandbox: would run ${SYSTEMCTL} disable cloud-pulse-agent.service"
  elif command -v "$SYSTEMCTL" >/dev/null 2>&1; then
    "$SYSTEMCTL" stop cloud-pulse-agent.service 2>/dev/null || true
    "$SYSTEMCTL" disable cloud-pulse-agent.service 2>/dev/null || true
  fi

  rm -f "$UNIT_FILE"
  rm -f "$BIN_PATH"
  log "removed unit and binary"

  if { [ "$IS_SANDBOX" -ne 1 ] || [ -n "${CP_SYSTEMCTL:-}" ]; } && command -v "$SYSTEMCTL" >/dev/null 2>&1; then
    "$SYSTEMCTL" daemon-reload || true
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

# run_install_flow — the actual download/install/start sequence, shared
# by the flag-driven path and the interactive menu.
run_install_flow() {
  require_cmd awk
  require_cmd grep

  TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/cloud-pulse-agent-install.XXXXXX")"
  trap 'rm -rf "$TMP_DIR"' EXIT

  detect_preserved_docker_flag
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

  # Menu only when: no arguments at all AND interactive. See
  # install-hub.sh's identical check for the full rationale.
  if [ "$OPT_ANY_FLAG_GIVEN" -eq 0 ] && tty_available; then
    menu_loop
    return
  fi

  case "$OPT_ACTION" in
    install)
      if is_installed; then
        err "already installed; choose --reinstall to upgrade, or run 'sudo cloud-pulse-agent update' on v0.3.0+"
        exit 1
      fi
      ;;
    reinstall)
      if ! is_installed; then
        err "not installed; use --install for a fresh install (requires --hub-url/--token)"
        exit 1
      fi
      ;;
    "")
      : # auto: fall through to run_install_flow regardless of state
      ;;
  esac

  run_install_flow
}

main "$@"
