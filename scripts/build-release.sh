#!/usr/bin/env bash
# build-release.sh — build cloud-pulse-hub and cloud-pulse-agent release binaries
# for all supported target platforms, and produce a checksums.txt.
#
# Usage:
#   scripts/build-release.sh <version> [outdir=dist]
#
# Env overrides:
#   TARGETS            space-separated "goos/goarch" list (default: all supported targets)
#   ALLOW_ANY_VERSION  if "1", skip strict version format validation
#
# CGO is always disabled for these builds (CGO stays off, never turned on).
set -euo pipefail

usage() {
  echo "Usage: $0 <version> [outdir=dist]" >&2
  echo "  version must match v[0-9]+.[0-9]+.[0-9]+(-.+)? unless ALLOW_ANY_VERSION=1" >&2
  exit 1
}

if [ "$#" -lt 1 ]; then
  usage
fi

VERSION="$1"
OUTDIR="${2:-dist}"

VERSION_RE='^v[0-9]+\.[0-9]+\.[0-9]+(-.+)?$'
if [ "${ALLOW_ANY_VERSION:-0}" != "1" ]; then
  if ! [[ "$VERSION" =~ $VERSION_RE ]]; then
    echo "error: version '$VERSION' does not match required format: v<major>.<minor>.<patch>[-<pre>]" >&2
    echo "       (set ALLOW_ANY_VERSION=1 to bypass this check)" >&2
    exit 1
  fi
fi

MODULE="github.com/leejeonghun001/cloud-pulse"

# Default release targets: goos/goarch pairs. linux/arm uses GOARM=7 (armv7 asset suffix).
DEFAULT_TARGETS="linux/amd64 linux/arm64 linux/arm darwin/amd64 darwin/arm64 windows/amd64 windows/arm64 freebsd/amd64"
TARGETS="${TARGETS:-$DEFAULT_TARGETS}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

mkdir -p "$OUTDIR"

COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo none)"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

LDFLAGS="-s -w -X ${MODULE}/internal/version.Version=${VERSION} -X ${MODULE}/internal/version.Commit=${COMMIT} -X ${MODULE}/internal/version.Date=${BUILD_DATE}"

# summary table rows: name os arch size status
declare -a SUMMARY_ROWS=()

sha256_of() {
  # portable sha256 helper: prefer sha256sum, fall back to shasum -a 256
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1"
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1"
  else
    echo "error: neither sha256sum nor shasum is available" >&2
    exit 1
  fi
}

build_binary() {
  local goos="$1" goarch="$2" goarm="$3" pkg="$4" name="$5" asset_arch="$6"
  local ext=""
  if [ "$goos" = "windows" ]; then
    ext=".exe"
  fi

  local out_name="${name}-${goos}-${asset_arch}${ext}"
  local out_path="${OUTDIR}/${out_name}"

  local size="-"
  local status="ok"

  if CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" GOARM="$goarm" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$out_path" "$pkg"; then
    if [ -f "$out_path" ]; then
      size="$(du -h "$out_path" | cut -f1)"
    fi
  else
    status="FAILED"
  fi

  SUMMARY_ROWS+=("${out_name}|${goos}/${goarch}${goarm:+ (armv${goarm})}|${size}|${status}")

  if [ "$status" = "FAILED" ]; then
    echo "error: build failed for ${out_name}" >&2
    return 1
  fi
}

echo "Building cloud-pulse ${VERSION} (commit ${COMMIT}, date ${BUILD_DATE})"
echo "Targets: ${TARGETS}"
echo

for target in $TARGETS; do
  goos="${target%/*}"
  goarch="${target#*/}"

  goarm=""
  asset_arch="$goarch"
  if [ "$goos" = "linux" ] && [ "$goarch" = "arm" ]; then
    goarm="7"
    asset_arch="armv7"
  fi

  if [ -d "cmd/hub" ]; then
    build_binary "$goos" "$goarch" "$goarm" "./cmd/hub" "cloud-pulse-hub" "$asset_arch"
  fi
  if [ -d "cmd/agent" ]; then
    build_binary "$goos" "$goarch" "$goarm" "./cmd/agent" "cloud-pulse-agent" "$asset_arch"
  fi
done

if [ ! -d "cmd/hub" ] && [ ! -d "cmd/agent" ]; then
  echo "warning: neither cmd/hub nor cmd/agent exist; nothing was built." >&2
fi

# Write checksums.txt in sha256sum format: "<hash>  <filename>"
CHECKSUMS_FILE="${OUTDIR}/checksums.txt"
: > "$CHECKSUMS_FILE"
shopt -s nullglob
BINARIES=()
for f in "$OUTDIR"/cloud-pulse-*; do
  BINARIES+=("$f")
done
shopt -u nullglob

if [ "${#BINARIES[@]}" -gt 0 ]; then
  (
    cd "$OUTDIR"
    for f in "${BINARIES[@]}"; do
      sha256_of "$(basename "$f")"
    done
  ) >> "$CHECKSUMS_FILE"
fi

echo
echo "Summary:"
printf '%-40s %-22s %-8s %s\n' "ASSET" "TARGET" "SIZE" "STATUS"
printf '%-40s %-22s %-8s %s\n' "-----" "------" "----" "------"
for row in "${SUMMARY_ROWS[@]}"; do
  IFS='|' read -r name tgt size status <<<"$row"
  printf '%-40s %-22s %-8s %s\n' "$name" "$tgt" "$size" "$status"
done
echo
echo "checksums: ${CHECKSUMS_FILE}"
