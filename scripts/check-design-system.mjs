#!/usr/bin/env node
// check-design-system.mjs — isolated, data-free browser evidence for the
// dashboard's shared design-system components. This deliberately does not
// load main.js, contact a hub/API, or perform any authentication action.
// It serves the checked-out app.css and JS assets read-only plus a temporary
// showcase document, then drives a fresh headless Firefox profile via BiDi.

import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import {
  existsSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { createServer as createHTTPServer } from "node:http";
import { createServer as createTCPServer } from "node:net";
import { dirname, extname, join, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import { safeVerificationPath, verificationEnvironment } from "./verify-round28-safety.mjs";

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url));
const WEB_DIR = realpathSync(resolve(SCRIPT_DIR, "../web"));
const ASSETS_DIR = realpathSync(join(WEB_DIR, "assets"));
const OUT_ROOT = resolve(SCRIPT_DIR, "../.verification/phase1");
const LOOPBACK = "127.0.0.1";
const DEFAULT_PORT = 18090;
const VIEWPORTS = [360, 768, 1440];
const LANGUAGES = ["en", "ko"];
const THEMES = ["light", "dark"];
const VIEWPORT_HEIGHT = 1400;
const BROWSER_START_TIMEOUT_MS = 30_000;
const COMMAND_TIMEOUT_MS = 10_000;
const RUN_TIMEOUT_MS = 180_000;

export function parseArgs(argv) {
  const options = { host: LOOPBACK, port: DEFAULT_PORT };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === "--help" || arg === "-h") {
      process.stdout.write(
        "usage: node scripts/check-design-system.mjs [--host 127.0.0.1] [--port 18090] [--artifact-root DIR]\n" +
          "Only 127.0.0.1 is accepted; port 8090 is explicitly forbidden.\n" +
          `Legacy default artifacts: ${OUT_ROOT}; Round28 must select .verification/round28 with --artifact-root.\n`,
      );
      return null;
    }
    if (!["--host", "--port", "--artifact-root"].includes(arg)) throw new Error(`unknown argument: ${arg}`);
    const value = argv[++i];
    if (value === undefined || value.startsWith("--")) throw new Error(`${arg} requires a value`);
    if (arg === "--host") options.host = value;
    else if (arg === "--artifact-root") options.artifactRoot = resolveArtifactRoot(value);
    else if (!/^\d+$/.test(value)) throw new Error("--port must be an integer");
    else options.port = Number(value);
  }

  if (options.host !== LOOPBACK) throw new Error(`refusing non-loopback bind address ${JSON.stringify(options.host)}; only ${LOOPBACK} is allowed`);
  if (!Number.isInteger(options.port) || options.port < 1024 || options.port > 65535) {
    throw new Error("--port must be between 1024 and 65535");
  }
  if (options.port === 8090) throw new Error("refusing port 8090; this checker is test-only and loopback-only");
  return options;
}

export function resolveArtifactRoot(path = OUT_ROOT) {
  return safeVerificationPath(path, "gallery artifact root");
}

export function prepareGalleryArtifacts(options) {
  const root = resolveArtifactRoot(options.artifactRoot || process.env.CP_VERIFY_ARTIFACT_ROOT || OUT_ROOT);
  const env = verificationEnvironment(root);
  const runDir = mkdtempSync(join(root, `run-${new Date().toISOString().replace(/[:.]/g, "-")}-`));
  return { root, runDir, env, reportPath: safeVerificationPath(join(runDir, "report.json")) };
}

