#!/usr/bin/env node
// check-i18n.mjs — hard-coded UI string lint (SPEC-v0.8 §3-3). Flags
// user-visible string literals in web/assets/js/** that bypass
// core/i18n.js's t(), and separately verifies the en/ko catalog files
// stay in parity (same keys, same {placeholder} sets) and that every
// t('literal.key') call site actually exists in en.json.
//
// Usage: node scripts/check-i18n.mjs
//   [--allow scripts/i18n-lint.allow.json] [--pending scripts/i18n-lint.pending.json]
//   [--root DIR] [--json]
//
// Exit codes: 0 = clean, 1 = violations found or a config error.
//
// No third-party dependencies (repo-wide convention).
import { readFileSync, existsSync } from "node:fs";
import { join, relative } from "node:path";
import { globSync } from "node:fs";
import {
  findHardcodedStrings,
  parseAllowList,
  parsePendingList,
  classifyFindings,
  evaluatePendingPolicy,
  buildFindingKey,
  relPath,
  flattenCatalog,
  diffCatalogKeys,
  diffPlaceholders,
  findTranslationKeyUsages,
} from "./check-i18n-lib.mjs";

function parseArgs(argv) {
  const opt = {
    allow: "scripts/i18n-lint.allow.json",
    pending: "scripts/i18n-lint.pending.json",
    root: ".",
    json: false,
  };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    const next = () => argv[++i];
    switch (a) {
      case "--allow": opt.allow = next(); break;
      case "--pending": opt.pending = next(); break;
      case "--root": opt.root = next(); break;
      case "--json": opt.json = true; break;
      case "-h":
      case "--help":
        printUsage();
        process.exit(0);
        break;
      default:
        process.stderr.write(`check-i18n.mjs: unknown argument "${a}"\n`);
        printUsage();
        process.exit(1);
    }
  }
  return opt;
}

function printUsage() {
  process.stderr.write(
    "usage: node scripts/check-i18n.mjs [--allow FILE] [--pending FILE] [--root DIR] [--json]\n",
  );
}

function readJSON(path, fallback) {
  if (!existsSync(path)) return fallback;
  return JSON.parse(readFileSync(path, "utf8"));
}

function main() {
  const opt = parseArgs(process.argv.slice(2));
  const root = opt.root;

  const allowList = parseAllowList(readJSON(join(root, opt.allow), []));
  const pendingFiles = parsePendingList(readJSON(join(root, opt.pending), []));

  const jsFiles = globSync("web/assets/js/**/*.js", { cwd: root }).sort();

  // -------------------------------------------------------------------
  // 1. Hard-coded string detection, with allow-list + pending-list
  //    enforcement.
  // -------------------------------------------------------------------
  const findingsByFile = new Map();
  const allAllowed = [];
  for (const rel of jsFiles) {
    const abs = join(root, rel);
    const source = readFileSync(abs, "utf8");
    const found = findHardcodedStrings(source, rel);
    const { violations, allowed } = classifyFindings(found, allowList);
    findingsByFile.set(rel, violations);
    allAllowed.push(...allowed);
  }

  const { nonPendingViolations, staleCleanPendingFiles } = evaluatePendingPolicy(findingsByFile, pendingFiles);

  // Every pending-list entry must refer to a file that actually still
  // exists under web/assets/js/** (catches a stale entry left behind
  // after a file rename/deletion).
  const jsFileSet = new Set(jsFiles.map(relPath));
  const unknownPendingFiles = pendingFiles.filter((f) => !jsFileSet.has(f));

  // -------------------------------------------------------------------
  // 2. Catalog parity: key set + placeholder set, en <-> ko.
  // -------------------------------------------------------------------
  const enPath = join(root, "web/assets/i18n/en.json");
  const koPath = join(root, "web/assets/i18n/ko.json");
  const enTree = readJSON(enPath, null);
  const koTree = readJSON(koPath, null);
  const catalogErrors = [];
  let missingKeyDiff = { onlyInA: [], onlyInB: [] };
  let placeholderMismatches = [];
  let enFlat = new Map();

  if (enTree === null) catalogErrors.push(`missing catalog: ${relative(root, enPath) || enPath}`);
  if (koTree === null) catalogErrors.push(`missing catalog: ${relative(root, koPath) || koPath}`);
  if (enTree !== null && koTree !== null) {
    enFlat = flattenCatalog(enTree);
    const koFlat = flattenCatalog(koTree);
    missingKeyDiff = diffCatalogKeys(enFlat, koFlat);
    placeholderMismatches = diffPlaceholders(enFlat, koFlat);
  }

  // -------------------------------------------------------------------
  // 3. Every literal t('key') call site must exist in en.json.
  // -------------------------------------------------------------------
  const missingTKeys = new Set();
  if (enTree !== null) {
    for (const rel of jsFiles) {
      const abs = join(root, rel);
      const source = readFileSync(abs, "utf8");
      for (const key of findTranslationKeyUsages(source)) {
        if (!enFlat.has(key)) missingTKeys.add(`${key} (used in ${rel})`);
      }
    }
  }

  // -------------------------------------------------------------------
  // Reporting
  // -------------------------------------------------------------------
  const ok =
    nonPendingViolations.length === 0 &&
    staleCleanPendingFiles.length === 0 &&
    unknownPendingFiles.length === 0 &&
    catalogErrors.length === 0 &&
    missingKeyDiff.onlyInA.length === 0 &&
    missingKeyDiff.onlyInB.length === 0 &&
    placeholderMismatches.length === 0 &&
    missingTKeys.size === 0;

  if (opt.json) {
    process.stdout.write(JSON.stringify({
      ok,
      violations: nonPendingViolations.map((f) => ({ ...f, key: buildFindingKey(f) })),
      allowed: allAllowed.map((f) => ({ ...f, key: buildFindingKey(f) })),
      staleCleanPendingFiles,
      unknownPendingFiles,
      catalogErrors,
      missingKeysInKo: missingKeyDiff.onlyInA,
      missingKeysInEn: missingKeyDiff.onlyInB,
      placeholderMismatches,
      missingTKeys: [...missingTKeys].sort(),
    }, null, 2) + "\n");
  } else {
    printReport({
      ok,
      nonPendingViolations,
      allAllowed,
      staleCleanPendingFiles,
      unknownPendingFiles,
      catalogErrors,
      missingKeyDiff,
      placeholderMismatches,
      missingTKeys,
    });
  }

  process.exit(ok ? 0 : 1);
}

