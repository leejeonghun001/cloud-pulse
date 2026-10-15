#!/usr/bin/env node
// Round32: bounded local Firefox render-readiness baseline preparation.
// Actual measurements run only with explicit --run after parent source-freeze
// authorization; route readiness is DOM evidence, never paint or UX evidence.
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { request as httpRequest } from "node:http";
import {
  closeSync,
  fstatSync,
  fsyncSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  openSync,
  readdirSync,
  readlinkSync,
  readFileSync,
  realpathSync,
  rmdirSync,
  rmSync,
  unlinkSync,
  writeSync,
  writeFileSync,
} from "node:fs";
import { createHash, randomBytes, randomUUID } from "node:crypto";
import { basename, dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = realpathSync(resolve(dirname(fileURLToPath(import.meta.url)), ".."));
export const APPROVED_TMP = "/tmp/opencode";
const HUB_PORT = 18142;
const BIDI_PORT = 18143;
const HUB_ORIGIN = `http://127.0.0.1:${HUB_PORT}`;
export const SAMPLES_PER_ROUTE = 10;
const ROUTE_READY_TIMEOUT_MS = 20_000;
const HUB_START_TIMEOUT_MS = 20_000;
const FIREFOX_START_TIMEOUT_MS = 40_000;
const BIDI_COMMAND_TIMEOUT_MS = 15_000;
const MAX_RUN_TIME_MS = 8 * 60_000;
const MAX_STDOUT_BYTES = 1 << 20;
const MAX_STDERR_BYTES = 1 << 20;
const MAX_FIREFOX_STDERR_BYTES = 256 << 10;
const MAX_HTTP_AUTH_RESPONSE_BYTES = 64 << 10;
const MAX_BIDI_MESSAGE_BYTES = 256 << 10;
const MAX_BIDI_PENDING_COMMANDS = 256;
const MAX_OUTPUT_JSON_BYTES = 2 << 20;
const STABLE_MUTATION_FREE_FRAMES = 3;
const MAX_NETWORK_DECISIONS = 512;
const HUB_AGENT_TOKEN = "round32-synthetic-agent-token-0123456789";
const DEFAULT_PASSWORD = "changeme";
export const ROUTE_SPECS = Object.freeze([
  Object.freeze({ key: "overview", route: "#/", selector: ".cp-systems-card", marker: "Round32 overview fixture host" }),
  Object.freeze({ key: "hostdetail", route: "#/host/round32-fixture-host", selector: ".cp-host-detail-top", marker: "Round32 host-detail fixture marker" }),
  Object.freeze({ key: "activity", route: "#/alerts", selector: '.cp-activity-event[data-event-id="32001"]', marker: "Round32 Activity fixture rule" }),
  Object.freeze({ key: "costs", route: "#/costs", selector: ".cp-costs-hosts", marker: "Round32 costs fixture host" }),
  Object.freeze({ key: "notifications", route: "#/settings/notifications", selector: ".cp-channel-row-name", marker: "Round32 notification fixture channel" }),
]);

export class HarnessError extends Error {
  constructor(message, code = "HARNESS_ERROR") {
    super(message);
    this.name = "HarnessError";
    this.code = code;
  }
}

export function percentile(values, p) {
  if (!values.length) return null;
  if (!(p >= 0 && p <= 100)) throw new RangeError("percentile must be between 0 and 100");
  const ordered = [...values].sort((a, b) => a - b);
  const position = (ordered.length - 1) * p / 100;
  const low = Math.floor(position), high = Math.ceil(position);
  return low === high ? ordered[low] : ordered[low] + (ordered[high] - ordered[low]) * (position - low);
}

export function summarize(values) {
  if (!values.length) return { n: 0, median: null, p95: null, sample_max: null };
  return {
    n: values.length,
    median: percentile(values, 50),
    p95: percentile(values, 95),
    sample_max: Math.max(...values),
  };
}

export function routeReadySnapshot(snapshot, spec, minimumStableFrames = STABLE_MUTATION_FREE_FRAMES) {
  return snapshot?.hash === spec.route && snapshot?.markerText?.includes(spec.marker) === true &&
    snapshot.stableMutationFreeFrames >= minimumStableFrames;
}

export function classifyNetworkRequest({ url, method = "GET", redirectCount = 0, hubOrigin = HUB_ORIGIN }) {
  let parsed;
  try { parsed = new URL(url, hubOrigin); }
  catch { return { allow: false, reason: "malformed-url", path: "<invalid>" }; }
  const path = parsed.pathname.slice(0, 256);
  if (redirectCount > 0) return { allow: false, reason: "redirect", path };
  if (parsed.origin !== hubOrigin || parsed.protocol !== "http:" || parsed.hostname !== "127.0.0.1" || parsed.username || parsed.password) {
    return { allow: false, reason: "non-owned-origin", path };
  }
  const verb = String(method).toUpperCase();
  const auth = new Set([
    "GET /api/v1/auth/me",
    "POST /api/v1/auth/login",
    "POST /api/v1/auth/password",
  ]);
  if (auth.has(`${verb} ${parsed.pathname}`)) return { allow: true, reason: "owned-hub-auth", path };
  if (parsed.pathname.startsWith("/api/v1/")) {
    return { allow: false, reason: verb === "GET" || verb === "HEAD" ? "dashboard-api-must-use-synthetic-fixture" : "non-auth-api-mutation", path };
  }
  const staticGet = (verb === "GET" || verb === "HEAD") &&
    (parsed.pathname === "/" || parsed.pathname === "/login" || parsed.pathname === "/favicon.ico" || parsed.pathname.startsWith("/assets/"));
  return staticGet ? { allow: true, reason: "owned-hub-static", path } : { allow: false, reason: "unapproved-request", path };
}

const makeSample = (ts, cpu = 21.5) => ({
  ts, cpu_percent: cpu, load1: 0.42, load5: 0.38, load15: 0.31,
  mem_total: 8_000_000_000, mem_available: 4_000_000_000, mem_used: 4_000_000_000,
  mem_used_percent: 50, mem_cached: 1_000_000_000, swap_total: 0, swap_used: 0,
  disk_total: 60_000_000_000, disk_used: 20_000_000_000, disk_used_percent: 33,
  disk_read_bps: 2048, disk_write_bps: 4096, net_rx_bps: 1024, net_tx_bps: 2048,
  net_rx_bytes: 4096, net_tx_bytes: 8192, uptime_seconds: 86400,
});

export function fixtureForRequest(method, pathname, locale = "en", now = Math.floor(Date.now() / 1000)) {
  if (String(method).toUpperCase() !== "GET") return null;
  const hostId = "round32-fixture-host";
  const overviewHost = {
    host: { id: hostId, hostname: "Round32 overview fixture host", provider: "other", os: "linux", arch: "arm64", platform: "linux", agent_version: "dev", remote_update: {} },
    status: "up", last_seen: now, latest: makeSample(now),
    egress: { tx_bytes: 65536, rx_bytes: 32768, tx_limit_bytes: 0, rx_limit_bytes: 0, level: "ok", rx_level: "ok", limit_source: "provider", rx_limit_source: "provider" },
    update: null,
  };
  const detailSummary = {
    ...overviewHost,
    host: { ...overviewHost.host, hostname: "Round32 host-detail fixture marker" },
    latest: makeSample(now, 34.5),
    egress: { ...overviewHost.egress, tx_bytes: 131072, rx_bytes: 65536 },
  };
  const event = {
    id: 32001, rule_id: 32002, rule_name: "Round32 Activity fixture rule", host_id: hostId,
    hostname: "Round32 activity fixture host", metric: "cpu_percent", state: "firing",
    value: 92, threshold: 90, started_at: now - 120, notified_at: now - 60,
    resolved_at: 0, deliveries: [],
  };
  const channel = {
    id: 32003, name: "Round32 notification fixture channel", type: "webhook", enabled: false,
    config: {}, created_at: now - 300, updated_at: now - 60,
  };
  const emptyStats = { total: 0, success: 0, last_success_at: 0, last_failure_at: 0, avg_latency_ms: 0, max_latency_ms: 0 };
  const channelHealth = {
    channel_id: channel.id, channel_name: channel.name, channel_type: channel.type, enabled: false,
    state: "unknown", stats_7d: emptyStats, stats_30d: emptyStats,
    settings: null, last_synthetic: null, recent_synthetic: [],
  };
  const billingHost = {
    host_id: hostId, hostname: "Round32 costs fixture host", provider: "other", instance_id: "",
    matched: false, cloud_mtd: 0, cloud_forecast: 0,
    network_estimate: { mtd: 0, projected: 0, plan_id: 0 }, total_mtd: 0, total_forecast: 0,
  };
  const series = {
    host_id: hostId, resolution: 15, from: now - 3600, to: now,
    ts: [now - 30, now - 15, now], cpu: [20, 25, 21], mem: [50, 51, 50],
    disk: [33, 33, 33], net_rx: [1, 2, 1], net_tx: [2, 3, 2],
    disk_read: [2, 2, 3], disk_write: [3, 4, 3], load1: [0.4, 0.5, 0.42],
  };
  const route = pathname.split("?")[0];
  if (route === "/api/v1/hosts") return { hosts: [overviewHost], generated_at: now };
  if (route === "/api/v1/egress") return { month: new Date(now * 1000).toISOString().slice(0, 7), hosts: [{ host_id: hostId, month: new Date(now * 1000).toISOString().slice(0, 7), tx_bytes: 65536, rx_bytes: 32768, tx_limit_bytes: 0, rx_limit_bytes: 0, limit_source: "provider", rx_limit_source: "provider" }] };
  if (route === "/api/v1/buckets") return { buckets: [], collectors: [] };
  if (route === "/api/v1/storage/accounts") return { accounts: [] };
  if (route === "/api/v1/version") return { version: "dev", commit: "none", date: "unknown", latest_version: "", update_check_enabled: false };
  if (route === "/api/v1/billing") return { snapshots: [], hosts: [billingHost], display_currency: { currency: "USD", krw_per_usd: 0, updated_at: 0 }, interval_seconds: 86400 };
  if (route === "/api/v1/settings/billing/currency") return { currency: "USD", krw_per_usd: 0, updated_at: 0 };
  if (route === "/api/v1/alerts/active") return { events: [event] };
  if (route === "/api/v1/alerts/events") return { events: [event], has_more: false, next_before: 0 };
  if (route === "/api/v1/alerts/channels") return [channel];
  if (route === "/api/v1/alerts/channels/health") return { channels: [channelHealth], banner_needed: false, banner_channel_names: [] };
  if (route === `/api/v1/hosts/${hostId}`) return detailSummary;
  if (route === `/api/v1/hosts/${hostId}/metrics`) return series;
  if (route === `/api/v1/hosts/${hostId}/inventory`) return { ports: [], containers: [] };
  if (locale === "en" || locale === "ko") return null;
  return null;
}

export function cleanChildEnvironment(home, tmp) {
  return {
    PATH: process.env.PATH || "/usr/bin:/bin",
    HOME: home,
    TMPDIR: tmp,
    LANG: "C.UTF-8",
    LC_ALL: "C.UTF-8",
    TZ: "UTC",
    GOTELEMETRY: "off",
    GOENV: "off",
    GOWORK: "off",
    GOPROXY: "off",
    GOSUMDB: "off",
    GOTOOLCHAIN: "local",
  };
}

function lstatMaybe(path) {
  try { return lstatSync(path); }
  catch (error) { if (error.code === "ENOENT") return null; throw error; }
}

export function validateEvidencePaths(root = ROOT, runId = "test-run") {
  const absRoot = realpathSync(root);
  let cursor = absRoot;
  for (const component of [".verification", "round32", "render-preparation", "runs"]) {
    cursor = join(cursor, component);
    const stat = lstatMaybe(cursor);
    if (stat && (!stat.isDirectory() || stat.isSymbolicLink())) throw new HarnessError(`unsafe evidence ancestor: ${cursor}`, "UNSAFE_PATH");
  }
  if (!/^[a-z0-9-]{1,80}$/.test(runId)) throw new HarnessError("invalid evidence run id", "UNSAFE_PATH");
  const outputDir = join(absRoot, ".verification", "round32", "render-preparation", "runs", runId);
  if (lstatMaybe(outputDir)) throw new HarnessError(`evidence destination exists; refusing overwrite: ${outputDir}`, "EXISTS");
  return { baseDir: join(absRoot, ".verification", "round32", "render-preparation"), runsDir: dirname(outputDir), outputDir };
}

export function cleanupOwnedStage(stage, ownedRoot, prefix = "round32-render-") {
  const root = realpathSync(ownedRoot);
  const info = lstatMaybe(stage);
  if (!info) return;
  if (dirname(stage) !== root || !stage.split(/[\\/]/).at(-1).startsWith(prefix) || info.isSymbolicLink() || !info.isDirectory()) {
    throw new HarnessError("refusing cleanup outside invocation-owned temporary directory", "UNSAFE_CLEANUP");
  }
  rmSync(stage, { recursive: true, force: false });
}

function ensureDirectoryChain(root, relativeParts) {
  let cursor = realpathSync(root);
  for (const component of relativeParts) {
    cursor = join(cursor, component);
    try { mkdirSync(cursor, { mode: 0o700 }); }
    catch (error) { if (error.code !== "EEXIST") throw error; }
    const stat = lstatSync(cursor);
    if (!stat.isDirectory() || stat.isSymbolicLink()) throw new HarnessError(`unsafe evidence ancestor: ${cursor}`, "UNSAFE_PATH");
  }
  return cursor;
}

function boundedDrain(stream, cap, onData = undefined) {
  let total = 0;
  let bytes = Buffer.alloc(0);
  let overflow = false;
  let overflowResolve;
  const overflowed = new Promise((resolveOverflow) => { overflowResolve = resolveOverflow; });
  const closed = new Promise((resolveClosed, rejectClosed) => {
    stream.on("data", (chunk) => {
      total += chunk.length;
      if (onData) onData(chunk);
      const available = Math.max(0, cap - bytes.length);
      if (available) bytes = Buffer.concat([bytes, chunk.subarray(0, available)], bytes.length + Math.min(available, chunk.length));
      if (chunk.length > available && !overflow) { overflow = true; overflowResolve(); }
    });
    stream.once("end", resolveClosed);
    stream.once("close", resolveClosed);
    stream.once("error", rejectClosed);
  });
  return { closed, overflowed, overflow: () => overflow, total: () => total, bytes: () => bytes };
}

function processGroupExists(pid) {
  if (!pid || process.platform === "win32") return false;
  try { process.kill(-pid, 0); return true; }
  catch (error) { if (error.code === "ESRCH") return false; if (error.code === "EPERM") return true; throw error; }
}

function delay(ms, signal = undefined) {
  if (signal?.aborted) return Promise.reject(signal.reason || new HarnessError("cancelled", "CANCELLED"));
  return new Promise((resolveDelay, rejectDelay) => {
    const timer = setTimeout(done, ms);
    function done() { signal?.removeEventListener("abort", abort); resolveDelay(); }
    function abort() { clearTimeout(timer); signal?.removeEventListener("abort", abort); rejectDelay(signal.reason || new HarnessError("cancelled", "CANCELLED")); }
    signal?.addEventListener("abort", abort, { once: true });
  });
}

async function stopOwnedGroup(child, graceMs = 500, reapMs = 2000) {
  const pid = child?.pid;
  if (!pid) return;
  const sendGroupSignal = (sig) => { try { process.kill(-pid, sig); } catch (error) { if (error.code !== "ESRCH") throw error; } };
  if (processGroupExists(pid)) sendGroupSignal("SIGTERM");
  let deadline = Date.now() + graceMs;
  while (processGroupExists(pid) && Date.now() < deadline) await delay(20).catch(() => {});
  if (processGroupExists(pid)) {
    sendGroupSignal("SIGKILL");
    deadline = Date.now() + reapMs;
    while (processGroupExists(pid) && Date.now() < deadline) await delay(20).catch(() => {});
    if (processGroupExists(pid)) throw new HarnessError("owned process group remained after SIGKILL", "CLEANUP_FAILED");
  }
  if (child.exitCode === null && child.signalCode === null) {
    await Promise.race([new Promise((resolveExit) => child.once("exit", resolveExit)), delay(reapMs)]);
  }
  if (child.exitCode === null && child.signalCode === null) throw new HarnessError("owned process leader did not exit", "CLEANUP_FAILED");
}

async function waitChild(child, { timeoutMs, signal, streams = [] }) {
  const start = Date.now();
  let spawnError = null;
  const onError = (error) => { spawnError = error; };
  child.once("error", onError);
  try {
    while (child.exitCode === null && child.signalCode === null) {
      if (spawnError) throw new HarnessError(`owned command spawn failed: ${spawnError.code || "error"}`, "SPAWN_FAILED");
      if (signal?.aborted) throw signal.reason || new HarnessError("cancelled", "CANCELLED");
      if (streams.some((stream) => stream.overflow())) throw new HarnessError("child output exceeded byte cap", "OUTPUT_LIMIT");
      if (Date.now() - start >= timeoutMs) throw new HarnessError("owned command timed out", "TIMEOUT");
      await delay(25, signal);
    }
  } finally {
    child.removeListener("error", onError);
  }
  if (streams.some((stream) => stream.overflow())) throw new HarnessError("child output exceeded byte cap", "OUTPUT_LIMIT");
  if (child.exitCode !== 0) throw new HarnessError(`owned command exited ${child.exitCode ?? child.signalCode}`, "CHILD_FAILED");
}

export async function runOwnedCommand(command, args, { env, cwd = ROOT, timeoutMs = 30_000, stdoutCap = MAX_STDOUT_BYTES, stderrCap = MAX_STDERR_BYTES, signal } = {}) {
  const child = spawn(command, args, { cwd, env, detached: true, stdio: ["ignore", "pipe", "pipe"] });
  child.on("error", () => {});
  const stdout = boundedDrain(child.stdout, stdoutCap);
  const stderr = boundedDrain(child.stderr, stderrCap);
  let failure;
  try { await waitChild(child, { timeoutMs, signal, streams: [stdout, stderr] }); }
  catch (error) { failure = error; }
  try { await stopOwnedGroup(child); }
  catch (error) { failure ||= error; }
  const drainResults = await Promise.allSettled([stdout.closed, stderr.closed]);
  if (drainResults.some((result) => result.status === "rejected")) failure ||= new HarnessError("owned process output drain failed", "OUTPUT_DRAIN");
  if (failure) throw failure;
  return { stdout: stdout.bytes(), stderr: stderr.bytes(), stdoutBytes: stdout.total(), stderrBytes: stderr.total() };
}

export function installFirefoxPreferences(profile, locale) {
  const text = [
    'user_pref("app.update.enabled", false);',
    'user_pref("app.update.auto", false);',
    'user_pref("app.update.background.enabled", false);',
    'user_pref("extensions.update.enabled", false);',
    'user_pref("extensions.getAddons.cache.enabled", false);',
    'user_pref("datareporting.healthreport.uploadEnabled", false);',
    'user_pref("datareporting.policy.dataSubmissionEnabled", false);',
    'user_pref("toolkit.telemetry.enabled", false);',
    'user_pref("toolkit.telemetry.unified", false);',
    'user_pref("browser.shell.checkDefaultBrowser", false);',
    'user_pref("network.proxy.type", 0);',
    'user_pref("network.dns.disablePrefetch", true);',
    'user_pref("network.prefetch-next", false);',
    'user_pref("network.predictor.enabled", false);',
    'user_pref("network.http.speculative-parallel-limit", 0);',
    'user_pref("browser.urlbar.speculativeConnect.enabled", false);',
    'user_pref("dom.serviceWorkers.enabled", false);',
    `user_pref("intl.locale.requested", ${JSON.stringify(locale)});`,
    "",
  ].join("\n");
  writeFileSync(join(profile, "user.js"), text, { mode: 0o600, flag: "wx" });
  return text;
}

function makeAuthRequest(path, body, token, { timeoutMs = 5000, signal } = {}) {
  if (!new Set(["/api/v1/auth/login", "/api/v1/auth/password"]).has(path)) throw new HarnessError("auth helper refused non-auth path", "REQUEST_POLICY");
  const payload = Buffer.from(JSON.stringify(body));
  return new Promise((resolveResponse, rejectResponse) => {
    const request = httpRequest(`${HUB_ORIGIN}${path}`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "Content-Length": String(payload.length), ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      agent: false,
    }, (response) => {
      let received = 0;
      const chunks = [];
      response.on("data", (chunk) => {
        received += chunk.length;
        if (received > MAX_HTTP_AUTH_RESPONSE_BYTES) request.destroy(new HarnessError("auth response exceeded cap", "OUTPUT_LIMIT"));
        else chunks.push(chunk);
      });
      response.on("end", () => {
        try { resolveResponse({ status: response.statusCode, body: JSON.parse(Buffer.concat(chunks).toString("utf8")) }); }
        catch { rejectResponse(new HarnessError("auth endpoint returned invalid JSON", "AUTH_FAILED")); }
      });
    });
    request.setTimeout(timeoutMs, () => request.destroy(new HarnessError("auth request timed out", "TIMEOUT")));
    request.once("error", rejectResponse);
    const abort = () => request.destroy(signal.reason || new HarnessError("cancelled", "CANCELLED"));
    if (signal?.aborted) abort();
    else signal?.addEventListener("abort", abort, { once: true });
    request.once("close", () => signal?.removeEventListener("abort", abort));
    request.end(payload);
  });
}

