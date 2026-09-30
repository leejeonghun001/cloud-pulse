#!/usr/bin/env node
// ui-audit.mjs — repo UI accessibility/responsiveness audit tool
// (SPEC-v0.7 §5). Drives headless Firefox via WebDriver BiDi (Node 22+
// built-in WebSocket, zero npm dependencies), ported and extended from
// the orchestrator's tools/shot.mjs: signs in through the real login
// form, switches theme, visits every configured route at every
// viewport x theme combination, and runs the checks below. Pure logic
// (color/contrast math, target-size rules, report aggregation) lives in
// ./ui-audit-lib.mjs so it can be unit-tested without a browser — see
// web/test/ui-audit-lib.test.mjs.
//
// Checks performed per (route, viewport, theme):
//   1. Horizontal overflow (scrollWidth > clientWidth on <html>/<body>)
//   2. WCAG contrast for visible text (ancestor-composited background)
//   3. Interactive target size < 24x24 (WCAG 2.2 AA 2.5.8, inline-text
//      exception honored)
//   4. JS errors / console.error / CSP violations
//   5. Visible focus indicator on Tab, sampled over the first N
//      focusable elements
//
// Known limitation carried over from shot.mjs: --localStorage-at-
// navigation-time has a pre-navigation-fetch race in Firefox's BiDi
// implementation. This tool never uses that flag — theme is instead set
// by --eval'ing `localStorage.setItem(...)` *after* the page has loaded
// once, followed by an explicit reload, which sidesteps the race
// entirely (see setTheme() below).
//
// Usage:
//   node scripts/ui-audit.mjs --hub http://127.0.0.1:18090 \
//     --user admin --password-file /path/to/pw.txt \
//     --out /tmp/ui-audit-out [--routes a,b,c] \
//     [--viewports 360x800,390x844,768x1024,1024x768,1440x900] \
//     [--themes dark,light] [--allow scripts/ui-audit.allow.json] \
//     [--timeout 120000] [--wait 1500] [--focus-sample 12]
//
// Exit codes: 0 = no unallowed violations, 1 = unallowed violations
// found (or a usage/config error), 3 = tool-level failure (browser
// never came up, hard timeout, etc.) — mirrors shot.mjs's 1/2/3
// convention but repurposes "2" as "1" here since this tool's overall
// pass/fail is a single aggregate exit code, not per-invocation.
import { spawn } from "node:child_process";
import { mkdtempSync, rmSync, mkdirSync, writeFileSync, readFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createServer } from "node:net";
import {
  evaluateContrast,
  evaluateTargetSize,
  violationKey,
  buildRouteKey,
  parseViewportSpec,
  parseAllowList,
  aggregateReport,
} from "./ui-audit-lib.mjs";

const DEFAULT_ROUTES = ["#/login", "#/", "#/alerts", "#/costs", "#/updates"];
const SETTINGS_SECTIONS = [
  "general",
  "network",
  "security",
  "agents",
  "traffic",
  "notifications",
  "alert-rules",
  "billing",
  "storage",
];
const DEFAULT_VIEWPORTS = "360x800,390x844,768x1024,1024x768,1440x900";
const DEFAULT_THEMES = "dark,light";