export function showcaseHTML(lang) {
  const words = lang === "ko"
    ? {
        title: "디자인 시스템 예시",
        intro: "실제 측정값이나 API 데이터가 없는 컴포넌트 상태 예시입니다.",
        focus: "키보드 포커스 예시",
        statusTitle: "상태 표시",
        statusHelp: "상태 이름만 표시하며 호스트 데이터를 사용하지 않습니다.",
        online: "온라인",
        offline: "오프라인",
        warning: "경고",
        critical: "위험",
        neutral: "미확인",
        longLabel: "전체 네트워크 송신 사용량과 저장 공간 상태를 확인하는 긴 한국어 설명",
        controlsTitle: "입력과 선택 상태",
        fieldLabel: "표시 이름 예시",
        fieldHelp: "예시 입력입니다. 저장하거나 전송하지 않습니다.",
        action: "기본 작업",
        secondary: "보조 작업",
        danger: "위험 작업",
        disabled: "사용 불가",
        selected: "선택된 설정",
        legacyTitle: "이전 상태 점 호환성",
        providerTitle: "공급자 배지",
        metricTitle: "측정값 자리 표시자",
        metricLabel: "CPU",
        noData: "미확인",
        barTitle: "사용량 막대 자리 표시자",
        storage: "저장 공간",
        network: "네트워크",
        sparkTitle: "스파크라인 자리 표시자",
        noSamples: "미확인",
      }
    : {
        title: "Design system examples",
        intro: "Component state examples with no measurements or API data.",
        focus: "Keyboard focus example",
        statusTitle: "Status indicators",
        statusHelp: "State labels only; no host data is used.",
        online: "Online",
        offline: "Offline",
        warning: "Warning",
        critical: "Critical",
        neutral: "No data",
        longLabel: "A longer description of total network transfer usage and storage availability",
        controlsTitle: "Inputs and selected states",
        fieldLabel: "Example display name",
        fieldHelp: "An example input. Nothing is saved or sent.",
        action: "Primary action",
        secondary: "Secondary action",
        danger: "Danger action",
        disabled: "Unavailable",
        selected: "Selected settings",
        legacyTitle: "Legacy status-dot compatibility",
        providerTitle: "Provider badges",
        metricTitle: "Metric placeholder",
        metricLabel: "CPU",
        noData: "No data",
        barTitle: "Usage-bar placeholders",
        storage: "Storage",
        network: "Network",
        sparkTitle: "Sparkline placeholder",
        noSamples: "No data",
      };

  return `<!doctype html>
<html lang="${lang}">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>${words.title}</title>
  <script src="/assets/js/theme-init.js"></script>
  <link rel="stylesheet" href="/assets/app.css">
  <style>
    body { margin: 0; min-height: 100vh; padding: 1rem; background: rgb(var(--bg)); color: rgb(var(--text)); font-family: var(--font-sans); }
    .showcase-page { width: min(100%, 80rem); margin-inline: auto; display: grid; gap: 1rem; }
    .showcase-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 18rem), 1fr)); gap: 1rem; align-items: start; }
    .showcase-row { display: flex; flex-wrap: wrap; align-items: center; gap: .75rem; }
    .showcase-stack { display: grid; gap: .75rem; min-width: 0; }
    .showcase-card-group { display: grid; gap: .75rem; }
    .showcase-focus-card { display: block; }
    .showcase-page > header { padding-block: .5rem; }
    .showcase-long { max-width: 12rem; }
    .showcase-controls { border: 0; padding: 0; margin: 0; min-width: 0; }
  </style>
</head>
<body>
  <main class="showcase-page">
    <header class="showcase-stack">
      <h1 class="cp-page-title">${words.title}</h1>
      <p class="cp-card-subtitle">${words.intro}</p>
      <a id="focus-example" class="cp-card cp-card-interactive showcase-focus-card" href="#status-examples">${words.focus}</a>
    </header>

    <div class="showcase-grid">
      <section id="status-examples" class="cp-card showcase-card-group" aria-labelledby="status-title">
        <div class="cp-card-header"><h2 id="status-title" class="cp-card-title">${words.statusTitle}</h2></div>
        <p class="cp-card-subtitle">${words.statusHelp}</p>
        <div class="showcase-row" aria-label="Status pill examples">
          <span class="cp-status-pill cp-status-pill-ok" data-status-pill="online"><span class="cp-dot cp-status-dot-ok cp-status-dot-live" aria-hidden="true"></span>${words.online}</span>
          <span class="cp-status-pill cp-status-pill-exceeded" data-status-pill="offline"><span class="cp-dot cp-status-dot-exceeded" aria-hidden="true"></span>${words.offline}</span>
          <span class="cp-status-pill cp-status-pill-solid" data-status-pill="solid-offline"><span class="cp-dot cp-status-dot-exceeded" aria-hidden="true"></span>${words.offline}</span>
          <span class="cp-status-pill cp-status-pill-warning" data-status-pill="warning"><span class="cp-dot cp-status-dot-warning" aria-hidden="true"></span>${words.warning}</span>
          <span class="cp-status-pill cp-status-pill-critical" data-status-pill="critical"><span class="cp-dot cp-dot-lg cp-status-dot-critical" aria-hidden="true"></span>${words.critical}</span>
          <span class="cp-status-pill cp-status-pill-neutral" data-status-pill="neutral"><span class="cp-dot cp-status-dot-neutral" aria-hidden="true"></span>${words.neutral}</span>
        </div>
        <div class="showcase-stack">
          <h3 class="cp-card-subtitle">${words.legacyTitle}</h3>
          <div class="showcase-row" aria-label="Legacy dot examples">
            <span class="cp-dot cp-dot-up" data-legacy-dot="up" aria-hidden="true"></span><span>${words.online}</span>
            <span class="cp-dot cp-dot-down" data-legacy-dot="down" aria-hidden="true"></span><span>${words.offline}</span>
          </div>
        </div>
      </section>

      <section class="cp-card showcase-card-group" aria-labelledby="provider-title">
        <h2 id="provider-title" class="cp-card-title">${words.providerTitle}</h2>
        <div class="showcase-row" aria-label="Provider badge examples">
          <span class="cp-badge cp-badge-aws" data-provider-badge="aws">AWS</span>
          <span class="cp-badge cp-badge-oci" data-provider-badge="oci">OCI</span>
          <span class="cp-badge cp-badge-other" data-provider-badge="other">Other</span>
          <span class="cp-badge cp-badge-r2" data-provider-badge="r2">R2</span>
          <span class="cp-badge cp-badge-hub" data-provider-badge="hub">Hub</span>
          <span class="cp-badge cp-badge-update" data-provider-badge="update">Update</span>
        </div>
        <div class="showcase-row">
          <span class="cp-chip cp-chip-ok" data-chip="ok">${words.online}</span>
          <span class="cp-chip cp-chip-warning" data-chip="warning">${words.warning}</span>
          <span class="cp-chip cp-chip-critical" data-chip="critical">${words.critical}</span>
          <span class="cp-chip cp-chip-exceeded" data-chip="exceeded">${words.offline}</span>
          <span class="cp-chip cp-chip-other" data-chip="neutral">${words.neutral}</span>
        </div>
      </section>

      <section class="cp-card showcase-card-group" aria-labelledby="metric-title">
        <h2 id="metric-title" class="cp-card-title">${words.metricTitle}</h2>
        <div class="cp-stat-card" data-metric-placeholder>
          <div class="cp-stat-card-icon" aria-hidden="true"></div>
          <div class="cp-stat-card-text">
            <span class="cp-stat-card-label" data-type-sample>${words.metricLabel}</span>
            <span class="cp-stat-card-value" data-type-sample>${words.noData}</span>
          </div>
        </div>
        <div class="cp-stat-card cp-stat-card-stack" data-stacked-card>
          <span class="cp-stat-card-label" data-long-label>${words.longLabel}</span>
          <span class="cp-stat-card-value">${words.noData}</span>
          <span class="cp-stat-card-detail">${words.fieldHelp}</span>
        </div>
        <div class="cp-card cp-card-compact" data-compact-card>
          <h3 class="cp-card-title" data-long-label>${words.longLabel}</h3>
          <span class="cp-status-pill cp-status-pill-neutral showcase-long" data-long-label>${words.longLabel}</span>
        </div>
      </section>

      <section class="cp-card showcase-card-group" aria-labelledby="bar-title">
        <h2 id="bar-title" class="cp-card-title">${words.barTitle}</h2>
        <div class="cp-meter" data-empty-bar>
          <div class="cp-meter-head"><span class="cp-meter-label" data-type-sample>${words.storage}</span><span class="cp-meter-value" data-type-sample>${words.noData}</span></div>
          <div class="cp-bar" aria-hidden="true"><span class="cp-bar-track cp-bar-track-lg cp-bar-track-indeterminate"></span><span class="cp-bar-text" data-type-sample>${words.noData}</span></div>
        </div>
        <div class="cp-meter" data-empty-bar>
          <div class="cp-meter-head"><span class="cp-meter-label" data-type-sample>${words.network}</span><span class="cp-meter-value" data-type-sample>${words.noData}</span></div>
          <div class="cp-bar" aria-hidden="true"><span class="cp-bar-track cp-bar-track-indeterminate"></span><span class="cp-bar-text" data-type-sample>${words.noData}</span></div>
        </div>
      </section>

      <section class="cp-card" aria-labelledby="controls-title">
        <h2 id="controls-title" class="cp-card-title">${words.controlsTitle}</h2>
        <fieldset class="showcase-controls showcase-stack" data-showcase-controls>
          <legend class="cp-sr-only">${words.controlsTitle}</legend>
          <div class="cp-field">
            <label for="example-name" class="cp-label">${words.fieldLabel}</label>
            <input id="example-name" class="cp-input" placeholder="${words.fieldLabel}" aria-describedby="example-help" autocomplete="off">
            <p id="example-help" class="cp-help-text">${words.fieldHelp}</p>
          </div>
          <div class="cp-field">
            <label for="example-state" class="cp-label">${words.statusTitle}</label>
            <div class="cp-input-group">
              <select id="example-state" class="cp-select"><option>${words.neutral}</option><option>${words.online}</option></select>
              <span class="cp-input-group-btn" aria-hidden="true" style="pointer-events:none"><svg class="cp-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="m6 9 6 6 6-6"></path></svg></span>
            </div>
          </div>
          <label class="cp-checkbox-row"><input type="checkbox">${words.secondary}</label>
          <div class="showcase-row">
            <button type="button" class="cp-btn cp-btn-primary">${words.action}</button>
            <button type="button" class="cp-btn">${words.secondary}</button>
            <button type="button" class="cp-btn cp-btn-danger">${words.danger}</button>
            <button type="button" class="cp-btn" disabled>${words.disabled}</button>
          </div>
        </fieldset>
        <a id="selected-navigation" class="cp-settings-nav-link cp-settings-nav-link-active" href="#controls-title" aria-current="page">${words.selected}</a>
      </section>

      <section class="cp-card showcase-card-group" aria-labelledby="spark-title">
        <h2 id="spark-title" class="cp-card-title">${words.sparkTitle}</h2>
        <div class="cp-sparkline cp-sparkline-sm" data-empty-sparkline aria-label="${words.noSamples}">
          <div class="cp-sparkline-empty">${words.noSamples}</div>
        </div>
      </section>
    </div>
  </main>
</body>
</html>`;
}

function contentTypeFor(path) {
  switch (extname(path).toLowerCase()) {
    case ".css": return "text/css; charset=utf-8";
    case ".js": return "text/javascript; charset=utf-8";
    case ".json": return "application/json; charset=utf-8";
    case ".svg": return "image/svg+xml";
    case ".png": return "image/png";
    case ".woff": return "font/woff";
    case ".woff2": return "font/woff2";
    default: return "application/octet-stream";
  }
}

