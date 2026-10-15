#!/usr/bin/env node
// Focused, data-safe browser smoke for the real login route. The isolated
// Phase 1 runner owns the hub lifecycle; this tool only drives Firefox via
// the same zero-dependency WebDriver BiDi approach as ui-audit.mjs.
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { safeVerificationPath, verificationEnvironment, ROUND28_ROOT } from "./verify-round28-safety.mjs";
import { parseColor } from "./check-design-system.mjs";
import { firefoxMotionPreference } from './verify-round28-runtime-lib.mjs';

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = realpathSync(resolve(SCRIPT_DIR, ".."));
const VERIFY_ROOT = resolve(REPO_ROOT, ".verification");
const START_TIMEOUT_MS = 45_000;
const COMMAND_TIMEOUT_MS = 20_000;
const sleep = (ms) => new Promise((resolveSleep) => setTimeout(resolveSleep, ms));

function usage() {
  process.stdout.write(
    "usage: node scripts/verify-phase1-auth.mjs --hub http://127.0.0.1:18190 --password-file FILE --out DIR\n" +
      "This check is restricted to loopback :18190 and writes only beneath .verification/.\n",
  );
}

function parseArgs(argv) {
  const options = { hub: null, passwordFile: null, out: null };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === "--help" || arg === "-h") return null;
    if (!["--hub", "--password-file", "--out"].includes(arg)) throw new Error(`unknown argument: ${arg}`);
    const value = argv[++i];
    if (!value || value.startsWith("--")) throw new Error(`${arg} requires a value`);
    options[{ "--hub": "hub", "--password-file": "passwordFile", "--out": "out" }[arg]] = value;
  }
  for (const key of ["hub", "passwordFile", "out"]) if (!options[key]) throw new Error(`--${key === "passwordFile" ? "password-file" : key} is required`);
  const hub = new URL(options.hub);
  if (hub.protocol !== "http:" || hub.hostname !== "127.0.0.1" || hub.port !== "18190" || hub.pathname !== "/" || hub.username || hub.password || hub.search || hub.hash) {
    throw new Error("refusing hub URL; only http://127.0.0.1:18190/ is accepted");
  }
  options.hub = hub.origin;
  return options;
}

function localPath(path, label) {
  return safeVerificationPath(path, label);
}

const freePort = () => new Promise((resolvePort, rejectPort) => {
  const server = createServer();
  server.once("error", rejectPort);
  server.listen(0, "127.0.0.1", () => {
    const { port } = server.address();
    server.close(() => resolvePort(port));
  });
});

export class BidiSession {
  constructor({ reducedMotion } = {}) {
    if(reducedMotion!==undefined)firefoxMotionPreference(reducedMotion);
    this.reducedMotion=reducedMotion;
    this.firefox = null;
    this.websocket = null;
    this.context = null;
    this.profile = null;
    this.nextId = 0;
    this.pending = new Map();
    this.consoleErrors = [];
    this.javascriptErrors = [];
    this.networkFailures = [];
    this.capabilities = {};
    this.stopPromise = null;
  }

