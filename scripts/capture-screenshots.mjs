#!/usr/bin/env node
// capture-screenshots.mjs — reproducible documentation screenshot tool
// (SPEC-v0.8 §2 tree cleanup). Drives headless Firefox via WebDriver
// BiDi, the same zero-dependency approach as scripts/ui-audit.mjs (that
// file's BidiSession class is not exported, since it also runs its own
// main() on import, so this tool ports the same small BiDi wrapper
// rather than importing it — see the class below, intentionally kept
// minimal and limited to session/navigate/eval/screenshot). Pure
// contrast/report helpers are NOT needed here, so ui-audit-lib.mjs is
// not imported.
//
// Usage:
//   node scripts/capture-screenshots.mjs --hub http://127.0.0.1:18099 \
//     --password-file /tmp/pw.txt --out docs/screenshots \
//     [--shots scripts/capture-screenshots.json] \
//     [--markers-file /tmp/markers.json] [--timeout 120000] [--wait 1200]
//
// Before EVERY capture, this tool walks the live DOM (text nodes +
// title/aria-label/placeholder/alt attributes) and replaces any
// occurrence of a configured "local marker" (this machine's real
// hostname/IPv4/IPv6, supplied only via --markers-file or
// CP_PREPUBLISH_MARKERS — never hardcoded here) with a fixed demo
// value, so no screenshot can ever leak this machine's identity even
// if a route's data wasn't fully seeded with demo-only values.
//
// Exit codes: 0 = every shot captured, 1 = usage/config error,
// 3 = tool-level failure (browser never came up, hard timeout).
import { spawn, spawnSync } from "node:child_process";
import {
  mkdtempSync,
  rmSync,
  mkdirSync,
  writeFileSync,
  readFileSync,
  existsSync,
  statSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { createServer } from "node:net";
import { assertProtectedIdentifiersPreserved } from "./capture-screenshots-lib.mjs";

const DEFAULT_VIEWPORT = { width: 1440, height: 900, label: "1440x900" };
const DEFAULT_WAIT_MS = 1200;
const DEFAULT_TIMEOUT_MS = 180000;
const DEMO_HOSTNAME = "demo-hub";
const DEMO_IPV4_POOL = ["192.0.2.10", "192.0.2.11", "198.51.100.20", "198.51.100.21"];
const DEMO_IPV6 = "2001:db8::1";

// ---------------------------------------------------------------------------
// CLI parsing
// ---------------------------------------------------------------------------

function parseArgs(argv) {
  const opt = {
    hub: null,
    user: "admin",
    passwordFile: null,
    out: null,
    shotsFile: join(dirname(new URL(import.meta.url).pathname), "capture-screenshots.json"),
    markersFile: null,
    timeout: DEFAULT_TIMEOUT_MS,
    wait: DEFAULT_WAIT_MS,
  };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    const next = () => argv[++i];
    switch (a) {
      case "--hub": opt.hub = next(); break;
      case "--user": opt.user = next(); break;
      case "--password-file": opt.passwordFile = next(); break;
      case "--out": opt.out = next(); break;
      case "--shots": opt.shotsFile = next(); break;
      case "--markers-file": opt.markersFile = next(); break;
      case "--timeout": opt.timeout = Number(next()); break;
      case "--wait": opt.wait = Number(next()); break;
      case "-h":
      case "--help":
        printUsage();
        process.exit(0);
        break;
      default:
        process.stderr.write(`capture-screenshots.mjs: unknown argument "${a}"\n`);
        printUsage();
        process.exit(1);
    }
  }
  if (!opt.hub) fail("--hub URL is required");
  if (!opt.passwordFile) fail("--password-file FILE is required (never pass the password as an argument)");
  if (!opt.out) fail("--out DIR is required");
  return opt;
}

function printUsage() {
  process.stderr.write(
    "usage: node scripts/capture-screenshots.mjs --hub URL --password-file FILE --out DIR\n" +
      "  [--user admin] [--shots FILE] [--markers-file FILE] [--timeout ms] [--wait ms]\n" +
      "\n" +
      "The password is NEVER accepted on argv — only via --password-file.\n" +
      "--markers-file points at a JSON file ({\"hostname\":\"...\",\"ipv4\":[...],\"ipv6\":[...]})\n" +
      "containing THIS MACHINE's real hostname/IPs, obtained at runtime (e.g. via\n" +
      "`hostname` and `ip -brief addr`) — never checked into the repo. Every DOM\n" +
      "occurrence of a listed value is replaced with a fixed demo value before each\n" +
      "capture. CP_PREPUBLISH_MARKERS (a path, same JSON shape) is used if\n" +
      "--markers-file is not given.\n",
  );
}