export function createShowcaseServer(port) {
  parseArgs(["--port", String(port)]);
  const traffic = { apiRequests: [], servedAssets: [], assetHashes: new Map(), failedAssets: [] };
  const server = createHTTPServer(async (request, response) => {
    const method = request.method || "GET";
    let url;
    try {
      url = new URL(request.url || "/", `http://${LOOPBACK}:${port}`);
    } catch {
      response.writeHead(400).end("Bad request");
      return;
    }

    response.on("finish", () => {
      if (url.pathname.startsWith("/assets/") && response.statusCode >= 400) {
        traffic.failedAssets.push({ method, path: url.pathname, status: response.statusCode });
      }
    });
    let decodedPath;
    try { decodedPath = decodeURIComponent(url.pathname).toLowerCase(); }
    catch { response.writeHead(400).end("Invalid path"); return; }
    if (/^\/(?:api|auth|login|logout|signin|signout)(?:\/|$)/.test(decodedPath)) {
      traffic.apiRequests.push({ method, path: url.pathname });
      response.writeHead(404, { "Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store" });
      response.end('{"error":"not found"}');
      return;
    }
    if (method !== "GET" && method !== "HEAD") {
      response.writeHead(405, { Allow: "GET, HEAD", "Cache-Control": "no-store" }).end("Method not allowed");
      return;
    }

    if (url.pathname === "/__showcase") {
      const lang = url.searchParams.get("lang") || "en";
      if (!LANGUAGES.includes(lang) || [...url.searchParams.keys()].some((key) => key !== "lang")) {
        response.writeHead(400, { "Cache-Control": "no-store" }).end("Invalid showcase query");
        return;
      }
      response.writeHead(200, {
        "Content-Type": "text/html; charset=utf-8",
        "Cache-Control": "no-store",
        "Content-Security-Policy": "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'",
        "X-Content-Type-Options": "nosniff",
      });
      response.end(method === "HEAD" ? undefined : showcaseHTML(lang));
      return;
    }

    if (!url.pathname.startsWith("/assets/")) {
      response.writeHead(404, { "Cache-Control": "no-store" }).end("Not found");
      return;
    }

    let decoded;
    try {
      decoded = decodeURIComponent(url.pathname.slice("/assets/".length));
    } catch {
      response.writeHead(400, { "Cache-Control": "no-store" }).end("Invalid asset path");
      return;
    }
    const filePath = resolve(ASSETS_DIR, decoded);
    if (!filePath.startsWith(`${ASSETS_DIR}${sep}`)) {
      response.writeHead(403, { "Cache-Control": "no-store" }).end("Forbidden");
      return;
    }

    try {
      const actualPath = realpathSync(filePath);
      if (!actualPath.startsWith(`${ASSETS_DIR}${sep}`)) {
        response.writeHead(403, { "Cache-Control": "no-store" }).end("Forbidden");
        return;
      }
      const body = readFileSync(actualPath);
      const assetName = relative(ASSETS_DIR, actualPath);
      traffic.servedAssets.push(assetName);
      const hashes = traffic.assetHashes.get(assetName) || new Set();
      hashes.add(createHash("sha256").update(body).digest("hex"));
      traffic.assetHashes.set(assetName, hashes);
      response.writeHead(200, {
        "Content-Type": contentTypeFor(actualPath),
        "Cache-Control": "no-store",
        "X-Content-Type-Options": "nosniff",
      });
      response.end(method === "HEAD" ? undefined : body);
    } catch (error) {
      if (error?.code === "ENOENT" || error?.code === "ENOTDIR") {
        response.writeHead(404, { "Cache-Control": "no-store" }).end("Not found");
      } else {
        response.writeHead(500, { "Cache-Control": "no-store" }).end("Asset read failed");
      }
    }
  });
  return { server, traffic };
}

export function validateTraffic(report, result, traffic, sessions = []) {
  const browserErrors = sessions.flatMap((session) => session.logs || []);
  const networkFailures = sessions.flatMap((session) => session.networkFailures || []);
  addCheck(report, result, "no API/auth routes requested", traffic.apiRequests.length === 0, traffic.apiRequests);
  addCheck(report, result, "no requested assets failed", traffic.failedAssets.length === 0 && networkFailures.length === 0,
    { failedAssets: traffic.failedAssets, networkFailures });
  addCheck(report, result, "no remaining browser console or JavaScript errors", browserErrors.length === 0, browserErrors);
}

export async function listen(server, port) {
  await new Promise((resolveListen, reject) => {
    const onError = (error) => {
      server.off("listening", onListening);
      reject(error);
    };
    const onListening = () => {
      server.off("error", onError);
      resolveListen();
    };
    server.once("error", onError);
    server.once("listening", onListening);
    server.listen(port, LOOPBACK);
  });
}

function freeTCPPort() {
  return new Promise((resolvePort, reject) => {
    const probe = createTCPServer();
    probe.once("error", reject);
    probe.listen(0, LOOPBACK, () => {
      const { port } = probe.address();
      probe.close((error) => error ? reject(error) : resolvePort(port));
    });
  });
}

const sleep = (milliseconds) => new Promise((resolveSleep) => setTimeout(resolveSleep, milliseconds));

class BidiSession {
  constructor(profileDir, { reducedMotion = false, signal, artifactRoot = OUT_ROOT } = {}) {
    this.profileDir = profileDir;
    this.reducedMotion = reducedMotion;
    this.signal = signal;
    this.artifactRoot = resolveArtifactRoot(artifactRoot);
    this.firefox = null;
    this.websocket = null;
    this.context = null;
    this.requestId = 0;
    this.pending = new Map();
    this.logs = [];
    this.networkFailures = [];
  }

  async start() {
    this.signal?.throwIfAborted();
    if (this.reducedMotion) {
      writeFileSync(safeVerificationPath(join(this.profileDir, "user.js")), 'user_pref("ui.prefersReducedMotion", 1);\n', { mode: 0o600 });
    } else {
      writeFileSync(safeVerificationPath(join(this.profileDir, "user.js")), 'user_pref("ui.prefersReducedMotion", 0);\n', { mode: 0o600 });
    }

    const debugPort = await freeTCPPort();
    this.firefox = spawn(
      process.env.FIREFOX || "firefox",
      ["--headless", "--no-remote", "--profile", this.profileDir, `--remote-debugging-port=${debugPort}`],
      { stdio: ["ignore", "ignore", "pipe"], env: this.artifactRoot === OUT_ROOT
        ? { ...verificationEnvironment(OUT_ROOT), TMPDIR: join(OUT_ROOT, "tmp"), TMP: join(OUT_ROOT, "tmp"), TEMP: join(OUT_ROOT, "tmp") }
        : verificationEnvironment(this.artifactRoot) },
    );
    let stderr = "";
    this.firefox.stderr.on("data", (chunk) => {
      stderr = `${stderr}${chunk}`.slice(-4000);
    });
    let exited = false;
    let spawnError = null;
    this.firefox.once("error", (error) => { spawnError = error; exited = true; });
    this.firefox.once("exit", () => { exited = true; });

    const deadline = Date.now() + BROWSER_START_TIMEOUT_MS;
    let connected = false;
    while (!connected && !exited && Date.now() < deadline) {
      this.signal?.throwIfAborted();
      if (/WebDriver BiDi listening/.test(stderr)) {
        try {
          this.websocket = new WebSocket(`ws://${LOOPBACK}:${debugPort}/session`);
          await new Promise((resolveOpen, rejectOpen) => {
            const timeout = setTimeout(() => rejectOpen(new Error("Firefox BiDi WebSocket connect timed out")), 5000);
            this.websocket.addEventListener("open", () => { clearTimeout(timeout); resolveOpen(); }, { once: true });
            this.websocket.addEventListener("error", () => { clearTimeout(timeout); rejectOpen(new Error("Firefox BiDi WebSocket connect failed")); }, { once: true });
          });
          connected = true;
        } catch {
          this.websocket?.close();
          this.websocket = null;
          await sleep(100);
        }
      } else {
        await sleep(100);
      }
    }
    this.signal?.throwIfAborted();
    if (!connected) throw new Error(`Firefox WebDriver BiDi did not start: ${spawnError?.message || stderr.slice(-800)}`);

    this.websocket.addEventListener("message", (event) => this.onMessage(JSON.parse(String(event.data))));
    this.websocket.addEventListener("close", () => {
      for (const { reject, timer, onAbort } of this.pending.values()) {
        clearTimeout(timer);
        this.signal?.removeEventListener("abort", onAbort);
        reject(new Error("Firefox BiDi WebSocket closed"));
      }
      this.pending.clear();
    });
    const browserSession = await this.send("session.new", { capabilities: {} });
    this.capabilities = browserSession.capabilities || {};
    await this.send("session.subscribe", { events: ["log.entryAdded", "network.fetchError"] });
    await this.send("script.addPreloadScript", { functionDeclaration: `() => {
      window.__galleryResourceErrors = [];
      window.__galleryCSP = [];
      addEventListener('error', (event) => { if (event.target !== window) window.__galleryResourceErrors.push(event.target?.src || event.target?.href || event.target?.tagName || 'resource'); }, true);
      addEventListener('securitypolicyviolation', (event) => window.__galleryCSP.push(event.violatedDirective + ' ' + event.blockedURI));
    }` });
    const tab = await this.send("browsingContext.create", { type: "tab" });
    this.context = tab.context;
  }