function printReport({
  ok,
  nonPendingViolations,
  allAllowed,
  staleCleanPendingFiles,
  unknownPendingFiles,
  catalogErrors,
  missingKeyDiff,
  placeholderMismatches,
  missingTKeys,
}) {
  const lines = [];
  lines.push(`check-i18n: ${nonPendingViolations.length} violation(s), ${allAllowed.length} allow-listed finding(s)`);

  if (nonPendingViolations.length > 0) {
    lines.push("");
    lines.push("Hard-coded UI strings (not in web/assets/js/** an allow-listed or pending file):");
    for (const f of nonPendingViolations) {
      lines.push(`  ${buildFindingKey(f)}  ${f.snippet}`);
    }
  }
  if (staleCleanPendingFiles.length > 0) {
    lines.push("");
    lines.push("Pending files with ZERO findings (remove from scripts/i18n-lint.pending.json):");
    for (const f of staleCleanPendingFiles) lines.push(`  ${f}`);
  }
  if (unknownPendingFiles.length > 0) {
    lines.push("");
    lines.push("Pending-list entries that don't match any web/assets/js/** file (stale, remove):");
    for (const f of unknownPendingFiles) lines.push(`  ${f}`);
  }
  if (catalogErrors.length > 0) {
    lines.push("");
    lines.push("Catalog errors:");
    for (const e of catalogErrors) lines.push(`  ${e}`);
  }
  if (missingKeyDiff.onlyInA.length > 0) {
    lines.push("");
    lines.push("Keys present in en.json but missing from ko.json:");
    for (const k of missingKeyDiff.onlyInA) lines.push(`  ${k}`);
  }
  if (missingKeyDiff.onlyInB.length > 0) {
    lines.push("");
    lines.push("Keys present in ko.json but missing from en.json:");
    for (const k of missingKeyDiff.onlyInB) lines.push(`  ${k}`);
  }
  if (placeholderMismatches.length > 0) {
    lines.push("");
    lines.push("Keys whose {placeholder} set differs between en.json and ko.json:");
    for (const k of placeholderMismatches) lines.push(`  ${k}`);
  }
  if (missingTKeys.size > 0) {
    lines.push("");
    lines.push("t() call sites referencing a key missing from en.json:");
    for (const k of missingTKeys) lines.push(`  ${k}`);
  }

  lines.push("");
  lines.push(ok ? "check-i18n: OK" : "check-i18n: FAILED");
  process.stdout.write(lines.join("\n") + "\n");
}

main();
