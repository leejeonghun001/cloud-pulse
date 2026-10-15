// charts.js — uPlot wrappers with a theme-aware palette (re-reads CSS
// variables on "cp-theme-change" via re-creation by callers), resize
// -aware sizing, a local-timezone time axis, and lifecycle management
// (destroy on navigation). Depends only on the vendored uPlot ESM
// build.
import uPlot from "../../vendor/uplot/uPlot.esm.js";
import { t } from "../core/i18n.js";

/**
 * Series colors, reused across charts for visual consistency. Declared
 * as CSS custom properties in web/src/tokens.css (--chart-cp-*) and read
 * back here, so a palette change is one edit in the design system rather
 * than a JS edit that would silently drift from the CSS.
 *
 * The FALLBACK map below is a defensive light-theme palette: if
 * app.css ever fails to load (a stale cached build, a stripped asset
 * bundle) a chart should still draw in roughly the right colors rather
 * than render every series as transparent black.
 */
const SERIES_COLOR_FALLBACK = {
  cpu: "#0e7490",
  mem: "#7c3aed",
  disk: "#c2410c",
  netRx: "#047857",
  netTx: "#be185d",
  diskRead: "#2563eb",
  diskWrite: "#a16207",
  load1: "#dc2626",
  sparkline: "#0369a1",
};