  onMessage(message) {
    if (message.method === "network.fetchError") {
      const url = message.params.request?.url || "";
      try { if (new URL(url).pathname.startsWith("/assets/")) this.networkFailures.push({ url, error: message.params.errorText || "" }); }
      catch { this.networkFailures.push({ url, error: "invalid asset failure URL" }); }
      return;
    }
    if (message.id && this.pending.has(message.id)) {
      const { resolve, reject, timer, onAbort } = this.pending.get(message.id);
      clearTimeout(timer);
      this.signal?.removeEventListener("abort", onAbort);
      this.pending.delete(message.id);
      if (message.type === "error") reject(new Error(`${message.error}: ${message.message}`));
      else resolve(message.result);
      return;
    }
    if (message.method === "log.entryAdded") {
      const entry = message.params;
      if (entry.level === "error" || entry.type === "javascript") {
        this.logs.push({ level: entry.level || "error", type: entry.type || "console", text: String(entry.text || "").slice(0, 1000) });
      }
    }
  }

  send(method, params = {}) {
    return new Promise((resolveCommand, rejectCommand) => {
      this.signal?.throwIfAborted();
      const id = ++this.requestId;
      const onAbort = () => {
        clearTimeout(timer);
        this.pending.delete(id);
        rejectCommand(this.signal.reason);
      };
      const timer = setTimeout(() => {
        this.pending.delete(id);
        this.signal?.removeEventListener("abort", onAbort);
        rejectCommand(new Error(`BiDi command timed out: ${method}`));
      }, COMMAND_TIMEOUT_MS);
      this.pending.set(id, { resolve: resolveCommand, reject: rejectCommand, timer, onAbort });
      this.signal?.addEventListener("abort", onAbort, { once: true });
      try {
        this.websocket.send(JSON.stringify({ id, method, params }));
      } catch (error) {
        clearTimeout(timer);
        this.signal?.removeEventListener("abort", onAbort);
        this.pending.delete(id);
        rejectCommand(error);
      }
    });
  }

  async setViewport(width, height = VIEWPORT_HEIGHT) {
    await this.send("browsingContext.setViewport", { context: this.context, viewport: { width, height } });
  }

  async navigate(url) {
    await this.send("browsingContext.navigate", { context: this.context, url, wait: "complete" });
  }

  async evaluate(expression) {
    const result = await this.send("script.evaluate", {
      expression,
      target: { context: this.context },
      awaitPromise: true,
      resultOwnership: "none",
      serializationOptions: { maxObjectDepth: 12 },
    });
    if (result.type === "exception") throw new Error(result.exceptionDetails?.text || "script.evaluate exception");
    return deserialize(result.result);
  }

  async pressTab() {
    await this.send("input.performActions", {
      context: this.context,
      actions: [{ type: "key", id: "keyboard", actions: [{ type: "keyDown", value: "\uE004" }, { type: "keyUp", value: "\uE004" }] }],
    });
    await this.send("input.releaseActions", { context: this.context });
  }

  async screenshot() {
    const result = await this.send("browsingContext.captureScreenshot", { context: this.context, origin: "document" });
    return Buffer.from(result.data, "base64");
  }

  async stop() {
    try { this.websocket?.close(); } catch { /* Already closed. */ }
    if (this.firefox && this.firefox.exitCode === null && this.firefox.signalCode === null) {
      const exited = new Promise((resolveExit) => this.firefox.once("exit", resolveExit));
      try { this.firefox.kill("SIGTERM"); } catch { /* Only this spawned process is targeted. */ }
      const timeout = new Promise((resolveTimeout) => setTimeout(() => resolveTimeout(false), 3000));
      if (!(await Promise.race([exited.then(() => true), timeout]))) {
        try { this.firefox.kill("SIGKILL"); } catch { /* Already exited. */ }
        await Promise.race([exited, sleep(1000)]);
      }
    }
    const stopped = !this.firefox?.pid || this.firefox.exitCode !== null || this.firefox.signalCode !== null;
    if (stopped) {
      try { rmSync(safeVerificationPath(this.profileDir, "owned gallery profile"), { recursive: true, force: true }); } catch { /* Cleanup failure is reflected in profileRemoved. */ }
    }
    return {
      pid: this.firefox?.pid || null,
      stopped,
      exitCode: this.firefox?.exitCode ?? null,
      signalCode: this.firefox?.signalCode ?? null,
      profileRemoved: !existsSync(this.profileDir),
    };
  }
}

function deserialize(remoteValue) {
  if (!remoteValue) return null;
  switch (remoteValue.type) {
    case "string":
    case "number":
    case "boolean": return remoteValue.value;
    case "null":
    case "undefined": return null;
    case "array": return (remoteValue.value || []).map(deserialize);
    case "object": return Object.fromEntries((remoteValue.value || []).map(([key, value]) => [typeof key === "string" ? key : deserialize(key), deserialize(value)]));
    default: return `[${remoteValue.type}]`;
  }
}

