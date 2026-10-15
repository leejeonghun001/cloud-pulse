// Overview grid cards only. HostSummary owns every value and health state;
// this renderer never fetches, estimates missing values, or creates trends.
import { el, progressBar, providerBadge, egressLevelChip, hubOverrideBadge, levelClassForBar } from "../ui/components.js";
import { icon } from "../ui/icons.js";
import { formatBytes, formatBitrate, formatPercent, formatDuration, formatRelativeTimeFromUnixSeconds, formatLoad } from "../core/format.js";
import { agentVersionText, hostUpdateBadgeText } from "../core/updates.js";
import { thresholdBarClass, thresholdLevel } from "./hosttable.js";
import { t } from "../core/i18n.js";

// Accept real numeric API fields, including zero. null/strings/NaN are not zero.
export const measured = (value) => typeof value === "number" && Number.isFinite(value) && value >= 0;
const reading = (value, format) => measured(value) ? format(value) : "–";
const relative = (seconds, nowMs) => measured(seconds) && seconds > 0
  ? formatRelativeTimeFromUnixSeconds(seconds, nowMs) : t("overview.unknown");

export function gridStatus(status) {
  if (status === "up") return { tone: "ok", label: t("overview.card_online") };
  if (status === "down") return { tone: "exceeded", label: t("overview.card_offline") };
  return { tone: "neutral", label: t("overview.unknown") };
}

/**
 * gridAccentLevel reduces only measured host conditions to the one visual
 * priority stripe on a card. This makes a severe real state scannable without
 * recoloring every metric, and never assigns a tone to unknown values.
 * @param {any} row models.HostSummary JSON
 * @returns {"exceeded"|"critical"|"warning"|"neutral"}
 */
export function gridAccentLevel(row) {
  if (row?.status === "down") return "exceeded";
  const levels = [];
  const traffic = row?.egress;
  for (const [bytesKey, limitKey, levelKey] of [["tx_bytes", "limit_bytes", "level"], ["rx_bytes", "rx_limit_bytes", "rx_level"]]) {
    // A traffic level is real only when the API supplied both sides of a
    // bounded measurement; an orphaned level must not color a host card.
    if (measured(traffic?.[bytesKey]) && measured(traffic?.[limitKey]) && traffic[limitKey] > 0) levels.push(traffic[levelKey]);
  }
  if (levels.includes("exceeded")) return "exceeded";
  for (const value of [row?.latest?.cpu_percent, row?.latest?.mem_used_percent, row?.latest?.disk_used_percent]) {
    if (measured(value)) levels.push(thresholdLevel(value));
  }
  if (levels.includes("critical")) return "critical";
  if (levels.includes("warning")) return "warning";
  return "neutral";
}

function emptyTrack() {
  return el("div", { class: "cp-bar-track cp-bar-track-indeterminate", attrs: { "aria-hidden": "true" } });
}

function resourceTile(key, label, value) {
  const tile = el("div", { class: "cp-host-resource", attrs: { "data-resource": key } });
  const head = el("div", { class: "cp-host-resource-head" });
  head.append(el("span", { class: "cp-host-resource-label", text: label }));
  head.append(el("span", { class: "cp-host-resource-value", text: reading(value, formatPercent) }));
  tile.append(head);
  if (measured(value)) {
    const bar = progressBar({ value: Math.min(value, 100), max: 100, label: t("host.metric_usage_label", { label }), levelClass: thresholdBarClass(value), valueText: "" });
    bar.setAttribute("aria-valuetext", formatPercent(value));
    tile.append(bar);
  } else {
    tile.append(emptyTrack());
  }
  return tile;
}

function networkReading(label, value, direction) {
  const item = el("span", { class: "cp-host-network-item", attrs: { "data-network": direction } });
  const heading = el("span", { class: "cp-host-network-label" });
  heading.append(icon(direction === "rx" ? "arrowDown" : "arrowUp"), el("span", { text: label }));
  item.append(heading, el("span", { class: "cp-host-network-value", text: reading(value, formatBitrate) }));
  return item;
}

export function gridEgressRow(label, egress, direction) {
  const out = direction === "out";
  const bytes = out ? egress?.tx_bytes : egress?.rx_bytes;
  const limit = out ? egress?.limit_bytes : egress?.rx_limit_bytes;
  const level = out ? egress?.level : egress?.rx_level;
  const knownLevel = ["ok", "warning", "critical", "exceeded"].includes(level);
  const wrap = el("div", { class: "cp-host-traffic-row", attrs: { "data-traffic": direction } });
  const head = el("div", { class: "cp-host-traffic-head" });
  const labelGroup = el("span", { class: "cp-host-traffic-label" });
  labelGroup.append(el("span", { text: label }));
  if ((out ? egress?.limit_source : egress?.rx_limit_source) === "hub") labelGroup.append(hubOverrideBadge());
  head.append(labelGroup);
  // A level is meaningful only alongside a reported amount and limit.
  if (knownLevel && measured(bytes) && measured(limit)) head.append(egressLevelChip(level));
  wrap.append(head);
  const amount = el("p", { class: "cp-host-traffic-amount" });
  amount.append(el("span", { text: reading(bytes, formatBytes) }));
  amount.append(el("span", {
    class: "cp-host-traffic-limit",
    text: limit === 0 ? t("overview.no_limit") : measured(limit) ? `/ ${formatBytes(limit)}` : t("overview.card_limit_unknown"),
  }));
  wrap.append(amount);
  if (measured(bytes) && measured(limit) && limit > 0) {
    const bar = progressBar({ value: Math.min(bytes, limit), max: limit, label: t("host.egress_usage_label", { label }), levelClass: knownLevel ? levelClassForBar(level) : "cp-host-bar-neutral", valueText: "" });
    // The track is bounded, while its accessible value retains the real usage.
    bar.setAttribute("aria-valuetext", `${formatBytes(bytes)} / ${formatBytes(limit)}`);
    wrap.append(bar);
  } else if (limit !== 0) {
    wrap.append(emptyTrack());
  }
  return wrap;
}

