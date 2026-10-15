#!/usr/bin/env node
// Check only worktree source documents: check-docs.py's default rglob also
// scans ignored .verification caches, generating vendor-only false positives.
import { spawnSync } from "node:child_process";
import { copyFileSync, lstatSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { REPO_ROOT, ROUND28_ROOT, safeVerificationPath, verificationEnvironment } from "./verify-round28-safety.mjs";

const env = verificationEnvironment();
const snapshot = safeVerificationPath(join(ROUND28_ROOT, "docs-source-snapshot"));
const listed = spawnSync("git", ["ls-files", "--cached", "--others", "--exclude-standard", "-z"], { cwd: REPO_ROOT, env, encoding: "utf8" });
if (listed.status !== 0) throw new Error(listed.stderr || "git source listing failed");
const files = [...new Set(listed.stdout.split("\0").filter(Boolean))];
rmSync(snapshot, { recursive: true, force: true });
mkdirSync(snapshot, { recursive: true, mode: 0o700 });
for (const file of files) {
  const source = join(REPO_ROOT, file);
  const target = safeVerificationPath(join(snapshot, file), "source snapshot file", snapshot);
  // Deleted worktree files are intentionally not present in this snapshot.
  let stat;
  try { stat = lstatSync(source); } catch (error) { if (error.code === "ENOENT") continue; throw error; }
  if (!stat.isFile()) throw new Error(`source snapshot refuses nonregular file: ${file}`);
  mkdirSync(dirname(target), { recursive: true, mode: 0o700 });
  copyFileSync(source, target);
}
writeFileSync(join(ROUND28_ROOT, "baseline/docs-source-files.json"), `${JSON.stringify(files, null, 2)}\n`, { mode: 0o600 });
const result = spawnSync("python3", [join(REPO_ROOT, "scripts/check-docs.py"), "--repo-root", snapshot, "--check-flags"], { cwd: REPO_ROOT, env, stdio: "inherit" });
process.exitCode = result.status ?? 2;