export const INSPECT_PAGE = `JSON.stringify((() => {
  const style = (element) => getComputedStyle(element);
  const describe = (element) => ({
    foreground: style(element).color,
    background: style(element).backgroundColor,
    backgrounds: (() => { const result = []; let node = element; while (node && result.length < 16) { result.push(style(node).backgroundColor); node = node.parentElement; } return result; })(),
    backgroundImages: (() => { const result = []; let node = element; while (node && result.length < 16) { result.push(style(node).backgroundImage); node = node.parentElement; } return result; })(),
    fontSize: style(element).fontSize,
    lineHeight: style(element).lineHeight,
  });
  const byData = (attribute) => Array.from(document.querySelectorAll('[' + attribute + ']'));
  const root = document.documentElement;
  const focus = document.querySelector('#focus-example');
  const statuses = byData('data-status-pill').map((element) => ({ state: element.dataset.statusPill, ...describe(element) }));
  const statusDots = byData('data-status-pill').map((element) => { const dot = element.querySelector('.cp-dot'); return { state: element.dataset.statusPill, background: dot ? style(dot).backgroundColor : '', boxShadow: dot ? style(dot).boxShadow : '', width: dot ? dot.getBoundingClientRect().width : 0, height: dot ? dot.getBoundingClientRect().height : 0 }; });
  const badges = byData('data-provider-badge').map((element) => ({ provider: element.dataset.providerBadge, ...describe(element) }));
  const legacyDots = byData('data-legacy-dot').map((element) => ({ state: element.dataset.legacyDot, background: style(element).backgroundColor, backgroundImage: style(element).backgroundImage, boxShadow: style(element).boxShadow, width: element.getBoundingClientRect().width, height: element.getBoundingClientRect().height }));
   const typeSamples = Array.from(document.querySelectorAll('.cp-page-title, .cp-card-title, .cp-card-subtitle, .cp-stat-card-label, .cp-stat-card-value, .cp-stat-card-detail, .cp-meter-label, .cp-meter-value, .cp-bar-text, .cp-status-pill, .cp-badge, .cp-chip, .cp-label, .cp-help-text, .cp-settings-nav-link, .cp-btn:not(:disabled), .cp-input, .cp-select')).map((element) => ({ selector: element.className, text: element.textContent.trim(), ...describe(element) }));
  const statCard = document.querySelector('[data-metric-placeholder]');
  const card = document.querySelector('.cp-card');
  const tracks = Array.from(document.querySelectorAll('.cp-bar-track'));
  const sparklines = Array.from(document.querySelectorAll('[data-empty-sparkline]'));
  const liveDot = document.querySelector('.cp-status-dot-live');
  const chartNames = [
    'cpu', 'mem', 'disk', 'net-rx', 'net-tx', 'disk-read', 'disk-write', 'load1', 'sparkline', 'axis', 'grid'
  ];
  const chartTokens = Object.fromEntries(chartNames.map((name) => [name, style(root).getPropertyValue('--chart-cp-' + name).trim()]));
  return {
    language: root.lang,
    dark: root.classList.contains('dark'),
    viewportWidth: window.innerWidth,
    documentWidth: root.scrollWidth,
    clientWidth: root.clientWidth,
    bodyWidth: document.body.scrollWidth,
    statuses,
    statusDots,
     badges,
     chips: byData('data-chip').map((element) => ({ state: element.dataset.chip, ...describe(element) })),
     longLabels: byData('data-long-label').map((element) => ({ text: element.textContent, width: element.clientWidth, scrollWidth: element.scrollWidth, height: element.getBoundingClientRect().height })),
     compactPadding: style(document.querySelector('[data-compact-card]')).paddingTop,
     stackedDirection: style(document.querySelector('[data-stacked-card]')).flexDirection,
     dotSizes: Array.from(document.querySelectorAll('.cp-dot')).map((element) => ({ large: element.classList.contains('cp-dot-lg'), width: element.getBoundingClientRect().width, height: element.getBoundingClientRect().height })),
     trackSizes: tracks.map((element) => ({ large: element.classList.contains('cp-bar-track-lg'), height: element.getBoundingClientRect().height })),
     controls: { inputs: document.querySelectorAll('[data-showcase-controls] input').length, selects: document.querySelectorAll('[data-showcase-controls] select').length, buttons: document.querySelectorAll('[data-showcase-controls] button').length, disabled: document.querySelectorAll('[data-showcase-controls] button:disabled').length },
    legacyDots,
    typeSamples,
    cardBackground: card ? style(card).backgroundColor : '',
    statBackground: statCard ? style(statCard).backgroundColor : '',
    tracks: tracks.map((element) => ({ background: style(element).backgroundColor, image: style(element).backgroundImage, children: element.children.length })),
    sparklineCount: sparklines.length,
    sparklineHasSVG: sparklines.some((element) => element.querySelector('svg, canvas')),
    barFillCount: document.querySelectorAll('.cp-bar-fill').length,
    valueAttributeCount: document.querySelectorAll('[aria-valuenow]').length,
    visibleTextHasNumber: /\\d/.test((document.body.innerText || '').replace(/\\bR2\\b/g, 'R-two')),
     hasAPIOrLoginForm: Boolean(document.querySelector('form, input[type=password], input:not([data-showcase-controls] input), button:not([data-showcase-controls] button)') || document.body.innerText.includes('/api/')),
    authStorageKeys: Object.keys(localStorage).filter((key) => /token|session|auth/i.test(key)),
    authSessionStorageKeys: Object.keys(sessionStorage).filter((key) => /token|session|auth/i.test(key)),
    resourceErrors: window.__galleryResourceErrors || [],
    cspViolations: window.__galleryCSP || [],
    chartTokens,
    reducedMotion: matchMedia('(prefers-reduced-motion: reduce)').matches,
    liveDotAnimationDuration: liveDot ? style(liveDot).animationDuration : '',
    liveDotAnimationIterations: liveDot ? style(liveDot).animationIterationCount : '',
    liveDotAnimationName: liveDot ? style(liveDot).animationName : '',
    focusId: focus?.id || '',
  };
})())`;

const INSPECT_FOCUS = `JSON.stringify((() => {
  const element = document.querySelector('#focus-example');
  if (!element || document.activeElement !== element) return { focused: false };
  const style = getComputedStyle(element);
  return {
    focused: true,
    outlineStyle: style.outlineStyle,
    outlineWidth: style.outlineWidth,
    outlineColor: style.outlineColor,
    boxShadow: style.boxShadow,
  };
})())`;

function parseRemoteJSON(value) {
  return JSON.parse(value);
}

