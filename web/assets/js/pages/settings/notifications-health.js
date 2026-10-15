// settings/notifications-health.js — the Notifications section's
// "Health" sub-tab (SPEC-v0.8 §3-2): a channel-health report fetched
// from GET /api/v1/alerts/channels/health, rendered as one card per
// channel (status badge, 7d/30d delivery stats, a 12-week synthetic-
// test result strip with a text alternative, a diagnosis card on the
// most recent failure, a Run now button, and the weekly synthetic-test
// schedule toggle). Owned by the channel-health stage; see
// pages/settings/notifications.js for the tab row this mounts under.
import { el, clearChildren, emptyState, errorBanner } from "../../ui/components.js";
import { icon } from "../../ui/icons.js";
import { showToast } from "../../ui/toast.js";
import { t, formatDateTimeLocalized } from "../../core/i18n.js";
import { getChannelsHealth, runChannelSyntheticTest, setChannelHealthSettings, ApiError } from "../../core/api.js";
import { diagnosisViewModel } from "../../core/diagnosis-view.js";
import {
  utcToLocal,
  localToUtc,
  formatMinuteOfDay,
  parseMinuteOfDay,
  WEEKDAY_LABEL_KEYS,
} from "../../core/channel-health-schedule.js";

/** SYNTHETIC_STRIP_LENGTH mirrors the hub's own 12-week cap
 * (channelHealthSyntheticStripLimit in internal/hub/
 * channelhealthroutes.go) purely for the "no runs yet" padding below —
 * the API already returns at most this many entries. */
const SYNTHETIC_STRIP_LENGTH = 12;

const STATE_CHIP_CLASS = {
  healthy: "cp-chip-ok",
  degraded: "cp-chip-warning",
  failing: "cp-chip-critical",
  unknown: "cp-chip-other",
};

const STATE_LABEL_KEY = {
  healthy: "health.state_healthy",
  degraded: "health.state_degraded",
  failing: "health.state_failing",
  unknown: "health.state_unknown",
};

const OUTCOME_LABEL_KEY = {
  accepted: "health.synthetic_result_accepted",
  verified: "health.synthetic_result_verified",
  failed: "health.synthetic_result_failed",
};

const OUTCOME_CHIP_CLASS = {
  accepted: "cp-chip-ok",
  verified: "cp-chip-ok",
  failed: "cp-chip-critical",
};

/**
 * mountChannelHealthTab renders the Health sub-tab into container.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} [ctx.announce]
 * @returns {{refresh: () => Promise<void>, teardown: () => void}}
 */
export function mountChannelHealthTab(container, { announce } = {}) {
  const controller = new AbortController();

  async function refresh() {
    clearChildren(container);
    let report;
    try {
      report = await getChannelsHealth(controller.signal);
    } catch (err) {
      if (err?.name === "AbortError") return;
      container.append(errorBanner(describeError(err)));
      return;
    }

    container.append(el("p", { class: "cp-muted-small", text: t("health.page_description") }));
    container.append(el("p", { class: "cp-notify-source-note", text: t("settings.notifications.health_evidence_note") }));

    if (report.banner_needed && report.banner_channel_names?.length) {
      container.append(bannerNotice(report.banner_channel_names));
    }

    if (!report.channels || report.channels.length === 0) {
      container.append(
        emptyState({
          title: t("settings.notifications.empty_title"),
          message: t("settings.notifications.empty_message"),
          iconName: "activity",
        }),
      );
      return;
    }

    const grid = el("div", { class: "cp-channel-health-grid" });
    for (const channelHealth of report.channels) {
      grid.append(channelHealthCard({ channelHealth, signal: controller.signal, announce, onChanged: refresh }));
    }
    container.append(grid);
  }

  function teardown() {
    controller.abort();
  }

  return { refresh, teardown };
}

/**
 * bannerNotice renders an inline notice (not the global app-shell
 * banner — see ui/channel-health-banner.js for that) summarizing which
 * channel(s) have no eligible fallback right now, for immediate
 * context directly on the Health tab itself.
 */
function bannerNotice(channelNames) {
  const wrap = el("div", { class: "cp-error-banner", attrs: { role: "alert" } });
  const text =
    channelNames.length === 1
      ? t("health.banner_message", { count: 1, name: channelNames[0] })
      : t("health.banner_message", { count: channelNames.length });
  wrap.append(el("p", { text }));
  return wrap;
}

