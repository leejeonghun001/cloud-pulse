#!/usr/bin/env bash
#
# install-hub.sh — one-click installer for cloud-pulse-hub.
#
# Downloads a pre-compiled cloud-pulse-hub release binary from GitHub
# Releases, verifies its sha256 checksum, installs it as a systemd
# service running as an unprivileged system user, and writes its
# configuration to /etc/cloud-pulse/hub.env.
#
# Usage (typical, piped from curl):
#   curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-hub.sh \
#     | sudo bash -s -- [flags]
#
# Flags:
#   --install                    Fresh install. Errors (with a hint) if
#                               already installed.
#   --reinstall                   Upgrade/reinstall an existing install in
#                               place, keeping all env values and data.
#                               Errors (with a hint) if not installed.
#   --version vX.Y.Z          Install a specific release (default: latest).
#   --listen ADDR              Listen address (default: :8090).
#   --allowed-cidrs LIST        Comma-separated CIDR allowlist (default:
#                               tailscale + loopback, matches hub default).
#   --ui-token TOKEN            Set CP_UI_TOKEN explicitly. This is an
#                               OPTIONAL static API bearer token for
#                               scripts (e.g. curl against the REST API);
#                               the dashboard itself always requires
#                               signing in and never uses this token.
#   --generate-ui-token         Generate a random CP_UI_TOKEN if none is
#                               already set (idempotent; keeps an existing
#                               token unchanged). Optional; only needed for
#                               scripted API access.
#   --rotate-ui-token           Always generate a new CP_UI_TOKEN, even if
#                               one already exists.
#   --agent-token TOKEN         Set CP_AGENT_TOKEN explicitly.
#   --webhook-url URL           Set CP_ALERT_WEBHOOK_URL.
#   --prefix DIR                Binary install prefix (default: /usr/local).
#   --uninstall                 Stop/disable the service, remove the unit
#                               and binary. Config/data are kept unless
#                               --purge is also given.
#   --purge                     With --uninstall, also remove
#                               /etc/cloud-pulse/hub.env and the data dir.
#   -y, --yes                    Assume default answers to any
#                               confirmation prompt (Y for install/
#                               reinstall, N for purge unless --purge is
#                               also given); never blocks on a prompt.
#   --dry-run                   Print the actions that would be taken and
#                               exit without changing anything.
#   -h, --help                  Show this help and exit.
#
# Interactive menu:
#   Running with NO arguments at all, from a real terminal (stdin/stdout
#   attached to a tty, and CP_NONINTERACTIVE is not "1"), shows a menu
#   (Install / Reinstall / Uninstall / Exit) instead of the flag-driven
#   behavior below. All menu prompts read from /dev/tty, never stdin, so
#   `curl ... | sudo bash` (whose stdin is the script itself) still shows
#   the menu correctly when run at a terminal. Piping any flag, or
#   running without a tty (e.g. from CI or `curl | bash` with a flag),
#   always skips the menu.
#
# Re-running this script (without --uninstall, and without a tty/menu)
# upgrades an existing install in place: the binary and systemd unit are
# replaced, but hub.env is preserved unless an explicit flag supplies a
# new value for one of its fields. This is the same behavior whether or
# not --reinstall is passed explicitly.
#
# Environment overrides:
#   CP_RELEASE_BASE_URL   Override the base URL assets are downloaded
#                         from (default: the GitHub release URLs described
#                         above). Assets are expected at
#                         "$CP_RELEASE_BASE_URL/<asset-name>". Useful for
#                         testing against a local HTTP server or a mirror.
#   CP_INSTALL_ROOT       Sandbox mode. When set, every system path this
#                         script touches (binary prefix, /etc/cloud-pulse,
#                         /etc/systemd/system, /var/lib/cloud-pulse) is
#                         prefixed with this directory instead of the real
#                         root filesystem. The root check, `useradd`,
#                         `chown`, and `systemctl` calls are skipped and
#                         replaced with "sandbox: would run ..." messages,
#                         unless CP_SYSTEMCTL explicitly names a systemctl
#                         shim (a test hook), in which case that shim runs.
#                         Downloading, checksum verification, file
#                         installation, unit rendering, and
#                         `systemd-analyze verify` (if available) still
#                         run for real against the sandboxed paths. This
#                         lets scripts/test-install.sh exercise the full
#                         script logic without root or a real systemd.
#   CP_TEST_UNAME_M       Override the value used in place of `uname -m`
#                         (test hook for exercising the unknown-arch path).
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
CP_RAW_ASSET_NAME_HUB_PREFIX="cloud-pulse-hub"
CP_SERVICE_USER="cloud-pulse"
CP_SERVICE_GROUP="cloud-pulse"

# ---------------------------------------------------------------------------
# Globals populated by argument parsing / detection; declared up front so
# main() reads as a straight-line summary of the install flow.
# ---------------------------------------------------------------------------

DEFAULT_LISTEN=":8090"
DEFAULT_ALLOWED_CIDRS="100.64.0.0/10,fd7a:115c:a1e0::/48,127.0.0.0/8,::1/128"

OPT_VERSION=""
# Empty means the option was omitted. Resolve it only after paths have
# been set up, so reinstall can prefer a value already in hub.env.
OPT_LISTEN=""
OPT_ALLOWED_CIDRS=""
OPT_UI_TOKEN=""
OPT_GENERATE_UI_TOKEN=0
OPT_ROTATE_UI_TOKEN=0
OPT_AGENT_TOKEN=""
OPT_WEBHOOK_URL=""
OPT_PREFIX="/usr/local"
OPT_ACTION=""
OPT_UNINSTALL=0
OPT_PURGE=0
OPT_YES=0
OPT_DRY_RUN=0
OPT_ANY_FLAG_GIVEN=0

