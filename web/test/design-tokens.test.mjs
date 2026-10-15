// Source and generated-CSS guards. Browser evidence remains a separate check.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join, relative } from "node:path";
import { contrastRatio, parseColor } from "../../scripts/ui-audit-lib.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const srcDir = join(here, "..", "src");
const stripComments = (css) => css.replace(/\/\*[\s\S]*?\*\//g, "");
const tokensCSS = stripComments(readFileSync(join(srcDir, "tokens.css"), "utf8"));
const escape = (value) => value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

// Match actual, balanced leaf rules, not selector names quoted in comments.
function rules(css) {
  return [...stripComments(css).matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((match) => ({
    selectors: match[1].trim().split(",").map((selector) => selector.trim()),
    body: match[2],
  }));
}
function declarations(body) {
  return new Map([...body.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)].map((match) => [match[1], match[2].trim()]));
}
function scopedTokens(selector) {
  const blocks = [...tokensCSS.matchAll(new RegExp(`(?:^|\\n)\\s*${escape(selector)}\\s*\\{([^{}]*)\\}`, "g"))];
  assert.ok(blocks.length, `${selector} must have a real token rule`);
  return new Map(blocks.flatMap((match) => [...declarations(match[1])]));
}
const light = scopedTokens(":root");
const dark = new Map([...light, ...scopedTokens(".dark")]);
const theme = declarations(tokensCSS.match(/@theme\s*\{([^{}]*)\}/)?.[1] || "");
const THEMES = { light, dark };
const SURFACES = ["bg", "surface", "surface-raised", "surface-sunken"];
const LEVELS = ["ok", "warning", "critical", "exceeded"];

function triplet(tokens, name) {
  const value = tokens.get(`--${name}`);
  assert.match(value || "", /^\d+(?:\.\d+)?\s+\d+(?:\.\d+)?\s+\d+(?:\.\d+)?$/, `--${name} must be an R G B triplet`);
  const color = parseColor(`rgb(${value})`);
  assert.ok(color, `--${name} must parse`);
  return color;
}
const over = (fg, bg, a) => ({ r: fg.r * a + bg.r * (1 - a), g: fg.g * a + bg.g * (1 - a), b: fg.b * a + bg.b * (1 - a), a: 1 });
function checkContrast(fg, bg, minimum, label) {
  assert.ok(fg && bg, `${label} needs valid colors`);
  const ratio = contrastRatio(fg, bg);
  assert.ok(ratio >= minimum, `${label}: ${ratio.toFixed(2)}:1, need ${minimum}:1`);
}
function allCssFiles(dir) {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    return statSync(path).isDirectory() ? allCssFiles(path) : path.endsWith(".css") ? [path] : [];
  });
}
const styles = allCssFiles(srcDir).filter((file) => !file.endsWith("/tokens.css")).map((file) => ({
  path: relative(srcDir, file), code: stripComments(readFileSync(file, "utf8")),
}));

test("semantic color tokens resolve as triplets in both themes", () => {
  for (const tokens of Object.values(THEMES)) {
    for (const name of [
      ...SURFACES, "surface-overlay", "muted-surface", "border", "border-strong", "text", "muted", "muted-subtle",
      "text-inverse", "link", "ring", "shadow-color", "accent", "status-neutral-fg",
      ...LEVELS.flatMap((level) => [`status-${level}`, `status-${level}-fg`, `status-${level}-fg-soft`]),
    ]) triplet(tokens, name);
    assert.equal(tokens.get("--muted-surface"), tokens.get("--surface-sunken"), "legacy sunken alias must remain equal");
  }
});

test("alpha triplets and complete status colors stay synchronized", () => {
  for (const name of [...LEVELS, "accent"]) {
    const raw = theme.get(`--color-cp-${name}`);
    assert.ok(parseColor(raw), `${name} needs a complete @theme color`);
    assert.deepEqual(triplet(light, name === "accent" ? name : `status-${name}`), parseColor(raw));
  }
});

test("text and links clear AA on all named surfaces, focus rings clear 3:1", () => {
  for (const [mode, tokens] of Object.entries(THEMES)) {
    for (const surface of SURFACES) {
      for (const name of ["text", "muted", "muted-subtle", "link"]) {
        checkContrast(triplet(tokens, name), triplet(tokens, surface), 4.5, `${mode} ${name}/${surface}`);
      }
      checkContrast(triplet(tokens, "ring"), triplet(tokens, surface), 3, `${mode} focus/${surface}`);
    }
  }
});