/** Maps each series key to its --chart-cp-* custom property name. */
const SERIES_COLOR_TOKEN = {
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

/**
 * readToken resolves a CSS custom property to a usable color string,
 * falling back to `fallback` when the property is unset or empty (which
 * is what a browser reports for an undefined custom property).
 * @param {string} name
 * @param {string} fallback
 * @returns {string}
 */
function readToken(name, fallback) {
  if (typeof document === "undefined" || typeof getComputedStyle !== "function") return fallback;
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

/** Keep the enumerable property API, but resolve colors at read time so
 *  callers recreating charts after cp-theme-change see the active palette. */
export const SERIES_COLORS = Object.freeze(
  Object.defineProperties({}, Object.fromEntries(
    Object.entries(SERIES_COLOR_TOKEN).map(([key, name]) => [key, {
      enumerable: true,
      get: () => readToken(name, SERIES_COLOR_FALLBACK[key]),
    }]),
  )),
);

/**
 * readThemeColors reads the current axis/grid colors from CSS
 * variables so charts match the active light/dark theme. Resolved on
 * every call (not cached at module load) so a chart created after a
 * theme toggle gets the new theme's axis without a reload.
 * @returns {{axis: string, grid: string}}
 */
function readThemeColors() {
  return {
    axis: readToken("--chart-cp-axis", "#64646c"),
    grid: readToken("--chart-cp-grid", "#e4e4e7"),
  };
}

/**
 * timeAxisValues formats x-axis tick labels in the browser's local
 * timezone (uPlot's default formatter otherwise assumes UTC-agnostic
 * seconds without a locale-aware time-of-day label).
 * @param {number} rawValue unix seconds
 * @returns {string}
 */
function formatTickTime(rawValue) {
  const d = new Date(rawValue * 1000);
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

/**
 * baseOpts returns uPlot options shared by every chart: a themed
 * background (styling lives in app.css), a local-time x axis, and no
 * built-in legend (we render our own accessible legend/tooltip text).
 * @param {number} width
 * @param {number} height
 * @param {string} title
 * @param {(rawValue: number) => string} [yAxisFormatter] optional tick
 *   formatter for the y-axis (defaults to uPlot's locale number format,
 *   which renders large byte/sec magnitudes as bare grouped integers —
 *   pass a formatter for byte-rate charts so ticks read e.g. "1.0 MiB/s").
 * @returns {Object}
 */
function baseOpts(width, height, title, yAxisFormatter, syncKey) {
  const colors = readThemeColors();
  return {
    width,
    height,
    title: "",
    class: "cp-uplot",
    cursor: {
      points: { size: 6 },
      // Sharing a sync key across a set of charts (host detail's
      // CPU/Memory/Disk/Bandwidth/Disk IO/Load charts) makes uPlot
      // broadcast cursor position/focus between them, so hovering one
      // chart highlights the same timestamp on every other chart in
      // the group ("synced cursor across charts" per SPEC-v0.4 §4).
      sync: syncKey ? { key: syncKey } : undefined,
    },
    legend: {
      show: true,
    },
    axes: [
      {
        stroke: colors.axis,
        grid: { stroke: colors.grid, width: 1 },
        ticks: { stroke: colors.grid },
        values: (_u, vals) => vals.map(formatTickTime),
      },
      {
        stroke: colors.axis,
        grid: { stroke: colors.grid, width: 1 },
        ticks: { stroke: colors.grid },
        values: yAxisFormatter
          ? (_u, vals) => vals.map((v) => (v == null ? "" : yAxisFormatter(v)))
          : undefined,
        // A fixed axis width avoids uPlot's label-width-driven layout
        // convergence loop recomputing splits across multiple cycles
        // when a custom (wider, unit-suffixed) values() formatter is
        // used — on this build or Firefox that feedback loop was
        // observed to occasionally paint an intermediate cycle's stale
        // split values instead of the converged final ones. 84px fits
        // the widest label this dashboard renders ("999.9 KiB/s").
        size: yAxisFormatter ? 84 : undefined,
      },
    ],
    series: [{ label: t("host.chart_series_time") }],
    _title: title,
  };
}

/**
 * ChartHandle wraps a uPlot instance with a ResizeObserver so the chart
 * stays sized to its container, and a destroy() that disconnects both.
 */
export class ChartHandle {
  /**
   * @param {HTMLElement} container
   * @param {Object} uplotOpts
   * @param {Array} data uPlot-format [xs, ...series]
   */
  constructor(container, uplotOpts, data) {
    this.container = container;
    const rect = container.getBoundingClientRect();
    const width = Math.max(Math.floor(rect.width) || 320, 240);
    const height = uplotOpts.height || 220;
    this.lastWidth = width;

    this.plot = new uPlot({ ...uplotOpts, width, height }, data, container);

    // ResizeObserver's callback fires once immediately upon observe() in
    // most browsers, reporting the container's current size. Skip
    // calling setSize() when the width hasn't actually changed so the
    // chart only repaints on a real layout change.
    this.resizeObserver = new ResizeObserver((entries) => {
      for (const entry of entries) {
        const w = Math.max(Math.floor(entry.contentRect.width), 240);
        if (w === this.lastWidth) continue;
        this.lastWidth = w;
        this.plot.setSize({ width: w, height });
      }
    });
    this.resizeObserver.observe(container);
  }

  /** setData replaces the chart's data without recreating the instance. */
  setData(data) {
    this.plot.setData(data);
  }

  /** destroy tears down the ResizeObserver and the uPlot instance. */
  destroy() {
    this.resizeObserver.disconnect();
    this.plot.destroy();
    this.plot = null;
  }
}

/**
 * createTimeSeriesChart builds a multi-series time chart.
 * @param {HTMLElement} container
 * @param {Object} opts
 * @param {string} opts.title
 * @param {{label: string, color: string, unit?: string}[]} opts.series series metadata (excluding the time series)
 * @param {number[]} opts.timestamps unix seconds
 * @param {number[][]} opts.values one array per series, same length as timestamps
 * @param {(rawValue: number, seriesIdx: number) => string} [opts.valueFormatter] legend/cursor value formatter
 * @param {(rawValue: number) => string} [opts.yAxisFormatter] y-axis tick formatter
 * @param {boolean} [opts.lineOnly] renders comparison-style series without area fills
 * @returns {ChartHandle}
 */
export function createTimeSeriesChart(container, opts) {
  const { title, series, timestamps, values, valueFormatter, yAxisFormatter, syncKey, lineOnly = false } = opts;

  const uplotSeries = [
    { label: t("host.chart_series_time") },
    ...series.map((s) => ({
      label: s.label,
      stroke: s.color,
      width: lineOnly ? 1.5 : 2,
      // A translucent fill under the line ("area chart" per
      // SPEC-v0.4 §4) using the series' own color at low alpha so
      // multiple overlapping series (e.g. Rx/Tx) stay readable.
      ...(lineOnly ? {} : { fill: `${s.color}26` }),
      points: { show: false },
      value: valueFormatter
        ? (_u, v, seriesIdx) => (v == null ? "–" : valueFormatter(v, seriesIdx))
        : undefined,
    })),
  ];

  const options = {
    ...baseOpts(container.clientWidth || 320, 220, title, yAxisFormatter, syncKey),
    series: uplotSeries,
  };

  const data = [timestamps, ...values];
  return new ChartHandle(container, options, data);
}

/**
 * createSparkline builds a minimal, axis-less single-series chart for
 * compact inline use (bucket request-rate history).
 * @param {HTMLElement} container
 * @param {number[]} timestamps unix seconds
 * @param {number[]} values
 * @param {string} [color]
 * @returns {ChartHandle}
 */
export function createSparkline(container, timestamps, values, color = SERIES_COLORS.sparkline) {
  const options = {
    width: container.clientWidth || 160,
    height: 40,
    class: "cp-sparkline",
    cursor: { show: false },
    legend: { show: false },
    axes: [
      { show: false },
      { show: false },
    ],
    series: [
      { label: t("host.chart_series_time") },
      { label: t("host.chart_series_requests"), stroke: color, width: 1.5, points: { show: false }, fill: `${color}22` },
    ],
  };
  return new ChartHandle(container, options, [timestamps, values]);
}