# Set when a UI token was newly generated/rotated during this run, so
# print_summary can print it exactly once (D in SPEC-v0.3.1 A).
UI_TOKEN_WAS_GENERATED=0

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
DATA_DIR=""
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
  echo "[install-hub] $*"
}

err() {
  echo "[install-hub] error: $*" >&2
}

usage() {
  sed -n '2,55p' "$0" | sed 's/^# \{0,1\}//'
}

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    err "required command '$1' not found in PATH"
    exit 1
  fi
}

# fetch URL OUT_PATH — download URL to OUT_PATH using curl, falling back to
# wget if curl is unavailable.
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
# escape a KEY=VALUE line when later written into hub.env and parsed by
# systemd's EnvironmentFile= (and by our own load_env_file_safe). Without
# this check, a value containing a newline injects a second, arbitrary
# "KEY=VALUE" line into the file (e.g. --host-id "$(printf 'x\nCP_HUB_URL=
# http://evil/\n#')" would append a rogue CP_HUB_URL line that wins under
# systemd's last-line-wins parsing); a value containing unquoted
# whitespace silently truncates at the first space when later consumed by
# anything that word-splits it, corrupting the config with no clear error.
# Reject each case explicitly, naming the offending flag, instead of
# letting either failure mode surface as a generic downstream error. A
# literal backslash is also rejected: systemd's real EnvironmentFile=
# parser applies POSIX-style unquoted backslash-escape rules to unquoted
# values, silently dropping/altering the backslash on load (e.g. a value
# ending up different at service runtime than what was written), and our
# own load_env_file_safe() (used by -check-config) does not replicate
# that escaping — so a backslash would let -check-config validate a
# string that differs from the one systemd actually hands to the service.
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

# random_hex_token N — print N random bytes as hex using the hub binary's
# -gen-token when available, else /dev/urandom via od.
random_hex_token_fallback() {
  local bytes="$1"
  if [ -r /dev/urandom ]; then
    od -An -tx1 -N "$bytes" /dev/urandom | tr -d ' \n'
    echo
  else
    err "no source of randomness available (/dev/urandom missing)"
    exit 1
  fi
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
# write in this process. Used to decide whether the interactive menu
# (and any prompt) may be shown at all: a script piped via
# `curl | sudo bash` has its stdin consumed by the script body itself,
# so every prompt in this file reads from /dev/tty explicitly rather
# than stdin, and this check guards against the case where no
# controlling terminal exists at all (cron, CI, `bash script.sh` with
# stdin/stdout redirected to files) where opening /dev/tty would itself
# fail or hang.
tty_available() {
  if [ "${CP_NONINTERACTIVE:-0}" = "1" ]; then
    return 1
  fi
  { : <"/dev/tty"; } 2>/dev/null && { : >"/dev/tty"; } 2>/dev/null
}

# prompt_tty PROMPT_TEXT — print PROMPT_TEXT to /dev/tty with no
# trailing newline (caller's read then reads the answer from /dev/tty).
prompt_tty() {
  printf '%s' "$1" >/dev/tty
}

# read_line_tty VARNAME — read one line from /dev/tty into VARNAME.
# Returns the read builtin's exit status (1 on EOF) so callers can
# distinguish "user typed nothing" from "stdin/tty closed".
read_line_tty() {
  local __varname="$1"
  # shellcheck disable=SC2229 # intentional: read -r into a
  # caller-provided variable name passed as $1, not a literal "r".
  IFS= read -r "$__varname" </dev/tty
}

# read_hidden_tty VARNAME PROMPT_TEXT — prompt on /dev/tty and read one
# line from /dev/tty with echo disabled (for secrets like the agent
# token), printing a newline afterward since -s suppresses the one the
# terminal would otherwise echo.
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

# confirm_tty PROMPT_TEXT DEFAULT_YES — print "PROMPT_TEXT [Y/n]: " (or
# "[y/N]: " when DEFAULT_YES=0) to /dev/tty, read one line from /dev/tty,
# and return 0 for yes / 1 for no. Empty input takes DEFAULT_YES. If
# OPT_YES is set, returns DEFAULT_YES immediately without prompting (-y
# never blocks on a confirmation).
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
      --version)
        OPT_VERSION="${2:?--version requires an argument}"
        shift 2
        ;;
      --listen)
        OPT_LISTEN="${2:?--listen requires an argument}"
        validate_env_value "$OPT_LISTEN" --listen
        shift 2
        ;;
      --allowed-cidrs)
        OPT_ALLOWED_CIDRS="${2:?--allowed-cidrs requires an argument}"
        validate_env_value "$OPT_ALLOWED_CIDRS" --allowed-cidrs
        shift 2
        ;;
      --ui-token)
        OPT_UI_TOKEN="${2:?--ui-token requires an argument}"
        validate_env_value "$OPT_UI_TOKEN" --ui-token
        shift 2
        ;;
      --generate-ui-token)
        OPT_GENERATE_UI_TOKEN=1
        shift
        ;;
      --rotate-ui-token)
        OPT_ROTATE_UI_TOKEN=1
        shift
        ;;
      --agent-token)
        OPT_AGENT_TOKEN="${2:?--agent-token requires an argument}"
        validate_env_value "$OPT_AGENT_TOKEN" --agent-token
        shift 2
        ;;
      --webhook-url)
        OPT_WEBHOOK_URL="${2:?--webhook-url requires an argument}"
        validate_env_value "$OPT_WEBHOOK_URL" --webhook-url
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
    err "cloud-pulse-hub's installer only supports Linux (systemd). Detected: ${uname_s}"
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
  DATA_DIR="${root}/var/lib/cloud-pulse"
  ENV_FILE="${ETC_DIR}/hub.env"
  UNIT_FILE="${SYSTEMD_DIR}/cloud-pulse-hub.service"
  BIN_PATH="${BIN_DIR}/cloud-pulse-hub"
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
  ASSET_NAME="${CP_RAW_ASSET_NAME_HUB_PREFIX}-${OS_NAME}-${ARCH}"
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
# hub.env rendering (preserve existing values on upgrade unless overridden)
# ---------------------------------------------------------------------------