export function parseColor(value) {
  if (typeof value !== "string") return null;
  const color = value.trim().toLowerCase();
  if (color === "transparent") return [0, 0, 0, 0];
  const hex = color.match(/^#([\da-f]{3,4}|[\da-f]{6}|[\da-f]{8})$/i);
  if (hex) {
    let digits = hex[1];
    if (digits.length <= 4) digits = [...digits].map((digit) => `${digit}${digit}`).join("");
    const hasAlpha = digits.length === 8;
    return [
      Number.parseInt(digits.slice(0, 2), 16),
      Number.parseInt(digits.slice(2, 4), 16),
      Number.parseInt(digits.slice(4, 6), 16),
      hasAlpha ? Number.parseInt(digits.slice(6, 8), 16) / 255 : 1,
    ];
  }
  const rgb = color.match(/^rgba?\((.*)\)$/);
  if (!rgb) return null;
  const parts = rgb[1].replaceAll(",", " ").replace("/", " ").trim().split(/\s+/);
  if (parts.length < 3 || parts.length > 4) return null;
  if (!parts.every((part) => /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?%?$/i.test(part))) return null;
  const channel = (part) => part.endsWith("%") ? Number.parseFloat(part) * 2.55 : Number.parseFloat(part);
  const alpha = (part) => part === undefined ? 1 : part.endsWith("%") ? Number.parseFloat(part) / 100 : Number.parseFloat(part);
  const result = [channel(parts[0]), channel(parts[1]), channel(parts[2]), alpha(parts[3])];
  return result.every(Number.isFinite) ? result : null;
}

export function composite(foreground, background) {
  const alpha = foreground[3] + background[3] * (1 - foreground[3]);
  if (alpha === 0) return [0, 0, 0, 0];
  return [0, 1, 2].map((index) => (foreground[index] * foreground[3] + background[index] * background[3] * (1 - foreground[3])) / alpha).concat(alpha);
}

export function resolvedBackground(backgrounds) {
  let result = [0, 0, 0, 0];
  for (const raw of [...backgrounds].reverse()) {
    const color = parseColor(raw);
    if (!color) return null;
    result = composite(color, result);
  }
  return result;
}

function luminance(color) {
  const linear = color.slice(0, 3).map((component) => {
    const value = component / 255;
    return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
  });
  return linear[0] * 0.2126 + linear[1] * 0.7152 + linear[2] * 0.0722;
}

export function contrastRatio(foreground, background) {
  const [lighter, darker] = [luminance(foreground), luminance(background)].sort((a, b) => b - a);
  return (lighter + 0.05) / (darker + 0.05);
}

function durationMs(raw) {
  const match = String(raw).trim().match(/^([\d.e+-]+)(ms|s)$/i);
  if (!match) return Number.NaN;
  const value = Number(match[1]);
  return match[2].toLowerCase() === "s" ? value * 1000 : value;
}

function addCheck(report, result, name, ok, detail = undefined) {
  const check = { name, passed: Boolean(ok) };
  if (detail !== undefined) check.detail = detail;
  result.checks.push(check);
  if (!ok) report.failures.push({ scenario: result.scenario, check: name, detail: detail ?? null });
}

export function validateScenario(report, result, page, language, theme, width) {
  const expectedDark = theme === "dark";
  addCheck(report, result, "language selected", page.language === language, { expected: language, actual: page.language });
  addCheck(report, result, "theme selected", page.dark === expectedDark, { expectedDark, actualDark: page.dark });
  addCheck(report, result, "viewport selected", page.viewportWidth === width, { expected: width, actual: page.viewportWidth });
  addCheck(report, result, "no horizontal overflow", Math.max(page.documentWidth, page.bodyWidth) <= page.clientWidth + 1, { documentWidth: page.documentWidth, bodyWidth: page.bodyWidth, clientWidth: page.clientWidth });
  addCheck(report, result, "showcase has no numeric measurement text", !page.visibleTextHasNumber);
  addCheck(report, result, "showcase has no app/API/login controls", !page.hasAPIOrLoginForm);
  addCheck(report, result, "fresh profile has no auth/session/token storage", page.authStorageKeys.length === 0 && page.authSessionStorageKeys.length === 0, { local: page.authStorageKeys, session: page.authSessionStorageKeys });
  addCheck(report, result, "no page resource or CSP errors", !page.resourceErrors?.length && !page.cspViolations?.length,
    { resourceErrors: page.resourceErrors || [], cspViolations: page.cspViolations || [] });
  addCheck(report, result, "bars and sparklines are empty placeholders", page.barFillCount === 0 && page.valueAttributeCount === 0 && page.sparklineCount === 1 && !page.sparklineHasSVG && page.tracks.length >= 1 && page.tracks.every((track) => track.children === 0), {
    barFillCount: page.barFillCount,
    valueAttributeCount: page.valueAttributeCount,
    sparklineCount: page.sparklineCount,
    sparklineHasSVG: page.sparklineHasSVG,
    tracks: page.tracks,
  });
  addCheck(report, result, "base/stat card surfaces are visible", Boolean(parseColor(page.cardBackground)?.[3] > 0 && parseColor(page.statBackground)?.[3] > 0), {
    cardBackground: page.cardBackground,
    statBackground: page.statBackground,
  });

   const requiredStates = new Set(["online", "offline", "solid-offline", "warning", "critical", "neutral"]);
  const presentStates = new Set(page.statuses.map((entry) => entry.state));
  addCheck(report, result, "status pill state examples present", [...requiredStates].every((state) => presentStates.has(state)), [...presentStates]);
  for (const status of page.statuses) {
    const foreground = parseColor(status.foreground);
    const ownBackground = parseColor(status.background);
    const background = resolvedBackground(status.backgrounds);
    const ratio = textContrast(status, foreground, background);
    addCheck(report, result, `status ${status.state} has a visible background`, Boolean(ownBackground && ownBackground[3] > 0), status.background);
    addCheck(report, result, `status ${status.state} foreground/background AA`, Number.isFinite(ratio) && ratio >= 4.5, {
      ratio: Number.isFinite(ratio) ? Number(ratio.toFixed(3)) : null,
      required: 4.5,
      foreground: status.foreground,
      backgrounds: status.backgrounds,
    });
  }

  for (const dot of page.statusDots) {
    const color = parseColor(dot.background);
    const hollowNeutral = dot.state === "neutral" && dot.boxShadow && dot.boxShadow !== "none";
    addCheck(report, result, `status ${dot.state} dot has visible fill or neutral ring`, Boolean(dot.width > 0 && dot.height > 0 && ((color && color[3] > 0) || hollowNeutral)), dot);
  }
  addCheck(report, result, "every status pill has a dot example", page.statusDots.length === page.statuses.length && [...requiredStates].every((state) => page.statusDots.some((dot) => dot.state === state)));

  const legacyStates = new Set(page.legacyDots.map((entry) => entry.state));
  addCheck(report, result, "legacy up/down dots present", legacyStates.has("up") && legacyStates.has("down"), [...legacyStates]);
  for (const dot of page.legacyDots) {
    const color = parseColor(dot.background);
    addCheck(report, result, `legacy ${dot.state} dot has visible fill`, Boolean(color && color[3] > 0 && dot.width > 0 && dot.height > 0), {
      background: dot.background,
      backgroundImage: dot.backgroundImage,
      boxShadow: dot.boxShadow,
    });
  }

  for (const badge of page.badges) {
    const foreground = parseColor(badge.foreground);
    const ownBackground = parseColor(badge.background);
    const background = resolvedBackground(badge.backgrounds);
    const ratio = textContrast(badge, foreground, background);
    addCheck(report, result, `provider ${badge.provider} has a visible background`, Boolean(ownBackground && ownBackground[3] > 0), badge.background);
    addCheck(report, result, `provider ${badge.provider} badge AA`, Number.isFinite(ratio) && ratio >= 4.5, {
      ratio: Number.isFinite(ratio) ? Number(ratio.toFixed(3)) : null,
      required: 4.5,
      foreground: badge.foreground,
      backgrounds: badge.backgrounds,
    });
  }
   addCheck(report, result, "provider badges present", ["aws", "oci", "other", "r2", "hub", "update"].every((provider) => page.badges.some((badge) => badge.provider === provider)), page.badges.map((badge) => badge.provider));

   addCheck(report, result, "all legacy chip states present", ["ok", "warning", "critical", "exceeded", "neutral"].every((state) => page.chips.some((chip) => chip.state === state)));
   addCheck(report, result, "compact card keeps compact padding at every width", Number.parseFloat(page.compactPadding) === 14, page.compactPadding);
   addCheck(report, result, "stacked card is opt-in vertical composition", page.stackedDirection === "column", page.stackedDirection);
   addCheck(report, result, "long labels wrap without clipping", page.longLabels.length >= 3 && page.longLabels.every((label) => label.height > 0 && label.scrollWidth <= label.width + 1), page.longLabels);
   addCheck(report, result, "default and large status dots retain size", page.dotSizes.some((dot) => dot.large) && page.dotSizes.every((dot) => dot.width === (dot.large ? 12 : 10) && dot.height === dot.width), page.dotSizes);
   addCheck(report, result, "default and large empty tracks retain size", page.trackSizes.some((track) => track.large) && page.trackSizes.some((track) => !track.large) && page.trackSizes.every((track) => track.height === (track.large ? 8 : 6)), page.trackSizes);
   addCheck(report, result, "isolated form controls and disabled state present", page.controls.inputs === 2 && page.controls.selects === 1 && page.controls.buttons === 4 && page.controls.disabled === 1, page.controls);

  for (const sample of page.typeSamples) {
    const fontSize = Number.parseFloat(sample.fontSize);
    const lineHeight = Number.parseFloat(sample.lineHeight);
    // Micro/chip explicitly pair with the documented `normal` token; normal
    // is not evidence of missing leading for these historical badge sizes.
    const normalMicro = /\bcp-badge\b|\bcp-chip\b/.test(sample.selector) && sample.lineHeight === "normal";
    addCheck(report, result, `typography size/leading ${sample.selector}`, Number.isFinite(fontSize) && fontSize > 0 && ((Number.isFinite(lineHeight) && lineHeight > 0) || normalMicro), {
      fontSize: sample.fontSize,
      lineHeight: sample.lineHeight,
      text: sample.text,
    });
    const foreground = parseColor(sample.foreground);
    const ratio = textContrast(sample, foreground, resolvedBackground(sample.backgrounds));
    addCheck(report, result, `typography AA ${sample.selector}`, Number.isFinite(ratio) && ratio >= 4.5, { ratio: Number.isFinite(ratio) ? Number(ratio.toFixed(3)) : null, foreground: sample.foreground, backgrounds: sample.backgrounds });
  }

  const chartTokens = Object.entries(page.chartTokens);
  const seriesTokens = chartTokens.filter(([token]) => !["axis", "grid"].includes(token));
  const invalidChartTokens = chartTokens.filter(([, value]) => !parseColor(value) || parseColor(value)[3] <= 0);
  addCheck(report, result, "chart color tokens resolve to real colors", invalidChartTokens.length === 0 && seriesTokens.length === 9, {
    values: page.chartTokens,
    invalid: invalidChartTokens.map(([token]) => token),
  });
  const chartSurface = parseColor(page.cardBackground);
  for (const [token, raw] of seriesTokens) {
    const color = parseColor(raw);
    const ratio = color && chartSurface?.[3] >= 0.999 ? contrastRatio(composite(color, chartSurface), chartSurface) : Number.NaN;
    addCheck(report, result, `chart ${token} token non-text contrast`, Number.isFinite(ratio) && ratio >= 3, { ratio: Number.isFinite(ratio) ? Number(ratio.toFixed(3)) : null, required: 3, color: raw, surface: page.cardBackground });
  }
  result.themeEvidence = {
    requestedTheme: theme,
    axis: page.chartTokens.axis,
    grid: page.chartTokens.grid,
    series: Object.fromEntries(seriesTokens),
  };
}

export function textContrast(sample, foreground, background) {
  if (!foreground || foreground[3] <= 0 || !background || background[3] < 0.999 || sample.backgroundImages?.some((image) => image !== "none")) return Number.NaN;
  return contrastRatio(composite(foreground, background), background);
}

async function verifyKeyboardFocus(session, report, result) {
  const baseline = parseRemoteJSON(await session.evaluate(`JSON.stringify({ boxShadow: getComputedStyle(document.querySelector('#focus-example')).boxShadow })`));
  await session.pressTab();
  const focus = parseRemoteJSON(await session.evaluate(INSPECT_FOCUS));
  const width = Number.parseFloat(focus.outlineWidth || "0");
  const visibleOutline = focus.outlineStyle && focus.outlineStyle !== "none" && width > 0 && parseColor(focus.outlineColor)?.[3] > 0;
  const visibleShadow = focus.boxShadow && focus.boxShadow !== "none" && focus.boxShadow !== baseline.boxShadow;
  addCheck(report, result, "keyboard focus reaches showcase link with visible indicator", Boolean(focus.focused && (visibleOutline || visibleShadow)), focus);
}

async function verifySelectedHover(session, report, result) {
  const inspect = `JSON.stringify((() => {
    const element = document.querySelector('#selected-navigation');
    element.scrollIntoView({ block: 'center' });
    const rect = element.getBoundingClientRect();
    const css = getComputedStyle(element);
    return { x: Math.round(rect.x + rect.width / 2), y: Math.round(rect.y + rect.height / 2), foreground: css.color, background: css.backgroundColor };
  })())`;
  const before = parseRemoteJSON(await session.evaluate(inspect));
  await session.send("input.performActions", {
    context: session.context,
    actions: [{ type: "pointer", id: "mouse", parameters: { pointerType: "mouse" }, actions: [{ type: "pointerMove", x: before.x, y: before.y, duration: 0 }] }],
  });
  await sleep(180);
  const after = parseRemoteJSON(await session.evaluate(`JSON.stringify((() => { const element = document.querySelector('#selected-navigation'); const css = getComputedStyle(element); return { hovered: element.matches(':hover'), foreground: css.color, background: css.backgroundColor }; })())`));
  addCheck(report, result, "selected settings remains selected on actual pointer hover", after.hovered && before.foreground === after.foreground && before.background === after.background, { before, after });
  await session.send("input.releaseActions", { context: session.context });
  await session.evaluate("window.scrollTo(0, 0)");
}

async function verifyControlFocus(session, report, result) {
  const count = await session.evaluate("document.querySelectorAll('[data-showcase-controls] input, [data-showcase-controls] select, [data-showcase-controls] button:not(:disabled)').length");
  await session.evaluate("document.querySelector('#focus-example').focus()");
  for (let index = 0; index < count; index++) {
    await session.pressTab();
    const focus = parseRemoteJSON(await session.evaluate(`JSON.stringify((() => {
      const element = document.activeElement;
      const css = getComputedStyle(element);
      const rect = element.getBoundingClientRect();
      return { tag: element.tagName, inControls: Boolean(element.closest('[data-showcase-controls]')), disabled: element.disabled, width: rect.width, height: rect.height, outlineStyle: css.outlineStyle, outlineWidth: css.outlineWidth, outlineColor: css.outlineColor, surface: getComputedStyle(element.closest('.cp-card')).backgroundColor };
    })())`));
    const foreground = parseColor(focus.outlineColor);
    const background = parseColor(focus.surface);
    const ratio = foreground && background ? contrastRatio(foreground, background) : Number.NaN;
    addCheck(report, result, `control keyboard focus ${index}`, focus.inControls && !focus.disabled && focus.width >= 24 && focus.height >= 24 && focus.outlineStyle !== "none" && Number.parseFloat(focus.outlineWidth) >= 2 && ratio >= 3, { ...focus, contrast: ratio });
  }
  await session.evaluate("document.activeElement.blur(); document.querySelector('#focus-example').focus(); window.scrollTo(0, 0)");
}

async function verifyReducedMotion(session, report, url) {
  await session.setViewport(360, VIEWPORT_HEIGHT);
  await session.navigate(`${url}?lang=en`);
  await session.evaluate(`localStorage.setItem("cp_theme", "light")`);
  await session.navigate(`${url}?lang=en`);
  const page = parseRemoteJSON(await session.evaluate(INSPECT_PAGE));
  const duration = durationMs(page.liveDotAnimationDuration);
  const result = { scenario: "reduced-motion/en/light/360", checks: [], themeEvidence: { liveDotAnimationDuration: page.liveDotAnimationDuration, iterationCount: page.liveDotAnimationIterations } };
  addCheck(report, result, "browser reduced-motion preference is active", page.reducedMotion, page.reducedMotion);
  addCheck(report, result, "live status pulse is disabled or reduced to a single near-zero iteration", page.liveDotAnimationName === "none" || (Number.isFinite(duration) && duration <= 0.01 && Number(page.liveDotAnimationIterations) <= 1), {
    durationMs: Number.isFinite(duration) ? duration : page.liveDotAnimationDuration,
    iterationCount: page.liveDotAnimationIterations,
    animationName: page.liveDotAnimationName,
  });
  report.results.push(result);
}

async function run(options) {
  const { root: artifactRoot, runDir, reportPath } = prepareGalleryArtifacts(options);
  const cssSHA256 = () => createHash("sha256").update(readFileSync(join(ASSETS_DIR, "app.css"))).digest("hex");
  const initialCSS = cssSHA256();
  const report = {
    schemaVersion: 1,
    status: "running",
    startedAt: new Date().toISOString(),
    finishedAt: null,
    server: { host: options.host, port: options.port, showcasePath: "/__showcase", servesAPI: false },
    browser: { executable: process.env.FIREFOX || "firefox", headless: true, profileIsolated: true, credentialsUsed: false },
    matrix: { viewports: VIEWPORTS, languages: LANGUAGES, themes: THEMES, reducedMotion: true },
    artifacts: [],
    artifactRoot,
    results: [],
    failures: [],
    verificationScope: "Isolated, data-free component showcase using actual checked-out assets; no app routes, login, API data, or authenticated dashboard render verified.",
    timeoutMs: RUN_TIMEOUT_MS,
    assetConsistency: { initialCSS, finalCSS: null },
    cleanup: { serverClosed: false, ownedBrowsers: [] },
  };
  const { server, traffic } = createShowcaseServer(options.port);
  let serverStarted = false;
  const sessions = [];
  const controller = new AbortController();
  const onInterrupt = () => controller.abort(new Error("design-system checker interrupted"));
  const timeout = setTimeout(() => controller.abort(new Error(`design-system checker exceeded ${RUN_TIMEOUT_MS}ms`)), RUN_TIMEOUT_MS);
  process.once("SIGINT", onInterrupt);
  process.once("SIGTERM", onInterrupt);

  const writeReport = () => {
    report.finishedAt = new Date().toISOString();
    report.status = report.failures.length === 0 ? "passed" : "failed";
    writeFileSync(safeVerificationPath(reportPath), `${JSON.stringify(report, null, 2)}\n`, { mode: 0o600 });
  };

  try {
    await listen(server, options.port);
    serverStarted = true;
    report.server.url = `http://${options.host}:${options.port}`;

    const profileDir = mkdtempSync(join(runDir, "firefox-profile-"));
    const session = new BidiSession(profileDir, { signal: controller.signal, artifactRoot });
    sessions.push(session);
    await session.start();
    report.browser.name = session.capabilities.browserName || "firefox";
    report.browser.version = session.capabilities.browserVersion || "unknown";
    report.browser.platform = session.capabilities.platformName || "unknown";
    const showcaseURL = `${report.server.url}/__showcase`;

    for (const language of LANGUAGES) {
      for (const theme of THEMES) {
        for (const width of VIEWPORTS) {
          const scenario = `${language}/${theme}/${width}`;
          const result = { scenario, checks: [], themeEvidence: null, consoleErrors: [] };
          report.results.push(result);
          result.cssSHA256 = { before: cssSHA256(), after: null };
          addCheck(report, result, "CSS digest matches initial asset before scenario", result.cssSHA256.before === initialCSS, result.cssSHA256.before);
          await session.setViewport(width);
          await session.navigate(`${showcaseURL}?lang=${language}`);
          await session.evaluate(`localStorage.setItem("cp_theme", ${JSON.stringify(theme)})`);
          await session.navigate(`${showcaseURL}?lang=${language}`);
          const page = parseRemoteJSON(await session.evaluate(INSPECT_PAGE));
          validateScenario(report, result, page, language, theme, width);
           await verifyKeyboardFocus(session, report, result);
           await verifyControlFocus(session, report, result);
           await verifySelectedHover(session, report, result);
          result.consoleErrors = session.logs.splice(0);
          addCheck(report, result, "no browser console errors", result.consoleErrors.length === 0, result.consoleErrors);

          const imageName = `showcase-${language}-${theme}-${width}.png`;
          const imagePath = join(runDir, imageName);
          writeFileSync(safeVerificationPath(imagePath), await session.screenshot(), { mode: 0o600 });
          report.artifacts.push({ type: "screenshot", path: imagePath, scenario });
          result.cssSHA256.after = cssSHA256();
          addCheck(report, result, "CSS digest matches initial asset after scenario", result.cssSHA256.after === initialCSS, result.cssSHA256.after);
        }
      }
    }

    const reducedProfileDir = mkdtempSync(join(runDir, "firefox-reduced-profile-"));
    const reducedSession = new BidiSession(reducedProfileDir, { reducedMotion: true, signal: controller.signal, artifactRoot });
    sessions.push(reducedSession);
    await reducedSession.start();
    const reducedCSSBefore = cssSHA256();
    await verifyReducedMotion(reducedSession, report, showcaseURL);
    const reducedImagePath = join(runDir, "showcase-reduced-motion.png");
    writeFileSync(safeVerificationPath(reducedImagePath), await reducedSession.screenshot(), { mode: 0o600 });
    report.artifacts.push({ type: "screenshot", path: reducedImagePath, scenario: "reduced-motion/en/light/360" });
    const reducedResult = report.results.find((result) => result.scenario === "reduced-motion/en/light/360");
    reducedResult.cssSHA256 = { before: reducedCSSBefore, after: cssSHA256() };
    addCheck(report, reducedResult, "CSS digest matches initial asset throughout reduced-motion scenario", reducedResult.cssSHA256.before === initialCSS && reducedResult.cssSHA256.after === initialCSS, reducedResult.cssSHA256);

    const isolationResult = { scenario: "server-isolation", checks: [] };
    addCheck(report, isolationResult, "no API/auth endpoints were requested", traffic.apiRequests.length === 0, traffic.apiRequests);
    addCheck(report, isolationResult, "assets did not change during the browser matrix", [...traffic.assetHashes.values()].every((hashes) => hashes.size === 1));
    report.results.push(isolationResult);
    report.server.servedAssetPaths = [...new Set(traffic.servedAssets)].sort();
    report.server.apiRequests = traffic.apiRequests;
    report.server.failedAssets = traffic.failedAssets;
    report.server.assetSHA256 = Object.fromEntries([...traffic.assetHashes].map(([path, hashes]) => [path, [...hashes]]));
    report.browser.consoleErrors = [...session.logs, ...reducedSession.logs];
    addCheck(report, isolationResult, "no remaining browser console errors", report.browser.consoleErrors.length === 0, report.browser.consoleErrors);

    const light = report.results.find((result) => result.scenario === "en/light/360")?.themeEvidence;
    const dark = report.results.find((result) => result.scenario === "en/dark/360")?.themeEvidence;
    if (light && dark) {
      report.chartThemeComparison = {
        axisChanged: light.axis !== dark.axis,
        gridChanged: light.grid !== dark.grid,
        seriesChanged: Object.fromEntries(Object.keys(light.series).map((series) => [series, light.series[series] !== dark.series[series]])),
      };
      addCheck(report, isolationResult, "chart axis/grid tokens change with theme", report.chartThemeComparison.axisChanged && report.chartThemeComparison.gridChanged, report.chartThemeComparison);
    }
  } catch (error) {
    report.failures.push({ scenario: "harness", check: "browser run completed", detail: String(error?.stack || error) });
  } finally {
    for (const session of sessions.reverse()) report.cleanup.ownedBrowsers.push(await session.stop());
    if (serverStarted) {
      server.closeAllConnections();
      await new Promise((resolveClose) => server.close(() => resolveClose()));
    }
    report.cleanup.serverClosed = !server.listening;
    report.server.servedAssetPaths = [...new Set(traffic.servedAssets)].sort();
    report.server.apiRequests = traffic.apiRequests;
    report.server.assetSHA256 = Object.fromEntries([...traffic.assetHashes].map(([path, hashes]) => [path, [...hashes]]));
    report.server.failedAssets = traffic.failedAssets;
    report.assetConsistency.finalCSS = cssSHA256();
    const finalization = { scenario: "run-finalization", checks: [] };
    const servedCSS = traffic.assetHashes.get("app.css");
    addCheck(report, finalization, "CSS digest unchanged through run end and every served response", report.assetConsistency.finalCSS === initialCSS && servedCSS?.size === 1 && servedCSS.has(initialCSS), report.assetConsistency);
    addCheck(report, finalization, "own static server is closed", report.cleanup.serverClosed);
    addCheck(report, finalization, "only spawned browser processes stopped and own profiles removed", report.cleanup.ownedBrowsers.every((browser) => browser.stopped && browser.profileRemoved), report.cleanup.ownedBrowsers);
    addCheck(report, finalization, "no API/auth requests through run end", traffic.apiRequests.length === 0, traffic.apiRequests);
    validateTraffic(report, finalization, traffic, sessions);
    report.results.push(finalization);
    report.totals = {
      cases: report.results.length,
      checks: report.results.reduce((sum, result) => sum + result.checks.length, 0),
      passedChecks: report.results.reduce((sum, result) => sum + result.checks.filter((check) => check.passed).length, 0),
      failedChecks: report.results.reduce((sum, result) => sum + result.checks.filter((check) => !check.passed).length, 0),
      failures: report.failures.length,
      screenshots: report.artifacts.length,
      apiAuthRequests: traffic.apiRequests.length,
    };
    clearTimeout(timeout);
    process.removeListener("SIGINT", onInterrupt);
    process.removeListener("SIGTERM", onInterrupt);
    writeReport();
  }

  process.stdout.write(`Design-system check ${report.status}.\nReport: ${reportPath}\nScreenshots: ${runDir}\n`);
  if (report.failures.length) {
    for (const failure of report.failures) {
      process.stderr.write(`FAIL ${failure.scenario} / ${failure.check}: ${JSON.stringify(failure.detail)}\n`);
    }
    process.exitCode = 1;
  }
}

const isMain = process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (isMain) {
  try {
    const options = parseArgs(process.argv.slice(2));
    if (options) await run(options);
  } catch (error) {
    process.stderr.write(`check-design-system.mjs: ${error?.message || error}\n`);
    process.exitCode = 1;
  }
}
