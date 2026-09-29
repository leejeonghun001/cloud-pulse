// buckets.js — builds the overview page's object storage section: a
// card per bucket (S3/R2) with usage stats, a request-rate sparkline,
// R2 free-tier progress bars, and a collector status list.
import { el, progressBar, storageBadge, emptyState, errorBanner } from "../ui/components.js";
import { formatBytes, formatNumber, formatRelativeTimeFromUnixSeconds } from "../core/format.js";
import { createSparkline, SERIES_COLORS } from "./charts.js";

/**
 * buildBucketsSection renders the full object-storage section.
 * @param {Object} opts
 * @param {Array} opts.buckets models.BucketView[] JSON
 * @param {Array} opts.collectors models.CollectorStatus[] JSON
 * @param {number} opts.nowMs
 * @returns {{node: HTMLElement, charts: import('./charts.js').ChartHandle[]}}
 */
export function buildBucketsSection({ buckets, collectors, nowMs }) {
  const section = el("section", { class: "cp-section", attrs: { "aria-labelledby": "cp-buckets-heading" } });
  section.append(el("h2", { id: "cp-buckets-heading", text: "Object storage" }));

  const charts = [];

  if (buckets.length === 0) {
    section.append(
      emptyState({
        title: "No buckets tracked",
        message:
          "Set CP_S3_BUCKETS (e.g. \"my-bucket:us-east-1\") and/or CP_R2_ACCOUNT_ID + CP_R2_API_TOKEN on the hub to start collecting object storage stats.",
      }),
    );
  } else {
    const grid = el("div", { class: "cp-bucket-grid" });
    for (const view of buckets) {
      const { node, chart } = bucketCard(view, nowMs);
      grid.append(node);
      if (chart) charts.push(chart);
    }
    section.append(grid);
  }

  section.append(collectorStatusList(collectors, nowMs));

  return { node: section, charts };
}

/**
 * bucketCard builds a single bucket's card.
 * @param {Object} view models.BucketView JSON
 * @param {number} nowMs
 * @returns {{node: HTMLElement, chart: import('./charts.js').ChartHandle|null}}
 */
function bucketCard(view, nowMs) {
  const stats = view.latest;
  const card = el("div", { class: "cp-card cp-bucket-card" });

  const header = el("div", { class: "cp-bucket-card-header" });
  header.append(
    storageBadge(stats.provider),
    el("h3", { class: "cp-bucket-card-name", text: stats.bucket }),
  );
  card.append(header);
  if (stats.region) {
    card.append(el("p", { class: "cp-muted-small", text: stats.region }));
  }

  if (stats.error) {
    card.append(errorBanner(stats.error));
  }

  const grid = el("div", { class: "cp-bucket-stats-grid" });
  grid.append(
    statPair("Stored", formatBytes(stats.size_bytes)),
    statPair("Objects", formatNumber(stats.object_count)),
    statPair("Class A ops (MTD)", formatNumber(stats.class_a_ops_mtd)),
    statPair("Class B ops (MTD)", formatNumber(stats.class_b_ops_mtd)),
  );
  card.append(grid);

  if (stats.provider === "s3") {
    card.append(
      el("p", { class: "cp-muted-small", text: `Egress (MTD): ${formatBytes(stats.egress_bytes_mtd)}` }),
    );
  }

  let chart = null;
  if (!stats.request_metrics_available) {
    card.append(el("p", { class: "cp-hint", text: "Request metrics not enabled for this bucket." }));
  } else {
    const windowMinutes = Math.max(stats.window_seconds / 60, 1);
    const reqPerMin = stats.requests_window / windowMinutes;
    card.append(
      el("p", { class: "cp-muted-small", text: `Requests (last window): ${formatNumber(stats.requests_window)} (~${reqPerMin.toFixed(1)}/min)` }),
    );

    if (view.history && view.history.length > 1) {
      const sparkWrap = el("div", {
        class: "cp-sparkline-wrap",
        attrs: {
          "aria-label": `Requests per collection window over the last 24 hours for ${stats.bucket}`,
          role: "img",
        },
      });
      card.append(sparkWrap);
      const timestamps = view.history.map((p) => p.ts);
      const values = view.history.map((p) => p.requests_window);
      // Deferred: appended to DOM first so clientWidth is measurable.
      queueMicrotask(() => {
        if (sparkWrap.isConnected) {
          chart = createSparkline(sparkWrap, timestamps, values, SERIES_COLORS.sparkline);
        }
      });
    }
  }

  if (view.free_tier) {
    card.append(freeTierBars(stats, view.free_tier));
  }

  card.append(
    el("p", {
      class: "cp-muted-small",
      text: `Collected ${formatRelativeTimeFromUnixSeconds(stats.collected_at, nowMs)}`,
    }),
  );

  return { node: card, chart };
}