test("status foregrounds clear AA on bare surfaces and supported tints", () => {
  for (const [mode, tokens] of Object.entries(THEMES)) {
    for (const level of LEVELS) {
      const tint = triplet(tokens, `status-${level}`);
      for (const surface of SURFACES) {
        const bg = triplet(tokens, surface);
        const fg = triplet(tokens, `status-${level}-fg`);
        checkContrast(fg, bg, 4.5, `${mode} ${level}/${surface}`);
        for (const alpha of [0.1, 0.15, 0.2]) {
          checkContrast(fg, over(tint, bg, alpha), 4.5, `${mode} ${level}/${surface}, ${alpha} tint`);
        }
        checkContrast(triplet(tokens, `status-${level}-fg-soft`), over(tint, bg, 0.1), 4.5, `${mode} ${level}-soft/${surface}`);
      }
    }
  }
});

test("opaque badge and accent fills clear AA for inverse text", () => {
  for (const [name, value] of theme) {
    if (!/^--color-cp-(?:badge-|accent)/.test(name)) continue;
    assert.match(value, /^#[\da-f]{6}$/i, `${name} must be an opaque hex color`);
    checkContrast(triplet(light, "text-inverse"), parseColor(value), 4.5, name);
  }
});

test("type, radius, elevation, motion and size tokens exist", () => {
  for (const name of [
    ...["micro", "chip", "xs", "sm", "md", "lg", "xl", "2xl", "3xl"].flatMap((step) => [`text-cp-${step}`, `leading-cp-${step}`]),
    "leading-cp-tight", "leading-cp-snug", "leading-cp-normal",
    ...["regular", "medium", "semibold"].map((step) => `weight-cp-${step}`),
    ...["tight", "normal", "wide"].map((step) => `tracking-cp-${step}`),
    ...["xs", "sm", "md", "lg", "full"].map((step) => `radius-cp-${step}`),
    ...[0, 1, 2, 3].map((step) => `elevation-cp-${step}`),
    ...["fast", "base", "pulse", "spin"].map((step) => `motion-duration-cp-${step}`),
    "motion-ease", "motion-ease-out", "focus-ring-cp-width", "focus-ring-cp-offset",
    "size-cp-dot", "size-cp-dot-lg", "size-cp-track", "size-cp-track-lg",
  ]) assert.ok(light.has(`--${name}`), `--${name} must exist`);
});

test("type sizes preserve the existing Tailwind scale and built CSS matches source", () => {
  const built = stripComments(readFileSync(join(here, "..", "assets", "app.css"), "utf8"));
  const builtTokens = declarations(built);
  const number = (raw) => {
    const calc = raw?.match(/^calc\(\s*([\d.]+)\s*\/\s*([\d.]+)\s*\)$/);
    return calc ? +calc[1] / +calc[2] : Number.parseFloat(raw);
  };
  // Canonical v4 defaults: no dependence on unused Tailwind vars being emitted.
  const baseline = [["micro", 0.625, "normal"], ["chip", 0.6875, "normal"], ["xs", 0.75, 1 / 0.75],
    ["sm", 0.875, 1.25 / 0.875], ["md", 1, 1.5], ["lg", 1.125, 1.75 / 1.125],
    ["xl", 1.25, 1.75 / 1.25], ["2xl", 1.5, 2 / 1.5], ["3xl", 1.875, 2.25 / 1.875]];
  let previous = 0;
  for (const [step, size, leading] of baseline) {
    const name = `--text-cp-${step}`;
    const line = `--leading-cp-${step}`;
    assert.match(light.get(name), /^[\d.]+rem$/, `${name} must use rem`);
    assert.equal(number(light.get(name)), size);
    assert.ok(size > previous);
    previous = size;
    assert.equal(number(builtTokens.get(name)), size, `run make css: ${name} is stale`);
    for (const values of [light, builtTokens]) {
      if (leading === "normal") assert.equal(values.get(line), leading);
      else assert.ok(Math.abs(number(values.get(line)) - leading) < 0.00001, `${line} needs its historical leading`);
    }
  }
});

test("each type-scale rule pairs its size with explicit leading", () => {
  for (const { path, code } of styles) {
    for (const { selectors, body } of rules(code)) {
      if (!/(?:font-size:\s*var\(--text-cp-|text-\[length:var\(--text-cp-)/.test(body)) continue;
      assert.match(body, /leading-\[length:var\(--leading-cp-|line-height:\s*(?:var\(--leading-cp-[\w-]+\)|inherit|normal)|leading-none/,
        `${path}: ${selectors.join(", ")} needs explicit leading`);
    }
  }
});

test("chart series are distinct complete hex colors with 3:1 surface contrast in each theme", () => {
  for (const [mode, tokens] of Object.entries(THEMES)) {
    const seen = new Set();
    for (const name of ["cpu", "mem", "disk", "net-rx", "net-tx", "disk-read", "disk-write", "load1", "sparkline"]) {
      const color = tokens.get(`--chart-cp-${name}`);
      assert.match(color || "", /^#[\da-f]{6}$/i, `${mode} ${name} must support hex alpha suffixes`);
      assert.equal(seen.has(color.toLowerCase()), false, `${mode} ${name} duplicates another series`);
      seen.add(color.toLowerCase());
      for (const surface of SURFACES) checkContrast(parseColor(color), triplet(tokens, surface), 3, `${mode} chart ${name}/${surface}`);
    }
    for (const name of ["axis", "grid", "cat-1", "cat-2", "cat-3", "cat-4", "cat-5"]) {
      assert.ok(parseColor(tokens.get(`--chart-cp-${name}`)), `${mode} ${name} must be a complete color`);
    }
  }
});

test("styles only compose rgb with declared triplets, never complete hex tokens", () => {
  for (const { path, code } of styles) {
    assert.doesNotMatch(code, /rgb\(var\(--color-/, `${path}: rgb(hex) is invalid CSS`);
    assert.doesNotMatch(code, /color-mix\(/, `${path}: use measurable RGB composition`);
    for (const match of code.matchAll(/rgb\(var\(--([\w-]+)\)/g)) {
      for (const tokens of Object.values(THEMES)) triplet(tokens, match[1]);
    }
    for (const match of code.matchAll(/(?:background-color|border-color|color):\s*var\((--color-[\w-]+)\)/g)) {
      assert.ok(parseColor(theme.get(match[1])), `${path}: ${match[1]} must resolve to a complete color`);
    }
  }
});

test("styles do not hardcode colors or redeclare visual tokens", () => {
  for (const { path, code } of styles) {
    assert.doesNotMatch(code, /#[0-9a-f]{3,8}\b/i, `${path}: raw hex`);
    assert.doesNotMatch(code, /\b(?:rgba?|hsla?|oklch|oklab)\(\s*[\d.]/i, `${path}: raw color channels`);
    assert.doesNotMatch(code, /\b(?:text|bg|border|ring|fill|stroke|from|to|via)-(?:red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose|slate|gray|zinc|neutral|stone)-\d{2,3}\b/, `${path}: palette keyword`);
    assert.doesNotMatch(code, /\b(?:text-white|bg-black|border-white)\b/, `${path}: use inverse/overlay tokens`);
    assert.doesNotMatch(code, /(?:^|[\s:])(?:text-(?:xs|sm|base|lg|xl|2xl|3xl)|text-\[\d+(?:px|rem)\]|rounded-(?:sm|md|lg|xl|full)|shadow-(?:sm|lg|2xl)|font-(?:normal|medium|semibold)|tracking-(?:tight|normal|wide))(?=[\s;]|$)/,
      `${path}: shared type/radius/elevation must consume design tokens`);
    assert.doesNotMatch(code, /^\s*--(?:color-|status-|surface-|chart-|text-cp-|leading-cp-|radius-cp-|elevation-cp-|motion-)[\w-]+\s*:/gm, `${path}: duplicate token declaration`);
    for (const name of declarations(code).keys()) {
      assert.equal(light.has(name) || theme.has(name), false, `${path}: ${name} is owned by tokens.css`);
    }
  }
});

test("component base selectors have one owning stylesheet", () => {
  const owners = new Map();
  for (const { path, code } of styles) {
    for (const { selectors } of rules(code)) {
      for (const selector of selectors) {
        if (!/^\.cp-[\w-]+$/.test(selector)) continue;
        if (!owners.has(selector)) owners.set(selector, new Set());
        owners.get(selector).add(path);
      }
    }
  }
  for (const [selector, files] of owners) assert.equal(files.size, 1, `${selector} has competing owners: ${[...files].join(", ")}`);
});

test("legacy dot aliases and large-size contracts remain correct", () => {
  const status = styles.find(({ path }) => path === "components/status.css").code;
  const dotRules = rules(status);
  for (const [alias, canonical, token] of [["up", "ok", "ok"], ["down", "exceeded", "exceeded"], ["warning", "warning", "warning"]]) {
    const rule = dotRules.find(({ selectors }) => selectors.includes(`.cp-dot-${alias}`));
    assert.ok(rule?.selectors.includes(`.cp-status-dot-${canonical}`), `${alias} must share its canonical status rule`);
    assert.match(rule.body, new RegExp(`background-color:\\s*var\\(--color-cp-${token}\\)`));
  }
  const neutral = dotRules.find(({ selectors }) => selectors.includes(".cp-chip-other"));
  assert.ok(neutral?.selectors.includes(".cp-chip-neutral"), "legacy other chips must share the neutral style");
  assert.match(neutral.body, /color:\s*rgb\(var\(--status-neutral-fg\)\)/);
  for (const family of ["dot", "track"]) {
    assert.ok(parseFloat(light.get(`--size-cp-${family}-lg`)) > parseFloat(light.get(`--size-cp-${family}`)), `${family}-lg must be larger`);
  }
});

test("card title size and compact density preserve their explicit contracts", () => {
  const cardRules = rules(styles.find(({ path }) => path === "components/card.css").code);
  const title = cardRules.find(({ selectors }) => selectors.includes(".cp-card-title"));
  assert.match(title.body, /text-\[length:var\(--text-cp-md\)\]/, "shared card titles stay at the existing 16px");
  const compact = cardRules.find(({ selectors }) => selectors.includes(".cp-card-compact"));
  assert.match(compact.body, /\bp-3\.5\b/);
  assert.match(compact.body, /\bsm:p-3\.5\b/, "compact density must override the base card at sm too");
  const surface = cardRules.find(({ selectors }) => selectors.includes(".cp-card") && selectors.includes(".cp-stat-card"));
  assert.ok(surface, "base/stat card surfaces must share one rule");
  assert.match(surface.body, /bg-\[rgb\(var\(--surface\)\)\]/);
  assert.match(surface.body, /box-shadow:\s*var\(--elevation-cp-0\)/);
});

test("motion is tokenized and reduced motion stops repeat animations", () => {
  for (const { path, code } of styles) {
    assert.doesNotMatch(code, /(?:transition-duration|animation-duration):\s*[\d.]+(?:ms|s)\b/, `${path}: raw duration`);
    assert.doesNotMatch(code, /animation:\s*[^;]*\s[\d.]+(?:ms|s)\b/, `${path}: raw animation duration`);
  }
  assert.match(tokensCSS, /@media\s*\(prefers-reduced-motion:\s*reduce\)/);
  assert.match(tokensCSS, /animation-iteration-count:\s*1\s*!important/);
  assert.match(tokensCSS, /transition-duration:\s*0\.001ms\s*!important/);
});

test("input imports tokens and all shared families before shell styles", () => {
  const input = stripComments(readFileSync(join(srcDir, "input.css"), "utf8"));
  const files = ["tokens.css", "components/typography.css", "components/card.css", "components/status.css", "components/meter.css", "base.css"];
  let previous = -1;
  for (const file of files) {
    const index = input.indexOf(`@import "./${file}";`);
    assert.ok(index > previous, `${file} must be imported in shared-family order`);
    previous = index;
  }
});

test("selected Settings navigation retains its identity on hover", () => {
  const settings = styles.find(({ path }) => path === "pages/settings.css").code;
  const hover = rules(settings).find(({ selectors }) => selectors.includes(".cp-settings-nav-link-active:hover"));
  assert.ok(hover, "selected state needs an explicit hover override");
  assert.match(hover.body, /color:\s*rgb\(var\(--link\)\)/);
  assert.match(hover.body, /background-color:\s*rgb\(var\(--accent\)\s*\/\s*0\.1\)/);
  assert.match(settings, /@media\s*\(hover:\s*hover\)\s*\{\s*\.cp-settings-nav-link-active:hover/);
});

test("shared keyboard controls never animate the focus outline from inverse text", () => {
  const baseRules = rules(styles.find(({ path }) => path === "base.css").code);
  for (const name of ["btn", "icon-btn", "input", "select", "settings-nav-link"]) {
    const focus = baseRules.find(({ selectors }) => selectors.includes(`.cp-${name}:focus-visible`));
    assert.ok(focus, `${name} needs immediate focus`);
    assert.match(focus.body, /outline:\s*var\(--focus-ring-cp-width\) solid rgb\(var\(--ring\)\)/);
    const transition = focus.body.match(/transition-property:\s*([^;]+);/)?.[1];
    assert.ok(transition);
    assert.doesNotMatch(transition, /outline|\ball\b/);
  }
});