export function hostGridCard(row, nowMs) {
  const { host, latest } = row;
  const status = gridStatus(row.status);
  const accent = gridAccentLevel(row);
  const name = host.hostname || host.id;
  const update = hostUpdateBadgeText(row.update);
  const card = el("a", {
    class: `cp-card cp-card-interactive cp-host-card cp-host-grid-card cp-host-grid-card-${accent}${row.status === "down" ? " cp-card-offline cp-host-card-offline" : ""}`,
    attrs: {
      href: `#/host/${encodeURIComponent(host.id)}`,
      "aria-label": update
        ? t("overview.card_open_host_update", { name, status: status.label })
        : t("overview.card_open_host", { name, status: status.label }),
    },
  });
  const top = el("div", { class: "cp-host-card-topline" });
  const badges = el("div", { class: "cp-host-card-badges" });
  badges.append(providerBadge(host.provider));
  if (update) {
    const updateChip = el("span", {
      class: "cp-host-card-update",
      attrs: {
        title: row.update?.command ? t("overview.agent_update_title_with_command", { label: update.label, command: row.update.command }) : update.label,
        "aria-hidden": "true",
      },
    });
    updateChip.append(icon("cloudUpload"));
    badges.append(updateChip);
  }
  const pill = el("span", { class: `cp-status-pill cp-status-pill-${status.tone} cp-host-card-status` });
  pill.append(el("span", { class: `cp-dot cp-status-dot-${status.tone}`, attrs: { "aria-hidden": "true" } }), el("span", { text: status.label }));
  top.append(badges, pill);
  card.append(top);

  const identity = el("div", { class: "cp-host-card-identity" });
  const mark = el("span", { class: "cp-host-card-mark", attrs: { "aria-hidden": "true" } });
  mark.append(icon("server"));
  const title = el("div", { class: "cp-host-card-title-group" });
  title.append(el("h3", { class: "cp-host-grid-name", text: name }));
  title.append(el("p", { class: "cp-host-card-meta", text: t("overview.grid_os_arch_agent", { os: host.os || t("overview.unknown"), arch: host.arch || t("overview.unknown"), agent: agentVersionText(host.agent_version) }) }));
  identity.append(mark, title);
  card.append(identity);

  const snapshot = el("div", { class: "cp-host-snapshot" });
  snapshot.append(el("span", { text: latest ? t(row.status === "up" ? "overview.card_latest_metrics" : "overview.card_last_reported_metrics") : t("host.no_samples_yet") }));
  if (latest?.ts > 0 && measured(latest.ts)) snapshot.append(el("span", { text: relative(latest.ts, nowMs) }));
  card.append(snapshot);
  const resources = el("div", { class: "cp-host-resources" });
  resources.append(resourceTile("cpu", t("overview.metric_cpu"), latest?.cpu_percent));
  resources.append(resourceTile("memory", t("overview.metric_memory"), latest?.mem_used_percent));
  resources.append(resourceTile("disk", t("overview.metric_disk"), latest?.disk_used_percent));
  card.append(resources);

  const meta = el("div", { class: "cp-host-meta-row" });
  meta.append(networkReading(t("overview.card_receive"), latest?.net_rx_bps, "rx"), networkReading(t("overview.card_send"), latest?.net_tx_bps, "tx"));
  meta.append(el("span", { class: "cp-host-runtime", text: t("overview.load_value", { value: reading(latest?.load1, formatLoad) }) }));
  meta.append(el("span", { class: "cp-host-runtime", text: t("host.badge_uptime", { value: reading(latest?.uptime_seconds, formatDuration) }) }));
  card.append(meta);

  const traffic = el("div", { class: "cp-host-card-traffic" });
  traffic.append(el("h4", { class: "cp-host-section-label", text: t("overview.card_monthly_traffic") }));
  traffic.append(gridEgressRow(t("host.egress_outbound_label"), row.egress, "out"), gridEgressRow(t("host.egress_inbound_label"), row.egress, "in"));
  card.append(traffic);

  const footer = el("div", { class: "cp-host-card-footer" });
  footer.append(el("span", { class: "cp-host-card-last-seen", text: t("overview.last_seen", { relative: relative(row.last_seen, nowMs) }) }));
  const affordance = el("span", { class: "cp-host-card-open", attrs: { "aria-hidden": "true" } });
  affordance.append(el("span", { text: t("overview.card_view_details") }), icon("chevronRight"));
  footer.append(affordance);
  card.append(footer);
  return card;
}