  async start() {
    const env = verificationEnvironment(ROUND28_ROOT);
    this.profile = mkdtempSync(join(env.TMPDIR, "phase1-auth-firefox-"));
    if(this.reducedMotion!==undefined)writeFileSync(safeVerificationPath(join(this.profile,'user.js')),firefoxMotionPreference(this.reducedMotion),{mode:0o600});
    const port = await freePort();
    this.firefox = spawn(process.env.FIREFOX || "firefox", [
      "--headless", "--no-remote", "--profile", this.profile,
      `--remote-debugging-port=${port}`,
    ], { stdio: ["ignore", "ignore", "pipe"], env });

    let stderr = "";
    this.firefox.stderr.on("data", (chunk) => { stderr = (stderr + chunk.toString()).slice(-16_000); });
    let exited = false;
    let spawnError = null;
    this.firefox.once("error", (error) => { spawnError = error; exited = true; });
    this.firefox.once("exit", () => { exited = true; });
    let websocket = null;
    const deadline = Date.now() + START_TIMEOUT_MS;
    while (!websocket && !exited && Date.now() < deadline) {
      if (/WebDriver BiDi listening/.test(stderr)) {
        websocket = new WebSocket(`ws://127.0.0.1:${port}/session`);
        this.websocket = websocket;
        await new Promise((resolveOpen, rejectOpen) => {
          const timer = setTimeout(() => rejectOpen(new Error("Firefox BiDi WebSocket connect timed out")), 5000);
          websocket.addEventListener("open", () => { clearTimeout(timer); resolveOpen(); }, { once: true });
          websocket.addEventListener("error", () => { clearTimeout(timer); rejectOpen(new Error("Firefox BiDi WebSocket connect failed")); }, { once: true });
        });
      } else {
        await sleep(100);
      }
    }
    if (!websocket) throw new Error(`Firefox BiDi did not start: ${spawnError?.message || stderr.slice(-800)}`);
    this.websocket = websocket;
    websocket.addEventListener("message", (event) => this.onMessage(JSON.parse(event.data)));
    websocket.addEventListener("close", () => {
      for (const { reject, timer } of this.pending.values()) { clearTimeout(timer); reject(new Error("Firefox BiDi socket closed")); }
      this.pending.clear();
    });
    this.capabilities = (await this.send("session.new", { capabilities: {} })).capabilities || {};
    await this.send("session.subscribe", { events: ["log.entryAdded", "network.responseCompleted", "network.fetchError"] });
    await this.send("script.addPreloadScript", { functionDeclaration: `() => {
      window.__verificationResourceErrors = [];
      window.__verificationCSP = [];
      addEventListener('error', (event) => { if (event.target !== window) window.__verificationResourceErrors.push(event.target?.src || event.target?.href || event.target?.tagName || 'resource'); }, true);
      addEventListener('securitypolicyviolation', (event) => window.__verificationCSP.push(event.violatedDirective + ' ' + event.blockedURI));
    }` });
    this.context = (await this.send("browsingContext.create", { type: "tab" })).context;
  }

  onMessage(message) {
    if (message.method === "network.fetchError" || (message.method === "network.responseCompleted" && message.params.response?.status >= 400)) {
      const url = message.params.request?.url || "";
      // Authentication and absent-inventory 4xx responses are intentional in
      // contract suites; asset failures are never expected or suppressed.
      let isAsset = false;
      try { isAsset = new URL(url).pathname.startsWith("/assets/"); } catch { /* Malformed network URLs still fail on fetchError. */ }
      if (isAsset) {
        this.networkFailures.push({ url, status: message.params.response?.status ?? null, error: message.params.errorText || "" });
      }
      return;
    }
    if (message.id && this.pending.has(message.id)) {
      const pending = this.pending.get(message.id);
      clearTimeout(pending.timer);
      this.pending.delete(message.id);
      if (message.type === "error") pending.reject(new Error(`${message.error}: ${message.message}`));
      else pending.resolve(message.result);
      return;
    }
    if (message.method === "log.entryAdded") {
      const entry = message.params;
      if (entry.type === "javascript") {
        const frame = entry.stackTrace?.callFrames?.[0];
        this.javascriptErrors.push({ text: entry.text, url: frame?.url || "", line: frame?.lineNumber ?? null });
      } else if (entry.level === "error") {
        this.consoleErrors.push({ text: entry.text, type: entry.type || "console" });
      }
    }
  }

