// chart-tokens.test.mjs — token/fallback and data-binding regression tests
// for pages/charts.js. These tests do not construct a chart or invent sample
// metric points; an isolated VM covers actual token readers and theme changes.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { runInNewContext, Script } from "node:vm";
import { once } from "node:events";
import {
  parseArgs,
  showcaseHTML,
  createShowcaseServer,
  parseColor as harnessColor,
  composite,
  resolvedBackground,
  contrastRatio,
  textContrast,
  INSPECT_PAGE,
} from "../../scripts/check-design-system.mjs";

const chartsPath = fileURLToPath(new URL("../assets/js/pages/charts.js", import.meta.url));
const hostPagePath = fileURLToPath(new URL("../assets/js/pages/host.js", import.meta.url));
const bucketPagePath = fileURLToPath(new URL("../assets/js/pages/buckets.js", import.meta.url));
const tokensPath = fileURLToPath(new URL("../src/tokens.css", import.meta.url));
const chartsSource = readFileSync(chartsPath, "utf8");
const hostPageSource = readFileSync(hostPagePath, "utf8");
const bucketPageSource = readFileSync(bucketPagePath, "utf8");
const tokensSource = readFileSync(tokensPath, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

const SERIES_TOKENS = {
  cpu: "--chart-cp-cpu",
  mem: "--chart-cp-mem",
  disk: "--chart-cp-disk",
  netRx: "--chart-cp-net-rx",
  netTx: "--chart-cp-net-tx",
  diskRead: "--chart-cp-disk-read",
  diskWrite: "--chart-cp-disk-write",
  load1: "--chart-cp-load1",
  sparkline: "--chart-cp-sparkline",
};

function parseColor(raw) {
  if (typeof raw !== "string") return null;
  const value = raw.trim().toLowerCase();
  const hex = value.match(/^#([\da-f]{3,4}|[\da-f]{6}|[\da-f]{8})$/i);
  if (hex) {
    let digits = hex[1];
    if (digits.length <= 4) digits = [...digits].map((digit) => `${digit}${digit}`).join("");
    return [
      Number.parseInt(digits.slice(0, 2), 16),
      Number.parseInt(digits.slice(2, 4), 16),
      Number.parseInt(digits.slice(4, 6), 16),
      digits.length === 8 ? Number.parseInt(digits.slice(6, 8), 16) / 255 : 1,
    ];
  }
  const rgb = value.match(/^rgba?\((.*)\)$/);
  if (!rgb) return null;
  const parts = rgb[1].replaceAll(",", " ").replace("/", " ").trim().split(/\s+/);
  if (parts.length < 3 || parts.length > 4) return null;
  const channel = (part) => part.endsWith("%") ? Number.parseFloat(part) * 2.55 : Number.parseFloat(part);
  const alpha = (part) => part === undefined ? 1 : part.endsWith("%") ? Number.parseFloat(part) / 100 : Number.parseFloat(part);
  const result = [channel(parts[0]), channel(parts[1]), channel(parts[2]), alpha(parts[3])];
  return result.every(Number.isFinite) ? result : null;
}

function tokenMaps() {
  const light = new Map();
  const dark = new Map();
  // Include @theme aliases, then apply runtime blocks in source order.
  // These selectors have equal specificity; a later :root wins in dark too.
  for (const [, selector, body] of tokensSource.matchAll(/(?:^|\n)\s*(@theme|:root|\.dark)\s*\{([^{}]*)\}/g)) {
    for (const [, name, value] of body.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) {
      if (selector !== ".dark") light.set(name, value.trim());
      dark.set(name, value.trim());
    }
  }
  return { light, dark };
}

function resolvedToken(values, name, seen = new Set()) {
  assert.ok(!seen.has(name), `cyclic CSS token alias: ${name}`);
  const next = new Set(seen).add(name);
  return (values.get(name) || "").replace(/var\(\s*(--[\w-]+)\s*(?:,\s*([^()]+))?\)/g, (_, alias, fallback) => resolvedToken(values, alias, next) || fallback?.trim() || "");
}

function chartSandbox(tokenValues, { theme = "light" } = {}) {
  let activeTokens = tokenValues;
  let activeTheme = theme;
  const root = { classList: { contains: (name) => name === "dark" && activeTheme === "dark" } };
  const context = {
    uPlot: class { constructor() { throw new Error("these tests must not construct charts or metric data"); } },
    t: (key) => key,
  };
  context.document = { documentElement: root };
  context.getComputedStyle = (element) => {
    assert.equal(element, root, "chart tokens should be read from document.documentElement");
    return { getPropertyValue: (name) => resolvedToken(activeTokens, name) };
  };
  // Run the checked-out module body unchanged except for ESM syntax and
  // imports. Vendored uPlot/i18n are not loaded, nor is any canvas created.
  const executable = chartsSource.replace(/^import\s+.*;\s*$/gm, "").replace(/^export\s+/gm, "");
  const api = runInNewContext(`${executable}\n({ SERIES_COLORS, readThemeColors, baseOpts });`, context, { filename: chartsPath, timeout: 1000 });
  return { api, setTokens: (values, nextTheme = "light") => { activeTokens = values; activeTheme = nextTheme; } };
}

function colorsEqual(first, second) {
  const a = parseColor(first);
  const b = parseColor(second);
  return Boolean(a && b && a.every((part, index) => Math.abs(part - b[index]) < 0.01));
}

test("SERIES_COLORS keeps its key API and resolves real CSS chart-token colors", () => {
  const { light, dark } = tokenMaps();
  for (const token of Object.values(SERIES_TOKENS)) {
    assert.ok(light.get(token), `${token} must be declared in web/src/tokens.css`);
    assert.ok(parseColor(resolvedToken(light, token)), `${token} must resolve to a complete usable color`);
  }

  const { SERIES_COLORS } = chartSandbox(light).api;
  assert.deepEqual(Object.keys(SERIES_COLORS).sort(), Object.keys(SERIES_TOKENS).sort());
  for (const [series, token] of Object.entries(SERIES_TOKENS)) {
    assert.ok(parseColor(SERIES_COLORS[series]), `${series} resolves to a valid CSS color`);
    assert.ok(colorsEqual(SERIES_COLORS[series], resolvedToken(light, token)), `${series} should use ${token}`);
  }

  const darkModule = chartSandbox(dark, { theme: "dark" }).api;
  for (const [series, token] of Object.entries(SERIES_TOKENS)) {
    assert.ok(parseColor(darkModule.SERIES_COLORS[series]), `${series} resolves to a valid dark-theme color`);
    assert.ok(colorsEqual(darkModule.SERIES_COLORS[series], resolvedToken(dark, token)), `${series} follows ${token} under dark tokens`);
  }
});

test("SERIES_COLORS has valid fallbacks when computed CSS tokens are absent", () => {
  const { SERIES_COLORS } = chartSandbox(new Map()).api;
  assert.deepEqual(Object.keys(SERIES_COLORS).sort(), Object.keys(SERIES_TOKENS).sort());
  for (const [series, color] of Object.entries(SERIES_COLORS)) {
    assert.ok(parseColor(color), `${series} fallback should be a valid color, got ${JSON.stringify(color)}`);
  }
  const { axis, grid } = chartSandbox(new Map()).api.readThemeColors();
  assert.ok(parseColor(axis), "axis fallback should be a complete color");
  assert.ok(parseColor(grid), "grid fallback should be a complete color");
});

test("SERIES_COLORS observes the active CSS palette on theme changes and supplies valid area-fill colors", () => {
  const { light, dark } = tokenMaps();
  const sandbox = chartSandbox(light);
  for (const [values, theme] of [[light, "light"], [dark, "dark"], [light, "light"]]) {
    sandbox.setTokens(values, theme);
    for (const [series, token] of Object.entries(SERIES_TOKENS)) {
      const color = sandbox.api.SERIES_COLORS[series];
      assert.ok(colorsEqual(color, resolvedToken(values, token)), `${series} should observe the current ${theme} palette`);
      // Actual chart code appends hex alpha to its series color, so a
      // complete but non-hex rgb() string is not sufficient for this API.
      if (/fill:\s*`\$\{s\.color\}26`/.test(chartsSource)) assert.ok(parseColor(`${color}26`), `${series} must support the actual area fill`);
      if (series === "sparkline" && /fill:\s*`\$\{color\}22`/.test(chartsSource)) assert.ok(parseColor(`${color}22`), "sparkline must support the actual area fill");
    }
  }
});

test("chart axes read theme tokens per chart creation and the token source supports both themes", () => {
  const { light, dark } = tokenMaps();
  for (const name of ["--chart-cp-axis", "--chart-cp-grid"]) {
    assert.ok(parseColor(resolvedToken(light, name)), `${name} needs a valid light-theme color`);
    assert.ok(parseColor(resolvedToken(dark, name)), `${name} needs a valid dark-theme color`);
    assert.ok(!colorsEqual(resolvedToken(light, name), resolvedToken(dark, name)), `${name} must change with the theme`);
  }

  const readThemeColors = chartsSource.match(/function readThemeColors\(\)\s*\{([\s\S]*?)\n\}/);
  assert.ok(readThemeColors, "readThemeColors should remain the chart-axis token boundary");
  assert.match(readThemeColors[1], /readToken\(["']--chart-cp-axis["']/);
  assert.match(readThemeColors[1], /readToken\(["']--chart-cp-grid["']/);
  assert.match(chartsSource, /function baseOpts\([\s\S]*?const colors = readThemeColors\(\)/);

  const sandbox = chartSandbox(light);
  for (const [values, theme] of [[light, "light"], [dark, "dark"], [light, "light"]]) {
    sandbox.setTokens(values, theme);
    const colors = sandbox.api.readThemeColors();
    assert.equal(colors.axis, resolvedToken(values, "--chart-cp-axis"));
    assert.equal(colors.grid, resolvedToken(values, "--chart-cp-grid"));
    // Actual options only: no uPlot invocation or metric arrays.
    for (const axis of sandbox.api.baseOpts(360, 220, "").axes) {
      assert.equal(axis.stroke, colors.axis);
      assert.equal(axis.grid.stroke, colors.grid);
      assert.equal(axis.ticks.stroke, colors.grid);
    }
  }
});

test("time-series and sparkline constructors preserve caller-provided API-bound arrays", () => {
  assert.match(chartsSource, /export function createTimeSeriesChart\(container, opts\)/);
  assert.match(chartsSource, /const \{ title, series, timestamps, values,[\s\S]*?\} = opts;/);
  assert.match(chartsSource, /const data = \[timestamps, \.\.\.values\];/);
  assert.match(chartsSource, /return new ChartHandle\(container, options, data\);/);

  assert.match(chartsSource, /export function createSparkline\(container, timestamps, values,/);
  assert.match(chartsSource, /new ChartHandle\(container, options, \[timestamps, values\]\)/);

  // Real callers continue passing the host API response series and bucket
  // history arrays through; these source assertions intentionally create no
  // mock samples or numeric metric points.
  assert.match(hostPageSource, /timestamps:\s*series\.ts/);
  assert.match(hostPageSource, /values:\s*spec\.values/);
  assert.match(hostPageSource, /series:\s*spec\.series/);
  assert.match(bucketPageSource, /view\.history\.map\(\(p\)\s*=>\s*p\.ts\)/);
  assert.match(bucketPageSource, /view\.history\.map\(\(p\)\s*=>\s*p\.requests_window\)/);
  assert.match(bucketPageSource, /createSparkline\(sparkWrap, timestamps, values, SERIES_COLORS\.sparkline\)/);
});

// Harness-only checks live here to keep the verifier's write scope bounded.
// Nothing below starts Firefox, loads main.js, or reaches the live hub.
test("design-system CLI refuses the live port, non-loopback hosts, and malformed arguments", () => {
  assert.deepEqual(parseArgs([]), { host: "127.0.0.1", port: 18090 });
  assert.deepEqual(parseArgs(["--port", "18091"]), { host: "127.0.0.1", port: 18091 });
  for (const args of [
    ["--port", "8090"], ["--port", "0"], ["--port", "65536"],
    ["--port", "18090suffix"], ["--port"], ["--host", "0.0.0.0"],
    ["--host", "example.com"], ["--host", "::"], ["--hub", "http://127.0.0.1:8090"],
  ]) assert.throws(() => parseArgs(args), `must reject ${JSON.stringify(args)}`);
  assert.throws(() => createShowcaseServer(8090), /refusing port 8090/);
});

test("showcase contains actual asset references and labeled empty slots, never metric samples or app bootstrap", () => {
  for (const lang of ["ko", "en"]) {
    const html = showcaseHTML(lang);
    assert.match(html, new RegExp(`<html lang="${lang}">`));
    assert.match(html, /href="\/assets\/app\.css"/);
    assert.match(html, /src="\/assets\/js\/theme-init\.js"/);
    assert.match(html, /data-empty-sparkline/);
    assert.match(html, /data-empty-bar/);
    assert.match(html, lang === "ko" ? /미확인/ : /No data/);
    assert.doesNotMatch(html, /<\s*(?:form|canvas)\b|type="(?:submit|password)"|aria-valuenow|cp-bar-fill|\/api\/|main\.js|fetch\(/i);
    assert.equal((html.match(/<svg/g) || []).length, 1, "only the decorative select chevron, never a chart");
    assert.match(html, /data-showcase-controls/);
    assert.equal((html.match(/<button type="button"/g) || []).length, 4);
    for (const component of ["cp-card-compact", "cp-stat-card-stack", "cp-dot-lg", "cp-bar-track-lg", "cp-status-pill-critical", "cp-badge-hub", "cp-badge-update", "cp-chip-critical", "cp-settings-nav-link-active", "data-long-label"]) {
      assert.ok(html.includes(component), `${component} needs browser evidence`);
    }
    const visibleText = html.replace(/<style>[\s\S]*?<\/style>/g, "").replace(/<[^>]*>/g, "").replace(/\bR2\b/g, "R-two");
    assert.doesNotMatch(visibleText, /\d/, "R2 is a brand, not invented measurement data");
  }
  // This compiles the real expression sent to Firefox, including escaping.
  assert.doesNotThrow(() => new Script(INSPECT_PAGE));
  const numericCheck = INSPECT_PAGE.match(/visibleTextHasNumber:\s*(.*),\n/)[1];
  assert.equal(runInNewContext(numericCheck, { document: { body: { innerText: "R2 No data" } } }), false);
  assert.equal(runInNewContext(numericCheck, { document: { body: { innerText: "unexpected digit7" } } }), true);
});

test("gallery artifacts and Firefox scratch stay beneath the repository", () => {
  const harness = readFileSync(new URL("../../scripts/check-design-system.mjs", import.meta.url), "utf8");
  assert.match(harness, /const OUT_ROOT = resolve\(SCRIPT_DIR, "\.\.\/\.verification\/phase1"\)/);
  assert.doesNotMatch(harness, /\/tmp\/opencode|tmpdir\(/);
  for (const key of ["TMPDIR", "TMP", "TEMP"]) assert.match(harness, new RegExp(`${key}: join\\(OUT_ROOT, "tmp"\\)`));
  assert.match(harness, /origin: "document"/, "capture every component, not just the top viewport");
});

test("harness color math composites ancestor tints and never treats unresolved colors as passing", () => {
  assert.deepEqual(harnessColor("rgb(100% 0% 0% / 50%)"), [254.99999999999997, 0, 0, 0.5]);
  assert.deepEqual(harnessColor("#fff"), [255, 255, 255, 1]);
  assert.deepEqual(harnessColor("transparent"), [0, 0, 0, 0]);
  for (const raw of ["rgb(#ef4444)", "rgb(255oops 0 0)", "var(--color-cp-ok)", "255 0 0", ""]) {
    assert.equal(harnessColor(raw), null, `must reject ${raw}`);
  }
  assert.deepEqual(composite([255, 0, 0, 0.5], [255, 255, 255, 1]), [255, 127.5, 127.5, 1]);
  assert.deepEqual(resolvedBackground(["rgba(255, 0, 0, 0.5)", "transparent", "#fff"]), [255, 127.5, 127.5, 1]);
  assert.equal(resolvedBackground(["rgb(#fff)", "#fff"]), null);
  assert.equal(resolvedBackground(["transparent"])[3], 0, "no made-up opaque canvas");
  assert.equal(contrastRatio([0, 0, 0, 1], [255, 255, 255, 1]), 21);
  assert.ok(textContrast({ backgroundImages: ["none"] }, [0, 0, 0, 0.5], [255, 255, 255, 1]) < 4.5, "foreground alpha must be composited too");
  assert.ok(Number.isNaN(textContrast({}, [0, 0, 0, 1], null)), "unknown backgrounds cannot pass AA");
  assert.ok(Number.isNaN(textContrast({ backgroundImages: ["linear-gradient(red, blue)"] }, [0, 0, 0, 1], [255, 255, 255, 1])), "do not invent a background behind an image");
});

test("own loopback static server serves actual assets and no app or traversal routes", { timeout: 10_000 }, async () => {
  const { server, traffic } = createShowcaseServer(18090);
  // The CLI uses --port; this unit test binds a fresh ephemeral test socket,
  // never attaches to an existing process or the live application port.
  server.listen(0, "127.0.0.1");
  try {
    await once(server, "listening");
    const { address, port } = server.address();
    assert.equal(address, "127.0.0.1");
    assert.notEqual(port, 8090);
    const origin = `http://127.0.0.1:${port}`;
    const request = (path, options = {}) => fetch(`${origin}${path}`, { ...options, signal: AbortSignal.timeout(5000) });
    const gallery = await request("/__showcase?lang=ko");
    assert.equal(gallery.status, 200);
    assert.equal(await gallery.text(), showcaseHTML("ko"));
    assert.match(gallery.headers.get("content-security-policy"), /default-src 'self'/);
    const css = await request("/assets/app.css");
    assert.equal(css.status, 200);
    assert.equal(await css.text(), readFileSync(new URL("../assets/app.css", import.meta.url), "utf8"));
    const theme = await request("/assets/js/theme-init.js");
    assert.equal(theme.status, 200);
    assert.equal(await theme.text(), readFileSync(new URL("../assets/js/theme-init.js", import.meta.url), "utf8"));
    assert.equal((await request("/__showcase?lang=xx")).status, 400);
    assert.equal((await request("/__showcase?route=dashboard")).status, 400);
    assert.equal((await request("/__showcase", { method: "POST" })).status, 405);
    assert.equal((await request("/")).status, 404);
    assert.equal((await request("/index.html")).status, 404);
    assert.equal((await request("/assets/%2e%2e%2ftokens.css")).status, 403);
    assert.equal((await request("/__showcase", { method: "HEAD" })).status, 200);
    assert.deepEqual(traffic.apiRequests, [], "no API or auth requests, even in tests");
    assert.ok(traffic.servedAssets.includes("app.css"));
    assert.equal(traffic.assetHashes.get("app.css").size, 1);
  } finally {
    server.closeAllConnections();
    await new Promise((resolveClose) => server.close(resolveClose));
  }
});
