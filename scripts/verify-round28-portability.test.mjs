import test from 'node:test';
import assert from 'node:assert/strict';
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync } from 'node:fs';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { REPO_ROOT, ROUND28_ROOT, safeVerificationPath } from './verify-round28-safety.mjs';

const names = ['verify-phase1-browser.sh', 'verify-phase1-prep.sh', 'verify-round28-baseline.sh',
  'verify-round28-env.sh', 'verify-round28-focused-proof.sh', 'verify-round28-integrated.sh'];
const scratch = safeVerificationPath(join(ROUND28_ROOT, 'tmp'));
mkdirSync(scratch, { recursive: true, mode: 0o700 });

function fixture() {
  const base = mkdtempSync(join(scratch, 'portable checkout with spaces '));
  const repo = join(base, 'copied checkout');
  mkdirSync(join(repo, 'scripts'), { recursive: true, mode: 0o700 });
  mkdirSync(join(base, 'different cwd'), { mode: 0o700 });
  for (const name of [...names, 'verify-round28-safety.mjs']) copyFileSync(join(REPO_ROOT, 'scripts', name), join(repo, 'scripts', name));
  return { base, repo, cwd: join(base, 'different cwd'), round: join(repo, '.verification/round28') };
}
function bash(f, args, env = {}) {
  return spawnSync('bash', args, { cwd: f.cwd, encoding: 'utf8', env: { ...process.env, ...env }, timeout: 20000 });
}
function cleanup(f) { rmSync(f.base, { recursive: true, force: true }); }

test('relocated sourced environment derives canonical checkout from another cwd with spaces', () => {
  const f = fixture();
  try {
    const result = bash(f, ['-c', 'source "$1" || exit $?; printf "%s\\n" "$REPO_ROOT" "$ROUND28_ROOT" "$TMPDIR" "$HOME" "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" "$GOMODCACHE" "$GOPROXY" "$GOSUMDB" "${http_proxy-unset}" "${HTTPS_PROXY-unset}"', '_', join(f.repo, 'scripts/verify-round28-env.sh')], { http_proxy: 'invalid', HTTPS_PROXY: 'invalid' });
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(result.stdout.trim().split('\n'), [f.repo, f.round, join(f.round,'tmp'), join(f.round,'home'), join(f.round,'home/config'), join(f.round,'home/data'), join(f.round,'cache/gomod'), 'off', 'off', 'unset', 'unset']);
  } finally { cleanup(f); }
});

test('late environment symlink refuses before the first temporary/cache mkdir', () => {
  const f = fixture();
  try {
    mkdirSync(join(f.round, 'home'), { recursive: true, mode: 0o700 });
    const target = join(f.base, 'must not exist');
    symlinkSync(target, join(f.round, 'home/config'));
    const result = bash(f, ['-c', 'source "$1"', '_', join(f.repo, 'scripts/verify-round28-env.sh')]);
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /symlink/);
    for (const path of [target, join(f.round,'tmp'), join(f.round,'cache')]) assert.equal(existsSync(path), false, path);
  } finally { cleanup(f); }
});

test('symlink verification root is refused before environment writes', () => {
  const f = fixture();
  try {
    const target = join(f.base, 'unwritten target');
    symlinkSync(target, join(f.repo, '.verification'));
    const result = bash(f, ['-c', 'source "$1"', '_', join(f.repo, 'scripts/verify-round28-env.sh')]);
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /symlink/);
    assert.equal(existsSync(target), false);
  } finally { cleanup(f); }
});

test('relocated executors help and browser preflight never start a hub or create an unsafe artifact', () => {
  const f = fixture();
  try {
    for (const name of names.filter(n => n !== 'verify-round28-env.sh')) {
      const result = bash(f, [join(f.repo,'scripts',name), '--help']);
      assert.equal(result.status, 0, `${name}: ${result.stderr}`);
      assert.match(result.stdout, /usage:/i);
    }
    const runner = join(f.repo, 'scripts/verify-phase1-browser.sh');
    const result = bash(f, [runner, '--stage', 'round28', '--preflight-only']);
    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /no hub started/);
    const target = join(f.base, 'unsafe artifact');
    const unsafe = bash(f, [runner, '--stage','round28','--preflight-only','--artifact-root',target]);
    assert.notEqual(unsafe.status, 0);
    assert.match(unsafe.stderr, /must be inside/);
    assert.equal(existsSync(target), false);
    assert.equal(existsSync(join(f.round,'owned-hub.pid')), false);
  } finally { cleanup(f); }
});

test('all six shell scripts are syntax-valid and contain no literal personal repository pin', () => {
  for (const name of names) {
    const path = join(REPO_ROOT,'scripts',name);
    const result = spawnSync('bash',['-n',path],{ encoding:'utf8' });
    assert.equal(result.status,0,result.stderr);
    assert.doesNotMatch(readFileSync(path,'utf8'), /\/home\/[a-z][a-z0-9_-]*\//i);
  }
});
