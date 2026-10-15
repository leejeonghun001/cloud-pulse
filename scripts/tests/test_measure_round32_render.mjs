import test from "node:test";
import assert from "node:assert/strict";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import {
  APPROVED_TMP,
  HarnessError,
  ROUTE_SPECS,
  SAMPLES_PER_ROUTE,
  classifyNetworkRequest,
  fixtureForRequest,
  parseCommandLine,
  parseListeningSocketInodes,
  percentile,
  publishReport,
  routeReadySnapshot,
  runOwnedCommand,
  summarize,
  validateEvidencePaths,
} from "../measure-round32-render.mjs";

function tempRoot() {
  return mkdtempSync(join(APPROVED_TMP, "round32-render-test-"));
}

test("preparation CLI is inert unless the parent-scheduled --run flag is explicit", () => {
  assert.deepEqual(parseCommandLine([]), { run: false, help: false });
  assert.deepEqual(parseCommandLine(["--help"]), { run: false, help: true });
  assert.deepEqual(parseCommandLine(["--run"]), { run: true, help: false });
  assert.throws(() => parseCommandLine(["--hub", "http://127.0.0.1:8090"]), /only --run/);
});

test("routes have unique, populated fixture markers and fixed sample design", () => {
  assert.equal(ROUTE_SPECS.length, 5);
  assert.equal(SAMPLES_PER_ROUTE, 10);
  assert.equal(new Set(ROUTE_SPECS.map((route) => route.route)).size, 5);
  assert.equal(new Set(ROUTE_SPECS.map((route) => route.marker)).size, 5);
  for (const route of ROUTE_SPECS) {
    assert.ok(route.selector.length > 0);
    assert.ok(route.marker.startsWith("Round32 "));
  }
  assert.equal(fixtureForRequest("GET", "/api/v1/hosts").hosts[0].host.hostname, "Round32 overview fixture host");
  assert.equal(fixtureForRequest("GET", "/api/v1/hosts/round32-fixture-host").host.hostname, "Round32 host-detail fixture marker");
  assert.equal(fixtureForRequest("GET", "/api/v1/alerts/events").events[0].rule_name, "Round32 Activity fixture rule");
  assert.equal(fixtureForRequest("GET", "/api/v1/billing").hosts[0].hostname, "Round32 costs fixture host");
  assert.equal(fixtureForRequest("GET", "/api/v1/alerts/channels")[0].name, "Round32 notification fixture channel");
});

test("readiness requires exact route, unique populated marker, and stable frames", () => {
  const route = ROUTE_SPECS[0];
  assert.equal(routeReadySnapshot({ hash: route.route, markerText: route.marker, stableMutationFreeFrames: 3 }, route), true);
  assert.equal(routeReadySnapshot({ hash: ROUTE_SPECS[1].route, markerText: route.marker, stableMutationFreeFrames: 8 }, route), false, "stale route must not pass");
  assert.equal(routeReadySnapshot({ hash: route.route, markerText: "old content", stableMutationFreeFrames: 8 }, route), false, "stale DOM marker must not pass");
  assert.equal(routeReadySnapshot({ hash: route.route, markerText: route.marker, stableMutationFreeFrames: 2 }, route), false, "insufficient stable frames must not pass");
});

test("network policy allows only loopback static resources and owned auth; denies origins, redirects, API passthrough, and writes", () => {
  assert.equal(classifyNetworkRequest({ url: "http://127.0.0.1:18142/assets/js/main.js" }).allow, true);
  assert.equal(classifyNetworkRequest({ url: "http://127.0.0.1:18142/api/v1/auth/me" }).allow, true);
  assert.equal(classifyNetworkRequest({ url: "http://127.0.0.1:18142/api/v1/auth/password", method: "POST" }).allow, true);
  assert.equal(classifyNetworkRequest({ url: "http://127.0.0.1:18142/api/v1/hosts" }).allow, false, "dashboard reads must be fixture-fulfilled, not real hub reads");
  assert.equal(classifyNetworkRequest({ url: "http://127.0.0.1:18142/api/v1/alerts/channels/test", method: "POST" }).reason, "non-auth-api-mutation");
  assert.equal(classifyNetworkRequest({ url: "https://example.invalid/" }).reason, "non-owned-origin");
  assert.equal(classifyNetworkRequest({ url: "http://127.0.0.1:18142/login", redirectCount: 1 }).reason, "redirect");
  assert.equal(classifyNetworkRequest({ url: "http://user@127.0.0.1:18142/" }).reason, "non-owned-origin");
});

