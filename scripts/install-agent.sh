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
#   --remote-update           Opt this agent into the hub's remote
#                         batch-update feature (SPEC-v0.6 §2): sets
#                         CP_REMOTE_UPDATE=on, adds
#                         StateDirectory=cloud-pulse-agent to the unit,
#                         and installs+enables
#                         cloud-pulse-agent-update.path (which triggers a
#                         root oneshot cloud-pulse-agent-update.service
#                         running `cloud-pulse-agent update
#                         --from-request` whenever the hub delivers a
#                         request). The hub can still only ask this
#                         agent to move to a tag from its own
#                         already-trusted release feed — see
#                         internal/agent/remoteupdate.go's security
#                         model doc comment. Without this flag, remote
#                         updates stay off (the default) and any
#                         hub-side attempt to update this host reports
#                         failed/not_enabled.
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
#                         When explicitly set, it is persisted in agent.env
#                         for remote-update helpers (mirrors/air-gapped).
#   CP_UPDATE_LATEST_URL  Override the latest-release URL. When explicitly
#                         set, it is persisted alongside CP_RELEASE_BASE_URL
#                         for remote-update helpers (mirrors/air-gapped).
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
#   CP_TEST_UNAME_S       Override the value used in place of `uname -s`
#                         for platform detection (test hook: set to
#                         "Darwin" to exercise the darwin-launchd branch
#                         from a non-macOS host — see
#                         scripts/test-install.sh's darwin sandbox
#                         tests).
#   CP_LAUNCHCTL          Override the `launchctl` binary path
#                         (darwin-launchd only; test hook, mirrors
#                         CP_SYSTEMCTL).
#   CP_DSCL               Override the `dscl` binary path
#                         (darwin-launchd only; test hook).
#   CP_CHOWN              Override the `chown` binary path
#                         (darwin-launchd sandbox test hook).
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

# macOS-only (darwin-launchd platform, SPEC-v0.7 §1): dedicated hidden
# system user the LaunchDaemon runs as, and the fixed launchd job
# label/paths — see internal/launchd's identical Go-side constants.
CP_DARWIN_USER="_cloudpulse"
CP_DARWIN_GROUP="_cloudpulse"
CP_LAUNCHD_LABEL="com.cloudpulse.agent"
CP_LAUNCHD_UPDATE_LABEL="com.cloudpulse.agent-update"

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
OPT_REMOTE_UPDATE=0
OPT_PREFIX="/usr/local"
OPT_ACTION=""
OPT_UNINSTALL=0
OPT_PURGE=0
OPT_YES=0
OPT_DRY_RUN=0
OPT_ANY_FLAG_GIVEN=0

SANDBOX_ROOT="${CP_INSTALL_ROOT:-}"
SYSTEMCTL="${CP_SYSTEMCTL:-systemctl}"
LAUNCHCTL="${CP_LAUNCHCTL:-launchctl}"
DSCL="${CP_DSCL:-dscl}"
CHOWN="${CP_CHOWN:-chown}"
IS_SANDBOX=0

# PLATFORM is set by detect_platform: "linux-systemd", "darwin-launchd",
# or "unsupported". Every later branch that differs between systemd and
# launchd (system-user creation, unit/plist rendering, service
# start/stop, uninstall) dispatches on this value rather than re-testing
# `uname` — detect_platform is the single place OS detection happens.
PLATFORM=""

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
UPDATE_PATH_UNIT_FILE=""
UPDATE_SERVICE_UNIT_FILE=""
UPDATE_STATE_DIR=""
UPDATE_REQUEST_FILE=""
UPDATE_RESULT_DIR=""

# darwin-launchd-only path globals (SPEC-v0.7 §1); left empty and
# unused on linux-systemd.
PLIST_PATH=""
UPDATE_PLIST_PATH=""
LOG_PATH=""

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

# run_with_timeout SECONDS CMD... — use GNU timeout when available, then
# Homebrew coreutils' gtimeout. Stock macOS provides neither, so run the
# command directly rather than making a supported macOS install fail merely
# because a diagnostic/rendering timeout utility is absent. All callers still
# receive the command's real exit status.
run_with_timeout() {
  local seconds="$1"
  shift
  if command -v timeout >/dev/null 2>&1; then
    timeout "$seconds" "$@"
  elif command -v gtimeout >/dev/null 2>&1; then
    gtimeout "$seconds" "$@"
  else
    "$@"
  fi
}

xml_escape() {
  # All caller values are installer-controlled paths/labels, but XML escaping
  # keeps fallback plists valid and byte-identical to internal/launchd.Render
  # when a sandbox prefix includes XML-significant characters.
  printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' -e 's/"/\&quot;/g' -e "s/'/\&apos;/g"
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
  out="$(run_with_timeout 10 "$bin_path" -version 2>/dev/null || true)"
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
      --remote-update)
        OPT_REMOTE_UPDATE=1
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