export async function authenticateFreshHub(signal) {
  const login = await makeAuthRequest("/api/v1/auth/login", { username: "admin", password: DEFAULT_PASSWORD }, "", { signal });
  if (login.status !== 200 || !login.body?.token || !login.body.must_change_password) throw new HarnessError("fresh local hub did not require the expected initial admin password change", "AUTH_FAILED");
  const nextPassword = `R32-${randomBytes(24).toString("base64url")}9!`;
  const changed = await makeAuthRequest("/api/v1/auth/password", { current_password: DEFAULT_PASSWORD, new_password: nextPassword }, login.body.token, { signal });
  if (changed.status !== 200 || !changed.body?.token || changed.body.must_change_password) throw new HarnessError("fresh local hub password rotation failed", "AUTH_FAILED");
  return changed.body.token;
}

export function browserFetchPreload({ locale, sessionToken, fixtures }) {
  const fixtureJSON = JSON.stringify(fixtures);
  const authToken = JSON.stringify(sessionToken);
  return `() => {
    localStorage.setItem("cp_session", ${authToken});
    localStorage.setItem("cp_lang", ${JSON.stringify(locale)});
    window.__round32FirstStart = performance.now();
    window.__round32FirstStart = performance.now();
    window.__round32FixtureReads = Object.create(null);
    window.__round32FetchViolations = [];
    window.__round32FetchViolationCount = 0;
    window.__round32MutationCount = 0;
    new MutationObserver(() => { window.__round32MutationCount++; }).observe(document, {subtree:true, childList:true, attributes:true, characterData:true});
    const fixture = ${fixtureJSON};
    const original = window.fetch.bind(window);
    window.fetch = async (input, init = {}) => {
      const url = new URL(typeof input === "string" ? input : input.url, location.href);
      const method = String(init.method || input?.method || "GET").toUpperCase();
      const deny = (reason) => { window.__round32FetchViolationCount = Math.min(window.__round32FetchViolationCount + 1, 10000); if(window.__round32FetchViolations.length<128) window.__round32FetchViolations.push({reason,method,path:url.pathname.slice(0,128)}); throw new Error("Round32 blocked dashboard fetch"); };
      if (url.origin !== location.origin) deny("non-owned-origin");
      if (url.pathname === "/api/v1/auth/me" && method === "GET") return original(input, init);
      if (!url.pathname.startsWith("/api/v1/")) return original(input, init);
      if (method !== "GET") deny("non-auth-api-mutation");
      const key = url.pathname;
      if (!Object.prototype.hasOwnProperty.call(fixture, key)) deny("missing-synthetic-read-fixture");
      window.__round32FixtureReads[key] = Math.min((window.__round32FixtureReads[key] || 0) + 1, 10000);
      return new Response(JSON.stringify(fixture[key]), {status:200, headers:{"Content-Type":"application/json"}});
    };
  }`;
}