  send(method, params = {}) {
    const id = ++this.nextId;
    return new Promise((resolveCommand, rejectCommand) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        rejectCommand(new Error(`BiDi command timed out: ${method}`));
      }, COMMAND_TIMEOUT_MS);
      this.pending.set(id, { resolve: resolveCommand, reject: rejectCommand, timer });
      try { this.websocket.send(JSON.stringify({ id, method, params })); }
      catch (error) { clearTimeout(timer); this.pending.delete(id); rejectCommand(error); }
    });
  }

  async evaluate(expression) {
    const result = await this.send("script.evaluate", {
      expression,
      target: { context: this.context },
      awaitPromise: true,
      resultOwnership: "none",
      serializationOptions: { maxObjectDepth: 8 },
    });
    if (result.type === "exception") throw new Error(result.exceptionDetails?.text || "page evaluation failed");
    return deserialize(result.result);
  }

  async evaluateJSON(expression) {
    const raw = await this.evaluate(`(async () => JSON.stringify(await (${expression})))()`);
    if (typeof raw !== "string") throw new Error("page did not return JSON-serializable data");
    return JSON.parse(raw);
  }

  async navigate(url) {
    await this.send("browsingContext.navigate", { context: this.context, url, wait: "complete" });
  }

  async setViewport(width, height) {
    await this.send("browsingContext.setViewport", { context: this.context, viewport: { width, height } });
  }

  async pressTab() {
    await this.pressKey("\uE004");
  }

  async pressKey(value) {
    await this.send("input.performActions", {
      context: this.context,
      actions: [{ type: "key", id: "keyboard", actions: [{ type: "keyDown", value }, { type: "keyUp", value }] }],
    });
    await this.send("input.releaseActions", { context: this.context });
  }

  async screenshot(path, origin = "viewport") {
    const shot = await this.send("browsingContext.captureScreenshot", { context: this.context, origin });
    writeFileSync(safeVerificationPath(path, "screenshot"), Buffer.from(shot.data, "base64"), { mode: 0o600 });
  }

  stop() {
    if (this.stopPromise) return this.stopPromise;
    this.stopPromise = (async () => {
      try { this.websocket?.close(); } catch { /* already closed */ }
      if (this.firefox && this.firefox.exitCode === null && this.firefox.signalCode === null) {
        const exited = new Promise((resolveExit) => this.firefox.once("exit", resolveExit));
        try { this.firefox.kill("SIGTERM"); } catch { /* already exited */ }
        await Promise.race([exited, sleep(4000)]);
        if (this.firefox.exitCode === null && this.firefox.signalCode === null) {
          try { this.firefox.kill("SIGKILL"); } catch { /* already exited */ }
          await Promise.race([exited, sleep(1000)]);
        }
      }
      if (this.firefox?.pid && this.firefox.exitCode === null && this.firefox.signalCode === null) throw new Error("owned Firefox did not stop; profile retained");
      if (this.profile) rmSync(safeVerificationPath(this.profile, "owned profile"), { recursive: true, force: true });
    })();
    return this.stopPromise;
  }
}

function deserialize(value) {
  if (!value) return null;
  if (value.type === "string" || value.type === "number" || value.type === "boolean") return value.value;
  if (value.type === "null" || value.type === "undefined") return null;
  if (value.type === "array") return value.value.map(deserialize);
  if (value.type === "object") return Object.fromEntries(value.value.map(([key, val]) => [key, deserialize(val)]));
  return value.value ?? null;
}

async function waitFor(session, expression, label, timeoutMs = 15_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try { if (await session.evaluate(expression)) return; }
    catch { /* A navigation can temporarily destroy the evaluation realm. */ }
    await sleep(100);
  }
  throw new Error(`timed out waiting for ${label}`);
}

async function setInputAndSubmit(session, password) {
  const result = await session.evaluateJSON(`(() => {
    const username = document.getElementById("cp-login-username");
    const passwordInput = document.getElementById("cp-login-password");
    const form = username?.closest("form");
    if (!username || !passwordInput || !form) return { ok: false, reason: "login form fields missing" };
    username.value = "admin";
    passwordInput.value = ${JSON.stringify(password)};
    username.dispatchEvent(new Event("input", { bubbles: true }));
    passwordInput.dispatchEvent(new Event("input", { bubbles: true }));
    form.requestSubmit();
    return { ok: true };
  })()`);
  if (!result.ok) throw new Error(result.reason);
}

