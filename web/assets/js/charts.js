// charts.js — uPlot wrappers with a dark theme, resize-aware sizing, a
// local-timezone time axis, and lifecycle management (destroy on
// navigation). Depends only on the vendored uPlot ESM build.
import uPlot from "../vendor/uplot/uPlot.esm.js";

/** Dark-theme series colors, reused across charts for visual consistency. */
export const SERIES_COLORS = {
  cpu: "#22d3ee",
  mem: "#a78bfa",
  disk: "#fb923c",
  netRx: "#34d399",
  netTx: "#f472b6",
  diskRead: "#60a5fa",
  diskWrite: "#fbbf24",
  load1: "#f87171",
  sparkline: "#38bdf8",
};

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
 * baseOpts returns uPlot options shared by every chart: dark background
 * via CSS (styling lives in app.css), a local-time x axis, and no
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
function baseOpts(width, height, title, yAxisFormatter) {
  return {
    width,
    height,
    title: "",
    class: "cp-uplot",
    cursor: {
      points: { size: 6 },
    },
    legend: {
      show: true,
    },
    axes: [
      {
        stroke: "#8b98a5",
        grid: { stroke: "#1f2a37", width: 1 },
        ticks: { stroke: "#1f2a37" },
        values: (_u, vals) => vals.map(formatTickTime),
      },
      {
        stroke: "#8b98a5",
        grid: { stroke: "#1f2a37", width: 1 },
        ticks: { stroke: "#1f2a37" },
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
    series: [{ label: "Time" }],
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
 * @returns {ChartHandle}
 */
export function createTimeSeriesChart(container, opts) {
  const { title, series, timestamps, values, valueFormatter, yAxisFormatter } = opts;

  const uplotSeries = [
    { label: "Time" },
    ...series.map((s) => ({
      label: s.label,
      stroke: s.color,
      width: 2,
      points: { show: false },
      value: valueFormatter
        ? (_u, v, seriesIdx) => (v == null ? "–" : valueFormatter(v, seriesIdx))
        : undefined,
    })),
  ];

  const options = {
    ...baseOpts(container.clientWidth || 320, 220, title, yAxisFormatter),
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
      { label: "Time" },
      { label: "Requests", stroke: color, width: 1.5, points: { show: false }, fill: `${color}22` },
    ],
  };
  return new ChartHandle(container, options, [timestamps, values]);
}
