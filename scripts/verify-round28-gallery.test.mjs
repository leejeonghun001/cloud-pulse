import test from "node:test";
import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, symlinkSync } from "node:fs";
import { join } from "node:path";
import { REPO_ROOT, ROUND28_ROOT } from "./verify-round28-safety.mjs";
import { createShowcaseServer, listen, parseArgs, prepareGalleryArtifacts, resolveArtifactRoot, validateTraffic } from "./check-design-system.mjs";

const root = join(ROUND28_ROOT, "audit/tooling");
mkdirSync(root, { recursive: true, mode: 0o700 });
const fixture = () => mkdtempSync(join(root, "gallery-unit-"));

// Exercise the actual handler without listening, starting Firefox or serving
// a gallery to any browser while parallel product writers are active.
async function request(server, path, method = "GET") {
  const response = new EventEmitter();
  response.statusCode = 200;
  response.writeHead = (status, headers) => { response.statusCode = status; response.headers = headers; return response; };
  const ended = new Promise((resolve) => {
    response.end = (body) => { response.body = body; response.emit("finish"); resolve(response); return response; };
  });
  server.emit("request", { method, url: path }, response);
  return ended;
}

test("gallery preserves existing CLI defaults and accepts an explicit Round28 artifact root", () => {
  assert.deepEqual(parseArgs([]), { host: "127.0.0.1", port: 18090 });
  assert.equal(parseArgs(["--artifact-root", ROUND28_ROOT]).artifactRoot, ROUND28_ROOT);
  assert.equal(resolveArtifactRoot(ROUND28_ROOT), ROUND28_ROOT);
  assert.throws(() => parseArgs(["--artifact-root"]), /requires a value/);
  assert.throws(() => parseArgs(["--host", "0.0.0.0"]), /non-loopback/);
  assert.throws(() => parseArgs(["--port", "8090"]), /refusing port 8090/);
  assert.throws(() => createShowcaseServer(8090), /refusing port 8090/);
});

test("gallery rejects source-tree and prefix-lookalike artifact roots without writing", () => {
  const sourceTarget = join(REPO_ROOT, "web/round28-gallery-rejected-root");
  const lookalike = join(REPO_ROOT, ".verification-not-a-root/gallery");
  for (const path of [sourceTarget, lookalike]) {
    assert.throws(() => parseArgs(["--artifact-root", path]), /must be inside/);
    assert.throws(() => prepareGalleryArtifacts({ artifactRoot: path }), /must be inside/);
    assert.equal(existsSync(path), false);
  }
});

test("gallery rejects dangling symlink ancestor before creating output or scratch", () => {
  const directory = fixture();
  const target = join(directory, "must-not-be-created");
  try {
    symlinkSync(target, join(directory, "escape"));
    const output = join(directory, "escape/nested");
    assert.throws(() => parseArgs(["--artifact-root", output]), /symlink/);
    assert.throws(() => prepareGalleryArtifacts({ artifactRoot: output }), /symlink/);
    assert.equal(existsSync(target), false);
    assert.deepEqual(readdirSync(directory), ["escape"]);
  } finally { rmSync(directory, { recursive: true, force: true }); }
});

test("gallery validates nested cache ancestors before any environment mkdir", () => {
  const directory = fixture();
  try {
    symlinkSync(join(directory, "absent"), join(directory, "cache"));
    assert.throws(() => prepareGalleryArtifacts({ artifactRoot: directory }), /symlink/);
    assert.equal(existsSync(join(directory, "tmp")), false);
    assert.deepEqual(readdirSync(directory), ["cache"]);
  } finally { rmSync(directory, { recursive: true, force: true }); }
});

test("Round28 artifact preparation puts reports, profiles and every subprocess path under its root", () => {
  const directory = fixture();
  try {
    const prepared = prepareGalleryArtifacts({ artifactRoot: directory });
    assert.equal(prepared.root, directory);
    assert.ok(prepared.runDir.startsWith(`${directory}/run-`));
    assert.ok(prepared.reportPath.startsWith(`${prepared.runDir}/`));
    for (const name of ["TMPDIR", "TMP", "TEMP", "HOME", "GOCACHE", "GOMODCACHE", "PYTHONPYCACHEPREFIX", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"]) {
      assert.ok(prepared.env[name].startsWith(`${directory}/`), name);
    }
    // Only preparation is exercised: neither a report nor a browser exists.
    assert.equal(existsSync(prepared.reportPath), false);
  } finally { rmSync(directory, { recursive: true, force: true }); }
});

test("gallery denies and accounts for API/auth requests without opening a socket", async () => {
  const { server, traffic } = createShowcaseServer(18090);
  for (const path of ["/api", "/api/v1/auth/me", "/login", "/logout", "/auth/session", "/signin", "/signout", "/%61pi/v1/hosts"]) {
    assert.equal((await request(server, path)).statusCode, 404);
  }
  assert.equal(server.listening, false);
  assert.equal(traffic.apiRequests.length, 8);
  const report = { failures: [] }, result = { scenario: "api-denial", checks: [] };
  validateTraffic(report, result, traffic);
  assert.ok(report.failures.some((item) => item.check === "no API/auth routes requested"));
});

test("occupied-port bind errors reject instead of attaching to an existing server", async () => {
  const server = new EventEmitter();
  server.listen = () => { const error = new Error("unit-only occupied port"); error.code = "EADDRINUSE"; server.emit("error", error); };
  await assert.rejects(listen(server, 18090), { code: "EADDRINUSE" });
});

test("requested missing and traversal assets fail explicitly even with an empty console", async () => {
  const { server, traffic } = createShowcaseServer(18090);
  assert.equal((await request(server, "/assets/round28-missing-unit-only.js")).statusCode, 404);
  assert.equal((await request(server, "/assets/%2e%2e%2ftokens.css")).statusCode, 403);
  assert.deepEqual(traffic.failedAssets.map((item) => item.status), [404, 403]);
  const report = { failures: [] }, result = { scenario: "missing-asset", checks: [] };
  validateTraffic(report, result, traffic, [{ logs: [], networkFailures: [] }]);
  assert.ok(report.failures.some((item) => item.check === "no requested assets failed"));
  assert.equal(server.listening, false);
});

test("console/JavaScript and failed asset network events cannot be reported as passing", () => {
  const traffic = { apiRequests: [], failedAssets: [] };
  const report = { failures: [] }, result = { scenario: "browser-errors", checks: [] };
  validateTraffic(report, result, traffic, [{ logs: [{ type: "javascript", text: "unit-only exception" }], networkFailures: [{ url: "/assets/failed.js" }] }]);
  assert.equal(report.failures.length, 2);
});

test("npm manifest is private, dependency-free and delegates the complete existing JS suite", () => {
  const manifest = JSON.parse(readFileSync(join(REPO_ROOT, "package.json"), "utf8"));
  assert.equal(manifest.private, true);
  assert.equal(manifest.type, "module");
  assert.equal(manifest.dependencies, undefined);
  assert.equal(manifest.devDependencies, undefined);
  assert.equal(manifest.scripts.test, "make test-js", "portable npm entrypoint delegates the existing complete JS suite; Round28 safety is a separate explicit gate");
});