function fail(msg) {
  process.stderr.write(`capture-screenshots.mjs: ${msg}\n`);
  process.exit(1);
}

function readPasswordFile(path) {
  if (!existsSync(path)) fail(`--password-file "${path}" does not exist`);
  const raw = readFileSync(path, "utf8");
  const pw = raw.replace(/\r?\n$/, "");
  if (pw.length === 0) fail(`--password-file "${path}" is empty`);
  return pw;
}

/**
 * loadMarkers reads the local-only marker file (never committed — see
 * usage text). Returns { hostname, ipv4: [...], ipv6: [...] }, all
 * optional/empty if no marker source is configured, so this tool still
 * runs (with no substitution) against a hub that has no local markers
 * to scrub, e.g. in CI.
 */
function loadMarkers(path) {
  const empty = { hostname: null, ipv4: [], ipv6: [] };
  if (!path) return empty;
  if (!existsSync(path)) fail(`--markers-file "${path}" does not exist`);
  const st = statSync(path);
  if ((st.mode & 0o077) !== 0) {
    process.stderr.write(`capture-screenshots.mjs: warning: "${path}" is readable by group/other; tighten to 0600\n`);
  }
  const raw = JSON.parse(readFileSync(path, "utf8"));
  return {
    hostname: raw.hostname || null,
    ipv4: Array.isArray(raw.ipv4) ? raw.ipv4.filter(Boolean) : [],
    ipv6: Array.isArray(raw.ipv6) ? raw.ipv6.filter(Boolean) : [],
  };
}

/**
 * buildSubstitutions maps each real marker value to a fixed demo value.
 * hostname is a "word" marker (word-boundary matched — see
 * capture-screenshots-lib.mjs; a hostname that happens to be a prefix of
 * an unrelated identifier like a GitHub owner name must NOT match).
 * ipv4 markers are matched as whole dotted-quad addresses only (never as
 * a prefix of a longer address). ipv6 addresses use ':' as a natural
 * delimiter not found in any protected identifier here, so they're
 * treated as "word" markers too (word-boundary chars [A-Za-z0-9_-] don't
 * include ':', so any adjacent ':' or non-hex character already forms a
 * boundary).
 */
function buildSubstitutions(markers) {
  const subs = [];
  if (markers.hostname) subs.push({ real: markers.hostname, demo: DEMO_HOSTNAME, kind: "word" });
  markers.ipv4.forEach((ip, i) =>
    subs.push({ real: ip, demo: DEMO_IPV4_POOL[i % DEMO_IPV4_POOL.length], kind: "ipv4" }),
  );
  markers.ipv6.forEach((ip) => subs.push({ real: ip, demo: DEMO_IPV6, kind: "word" }));
  return subs;
}

/**
 * discoverProtectedIdentifiers reads the project's own identity markers
 * so substitution can never mangle them: the Go module path (from
 * go.mod) and the owner/repo slug (from `git remote get-url origin`).
 * Both the full "owner/repo" slug and the bare owner name are protected,
 * since the reported defect is specifically the owner name being a
 * superstring of a colliding local hostname marker. Best-effort: a
 * failure to read either source is non-fatal (returns whatever was
 * found), since capture-screenshots.mjs must still work in a checkout
 * without a configured git remote (e.g. a fresh clone via tarball).
 * @param {string} repoRoot
 * @returns {string[]}
 */
function discoverProtectedIdentifiers(repoRoot) {
  const identifiers = new Set();

  try {
    const goModPath = join(repoRoot, "go.mod");
    if (existsSync(goModPath)) {
      const goMod = readFileSync(goModPath, "utf8");
      const m = goMod.match(/^module\s+(\S+)/m);
      if (m) {
        identifiers.add(m[1]); // e.g. "github.com/leejeonghun001/cloud-pulse"
        const parts = m[1].split("/");
        if (parts.length >= 3) {
          identifiers.add(parts.slice(1).join("/")); // "leejeonghun001/cloud-pulse"
          identifiers.add(parts[1]); // "leejeonghun001"
        }
      }
    }
  } catch {
    // best effort
  }

  try {
    const r = spawnSync("git", ["remote", "get-url", "origin"], { cwd: repoRoot, encoding: "utf8" });
    if (r.status === 0 && r.stdout) {
      const url = r.stdout.trim();
      // Matches both "git@github.com:owner/repo.git" and
      // "https://github.com/owner/repo.git" forms.
      const m = url.match(/github\.com[:/]([^/]+)\/([^/]+?)(?:\.git)?$/);
      if (m) {
        identifiers.add(`${m[1]}/${m[2]}`);
        identifiers.add(m[1]);
      }
    }
  } catch {
    // best effort
  }

  return Array.from(identifiers).filter(Boolean);
}