function browserReadyExpression(spec, { initial = false, timeoutMs = ROUTE_READY_TIMEOUT_MS } = {}) {
  const startName = initial ? "__round32FirstStart" : "__round32CurrentStart";
  const route = JSON.stringify(spec.route);
  const selector = JSON.stringify(spec.selector);
  const marker = JSON.stringify(spec.marker);
  return `(() => new Promise((resolve, reject) => {
    const start = Number(window.${startName});
    if (!Number.isFinite(start)) { reject(new Error("missing browser performance start")); return; }
    const deadline = performance.now() + ${timeoutMs};
    let stable = 0, previousMutations = Number(window.__round32MutationCount || 0);
    const tick = () => {
      const node = document.querySelector(${selector});
      const matching = location.hash === ${route};
      const markerText = node ? node.textContent || "" : "";
      const hasMarker = markerText.includes(${marker});
      const mutations = Number(window.__round32MutationCount || 0);
      if (matching && hasMarker && mutations === previousMutations) stable++;
      else stable = 0;
      previousMutations = mutations;
      if (matching && hasMarker && stable >= ${STABLE_MUTATION_FREE_FRAMES}) {
        resolve({hash:location.hash, markerText, stableMutationFreeFrames:stable, elapsedMs:performance.now()-start});
        return;
      }
      if (performance.now() >= deadline) { reject(new Error("route/fixture DOM did not become stable before deadline")); return; }
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  }))()`;
}

