#!/usr/bin/env bash
# Full integration or separately gated --focused-proof prebuild. The latter
# runs only focused source/helper units and checks, NOT full npm/Go/Python QA.
# Both build a fresh development binary after source stability; never start hub.
set -uo pipefail
umask 077
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
if [[ "${1:-}" == --help && $# == 1 ]]; then
  echo 'usage: verify-round28-integrated.sh [--firstimpression|--focused-proof]'
  exit 0
fi
source "$REPO_ROOT/scripts/verify-round28-env.sh" || exit 2
cd "$REPO_ROOT" || exit 2
ROOT="$PWD/.verification/round28"
FIRSTIMPRESSION=0
FOCUSED_BUILD=0
if [[ "${1:-}" == --firstimpression && $# == 1 ]]; then FIRSTIMPRESSION=1;
elif [[ "${1:-}" == --focused-proof && $# == 1 ]]; then FIRSTIMPRESSION=1; FOCUSED_BUILD=1;
elif (( $# )); then echo 'usage: verify-round28-integrated.sh [--firstimpression|--focused-proof]' >&2; exit 2; fi
INTEGRATION_ROOT="$ROOT/integrated"
if (( FIRSTIMPRESSION )); then INTEGRATION_ROOT="$ROOT/firstimpression/integration"; fi
if (( FOCUSED_BUILD )); then
  node --input-type=module -e 'import{readFileSync}from"node:fs";const p=JSON.parse(readFileSync(".verification/round28/capture-plan.json"));if(p.focusedProofGateAccepted!==true)throw new Error("Focused prebuild gate closed");' || exit 2
  INTEGRATION_ROOT="$ROOT/firstimpression/focused-proof/build"
fi
node scripts/verify-round28-safety.mjs --path "$INTEGRATION_ROOT" >/dev/null || exit 2
mkdir -p "$INTEGRATION_ROOT"
RUN="$(mktemp -d "$INTEGRATION_ROOT/run.XXXXXX")"
mkdir -p "$RUN/logs"
printf '%s\n' "$RUN" > "$INTEGRATION_ROOT/latest-run.txt"
printf '%s\n' "TMPDIR=$TMPDIR" "TMP=$TMP" "TEMP=$TEMP" "GOCACHE=$GOCACHE" "GOMODCACHE=$GOMODCACHE" "PYTHONPYCACHEPREFIX=$PYTHONPYCACHEPREFIX" "npm_config_cache=$npm_config_cache" "HOME=$HOME" "CGO_ENABLED=$CGO_ENABLED" "GOPROXY=$GOPROXY" "GOSUMDB=$GOSUMDB" > "$RUN/environment.txt"
{ go version; node --version; npm --version; python3 --version; } > "$RUN/toolchain.txt" 2>&1

snapshot() {
  python3 - "$1" <<'PY'
import hashlib, json, os, pathlib, subprocess, sys
root = pathlib.Path.cwd()
paths = subprocess.check_output(['git', 'ls-files', '--cached', '--others', '--exclude-standard', '-z']).split(b'\0')
manifest = {}
for raw in sorted(set(paths)):
    if not raw:
        continue
    name = os.fsdecode(raw)
    path = root / name
    if not path.exists():
        manifest[name] = {'deleted': True}
    elif path.is_symlink():
        manifest[name] = {'symlink': os.readlink(path)}
    elif path.is_file():
        manifest[name] = {'sha256': hashlib.sha256(path.read_bytes()).hexdigest(), 'size': path.stat().st_size}
result = {'head': subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip(),
          'branch': subprocess.check_output(['git','branch','--show-current'],text=True).strip(),
          'files': manifest}
inventory=[]
for name in sorted(manifest,key=lambda name:name.encode('utf-8')):
    path=root/name
    if not path.exists(): continue
    mode='120000' if path.is_symlink() else '100755' if path.stat().st_mode & 0o111 else '100644'
    payload=os.readlink(path).encode('utf-8') if path.is_symlink() else path.read_bytes()
    inventory.append({'path':name,'mode':mode,'sha256':hashlib.sha256(payload).hexdigest()})
result['input_inventory']=inventory
result['tree_sha256']=hashlib.sha256(json.dumps(inventory,ensure_ascii=False,separators=(',',':')).encode('utf-8')).hexdigest()
pathlib.Path(sys.argv[1]).write_text(json.dumps(result,sort_keys=True,indent=2)+'\n')
PY
}
snapshot "$RUN/source-before.json" || exit 2
git status --short > "$RUN/git-status-before.txt"
git diff --stat > "$RUN/git-diff-stat-before.txt"
FAILED=0
run() {
  local label="$1" rc=0
  shift
  printf '%q ' "$@" >> "$RUN/commands.txt"; printf '\n' >> "$RUN/commands.txt"
  "$@" > "$RUN/logs/$label.log" 2>&1 || rc=$?
  printf '%s\texit=%s\n' "$label" "$rc" >> "$RUN/results.tsv"
  if (( rc )); then FAILED=1; fi
}
if (( FOCUSED_BUILD )); then
  run focused-source-unit node --test web/test/dialog.test.mjs web/test/onboarding.test.mjs web/test/main-refresh.test.mjs web/test/notifications-workflow.test.mjs web/test/costs-billing-render.test.mjs
else
  run go-build-all go build ./...
  run go-vet-all go vet ./...
  GOFLAGS=-v run go-test-all go test ./...
  run npm-test npm test
fi
run verification-safety node --test scripts/verify-round28-safety.test.mjs scripts/verify-round28-gallery.test.mjs
run firstimpression-script node --test scripts/verify-round28-firstimpression.test.mjs
run runtime-helper node --test scripts/verify-round28-runtime.test.mjs
run focused-proof-helper node --test scripts/verify-round28-focused-proof.test.mjs
if (( ! FOCUSED_BUILD )); then
  run python-suite make test-py
  run raw-docs python3 scripts/check-docs.py --check-flags
  run source-docs node scripts/verify-round28-docs.mjs
fi
run i18n node scripts/check-i18n.mjs --json
run css-selectors node scripts/verify-round28-css.mjs
run diff-whitespace git diff --check
run verification-shell-syntax bash -n scripts/verify-phase1-browser.sh scripts/verify-round28-env.sh scripts/verify-round28-integrated.sh
run firstimpression-syntax node --check scripts/verify-round28-firstimpression.mjs
run runtime-syntax node --check scripts/verify-round28-runtime.mjs
run merge-syntax node --check scripts/verify-round28-merge.mjs
run runtime-lib-syntax node --check scripts/verify-round28-runtime-lib.mjs
run runtime-test-syntax node --check scripts/verify-round28-runtime.test.mjs
run auth-helper-syntax node --check scripts/verify-phase1-auth.mjs
run focused-proof-syntax node --check scripts/verify-round28-focused-proof.mjs
snapshot "$RUN/source-after-tests.json" || FAILED=1
if ! cmp -s "$RUN/source-before.json" "$RUN/source-after-tests.json"; then
  printf 'FAIL: source changed during integrated tests; no audit binary built.\n' > "$RUN/source-stability.txt"
  FAILED=1
else
  printf 'PASS: complete nonignored worktree source manifest unchanged during tests.\n' > "$RUN/source-stability.txt"
fi

if (( ! FAILED )); then
  BIN_DIR="$ROOT/audit-bin"
  if (( FIRSTIMPRESSION )); then BIN_DIR="$ROOT/firstimpression/dev-qa"; fi
  if (( FOCUSED_BUILD )); then BIN_DIR="$ROOT/firstimpression/dev-qa/focused-$(basename "$RUN")"; fi
  BIN="$BIN_DIR/cloud-pulse-hub"
  node scripts/verify-round28-safety.mjs --path "$BIN" >/dev/null || exit 2
  mkdir -p "$BIN_DIR"
  COMMIT="$(git rev-parse HEAD)"
  BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  VERSION=dev-round28-dirty
  LDFLAGS="-X github.com/leejeonghun001/cloud-pulse/internal/version.Version=$VERSION -X github.com/leejeonghun001/cloud-pulse/internal/version.Commit=${COMMIT}-dirty -X github.com/leejeonghun001/cloud-pulse/internal/version.Date=$BUILD_DATE"
  # No release -s/-w/-trimpath flags or packaging; this is Step2 preliminary QA.
  run development-audit-build go build -ldflags "$LDFLAGS" -o "$BIN" ./cmd/hub
  if (( ! FAILED )); then
    SHA256="$(sha256sum "$BIN" | cut -d' ' -f1)"
    run development-audit-header node scripts/verify-round28-safety.mjs --audit-binary "$BIN" "$SHA256"
    go version -m "$BIN" > "$BIN_DIR/buildinfo.txt"
    cp "$RUN/source-after-tests.json" "$BIN_DIR/source-manifest.json"
    printf '%s\n' "$SHA256" > "$BIN_DIR/cloud-pulse-hub.sha256"
    printf 'purpose=preliminary-development-QA-only\nrelease_verification=NOT_PERFORMED\nversion=%s\ncommit=%s-dirty\ncreated_at=%s\nbinary=%s\nsha256=%s\n' "$VERSION" "$COMMIT" "$BUILD_DATE" "$BIN" "$SHA256" > "$BIN_DIR/provenance.txt"
    snapshot "$RUN/source-after-build.json" || FAILED=1
    if ! cmp -s "$RUN/source-after-tests.json" "$RUN/source-after-build.json"; then
      printf 'FAIL: source changed during audit build; audit binary is not accepted.\n' >> "$RUN/source-stability.txt"
      FAILED=1
    else
      printf 'PASS: complete source manifest unchanged through native audit build.\n' >> "$RUN/source-stability.txt"
    fi
  fi
fi

python3 - "$RUN" "${BIN_DIR:-$ROOT/firstimpression/dev-qa}" "$FAILED" "$FOCUSED_BUILD" <<'PY'
import hashlib,json,pathlib,re,sys
run=pathlib.Path(sys.argv[1]); binary_dir=pathlib.Path(sys.argv[2]); failed=int(sys.argv[3]); focused=bool(int(sys.argv[4]))
commands=(run/'commands.txt').read_text().splitlines()
rows=(run/'results.tsv').read_text().splitlines()
exits={row.split('\t')[0]:int(row.split('exit=')[1]) for row in rows}
def log(name):
    path=run/'logs'/f'{name}.log'
    return path.read_text() if path.exists() else ''
go=log('go-test-all'); js=log('npm-test'); safety=log('verification-safety'); py=log('python-suite')
def count(pattern,text): return len(re.findall(pattern,text,re.M))
def total(pattern,text):
    match=re.search(pattern,text)
    return int(match[1]) if match else None
before=json.loads((run/'source-before.json').read_text())
assets={name:item['sha256'] for name,item in before['files'].items() if name.startswith('web/assets/') and 'sha256' in item}
(run/'source-asset-digests.json').write_text(json.dumps(assets,sort_keys=True,indent=2)+'\n')
if not failed and (binary_dir/'cloud-pulse-hub').exists():
    (binary_dir/'asset-manifest.json').write_text(json.dumps(assets,sort_keys=True,indent=2)+'\n')
summary={'status':'failed' if failed else 'passed','scope':'focused-prebuild-not-full-suite' if focused else 'full-integration','binary_dir':str(binary_dir),'commands':commands,'exits':exits,
 'go':{'packages_passed':count(r'^ok\s',go),'cached_packages':count(r'^ok\s.*\(cached\)',go),
       'top_level_pass':count(r'^--- PASS:',go),'pass_including_subtests':count(r'^\s*--- PASS:',go),
       'skip_including_subtests':count(r'^\s*--- SKIP:',go),'failed':count(r'^\s*--- FAIL:',go)},
 'npm':{'tests':total(r'ℹ tests (\d+)',js),'passed':total(r'ℹ pass (\d+)',js),'failed':total(r'ℹ fail (\d+)',js)},
  'verification_safety':{'tests':total(r'ℹ tests (\d+)',safety),'passed':total(r'ℹ pass (\d+)',safety),'failed':total(r'ℹ fail (\d+)',safety)},
  'capture_helper':{'tests':total(r'ℹ tests (\d+)',log('firstimpression-script')),'passed':total(r'ℹ pass (\d+)',log('firstimpression-script')),'failed':total(r'ℹ fail (\d+)',log('firstimpression-script'))},
  'runtime_helper':{'tests':total(r'ℹ tests (\d+)',log('runtime-helper')),'passed':total(r'ℹ pass (\d+)',log('runtime-helper')),'failed':total(r'ℹ fail (\d+)',log('runtime-helper'))},
 'python':{'tests':total(r'Ran (\d+) tests',py),'skipped':total(r'OK \(skipped=(\d+)\)',py)},
 'docs_output':log('raw-docs').strip(), 'asset_count':len(assets),
 'source_stability':(run/'source-stability.txt').read_text(),
 'hub_started':False,'firefox_started':False,'gallery_started':False,'release_built':False,
 'development_audit_binary_prepared':not failed and exits.get('development-audit-header')==0}
try: summary['i18n']=json.loads(log('i18n'))
except json.JSONDecodeError: summary['i18n_output']=log('i18n')
(run/'summary.json').write_text(json.dumps(summary,indent=2)+'\n')
markdown=['# Integrated Round28 Step1','',f"Status: **{summary['status']}**",'', '## Actual commands and exits']
markdown.extend(f'- `{command.strip()}` — exit {exits[row.split(chr(9))[0]]}' for command,row in zip(commands,rows))
markdown.extend(['','## Totals',f"Go: {summary['go']}",f"Literal npm test: {summary['npm']}",f"Verification safety: {summary['verification_safety']}",f"Python: {summary['python']}",summary['docs_output'],f'Assets pinned: {len(assets)}','',summary['source_stability'],
 'No hub/gallery/Firefox started. No Step3 release built or verified.'])
if summary['development_audit_binary_prepared']:
    sha=(binary_dir/'cloud-pulse-hub.sha256').read_text().strip()
    markdown.extend(['','## Next login-12 matrix gate (only after parent acceptance)','```bash','(', '  source scripts/verify-round28-env.sh',
      '  bash scripts/verify-phase1-browser.sh --stage round28 \\',
      '    --artifact-root "$PWD/.verification/round28/step2-development" \\',
      '    --audit-binary "$PWD/.verification/round28/audit-bin/cloud-pulse-hub" \\',
      f'    --binary-sha256 {sha} --audit-accepted',')','```',
      'This tests the exact development audit binary only. Step3 must build its own release candidate and explicitly test that different binary.'])
(run/'summary.md').write_text('\n'.join(markdown)+'\n')
print(json.dumps({key:summary[key] for key in ['status','go','npm','verification_safety','python','docs_output','development_audit_binary_prepared']},indent=2))
print(f'Evidence: {run}')
PY
exit "$FAILED"
