// Host detail presentation only. All values come from HostSummary; no new
// measurements, chart samples, requests or health thresholds are inferred.
import { el, progressBar, providerBadge, egressLevelChip, hubOverrideBadge, levelClassForBar } from "../ui/components.js";
import { icon } from "../ui/icons.js";
import { hostUpdateBadgeText } from "../core/updates.js";
import { formatBytes, formatBitrate, formatPercent, formatDuration, formatRelativeTimeFromUnixSeconds, formatLoad } from "../core/format.js";
import { t } from "../core/i18n.js";
import { thresholdBarClass } from "./hosttable.js";

export const isMeasurement = (value) => typeof value === "number" && Number.isFinite(value) && value >= 0;
export const detailReading = (value, formatter) => isMeasurement(value) ? formatter(value) : "–";
const unknown = () => t("host.detail_unknown");
const relative = (seconds, nowMs) => isMeasurement(seconds) && seconds > 0 ? formatRelativeTimeFromUnixSeconds(seconds, nowMs) : unknown();
const emptyTrack = () => el("div", { class: "cp-bar-track cp-bar-track-indeterminate cp-detail-empty-track", attrs: { "aria-hidden": "true" } });

export function detailHostname(name) {
  const heading = el("h1", { class: "cp-page-title", attrs: { "aria-label": name } });
  // Optional breaks add no characters to copying or the accessible name.
  // Prefer hostname separators; overflow-wrap remains the last-resort escape.
  for (const part of name.split(/(?<=[.\-_])/u)) {
    heading.append(document.createTextNode(part));
    if (/[.\-_]$/u.test(part)) heading.append(el("wbr", { attrs: { "aria-hidden": "true" } }));
  }
  return heading;
}

function metadata(label, value, className = "cp-detail-meta-pair") {
  return el("div", { class: className, children: [el("dt", { text: label }), el("dd", { text: value })] });
}

export function buildDetailIdentity(summary, nowMs) {
  const h = summary.host;
  const top = el("div", { class: "cp-host-detail-top" });
  const identity = el("div", { class: "cp-host-detail-title-group" });
  const mark = el("span", { class: "cp-detail-host-mark", attrs: { "aria-hidden": "true" } });
  mark.append(icon("server"));
  const title = el("div", { class: "cp-detail-title-copy" });
  const badges = el("div", { class: "cp-detail-identity-badges" });
  badges.append(providerBadge(h.provider));
  const tone = summary.status === "up" ? "ok" : summary.status === "down" ? "exceeded" : "neutral";
  const label = summary.status === "up" ? t("host.detail_online") : summary.status === "down" ? t("host.detail_offline") : unknown();
  const pill = el("span", { class: `cp-status-pill cp-status-pill-${tone}` });
  pill.append(el("span", { class: `cp-dot cp-status-dot-${tone}`, attrs: { "aria-hidden": "true" } }), el("span", { text: label }));
  badges.append(pill);
  const update = hostUpdateBadgeText(summary.update);
  if (update) {
    // Do not replace the SPA's #/host route with an in-page hash.
    const jump = el("button", { class: "cp-detail-update-link", attrs: { id: "cp-detail-update-jump", type: "button" }, text: update.text });
    jump.addEventListener("click", () => {
      const panel = document.getElementById("cp-detail-agent-update");
      panel?.scrollIntoView({ block: "start" });
      panel?.querySelector("button, a")?.focus({ preventScroll: true });
    });
    badges.append(jump);
  }
  title.append(badges, detailHostname(h.hostname || h.id));
  title.append(el("p", { class: "cp-detail-last-seen", text: t("host.detail_last_seen", { relative: relative(summary.last_seen, nowMs) }) }));
  identity.append(mark, title);
  top.append(identity);
  const meta = el("dl", { class: "cp-detail-meta" });
  meta.append(metadata(t("host.detail_platform"), `${h.platform || h.os || unknown()} ${h.platform_version || ""}`.trim()));
  meta.append(metadata(t("host.detail_architecture"), h.arch || unknown()));
  meta.append(metadata(t("host.detail_processor"), `${h.cpu_model || unknown()} · ${t("host.detail_cores", { cores: isMeasurement(h.cpu_cores) && h.cpu_cores > 0 ? h.cpu_cores : unknown() })}`, "cp-detail-meta-pair cp-detail-meta-processor"));
  meta.append(metadata(t("host.detail_kernel"), h.kernel_version || unknown()));
  meta.append(metadata(t("host.detail_agent"), h.agent_version || unknown()));
  meta.append(metadata(t("host.detail_uptime"), detailReading(summary.latest?.uptime_seconds, formatDuration)));
  return { top, meta };
}