function loadShots(path) {
  if (!existsSync(path)) fail(`--shots file "${path}" does not exist`);
  const raw = JSON.parse(readFileSync(path, "utf8"));
  if (!Array.isArray(raw)) fail(`--shots file "${path}" must contain a JSON array`);
  return raw;
}

// ---------------------------------------------------------------------------
// PNG metadata stripping
// ---------------------------------------------------------------------------

const PNG_SIG = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
const KEEP_CHUNKS = new Set(["IHDR", "PLTE", "IDAT", "IEND", "tRNS"]);

/**
 * stripPngMetadata rewrites a PNG buffer keeping only pixel-data-
 * relevant chunks (IHDR/PLTE/tRNS/IDAT/IEND), dropping any ancillary
 * chunk that could carry metadata (tEXt/iTXt/zTXt/eXIf/tIME/etc).
 * Firefox's own captureScreenshot output is verified separately to
 * already be metadata-free (see verifyMinimalPng below); this function
 * is the actual enforcement so the tool never depends on that holding
 * true forever.
 * @param {Buffer} buf
 * @returns {{out: Buffer, droppedChunks: string[]}}
 */
function stripPngMetadata(buf) {
  if (buf.length < 8 || !buf.subarray(0, 8).equals(PNG_SIG)) {
    throw new Error("not a PNG (bad signature)");
  }
  const out = [PNG_SIG];
  const dropped = [];
  let off = 8;
  while (off + 8 <= buf.length) {
    const len = buf.readUInt32BE(off);
    const type = buf.subarray(off + 4, off + 8).toString("ascii");
    const chunkEnd = off + 8 + len + 4; // length + type + data + crc
    if (chunkEnd > buf.length) break;
    const wholeChunk = buf.subarray(off, chunkEnd);
    if (KEEP_CHUNKS.has(type)) {
      out.push(wholeChunk);
    } else {
      dropped.push(type);
    }
    off = chunkEnd;
    if (type === "IEND") break;
  }
  return { out: Buffer.concat(out), droppedChunks: dropped };
}

/** listPngChunks returns the ordered chunk-type list of a PNG buffer, for verification/logging. */
function listPngChunks(buf) {
  const types = [];
  let off = 8;
  while (off + 8 <= buf.length) {
    const len = buf.readUInt32BE(off);
    const type = buf.subarray(off + 4, off + 8).toString("ascii");
    types.push(type);
    off += 8 + len + 4;
    if (type === "IEND") break;
  }
  return types;
}

// ---------------------------------------------------------------------------
// BROWSER_START_TIMEOUT_MS bounds how long to wait for Firefox's WebDriver
// BiDi endpoint. Cold starts on CI runners (fresh profile) can exceed 20 s;
// override with CP_UI_AUDIT_BROWSER_START_MS.
const BROWSER_START_TIMEOUT_MS = Number(process.env.CP_UI_AUDIT_BROWSER_START_MS) || 90_000;

// BidiSession — minimal Firefox WebDriver BiDi driver.
// Ported from scripts/ui-audit.mjs's class of the same name (that file
// does not export it and runs main() at import time, so it can't be
// imported directly); trimmed to only what this tool needs
// (start/stop/navigate/evaluate/setViewport/screenshot).
// ---------------------------------------------------------------------------

const freePort = () =>
  new Promise((res, rej) => {
    const s = createServer();
    s.once("error", rej);
    s.listen(0, "127.0.0.1", () => {
      const { port } = s.address();
      s.close(() => res(port));
    });
  });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

class BidiSession {
  constructor() {
    this.ff = null;
    this.ws = null;
    this.profile = null;
    this.context = null;
    this.reqId = 0;
    this.pending = new Map();
  }