detect_platform() {
  local uname_s
  uname_s="${CP_TEST_UNAME_S:-$(uname -s)}"
  case "$uname_s" in
    Linux)
      PLATFORM="linux-systemd"
      OS_NAME="linux"
      ;;
    Darwin)
      PLATFORM="darwin-launchd"
      OS_NAME="darwin"
      ;;
    *)
      PLATFORM="unsupported"
      OS_NAME=""
      ;;
  esac
  if [ "$PLATFORM" = "unsupported" ]; then
    case "$uname_s" in
      MINGW*|MSYS*|CYGWIN*)
        err "this script does not support Windows. Use scripts/install-agent.ps1 instead:"
        err "  irm https://raw.githubusercontent.com/${CP_REPO}/main/scripts/install-agent.ps1 | iex"
        ;;
      *)
        err "cloud-pulse-agent's installer only supports Linux (systemd) and macOS (launchd). Detected: ${uname_s}"
        ;;
    esac
    exit 1
  fi
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
      if [ "$PLATFORM" = "darwin-launchd" ]; then
        err "unsupported CPU architecture for macOS: ${uname_m} (no armv7 release exists for darwin)"
        exit 1
      fi
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
  if [ "$PLATFORM" = "darwin-launchd" ]; then
    setup_paths_darwin "$root"
    return
  fi
  BIN_DIR="${root}${OPT_PREFIX}/bin"
  ETC_DIR="${root}/etc/cloud-pulse"
  SYSTEMD_DIR="${root}/etc/systemd/system"
  ENV_FILE="${ETC_DIR}/agent.env"
  UNIT_FILE="${SYSTEMD_DIR}/cloud-pulse-agent.service"
  BIN_PATH="${BIN_DIR}/cloud-pulse-agent"
  UPDATE_PATH_UNIT_FILE="${SYSTEMD_DIR}/cloud-pulse-agent-update.path"
  UPDATE_SERVICE_UNIT_FILE="${SYSTEMD_DIR}/cloud-pulse-agent-update.service"
  UPDATE_STATE_DIR="${root}/var/lib/cloud-pulse-agent"
  UPDATE_REQUEST_FILE="${UPDATE_STATE_DIR}/update-request.json"
  UPDATE_RESULT_DIR="${root}/var/lib/cloud-pulse-agent-update"
}

# setup_paths_darwin — SPEC-v0.7 §1's fixed macOS path table:
#   /usr/local/bin/cloud-pulse-agent            (root:wheel 0755)
#   /usr/local/etc/cloud-pulse/agent.env        (root:_cloudpulse 0640)
#   /Library/Application Support/cloud-pulse-agent/   (request file, _cloudpulse 0750)
#   /Library/Application Support/cloud-pulse-agent-update/  (result, root 0755)
#   /Library/LaunchDaemons/com.cloudpulse.agent.plist
#   /Library/LaunchDaemons/com.cloudpulse.agent-update.plist
#   /Library/Logs/cloud-pulse-agent.log
setup_paths_darwin() {
  local root="$1"
  BIN_DIR="${root}${OPT_PREFIX}/bin"
  ETC_DIR="${root}${OPT_PREFIX}/etc/cloud-pulse"
  ENV_FILE="${ETC_DIR}/agent.env"
  BIN_PATH="${BIN_DIR}/cloud-pulse-agent"
  PLIST_PATH="${root}/Library/LaunchDaemons/${CP_LAUNCHD_LABEL}.plist"
  UPDATE_PLIST_PATH="${root}/Library/LaunchDaemons/${CP_LAUNCHD_UPDATE_LABEL}.plist"
  UPDATE_STATE_DIR="${root}/Library/Application Support/cloud-pulse-agent"
  UPDATE_REQUEST_FILE="${UPDATE_STATE_DIR}/update-request.json"
  UPDATE_RESULT_DIR="${root}/Library/Application Support/cloud-pulse-agent-update"
  LOG_PATH="${root}/Library/Logs/cloud-pulse-agent.log"
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
  if [ "$PLATFORM" = "darwin-launchd" ]; then
    ensure_system_user_darwin
    return
  fi
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

# darwin_first_free_id USED_IDS PREFERRED — print an unused ID in the
# dedicated 200-400 system-account range. PREFERRED (normally the chosen
# user UID) is selected when available so the user and private group share an
# ID; otherwise the first free ID is used. This uses shell arithmetic rather
# than seq, which is not guaranteed by stock macOS.
darwin_first_free_id() {
  local used_ids="$1" preferred="$2" candidate
  case "$preferred" in
    ''|*[!0-9]*) ;;
    *)
      if [ "$preferred" -ge 200 ] && [ "$preferred" -le 400 ] && \
          ! printf '%s\n' "$used_ids" | grep -qx "$preferred"; then
        printf '%s\n' "$preferred"
        return
      fi
      ;;
  esac
  candidate=200
  while [ "$candidate" -le 400 ]; do
    if ! printf '%s\n' "$used_ids" | grep -qx "$candidate"; then
      printf '%s\n' "$candidate"
      return
    fi
    candidate=$((candidate + 1))
  done
  return 1
}

# darwin_chown OWNER PATH — preserve real ownership enforcement while letting
# the Linux-run darwin sandbox supply CP_CHOWN and record every ownership
# decision. Without that hook sandbox mode remains side-effect free.
darwin_chown() {
  local owner="$1" path="$2"
  if [ "$IS_SANDBOX" -eq 1 ] && [ -z "${CP_CHOWN:-}" ]; then
    log "sandbox: would run chown ${owner} ${path}"
    return
  fi
  "$CHOWN" "$owner" "$path"
}