function hashRouteURL(route) { return `${HUB_ORIGIN}/${route}`; }

function joinAbortable(promise, signal) {
  if (!signal) return promise;
  if (signal.aborted) return Promise.reject(signal.reason || new HarnessError("cancelled", "CANCELLED"));
  return new Promise((resolveJoin, rejectJoin) => {
    const abort = () => finish(signal.reason || new HarnessError("cancelled", "CANCELLED"));
    let settled = false;
    const finish = (error, value) => {
      if (settled) return;
      settled = true;
      signal.removeEventListener("abort", abort);
      if (error) rejectJoin(error); else resolveJoin(value);
    };
    signal.addEventListener("abort", abort, { once: true });
    promise.then((value) => finish(null, value), (error) => finish(error));
  });
}

export class BidiSession {
  constructor({ profile, env, signal, onDecision }) {
    this.profile = profile;
    this.env = env;
    this.signal = signal;
    this.onDecision = onDecision;
    this.firefox = null;
    this.firefoxSpawnError = null;
    this.websocket = null;
    this.context = null;
    this.capabilities = {};
    this.pending = new Map();
    this.nextId = 0;
    this.stderrTail = "";
    this.stopPromise = null;
  }

  async start(locale, sessionToken, fixtures) {
    this.firefox = spawn("firefox", ["--headless", "--no-remote", "--profile", this.profile, `--remote-debugging-port=${BIDI_PORT}`], {
      cwd: ROOT, env: this.env, detached: true, stdio: ["ignore", "ignore", "pipe"],
    });
    this.firefox.once("error", (error) => { this.firefoxSpawnError = error; });
    const browserStderr = boundedDrain(this.firefox.stderr, MAX_FIREFOX_STDERR_BYTES, (chunk) => {
      this.stderrTail = `${this.stderrTail}${chunk.toString("utf8")}`.slice(-8192);
    });
    this.stderrCapture = browserStderr;
    await waitForBiDiReady(this.firefox, browserStderr, () => /WebDriver BiDi listening/.test(this.stderrTail),
      FIREFOX_START_TIMEOUT_MS, this.signal, () => this.firefoxSpawnError);
    this.websocket = new WebSocket(`ws://127.0.0.1:${BIDI_PORT}/session`);
    await joinAbortable(new Promise((resolveOpen, rejectOpen) => {
      const timeout = setTimeout(() => rejectOpen(new HarnessError("Firefox WebSocket connect timed out", "FIREFOX_START")), 5000);
      this.websocket.addEventListener("open", () => { clearTimeout(timeout); resolveOpen(); }, { once: true });
      this.websocket.addEventListener("error", () => { clearTimeout(timeout); rejectOpen(new HarnessError("Firefox WebSocket connect failed", "FIREFOX_START")); }, { once: true });
    }), this.signal);
    this.websocket.addEventListener("message", (event) => this.onRawMessage(event.data));
    this.websocket.addEventListener("close", () => {
      for (const pending of this.pending.values()) { clearTimeout(pending.timer); pending.reject(new HarnessError("Firefox BiDi socket closed", "BIDI_CLOSED")); }
      this.pending.clear();
    });
    const session = await this.send("session.new", { capabilities: {} });
    this.capabilities = session.capabilities || {};
    this.context = (await this.send("browsingContext.create", { type: "tab" })).context;
    await this.send("session.subscribe", { events: ["network.beforeRequestSent"] });
    await this.send("network.addIntercept", { phases: ["beforeRequestSent"], contexts: [this.context] });
    await this.send("script.addPreloadScript", { functionDeclaration: browserFetchPreload({ locale, sessionToken, fixtures }) });
    await this.send("browsingContext.setViewport", { context: this.context, viewport: { width: 1366, height: 900 } });
    return this.capabilities;
  }

  onMessage(message) {
    if (message.method === "network.beforeRequestSent") {
      const req = message.params?.request || {};
      if (!message.params?.isBlocked) {
        this.onDecision({ method: String(req.method || "").toUpperCase(), path: "<unintercepted>", action: "fail", reason: "request-not-paused-by-network-fence" });
        try { this.websocket?.close(1008, "request was not intercepted"); } catch { /* fail closed */ }
        return;
      }
      const decision = classifyNetworkRequest({ url: req.url, method: req.method, redirectCount: message.params?.redirectCount || 0 });
      this.onDecision({ method: String(req.method || "").toUpperCase(), path: decision.path, action: decision.allow ? "continue" : "fail", reason: decision.reason });
      const command = decision.allow ? "network.continueRequest" : "network.failRequest";
      void this.send(command, { request: req.request }).catch(() => {});
      return;
    }
    if (message.id && this.pending.has(message.id)) {
      const pending = this.pending.get(message.id);
      clearTimeout(pending.timer);
      this.pending.delete(message.id);
      if (message.type === "error") pending.reject(new HarnessError(`${message.error}: ${message.message}`, "BIDI_COMMAND"));
      else pending.resolve(message.result);
    }
  }

  onRawMessage(data) {
    if (typeof data !== "string" || data.length > MAX_BIDI_MESSAGE_BYTES) {
      this.onDecision({ method: "", path: "<bidi-message>", action: "fail", reason: "bidi-message-size-cap" });
      try { this.websocket?.close(1009, "message cap"); } catch { /* closing */ }
      return;
    }
    try { this.onMessage(JSON.parse(data)); }
    catch {
      this.onDecision({ method: "", path: "<bidi-message>", action: "fail", reason: "malformed-bidi-message" });
      try { this.websocket?.close(1002, "invalid message"); } catch { /* closing */ }
    }
  }