test("percentiles preserve type-7 timing arithmetic", () => {
  const values = Array.from({ length: 10 }, (_, index) => index + 1);
  assert.equal(percentile(values, 50), 5.5);
  assert.equal(percentile(values, 95), 9.55);
  assert.equal(summarize(values).n, 10);
  assert.equal(summarize(values).sample_max, 10);
  assert.equal(summarize([]).p95, null);
});

test("proc tcp parser only reports owned LISTEN inodes for the requested port", () => {
  const fixture = "sl local_address rem_address st tx_queue:rx_queue tr:tm->when retrnsmt uid timeout inode\n" +
    "0: 0100007F:46DA 00000000:0000 0A 00000000:00000000 00:00000000 00000000 1000 0 43210\n" +
    "1: 0100007F:46DA 0100007F:0001 01 00000000:00000000 00:00000000 00000000 1000 0 43211\n";
  assert.deepEqual([...parseListeningSocketInodes(fixture, 18138)], ["43210"]);
  assert.deepEqual([...parseListeningSocketInodes(fixture, 18142)], []);
});

test("evidence ancestors reject symlinks, non-directories, and existing run outputs", () => {
  const root = tempRoot();
  try {
    const missing = validateEvidencePaths(root, "round32-test-run");
    assert.ok(missing.outputDir.endsWith("round32-test-run"));
    mkdirSync(join(root, ".verification"), { mode: 0o700 });
    writeFileSync(join(root, ".verification", "round32"), "not a directory");
    assert.throws(() => validateEvidencePaths(root, "round32-other-run"), /unsafe evidence ancestor/);
  } finally { rmSync(root, { recursive: true, force: true }); }

  const root2 = tempRoot();
  try {
    const link = join(root2, ".verification");
    const target = join(root2, "real");
    mkdirSync(target);
    // Build the parent first, then plant a symlink exactly where validation checks it.
    const verification = join(root2, "verify-target");
    mkdirSync(verification);
    symlinkSync(verification, link, "dir");
    assert.throws(() => validateEvidencePaths(root2, "round32-test"), /unsafe evidence ancestor/);
    assert.ok(existsSync(target));
  } finally {
    rmSync(root2, { recursive: true, force: true });
  }
});

test("report publication is exclusive and preserves a competing destination", () => {
  const root = tempRoot();
  try {
    const paths = validateEvidencePaths(root, "round32-safe-run");
    // Create only the guarded ancestors as this future writer would.
    mkdirSync(join(root, ".verification", "round32", "render-preparation", "runs"), { recursive: true, mode: 0o700 });
    const reportPath = publishReport(paths.outputDir, { status: "fixture", samples: [1, 2] }, root);
    assert.equal(JSON.parse(readFileSync(reportPath, "utf8")).status, "fixture");
    assert.throws(() => publishReport(paths.outputDir, { status: "replace" }, root), /evidence destination exists/);
    assert.equal(JSON.parse(readFileSync(reportPath, "utf8")).status, "fixture");
  } finally { rmSync(root, { recursive: true, force: true }); }
});