# ensure_system_user_darwin — creates/reuses a hidden _cloudpulse user and
# its private hidden _cloudpulse group. The private group is essential: using
# macOS's staff (GID 20) would expose the token-bearing agent.env to every
# normal login account. IDs are selected independently from 200-400, with the
# group preferring the user's UID when that GID is available.
ensure_system_user_darwin() {
  if [ "$OPT_DOCKER" -eq 1 ]; then
    log "warning: --docker has no effect on macOS (Docker Desktop is not the Linux docker-group model); ignoring"
  fi
  if [ "$IS_SANDBOX" -eq 1 ] && [ -z "${CP_DSCL:-}" ]; then
    log "sandbox: would create hidden system user/group ${CP_DARWIN_USER}/${CP_DARWIN_GROUP} (UID/GID 200-400)"
    return
  fi

  local user_exists=0 group_exists=0 user_uid="" user_primary_gid="" group_gid="" used_uids used_gids
  if "$DSCL" . -read "/Users/${CP_DARWIN_USER}" UniqueID >/dev/null 2>&1; then
    user_exists=1
    user_uid="$("$DSCL" . -read "/Users/${CP_DARWIN_USER}" UniqueID | awk '/^UniqueID: / { print $2; exit }')"
    user_primary_gid="$("$DSCL" . -read "/Users/${CP_DARWIN_USER}" PrimaryGroupID 2>/dev/null | awk '/^PrimaryGroupID: / { print $2; exit }' || true)"
  fi
  if "$DSCL" . -read "/Groups/${CP_DARWIN_GROUP}" PrimaryGroupID >/dev/null 2>&1; then
    group_exists=1
    group_gid="$("$DSCL" . -read "/Groups/${CP_DARWIN_GROUP}" PrimaryGroupID | awk '/^PrimaryGroupID: / { print $2; exit }')"
  fi

  used_uids="$("$DSCL" . -list /Users UniqueID 2>/dev/null | awk '{print $2}' || true)"
  used_gids="$("$DSCL" . -list /Groups PrimaryGroupID 2>/dev/null | awk '{print $2}' || true)"

  if [ "$user_exists" -eq 0 ]; then
    user_uid="$(darwin_first_free_id "$used_uids" "")" || {
      err "no unused UID available in the 200-400 system-account range for ${CP_DARWIN_USER}"
      exit 1
    }
  fi
  if [ "$group_exists" -eq 0 ]; then
    group_gid="$(darwin_first_free_id "$used_gids" "$user_uid")" || {
      err "no unused GID available in the 200-400 system-account range for ${CP_DARWIN_GROUP}"
      exit 1
    }
    "$DSCL" . -create "/Groups/${CP_DARWIN_GROUP}"
    "$DSCL" . -create "/Groups/${CP_DARWIN_GROUP}" RecordName "$CP_DARWIN_GROUP"
    "$DSCL" . -create "/Groups/${CP_DARWIN_GROUP}" RealName "cloud-pulse agent"
    "$DSCL" . -create "/Groups/${CP_DARWIN_GROUP}" Password '*'
    "$DSCL" . -create "/Groups/${CP_DARWIN_GROUP}" PrimaryGroupID "$group_gid"
    log "created hidden system group '${CP_DARWIN_GROUP}' (GID ${group_gid})"
  else
    log "system group '${CP_DARWIN_GROUP}' already exists (GID ${group_gid})"
  fi

  if [ "$user_exists" -eq 0 ]; then
    "$DSCL" . -create "/Users/${CP_DARWIN_USER}"
    "$DSCL" . -create "/Users/${CP_DARWIN_USER}" UserShell /usr/bin/false
    "$DSCL" . -create "/Users/${CP_DARWIN_USER}" RealName "cloud-pulse agent"
    "$DSCL" . -create "/Users/${CP_DARWIN_USER}" UniqueID "$user_uid"
    "$DSCL" . -create "/Users/${CP_DARWIN_USER}" NFSHomeDirectory /var/empty
    "$DSCL" . -create "/Users/${CP_DARWIN_USER}" IsHidden 1
    log "created hidden system user '${CP_DARWIN_USER}' (UID ${user_uid})"
  else
    log "system user '${CP_DARWIN_USER}' already exists"
  fi
  # Set even for a reused account: existing pre-fix installs used staff and
  # must be migrated to the private group before agent.env is written. Use
  # -change when the attribute exists so this replaces (rather than appends
  # to) its scalar Directory Services value.
  if [ -n "$user_primary_gid" ]; then
    if [ "$user_primary_gid" != "$group_gid" ]; then
      "$DSCL" . -change "/Users/${CP_DARWIN_USER}" PrimaryGroupID "$user_primary_gid" "$group_gid"
    fi
  else
    "$DSCL" . -create "/Users/${CP_DARWIN_USER}" PrimaryGroupID "$group_gid"
  fi
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
  local send_jitter="${10}" log_level="${11}" log_format="${12}" docker="${13}" remote_update="${14}"
  local seen_hub=0 seen_token=0 seen_host=0 seen_interval=0 seen_provider=0
  local seen_egress=0 seen_net=0 seen_time=0 seen_jitter=0 seen_log_level=0 seen_log_format=0 seen_docker=0 seen_remote_update=0
  local active_hub=0 active_token=0 active_host=0 active_interval=0 active_provider=0
  local active_egress=0 active_net=0 active_time=0 active_jitter=0 active_log_level=0 active_log_format=0 active_docker=0 active_remote_update=0
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
        CP_REMOTE_UPDATE=*) active_remote_update=1 ;;
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
        CP_REMOTE_UPDATE=*)
          seen_remote_update=1
          if [ "$is_commented" -eq 1 ]; then
            if [ "$active_remote_update" -eq 1 ] || [ -z "$remote_update" ]; then printf '%s\n' "$line"; else printf 'CP_REMOTE_UPDATE=%s\n' "$remote_update"; fi
          else
            printf 'CP_REMOTE_UPDATE=%s\n' "$remote_update"
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
    if [ "$seen_remote_update" -eq 0 ]; then
      if [ -n "$remote_update" ]; then printf 'CP_REMOTE_UPDATE=%s\n' "$remote_update"; else echo "#CP_REMOTE_UPDATE=off"; fi
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

# resolve_remote_update_value — --remote-update wins (always "on"),
# else the existing active CP_REMOTE_UPDATE value from agent.env is
# preserved on upgrade, else empty (meaning: let the binary default to
# "off" via an inactive #CP_REMOTE_UPDATE= comment). Same shape as
# resolve_docker_value, deliberately: SPEC-v0.6 §2 opts in exactly the
# same way --docker does.
resolve_remote_update_value() {
  if [ "$OPT_REMOTE_UPDATE" -eq 1 ]; then
    echo "on"
    return
  fi
  env_get_existing CP_REMOTE_UPDATE
}

# persist_update_source_override KEY VALUE FILE — CP_RELEASE_BASE_URL and
# CP_UPDATE_LATEST_URL are deliberately persisted only when explicitly set
# in the installer environment. This supports mirrors/air-gapped installs
# without overwriting an existing source on a later ordinary reinstall.
persist_update_source_override() {
  local key="$1" value="$2" path="$3" tmp_path="${TMP_DIR}/agent.env.source"
  if [ -z "$value" ]; then
    return
  fi
  validate_env_value "$value" "$key"
  awk -v key="$key" 'index($0, key "=") != 1 { print }' "$path" > "$tmp_path"
  printf '%s=%s\n' "$key" "$value" >> "$tmp_path"
  mv "$tmp_path" "$path"
  log "persisted ${key} in ${ENV_FILE} for privileged update helpers"
}