  send(method, params = {}) {
    if (!this.websocket || this.websocket.readyState !== WebSocket.OPEN) return Promise.reject(new HarnessError("Firefox BiDi is not open", "BIDI_CLOSED"));
    if (this.pending.size >= MAX_BIDI_PENDING_COMMANDS) return Promise.reject(new HarnessError("Firefox BiDi command concurrency exceeded cap", "BIDI_CAP"));
    const id = ++this.nextId;
    return joinAbortable(new Promise((resolveResult, rejectResult) => {
      const timer = setTimeout(() => { this.pending.delete(id); rejectResult(new HarnessError(`BiDi command timed out: ${method}`, "BIDI_TIMEOUT")); }, BIDI_COMMAND_TIMEOUT_MS);
      this.pending.set(id, { resolve: resolveResult, reject: rejectResult, timer });
      try { this.websocket.send(JSON.stringify({ id, method, params })); }
      catch (error) { clearTimeout(timer); this.pending.delete(id); rejectResult(error); }
    }), this.signal);
  }

  async evaluate(expression) {
    const result = await this.send("script.evaluate", {
      expression, target: { context: this.context }, awaitPromise: true,
      resultOwnership: "none", serializationOptions: { maxObjectDepth: 4 },
    });
    if (result.type === "exception") throw new HarnessError(result.exceptionDetails?.text || "page evaluate exception", "PAGE_EVALUATION");
    return deserialize(result.result);
  }

  async navigate(url, wait = "none") {
    if (!url.startsWith(`${HUB_ORIGIN}/`)) throw new HarnessError("browser navigation refused non-owned origin", "REQUEST_POLICY");
    return this.send("browsingContext.navigate", { context: this.context, url, wait });
  }

  async measureInitial(spec) {
    await this.navigate(hashRouteURL(spec.route), "complete");
    const snapshot = await this.evaluate(browserReadyExpression(spec, { initial: true }));
    if (!routeReadySnapshot(snapshot, spec)) throw new HarnessError("fresh-profile route marker/stability mismatch", "ROUTE_NOT_READY");
    return snapshot;
  }

  async measureTransition(spec) {
    const started = await this.evaluate(`(() => { window.__round32CurrentStart=performance.now(); return window.__round32CurrentStart; })()`);
    await this.navigate(hashRouteURL(spec.route), "none");
    const result = await this.evaluate(browserReadyExpression(spec));
    if (!routeReadySnapshot(result, spec)) throw new HarnessError("warm route marker/stability mismatch", "ROUTE_NOT_READY");
    return { ...result, actionStartMs: started };
  }

  stop() {
    if (this.stopPromise) return this.stopPromise;
    this.stopPromise = (async () => {
      if (this.context && this.websocket?.readyState === WebSocket.OPEN) {
        await this.send("browsingContext.close", { context: this.context }).catch(() => {});
      }
      try { this.websocket?.close(); } catch { /* already closed */ }
      if (this.firefox) await stopOwnedGroup(this.firefox);
      await this.stderrCapture?.closed.catch(() => {});
      if (existsSync(this.profile)) rmSync(this.profile, { recursive: true, force: true });
      if (existsSync(this.profile)) throw new HarnessError("owned Firefox profile was not removed", "CLEANUP_FAILED");
    })();
    return this.stopPromise;
  }
}

function deserialize(value) {
  if (!value) return null;
  if (["string", "number", "boolean"].includes(value.type)) return value.value;
  if (value.type === "null" || value.type === "undefined") return null;
  if (value.type === "array") return (value.value || []).map(deserialize);
  if (value.type === "object") return Object.fromEntries((value.value || []).map(([key, item]) => [typeof key === "string" ? key : deserialize(key), deserialize(item)]));
  return value.value ?? null;
}

async function assertPortsFree(ports) {
  for (const port of ports) {
    await new Promise((resolvePort, rejectPort) => {
      const server = createServer();
      server.once("error", () => rejectPort(new HarnessError(`required owned loopback port ${port} is unavailable; no process was touched`, "PORT_BUSY")));
      server.listen(port, "127.0.0.1", () => server.close((error) => error ? rejectPort(error) : resolvePort()));
    });
  }
}

export function parseListeningSocketInodes(procNetText, port) {
  const inodes = new Set();
  for (const line of procNetText.split(/\r?\n/).slice(1)) {
    const fields = line.trim().split(/\s+/);
    if (fields.length < 10 || fields[3] !== "0A") continue;
    const address = fields[1];
    const colon = address.lastIndexOf(":");
    if (colon < 0 || Number.parseInt(address.slice(colon + 1), 16) !== port) continue;
    inodes.add(fields[9]);
  }
  return inodes;
}

function processOwnsListeningPort(pid, port) {
  try {
    const owned = new Set();
    for (const name of readdirSync(`/proc/${pid}/fd`)) {
      let target;
      try { target = readlinkSync(`/proc/${pid}/fd/${name}`); } catch { continue; }
      const match = /^socket:\[(\d+)\]$/.exec(target);
      if (match) owned.add(match[1]);
    }
    for (const table of ["/proc/net/tcp", "/proc/net/tcp6"]) {
      const listening = parseListeningSocketInodes(readFileSync(table, "utf8"), port);
      for (const inode of listening) if (owned.has(inode)) return true;
    }
  } catch { return false; }
  return false;
}

async function waitForOwnedPort(child, port, timeoutMs, signal) {
  const deadline = Date.now() + timeoutMs;
  let spawnError = null;
  const onError = (error) => { spawnError = error; };
  child.once("error", onError);
  try {
  while (Date.now() < deadline) {
    if (signal?.aborted) throw signal.reason || new HarnessError("cancelled", "CANCELLED");
    if (spawnError) throw new HarnessError(`owned hub spawn failed: ${spawnError.code || "error"}`, "SPAWN_FAILED");
    if (child.exitCode !== null || child.signalCode !== null) throw new HarnessError("owned hub exited before it bound its port", "HUB_START");
    if (processOwnsListeningPort(child.pid, port)) return;
    await delay(100, signal);
  }
  throw new HarnessError(`owned hub did not listen on loopback port ${port} in time`, "HUB_START");
  } finally {
    child.removeListener("error", onError);
  }
}

function writePrivatePreferences(profile, locale) {
  const prefs = [
    ["app.update.enabled", false], ["app.update.auto", false], ["app.update.background.enabled", false],
    ["extensions.update.enabled", false], ["extensions.getAddons.cache.enabled", false],
    ["datareporting.healthreport.uploadEnabled", false], ["datareporting.policy.dataSubmissionEnabled", false],
    ["toolkit.telemetry.enabled", false], ["toolkit.telemetry.unified", false],
    ["network.captive-portal-service.enabled", false], ["network.connectivity-service.enabled", false],
    ["browser.shell.checkDefaultBrowser", false], ["network.proxy.type", 0],
    ["network.dns.disablePrefetch", true], ["network.prefetch-next", false], ["network.predictor.enabled", false],
    ["network.http.speculative-parallel-limit", 0], ["browser.urlbar.speculativeConnect.enabled", false],
    ["browser.cache.disk.enable", false], ["browser.cache.disk.capacity", 0], ["media.cache_size", 0],
    ["dom.serviceWorkers.enabled", false], ["intl.locale.requested", locale],
  ];
  const text = `${prefs.map(([name, value]) => `user_pref(${JSON.stringify(name)}, ${JSON.stringify(value)});`).join("\n")}\n`;
  writeFileSync(join(profile, "user.js"), text, { flag: "wx", mode: 0o600 });
}