/** statPair builds a small label/value pair used in the bucket stats grid. */
function statPair(label, value) {
  const wrap = el("div", { class: "cp-stat-pair" });
  wrap.append(el("span", { class: "cp-stat-value", text: value }));
  wrap.append(el("span", { class: "cp-stat-label", text: label }));
  return wrap;
}

/**
 * freeTierBars builds the R2 free-tier progress bars (storage, class A,
 * class B ops).
 * @param {Object} stats models.BucketStats JSON
 * @param {Object} freeTier models.FreeTier JSON
 * @returns {HTMLElement}
 */
function freeTierBars(stats, freeTier) {
  const wrap = el("div", { class: "cp-free-tier" });
  wrap.append(el("h4", { text: "R2 free tier" }));
  wrap.append(
    progressBar({
      value: stats.size_bytes,
      max: freeTier.storage_bytes,
      label: "Free tier storage usage",
      levelClass: barLevel(stats.size_bytes, freeTier.storage_bytes),
      valueText: `${formatBytes(stats.size_bytes)} / ${formatBytes(freeTier.storage_bytes)}`,
    }),
  );
  wrap.append(
    progressBar({
      value: stats.class_a_ops_mtd,
      max: freeTier.class_a_ops,
      label: "Free tier Class A operations usage",
      levelClass: barLevel(stats.class_a_ops_mtd, freeTier.class_a_ops),
      valueText: `${formatNumber(stats.class_a_ops_mtd)} / ${formatNumber(freeTier.class_a_ops)} Class A`,
    }),
  );
  wrap.append(
    progressBar({
      value: stats.class_b_ops_mtd,
      max: freeTier.class_b_ops,
      label: "Free tier Class B operations usage",
      levelClass: barLevel(stats.class_b_ops_mtd, freeTier.class_b_ops),
      valueText: `${formatNumber(stats.class_b_ops_mtd)} / ${formatNumber(freeTier.class_b_ops)} Class B`,
    }),
  );
  return wrap;
}

/** barLevel picks a bar color class from usage vs. a free-tier cap. */
function barLevel(value, max) {
  if (max <= 0) return "cp-bar-ok";
  const pct = (value / max) * 100;
  if (pct >= 100) return "cp-bar-exceeded";
  if (pct >= 95) return "cp-bar-critical";
  if (pct >= 80) return "cp-bar-warning";
  return "cp-bar-ok";
}

/**
 * collectorStatusList builds the collector health list.
 * @param {Array} collectors models.CollectorStatus[] JSON
 * @param {number} nowMs
 * @returns {HTMLElement}
 */
function collectorStatusList(collectors, nowMs) {
  const wrap = el("div", { class: "cp-collector-status" });
  wrap.append(el("h3", { text: "Collectors" }));

  if (collectors.length === 0) {
    wrap.append(el("p", { class: "cp-muted-small", text: "No cloud collectors configured." }));
    return wrap;
  }

  const list = el("ul", { class: "cp-collector-list" });
  for (const c of collectors) {
    const item = el("li", { class: "cp-collector-item" });
    item.append(el("span", { class: "cp-collector-name", text: c.name }));
    item.append(
      el("span", {
        class: "cp-muted-small",
        text: c.enabled ? `Last run ${formatRelativeTimeFromUnixSeconds(c.last_run, nowMs)}` : "Disabled",
      }),
    );
    if (c.last_error) {
      item.append(el("span", { class: "cp-collector-error", text: c.last_error }));
    }
    list.append(item);
  }
  wrap.append(list);
  return wrap;
}