write_env_file() {
  local hub_url token host_id interval provider egress_limit net_exclude time_sync send_jitter log_level log_format docker remote_update

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
  remote_update="$(resolve_remote_update_value)"

  mkdir -p "$ETC_DIR"
  local tmp_env="${TMP_DIR}/agent.env"
  rewrite_agent_env "$tmp_env" "$hub_url" "$token" "$host_id" "$interval" "$provider" "$egress_limit" "$net_exclude" "$time_sync" "$send_jitter" "$log_level" "$log_format" "$docker" "$remote_update"
  persist_update_source_override CP_RELEASE_BASE_URL "${CP_RELEASE_BASE_URL:-}" "$tmp_env"
  persist_update_source_override CP_UPDATE_LATEST_URL "${CP_UPDATE_LATEST_URL:-}" "$tmp_env"

  install -m 0640 "$tmp_env" "$ENV_FILE"
  local env_group
  env_group="$(env_file_group)"
  darwin_chown "root:${env_group}" "$ENV_FILE"
  log "wrote ${ENV_FILE}"
  RESOLVED_HUB_URL="$hub_url"
}
RESOLVED_HUB_URL=""

# env_file_group — the group write_env_file chowns ENV_FILE to:
# CP_SERVICE_GROUP on linux-systemd and the private _cloudpulse group on
# darwin-launchd. Never use staff/GID 20: its broad membership would expose
# the agent token to ordinary macOS users.
env_file_group() {
  if [ "$PLATFORM" = "darwin-launchd" ]; then
    echo "$CP_DARWIN_GROUP"
    return
  fi
  echo "$CP_SERVICE_GROUP"
}

# ---------------------------------------------------------------------------
# systemd unit rendering
# ---------------------------------------------------------------------------