function waitForBiDiReady(firefox, stderrCapture, ready, timeoutMs, signal, spawnError = () => null) {
  return new Promise((resolveReady, rejectReady) => {
    const start = Date.now();
    const abort = () => finish(signal.reason || new HarnessError("cancelled", "CANCELLED"));
    const poll = setInterval(() => {
      if (spawnError()) return finish(new HarnessError("Firefox could not be spawned", "FIREFOX_START"));
      if (stderrCapture.overflow()) return finish(new HarnessError("Firefox stderr exceeded cap", "OUTPUT_LIMIT"));
      if (firefox.exitCode !== null || firefox.signalCode !== null) return finish(new HarnessError("Firefox exited before BiDi startup", "FIREFOX_START"));
      if (Date.now() - start >= timeoutMs) return finish(new HarnessError("Firefox BiDi startup timed out", "FIREFOX_START"));
      if (ready()) finish(null);
    }, 50);
    function finish(error) {
      clearInterval(poll);
      signal?.removeEventListener("abort", abort);
      if (error) reject(error); else resolve();
    }
    signal?.addEventListener("abort", abort, { once: true });
  });
}

function makeFixtures(locale) {
  const now = Math.floor(Date.now() / 1000);
  const pathSet = new Map();
  const sample = makeSample(now);
  const overview = {
    host: { id: "round32-fixture-host", hostname: "Round32 overview fixture host", provider: "other", os: "linux", arch: "arm64", platform: "linux", agent_version: "dev", remote_update: {} },
    status: "up", last_seen: now, latest: sample,
    egress: { tx_bytes: 65536, rx_bytes: 32768, tx_limit_bytes: 0, rx_limit_bytes: 0, limit_source: "provider", rx_limit_source: "provider", level: "ok", rx_level: "ok" },
    update: null,
  };
  const detail = { ...overview, host: { ...overview.host, hostname: "Round32 host-detail fixture marker" }, latest: { ...sample, cpu_percent: 34.5 } };
  const event = { id: 32001, rule_id: 32002, rule_name: "Round32 Activity fixture rule", host_id: "round32-fixture-host", hostname: "Round32 Activity fixture host", metric: "cpu_percent", state: "firing", value: 92, threshold: 90, started_at: now - 120, notified_at: now - 60, resolved_at: 0, deliveries: [] };
  const channel = { id: 32003, name: "Round32 notification fixture channel", type: "webhook", enabled: false, config: {}, created_at: now - 300, updated_at: now - 60 };
  const networkEstimate = { mtd: 0, projected: 0, plan_id: 0 };
  const billHost = { host_id: "round32-fixture-host", hostname: "Round32 costs fixture host", provider: "other", instance_id: "", matched: false, cloud_mtd: 0, cloud_forecast: 0, network_estimate: networkEstimate, total_mtd: 0, total_forecast: 0 };
  const series = { host_id: "round32-fixture-host", resolution: 15, from: now - 3600, to: now, ts: [now - 30, now - 15, now], cpu: [20, 25, 21], mem: [50, 51, 50], disk: [33, 33, 33], net_rx: [1, 2, 1], net_tx: [2, 3, 2], disk_read: [2, 2, 3], disk_write: [3, 4, 3], load1: [0.4, 0.5, 0.42] };
  const emptyStats = { total: 0, success: 0, failure: 0, avg_duration_ms: 0, max_duration_ms: 0, last_success_at: 0, last_failure_at: 0 };
  const healthChannel = { channel_id: channel.id, channel_name: channel.name, channel_type: channel.type, enabled: false, state: "unknown", stats_7d: emptyStats, stats_30d: emptyStats, settings: null, last_synthetic: null, recent_synthetic: [] };
  const billing = { snapshots: [], hosts: [billHost], display_currency: { currency: "USD", krw_per_usd: 0, rate_updated_at: 0 }, interval_seconds: 86400 };
  const json = (path, value) => pathSet.set(path, value);
  json("/api/v1/hosts", { hosts: [overview], generated_at: now });
  json("/api/v1/egress", { month: new Date(now * 1000).toISOString().slice(0, 7), hosts: [{ host_id: "round32-fixture-host", hostname: "Round32 overview fixture host", egress: { month: new Date(now * 1000).toISOString().slice(0, 7), tx_bytes: 65536, rx_bytes: 32768, limit_bytes: 0, rx_limit_bytes: 0, percent: 0, rx_percent: 0, level: "ok", rx_level: "ok", projected_tx_bytes: 65536, projected_rx_bytes: 32768, limit_source: "provider", rx_limit_source: "none" } }] });
  json("/api/v1/buckets", { buckets: [], collectors: [] });
  json("/api/v1/storage/accounts", { accounts: [] });
  json("/api/v1/version", { version: "dev", commit: "none", date: "unknown", latest_version: "", update_check_enabled: false });
  json("/api/v1/billing", billing);
  json("/api/v1/settings/billing/currency", billing.display_currency);
  json("/api/v1/alerts/active", { events: [event] });
  json("/api/v1/alerts/events", { events: [event], has_more: false, next_before: 0 });
  json("/api/v1/alerts/channels", [channel]);
  json("/api/v1/alerts/channels/health", { channels: [healthChannel], banner_needed: false, banner_channel_names: [] });
  json("/api/v1/hosts/round32-fixture-host", detail);
  json("/api/v1/hosts/round32-fixture-host/metrics", series);
  json("/api/v1/hosts/round32-fixture-host/inventory", { collected_at: now, ports: [], docker: { status: "unavailable", containers: [] } });
  return { locale, fixtures: Object.fromEntries(pathSet) };
}

function makeTimestampSamples(result, locale, route, samples, kind) {
  return { locale, route: route.key, hash: route.route, kind, sample_count: samples, elapsed_ms: result.elapsedMs, dom_ready_semantics: "matching hash + route-specific synthetic marker + 3 mutation-free animation frames; no paint observation" };
}

async function measureLocale({ locale, token, scratch, overallSignal, onDecision }) {
  await assertPortsFree([BIDI_PORT]);
  const profile = mkdtempSync(join(scratch, `firefox-${locale}-`), { mode: 0o700 });
  writeFirefoxPreferences(profile, locale);
  const browserEnv = cleanChildEnvironment(join(scratch, `home-${locale}`), scratch);
  mkdirSync(browserEnv.HOME, { mode: 0o700 });
  const { fixtures } = makeFixtures(locale);
  const session = new BidiSession({ profile, env: browserEnv, signal: overallSignal, onDecision });
  activeSessions.add(session);
  const rows = [];
  let result;
  try {
    await session.start(locale, token, fixtures);
    const initial = await session.measureInitial(ROUTE_SPECS[0]);
    rows.push(makeTimestampSamples(initial, locale, ROUTE_SPECS[0], 1, "fresh-profile-first"));
    // Each route visit follows a different route, so the SPA router mounts a
    // fresh page. Ten transitions per route stay in this same warm context.
    const order = [ROUTE_SPECS[1], ROUTE_SPECS[2], ROUTE_SPECS[3], ROUTE_SPECS[4], ROUTE_SPECS[0]];
    const warm = new Map(ROUTE_SPECS.map((spec) => [spec.key, []]));
    for (let cycle = 0; cycle < SAMPLES_PER_ROUTE; cycle++) {
      for (const spec of order) {
        const result = await session.measureTransition(spec);
        warm.get(spec.key).push(result.elapsedMs);
      }
    }
    for (const spec of ROUTE_SPECS) {
      const values = warm.get(spec.key);
      if (values.length !== SAMPLES_PER_ROUTE) throw new HarnessError("warm route sample count mismatch", "INTERNAL");
      rows.push({ locale, route: spec.key, hash: spec.route, kind: "same-context-warm", sample_count: values.length,
        elapsed_ms: summarize(values), samples_ms: values,
        dom_ready_semantics: "matching hash + route-specific synthetic marker + 3 mutation-free animation frames; no paint observation" });
    }
    const pageAudit = await session.evaluate(`JSON.stringify({fetchViolationCount:window.__round32FetchViolationCount || 0,fetchViolations:window.__round32FetchViolations || [],fixtureReads:window.__round32FixtureReads || {}})`);
    const audit = JSON.parse(pageAudit);
    if (audit.fetchViolationCount || audit.fetchViolations.length) throw new HarnessError("synthetic API read policy reported a violation", "REQUEST_POLICY");
    result = { locale, rows, fixtureReads: audit.fixtureReads };
  } finally {
    try { await session.stop(); }
    finally { activeSessions.delete(session); }
  }
  result.profileRemoved = !existsSync(profile);
  if (!result.profileRemoved) throw new HarnessError("owned Firefox profile was not removed", "CLEANUP_FAILED");
  return result;
}