function ownedDescendantFixture(root, mode = "exit") {
  const marker = join(root, `stopped-${mode}`);
  const pidFile = join(root, `pids-${mode}`);
  const readyFile = join(root, `ready-${mode}`);
  const childSource = `process.on('SIGTERM',()=>{require('node:fs').writeFileSync(${JSON.stringify(marker)},'term');process.exit(0)});require('node:fs').writeFileSync(${JSON.stringify(readyFile)},'ready');setInterval(()=>{},1000);`;
  const parentSource = `const {spawn}=require('node:child_process');const fs=require('node:fs');const c=spawn(process.execPath,['-e',${JSON.stringify(childSource)}],{stdio:'ignore'});const end=Date.now()+3000;while(!fs.existsSync(${JSON.stringify(readyFile)})&&Date.now()<end)Atomics.wait(new Int32Array(new SharedArrayBuffer(4)),0,0,10);fs.writeFileSync(${JSON.stringify(pidFile)},String(process.pid)+' '+String(c.pid));${mode.startsWith("sleep") ? "setInterval(()=>{},1000);" : mode === "fail" ? "process.exit(7);" : "process.exit(0);"}`;
  return { command: process.execPath, args: ["-e", parentSource], marker, pidFile };
}

async function groupIsGone(pgid) {
  const end = Date.now() + 2000;
  while (Date.now() < end) {
    try { process.kill(-pgid, 0); }
    catch (error) { if (error.code === "ESRCH") return true; throw error; }
    await new Promise((resolveWait) => setTimeout(resolveWait, 20));
  }
  try { process.kill(-pgid, 0); return false; }
  catch (error) { return error.code === "ESRCH"; }
}

test("owned command cleanup kills descendants even after leader success/failure/timeout/cancel", async (t) => {
  const root = tempRoot();
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const env = { PATH: process.env.PATH || "/usr/bin:/bin", HOME: root, TMPDIR: root };
  for (const mode of ["exit", "fail"]) {
    const fixture = ownedDescendantFixture(root, mode);
    if (mode === "fail") await assert.rejects(runOwnedCommand(fixture.command, fixture.args, { env, cwd: root, timeoutMs: 5000 }), /exited 7/);
    else assert.equal((await runOwnedCommand(fixture.command, fixture.args, { env, cwd: root, timeoutMs: 5000 })).stdoutBytes, 0);
    const [leader] = readFileSync(fixture.pidFile, "utf8").split(" ").map(Number);
    assert.ok(existsSync(fixture.marker));
    assert.equal(await groupIsGone(leader), true);
  }
  const timeoutFixture = ownedDescendantFixture(root, "sleep");
  await assert.rejects(runOwnedCommand(timeoutFixture.command, timeoutFixture.args, { env, cwd: root, timeoutMs: 1000 }), /timed out/);
  const [timeoutLeader] = readFileSync(timeoutFixture.pidFile, "utf8").split(" ").map(Number);
  assert.ok(existsSync(timeoutFixture.marker));
  assert.equal(await groupIsGone(timeoutLeader), true);

  const cancelFixture = ownedDescendantFixture(root, "sleep-cancel");
  const controller = new AbortController();
  const timer = setInterval(() => {
    if (existsSync(cancelFixture.pidFile)) controller.abort(new HarnessError("fixture cancellation", "CANCELLED"));
  }, 10);
  try {
    await assert.rejects(runOwnedCommand(cancelFixture.command, cancelFixture.args, { env, cwd: root, timeoutMs: 5000, signal: controller.signal }), /fixture cancellation/);
  } finally { clearInterval(timer); }
  const [cancelLeader] = readFileSync(cancelFixture.pidFile, "utf8").split(" ").map(Number);
  assert.ok(existsSync(cancelFixture.marker));
  assert.equal(await groupIsGone(cancelLeader), true);
});

test("owned command stdout and stderr retain only their byte caps and timeout kills group", async (t) => {
  const root = tempRoot();
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const env = { PATH: process.env.PATH || "/usr/bin:/bin", HOME: root, TMPDIR: root };
  const code = "process.stdout.write('x'.repeat(100000));process.stderr.write('y'.repeat(100000));setInterval(()=>{},1000)";
  await assert.rejects(runOwnedCommand(process.execPath, ["-e", code], { env, cwd: root, timeoutMs: 3000, stdoutCap: 128, stderrCap: 128 }), /byte cap/);
});