function channelHealthCard({ channelHealth, signal, announce, onChanged }) {
  const card = el("div", { class: "cp-card cp-channel-health-card" });

  const head = el("div", { class: "cp-metric-head" });
  head.append(el("h3", { class: "cp-card-title", text: channelHealth.channel_name }));
  head.append(
    el("span", {
      class: `cp-chip ${STATE_CHIP_CLASS[channelHealth.state] || "cp-chip-other"}`,
      text: t(STATE_LABEL_KEY[channelHealth.state] || "health.state_unknown"),
    }),
  );
  card.append(head);
  const last = channelHealth.last_synthetic;
  card.append(el("p", { class: "cp-muted-small", text: last
    ? t("settings.notifications.last_synthetic_evidence", { outcome: t(OUTCOME_LABEL_KEY[last.outcome] || "health.synthetic_result_none"), when: formatWhen(last.started_at) })
    : t("settings.notifications.health_no_synthetic") }));

  card.append(statsRow(t("health.stats_7d_title"), channelHealth.stats_7d));
  card.append(statsRow(t("health.stats_30d_title"), channelHealth.stats_30d));

  card.append(syntheticStrip(channelHealth.recent_synthetic || []));

  if (channelHealth.last_synthetic?.outcome === "failed") {
    card.append(diagnosisCard(channelHealth.last_synthetic));
  }

  card.append(scheduleControls({ channelHealth, signal, announce, onChanged }));

  const runBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button", "aria-label": t("health.run_now_aria", { name: channelHealth.channel_name }) } });
  runBtn.append(icon("send"), el("span", { text: t("health.run_now") }));
  runBtn.addEventListener("click", async () => {
    runBtn.disabled = true;
    const originalText = runBtn.querySelector("span").textContent;
    runBtn.querySelector("span").textContent = t("health.running");
    try {
      await runChannelSyntheticTest(channelHealth.channel_id, signal);
      announce?.(t("health.run_now"));
      await onChanged();
    } catch (err) {
      if (err?.name === "AbortError") return;
      showToast({ message: describeError(err), variant: "error" });
    } finally {
      runBtn.disabled = false;
      const span = runBtn.querySelector("span");
      if (span) span.textContent = originalText;
    }
  });
  card.append(runBtn);

  return card;
}

function statsRow(title, stats) {
  const wrap = el("div", { class: "cp-channel-health-stats" });
  wrap.append(el("h4", { class: "cp-muted-small", text: title }));
  if (!stats || stats.total === 0) {
    wrap.append(el("p", { class: "cp-muted-small", text: t("health.no_data") }));
    return wrap;
  }
  const rate = Math.round((stats.success / stats.total) * 1000) / 10;
  const line = el("p", { text: t("health.success_rate", { rate }) });
  wrap.append(line);

  const details = el("p", { class: "cp-muted-small" });
  const parts = [];
  parts.push(stats.last_success_at ? t("health.last_success", { when: formatWhen(stats.last_success_at) }) : t("health.last_success", { when: t("health.never") }));
  parts.push(stats.last_failure_at ? t("health.last_failure", { when: formatWhen(stats.last_failure_at) }) : t("health.last_failure", { when: t("health.never") }));
  if (stats.avg_latency_ms) parts.push(t("health.avg_latency", { ms: stats.avg_latency_ms }));
  if (stats.max_latency_ms) parts.push(t("health.max_latency", { ms: stats.max_latency_ms }));
  details.textContent = parts.join(" · ");
  wrap.append(details);
  return wrap;
}

/**
 * syntheticStrip renders the last-12-weeks synthetic-test result strip
 * as a row of small colored chips, each with a title tooltip and a
 * shared visually-hidden text alternative (SPEC-v0.8 §3-2's
 * accessibility requirement) summarizing every result in one string
 * for screen readers, rather than relying on 12 individual chip
 * tooltips alone.
 */
function syntheticStrip(results) {
  const wrap = el("div", { class: "cp-channel-health-strip-wrap" });
  wrap.append(el("h4", { class: "cp-muted-small", text: t("health.synthetic_strip_title") }));

  if (results.length === 0) {
    wrap.append(el("p", { class: "cp-muted-small", text: t("health.synthetic_strip_empty") }));
    return wrap;
  }

  // recent_synthetic is newest-first from the API; display oldest-first
  // (left-to-right chronological, matching a calendar/chart reading
  // order) and pad to SYNTHETIC_STRIP_LENGTH with "no run" placeholders
  // on the left so the strip's length is visually stable regardless of
  // how much history exists yet.
  const chronological = [...results].reverse();
  const padCount = Math.max(0, SYNTHETIC_STRIP_LENGTH - chronological.length);

  const strip = el("div", { class: "cp-channel-health-strip", attrs: { "aria-hidden": "true" } });
  for (let i = 0; i < padCount; i++) {
    strip.append(el("span", { class: "cp-channel-health-strip-cell cp-chip-other", attrs: { title: t("health.synthetic_result_none") } }));
  }
  for (const r of chronological) {
    const label = `${t(OUTCOME_LABEL_KEY[r.outcome] || "health.synthetic_result_none")} — ${formatWhen(r.started_at)}`;
    strip.append(
      el("span", {
        class: `cp-channel-health-strip-cell ${OUTCOME_CHIP_CLASS[r.outcome] || "cp-chip-other"}`,
        attrs: { title: label },
      }),
    );
  }
  wrap.append(strip);
  const failed = chronological.filter((r) => r.outcome === "failed").length;
  wrap.append(el("p", { class: "cp-muted-small", attrs: { "aria-hidden": "true" }, text: t("health.synthetic_strip_counts", { passed: chronological.length - failed, failed }) }));

  const summary = chronological
    .map((r) => `${t(OUTCOME_LABEL_KEY[r.outcome] || "health.synthetic_result_none")} (${formatWhen(r.started_at)})`)
    .join(", ");
  wrap.append(el("p", { class: "cp-sr-only", text: t("health.synthetic_strip_aria", { summary }) }));

  return wrap;
}