env_get_existing() {
  local key="$1"
  if [ -f "$ENV_FILE" ]; then
    grep -E "^${key}=" "$ENV_FILE" 2>/dev/null | tail -n1 | cut -d= -f2- || true
  fi
}

resolve_agent_token() {
  local existing
  existing="$(env_get_existing CP_AGENT_TOKEN)"

  if [ -n "$OPT_AGENT_TOKEN" ]; then
    echo "$OPT_AGENT_TOKEN"
    return
  fi
  if [ -n "$existing" ]; then
    echo "$existing"
    return
  fi

  # Generate a fresh token. Prefer the just-installed binary's -gen-token
  # so the format matches exactly what the hub expects; fall back to
  # /dev/urandom if the binary can't run (e.g. cross-arch sandbox test).
  local generated=""
  if [ -x "$BIN_PATH" ]; then
    generated="$("$BIN_PATH" -gen-token 2>/dev/null || true)"
  fi
  if [ -z "$generated" ]; then
    generated="$(random_hex_token_fallback 32)"
  fi
  echo "$generated"
}

resolve_ui_token() {
  local existing
  existing="$(env_get_existing CP_UI_TOKEN)"

  if [ -n "$OPT_UI_TOKEN" ]; then
    echo "$OPT_UI_TOKEN"
    return
  fi
  if [ "$OPT_ROTATE_UI_TOKEN" -eq 1 ]; then
    # --rotate-ui-token always generates a fresh token, even if one
    # already exists.
    generate_token_value
    return
  fi
  if [ "$OPT_GENERATE_UI_TOKEN" -eq 1 ]; then
    # --generate-ui-token is idempotent: keep an existing token
    # unchanged, only generating a new one when none is set yet.
    if [ -n "$existing" ]; then
      echo "$existing"
      return
    fi
    generate_token_value
    return
  fi
  echo "$existing"
}

# ui_token_will_be_generated — true iff the next resolve_ui_token call
# will mint a brand-new token rather than reusing/keeping an existing
# one or an explicit --ui-token value. Must be checked BEFORE calling
# resolve_ui_token via command substitution: bash runs command
# substitutions in a subshell, so a flag set as a side effect inside
# resolve_ui_token itself would never be visible to the parent shell.
ui_token_will_be_generated() {
  if [ -n "$OPT_UI_TOKEN" ]; then
    return 1
  fi
  if [ "$OPT_ROTATE_UI_TOKEN" -eq 1 ]; then
    return 0
  fi
  if [ "$OPT_GENERATE_UI_TOKEN" -eq 1 ] && [ -z "$(env_get_existing CP_UI_TOKEN)" ]; then
    return 0
  fi
  return 1
}

# generate_token_value — print a freshly generated random hex token,
# preferring the just-installed binary's -gen-token (matches the exact
# format the hub expects) and falling back to /dev/urandom.
generate_token_value() {
  if [ -x "$BIN_PATH" ]; then
    "$BIN_PATH" -gen-token 2>/dev/null || random_hex_token_fallback 32
  else
    random_hex_token_fallback 32
  fi
}

resolve_webhook_url() {
  local existing
  existing="$(env_get_existing CP_ALERT_WEBHOOK_URL)"
  if [ -n "$OPT_WEBHOOK_URL" ]; then
    echo "$OPT_WEBHOOK_URL"
    return
  fi
  echo "$existing"
}

resolve_value() {
  # resolve_value FLAG_VALUE ENV_KEY DEFAULT FLAG_NAME — omitted flags use
  # the existing active value first, then the fresh-install default.
  local flag_value="$1" env_key="$2" default_value="$3" flag_name="$4" value
  value="$flag_value"
  if [ -z "$value" ]; then
    value="$(env_get_existing "$env_key")"
  fi
  if [ -z "$value" ]; then
    value="$default_value"
  fi
  validate_env_value "$value" "$flag_name"
  printf '%s\n' "$value"
}

