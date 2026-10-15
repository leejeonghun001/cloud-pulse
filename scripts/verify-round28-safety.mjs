#!/usr/bin/env node
// Mechanical verification-only containment and prebuilt-binary checks.
import { createHash } from "node:crypto";
import { existsSync, lstatSync, mkdirSync, readFileSync, realpathSync } from "node:fs";
import { dirname, join, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

export const REPO_ROOT = realpathSync(resolve(dirname(fileURLToPath(import.meta.url)), ".."));
export const VERIFY_ROOT = join(REPO_ROOT, ".verification");
export const ROUND28_ROOT = join(VERIFY_ROOT, "round28");

export function safeVerificationPath(path, label = "verification path", root = VERIFY_ROOT) {
  const absolute = resolve(path);
  if (absolute !== root && !absolute.startsWith(`${root}${sep}`)) throw new Error(`${label} must be inside ${root}`);
  // Check BEFORE mkdir/read/write: lexical containment alone permits symlink
  // escapes, and checking realpath only after mkdir is already too late.
  let current = REPO_ROOT;
  for (const part of relative(REPO_ROOT, absolute).split(sep).filter(Boolean)) {
    current = join(current, part);
    if (existsSync(current)) {
      if (lstatSync(current).isSymbolicLink()) throw new Error(`${label} contains a symlink: ${current}`);
      if (realpathSync(current) !== current) throw new Error(`${label} escaped its canonical path`);
    } else {
      try {
        if (lstatSync(current).isSymbolicLink()) throw new Error(`${label} contains a dangling symlink: ${current}`);
      } catch (error) { if (error.code !== "ENOENT") throw error; }
    }
  }
  return absolute;
}

export function verificationEnvironment(root = ROUND28_ROOT, tmp = join(root, "tmp")) {
  safeVerificationPath(root);
  safeVerificationPath(tmp, "temporary directory", root);
  const paths = {
    TMPDIR: tmp, TMP: tmp, TEMP: tmp,
    GOCACHE: join(root, "cache/go-build"),
    GOMODCACHE: join(root, "cache/gomod"),
    PYTHONPYCACHEPREFIX: join(root, "cache/python"),
    npm_config_cache: join(root, "cache/npm"),
    XDG_CACHE_HOME: join(root, "cache/xdg"),
    XDG_CONFIG_HOME: join(root, "home/config"),
    XDG_DATA_HOME: join(root, "home/data"),
    XDG_RUNTIME_DIR: join(root, "runtime"),
    HOME: join(root, "home"),
  };
  // Validate the whole target set before the first mkdir, including cache
  // ancestors: a later symlink must not cause partial environment writes.
  for (const path of Object.values(paths)) safeVerificationPath(path, "subprocess directory", root);
  for (const path of Object.values(paths)) mkdirSync(path, { recursive: true, mode: 0o700 });
  const env = { ...process.env, ...paths, CGO_ENABLED: "0", GOPROXY: "off", GOSUMDB: "off", NO_PROXY: "127.0.0.1,localhost", no_proxy: "127.0.0.1,localhost" };
  for (const key of ["http_proxy", "https_proxy", "all_proxy", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"]) delete env[key];
  return env;
}

export function validatePrebuiltBinary(path, expectedSHA256, { purpose = "release" } = {}) {
  if (!/^[a-f0-9]{64}$/.test(expectedSHA256 || "")) throw new Error("binary sha256 must be 64 lowercase hex characters");
  if (!["release", "development-audit"].includes(purpose)) throw new Error("invalid binary provenance purpose");
  const firstImpressionRoot = join(ROUND28_ROOT, 'firstimpression/dev-qa');
  const binaryRoot = purpose === 'development-audit' && resolve(path).startsWith(`${firstImpressionRoot}${sep}`)
    ? firstImpressionRoot : join(ROUND28_ROOT, purpose === "release" ? "releases" : "audit-bin");
  const absolute = safeVerificationPath(path, `${purpose} prebuilt binary`, binaryRoot);
  const stat = lstatSync(absolute);
  if (!stat.isFile() || !(stat.mode & 0o111) || stat.nlink !== 1) throw new Error("prebuilt binary must be a regular executable with no hardlinks");
  const bytes = readFileSync(absolute);
  if (!bytes.subarray(0, 4).equals(Buffer.from([0x7f, 0x45, 0x4c, 0x46]))) throw new Error("prebuilt hub must be an ELF binary, not a script");
  const machines = { arm64: 183, x64: 62 };
  if (process.platform !== "linux" || !machines[process.arch] || bytes.length < 64 || bytes[4] !== 2 || bytes[5] !== 1 || bytes[6] !== 1 ||
      bytes.readUInt16LE(18) !== machines[process.arch] || ![2, 3].includes(bytes.readUInt16LE(16)) || bytes.readUInt32LE(20) !== 1 || bytes.readUInt16LE(52) !== 64) {
    throw new Error(`prebuilt hub must have a native Linux ${process.arch} ELF64 executable header`);
  }
  const actual = createHash("sha256").update(bytes).digest("hex");
  if (actual !== expectedSHA256) throw new Error(`prebuilt binary digest mismatch: ${actual}`);
  return absolute;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [option, path, hash, ...extra] = process.argv.slice(2);
    if (extra.length || !path || !["--path", "--binary", "--audit-binary"].includes(option)) throw new Error("usage: verify-round28-safety.mjs --path PATH | --binary PATH SHA256 | --audit-binary PATH SHA256");
    process.stdout.write(`${option === "--path" ? safeVerificationPath(path) : validatePrebuiltBinary(path, hash, { purpose: option === "--audit-binary" ? "development-audit" : "release" })}\n`);
  } catch (error) { process.stderr.write(`${error.message}\n`); process.exitCode = 2; }
}