function parseArgs(argv) {
  const opt = {
    hub: null,
    user: "admin",
    passwordFile: null,
    routes: null,
    viewports: DEFAULT_VIEWPORTS,
    themes: DEFAULT_THEMES,
    out: null,
    allow: null,
    timeout: 180000,
    wait: 1500,
    focusSample: 12,
    includeFirstHost: true,
  };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    const next = () => argv[++i];
    switch (a) {
      case "--hub": opt.hub = next(); break;
      case "--user": opt.user = next(); break;
      case "--password-file": opt.passwordFile = next(); break;
      case "--routes": opt.routes = next().split(",").map((s) => s.trim()).filter(Boolean); break;
      case "--viewports": opt.viewports = next(); break;
      case "--themes": opt.themes = next(); break;
      case "--out": opt.out = next(); break;
      case "--allow": opt.allow = next(); break;
      case "--timeout": opt.timeout = Number(next()); break;
      case "--wait": opt.wait = Number(next()); break;
      case "--focus-sample": opt.focusSample = Number(next()); break;
      case "--no-host-route": opt.includeFirstHost = false; break;
      case "-h":
      case "--help":
        printUsage();
        process.exit(0);
        break;
      default:
        process.stderr.write(`ui-audit.mjs: unknown argument "${a}"\n`);
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
    "usage: node scripts/ui-audit.mjs --hub URL --password-file FILE --out DIR\n" +
      "  [--user admin] [--routes r1,r2,...] [--viewports WxH,...] [--themes dark,light]\n" +
      "  [--allow FILE] [--timeout ms] [--wait ms] [--focus-sample N] [--no-host-route]\n" +
      "\n" +
      "The password is NEVER accepted on argv — only via --password-file (a plain-text\n" +
      "file containing exactly the password, trailing newline optional).\n" +
      "\n" +
      "Default routes: login, #/, #/alerts, #/costs, #/updates, one #/host/<id> (unless\n" +
      "--no-host-route or no host exists yet), and every #/settings/<section>.\n",
  );
}

function fail(msg) {
  process.stderr.write(`ui-audit.mjs: ${msg}\n`);
  process.exit(1);
}

function readPasswordFile(path) {
  if (!existsSync(path)) fail(`--password-file "${path}" does not exist`);
  const raw = readFileSync(path, "utf8");
  const pw = raw.replace(/\r?\n$/, "");
  if (pw.length === 0) fail(`--password-file "${path}" is empty`);
  return pw;
}

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

/**
 * BidiSession wraps a headless Firefox process + its WebDriver BiDi
 * WebSocket, exposing the small set of commands this tool needs. Mirrors
 * shot.mjs's connection dance (spawn firefox --headless, scrape stderr
 * for the "WebDriver BiDi listening" line, connect a plain WebSocket)
 * but adds session-scoped console/CSP capture across MULTIPLE
 * navigations (shot.mjs only ever navigated once per process).
 */
class BidiSession {
  constructor() {
    this.ff = null;
    this.ws = null;
    this.profile = null;
    this.context = null;
    this.reqId = 0;
    this.pending = new Map();
    this.consoleLog = [];
    this.jsErrors = [];
    this.cspViolations = [];
  }