rewrite_hub_env() {
  # Rewrite managed keys in their original positions, preserving comments and
  # unrecognised settings verbatim. A commented managed key counts as present:
  # it is only activated when no active form exists and its resolved value is
  # non-empty. Do not source env files; manually edited configuration is data.
  local tmp_env="$1" listen="$2" agent_token="$3" allowed_cidrs="$4"
  local ui_token="$5" webhook_url="$6"
  local seen_listen=0 seen_data=0 seen_agent=0 seen_allowed=0 seen_ui=0 seen_webhook=0
  local active_listen=0 active_data=0 active_agent=0 active_allowed=0 active_ui=0 active_webhook=0
  local line normalized is_commented

  if [ -f "$ENV_FILE" ]; then
    # Identify active keys first. This makes an active assignment win over a
    # separately commented default regardless of their order in the file.
    while IFS= read -r line || [ -n "$line" ]; do
      normalized="${line#"${line%%[![:space:]]*}"}"
      case "$normalized" in
        \#*) ;;
        CP_LISTEN=*) active_listen=1 ;;
        CP_DATA_DIR=*) active_data=1 ;;
        CP_AGENT_TOKEN=*) active_agent=1 ;;
        CP_ALLOWED_CIDRS=*) active_allowed=1 ;;
        CP_UI_TOKEN=*) active_ui=1 ;;
        CP_ALERT_WEBHOOK_URL=*) active_webhook=1 ;;
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
        CP_LISTEN=*)
          seen_listen=1
          if [ "$is_commented" -eq 1 ] && [ "$active_listen" -eq 1 ]; then printf '%s\n' "$line"; else printf 'CP_LISTEN=%s\n' "$listen"; fi
          ;;
        CP_DATA_DIR=*)
          seen_data=1
          if [ "$is_commented" -eq 1 ] && [ "$active_data" -eq 1 ]; then printf '%s\n' "$line"; else echo "CP_DATA_DIR=/var/lib/cloud-pulse"; fi
          ;;
        CP_AGENT_TOKEN=*)
          seen_agent=1
          if [ "$is_commented" -eq 1 ] && [ "$active_agent" -eq 1 ]; then printf '%s\n' "$line"; else printf 'CP_AGENT_TOKEN=%s\n' "$agent_token"; fi
          ;;
        CP_ALLOWED_CIDRS=*)
          seen_allowed=1
          if [ "$is_commented" -eq 1 ] && [ "$active_allowed" -eq 1 ]; then printf '%s\n' "$line"; else printf 'CP_ALLOWED_CIDRS=%s\n' "$allowed_cidrs"; fi
          ;;
        CP_UI_TOKEN=*)
          seen_ui=1
          if [ "$is_commented" -eq 1 ]; then
            if [ "$active_ui" -eq 1 ] || [ -z "$ui_token" ]; then printf '%s\n' "$line"; else printf 'CP_UI_TOKEN=%s\n' "$ui_token"; fi
          elif [ -n "$ui_token" ]; then
            printf 'CP_UI_TOKEN=%s\n' "$ui_token"
          else
            echo "#CP_UI_TOKEN="
          fi
          ;;
        CP_ALERT_WEBHOOK_URL=*)
          seen_webhook=1
          if [ "$is_commented" -eq 1 ]; then
            if [ "$active_webhook" -eq 1 ] || [ -z "$webhook_url" ]; then printf '%s\n' "$line"; else printf 'CP_ALERT_WEBHOOK_URL=%s\n' "$webhook_url"; fi
          elif [ -n "$webhook_url" ]; then
            printf 'CP_ALERT_WEBHOOK_URL=%s\n' "$webhook_url"
          else
            echo "#CP_ALERT_WEBHOOK_URL="
          fi
          ;;
        *) printf '%s\n' "$line" ;;
      esac
    done < "$ENV_FILE" > "$tmp_env"
  else
    {
      echo "# cloud-pulse-hub configuration. Managed by install-hub.sh;"
      echo "# manual edits are preserved across re-runs of the installer."
    } > "$tmp_env"
  fi

  {
    [ "$seen_listen" -eq 1 ] || printf 'CP_LISTEN=%s\n' "$listen"
    [ "$seen_data" -eq 1 ] || echo "CP_DATA_DIR=/var/lib/cloud-pulse"
    [ "$seen_agent" -eq 1 ] || printf 'CP_AGENT_TOKEN=%s\n' "$agent_token"
    [ "$seen_allowed" -eq 1 ] || printf 'CP_ALLOWED_CIDRS=%s\n' "$allowed_cidrs"
    if [ "$seen_ui" -eq 0 ]; then
      if [ -n "$ui_token" ]; then printf 'CP_UI_TOKEN=%s\n' "$ui_token"; else echo "#CP_UI_TOKEN="; fi
    fi
    if [ "$seen_webhook" -eq 0 ]; then
      if [ -n "$webhook_url" ]; then printf 'CP_ALERT_WEBHOOK_URL=%s\n' "$webhook_url"; else echo "#CP_ALERT_WEBHOOK_URL="; fi
    fi
    if [ ! -f "$ENV_FILE" ]; then
      echo "#CP_OFFLINE_AFTER=60s"
      echo "#CP_CLOUD_INTERVAL=15m"
      echo "#CP_LOG_LEVEL=info"
      echo "#CP_LOG_FORMAT=text"
      echo "# S3 bucket monitoring (optional):"
      echo "#CP_S3_BUCKETS=my-bucket:us-east-1"
      echo "#CP_S3_REGION=us-east-1"
      echo "#AWS_ACCESS_KEY_ID="
      echo "#AWS_SECRET_ACCESS_KEY="
      echo "#AWS_SESSION_TOKEN="
      echo "#CP_S3_FILTER_ID=EntireBucket"
      echo "# Cloudflare R2 bucket monitoring (optional):"
      echo "#CP_R2_ACCOUNT_ID="
      echo "#CP_R2_API_TOKEN="
      echo "#CP_R2_BUCKETS="
    fi
  } >> "$tmp_env"
}

