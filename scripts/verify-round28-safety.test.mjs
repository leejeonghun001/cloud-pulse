import test from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { Script } from "node:vm";
import { REPO_ROOT, ROUND28_ROOT, safeVerificationPath, validatePrebuiltBinary, verificationEnvironment } from "./verify-round28-safety.mjs";
import { BidiSession } from "./verify-phase1-auth.mjs";
import { pathToHashRoute } from "../web/assets/js/core/router.js";

const audit = join(ROUND28_ROOT, "audit");
mkdirSync(audit, { recursive: true, mode: 0o700 });

test("paths reject worktree source and lexical traversal before creating anything", () => {
  assert.equal(safeVerificationPath(join(audit, "future/file")), join(audit, "future/file"));
  assert.throws(() => safeVerificationPath(join(REPO_ROOT, "web/future")), /must be inside/);
  assert.throws(() => safeVerificationPath(join(audit, "../../../web/future")), /must be inside/);
});

test("existing and dangling symlink ancestors are rejected before mkdir/read/write", () => {
  const fixture = mkdtempSync(join(audit, "safety-paths-"));
  try {
    mkdirSync(join(fixture, "target"));
    symlinkSync(join(fixture, "target"), join(fixture, "linked"));
    symlinkSync(join(fixture, "missing"), join(fixture, "dangling"));
    assert.throws(() => safeVerificationPath(join(fixture, "linked/new/path")), /symlink/);
    assert.throws(() => safeVerificationPath(join(fixture, "dangling/new/path")), /symlink/);
    assert.equal(existsSync(join(fixture, "target/new")), false);
    assert.equal(existsSync(join(fixture, "missing")), false);
  } finally { rmSync(fixture, { recursive: true, force: true }); }
});

