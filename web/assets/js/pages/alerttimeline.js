// Alert lifecycle occurrences only, not an audit feed. Preserve API order:
// pagination uses started_at, even when an occurrence resolved later.
import { el } from "../ui/components.js";
import { icon } from "../ui/icons.js";
import { eventSeverity, metricLabel, formatMetricValue, formatThreshold, isStorageScopedMetric, isThresholdlessMetric } from "../core/alerts.js";
import { formatDuration } from "../core/format.js";
import { getLang, t } from "../core/i18n.js";

const numeric = (value) => typeof value === "number" && Number.isFinite(value);
export function eventDate(seconds) {
  if (!numeric(seconds) || seconds <= 0) return null;
  const date = new Date(seconds * 1000);
  return Number.isNaN(date.getTime()) ? null : date;
}
const locale = () => getLang() === "ko" ? "ko-KR" : "en-US";
export function groupAlertEvents(events) {
  const groups = [];
  for (const event of events) {
    const date = eventDate(event.started_at);
    const key = date ? `${date.getFullYear()}-${date.getMonth() + 1}-${date.getDate()}` : "unknown";
    const previous = groups[groups.length - 1];
    if (previous?.key === key) previous.events.push(event);
    else groups.push({ key, date, events: [event] });
  }
  return groups;
}

export function eventStatus(state, event) {
  if (state === "firing") return { label: t("alerts_page.firing"), tone: eventSeverity(event), icon: "circleAlert" };
  if (state === "resolved") return { label: t("alerts_page.resolved"), tone: "ok", icon: "circleCheck" };
  return { label: t("alerts_page.activity_unknown"), tone: "neutral", icon: "circleAlert" };
}

export function eventTone(event) {
  if (event.state === "firing") return eventSeverity(event);
  if (event.state === "resolved") return "ok";
  return "neutral";
}

export function eventCondition(event) {
  const metric = event.metric ? metricLabel(event.metric) : t("alerts_page.activity_unknown");
  if (isThresholdlessMetric(event.metric)) return metric;
  const value = formatMetricValue(event.metric, event.value);
  const threshold = numeric(event.threshold) ? formatThreshold(event.metric, event.threshold) : "–";
  return t("alerts_page.value_vs_threshold", { metric, value, threshold });
}

function timestamp(seconds, full = false) {
  const date = eventDate(seconds);
  if (!date) return el("span", { class: "cp-activity-time", text: t("alerts_page.activity_time_unknown") });
  const text = new Intl.DateTimeFormat(locale(), full ? { dateStyle: "medium", timeStyle: "short" } : { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(date);
  return el("time", { class: "cp-activity-time", attrs: { datetime: date.toISOString(), title: new Intl.DateTimeFormat(locale(), { dateStyle: "full", timeStyle: "long" }).format(date) }, text });
}

export function eventHost(event, className = "cp-activity-host") {
  const label = event.hostname || event.host_id || t("alerts_page.activity_host_unknown");
  // Storage-scoped IDs are not host IDs; retain their text without a fake route.
  return event.host_id && !isStorageScopedMetric(event.metric)
    ? el("a", { class: className, attrs: { href: `#/host/${encodeURIComponent(event.host_id)}` }, text: label })
    : el("span", { class: className, text: label });
}

export function deliveryChips(deliveries) {
  const wrap = el("div", { class: "cp-alerts-delivery-chips cp-activity-delivery-summary" });
  if (!deliveries?.length) {
    wrap.append(el("span", { class: "cp-muted-small", text: t("alerts_page.activity_no_deliveries") }));
    return wrap;
  }
  for (const delivery of deliveries) {
    const channel = delivery.channel_name || t("alerts_page.activity_channel_unknown");
    const known = typeof delivery.ok === "boolean";
    const label = !known ? t("alerts_page.activity_delivery_unknown", { channel }) : delivery.ok ? t("alerts_page.delivered", { channel }) : t("alerts_page.delivery_failed", { channel, error: delivery.error || t("alerts_page.delivery_failed_generic") });
    const chip = el("span", { class: `cp-alerts-delivery-chip${known ? delivery.ok ? " cp-alerts-delivery-ok" : " cp-alerts-delivery-fail" : " cp-activity-delivery-unknown"}`, attrs: { title: label } });
    chip.append(icon(known && delivery.ok ? "check" : "circleAlert"), el("span", { text: label }));
    wrap.append(chip);
  }
  return wrap;
}

export function buildAlertTimeline(events, nowSeconds = Math.floor(Date.now() / 1000), { hideHost = false } = {}) {
  const timeline = el("div", { class: "cp-activity-timeline" });
  for (const [index, group] of groupAlertEvents(events).entries()) {
    const headingID = `cp-activity-date-${index}`;
    const section = el("section", { class: "cp-activity-day", attrs: { "aria-labelledby": headingID } });
    const dateLabel = group.date ? new Intl.DateTimeFormat(locale(), { dateStyle: "full" }).format(group.date) : t("alerts_page.activity_date_unknown");
    section.append(el("h3", { class: "cp-activity-date", attrs: { id: headingID }, text: dateLabel }));
    const list = el("ol", { class: "cp-activity-list", attrs: { role: "list" } });
    for (const event of group.events) {
      const status = eventStatus(event.state, event);
      const tone = eventTone(event);
      const item = el("li", { class: `cp-activity-event cp-activity-event-${tone} cp-activity-event-${event.state || "unknown"}`, attrs: { "data-event-id": String(event.id ?? "") } });
      const mark = el("span", { class: "cp-activity-mark", attrs: { "aria-hidden": "true" } });
      mark.append(icon(status.icon));
      const body = el("div", { class: "cp-activity-body" });
      const primary = el("div", { class: "cp-activity-primary" });
      const primaryCopy = el("div", { class: "cp-activity-primary-copy" });
      primaryCopy.append(el("h4", { class: "cp-activity-rule", text: event.rule_name || (event.metric ? metricLabel(event.metric) : t("alerts_page.activity_rule_unknown")) }));
      if (!hideHost) {
        const host = eventHost(event);
        if (event.id != null && host.hasAttribute("href")) host.setAttribute("id", `cp-activity-host-${encodeURIComponent(String(event.id))}`);
        primaryCopy.append(host);
      }
      primaryCopy.append(el("span", { class: `cp-status-pill cp-status-pill-${tone}`, text: status.label }));
      primary.append(primaryCopy, timestamp(event.started_at));
      const secondary = el("div", { class: "cp-activity-secondary" });
      secondary.append(el("span", { class: "cp-activity-condition", text: eventCondition(event) }));
      const start = eventDate(event.started_at);
      const end = event.state === "firing" ? nowSeconds : event.state === "resolved" ? event.resolved_at : null;
      secondary.append(el("span", { class: "cp-activity-lifecycle", text: start && numeric(end) && end >= event.started_at
        ? event.state === "firing" ? t("alerts_page.firing_for", { duration: formatDuration(end - event.started_at) }) : t("alerts_page.activity_duration", { duration: formatDuration(end - event.started_at) })
        : t("alerts_page.activity_duration_unknown") }));
      secondary.append(deliveryChips(event.deliveries));
      body.append(primary, secondary);
      item.append(mark, body); list.append(item);
    }
    section.append(list); timeline.append(section);
  }
  return timeline;
}