/**
 * diagnosisCard reuses core/diagnosis-view.js's view model to render
 * the most recent synthetic-test failure's diagnosis (SPEC-v0.6 §4
 * reuse, per SPEC-v0.8 §3-2's explicit instruction).
 */
function diagnosisCard(lastSynthetic) {
  const vm = diagnosisViewModel("", { code: lastSynthetic.diagnosis_code, detail: lastSynthetic.detail });
  const card = el("div", { class: "cp-diagnosis-card", attrs: { role: "alert" } });
  const head = el("div", { class: "cp-diagnosis-card-head" });
  head.append(icon("circleX"));
  head.append(el("strong", { text: t("health.diagnosis_title") }));
  card.append(head);
  if (vm.detail) card.append(el("p", { text: vm.detail }));
  return card;
}

/**
 * scheduleControls renders the weekly synthetic-test opt-in toggle
 * plus weekday/time fields (stored UTC, displayed local — see
 * core/channel-health-schedule.js).
 */
function scheduleControls({ channelHealth, signal, announce, onChanged }) {
  const wrap = el("div", { class: "cp-channel-health-schedule" });
  const settings = channelHealth.settings || { enabled: false, weekday: 0, minute_of_day: 0 };
  const local = utcToLocal(settings.weekday, settings.minute_of_day);
  const idBase = `cp-health-schedule-${channelHealth.channel_id}`;

  const toggleField = el("label", { class: "cp-checkbox-field" });
  const toggle = /** @type {HTMLInputElement} */ (el("input", { attrs: { type: "checkbox" } }));
  toggle.checked = Boolean(settings.enabled);
  toggleField.append(toggle, el("span", { text: t("health.schedule_toggle") }));
  wrap.append(toggleField);
  wrap.append(el("p", { class: "cp-muted-small", text: t("health.schedule_toggle_hint") }));

  const fieldsRow = el("div", { class: "cp-channel-health-schedule-fields" });

  const weekdaySelectWrap = el("div", { class: "cp-field" });
  weekdaySelectWrap.append(el("label", { class: "cp-label", attrs: { for: `${idBase}-weekday` }, text: t("health.schedule_weekday_label") }));
  const weekdaySelect = /** @type {HTMLSelectElement} */ (el("select", { class: "cp-select", attrs: { id: `${idBase}-weekday` } }));
  for (let i = 0; i < 7; i++) {
    weekdaySelect.append(el("option", { text: t(`health.weekday_${WEEKDAY_LABEL_KEYS[i]}`), attrs: { value: String(i) } }));
  }
  weekdaySelect.value = String(local.weekday);
  weekdaySelectWrap.append(weekdaySelect);
  fieldsRow.append(weekdaySelectWrap);

  const timeFieldWrap = el("div", { class: "cp-field" });
  timeFieldWrap.append(el("label", { class: "cp-label", attrs: { for: `${idBase}-time` }, text: t("health.schedule_time_label") }));
  const timeInput = /** @type {HTMLInputElement} */ (el("input", { class: "cp-input", attrs: { id: `${idBase}-time`, type: "time", value: formatMinuteOfDay(local.minuteOfDay) } }));
  timeFieldWrap.append(timeInput);
  timeFieldWrap.append(el("p", { class: "cp-muted-small", text: t("health.schedule_time_hint") }));
  fieldsRow.append(timeFieldWrap);

  wrap.append(fieldsRow);

  const statusEl = el("p", { class: "cp-muted-small", attrs: { role: "status", "aria-live": "polite" } });
  wrap.append(statusEl);

  async function save() {
    const minuteOfDay = parseMinuteOfDay(timeInput.value);
    if (minuteOfDay === null) return;
    const utc = localToUtc(Number(weekdaySelect.value), minuteOfDay);
    try {
      await setChannelHealthSettings(channelHealth.channel_id, { enabled: toggle.checked, weekday: utc.weekday, minute_of_day: utc.minuteOfDay }, signal);
      statusEl.textContent = t("health.schedule_saved");
      announce?.(t("health.schedule_saved"));
      await onChanged();
    } catch (err) {
      if (err?.name === "AbortError") return;
      statusEl.textContent = t("health.schedule_save_failed", { error: describeError(err) });
    }
  }

  toggle.addEventListener("change", save);
  weekdaySelect.addEventListener("change", save);
  timeInput.addEventListener("change", save);

  return wrap;
}

function formatWhen(unixSeconds) {
  if (!unixSeconds) return t("health.never");
  return formatDateTimeLocalized(unixSeconds * 1000, { dateStyle: "medium", timeStyle: "short" });
}

function describeError(err) {
  if (err instanceof ApiError) return err.message;
  return String(err?.message || err);
}
