// components.js — DOM builder helpers for the cloud-pulse dashboard.
// Every function here builds elements with document.createElement and
// assigns text via .textContent, never .innerHTML with API-derived
// data, so nothing an agent reports (hostname, bucket name, error
// strings, ...) can execute as markup.

import {
  formatBytes,
  formatBitrate,
  formatPercent,
  formatDuration,
  formatRelativeTimeFromUnixSeconds,
  formatNumber,
  formatLoad,
  clamp,
} from "./format.js";

/**
 * el creates an element with optional class names, attributes, and
 * children (strings become text nodes; nodes are appended as-is).
 * @param {string} tag
 * @param {Object} [opts]
 * @param {string} [opts.class]
 * @param {Object<string,string>} [opts.attrs]
 * @param {(Node|string)[]} [opts.children]
 * @param {string} [opts.text]
 * @returns {HTMLElement}
 */
export function el(tag, opts = {}) {
  const node = document.createElement(tag);
  if (opts.class) node.className = opts.class;
  if (opts.attrs) {
    for (const [k, v] of Object.entries(opts.attrs)) {
      node.setAttribute(k, v);
    }
  }
  if (opts.text !== undefined) node.textContent = opts.text;
  if (opts.children) {
    for (const child of opts.children) {
      node.append(typeof child === "string" ? document.createTextNode(child) : child);
    }
  }
  return node;
}

/** clearChildren removes all children of node. */
export function clearChildren(node) {
  while (node.firstChild) node.removeChild(node.firstChild);
}

/**
 * progressBar builds an accessible progress bar element.
 * @param {Object} opts
 * @param {number} opts.value current value
 * @param {number} opts.max maximum value (0 renders an empty/inert bar)
 * @param {string} opts.label aria-label describing what the bar measures
 * @param {string} [opts.levelClass] extra class for color level (e.g. "cp-bar-warning")
 * @param {string} [opts.valueText] visible text override (defaults to a percent)
 * @returns {HTMLElement}
 */
export function progressBar({ value, max, label, levelClass, valueText }) {
  const pct = max > 0 ? clamp((value / max) * 100, 0, 100) : 0;
  const track = el("div", { class: "cp-bar-track" });
  const fill = el("div", {
    class: `cp-bar-fill ${levelClass || "cp-bar-ok"}`,
  });
  fill.style.width = `${pct}%`;
  track.append(fill);

  const wrap = el("div", {
    class: "cp-bar",
    attrs: {
      role: "progressbar",
      "aria-valuenow": String(Math.round(max > 0 ? value : 0)),
      "aria-valuemin": "0",
      "aria-valuemax": String(Math.round(max)),
      "aria-label": label,
    },
  });
  wrap.append(track);

  const text = el("span", {
    class: "cp-bar-text",
    text: valueText ?? formatPercent(pct),
  });
  wrap.append(text);
  return wrap;
}

/**
 * statusDot builds a small colored dot indicating host up/down status.
 * @param {"up"|"down"} status
 * @returns {HTMLElement}
 */
export function statusDot(status) {
  const cls = status === "up" ? "cp-dot-up" : "cp-dot-down";
  return el("span", {
    class: `cp-dot ${cls}`,
    attrs: { "aria-hidden": "true" },
  });
}

/**
 * providerBadge builds a small badge naming the cloud provider.
 * @param {"aws"|"oci"|"other"|string} provider
 * @returns {HTMLElement}
 */
export function providerBadge(provider) {
  const labels = { aws: "AWS", oci: "OCI" };
  const label = labels[provider] || "Other";
  const cls = provider === "aws" ? "cp-badge-aws" : provider === "oci" ? "cp-badge-oci" : "cp-badge-other";
  return el("span", { class: `cp-badge ${cls}`, text: label });
}

/**
 * storageBadge builds a small badge naming the object-storage provider.
 * @param {"s3"|"r2"|string} provider
 * @returns {HTMLElement}
 */
