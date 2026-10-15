#!/usr/bin/env bash
# Local-only pre-browser verification for the Phase 1 UI review.
# All generated files (including subprocess temp files, Go build cache,
# Python bytecode and logs) stay in this checkout's ignored .verification/.
set -euo pipefail
umask 077
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
source "$REPO_ROOT/scripts/verify-round28-env.sh" || exit 2

ALLOWED_STAGES=(phase1 card-grid detail activity round28)
STAGE="${CP_VERIFY_STAGE:-phase1}"
ARTIFACT_ROOT="${CP_VERIFY_ARTIFACT_ROOT:-}"
GO_ONLY=0
while (( $# )); do
  case "$1" in
    --stage)
      (( $# >= 2 )) || { echo "--stage requires phase1|card-grid|detail|activity|round28" >&2; exit 2; }
      STAGE="$2"
      shift 2
      ;;
    --artifact-root)
      (( $# >= 2 )) || { echo "--artifact-root requires a value" >&2; exit 2; }
      ARTIFACT_ROOT="$2"
      shift 2
      ;;
    --go-only)
      GO_ONLY=1
      shift
      ;;
    -h|--help)
      echo "usage: scripts/verify-phase1-prep.sh [--stage phase1|card-grid|detail|activity|round28] [--artifact-root DIR] [--go-only]"
      echo "--go-only skips the unchanged Python/docs checks."
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

VERIFY_ROOT="${REPO_ROOT}/.verification"
STAGE_ROOT="${ARTIFACT_ROOT:-${VERIFY_ROOT}/${STAGE}}"
STAGE_ROOT="$(node "${REPO_ROOT}/scripts/verify-round28-safety.mjs" --path "$STAGE_ROOT")" || exit 2
if [[ "$STAGE" == round28 && "$STAGE_ROOT" != "${VERIFY_ROOT}/round28" && "$STAGE_ROOT" != "${VERIFY_ROOT}/round28/"* ]]; then
  echo "Round28 artifact root must remain beneath .verification/round28" >&2; exit 2
fi
CACHE_ROOT="${VERIFY_ROOT}/round28/cache"
mkdir -p "${STAGE_ROOT}"
RUN_ROOT="$(mktemp -d "${STAGE_ROOT}/prep.XXXXXX")"
TMP_ROOT="${RUN_ROOT}/tmp"
LOG_ROOT="${RUN_ROOT}/logs"

mkdir -p "${TMP_ROOT}" "${LOG_ROOT}" "${CACHE_ROOT}/go-build" "${CACHE_ROOT}/python"
chmod 700 "${STAGE_ROOT}" "${RUN_ROOT}" "${TMP_ROOT}" "${LOG_ROOT}" \
  "${CACHE_ROOT}" "${CACHE_ROOT}/go-build" "${CACHE_ROOT}/python"

export TMPDIR="${TMP_ROOT}"
export TMP="${TMP_ROOT}"
export TEMP="${TMP_ROOT}"
export GOCACHE="${CACHE_ROOT}/go-build"
export PYTHONPYCACHEPREFIX="${CACHE_ROOT}/python"
export CGO_ENABLED=0

cd "${REPO_ROOT}"

run_logged() {
  local name="$1"
  shift
  printf '\n==> %s\n' "$name"
  "$@" 2>&1 | tee "${LOG_ROOT}/${name//[^a-zA-Z0-9._-]/_}.log"
}

run_logged "go-build-all" go build ./...
run_logged "go-vet-all" go vet ./...
run_logged "go-test-all" go test ./...
if (( ! GO_ONLY )); then
  run_logged "python-unit-and-compile" make test-py
  run_logged "docs-integrity" python3 scripts/check-docs.py --check-flags
fi

printf '\nPre-browser verification passed for stage %s%s. Logs: %s\n' \
  "${STAGE}" "$([[ $GO_ONLY == 1 ]] && printf ' (Go-only)')" "${LOG_ROOT}"