  async start() {
    this.profile = mkdtempSync(join(tmpdir(), "cp-ui-audit-"));
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
    for (let i = 0; i < 200 && !ws && !exited; i++) {
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
    await this.send("session.subscribe", { events: ["log.entryAdded"] });
    const created = await this.send("browsingContext.create", { type: "tab" });
    this.context = created.context;
    await this.send("script.addPreloadScript", {
      functionDeclaration:
        "() => { window.__cpCSP = []; document.addEventListener('securitypolicyviolation', (e) => window.__cpCSP.push(e.violatedDirective + ' blocked ' + e.blockedURI)); }",
    });
  }

  _onMessage(m) {
    if (m.id && this.pending.has(m.id)) {
      const { res, rej } = this.pending.get(m.id);
      this.pending.delete(m.id);
      if (m.type === "error") rej(new Error(`${m.error}: ${m.message}`));
      else res(m.result);
      return;
    }
    if (m.method === "log.entryAdded") {
      const e = m.params;
      if (e.type === "javascript") {
        this.jsErrors.push(`${e.text} @${e.stackTrace?.callFrames?.[0]?.url ?? ""}:${e.stackTrace?.callFrames?.[0]?.lineNumber ?? ""}`);
      } else if (e.level === "error") {
        this.consoleLog.push(e.text);
      }
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

  /** evaluate runs a JS expression in the page and deserializes the result. */
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

  async screenshot(outPath) {
    const shot = await this.send("browsingContext.captureScreenshot", { context: this.context, origin: "viewport" });
    writeFileSync(outPath, Buffer.from(shot.data, "base64"));
  }

  /** drainCSP returns and clears CSP violations recorded since the last call. */
  async drainCSP() {
    const raw = await this.evaluate("JSON.stringify(window.__cpCSP || [])");
    const list = JSON.parse(raw || "[]");
    await this.evaluate("window.__cpCSP = []");
    return list;
  }

  /** drainConsoleAndErrors returns and clears console.error/JS-error buffers. */
  drainConsoleAndErrors() {
    const console_ = this.consoleLog.slice();
    const errors = this.jsErrors.slice();
    this.consoleLog = [];
    this.jsErrors = [];
    return { console: console_, errors };
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
// In-page extraction scripts. Kept as plain strings (evaluated via BiDi
// script.evaluate) rather than imported functions, since they execute
// inside the Firefox page context, not this Node process.
// ---------------------------------------------------------------------------

const EXTRACT_OVERFLOW = `
  (() => {
    const de = document.documentElement;
    const overflowX = de.scrollWidth > de.clientWidth + 1;
    return JSON.stringify({ overflowX, scrollWidth: de.scrollWidth, clientWidth: de.clientWidth });
  })()
`;

const EXTRACT_TEXT_NODES = `
  (() => {
    const out = [];
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT, {
      acceptNode(node) {
        if (!node.nodeValue || !node.nodeValue.trim()) return NodeFilter.FILTER_REJECT;
        const el = node.parentElement;
        if (!el) return NodeFilter.FILTER_REJECT;
        const rect = el.getBoundingClientRect();
        if (rect.width === 0 || rect.height === 0) return NodeFilter.FILTER_REJECT;
        const style = getComputedStyle(el);
        if (style.visibility === "hidden" || style.display === "none" || Number(style.opacity) === 0) return NodeFilter.FILTER_REJECT;
        return NodeFilter.FILTER_ACCEPT;
      },
    });
    const seen = new Set();
    let node;
    while ((node = walker.nextNode())) {
      const el = node.parentElement;
      if (seen.has(el)) continue;
      seen.add(el);
      const style = getComputedStyle(el);
      const backgrounds = [];
      let cur = el;
      let depth = 0;
      while (cur && depth < 20) {
        const s = getComputedStyle(cur);
        backgrounds.push(s.backgroundColor);
        cur = cur.parentElement;
        depth++;
      }
      out.push({
        text: node.nodeValue.trim().slice(0, 60),
        selector: describeElement(el),
        foreground: style.color,
        backgrounds,
        fontSizePx: parseFloat(style.fontSize),
        bold: Number(style.fontWeight) >= 700 || style.fontWeight === "bold",
      });
      if (out.length >= 400) break;
    }
    function describeElement(e) {
      if (!e) return "";
      const id = e.id ? "#" + e.id : "";
      const cls = e.className && typeof e.className === "string" ? "." + e.className.split(/\\s+/).filter(Boolean).slice(0, 2).join(".") : "";
      return e.tagName.toLowerCase() + id + cls;
    }
    return JSON.stringify(out);
  })()
`;

const EXTRACT_INTERACTIVE_TARGETS = `
  (() => {
    const selector = 'a[href], button, input, select, textarea, [role="button"], [tabindex]:not([tabindex="-1"])';
    const els = Array.from(document.querySelectorAll(selector));
    const out = [];
    for (const el of els) {
      const rect = el.getBoundingClientRect();
      if (rect.width === 0 && rect.height === 0) continue;
      const style = getComputedStyle(el);
      if (style.visibility === "hidden" || style.display === "none") continue;
      // WCAG 2.2 AA 2.5.8 inline exception: a link/target that sits inside a
      // run of text (its parent's display is inline and it has text
      // sibling content) is exempt from the 24x24 minimum.
      const parent = el.parentElement;
      const isInline = el.tagName === "A" && parent && getComputedStyle(parent).display.startsWith("inline") === false
        ? Array.from(parent.childNodes).some((n) => n.nodeType === Node.TEXT_NODE && n.nodeValue.trim().length > 0)
        : false;
      // WCAG 2.5.8 measures the target the user can activate: for a form
      // control inside (or referenced by) a <label>, clicking the label
      // activates the control, so the label's box is the target.
      let targetRect = rect;
      if (el.tagName === "INPUT" || el.tagName === "SELECT" || el.tagName === "TEXTAREA") {
        const label = el.closest("label") || (el.id ? document.querySelector('label[for="' + CSS.escape(el.id) + '"]') : null);
        if (label) {
          const lr = label.getBoundingClientRect();
          if (lr.width > 0 && lr.height > 0) targetRect = lr;
        }
      }
      out.push({
        selector: el.tagName.toLowerCase() + (el.id ? "#" + el.id : ""),
        widthPx: Math.max(rect.width, targetRect.width),
        heightPx: Math.max(rect.height, targetRect.height),
        isInline,
      });
      if (out.length >= 300) break;
    }
    return JSON.stringify(out);
  })()
`;

const EXTRACT_FOCUS_INDICATOR = (sampleN) => `
  (async () => {
    const selector = 'a[href], button, input, select, textarea, [tabindex]:not([tabindex="-1"])';
    const els = Array.from(document.querySelectorAll(selector)).filter((e) => {
      const r = e.getBoundingClientRect();
      return r.width > 0 && r.height > 0;
    }).slice(0, ${sampleN});
    const out = [];
    for (const el of els) {
      const before = getComputedStyle(el);
      const beforeBox = before.boxShadow + "|" + before.outlineWidth + "|" + before.outlineStyle + "|" + before.outlineColor;
      el.focus({ preventScroll: true });
      await new Promise((r) => setTimeout(r, 20));
      const focused = document.activeElement === el;
      const after = getComputedStyle(el);
      const afterBox = after.boxShadow + "|" + after.outlineWidth + "|" + after.outlineStyle + "|" + after.outlineColor;
      const changed = focused && (beforeBox !== afterBox);
      const hasVisibleOutline = focused && after.outlineStyle !== "none" && after.outlineWidth !== "0px";
      const hasVisibleBoxShadow = focused && after.boxShadow !== "none" && after.boxShadow !== before.boxShadow;
      out.push({
        selector: el.tagName.toLowerCase() + (el.id ? "#" + el.id : ""),
        focused,
        visibleIndicator: hasVisibleOutline || hasVisibleBoxShadow || changed,
      });
      el.blur();
    }
    return JSON.stringify(out);
  })()
`;

// ---------------------------------------------------------------------------
// Login + theme helpers
// ---------------------------------------------------------------------------

/**
 * loginViaForm drives the real #/login form (never calls the API
 * directly) so the audit exercises the actual sign-in UX. Waits for the
 * session to be established by polling localStorage's session-token key
 * the same way login.js's onLoggedIn callback would have set it.
 */
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
  // Poll for a stored session token (sessionStorage/localStorage key
  // owned by core/auth.js) rather than a fixed sleep, bounded to avoid
  // ever hanging the whole run on a broken login.
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

/**
 * setTheme sets the cp_theme localStorage key and reloads — the
 * documented workaround for BiDi's --localStorage-at-navigation race
 * (see this file's header comment and shot.mjs's own --localStorage
 * caveat).
 */
async function setTheme(session, theme, waitMs) {
  await session.evaluate(`localStorage.setItem("cp_theme", ${JSON.stringify(theme)})`);
  await session.evaluate(`location.reload()`);
  await sleep(waitMs);
}

async function resolveFirstHostRoute(session, waitMs) {
  await sleep(waitMs);
  try {
    const hostID = await session.evaluate(`
      (() => {
        const link = document.querySelector('a[href^="#/host/"]');
        if (!link) return null;
        const m = link.getAttribute("href").match(/^#\\/host\\/([^/?]+)/);
        return m ? m[1] : null;
      })()
    `);
    return hostID ? `#/host/${hostID}` : null;
  } catch {
    return null;
  }
}

// ---------------------------------------------------------------------------
// Per-route check execution
// ---------------------------------------------------------------------------

async function runChecksForRoute(session, { route, viewportLabel, theme, waitMs, focusSample, outDir, screenshotIndex }) {
  const routeKey = buildRouteKey({ route, viewport: viewportLabel, theme });
  const findings = [];

  await sleep(waitMs);

  // 1. Horizontal overflow
  try {
    const overflow = JSON.parse(await session.evaluate(EXTRACT_OVERFLOW));
    if (overflow.overflowX) {
      findings.push({
        key: violationKey("overflow", routeKey),
        checkId: "overflow",
        routeKey,
        detail: overflow,
      });
    }
  } catch (e) {
    findings.push({ key: violationKey("tool-error-overflow", routeKey), checkId: "tool-error", routeKey, detail: { error: String(e) } });
  }

  // 2. Contrast
  try {
    const nodes = JSON.parse(await session.evaluate(EXTRACT_TEXT_NODES));
    let contrastFailures = 0;
    const samples = [];
    for (const n of nodes) {
      const result = evaluateContrast({
        foreground: n.foreground,
        backgroundsNearestFirst: n.backgrounds,
        fontSizePx: n.fontSizePx,
        bold: n.bold,
      });
      if (!result.skipped && !result.pass) {
        contrastFailures++;
        if (samples.length < 20) samples.push({ selector: n.selector, text: n.text, ratio: result.ratio, required: result.required });
      }
    }
    if (contrastFailures > 0) {
      findings.push({
        key: violationKey("contrast", routeKey),
        checkId: "contrast",
        routeKey,
        detail: { count: contrastFailures, samples },
      });
    }
  } catch (e) {
    findings.push({ key: violationKey("tool-error-contrast", routeKey), checkId: "tool-error", routeKey, detail: { error: String(e) } });
  }

  // 3. Target size
  try {
    const targets = JSON.parse(await session.evaluate(EXTRACT_INTERACTIVE_TARGETS));
    let tooSmall = 0;
    const samples = [];
    for (const t of targets) {
      const result = evaluateTargetSize({ widthPx: t.widthPx, heightPx: t.heightPx, isInline: t.isInline });
      if (!result.pass) {
        tooSmall++;
        if (samples.length < 20) samples.push({ selector: t.selector, widthPx: result.widthPx, heightPx: result.heightPx });
      }
    }
    if (tooSmall > 0) {
      findings.push({
        key: violationKey("target-size", routeKey),
        checkId: "target-size",
        routeKey,
        detail: { count: tooSmall, samples },
      });
    }
  } catch (e) {
    findings.push({ key: violationKey("tool-error-target-size", routeKey), checkId: "tool-error", routeKey, detail: { error: String(e) } });
  }

  // 4. JS errors / console.error / CSP
  const { console: consoleErrors, errors: jsErrors } = session.drainConsoleAndErrors();
  const cspViolations = await session.drainCSP().catch(() => []);
  if (jsErrors.length > 0) {
    findings.push({ key: violationKey("js-error", routeKey), checkId: "js-error", routeKey, detail: { errors: jsErrors.slice(0, 20) } });
  }
  if (consoleErrors.length > 0) {
    findings.push({ key: violationKey("console-error", routeKey), checkId: "console-error", routeKey, detail: { errors: consoleErrors.slice(0, 20) } });
  }
  if (cspViolations.length > 0) {
    findings.push({ key: violationKey("csp", routeKey), checkId: "csp", routeKey, detail: { violations: cspViolations.slice(0, 20) } });
  }

  // 5. Focus indicator sample
  try {
    const focusResults = JSON.parse(await session.evaluate(EXTRACT_FOCUS_INDICATOR(focusSample)));
    const missing = focusResults.filter((r) => r.focused && !r.visibleIndicator);
    if (missing.length > 0) {
      findings.push({
        key: violationKey("focus-indicator", routeKey),
        checkId: "focus-indicator",
        routeKey,
        detail: { count: missing.length, sampled: focusResults.length, samples: missing.slice(0, 20) },
      });
    }
  } catch (e) {
    findings.push({ key: violationKey("tool-error-focus", routeKey), checkId: "tool-error", routeKey, detail: { error: String(e) } });
  }

  // Screenshot (always captured, for CI artifact upload / manual review)
  try {
    const safeName = routeToFilename(route, viewportLabel, theme, screenshotIndex);
    await session.screenshot(join(outDir, safeName));
  } catch {
    // Screenshot failure is not itself a violation; the checks above
    // already ran against the live DOM.
  }

  return findings;
}

function routeToFilename(route, viewportLabel, theme, index) {
  const slug = route.replace(/^#\/?/, "root").replace(/[^a-zA-Z0-9._-]+/g, "_").replace(/^_+|_+$/g, "") || "root";
  return `${String(index).padStart(3, "0")}_${slug}_${viewportLabel}_${theme}.png`;
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

async function main() {
  const opt = parseArgs(process.argv.slice(2));
  const password = readPasswordFile(opt.passwordFile);
  const viewports = opt.viewports.split(",").map((s) => parseViewportSpec(s));
  const themes = opt.themes.split(",").map((s) => s.trim()).filter(Boolean);
  for (const t of themes) {
    if (t !== "dark" && t !== "light") fail(`invalid theme "${t}" (only "dark"/"light" are supported)`);
  }

  mkdirSync(opt.out, { recursive: true });

  let allowList = [];
  if (opt.allow && existsSync(opt.allow)) {
    const raw = JSON.parse(readFileSync(opt.allow, "utf8"));
    allowList = parseAllowList(raw);
  }

  const session = new BidiSession();
  let hardTimedOut = false;
  const timer = setTimeout(() => {
    hardTimedOut = true;
    process.stderr.write(`ui-audit.mjs: hard timeout (${opt.timeout}ms) reached; aborting\n`);
    session.stop().finally(() => process.exit(3));
  }, opt.timeout);

  try {
    await session.start();

    // Establish routes list: defaults + settings sections + (optionally)
    // one real host detail route, discovered after logging in.
    let routes = opt.routes;
    if (!routes) {
      routes = [...DEFAULT_ROUTES, ...SETTINGS_SECTIONS.map((s) => `#/settings/${s}`)];
    }

    // Log in once (session persists across navigations within this one
    // Firefox profile/tab for the remainder of the run).
    await loginViaForm(session, opt.hub, opt.user, password, opt.wait);

    if (opt.includeFirstHost && !opt.routes) {
      await session.navigate(`${opt.hub}/#/`);
      const hostRoute = await resolveFirstHostRoute(session, opt.wait);
      if (hostRoute) routes.splice(2, 0, hostRoute); // after login, #/
    }

    const allFindings = [];
    let screenshotIndex = 0;
    const visited = [];

    for (const theme of themes) {
      await setTheme(session, theme, opt.wait);
      for (const vp of viewports) {
        await session.setViewport(vp.width, vp.height);
        for (const route of routes) {
          if (hardTimedOut) break;
          const url = `${opt.hub}/${route}`;
          await session.navigate(url);
          const findings = await runChecksForRoute(session, {
            route,
            viewportLabel: vp.label,
            theme,
            waitMs: opt.wait,
            focusSample: opt.focusSample,
            outDir: opt.out,
            screenshotIndex: screenshotIndex++,
          });
          allFindings.push(...findings);
          visited.push({ route, viewport: vp.label, theme, findingCount: findings.length });
        }
      }
    }

    const report = aggregateReport(allFindings, allowList);
    const fullReport = {
      generatedAt: new Date().toISOString(),
      hub: opt.hub,
      routes,
      viewports: viewports.map((v) => v.label),
      themes,
      visited,
      ...report,
    };
    writeFileSync(join(opt.out, "report.json"), JSON.stringify(fullReport, null, 2));

    printSummary(fullReport);

    clearTimeout(timer);
    await session.stop();
    process.exit(report.summary.violations > 0 ? 1 : 0);
  } catch (e) {
    clearTimeout(timer);
    process.stderr.write(`ui-audit.mjs: fatal error: ${e && e.stack ? e.stack : e}\n`);
    await session.stop();
    process.exit(3);
  }
}

function printSummary(report) {
  const lines = [
    `ui-audit: ${report.summary.total} finding(s) across ${report.visited.length} route/viewport/theme combination(s)`,
    `  violations (not allow-listed): ${report.summary.violations}`,
    `  allowed (allow-listed, informational): ${report.summary.allowed}`,
  ];
  for (const [checkId, count] of Object.entries(report.summary.byCheck)) {
    lines.push(`    ${checkId}: ${count}`);
  }
  if (report.summary.violations > 0) {
    lines.push("");
    lines.push("Unallowed violations (first 30):");
    for (const v of report.violations.slice(0, 30)) {
      lines.push(`  - ${v.key}`);
    }
  }
  process.stdout.write(lines.join("\n") + "\n");
}

main();
