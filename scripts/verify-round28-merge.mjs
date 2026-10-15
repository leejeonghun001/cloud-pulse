#!/usr/bin/env node
// Sole integration owner: exact terminal deltas, no unrelated catalog edits.
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { flattenDelta, validateLocaleDeltas } from './verify-round28-firstimpression.mjs';
import { REPO_ROOT, ROUND28_ROOT, safeVerificationPath } from './verify-round28-safety.mjs';

const plan = JSON.parse(readFileSync(join(ROUND28_ROOT, 'locale-integration-plan.json'), 'utf8'));
const files = ['web/assets/i18n/en.json', 'web/assets/i18n/ko.json', ...plan.deltas.map(x => x.path)];
// JSON.parse alone would silently discard repeated object members.
const duplicateCheck = spawnSync('python3', ['-c', `import json,sys
def unique(pairs):
 d={}
 for k,v in pairs:
  if k in d: raise ValueError('duplicate JSON member: '+k)
  d[k]=v
 return d
for p in sys.argv[1:]:
 with open(p,encoding='utf-8') as f: json.load(f,object_pairs_hook=unique)
`, ...files], { cwd: REPO_ROOT, encoding: 'utf8' });
if (duplicateCheck.status !== 0) throw new Error(duplicateCheck.stderr);
const result = validateLocaleDeltas(plan.deltas.map(x => ({ ...x, delta: JSON.parse(readFileSync(join(REPO_ROOT, x.path), 'utf8')) })));
const catalogs = Object.fromEntries(['en', 'ko'].map(lang => [lang, JSON.parse(readFileSync(join(REPO_ROOT, `web/assets/i18n/${lang}.json`), 'utf8'))]));
const before = Object.fromEntries(['en', 'ko'].map(lang => [lang, flattenDelta(catalogs[lang])]));
const copyEdits = [];
for (const [key, text] of Object.entries(result.merged.ko)) {
  let value = text;
  if (key.startsWith('costs.') || key.startsWith('billing.')) value = value.replace(/\bhub\b/g, '허브');
  if (key === 'costs.page_subtitle') value = value.replace('제공자가 조회한', '클라우드 제공자가 보고한');
  if (value !== text) copyEdits.push({ key, before: text, after: value });
  result.merged.ko[key] = value;
}
const changes = [];
for (const lang of ['en', 'ko']) {
  for (const [key, value] of Object.entries(result.merged[lang])) {
    const parts = key.split('.'); let node = catalogs[lang];
    for (const part of parts.slice(0, -1)) {
      if (node[part] === undefined) node[part] = {};
      if (!node[part] || typeof node[part] !== 'object') throw new Error(`catalog shape conflict: ${key}`);
      node = node[part];
    }
    changes.push({ lang, key, existing: Object.hasOwn(before[lang], key), before: before[lang][key] ?? null, after: value });
    node[parts.at(-1)] = value;
  }
  const after = flattenDelta(catalogs[lang]);
  for (const [key, value] of Object.entries(before[lang])) {
    if (!Object.hasOwn(after, key)) throw new Error(`lost existing key: ${key}`);
    if (!Object.hasOwn(result.merged[lang], key) && value !== after[key]) throw new Error(`unowned change: ${key}`);
  }
}
if (JSON.stringify(Object.keys(flattenDelta(catalogs.en)).sort()) !== JSON.stringify(Object.keys(flattenDelta(catalogs.ko)).sort())) throw new Error('full catalog key parity mismatch');
if (!process.argv.includes('--apply')) throw new Error('explicit --apply required');
const out = safeVerificationPath(join(ROUND28_ROOT, 'firstimpression/integration'));
mkdirSync(out, { recursive: true, mode: 0o700 });
for (const lang of ['en', 'ko']) writeFileSync(join(REPO_ROOT, `web/assets/i18n/${lang}.json`), JSON.stringify(catalogs[lang], null, 2) + '\n');
writeFileSync(join(out, 'locale-merge.json'), JSON.stringify({ status: 'merged', counts: result.counts, totalKeys: result.totalKeys, copyEdits, changes, noExistingKeyLoss: true, duplicateMembers: false }, null, 2) + '\n', { mode: 0o600 });
console.log(JSON.stringify({ counts: result.counts, totalKeys: result.totalKeys, copyEdits, existingReplacements: changes.filter(x => x.existing).length }));
