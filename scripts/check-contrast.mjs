#!/usr/bin/env node
// Checks the token pairs the dashboard actually uses for readable text. This
// intentionally has no package dependency so it can run in the release build.
import { readFileSync } from "node:fs";

const TOKEN_PATH = new URL("../web/src/tokens.css", import.meta.url);

function blockFor(css, selector) {
  const start = css.indexOf(selector);
  if (start < 0) throw new Error(`Missing ${selector} token block.`);
  const open = css.indexOf("{", start);
  let depth = 0;
  for (let i = open; i < css.length; i++) {
    if (css[i] === "{") depth++;
    if (css[i] === "}" && --depth === 0) return css.slice(open + 1, i);
  }
  throw new Error(`Unclosed ${selector} token block.`);
}

function tokensFrom(block) {
  return Object.fromEntries([...block.matchAll(/--([\w-]+)\s*:\s*([^;]+);/g)].map(([, key, value]) => [key, value.trim()]));
}

function color(value) {
  const hex = value.match(/^#([0-9a-f]{6})$/i);
  if (hex) return [0, 2, 4].map((offset) => Number.parseInt(hex[1].slice(offset, offset + 2), 16));
  const rgb = value.match(/^(\d+)\s+(\d+)\s+(\d+)$/);
  if (rgb) return rgb.slice(1).map(Number);
  throw new Error(`Expected a hex or RGB-triplet token, got “${value}”.`);
}

function blend(foreground, background, alpha) {
  return foreground.map((channel, index) => channel * alpha + background[index] * (1 - alpha));
}

function luminance(rgb) {
  const channels = rgb.map((channel) => {
    const value = channel / 255;
    return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
}

export function contrastRatio(foreground, background) {
  const [light, dark] = [luminance(foreground), luminance(background)].sort((a, b) => b - a);
  return (light + 0.05) / (dark + 0.05);
}

/** @returns {{theme: string, pair: string, ratio: number, minimum: number}[]} */
export function checkContrast(css = readFileSync(TOKEN_PATH, "utf8")) {
  const base = tokensFrom(blockFor(css, ":root"));
  const dark = { ...base, ...tokensFrom(blockFor(css, ".dark {")) };
  return [["light", base], ["dark", dark]].flatMap(([theme, tokens]) => {
    const token = (name) => color(tokens[name]);
    const surface = token("surface");
    const pairs = [
      ["body text", token("text"), token("bg"), 4.5],
      ["muted text", token("muted"), surface, 4.5],
      ["muted-small text", token("muted"), surface, 4.5],
      ["links", token("link"), surface, 4.5],
      ["badge ok", token("status-ok-fg"), blend(token("status-ok"), surface, 0.15), 4.5],
      ["badge warning", token("status-warning-fg"), blend(token("status-warning"), surface, 0.15), 4.5],
      ["badge critical", token("status-critical-fg"), blend(token("status-critical"), surface, 0.15), 4.5],
      ["nav active", token("text"), surface, 4.5],
      ["nav inactive", token("muted"), surface, 4.5],
      ["placeholder", token("muted"), token("surface-raised"), 4.5],
    ];
    return pairs.map(([pair, foreground, background, minimum]) => ({
      theme,
      pair,
      ratio: contrastRatio(foreground, background),
      minimum,
    }));
  });
}

export function assertContrast(css) {
  const failures = checkContrast(css).filter(({ ratio, minimum }) => ratio < minimum);
  if (failures.length) {
    throw new Error(failures.map(({ theme, pair, ratio, minimum }) => (
      `${theme} ${pair}: ${ratio.toFixed(2)}:1 (needs ${minimum}:1)`
    )).join("\n"));
  }
}

if (process.argv[1] && new URL(`file://${process.argv[1]}`).href === import.meta.url) {
  try {
    const results = checkContrast();
    assertContrast();
    for (const result of results) console.log(`${result.theme} ${result.pair}: ${result.ratio.toFixed(2)}:1`);
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