render_unit() {
  if [ "$PLATFORM" = "darwin-launchd" ]; then
    render_plist
    return
  fi
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

# render_plist — darwin-launchd equivalent of render_unit: ask the
# freshly installed binary to render its own plist via `plist print`
# (internal/launchd, SPEC-v0.7 §1), falling back to a built-in heredoc
# kept byte-identical to internal/launchd.Render's output (see the
# golden test in internal/launchd/launchd_test.go and the drift test in
# test-install.sh) only if that invocation fails (e.g. an explicit
# --version pin older than the tag that introduced the `plist`
# subcommand).
render_plist() {
  mkdir -p "$(dirname "$PLIST_PATH")"
  local tmp_plist="${TMP_DIR}/${CP_LAUNCHD_LABEL}.plist"

  if [ "${CP_INSTALL_FORCE_SCRIPT_UNIT:-0}" != "1" ] && render_plist_via_binary "$tmp_plist"; then
    log "rendered ${PLIST_PATH} via '${BIN_PATH} plist print'"
  else
    render_plist_fallback "$tmp_plist"
    log "rendered ${PLIST_PATH} via built-in fallback template"
  fi

  install -m 0644 "$tmp_plist" "$PLIST_PATH"
  log "wrote ${PLIST_PATH}"
}

# render_plist_via_binary OUT_PATH — mirrors render_unit_via_binary.
render_plist_via_binary() {
  local out_path="$1"
  if [ ! -x "$BIN_PATH" ]; then
    return 1
  fi
  local stderr_path="${TMP_DIR}/plist-print.stderr" summary
  if run_with_timeout 10 "$BIN_PATH" plist print --bin-path "$BIN_PATH" --env-file "$ENV_FILE" \
    --user "$CP_DARWIN_USER" --log-path "$LOG_PATH" >"$out_path" 2>"$stderr_path"; then
    return 0
  fi
  summary="$(tr '\n' ' ' < "$stderr_path" | cut -c1-300)"
  if [ -z "$summary" ]; then
    summary="command exited non-zero without stderr"
  fi
  log "plist renderer '${BIN_PATH} plist print' failed; using fallback: ${summary}"
  return 1
}

# render_plist_fallback OUT_PATH — bash heredoc fallback kept
# byte-identical to internal/launchd.Render's output.
render_plist_fallback() {
  local out_path="$1"
  local label bin_path env_file user log_path
  label="$(xml_escape "$CP_LAUNCHD_LABEL")"
  bin_path="$(xml_escape "$BIN_PATH")"
  env_file="$(xml_escape "$ENV_FILE")"
  user="$(xml_escape "$CP_DARWIN_USER")"
  log_path="$(xml_escape "$LOG_PATH")"
  {
    echo '<?xml version="1.0" encoding="UTF-8"?>'
    echo '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">'
    echo '<plist version="1.0">'
    echo '<dict>'
    echo '    <key>Label</key>'
    echo "    <string>${label}</string>"
    echo '    <key>ProgramArguments</key>'
    echo '    <array>'
    echo "        <string>${bin_path}</string>"
    echo '        <string>--env-file</string>'
    echo "        <string>${env_file}</string>"
    echo '    </array>'
    echo '    <key>UserName</key>'
    echo "    <string>${user}</string>"
    echo '    <key>KeepAlive</key>'
    echo '    <true/>'
    echo '    <key>RunAtLoad</key>'
    echo '    <true/>'
    echo '    <key>StandardOutPath</key>'
    echo "    <string>${log_path}</string>"
    echo '    <key>StandardErrorPath</key>'
    echo "    <string>${log_path}</string>"
    echo '</dict>'
    echo '</plist>'
  } > "$out_path"
}

# install_update_units — write cloud-pulse-agent-update.path/.service
# (SPEC-v0.6 §2) when --remote-update is in effect. Prefers asking the
# freshly installed binary to render both units via its own
# `systemd-unit apply` (which also handles the create-if-missing case;
# see cmd/agent/systemdunit.go's applyUpdateUnitsIfEnabled), falling
# back to a built-in heredoc pair only if that invocation fails (e.g. an
# explicit --version pin older than the tag that introduced
# `systemd-unit apply`'s remote-update awareness) — same
# binary-first-then-heredoc-fallback shape as render_unit/render_unit_via_binary
# above. When --remote-update is not in effect, this is a no-op: any
# previously installed update units are left exactly as they are
# (turning the feature back off is a separate, explicit action this
# script does not perform implicitly).
install_update_units() {
  if [ "$OPT_REMOTE_UPDATE" -ne 1 ]; then
    return
  fi
  if [ "$PLATFORM" = "darwin-launchd" ]; then
    install_update_plist
    return
  fi

  mkdir -p "$SYSTEMD_DIR" "$UPDATE_STATE_DIR" "$UPDATE_RESULT_DIR"

  if [ -x "$BIN_PATH" ] && run_with_timeout 15 "$BIN_PATH" systemd-unit apply \
      --unit-path "$UNIT_FILE" \
      --update-path-unit-path "$UPDATE_PATH_UNIT_FILE" \
      --update-service-unit-path "$UPDATE_SERVICE_UNIT_FILE" \
      --request-path "$UPDATE_REQUEST_FILE" \
      --result-dir "$UPDATE_RESULT_DIR" \
      --no-reload >/dev/null 2>&1; then
    log "rendered ${UPDATE_PATH_UNIT_FILE} / ${UPDATE_SERVICE_UNIT_FILE} via '${BIN_PATH} systemd-unit apply'"
  else
    install_update_units_fallback
    log "rendered ${UPDATE_PATH_UNIT_FILE} / ${UPDATE_SERVICE_UNIT_FILE} via built-in fallback template"
  fi
}

# install_update_plist — darwin-launchd equivalent of
# install_update_units: writes the com.cloudpulse.agent-update
# LaunchDaemon plist (WatchPaths=UPDATE_REQUEST_FILE, root, runs
# `cloud-pulse-agent update --from-request ... --result-dir ...` —
# SPEC-v0.7 §1) and ensures its state/result directories exist with
# the documented ownership (_cloudpulse 0750 request dir, root 0755
# result dir).
install_update_plist() {
  mkdir -p "$(dirname "$UPDATE_PLIST_PATH")" "$UPDATE_STATE_DIR" "$UPDATE_RESULT_DIR"
  darwin_chown "${CP_DARWIN_USER}:${CP_DARWIN_GROUP}" "$UPDATE_STATE_DIR"
  darwin_chown root "$UPDATE_RESULT_DIR"
  chmod 0750 "$UPDATE_STATE_DIR"
  chmod 0755 "$UPDATE_RESULT_DIR"

  local tmp_plist="${TMP_DIR}/${CP_LAUNCHD_UPDATE_LABEL}.plist"
  local label bin_path env_file request_file result_dir
  label="$(xml_escape "$CP_LAUNCHD_UPDATE_LABEL")"
  bin_path="$(xml_escape "$BIN_PATH")"
  env_file="$(xml_escape "$ENV_FILE")"
  request_file="$(xml_escape "$UPDATE_REQUEST_FILE")"
  result_dir="$(xml_escape "$UPDATE_RESULT_DIR")"
  {
    echo '<?xml version="1.0" encoding="UTF-8"?>'
    echo '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">'
    echo '<plist version="1.0">'
    echo '<dict>'
    echo '    <key>Label</key>'
    echo "    <string>${label}</string>"
    echo '    <key>ProgramArguments</key>'
    echo '    <array>'
    echo "        <string>${bin_path}</string>"
    echo '        <string>update</string>'
    echo '        <string>--env-file</string>'
    echo "        <string>${env_file}</string>"
    echo '        <string>--from-request</string>'
    echo "        <string>${request_file}</string>"
    echo '        <string>--result-dir</string>'
    echo "        <string>${result_dir}</string>"
    echo '    </array>'
    echo '    <key>WatchPaths</key>'
    echo '    <array>'
    echo "        <string>${request_file}</string>"
    echo '    </array>'
    echo '</dict>'
    echo '</plist>'
  } > "$tmp_plist"
  install -m 0644 "$tmp_plist" "$UPDATE_PLIST_PATH"
  log "wrote ${UPDATE_PLIST_PATH}"
}

# install_update_units_fallback — bash heredoc fallback kept
# byte-identical to internal/systemdunit.RenderUpdatePath/
# RenderUpdateService's output (see the golden test in
# internal/systemdunit/updateunit_test.go and the drift test in
# test-install.sh).
install_update_units_fallback() {
  local tmp_path_unit="${TMP_DIR}/cloud-pulse-agent-update.path"
  local tmp_service_unit="${TMP_DIR}/cloud-pulse-agent-update.service"
  {
    echo "[Unit]"
    echo "Description=Watch for cloud-pulse-agent remote update requests"
    echo
    echo "[Path]"
    echo "PathExists=${UPDATE_REQUEST_FILE}"
    echo "Unit=cloud-pulse-agent-update.service"
    echo
    echo "[Install]"
    echo "WantedBy=multi-user.target"
  } > "$tmp_path_unit"
  {
    echo "[Unit]"
    echo "Description=Apply a hub-requested cloud-pulse-agent update"
    echo
    echo "[Service]"
    echo "Type=oneshot"
    echo "EnvironmentFile=${ENV_FILE}"
    echo "ExecStart=${BIN_PATH} update --from-request ${UPDATE_REQUEST_FILE} --result-dir ${UPDATE_RESULT_DIR}"
  } > "$tmp_service_unit"
  install -m 0644 "$tmp_path_unit" "$UPDATE_PATH_UNIT_FILE"
  install -m 0644 "$tmp_service_unit" "$UPDATE_SERVICE_UNIT_FILE"
}

# enable_update_path_unit — enable (but never start; it only ever
# triggers on demand) cloud-pulse-agent-update.path so it survives a
# reboot, only when --remote-update is in effect. This is the "옵트인
# 시에만 enable" half of SPEC-v0.6 §2 systemctl invocations start_service
# doesn't already cover (start_service only knows about the main
# cloud-pulse-agent.service).
enable_update_path_unit() {
  if [ "$OPT_REMOTE_UPDATE" -ne 1 ]; then
    return
  fi
  if [ "$PLATFORM" = "darwin-launchd" ]; then
    enable_update_plist_darwin
    return
  fi
  if [ "$IS_SANDBOX" -eq 1 ] && [ -z "${CP_SYSTEMCTL:-}" ]; then
    log "sandbox: would run ${SYSTEMCTL} enable cloud-pulse-agent-update.path"
    return
  fi
  "$SYSTEMCTL" enable cloud-pulse-agent-update.path
  log "enabled cloud-pulse-agent-update.path (SPEC-v0.6 §2 remote update)"
}

# enable_update_plist_darwin — bootstrap the update-helper LaunchDaemon
# into the system domain (idempotent: a "already bootstrapped" failure
# from a re-run is not treated as fatal).
enable_update_plist_darwin() {
  if [ "$IS_SANDBOX" -eq 1 ] && [ -z "${CP_LAUNCHCTL:-}" ]; then
    log "sandbox: would run ${LAUNCHCTL} bootstrap system ${UPDATE_PLIST_PATH}"
    return
  fi
  "$LAUNCHCTL" bootstrap system "$UPDATE_PLIST_PATH" 2>/dev/null || true
  log "bootstrapped ${CP_LAUNCHD_UPDATE_LABEL} (SPEC-v0.7 §1 remote update)"
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
  if [ "$PLATFORM" = "darwin-launchd" ]; then
    return
  fi
  if [ "$OPT_DOCKER" -eq 1 ]; then
    return
  fi
  if [ -f "$UNIT_FILE" ] && grep -q '^SupplementaryGroups=.*docker' "$UNIT_FILE" 2>/dev/null; then
    OPT_DOCKER=1
    log "preserving existing --docker setting from ${UNIT_FILE} (SupplementaryGroups=docker)"
  fi
}

# detect_preserved_remote_update_flag — same idea as
# detect_preserved_docker_flag, for --remote-update (SPEC-v0.6 §2): a
# re-run that doesn't re-pass --remote-update but whose existing unit
# already has StateDirectory=cloud-pulse-agent preserves the opt-in.
detect_preserved_remote_update_flag() {
  if [ "$OPT_REMOTE_UPDATE" -eq 1 ]; then
    return
  fi
  if [ "$PLATFORM" = "darwin-launchd" ]; then
    if [ -f "$UPDATE_PLIST_PATH" ]; then
      OPT_REMOTE_UPDATE=1
      log "preserving existing --remote-update setting (${UPDATE_PLIST_PATH} present)"
    fi
    return
  fi
  if [ -f "$UNIT_FILE" ] && grep -q '^StateDirectory=cloud-pulse-agent$' "$UNIT_FILE" 2>/dev/null; then
    OPT_REMOTE_UPDATE=1
    log "preserving existing --remote-update setting from ${UNIT_FILE} (StateDirectory=cloud-pulse-agent)"
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
  local extra_args=()
  if [ "$OPT_DOCKER" -eq 1 ]; then
    extra_args+=(--supplementary-groups docker)
  fi
  if [ "$OPT_REMOTE_UPDATE" -eq 1 ]; then
    extra_args+=(--remote-update)
  fi
  run_with_timeout 10 "$BIN_PATH" systemd-unit print --bin-path "$BIN_PATH" --env-file "$ENV_FILE" \
    --user "$CP_SERVICE_USER" --group "$CP_SERVICE_GROUP" "${extra_args[@]}" >"$out_path" 2>/dev/null
}

# render_unit_fallback OUT_PATH — bash heredoc fallback kept
# byte-identical to internal/systemdunit.Render's agent template (see
# SPEC-v0.3.1 B and the drift test in test-install.sh). A
# SupplementaryGroups=docker line is added right after Group= when
# --docker was given (SPEC-v0.5 §C); a StateDirectory=cloud-pulse-agent
# line is added right after that (or after Group= if --docker wasn't
# given) when --remote-update was given (SPEC-v0.6 §2), matching
# Render's own placement.
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
    if [ "$OPT_REMOTE_UPDATE" -eq 1 ]; then
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
  if [ "$PLATFORM" = "darwin-launchd" ]; then
    verify_plist
    return
  fi
  if ! command -v systemd-analyze >/dev/null 2>&1; then
    log "systemd-analyze not available; skipping unit verification"
    return
  fi

  local verify_target="$UNIT_FILE"
  if [ "$IS_SANDBOX" -eq 1 ]; then
    verify_target="${TMP_DIR}/verify-cloud-pulse-agent.service"
    sed "s#^ExecStart=.*#ExecStart=${BIN_PATH}#" "$UNIT_FILE" > "$verify_target"
  fi

  if run_with_timeout 30 systemd-analyze verify "$verify_target" 2>&1 | tee "${TMP_DIR}/systemd-analyze.log"; then
    log "systemd-analyze verify: OK"
  else
    if grep -qiE 'unknown (lvalue|section)|failed to parse|invalid syntax' "${TMP_DIR}/systemd-analyze.log"; then
      err "systemd-analyze verify reported a fatal unit syntax error"
      exit 1
    fi
    log "systemd-analyze verify reported warnings (see above); continuing"
  fi
}