export function storageBadge(provider) {
  const label = provider === "s3" ? "S3" : provider === "r2" ? "R2" : provider.toUpperCase();
  const cls = provider === "s3" ? "cp-badge-aws" : "cp-badge-r2";
  return el("span", { class: `cp-badge ${cls}`, text: label });
}

/**
 * egressLevelChip builds a small colored chip naming an egress level.
 * @param {"ok"|"warning"|"critical"|"exceeded"} level
 * @returns {HTMLElement}
 */
export function egressLevelChip(level) {
  const labels = { ok: "OK", warning: "Warning", critical: "Critical", exceeded: "Exceeded" };
  return el("span", {
    class: `cp-chip cp-chip-${level}`,
    text: labels[level] || level,
  });
}

/** levelClassForBar maps an egress level to a progress-bar fill class. */
export function levelClassForBar(level) {
  switch (level) {
    case "warning":
      return "cp-bar-warning";
    case "critical":
      return "cp-bar-critical";
    case "exceeded":
      return "cp-bar-exceeded";
    default:
      return "cp-bar-ok";
  }
}

/**
 * metricRow builds a labeled mini progress bar row (used for CPU/Mem/Disk
 * in host cards).
 * @param {string} label
 * @param {number} percent
 * @param {string} [detail] extra text shown after the percent (e.g. "3.2/16 GiB")
 * @returns {HTMLElement}
 */
export function metricRow(label, percent, detail) {
  const row = el("div", { class: "cp-metric-row" });
  const head = el("div", { class: "cp-metric-head" }, );
  head.append(
    el("span", { class: "cp-metric-label", text: label }),
    el("span", { class: "cp-metric-value", text: detail ? `${formatPercent(percent)} · ${detail}` : formatPercent(percent) }),
  );
  row.append(head);
  row.append(
    progressBar({
      value: percent,
      max: 100,
      label: `${label} usage`,
      levelClass: percent >= 90 ? "cp-bar-critical" : percent >= 75 ? "cp-bar-warning" : "cp-bar-ok",
      valueText: "",
    }),
  );
  return row;
}

/**
 * hostCard builds a full host summary card, a keyboard-accessible link
 * to #/host/<id>.
 * @param {Object} summary a models.HostSummary JSON object
 * @param {number} nowMs
 * @returns {HTMLAnchorElement}
 */
export function hostCard(summary, nowMs) {
  const host = summary.host;
  const isUp = summary.status === "up";
  const latest = summary.latest;

  const card = /** @type {HTMLAnchorElement} */ (
    el("a", {
      class: `cp-card cp-host-card ${isUp ? "" : "cp-host-card-offline"}`.trim(),
      attrs: { href: `#/host/${encodeURIComponent(host.id)}` },
    })
  );

  const header = el("div", { class: "cp-host-card-header" });
  header.append(
    statusDot(summary.status),
    el("h3", { class: "cp-host-card-name", text: host.hostname }),
    providerBadge(host.provider),
  );
  card.append(header);

  card.append(
    el("p", {
      class: "cp-host-card-meta",
      text: `${host.os || "unknown"} / ${host.arch || "unknown"}`,
    }),
  );

  if (!isUp) {
    card.append(
      el("p", {
        class: "cp-host-card-offline-note",
        text: `Offline since ${formatRelativeTimeFromUnixSeconds(summary.last_seen, nowMs)}`,
      }),
    );
  }

  if (latest) {
    card.append(metricRow("CPU", latest.cpu_percent));
    card.append(
      metricRow(
        "Memory",
        latest.mem_used_percent,
        `${formatBytes(latest.mem_used)}/${formatBytes(latest.mem_total)}`,
      ),
    );
    card.append(
      metricRow(
        "Disk",
        latest.disk_used_percent,
        `${formatBytes(latest.disk_used)}/${formatBytes(latest.disk_total)}`,
      ),
    );

    const netRow = el("div", { class: "cp-host-card-net" });
    netRow.append(
      el("span", { text: `↓ ${formatBitrate(latest.net_rx_bps)}` }),
      el("span", { text: `↑ ${formatBitrate(latest.net_tx_bps)}` }),
      el("span", { text: `load ${formatLoad(latest.load1)}` }),
    );
    card.append(netRow);

    card.append(
      el("p", {
        class: "cp-host-card-uptime",
        text: `Uptime ${formatDuration(latest.uptime_seconds)} · Last seen ${formatRelativeTimeFromUnixSeconds(summary.last_seen, nowMs)}`,
      }),
    );
  } else {
    card.append(el("p", { class: "cp-host-card-meta", text: "No samples yet." }));
  }

  card.append(egressSection(summary.egress));

  return card;
}