function resource(label, key, value, detail, description) {
  const tile = el("div", { class: "cp-detail-resource", attrs: { "data-detail-resource": key } });
  const head = el("div", { class: "cp-detail-resource-head" });
  head.append(el("h3", { text: label }));
  tile.append(head, el("p", { class: "cp-detail-resource-value", text: detailReading(value, formatPercent) }));
  tile.append(el("p", { class: "cp-detail-resource-description", text: detail || description }));
  if (isMeasurement(value)) {
    const bar = progressBar({ value: Math.min(value, 100), max: 100, label: t("host.metric_usage_label", { label }), levelClass: thresholdBarClass(value), valueText: "" });
    bar.setAttribute("aria-valuetext", formatPercent(value));
    tile.append(bar);
  } else tile.append(emptyTrack());
  return tile;
}

export function buildDetailResources(summary, nowMs) {
  const latest = summary.latest;
  const section = el("section", { class: "cp-detail-resource-section", attrs: { "aria-labelledby": "cp-detail-resources-heading" } });
  const head = el("div", { class: "cp-detail-section-head" });
  head.append(el("h2", { class: "cp-detail-section-title", attrs: { id: "cp-detail-resources-heading" }, text: latest ? t(summary.status === "up" ? "host.detail_latest_metrics" : "host.detail_last_reported") : t("host.no_samples_yet") }));
  if (latest) head.append(el("span", { class: "cp-muted-small", text: t("host.detail_sample_age", { relative: relative(latest.ts, nowMs) }) }));
  section.append(head);
  const grid = el("div", { class: "cp-detail-resources" });
  const capacity = (used, total) => isMeasurement(used) && isMeasurement(total) && total > 0 ? `${formatBytes(used)} / ${formatBytes(total)}` : t("host.detail_capacity_unknown");
  grid.append(resource(t("host.chart_series_cpu"), "cpu", latest?.cpu_percent, null, t("host.chart_cpu_description")));
  grid.append(resource(t("host.chart_series_memory"), "memory", latest?.mem_used_percent, capacity(latest?.mem_used, latest?.mem_total)));
  grid.append(resource(t("host.chart_series_disk"), "disk", latest?.disk_used_percent, capacity(latest?.disk_used, latest?.disk_total)));
  const network = el("div", { class: "cp-detail-resource cp-detail-network", attrs: { "data-detail-resource": "network" } });
  network.append(el("h3", { text: t("host.chart_bandwidth_title") }));
  for (const [key, label, value] of [["rx", t("host.detail_receive"), latest?.net_rx_bps], ["tx", t("host.detail_send"), latest?.net_tx_bps]]) {
    const row = el("div", { class: "cp-detail-network-reading", attrs: { "data-detail-network": key } });
    row.append(el("span", { text: label }), el("strong", { text: detailReading(value, formatBitrate) }));
    network.append(row);
  }
  network.append(el("p", { class: "cp-detail-resource-description", text: t("host.detail_load", { value: detailReading(latest?.load1, formatLoad) }) }));
  grid.append(network);
  section.append(grid);
  return section;
}

export function detailTrafficDirection(egress, direction) {
  const out = direction === "out";
  const label = t(out ? "host.egress_outbound_label" : "host.egress_inbound_label");
  const bytes = out ? egress?.tx_bytes : egress?.rx_bytes;
  const limit = out ? egress?.limit_bytes : egress?.rx_limit_bytes;
  const projected = out ? egress?.projected_tx_bytes : egress?.projected_rx_bytes;
  const level = out ? egress?.level : egress?.rx_level;
  const validLevel = ["ok", "warning", "critical", "exceeded"].includes(level);
  const row = el("div", { class: "cp-detail-traffic-direction", attrs: { "data-detail-traffic": direction } });
  const head = el("div", { class: "cp-detail-section-head" });
  const heading = el("h3", { class: "cp-detail-section-title", text: label });
  if ((out ? egress?.limit_source : egress?.rx_limit_source) === "hub") heading.append(hubOverrideBadge());
  head.append(heading);
  if (isMeasurement(bytes) && isMeasurement(limit) && validLevel) head.append(egressLevelChip(level));
  row.append(head);
  const amount = el("p", { class: "cp-detail-traffic-value" });
  amount.append(el("strong", { text: detailReading(bytes, formatBytes) }), el("span", { text: limit === 0 ? t("host.detail_no_limit") : isMeasurement(limit) ? `/ ${formatBytes(limit)}` : t("host.detail_limit_unknown") }));
  row.append(amount);
  if (isMeasurement(bytes) && isMeasurement(limit) && limit > 0) {
    const barLabel = out ? t("host.egress_outbound_bar_label") : t("host.egress_inbound_bar_label");
    const bar = progressBar({ value: Math.min(bytes, limit), max: limit, label: barLabel, levelClass: validLevel ? levelClassForBar(level) : "cp-detail-bar-neutral", valueText: "" });
    bar.setAttribute("aria-valuetext", `${formatBytes(bytes)} / ${formatBytes(limit)}`);
    row.append(bar);
  } else if (limit !== 0) row.append(emptyTrack());
  if (limit !== 0 && isMeasurement(projected)) row.append(el("p", { class: "cp-detail-resource-description", text: t("host.egress_projected", { amount: formatBytes(projected) }) }));
  return row;
}