# verify_plist — darwin-launchd equivalent of verify_unit: `plutil -lint`
# validates the plist's XML/plist syntax (a standard macOS command-line
# tool, always present); a fatal syntax error aborts the install the
# same way a fatal systemd-analyze error does, non-fatal warnings just
# continue.
verify_plist() {
  if ! command -v plutil >/dev/null 2>&1; then
    log "plutil not available; skipping plist verification"
    return
  fi
  if run_with_timeout 10 plutil -lint "$PLIST_PATH" >"${TMP_DIR}/plutil.log" 2>&1; then
    log "plutil -lint: OK"
  else
    err "plutil -lint reported a fatal plist syntax error:"
    sed 's/^/  /' "${TMP_DIR}/plutil.log" || true
    exit 1
  fi
}

# ---------------------------------------------------------------------------
# service start
# ---------------------------------------------------------------------------

start_service() {
  if [ "$PLATFORM" = "darwin-launchd" ]; then
    start_service_darwin
    return
  fi
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

# start_service_darwin — darwin-launchd equivalent of start_service:
# bootout (if currently loaded) then bootstrap, so a re-run always
# picks up a changed plist, then kickstart -k to force an immediate
# (re)start, then poll `launchctl print` for a Running PID.
start_service_darwin() {
  local attempts=30 pid=""

  if [ "$IS_SANDBOX" -eq 1 ] && [ -z "${CP_LAUNCHCTL:-}" ]; then
    log "sandbox: would run ${LAUNCHCTL} bootout system/${CP_LAUNCHD_LABEL} (if loaded)"
    log "sandbox: would run ${LAUNCHCTL} bootstrap system ${PLIST_PATH}"
    log "sandbox: would run ${LAUNCHCTL} kickstart -k system/${CP_LAUNCHD_LABEL}"
    return
  fi

  if "$LAUNCHCTL" print "system/${CP_LAUNCHD_LABEL}" >/dev/null 2>&1; then
    SERVICE_ACTION="restarted"
    "$LAUNCHCTL" bootout "system/${CP_LAUNCHD_LABEL}" 2>/dev/null || true
  else
    SERVICE_ACTION="started"
  fi
  "$LAUNCHCTL" bootstrap system "$PLIST_PATH"
  "$LAUNCHCTL" kickstart -k "system/${CP_LAUNCHD_LABEL}" 2>/dev/null || true

  while [ "$attempts" -gt 0 ]; do
    attempts=$((attempts - 1))
    pid="$("$LAUNCHCTL" print "system/${CP_LAUNCHD_LABEL}" 2>/dev/null | awk '/pid = /{print $3; exit}' || true)"
    if [ -n "$pid" ]; then
      SERVICE_VERSION="$(probe_existing_version "$BIN_PATH")"
      return
    fi
    sleep 0.5
  done

  err "${CP_LAUNCHD_LABEL} failed to ${SERVICE_ACTION}; check: log show --predicate 'process == \"cloud-pulse-agent\"' --last 5m"
  exit 1
}

# ---------------------------------------------------------------------------
# post-install sanity checks (non-fatal)
# ---------------------------------------------------------------------------

run_sanity_checks() {
  if [ -x "$BIN_PATH" ]; then
    log "running sanity check: ${BIN_PATH} -once"
    if run_with_timeout 20 "$BIN_PATH" -once >/dev/null 2>"${TMP_DIR}/once.log"; then
      log "sanity check ok"
    else
      log "warning: '${BIN_PATH} -once' failed (non-fatal):"
      sed 's/^/  /' "${TMP_DIR}/once.log" || true
    fi
  fi

  if command -v curl >/dev/null 2>&1 && [ -n "$RESOLVED_HUB_URL" ]; then
    log "checking hub reachability: ${RESOLVED_HUB_URL}/healthz"
    if run_with_timeout 10 curl -fsS "${RESOLVED_HUB_URL%/}/healthz" >/dev/null 2>&1; then
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
    echo "  Service:      $(service_display_name) ${SERVICE_ACTION} (running ${SERVICE_VERSION})"
  else
    echo "  Service:      $(service_display_name)"
  fi
  echo
  print_version_hint
}

# service_display_name — the systemd unit name on linux-systemd, or the
# launchd label on darwin-launchd, for display in print_summary/error
# messages.
service_display_name() {
  if [ "$PLATFORM" = "darwin-launchd" ]; then
    echo "$CP_LAUNCHD_LABEL"
    return
  fi
  echo "cloud-pulse-agent.service"
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
  if [ "$PLATFORM" = "darwin-launchd" ]; then
    echo "  - ensure hidden system user/group '${CP_DARWIN_USER}/${CP_DARWIN_GROUP}' exists (dscl, UID/GID 200-400)"
    echo "  - write ${ENV_FILE} (mode 0640, owner root:${CP_DARWIN_GROUP})"
    echo "  - write ${PLIST_PATH}"
    if [ "$OPT_REMOTE_UPDATE" -eq 1 ]; then
      echo "  - write ${UPDATE_PLIST_PATH} (--remote-update, SPEC-v0.7 §1)"
    fi
    echo "  - plutil -lint the plist"
    echo "  - ${LAUNCHCTL} bootout system/${CP_LAUNCHD_LABEL} (if loaded) && bootstrap && kickstart -k"
    if [ "$OPT_REMOTE_UPDATE" -eq 1 ]; then
      echo "  - ${LAUNCHCTL} bootstrap system ${UPDATE_PLIST_PATH} (--remote-update)"
    fi
  else
    echo "  - ensure system user/group '${CP_SERVICE_USER}' exists"
    if [ "$OPT_DOCKER" -eq 1 ]; then
      echo "  - add '${CP_SERVICE_USER}' to the 'docker' group (--docker: root-equivalent access)"
    fi
    echo "  - write ${ENV_FILE} (mode 0640, owner root:${CP_SERVICE_GROUP})"
    echo "  - write ${UNIT_FILE}"
    if [ "$OPT_REMOTE_UPDATE" -eq 1 ]; then
      echo "  - write ${UPDATE_PATH_UNIT_FILE} / ${UPDATE_SERVICE_UNIT_FILE} (--remote-update, SPEC-v0.6 §2)"
    fi
    echo "  - systemd-analyze verify the unit"
    echo "  - ${SYSTEMCTL} daemon-reload && ${SYSTEMCTL} enable cloud-pulse-agent.service"
    echo "  - start cloud-pulse-agent.service if inactive; restart it if already running"
    if [ "$OPT_REMOTE_UPDATE" -eq 1 ]; then
      echo "  - ${SYSTEMCTL} enable cloud-pulse-agent-update.path (--remote-update)"
    fi
  fi
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
  if [ "$PLATFORM" = "darwin-launchd" ]; then
    if command -v "$LAUNCHCTL" >/dev/null 2>&1 && "$LAUNCHCTL" print "system/${CP_LAUNCHD_LABEL}" >/dev/null 2>&1; then
      echo " (service: loaded)"
    else
      echo " (service: not loaded)"
    fi
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

  if [ "$PLATFORM" != "darwin-launchd" ]; then
    prompt_tty "Monitor Docker containers? (adds the agent to the docker group; docker group access is root-equivalent) [y/N]: "
    if ! read_line_tty answer; then
      err "unexpected EOF on /dev/tty"
      exit 1
    fi
    case "$answer" in
      [Yy]|[Yy][Ee][Ss]) OPT_DOCKER=1 ;;
      *) ;;
    esac
  fi

  prompt_tty "Allow the hub to trigger updates of this agent? [y/N]: "
  if ! read_line_tty answer; then
    err "unexpected EOF on /dev/tty"
    exit 1
  fi
  case "$answer" in
    [Yy]|[Yy][Ee][Ss]) OPT_REMOTE_UPDATE=1 ;;
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
  if [ "$PLATFORM" = "darwin-launchd" ]; then
    do_uninstall_darwin
    return
  fi
  if [ "$IS_SANDBOX" -eq 1 ] && [ -z "${CP_SYSTEMCTL:-}" ]; then
    log "sandbox: would run ${SYSTEMCTL} stop cloud-pulse-agent.service"
    log "sandbox: would run ${SYSTEMCTL} disable cloud-pulse-agent.service"
    log "sandbox: would run ${SYSTEMCTL} stop cloud-pulse-agent-update.path"
    log "sandbox: would run ${SYSTEMCTL} disable cloud-pulse-agent-update.path"
  elif command -v "$SYSTEMCTL" >/dev/null 2>&1; then
    "$SYSTEMCTL" stop cloud-pulse-agent.service 2>/dev/null || true
    "$SYSTEMCTL" disable cloud-pulse-agent.service 2>/dev/null || true
    "$SYSTEMCTL" stop cloud-pulse-agent-update.path 2>/dev/null || true
    "$SYSTEMCTL" disable cloud-pulse-agent-update.path 2>/dev/null || true
  fi

  rm -f "$UNIT_FILE" "$UPDATE_PATH_UNIT_FILE" "$UPDATE_SERVICE_UNIT_FILE"
  rm -f "$BIN_PATH"
  log "removed unit(s) and binary"

  if { [ "$IS_SANDBOX" -ne 1 ] || [ -n "${CP_SYSTEMCTL:-}" ]; } && command -v "$SYSTEMCTL" >/dev/null 2>&1; then
    "$SYSTEMCTL" daemon-reload || true
  fi

  if [ "$OPT_PURGE" -eq 1 ]; then
    rm -f "$ENV_FILE"
    rm -rf "$UPDATE_STATE_DIR" "$UPDATE_RESULT_DIR"
    log "purged config and remote-update state (--purge)"
  else
    log "kept ${ENV_FILE} (pass --purge to remove it)"
  fi
}