test("every generated subprocess scratch/cache/home remains in Round28", () => {
  const env = verificationEnvironment();
  for (const key of ["TMPDIR", "TMP", "TEMP", "GOCACHE", "GOMODCACHE", "PYTHONPYCACHEPREFIX", "npm_config_cache", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR", "HOME"]) {
    assert.ok(env[key].startsWith(`${ROUND28_ROOT}/`), key);
  }
  assert.equal(env.GOPROXY, "off");
});

test("prebuilt provenance rejects malformed digests and binaries outside release root", () => {
  assert.throws(() => validatePrebuiltBinary(join(ROUND28_ROOT, "not-release"), "wrong"), /sha256/);
  assert.throws(() => validatePrebuiltBinary(join(REPO_ROOT, "bin/cloud-pulse-hub"), "0".repeat(64)), /must be inside/);
  assert.throws(() => validatePrebuiltBinary(join(ROUND28_ROOT, "audit-bin/cloud-pulse-hub"), "0".repeat(64)), /must be inside/, "audit binary must not masquerade as a release candidate");
  assert.throws(() => validatePrebuiltBinary(join(ROUND28_ROOT, "releases/cloud-pulse-hub"), "0".repeat(64), { purpose: "development-audit" }), /must be inside/);
});

test("synthetic ELF digest checks never execute, copy or build a release candidate", () => {
  const releases = join(ROUND28_ROOT, "releases");
  mkdirSync(releases, { recursive: true, mode: 0o700 });
  const fixture = mkdtempSync(join(releases, "synthetic-validation-only-"));
  const binary = join(fixture, "not-a-real-binary");
  const bytes = Buffer.alloc(64);
  Buffer.from([0x7f, 0x45, 0x4c, 0x46, 2, 1, 1]).copy(bytes);
  bytes.writeUInt16LE(2, 16);
  bytes.writeUInt16LE(process.arch === "arm64" ? 183 : 62, 18);
  bytes.writeUInt32LE(1, 20);
  bytes.writeUInt16LE(64, 52);
  try {
    writeFileSync(binary, bytes, { mode: 0o700 });
    const hash = createHash("sha256").update(bytes).digest("hex");
    assert.equal(validatePrebuiltBinary(binary, hash), binary);
    assert.throws(() => validatePrebuiltBinary(binary, "0".repeat(64)), /mismatch/);
    const wrongArch = Buffer.from(bytes);
    wrongArch.writeUInt16LE(process.arch === "arm64" ? 62 : 183, 18);
    writeFileSync(binary, wrongArch);
    assert.throws(() => validatePrebuiltBinary(binary, createHash("sha256").update(wrongArch).digest("hex")), /native Linux/);
    writeFileSync(binary, bytes.subarray(0, 8));
    assert.throws(() => validatePrebuiltBinary(binary, hash), /ELF64/);
    writeFileSync(binary, "#!/bin/sh\n");
    assert.throws(() => validatePrebuiltBinary(binary, hash), /ELF/);
  } finally { rmSync(fixture, { recursive: true, force: true }); }
});

test("asset 4xx/fetch failures are recorded; intentional API 401/404 is not a missing asset", () => {
  const session = new BidiSession();
  session.onMessage({ method: "network.responseCompleted", params: { request: { url: "http://127.0.0.1:18190/api/v1/auth/me" }, response: { status: 401 } } });
  session.onMessage({ method: "network.responseCompleted", params: { request: { url: "http://127.0.0.1:18190/assets/missing.js" }, response: { status: 404 } } });
  session.onMessage({ method: "network.fetchError", params: { request: { url: "http://127.0.0.1:18190/assets/failed.css" }, errorText: "failed" } });
  assert.equal(session.networkFailures.length, 2);
});

test("JSON browser evaluation awaits async metadata before serialization", async () => {
  const session = new BidiSession();
  session.evaluate = async (expression) => await new Script(expression).runInNewContext();
  assert.deepEqual(await session.evaluateJSON('(async () => ({status:200,body:{version:"v0.8.0-round28-dirty",commit:"3370161",date:"2026-10-06T00:00:00Z"}}))()'),
    {status:200,body:{version:"v0.8.0-round28-dirty",commit:"3370161",date:"2026-10-06T00:00:00Z"}});
  assert.deepEqual(await session.evaluateJSON('({status:200})'), {status:200});
});

test("login deep-link normalization matches the existing router, not a false pathname assertion", () => {
  assert.equal(pathToHashRoute("/login", "", ""), "/#/login");
  assert.equal(pathToHashRoute("/login", "?cp_verify=ko-dark-768", ""), "/#/login?cp_verify=ko-dark-768");
  const source = readFileSync(join(REPO_ROOT, "scripts/verify-phase1-auth.mjs"), "utf8");
  assert.match(source, /for \(const lang of \["en", "ko"\]\)/);
  assert.match(source, /\[\[360, 800\], \[768, 1024\], \[1440, 900\]\]/);
  assert.match(source, /for \(const theme of \["dark", "light"\]\)/);
  assert.doesNotMatch(source, /location\.reload\(/);
  assert.match(source, /state\.hash\.split\("\?"\)\[0\] === "#\/login"/);
  assert.doesNotMatch(source, /state\.hash === `#\/login\?cp_verify=/, "disposable navigation probe must not become a product route contract");
});

test("embedded expressions parse without starting Firefox or a hub", () => {
  for (const file of ["verify-phase1-auth.mjs", "verify-overview-contract.mjs", "verify-detail-contract.mjs", "verify-activity-contract.mjs"]) {
    const source = readFileSync(join(REPO_ROOT, "scripts", file), "utf8");
    // Literal extraction expressions (no template interpolation) compile here.
    for (const match of source.matchAll(/(?:(?:const EXTRACT_GRID|const EXTRACT_DETAIL) =|session\.(?:evaluate|evaluateJSON)\()\s*`([^`]+)`/g)) {
      if (match[1].includes("${")) continue;
      assert.doesNotThrow(() => new Script(match[1]));
    }
  }
});