async function main() {
  const options = parseArgs(process.argv.slice(2));
  if (!options) { usage(); return 0; }
  const passwordPath = localPath(options.passwordFile, "password file");
  const outDir = localPath(options.out, "output directory");
  if (!existsSync(passwordPath)) throw new Error(`password file does not exist: ${passwordPath}`);
  if ((statSync(passwordPath).mode & 0o077) !== 0) throw new Error("password file must be mode 0600 or stricter");
  const password = readFileSync(passwordPath, "utf8").replace(/\r?\n$/, "");
  if (!password) throw new Error("password file is empty");
  const expectedVersionMetadata = {
    version: process.env.CP_VERIFY_EXPECTED_VERSION || "",
    commit: process.env.CP_VERIFY_EXPECTED_COMMIT || "",
    date: process.env.CP_VERIFY_EXPECTED_DATE || "",
  };
  const requireVersionMetadata = Object.values(expectedVersionMetadata).some(Boolean);
  if (requireVersionMetadata && Object.values(expectedVersionMetadata).some((value) => !value)) {
    throw new Error("expected version, commit and date must all be supplied for provenance verification");
  }
  mkdirSync(outDir, { recursive: true, mode: 0o700 });
  const realOutDir = realpathSync(outDir);
  if (realOutDir !== VERIFY_ROOT && !realOutDir.startsWith(`${VERIFY_ROOT}/`)) throw new Error("resolved output directory escaped .verification");

  const report = {
    generatedAt: new Date().toISOString(),
    hub: options.hub,
    requestedPath: "/login",
    checks: [],
    consoleErrors: [],
    javascriptErrors: [],
    screenshots: [],
    matrix: [],
    networkFailures: [],
    profileRemoved: false,
  };
  const check = (name, pass, detail = undefined) => {
    report.checks.push({ name, pass: Boolean(pass), ...(detail === undefined ? {} : { detail }) });
    if (!pass) throw new Error(`check failed: ${name}`);
  };
  let session = null;
  let failure = null;

  try {
    session = new BidiSession();
    activeSession = session;
    await session.start();
    report.browser = session.capabilities;
    await session.setViewport(360, 800);
    await session.navigate(`${options.hub}/login`);
    await waitFor(session, `Boolean(document.getElementById("cp-login-username") && document.getElementById("cp-login-password"))`, "login form");

    const initialURL = await session.evaluateJSON(`({href: location.href, pathname: location.pathname, hash: location.hash})`);
    check("literal /login navigation normalizes to the app login form", initialURL.pathname === "/" && initialURL.hash === "#/login", { requestedURL: `${options.hub}/login`, ...initialURL });

    for (const lang of ["en", "ko"]) {
    const catalog = JSON.parse(readFileSync(join(REPO_ROOT, "web/assets/i18n", `${lang}.json`), "utf8"));
    for (const theme of ["dark", "light"]) {
      for (const [width, height] of [[360, 800], [768, 1024], [1440, 900]]) {
        await session.setViewport(width, height);
        await session.evaluate(`localStorage.setItem("cp_theme", ${JSON.stringify(theme)}); localStorage.setItem("cp_lang", ${JSON.stringify(lang)}); true`);
        await session.navigate(`${options.hub}/login?cp_verify=${lang}-${theme}-${width}`);
        await waitFor(session, `document.documentElement.lang === ${JSON.stringify(lang)} && document.documentElement.classList.contains("dark") === ${theme === "dark"} && Boolean(document.getElementById("cp-login-username") && document.getElementById("cp-login-password"))`, "localized login form after navigation");
        const state = await session.evaluateJSON(`(() => {
          const username = document.getElementById("cp-login-username");
          const passwordInput = document.getElementById("cp-login-password");
          const usernameLabel = document.querySelector('label[for="cp-login-username"]');
          const passwordLabel = document.querySelector('label[for="cp-login-password"]');
          const submit = username?.closest("form")?.querySelector('button[type="submit"]');
          const rect = (node) => { const r = node.getBoundingClientRect(); return {width: r.width, height: r.height}; };
          return {
            lang: document.documentElement.lang,
            theme: localStorage.getItem("cp_theme"),
            dark: document.documentElement.classList.contains("dark"),
            pathname: location.pathname,
            hash: location.hash,
            resourceErrors: window.__verificationResourceErrors || [],
            cspViolations: window.__verificationCSP || [],
            username: {exists: !!username, visible: !!username && rect(username).width > 0 && rect(username).height > 0, enabled: !!username && !username.disabled, required: !!username?.required, label: usernameLabel?.textContent.trim() || ""},
            password: {exists: !!passwordInput, visible: !!passwordInput && rect(passwordInput).width > 0 && rect(passwordInput).height > 0, enabled: !!passwordInput && !passwordInput.disabled, required: !!passwordInput?.required, label: passwordLabel?.textContent.trim() || ""},
            submit: {exists: !!submit, visible: !!submit && rect(submit).width > 0 && rect(submit).height > 0, enabled: !!submit && !submit.disabled, text: submit?.textContent.trim() || ""},
            viewportWidth: innerWidth,
            documentWidth: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth),
          };
        })()`);
        const prefix = `${lang}-${theme}-${width}x${height}`;
        // cp_verify only forces a full document navigation. An unauthenticated
        // bootstrap API response may intentionally replace its query with the
        // plain #/login route; query preservation is not a product contract.
        check(`${prefix}: literal request normalizes to login, locale, theme and viewport selected`, state.pathname === "/" && state.hash.split("?")[0] === "#/login" && state.lang === lang && state.theme === theme && state.dark === (theme === "dark") && state.viewportWidth === width, { requestedURL: `${options.hub}/login?cp_verify=${lang}-${theme}-${width}`, ...state });
        check(`${prefix}: username/password/sign-in controls usable`,
          state.username.exists && state.username.visible && state.username.enabled && state.username.required && state.username.label &&
          state.password.exists && state.password.visible && state.password.enabled && state.password.required && state.password.label &&
          state.submit.exists && state.submit.visible && state.submit.enabled && state.submit.text === catalog.login.sign_in && state.username.label === catalog.login.username_label && state.password.label === catalog.login.password_label, state);
        check(`${prefix}: no horizontal overflow`, state.documentWidth <= state.viewportWidth, state);
        check(`${prefix}: no missing resources or CSP violations`, state.resourceErrors.length === 0 && state.cspViolations.length === 0, state);
        const shotPath = join(realOutDir, `login-${prefix}.png`);
        await session.screenshot(shotPath);
        report.screenshots.push(shotPath);
        report.matrix.push({ lang, theme, width, height, requestedPath: "/login", normalizedPathname: state.pathname, screenshot: shotPath });

        if (lang === "en" && theme === "dark" && width === 360) {
          await session.evaluate("document.activeElement?.blur(); true");
          const baselineShadows = await session.evaluateJSON(`Array.from(document.querySelectorAll("a,button,input,select,textarea,[tabindex]")).map((node) => getComputedStyle(node).boxShadow)`);
          await session.pressTab();
          const focus = await session.evaluateJSON(`(() => {
            const node = document.activeElement;
            const style = getComputedStyle(node);
            return {
              tag: node?.tagName || "",
              id: node?.id || "",
              className: typeof node?.className === "string" ? node.className : "",
              outlineStyle: style.outlineStyle,
              outlineWidth: style.outlineWidth,
              outlineColor: style.outlineColor,
              boxShadow: style.boxShadow,
              focusVisible: node?.matches(":focus-visible") || false,
              focusableIndex: Array.from(document.querySelectorAll("a,button,input,select,textarea,[tabindex]")).indexOf(node),
              isBody: node === document.body,
            };
          })()`);
          const hasFocusRing = (focus.outlineStyle !== "none" && Number.parseFloat(focus.outlineWidth) > 0 && parseColor(focus.outlineColor)?.[3] > 0) || (focus.boxShadow !== "none" && focus.boxShadow !== baselineShadows[focus.focusableIndex]);
          check("360x800 dark: keyboard Tab reaches a focus-visible control with an actual focus ring", !focus.isBody && focus.focusVisible && focus.focusableIndex >= 0 && hasFocusRing, focus);
          const focusShot = join(realOutDir, "login-keyboard-focus-360x800.png");
          await session.screenshot(focusShot);
          report.screenshots.push(focusShot);
        }
      }
    }
    }

    // Revisit the literal route with English labels, then test one real
    // invalid attempt followed by a successful form submission.
    await session.setViewport(360, 800);
    await session.evaluate('localStorage.setItem("cp_lang", "en"); true');
    await session.navigate(`${options.hub}/login`);
    await waitFor(session, `Boolean(document.getElementById("cp-login-password"))`, "login form before authentication tests");
    await setInputAndSubmit(session, "phase1-deliberately-invalid");
    await waitFor(session, `Boolean(document.querySelector('[role="alert"]')?.textContent.trim())`, "invalid-login feedback");
    const invalidState = await session.evaluateJSON(`({
      alert: document.querySelector('[role="alert"]')?.textContent.trim() || "",
      session: Boolean(localStorage.getItem("cp_session") || sessionStorage.getItem("cp_session")),
      hash: location.hash,
    })`);
    check("invalid login shows feedback without creating a session",
      /invalid username or password/i.test(invalidState.alert) && !invalidState.session && invalidState.hash.includes("/login"), invalidState);
    const invalidShot = join(realOutDir, "login-invalid-feedback-360x800.png");
    await session.screenshot(invalidShot);
    report.screenshots.push(invalidShot);

    await setInputAndSubmit(session, password);
    await waitFor(session, `Boolean(localStorage.getItem("cp_session") || sessionStorage.getItem("cp_session"))`, "successful form login");
    await waitFor(session, `location.hash !== "#/login" && Boolean(document.querySelector('[aria-label="User menu"]'))`, "authenticated dashboard");
    const authenticated = await session.evaluateJSON(`({hash: location.hash, hasSession: Boolean(localStorage.getItem("cp_session") || sessionStorage.getItem("cp_session")), pageTitle: document.querySelector("main h1")?.textContent.trim() || document.title})`);
    check("valid login establishes a dashboard session", authenticated.hasSession && authenticated.hash !== "#/login", authenticated);
    const versionResponse = await session.evaluateJSON(`(async () => {
      const token = localStorage.getItem("cp_session") || sessionStorage.getItem("cp_session") || "";
      const response = await fetch("/api/v1/version", {headers:{Authorization:"Bearer " + token}});
      return {status:response.status, authenticatedWithSession:Boolean(token), body:await response.json()};
    })()`);
    check("authenticated version API agrees with expected binary metadata", versionResponse.status === 200 && versionResponse.authenticatedWithSession &&
      (!requireVersionMetadata || Object.entries(expectedVersionMetadata).every(([key, value]) => versionResponse.body[key] === value)),
      { expected: requireVersionMetadata ? expectedVersionMetadata : null, actual: versionResponse });
    report.versionMetadata = versionResponse.body;

    await session.evaluate(`document.querySelector('[aria-label="User menu"]')?.click(); true`);
    await waitFor(session, `Boolean(document.querySelector('[role="menu"] [role="menuitem"]'))`, "user menu");
    const signOut = await session.evaluateJSON(`(() => {
      const item = Array.from(document.querySelectorAll('[role="menu"] [role="menuitem"]')).find((node) => /sign out/i.test(node.textContent));
      if (!item) return false;
      item.click();
      return true;
    })()`);
    check("sign-out menu action is available", signOut);
    await waitFor(session, `!localStorage.getItem("cp_session") && !sessionStorage.getItem("cp_session") && location.hash.includes("/login")`, "logout to login route");
    const loggedOut = await session.evaluateJSON(`({hash: location.hash, hasSession: Boolean(localStorage.getItem("cp_session") || sessionStorage.getItem("cp_session")), hasLoginForm: Boolean(document.getElementById("cp-login-username"))})`);
    check("logout clears the session and restores login", !loggedOut.hasSession && loggedOut.hasLoginForm && loggedOut.hash.includes("/login"), loggedOut);

    await session.navigate(`${options.hub}/`);
    await waitFor(session, `location.hash.includes("/login") && Boolean(document.getElementById("cp-login-username"))`, "unauthenticated redirect after reload");
    const restored = await session.evaluateJSON(`({hash: location.hash, hasSession: Boolean(localStorage.getItem("cp_session") || sessionStorage.getItem("cp_session")), hasLoginForm: Boolean(document.getElementById("cp-login-username"))})`);
    check("direct dashboard revisit remains unauthenticated after logout", !restored.hasSession && restored.hasLoginForm && restored.hash.includes("/login"), restored);
    const restoreShot = join(realOutDir, "login-after-logout-360x800.png");
    await session.screenshot(restoreShot);
    report.screenshots.push(restoreShot);
  } catch (error) {
    failure = error;
    report.failure = error?.stack || String(error);
  } finally {
    if (session) {
      report.consoleErrors = session.consoleErrors;
      report.javascriptErrors = session.javascriptErrors;
      report.networkFailures = session.networkFailures;
      const profile = session.profile;
      await session.stop().catch((error) => { report.cleanupError = String(error); });
      report.profileRemoved = !profile || !existsSync(profile);
      activeSession = null;
    }
    if (report.consoleErrors.length || report.javascriptErrors.length || report.networkFailures.length) {
      report.checks.push({ name: "browser console, JavaScript and missing-asset errors absent", pass: false, detail: { consoleErrors: report.consoleErrors, javascriptErrors: report.javascriptErrors, networkFailures: report.networkFailures } });
      failure ||= new Error("browser console/JavaScript errors were recorded");
    } else {
      report.checks.push({ name: "browser console and JavaScript errors absent", pass: true });
    }
    report.passed = !failure && report.checks.every((item) => item.pass) && report.profileRemoved;
    report.totals = { scenarios: report.matrix.length, checks: report.checks.length, passedChecks: report.checks.filter((item) => item.pass).length, screenshots: report.screenshots.length };
    writeFileSync(safeVerificationPath(join(realOutDir, "auth-report.json")), `${JSON.stringify(report, null, 2)}\n`, { mode: 0o600 });
  }

  process.stdout.write(`phase1 auth browser smoke: ${report.passed ? "PASS" : "FAIL"} (${report.checks.filter((item) => item.pass).length}/${report.checks.length} checks)\n`);
  process.stdout.write(`report: ${join(realOutDir, "auth-report.json")}\n`);
  if (failure) process.stderr.write(`${failure.stack || failure}\n`);
  return report.passed ? 0 : 1;
}

let activeSession = null;
export function registerActiveSession(session) {
  activeSession = session;
}

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => {
    if (activeSession) void activeSession.stop().finally(() => process.exit(signal === "SIGINT" ? 130 : 143));
    else process.exit(signal === "SIGINT" ? 130 : 143);
  });
}

const invokedDirectly = Boolean(process.argv[1]) && resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (invokedDirectly) {
  const parsed = parseArgs(process.argv.slice(2));
  if (parsed === null) {
    usage();
    process.exit(0);
  }

  main().then((code) => process.exit(code)).catch((error) => {
    process.stderr.write(`verify-phase1-auth.mjs: ${error?.stack || error}\n`);
    process.exit(1);
  });
}
