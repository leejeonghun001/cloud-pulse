#!/usr/bin/env bash
# PREPARED executor: only after parent accepts terminal trap source and merged
# catalogs/CSS. Builds a fresh dev binary, runs focused proof only, never full QA.
set -euo pipefail
umask 077
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
if [[ "${1:-}" == --help && $# == 1 ]]; then
  echo 'usage: bash scripts/verify-round28-focused-proof.sh --focused-proof --parent-accepted'
  exit 0
fi
source "$REPO_ROOT/scripts/verify-round28-env.sh" || exit 2
cd "$REPO_ROOT"
if [[ $# != 2 || "$1" != --focused-proof || "$2" != --parent-accepted ]]; then
  echo 'usage: bash scripts/verify-round28-focused-proof.sh --focused-proof --parent-accepted' >&2; exit 2
fi
node --input-type=module -e 'import{readFileSync}from"node:fs";const p=JSON.parse(readFileSync(".verification/round28/capture-plan.json"));if(p.focusedProofGateAccepted!==true)throw new Error("Focused proof gate closed: no build/server permitted");'
bash scripts/verify-round28-integrated.sh --focused-proof
build_run="$(< .verification/round28/firstimpression/focused-proof/build/latest-run.txt)"
binary_dir="$(node --input-type=module -e 'import{readFileSync}from"node:fs";const r=JSON.parse(readFileSync(process.argv[1]));if(r.status!=="passed"||r.scope!=="focused-prebuild-not-full-suite")throw new Error("Prebuild not accepted");console.log(r.binary_dir)' "$build_run/summary.json")"
binary_sha="$(< "$binary_dir/cloud-pulse-hub.sha256")"
while IFS='=' read -r key value; do
  case "$key" in
    version) export CP_VERIFY_EXPECTED_VERSION="$value" ;;
    commit) export CP_VERIFY_EXPECTED_COMMIT="$value" ;;
    created_at) export CP_VERIFY_EXPECTED_DATE="$value" ;;
  esac
done < "$binary_dir/provenance.txt"
bash scripts/verify-phase1-browser.sh --stage round28 --focused-proof \
  --artifact-root "$PWD/.verification/round28/firstimpression/focused-proof/runtime" \
  --audit-binary "$binary_dir/cloud-pulse-hub" --binary-sha256 "$binary_sha" --audit-accepted
echo 'Focused proof passed only. Parent must authorize full QA on this SAME pinned binary; no rebuild/source changes allowed.'
echo "exact_binary=$binary_dir/cloud-pulse-hub sha256=$binary_sha prebuild=$build_run"