export function makeRunId(now = new Date(), nonce = randomUUID()) {
  return `round32-${now.toISOString().replace(/[-:.]/g, "").replace("T", "-").replace("Z", "")}-${nonce.slice(0, 8)}`;
}

async function runMeasurement(signal) {
  const runId = makeRunId();
  const paths = validateEvidencePaths(ROOT, runId);
  await assertPortsFree([HUB_PORT, BIDI_PORT]);
  if (realpathSync(APPROVED_TMP) !== APPROVED_TMP || !lstatSync(APPROVED_TMP).isDirectory()) throw new HarnessError("approved scratch must be the real /tmp/opencode directory", "UNSAFE_TMP");
  const evidenceBase = ensureDirectoryChain(ROOT, [".verification", "round32", "render-preparation", "runs"]);
  if (evidenceBase !== paths.runsDir) throw new HarnessError("evidence path changed after validation", "UNSAFE_PATH");
  const scratch = mkdtempSync(join(APPROVED_TMP, "round32-render-"), { mode: 0o700 });
  let hub = null;
  let measurement = null;
  let failure = null;
  const networkLedger = [];
  let networkAllowed = 0;
  let networkDenied = 0;
  try {
    const scratchReal = realpathSync(scratch);
    if (!scratchReal.startsWith(`${APPROVED_TMP}/`)) throw new HarnessError("scratch escaped approved root", "UNSAFE_TMP");
    const hostHome = process.env.HOME || "/home";
    const cacheEnv = cleanChildEnvironment(hostHome, scratch);
    const moduleCacheResult = await runOwnedCommand("go", ["env", "GOMODCACHE"], { env: cacheEnv, timeoutMs: 10_000, signal });
    const moduleCache = moduleCacheResult.stdout.toString("utf8").trim();
    if (!moduleCache || !moduleCache.startsWith("/")) throw new HarnessError("existing absolute Go module cache unavailable", "OFFLINE_BUILD");
    const buildHome = join(scratch, "build-home");
    mkdirSync(buildHome, { mode: 0o700 });
    const buildEnv = cleanChildEnvironment(buildHome, scratch);
    Object.assign(buildEnv, { CGO_ENABLED: "0", GOCACHE: join(scratch, "go-cache"), GOMODCACHE: moduleCache, GOFLAGS: "-mod=readonly" });
    const hubBinary = join(scratch, "cloud-pulse-hub-dev");
    await runOwnedCommand("go", ["build", "-mod=readonly", "-buildvcs=false", "-trimpath", "-o", hubBinary, "./cmd/hub"], { env: buildEnv, timeoutMs: 180_000, signal });
    const goVersion = await runOwnedCommand("go", ["version"], { env: buildEnv, timeoutMs: 10_000, signal });
    const firefoxVersionResult = await runOwnedCommand("firefox", ["--version"], { env: buildEnv, timeoutMs: 10_000, signal, stdoutCap: 4096, stderrCap: 4096 });
    const firefoxVersion = Buffer.concat([firefoxVersionResult.stdout, firefoxVersionResult.stderr]).toString("utf8").trim().slice(0, 256);
    const hubData = join(scratch, "hub-data");
    const hubHome = join(scratch, "hub-home");
    mkdirSync(hubData, { mode: 0o700 });
    mkdirSync(hubHome, { mode: 0o700 });
    const hubEnv = cleanChildEnvironment(hubHome, scratch);
    Object.assign(hubEnv, {
      CP_LISTEN: `127.0.0.1:${HUB_PORT}`, CP_DATA_DIR: hubData,
      CP_AGENT_TOKEN: HUB_AGENT_TOKEN, CP_ALLOWED_CIDRS: "127.0.0.1/32",
      CP_UPDATE_CHECK: "false", CP_BILLING: "off", CP_CLOUD_INTERVAL: "24h", CP_STORAGE_INTERVAL: "24h",
      CP_LOG_LEVEL: "error", CP_LOG_FORMAT: "json",
    });
    hub = spawn("./cloud-pulse-hub-dev", ["-listen", `127.0.0.1:${HUB_PORT}`, "-data-dir", hubData], {
      cwd: scratch, env: hubEnv, detached: true, stdio: "ignore",
    });
    hub.on("error", () => {});
    await waitForOwnedPort(hub, HUB_PORT, HUB_START_TIMEOUT_MS, signal);
    const sessionToken = await authenticateFreshHub(signal);
    const perLocale = [];
    for (const locale of ["en", "ko"]) {
      perLocale.push(await measureLocale({ locale, token: sessionToken, scratch, overallSignal: signal, onDecision: (entry) => {
        if (entry.action === "continue") networkAllowed = Math.min(networkAllowed + 1, 1_000_000);
        else networkDenied = Math.min(networkDenied + 1, 1_000_000);
        if (networkLedger.length < MAX_NETWORK_DECISIONS) networkLedger.push(entry);
      }}));
    }
    if (networkDenied) throw new HarnessError("browser attempted denied network; measurement discarded", "REQUEST_POLICY");
    measurement = {
      schema: "round32-render-readiness-v1",
      generated_at: new Date().toISOString(),
      status: "PASS",
      harness_sha256: createHash("sha256").update(readFileSync(fileURLToPath(import.meta.url))).digest("hex"),
      host: { os: process.platform, architecture: process.arch, node: process.version, go: goVersion.stdout.toString("utf8").trim(), firefox: firefoxVersion },
      build: { hub_version: (await runOwnedCommand(hubBinary, ["-version"], { env: buildEnv, cwd: scratch, timeoutMs: 5000, signal })).stdout.toString("utf8").trim(), hub_sha256: createHash("sha256").update(readFileSync(hubBinary)).digest("hex"), command: "go build -mod=readonly -buildvcs=false -trimpath ... ./cmd/hub", cgo_enabled: false, proxy: "off", sumdb: "off", module_cache: "existing local cache; downloads disabled" },
      hub: { origin: HUB_ORIGIN, port: HUB_PORT, data: "fresh private empty local DB; removed after run", billing: "off", update_check: "off", provider_credentials: "none" },
      browser: { bidi_port: BIDI_PORT, viewport: { width: 1366, height: 900 }, fresh_profile_first_route: "one initial overview per locale", warm_transitions_per_route_locale: SAMPLES_PER_ROUTE, locales: ["en", "ko"], external_requests: "blocked before network; no proxy environment inherited", redirects: "all redirect follow-up requests denied", mutations: "only owned-hub login/password auth setup is allowed; dashboard reads use synthetic fetch fixtures" },
      timing_summary_method: "linear interpolation (type 7); ten warm samples per route/locale, sample maximum observed only",
      timings: perLocale.map(({ locale, rows }) => ({ locale, rows })),
      synthetic_fixture_reads: perLocale.map(({ locale, fixtureReads }) => ({ locale, counts: fixtureReads })),
      network_policy: { allowed_request_count: networkAllowed, denied_request_count: networkDenied, observations_truncated_at: MAX_NETWORK_DECISIONS, observations: networkLedger },
      scope_limits: [
        "Hash-route timing uses browser performance.now around an explicit WebDriver BiDi navigation action; readiness requires the exact route hash, a unique populated synthetic marker, and three mutation-free animation frames.",
        "DOM readiness is not paint, compositor presentation, first contentful paint, human UX, remote latency, or an SLO. No TTFB or wire-byte metric is claimed.",
        "The single first view per locale is a fresh private Firefox profile. Ten samples per route/locale are same-context warm SPA transitions; English/Korean coverage is not UX validation.",
        "Ten-sample p95 is a small-sample interpolated percentile, not a tail-latency guarantee; observed sample maxima are not absolute worst cases.",
        "Dashboard GET data is fulfilled from synthetic in-memory fixtures. Hub network use is limited to initial admin login/password rotation and authenticated /auth/me; no report, notification, billing, storage, cloud-provider, or other POST is allowed.",
        "No screenshots are captured. The observer does not claim visual UX, native before/after comparison, product design validation, or deployment compatibility.",
      ],
    };
    measurement.cleanup = { firefox_profiles_removed: perLocale.every((entry) => entry.profileRemoved), owned_hub_stopped: false, scratch_removed: false };
  } catch (error) {
    failure = error;
  } finally {
    for (const session of activeSessions) {
      try { await session.stop(); }
      catch (error) { failure ||= error; }
    }
    activeSessions.clear();
    if (hub) {
      try { await stopOwnedGroup(hub); }
      catch (error) { failure ||= error; }
    }
    try {
      const stat = lstatSync(scratch);
      if (stat.isSymbolicLink() || !stat.isDirectory() || !realpathSync(scratch).startsWith(`${APPROVED_TMP}/`)) throw new HarnessError("owned scratch path changed; refusing cleanup", "CLEANUP_FAILED");
      rmSync(scratch, { recursive: true, force: false });
    } catch (error) { failure ||= error; }
  }
  if (failure) throw failure;
  await assertPortsFree([HUB_PORT, BIDI_PORT]);
  measurement.cleanup.owned_hub_stopped = hub.exitCode !== null || hub.signalCode !== null;
  measurement.cleanup.scratch_removed = !existsSync(scratch);
  measurement.cleanup.ports_free_after_run = true;
  if (!measurement.cleanup.owned_hub_stopped || !measurement.cleanup.scratch_removed) throw new HarnessError("owned hub/scratch cleanup incomplete", "CLEANUP_FAILED");
  publishReport(paths.outputDir, measurement);
  process.stdout.write(`Round32 render-readiness measurements saved: ${paths.outputDir}\n`);
  return measurement;
}