write_env_file() {
  local agent_token ui_token webhook_url listen allowed_cidrs
  agent_token="$(resolve_agent_token)"
  validate_env_value "$agent_token" --agent-token
  if ui_token_will_be_generated; then UI_TOKEN_WAS_GENERATED=1; fi
  ui_token="$(resolve_ui_token)"
  webhook_url="$(resolve_webhook_url)"
  if [ -n "$ui_token" ]; then validate_env_value "$ui_token" --ui-token; fi
  if [ -n "$webhook_url" ]; then validate_env_value "$webhook_url" --webhook-url; fi
  listen="$(resolve_value "$OPT_LISTEN" CP_LISTEN "$DEFAULT_LISTEN" --listen)"
  allowed_cidrs="$(resolve_value "$OPT_ALLOWED_CIDRS" CP_ALLOWED_CIDRS "$DEFAULT_ALLOWED_CIDRS" --allowed-cidrs)"

  mkdir -p "$ETC_DIR"
  local tmp_env="${TMP_DIR}/hub.env"
  rewrite_hub_env "$tmp_env" "$listen" "$agent_token" "$allowed_cidrs" "$ui_token" "$webhook_url"

  install -m 0640 "$tmp_env" "$ENV_FILE"
  if [ "$IS_SANDBOX" -eq 1 ]; then
    log "sandbox: would run chown root:${CP_SERVICE_GROUP} ${ENV_FILE}"
  else
    chown "root:${CP_SERVICE_GROUP}" "$ENV_FILE"
  fi
  log "wrote ${ENV_FILE}"

  # Export values used by the dashboard/agent summary without re-reading.
  RESOLVED_AGENT_TOKEN="$agent_token"
  RESOLVED_UI_TOKEN="$ui_token"
  RESOLVED_LISTEN="$listen"
}
RESOLVED_AGENT_TOKEN=""
RESOLVED_UI_TOKEN=""
RESOLVED_LISTEN=""

# ---------------------------------------------------------------------------
# systemd unit rendering
# ---------------------------------------------------------------------------

render_unit() {
  mkdir -p "$SYSTEMD_DIR"
  local tmp_unit="${TMP_DIR}/cloud-pulse-hub.service"

  if [ "${CP_INSTALL_FORCE_SCRIPT_UNIT:-0}" != "1" ] && render_unit_via_binary "$tmp_unit"; then
    log "rendered ${UNIT_FILE} via '${BIN_PATH} systemd-unit print'"
  else
    render_unit_fallback "$tmp_unit"
    log "rendered ${UNIT_FILE} via built-in fallback template"
  fi

  install -m 0644 "$tmp_unit" "$UNIT_FILE"
  log "wrote ${UNIT_FILE}"
}

# render_unit_via_binary OUT_PATH — ask the freshly installed hub binary
# to render its own systemd unit (SPEC-v0.3.1 B, internal/systemdunit),
# so the unit definition has a single source of truth shared with
# `cloud-pulse-hub systemd-unit apply` (used by the self-updater to keep
# an existing unit in sync with future template changes). Returns
# non-zero (leaving OUT_PATH untouched/undefined) if the binary doesn't
# support the subcommand yet (older binary, or --version pinned to a
# release before it existed) or the call otherwise fails; the caller
# falls back to render_unit_fallback in that case, so this is never
# fatal on its own.
render_unit_via_binary() {
  local out_path="$1"
  if [ ! -x "$BIN_PATH" ]; then
    return 1
  fi
  local read_write_path=""
  if [ "$IS_SANDBOX" -eq 1 ]; then
    read_write_path="$DATA_DIR"
  fi
  local -a args=(systemd-unit print --bin-path "$BIN_PATH" --env-file "$ENV_FILE"
    --user "$CP_SERVICE_USER" --group "$CP_SERVICE_GROUP")
  if [ -n "$read_write_path" ]; then
    args+=(--read-write-path "$read_write_path")
  fi
  timeout 10 "$BIN_PATH" "${args[@]}" >"$out_path" 2>/dev/null
}

# render_unit_fallback OUT_PATH — bash heredoc fallback kept
# byte-identical to internal/systemdunit.Render's hub template (see
# SPEC-v0.3.1 B and the drift test in test-install.sh). Used when the
# installed binary doesn't support `systemd-unit print` yet (pre-v0.3.1
# binary, explicit --version pin, or CP_INSTALL_FORCE_SCRIPT_UNIT=1).
render_unit_fallback() {
  local out_path="$1"
  local exec_start="$BIN_PATH"
  local state_directory_line=""
  local read_write_paths=""

  if [ "$IS_SANDBOX" -eq 1 ]; then
    # StateDirectory is meaningless without a real systemd managing
    # /var/lib; keep the data dir as an explicit ReadWritePaths entry so
    # systemd-analyze verify still has a coherent unit to check.
    read_write_paths="ReadWritePaths=${DATA_DIR}"
  else
    state_directory_line="StateDirectory=cloud-pulse"
  fi

  {
    echo "[Unit]"
    echo "Description=cloud-pulse hub (metrics ingestion + dashboard)"
    echo "After=network-online.target"
    echo "Wants=network-online.target"
    echo
    echo "[Service]"
    echo "Type=simple"
    echo "EnvironmentFile=${ENV_FILE}"
    echo "Environment=CP_DATA_DIR=/var/lib/cloud-pulse"
    echo "ExecStart=${exec_start}"
    echo "User=${CP_SERVICE_USER}"
    echo "Group=${CP_SERVICE_GROUP}"
    echo "Restart=on-failure"
    echo "RestartSec=5"
    if [ -n "$state_directory_line" ]; then
      echo "$state_directory_line"
    fi
    if [ -n "$read_write_paths" ]; then
      echo "$read_write_paths"
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
  } > "$out_path"
}