  // start opens the BiDi session, retrying once with a fresh profile: a
  // cold Firefox start on a CI runner occasionally misses the deadline.
  async start(attempts = 2) {
    let lastErr = null;
    for (let attempt = 1; attempt <= attempts; attempt++) {
      try {
        await this._startOnce();
        return;
      } catch (err) {
        lastErr = err;
        try { this.ff?.kill("SIGKILL"); } catch { /* already exited */ }
        process.stderr.write(`firefox start attempt ${attempt}/${attempts} failed: ${String(err.message).split("\n")[0]}\n`);
      }
    }
    throw lastErr;
  }

  async _startOnce() {
    this.profile = mkdtempSync(join(tmpdir(), "cp-shots-"));
    const port = await freePort();
    this.ff = spawn(
      "firefox",
      ["--headless", "--no-remote", "--profile", this.profile, `--remote-debugging-port=${port}`],
      { stdio: ["ignore", "ignore", "pipe"] },
    );
    let stderr = "";
    this.ff.stderr.on("data", (d) => {
      stderr += d.toString();
    });
    let exited = false;
    this.ff.on("exit", (code) => {
      exited = true;
      if (this._onExit) this._onExit(code, stderr);
    });

    let ws = null;
    const startDeadline = Date.now() + BROWSER_START_TIMEOUT_MS;
    while (!ws && !exited && Date.now() < startDeadline) {
      if (/WebDriver BiDi listening/.test(stderr)) {
        ws = new WebSocket(`ws://127.0.0.1:${port}/session`);
        await new Promise((res, rej) => {
          ws.onopen = res;
          ws.onerror = () => rej(new Error("ws connect failed"));
        });
      } else {
        await sleep(100);
      }
    }
    if (!ws) throw new Error(`firefox BiDi endpoint did not come up: ${stderr.slice(-500)}`);
    this.ws = ws;
    ws.onmessage = (ev) => this._onMessage(JSON.parse(ev.data));

    await this.send("session.new", { capabilities: {} });
    const created = await this.send("browsingContext.create", { type: "tab" });
    this.context = created.context;
  }

  _onMessage(m) {
    if (m.id && this.pending.has(m.id)) {
      const { res, rej } = this.pending.get(m.id);
      this.pending.delete(m.id);
      if (m.type === "error") rej(new Error(`${m.error}: ${m.message}`));
      else res(m.result);
    }
  }

  send(method, params = {}) {
    return new Promise((res, rej) => {
      const id = ++this.reqId;
      this.pending.set(id, { res, rej });
      this.ws.send(JSON.stringify({ id, method, params }));
    });
  }

  async setViewport(width, height) {
    await this.send("browsingContext.setViewport", { context: this.context, viewport: { width, height } });
  }

  async navigate(url) {
    await this.send("browsingContext.navigate", { context: this.context, url, wait: "complete" });
  }

  async evaluate(expression) {
    const r = await this.send("script.evaluate", {
      expression,
      target: { context: this.context },
      awaitPromise: true,
      resultOwnership: "none",
      serializationOptions: { maxObjectDepth: 6 },
    });
    if (r.type === "exception") {
      throw new Error(r.exceptionDetails?.text || "script.evaluate exception");
    }
    return deserialize(r.result);
  }

  async screenshotBuffer() {
    const shot = await this.send("browsingContext.captureScreenshot", { context: this.context, origin: "viewport" });
    return Buffer.from(shot.data, "base64");
  }

  async stop() {
    try {
      this.ws?.close();
    } catch {
      // already closed
    }
    try {
      this.ff?.kill("SIGKILL");
    } catch {
      // already dead
    }
    await sleep(200);
    try {
      rmSync(this.profile, { recursive: true, force: true });
    } catch {
      // best effort
    }
  }
}

function deserialize(v) {
  if (!v) return null;
  switch (v.type) {
    case "string":
    case "number":
    case "boolean":
      return v.value;
    case "null":
    case "undefined":
      return null;
    case "array":
      return (v.value || []).map(deserialize);
    case "object":
      return Object.fromEntries((v.value || []).map(([k, x]) => [typeof k === "string" ? k : deserialize(k), deserialize(x)]));
    default:
      return `[${v.type}]`;
  }
}

// ---------------------------------------------------------------------------
// Login + marker substitution + shot actions (in-page eval scripts)
// ---------------------------------------------------------------------------