/**
 * egressSection builds the monthly egress mini progress bar shown inside
 * a host card.
 * @param {Object} egress a models.EgressUsage JSON object
 * @returns {HTMLElement}
 */
export function egressSection(egress) {
  const wrap = el("div", { class: "cp-egress-mini" });
  const head = el("div", { class: "cp-metric-head" });
  head.append(el("span", { class: "cp-metric-label", text: "Monthly egress" }), egressLevelChip(egress.level));
  wrap.append(head);

  if (egress.limit_bytes === 0) {
    wrap.append(
      el("p", {
        class: "cp-egress-unlimited",
        text: `No limit · ${formatBytes(egress.tx_bytes)} used`,
      }),
    );
    return wrap;
  }

  wrap.append(
    progressBar({
      value: egress.tx_bytes,
      max: egress.limit_bytes,
      label: "Monthly egress usage",
      levelClass: levelClassForBar(egress.level),
      valueText: `${formatBytes(egress.tx_bytes)} / ${formatBytes(egress.limit_bytes)}`,
    }),
  );
  wrap.append(
    el("p", {
      class: "cp-egress-projection",
      text: `Projected: ${formatBytes(egress.projected_tx_bytes)} by month end`,
    }),
  );
  return wrap;
}

/**
 * emptyState builds a generic empty-state block with a heading, message,
 * and optional code sample shown in a <code> block.
 * @param {Object} opts
 * @param {string} opts.title
 * @param {string} opts.message
 * @param {string} [opts.code]
 * @returns {HTMLElement}
 */
export function emptyState({ title, message, code }) {
  const wrap = el("div", { class: "cp-empty-state" });
  wrap.append(el("h3", { text: title }));
  wrap.append(el("p", { text: message }));
  if (code) {
    const pre = el("pre", { class: "cp-code-block" });
    pre.append(el("code", { text: code }));
    wrap.append(pre);
  }
  return wrap;
}

/**
 * errorBanner builds a non-blocking error banner/toast element.
 * @param {string} message
 * @returns {HTMLElement}
 */
export function errorBanner(message) {
  return el("div", {
    class: "cp-error-banner",
    attrs: { role: "alert" },
    text: message,
  });
}

/**
 * summaryStrip builds the overview page's top summary bar.
 * @param {Object} stats
 * @param {number} stats.hostsUp
 * @param {number} stats.hostsDown
 * @param {number} stats.avgCPU
 * @param {number} stats.fleetEgressBytes
 * @param {number} stats.bucketsTracked
 * @param {number} stats.lastRefreshMs
 * @returns {HTMLElement}
 */
export function summaryStrip(stats) {
  const wrap = el("div", { class: "cp-summary-strip" });

  const items = [
    ["Hosts up", formatNumber(stats.hostsUp)],
    ["Hosts down", formatNumber(stats.hostsDown)],
    ["Avg CPU", formatPercent(stats.avgCPU)],
    ["Fleet egress (mo.)", formatBytes(stats.fleetEgressBytes)],
    ["Buckets tracked", formatNumber(stats.bucketsTracked)],
  ];
  for (const [label, value] of items) {
    const item = el("div", { class: "cp-summary-item" });
    item.append(el("span", { class: "cp-summary-value", text: value }));
    item.append(el("span", { class: "cp-summary-label", text: label }));
    wrap.append(item);
  }

  return wrap;
}