verify_unit() {
  if ! command -v systemd-analyze >/dev/null 2>&1; then
    log "systemd-analyze not available; skipping unit verification"
    return
  fi

  local verify_target="$UNIT_FILE"
  if [ "$IS_SANDBOX" -eq 1 ]; then
    # systemd-analyze verify resolves ExecStart relative to the running
    # system, not CP_INSTALL_ROOT, so give it a stub unit whose
    # ExecStart points at a real, executable stand-in path (the binary
    # we just installed under the sandbox root, which does exist on
    # disk even though systemd itself is not managing it).
    verify_target="${TMP_DIR}/verify-cloud-pulse-hub.service"
    sed "s#^ExecStart=.*#ExecStart=${BIN_PATH}#" "$UNIT_FILE" > "$verify_target"
  fi

  if timeout 30 systemd-analyze verify "$verify_target" 2>&1 | tee "${TMP_DIR}/systemd-analyze.log"; then
    log "systemd-analyze verify: OK"
  else
    # systemd-analyze verify exits non-zero for warnings too (e.g.
    # missing User/Group on a system without that user visible to the
    # verifier sandbox in some environments); treat unit-file syntax
    # errors as fatal but log warnings and continue, since the
    # installer already constructs Users/paths correctly.
    if grep -qiE 'unknown (lvalue|section)|failed to parse|invalid syntax' "${TMP_DIR}/systemd-analyze.log"; then
      err "systemd-analyze verify reported a fatal unit syntax error"
      exit 1
    fi
    log "systemd-analyze verify reported warnings (see above); continuing"
  fi
}

# ---------------------------------------------------------------------------
# check-config + service start
# ---------------------------------------------------------------------------

# load_env_file_safe ENV_FILE — parse a systemd-style EnvironmentFile
# (plain KEY=VALUE lines, '#' comments, blank lines) WITHOUT invoking a
# shell on its contents, and print NUL-separated "KEY=VALUE" records
# suitable for `env -i ... "$(cmd)"`-free consumption via a while-read
# loop. This deliberately avoids `source`/`eval`/`bash -c "...$var..."`
# on file content that ultimately derives from installer flags
# (--webhook-url, --ui-token, --agent-token, ...): sourcing such a file
# with bash performs full shell parsing (command substitution,
# backticks) on every line, which would let a value like
# --webhook-url 'https://x/$(rm -rf /)' execute arbitrary code as root.
# Lines that are not valid KEY=VALUE (KEY matching [A-Za-z_][A-Za-z0-9_]*)
# are rejected rather than silently ignored, since a malformed line in a
# file systemd will later parse as an EnvironmentFile is itself a sign
# something is wrong.
load_env_file_safe() {
  local file="$1" line key value
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      ''|'#'*) continue ;;
    esac
    case "$line" in
      [A-Za-z_]*=*) ;;
      *)
        err "malformed line in ${file} (expected KEY=VALUE): ${line}"
        exit 1
        ;;
    esac
    key="${line%%=*}"
    value="${line#*=}"
    case "$key" in
      *[!A-Za-z0-9_]*)
        err "malformed variable name in ${file}: ${key}"
        exit 1
        ;;
    esac
    printf '%s=%s\0' "$key" "$value"
  done < "$file"
}

run_check_config() {
  if [ ! -x "$BIN_PATH" ]; then
    return
  fi
  log "validating configuration with -check-config"
  local out status
  local -a env_args=()
  local rec
  while IFS= read -r -d '' rec; do
    env_args+=("$rec")
  done < <(load_env_file_safe "$ENV_FILE")
  set +e
  out="$(env -i "PATH=$PATH" "${env_args[@]}" "${BIN_PATH}" -check-config 2>&1)"
  status=$?
  set -e
  echo "$out"
  if [ "$status" -ne 0 ]; then
    err "-check-config failed; aborting before starting the service"
    exit 1
  fi
}

start_service() {
  local service="cloud-pulse-hub.service"
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

  err "${service} failed to ${SERVICE_ACTION}; check: journalctl -u cloud-pulse-hub"
  exit 1
}

# ---------------------------------------------------------------------------
# Dashboard URL + agent one-liner
# ---------------------------------------------------------------------------

detect_dashboard_host() {
  if command -v tailscale >/dev/null 2>&1; then
    local ts_ip
    ts_ip="$(timeout 5 tailscale ip -4 2>/dev/null | head -n1 || true)"
    if [ -n "$ts_ip" ]; then
      echo "$ts_ip"
      return
    fi
  fi
  if command -v hostname >/dev/null 2>&1; then
    local ip
    ip="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"
    if [ -n "$ip" ]; then
      echo "$ip"
      return
    fi
  fi
  echo "<this-host>"
}