/** loginViaForm mirrors ui-audit.mjs's helper of the same name. */
async function loginViaForm(session, hubURL, user, password, waitMs) {
  await session.navigate(`${hubURL}/#/login`);
  await sleep(waitMs);
  const escapedUser = JSON.stringify(user);
  const escapedPass = JSON.stringify(password);
  await session.evaluate(`
    (() => {
      const u = document.getElementById("cp-login-username");
      const p = document.getElementById("cp-login-password");
      if (!u || !p) throw new Error("login form fields not found");
      u.value = ${escapedUser};
      p.value = ${escapedPass};
      u.dispatchEvent(new Event("input", { bubbles: true }));
      p.dispatchEvent(new Event("input", { bubbles: true }));
      const form = u.closest("form");
      form.requestSubmit ? form.requestSubmit() : form.dispatchEvent(new Event("submit", { cancelable: true, bubbles: true }));
      return true;
    })()
  `);
  const deadline = Date.now() + 15000;
  let authed = false;
  while (Date.now() < deadline) {
    await sleep(300);
    authed = await session.evaluate(
      `Boolean(localStorage.getItem("cp_session") || sessionStorage.getItem("cp_session"))`,
    );
    if (authed) break;
  }
  if (!authed) throw new Error("login did not appear to establish a session within 15s");
}

async function setTheme(session, theme, waitMs) {
  await session.evaluate(`localStorage.setItem("cp_theme", ${JSON.stringify(theme)})`);
  await session.evaluate(`location.reload()`);
  await sleep(waitMs);
}

/**
 * resolveFirstHostRoute discovers a real "#/host/<id>" route from the
 * overview page's rendered host links, mirroring ui-audit.mjs's helper
 * of the same name — host IDs are hub-assigned strings (e.g.
 * "demo-aws-web-01"), never a fixed/predictable value, so any shot
 * targeting a host detail page must discover the id at runtime rather
 * than hardcoding one in scripts/capture-screenshots.json. An optional
 * hostIdHint picks the first link whose id CONTAINS the hint substring
 * (e.g. "web" to prefer a host known to have Docker/ports inventory
 * seeded) rather than always taking the first row.
 * @param {BidiSession} session
 * @param {string} hubURL
 * @param {number} waitMs
 * @param {string|null} hostIdHint
 * @returns {Promise<string|null>} e.g. "#/host/demo-aws-web-01", or null if no host exists yet.
 */
async function resolveFirstHostRoute(session, hubURL, waitMs, hostIdHint) {
  await session.navigate(`${hubURL}/#/`);
  await sleep(waitMs);
  try {
    const hostID = await session.evaluate(`
      (() => {
        const links = Array.from(document.querySelectorAll('a[href^="#/host/"]'));
        const ids = links.map((l) => {
          const m = l.getAttribute("href").match(/^#\\/host\\/([^/?]+)/);
          return m ? m[1] : null;
        }).filter(Boolean);
        const hint = ${JSON.stringify(hostIdHint || "")};
        if (hint) {
          const preferred = ids.find((id) => id.includes(hint));
          if (preferred) return preferred;
        }
        return ids[0] || null;
      })()
    `);
    return hostID ? `#/host/${hostID}` : null;
  } catch {
    return null;
  }
}

/**
 * loadInlinableLibSource reads scripts/capture-screenshots-lib.mjs's own
 * source and strips ES module `export` keywords, producing a plain
 * script body that can be prefixed onto an in-page eval expression so
 * every helper AND private helper/constant it depends on (e.g.
 * isWordBoundaryMatch, isIpv4BoundaryMatch, isWithinAny,
 * PROTECTED_URL_PATTERN, TOKEN_CHAR) is available with no manual
 * per-function enumeration — the browser-side copy is therefore always
 * byte-for-byte the same logic as the Node-side, unit-tested module
 * (see web/test/capture-screenshots.test.mjs), never a hand-duplicated
 * subset that could drift or miss a private dependency.
 *
 * Only a mechanical `export ` prefix removal is performed (no other
 * transformation), since `export function foo(...)`/`export const foo =`
 * are both valid plain statements once the `export ` keyword is
 * dropped — everything else in the file is already plain, portable
 * ES2020+ syntax with no import statements (verified by this file's own
 * header comment: "zero DOM/browser/child_process dependencies").
 * @returns {string}
 */
function loadInlinableLibSource() {
  const libPath = join(dirname(new URL(import.meta.url).pathname), "capture-screenshots-lib.mjs");
  const src = readFileSync(libPath, "utf8");
  return src.replace(/^export\s+/gm, "");
}