# do_uninstall_darwin — darwin-launchd equivalent of do_uninstall:
# bootout both LaunchDaemons (best-effort, "not loaded" is not fatal),
# remove the plists and binary, and (with --purge) the env file, the
# request/result directories, and the dedicated _cloudpulse user
# itself (dscl delete; a missing user is not fatal either — matches
# do_uninstall's "best-effort, never fails on an already-absent
# resource" philosophy).
do_uninstall_darwin() {
  if [ "$IS_SANDBOX" -eq 1 ] && [ -z "${CP_LAUNCHCTL:-}" ]; then
    log "sandbox: would run ${LAUNCHCTL} bootout system/${CP_LAUNCHD_LABEL}"
    log "sandbox: would run ${LAUNCHCTL} bootout system/${CP_LAUNCHD_UPDATE_LABEL}"
  elif command -v "$LAUNCHCTL" >/dev/null 2>&1; then
    "$LAUNCHCTL" bootout "system/${CP_LAUNCHD_LABEL}" 2>/dev/null || true
    "$LAUNCHCTL" bootout "system/${CP_LAUNCHD_UPDATE_LABEL}" 2>/dev/null || true
  fi

  rm -f "$PLIST_PATH" "$UPDATE_PLIST_PATH"
  rm -f "$BIN_PATH"
  log "removed plist(s) and binary"

  if [ "$OPT_PURGE" -eq 1 ]; then
    rm -f "$ENV_FILE"
    rm -rf "$UPDATE_STATE_DIR" "$UPDATE_RESULT_DIR"
    if [ "$IS_SANDBOX" -eq 1 ] && [ -z "${CP_DSCL:-}" ]; then
      log "sandbox: would run dscl . -delete /Users/${CP_DARWIN_USER} and /Groups/${CP_DARWIN_GROUP}"
    else
      "$DSCL" . -delete "/Users/${CP_DARWIN_USER}" 2>/dev/null || true
      "$DSCL" . -delete "/Groups/${CP_DARWIN_GROUP}" 2>/dev/null || true
    fi
    log "purged config, remote-update state, system user, and private group (--purge)"
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
  detect_preserved_remote_update_flag
  download_and_verify
  ensure_system_user
  install_binary
  write_env_file
  render_unit
  install_update_units
  verify_unit
  start_service
  enable_update_path_unit
  run_sanity_checks
  print_summary
}

main() {
  parse_args "$@"
  detect_sandbox
  detect_platform
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