print_summary() {
  local host listen_port listen_host
  listen_port="${RESOLVED_LISTEN##*:}"
  listen_host="${RESOLVED_LISTEN%:*}"
  # An explicit bind address (e.g. 127.0.0.1:8090 or [fd7a::1]:8090) is the only
  # address the hub answers on, so print it; wildcards fall back to detection.
  case "$listen_host" in
    ""|"0.0.0.0"|"[::]"|"::") host="$(detect_dashboard_host)" ;;
    *) host="$listen_host" ;;
  esac

  echo
  log "cloud-pulse-hub installed successfully."
  echo "  Dashboard:      http://${host}:${listen_port}/"
  echo "  Config file:    ${ENV_FILE}"
  if [ -n "$SERVICE_ACTION" ]; then
    echo "  Service:        cloud-pulse-hub.service ${SERVICE_ACTION} (running ${SERVICE_VERSION})"
  else
    echo "  Service:        cloud-pulse-hub.service"
  fi
  echo
  print_version_hint
  print_auth_summary
  print_ui_token_summary
  echo "Agent one-liner (token shown once here; also stored in ${ENV_FILE}):"
  echo "  curl -fsSL https://raw.githubusercontent.com/${CP_REPO}/main/scripts/install-agent.sh | sudo bash -s -- --hub-url http://${host}:${listen_port} --token ${RESOLVED_AGENT_TOKEN}"
  echo
  echo "Note: network settings changed from the dashboard (Settings > Network) take precedence over CP_LISTEN in ${ENV_FILE}."
  echo
}

# print_auth_summary — SPEC-v0.4 §3: the dashboard now always requires
# signing in (there is no more "no CP_UI_TOKEN => open read API" mode),
# so every install/reinstall summary tells the operator how to sign in.
# A fresh install always bootstraps the single admin account with the
# default password "changeme" and forces a password change on first
# login; a reinstall never touches the existing password, so it prints
# a reminder plus the recovery command instead.
print_auth_summary() {
  if [ "$IS_UPGRADE" -eq 1 ]; then
    echo "Sign in: admin (password unchanged) — forgot it? sudo cloud-pulse-hub reset-password"
  else
    echo "Sign in: admin / changeme (you'll be asked to choose a new password)"
  fi
  echo
}

# print_ui_token_summary — SPEC-v0.4 §3: CP_UI_TOKEN is an OPTIONAL
# static API bearer token for scripts (the dashboard itself no longer
# uses it at all, see print_auth_summary above). Print it exactly once
# if it was newly generated/rotated this run, "unchanged" if one already
# existed and was kept, or a short note that no such token is configured
# if none was ever requested.
print_ui_token_summary() {
  if [ "$UI_TOKEN_WAS_GENERATED" -eq 1 ]; then
    echo "API token (optional, for scripts; shown once, stored in ${ENV_FILE}): ${RESOLVED_UI_TOKEN}"
  elif [ -n "$RESOLVED_UI_TOKEN" ]; then
    echo "API token: unchanged (sudo grep CP_UI_TOKEN ${ENV_FILE})"
  else
    echo "API token: not configured (optional; pass --generate-ui-token to create one for scripts)"
  fi
  echo
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
  echo "Future updates: sudo cloud-pulse-hub update"
  echo
}

# ---------------------------------------------------------------------------
# dry-run
# ---------------------------------------------------------------------------

print_dry_run() {
  echo "[install-hub] dry-run: would perform the following actions:"
  echo "  - detect OS/arch, download ${CP_RAW_ASSET_NAME_HUB_PREFIX}-${OS_NAME}-${ARCH} (version: ${OPT_VERSION:-latest})"
  echo "  - verify sha256 against checksums.txt"
  echo "  - install -m 0755 to ${BIN_PATH}"
  echo "  - ensure system user/group '${CP_SERVICE_USER}' exists"
  echo "  - write ${ENV_FILE} (mode 0640, owner root:${CP_SERVICE_GROUP})"
  echo "  - write ${UNIT_FILE}"
  echo "  - systemd-analyze verify the unit"
  echo "  - ${SYSTEMCTL} daemon-reload && ${SYSTEMCTL} enable cloud-pulse-hub.service"
  echo "  - start cloud-pulse-hub.service if inactive; restart it if already running"
  echo "  - run -check-config before starting"
  if [ -x "$BIN_PATH" ]; then
    local existing_version
    existing_version="$(probe_existing_version "$BIN_PATH")"
    echo "  - this is an upgrade over an existing install${existing_version:+ (currently ${existing_version})}; would print 'Upgraded vA → vB'"
    if version_is_legacy "$existing_version"; then
      echo "  - would print: This install now includes the built-in updater."
    fi
  fi
  echo "  - would print: Future updates: sudo cloud-pulse-hub update"
}

# ---------------------------------------------------------------------------
# Interactive menu (SPEC-v0.3.1 A)
# ---------------------------------------------------------------------------

# is_installed — true iff a hub binary is currently installed at
# BIN_PATH (setup_paths must have already run).
is_installed() {
  [ -x "$BIN_PATH" ]
}

# service_status_text — human-readable "(service: active|inactive|...)"
# suffix for the menu's status line, or empty outside sandbox detection
# failure. Best-effort only; never fails the script.
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
  state="$("$SYSTEMCTL" is-active cloud-pulse-hub.service 2>/dev/null || true)"
  if [ -n "$state" ]; then
    echo " (service: ${state})"
  else
    echo ""
  fi
}