/**
 * scrubMarkers walks every text node and every title/aria-label/
 * placeholder/alt attribute in the live document, replacing any
 * occurrence of a configured real-world marker with its demo
 * replacement, using the SAME word-bounded/whole-IPv4-address matching
 * logic as scripts/capture-screenshots-lib.mjs (that module's entire
 * source, minus `export` keywords, is inlined into the in-page eval
 * below via loadInlinableLibSource() — see its docstring for why: this
 * guarantees there is exactly one implementation of the matching rules,
 * including every private helper it relies on, never a hand-duplicated
 * copy that could drift out of sync with the unit-tested module). Runs
 * entirely inside the page (a single evaluate call) so there is no gap
 * between scrubbing and the screenshot below it.
 *
 * Before substitution, captures a snapshot of document.body.innerHTML;
 * after substitution, asserts every protected identifier present in the
 * "before" snapshot is still present verbatim in the "after" snapshot —
 * failing the whole run (not just this shot) if not, per the
 * requirement that a corrupted protected identifier must hard-fail
 * capture rather than silently ship a bad screenshot.
 * @param {BidiSession} session
 * @param {Array<{real: string, demo: string, kind?: "word"|"ipv4"}>} substitutions
 * @param {string[]} protectedIdentifiers
 */
async function scrubMarkers(session, substitutions, protectedIdentifiers) {
  if (substitutions.length === 0) return 0;
  const subsJSON = JSON.stringify(substitutions);
  const protectedJSON = JSON.stringify(protectedIdentifiers || []);
  const { count, before, after } = await session.evaluate(`
    (() => {
      ${loadInlinableLibSource()}

      const subs = ${subsJSON};
      const protectedIdentifiers = ${protectedJSON};

      const before = document.body.innerHTML;
      let replaced = 0;
      function apply(s) {
        let out = s;
        for (const sub of subs) {
          const fn = sub.kind === "ipv4" ? applyIpv4Substitution : applyWordBoundedSubstitution;
          const r = fn(out, sub.real, sub.demo, protectedIdentifiers);
          out = r.text;
          replaced += r.replacedCount;
        }
        return out;
      }
      const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
      let node;
      while ((node = walker.nextNode())) {
        if (!node.nodeValue) continue;
        const next = apply(node.nodeValue);
        if (next !== node.nodeValue) node.nodeValue = next;
      }
      const attrs = ["title", "aria-label", "placeholder", "alt", "value"];
      document.querySelectorAll("*").forEach((el) => {
        for (const a of attrs) {
          if (!el.hasAttribute(a)) continue;
          const v = el.getAttribute(a);
          const next = apply(v);
          if (next !== v) el.setAttribute(a, next);
        }
        if ("value" in el && typeof el.value === "string" && el.value) {
          const next = apply(el.value);
          if (next !== el.value) el.value = next;
        }
      });
      const after = document.body.innerHTML;
      return { count: replaced, before, after };
    })()
  `);
  assertProtectedIdentifiersPreserved(before, after, protectedIdentifiers);
  return count;
}