const activeSessions = new Set();

function writeFirefoxPreferences(profile, locale) {
  const prefs = [
    ["app.update.enabled", false], ["app.update.auto", false], ["app.update.background.enabled", false],
    ["extensions.update.enabled", false], ["extensions.getAddons.cache.enabled", false],
    ["datareporting.healthreport.uploadEnabled", false], ["datareporting.policy.dataSubmissionEnabled", false],
    ["toolkit.telemetry.enabled", false], ["toolkit.telemetry.unified", false],
    ["browser.shell.checkDefaultBrowser", false], ["network.proxy.type", 0],
    ["network.dns.disablePrefetch", true], ["network.prefetch-next", false], ["network.predictor.enabled", false],
    ["network.http.speculative-parallel-limit", 0], ["browser.urlbar.speculativeConnect.enabled", false],
    ["dom.serviceWorkers.enabled", false], ["intl.locale.requested", locale],
  ];
  writeFileSync(join(profile, "user.js"), `${prefs.map(([key, value]) => `user_pref(${JSON.stringify(key)}, ${JSON.stringify(value)});`).join("\n")}\n`, { flag: "wx", mode: 0o600 });
}

export function publishReport(outputDir, measurement, root = ROOT) {
  const runId = basename(outputDir);
  const checked = validateEvidencePaths(root, runId);
  if (checked.outputDir !== outputDir) throw new HarnessError("refusing report path outside the validated run directory", "UNSAFE_PATH");
  const payload = Buffer.from(`${JSON.stringify(measurement, null, 2)}\n`, "utf8");
  if (payload.length > MAX_OUTPUT_JSON_BYTES) throw new HarnessError("render report exceeds output-size cap", "OUTPUT_LIMIT");
  mkdirSync(outputDir, { mode: 0o700 }); // exclusive: existing/racing output directory is never replaced
  const reportPath = join(outputDir, "render-measurements.json");
  let descriptor = null;
  let ownedStat = null;
  try {
    descriptor = openSync(reportPath, "wx", 0o600);
    ownedStat = fstatSync(descriptor);
    let offset = 0;
    while (offset < payload.length) {
      const written = writeSync(descriptor, payload, offset, payload.length - offset);
      if (written <= 0) throw new HarnessError("render report write made no progress", "OUTPUT_WRITE");
      offset += written;
    }
    fsyncSync(descriptor);
    closeSync(descriptor);
    descriptor = null;
    const resultStat = lstatSync(reportPath);
    if (resultStat.isSymbolicLink() || resultStat.dev !== ownedStat.dev || resultStat.ino !== ownedStat.ino) {
      throw new HarnessError("private report path changed while being written", "UNSAFE_PATH");
    }
    return reportPath;
  } catch (error) {
    if (descriptor !== null) { try { closeSync(descriptor); } catch { /* already closed */ } }
    if (ownedStat) {
      try {
        const current = lstatSync(reportPath);
        if (!current.isSymbolicLink() && current.dev === ownedStat.dev && current.ino === ownedStat.ino) unlinkSync(reportPath);
      } catch { /* Never remove a path we cannot prove belongs to this write. */ }
    }
    try { rmdirSync(outputDir); } catch { /* Only remove our empty, unique directory. */ }
    throw error;
  }
}

export function parseCommandLine(argv) {
  if (argv.length === 0) return { run: false, help: false };
  if (argv.length === 1 && (argv[0] === "--help" || argv[0] === "-h")) return { run: false, help: true };
  if (argv.length === 1 && argv[0] === "--run") return { run: true, help: false };
  throw new HarnessError("expected only --run (after parent source-freeze scheduling) or --help", "USAGE");
}

export function usage() {
  return "usage: node scripts/measure-round32-render.mjs [--run]\n" +
    "Without --run this preparation harness does not build or launch anything.\n" +
    "--run performs bounded loopback-only Firefox render-readiness sampling; schedule only after parent confirms source freeze.\n";
}

async function main(argv = process.argv.slice(2)) {
  let options;
  try { options = parseCommandLine(argv); }
  catch (error) { process.stderr.write(`measure-round32-render: ${error.message}\n${usage()}`); return 2; }
  if (options.help || !options.run) { process.stdout.write(usage()); return 0; }
  if (process.platform !== "linux") {
    process.stderr.write("measure-round32-render: --run currently requires Linux /proc and process-group ownership checks\n");
    return 2;
  }
  const controller = new AbortController();
  let receivedSignal = 0;
  const handleSignal = (signum) => {
    if (!receivedSignal) {
      receivedSignal = signum;
      controller.abort(new HarnessError("measurement cancelled", "CANCELLED"));
    }
  };
  const onInt = () => handleSignal(2);
  const onTerm = () => handleSignal(15);
  const deadline = setTimeout(() => controller.abort(new HarnessError("overall measurement deadline exceeded", "TIMEOUT")), MAX_RUN_TIME_MS);
  process.on("SIGINT", onInt);
  process.on("SIGTERM", onTerm);
  try {
    await runMeasurement(controller.signal);
    return 0;
  } catch (error) {
    const label = error instanceof HarnessError ? error.code : error?.name || "ERROR";
    const message = String(error?.message || error).split("\n")[0].slice(0, 240);
    process.stderr.write(`measure-round32-render: ${label}: ${message}\n`);
    return receivedSignal === 2 ? 130 : receivedSignal === 15 ? 143 : 1;
  } finally {
    clearTimeout(deadline);
    process.removeListener("SIGINT", onInt);
    process.removeListener("SIGTERM", onTerm);
  }
}

const invokedDirectly = Boolean(process.argv[1]) && resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (invokedDirectly) main().then((code) => { process.exitCode = code; }).catch((error) => {
  process.stderr.write(`measure-round32-render: ${error?.name || "ERROR"}: ${String(error?.message || error).split("\n")[0].slice(0, 240)}\n`);
  process.exitCode = 1;
});