# prompt_fresh_install_values — interactively collect the values a fresh
# install needs (SPEC-v0.4 §3 "1) Install" prompts: listen port and an
# optional alert webhook URL only — the web Settings page's UI-token
# prompts were removed once the dashboard gained its own admin/changeme
# sign-in; CP_UI_TOKEN is now purely an optional static API token for
# scripts, set via --generate-ui-token/--rotate-ui-token/--ui-token),
# applying the same validate_env_value checks used for the equivalent
# flags. Only called when OPT_ACTION=install (menu path or --install
# flag) and nothing is installed yet.
prompt_fresh_install_values() {
  local answer

  prompt_tty "Listen port [8090]: "
  if ! read_line_tty answer; then
    err "unexpected EOF on /dev/tty"
    exit 1
  fi
  if [ -n "$answer" ]; then
    case "$answer" in
      ''|*[!0-9]*)
        err "listen port must be numeric"
        exit 1
        ;;
    esac
    if [ "$answer" -lt 1 ] || [ "$answer" -gt 65535 ]; then
      err "listen port must be between 1 and 65535"
      exit 1
    fi
    OPT_LISTEN=":${answer}"
    validate_env_value "$OPT_LISTEN" --listen
  fi

  prompt_tty "Alert webhook URL (optional, Enter to skip): "
  if ! read_line_tty answer; then
    err "unexpected EOF on /dev/tty"
    exit 1
  fi
  if [ -n "$answer" ]; then
    validate_env_value "$answer" --webhook-url
    OPT_WEBHOOK_URL="$answer"
  fi
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
    echo "cloud-pulse hub installer"
    echo "  Status: ${status_line}"
    echo "  1) Install      (설치)"
    echo "  2) Reinstall    (재설치: latest version, keeps settings, tokens and data)"
    echo "  3) Uninstall    (삭제)"
    echo "  0) Exit"
  } >/dev/tty
}

# run_menu — show the menu and read a single choice from /dev/tty,
# re-prompting on invalid input up to 5 times. Sets OPT_ACTION or exits
# (0 for explicit Exit, 1 for too many invalid tries or EOF).
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

# menu_loop — the full interactive session: shows the menu repeatedly
# (SPEC: "1) Install on installed -> message + menu again"; every other
# choice performs the action once and exits).
menu_loop() {
  while :; do
    run_menu
    case "$OPT_ACTION" in
      install)
        if is_installed; then
          echo >/dev/tty
          echo "cloud-pulse-hub is already installed. Choose 2 to reinstall/upgrade, or run 'sudo cloud-pulse-hub update' on v0.3.0+." >/dev/tty
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
          echo "cloud-pulse-hub is not installed. Choose 1 to install." >/dev/tty
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
        if ! confirm_tty "Uninstall cloud-pulse-hub?" 0; then
          OPT_ACTION=""
          continue
        fi
        if confirm_tty "Also delete configuration and tokens (${ENV_FILE}) and ALL collected data (${DATA_DIR})?" 0; then
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
    log "sandbox: would run ${SYSTEMCTL} stop cloud-pulse-hub.service"
    log "sandbox: would run ${SYSTEMCTL} disable cloud-pulse-hub.service"
  elif command -v "$SYSTEMCTL" >/dev/null 2>&1; then
    "$SYSTEMCTL" stop cloud-pulse-hub.service 2>/dev/null || true
    "$SYSTEMCTL" disable cloud-pulse-hub.service 2>/dev/null || true
  fi

  rm -f "$UNIT_FILE"
  rm -f "$BIN_PATH"
  log "removed unit and binary"

  if { [ "$IS_SANDBOX" -ne 1 ] || [ -n "${CP_SYSTEMCTL:-}" ]; } && command -v "$SYSTEMCTL" >/dev/null 2>&1; then
    "$SYSTEMCTL" daemon-reload || true
  fi

  if [ "$OPT_PURGE" -eq 1 ]; then
    rm -f "$ENV_FILE"
    rm -rf "$DATA_DIR"
    log "purged config and data (--purge)"
  else
    log "kept ${ENV_FILE} and ${DATA_DIR} (pass --purge to remove them)"
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

  TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/cloud-pulse-hub-install.XXXXXX")"
  trap 'rm -rf "$TMP_DIR"' EXIT

  download_and_verify
  ensure_system_user
  install_binary
  write_env_file
  render_unit
  verify_unit
  run_check_config
  start_service
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
      echo "[install-hub] dry-run: would uninstall (purge=${OPT_PURGE})"
      exit 0
    fi
    do_uninstall
    exit 0
  fi

  if [ "$OPT_DRY_RUN" -eq 1 ]; then
    print_dry_run
    exit 0
  fi

  # Menu only when: no arguments at all AND interactive (tty available
  # for both read+write). Any flag at all, or no tty, means the
  # existing flag-driven behavior below runs unmodified: an explicit
  # action (--install/--reinstall) is validated against the current
  # install state; no action at all keeps the historical "auto"
  # behavior (install when not installed, reinstall/upgrade when
  # installed).
  if [ "$OPT_ANY_FLAG_GIVEN" -eq 0 ] && tty_available; then
    menu_loop
    return
  fi

  case "$OPT_ACTION" in
    install)
      if is_installed; then
        err "already installed; choose --reinstall to upgrade, or run 'sudo cloud-pulse-hub update' on v0.3.0+"
        exit 1
      fi
      ;;
    reinstall)
      if ! is_installed; then
        err "not installed; use --install for a fresh install"
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