/** runShotActions executes the optional list of --eval-style DOM actions before capture (e.g. opening a dialog). */
async function runShotActions(session, actions, waitMs) {
  for (const action of actions || []) {
    if (action.type === "click") {
      await session.evaluate(`
        (() => {
          const el = document.querySelector(${JSON.stringify(action.selector)});
          if (!el) throw new Error("selector not found: " + ${JSON.stringify(action.selector)});
          el.click();
          return true;
        })()
      `);
    } else if (action.type === "eval") {
      await session.evaluate(action.expression);
    } else if (action.type === "wait") {
      await sleep(action.ms || waitMs);
      continue;
    } else {
      throw new Error(`unknown shot action type "${action.type}"`);
    }
    await sleep(action.waitAfterMs ?? waitMs);
  }
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

async function main() {
  const opt = parseArgs(process.argv.slice(2));
  const password = readPasswordFile(opt.passwordFile);
  const markersPath = opt.markersFile || process.env.CP_PREPUBLISH_MARKERS || null;
  const markers = loadMarkers(markersPath);
  const substitutions = buildSubstitutions(markers);
  const repoRoot = join(dirname(new URL(import.meta.url).pathname), "..");
  const protectedIdentifiers = discoverProtectedIdentifiers(repoRoot);
  const shots = loadShots(opt.shotsFile);

  mkdirSync(opt.out, { recursive: true });

  const session = new BidiSession();
  let hardTimedOut = false;
  const timer = setTimeout(() => {
    hardTimedOut = true;
    process.stderr.write(`capture-screenshots.mjs: hard timeout (${opt.timeout}ms) reached; aborting\n`);
    session.stop().finally(() => process.exit(3));
  }, opt.timeout);

  const results = [];
  try {
    await session.start();

    // The #/login shot (if present) must be captured BEFORE this tool
    // establishes its own session, since a real authenticated visit to
    // "#/login" may immediately redirect away (see core/router.js's
    // auth guard). Any other shot runs post-login, in file order.
    const loginShots = shots.filter((s) => s.route === "#/login");
    const otherShots = shots.filter((s) => s.route !== "#/login");

    let lastTheme = null;
    const captureOne = async (shot) => {
      if (!shot.name || (!shot.route && !shot.resolveHostRoute)) fail(`shot entry missing "name" and "route"/"resolveHostRoute": ${JSON.stringify(shot)}`);
      const viewport = shot.viewport
        ? { width: shot.viewport.width, height: shot.viewport.height }
        : DEFAULT_VIEWPORT;
      const theme = shot.theme || "dark";

      await session.setViewport(viewport.width, viewport.height);
      // Defensively close any native <dialog> left open by a previous
      // shot's actions (e.g. the "add-agent" shot's opened dialog) —
      // dialogs are DOM/session state, not route state, so a hash
      // navigation alone does not dismiss one.
      await session.evaluate(`
        document.querySelectorAll("dialog[open]").forEach((d) => { try { d.close(); } catch {} });
        true
      `).catch(() => {});
      // localStorage is inaccessible on the initial about:blank document
      // (SecurityError) — navigate to the target route FIRST, then set/
      // re-apply the theme (which itself reloads), matching ui-audit.mjs's
      // documented workaround for BiDi's --localStorage-at-navigation race.
      let route = shot.route;
      if (shot.resolveHostRoute) {
        const resolved = await resolveFirstHostRoute(session, opt.hub, opt.wait, shot.hostIdHint);
        if (!resolved) throw new Error(`shot "${shot.name}": resolveHostRoute requested but no host exists on this hub`);
        route = resolved;
      }
      await session.navigate(`${opt.hub}/${route}`);
      if (theme !== lastTheme) {
        await setTheme(session, theme, opt.wait);
        lastTheme = theme;
      }
      await sleep(shot.waitMs ?? opt.wait);
      await runShotActions(session, shot.actions, opt.wait);

      const replaced = await scrubMarkers(session, substitutions, protectedIdentifiers);

      const rawPng = await session.screenshotBuffer();
      const chunksBefore = listPngChunks(rawPng);
      const { out: cleanPng, droppedChunks } = stripPngMetadata(rawPng);

      const outPath = join(opt.out, `${shot.name}.png`);
      writeFileSync(outPath, cleanPng);

      results.push({
        name: shot.name,
        route,
        viewport: `${viewport.width}x${viewport.height}`,
        theme,
        bytes: cleanPng.length,
        markersReplaced: replaced,
        chunksBefore,
        droppedChunks,
      });
      process.stdout.write(
        `captured ${shot.name}.png (${cleanPng.length} bytes, ${replaced} marker replacement(s), ` +
          `dropped chunks: ${droppedChunks.length ? droppedChunks.join(",") : "none"})\n`,
      );
    };

    for (const shot of loginShots) {
      if (hardTimedOut) break;
      await captureOne(shot);
    }

    await loginViaForm(session, opt.hub, opt.user, password, opt.wait);
    lastTheme = null; // theme was set pre-login against an unauthenticated page; force a re-apply post-login

    for (const shot of otherShots) {
      if (hardTimedOut) break;
      await captureOne(shot);
    }

    writeFileSync(join(opt.out, "capture-report.json"), JSON.stringify({ generatedAt: new Date().toISOString(), hub: opt.hub, results }, null, 2));

    clearTimeout(timer);
    await session.stop();
    process.exit(hardTimedOut ? 3 : 0);
  } catch (e) {
    clearTimeout(timer);
    process.stderr.write(`capture-screenshots.mjs: fatal error: ${e && e.stack ? e.stack : e}\n`);
    await session.stop();
    process.exit(3);
  }
}

main();
