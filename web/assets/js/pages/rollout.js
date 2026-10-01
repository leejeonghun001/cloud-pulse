// rollout.js — Fleet → Rollout page (SPEC-v0.8 §3-1), routed at
// "#/rollout" (the former "#/updates" route redirects here, see
// main.js's routeUpdatesRedirect). Integrates and extends the former
// dedicated Updates page (pages/updates.js, still present but no
// longer routed to — see notes/v08-partB-prep.md §5): summary cards,
// version distribution by OS, outdated-host list, batch history with
// the existing progress panel, and an "update all remotely-updatable
// outdated hosts" action with a confirm dialog + parallelism choice.
import { el, clearChildren, emptyState, errorBanner } from "../ui/components.js";
import { icon } from "../ui/icons.js";
import { showToast } from "../ui/toast.js";
import { selectField } from "../ui/select.js";
import { createDialog, buildDialogActions } from "../ui/dialog.js";
import { t } from "../core/i18n.js";
import { formatRelativeTimeFromUnixSeconds } from "../core/format.js";
import {
  getHosts,
  getFleetVersions,
  getUpdateBatches,
  getUpdateJobs,
  createUpdateBatch,
  retryUpdateJob,
  cancelUpdateBatch,
  ApiError,
} from "../core/api.js";
import {
  capabilityLabel,
  osLabel,
  summaryCardFigures,
  versionDistributionByOS,
  segmentPercentages,
  outdatedHostRows,
  behindForText,
  remotelyUpdatableOutdatedHostIDs,
  batchStatusText,
  batchDurationText,
  failureReasonSummary,
  parallelismOptions,
} from "../core/rollout.js";

/** PROGRESS_POLL_MS is the auto-refresh interval for the progress
 * panel while a batch has any non-terminal job (SPEC-v0.6 §2: "5초
 * 간격 자동 갱신"). */
export const PROGRESS_POLL_MS = 5000;

/** JOB_STATE_KEYS/CLASSES map a models.UpdateJobState to its i18n key
 * and a chip color class. */
const JOB_STATE_KEYS = {
  queued: "rollout.job_state_queued",
  in_progress: "rollout.job_state_in_progress",
  succeeded: "rollout.job_state_succeeded",
  failed: "rollout.job_state_failed",
};
const JOB_STATE_CLASSES = { queued: "cp-chip-other", in_progress: "cp-chip-warning", succeeded: "cp-chip-ok", failed: "cp-chip-critical" };

/** REASON_KEYS maps a models.UpdateJobReason to its i18n key. */
const REASON_KEYS = {
  not_enabled: "rollout.reason_not_enabled",
  unsupported: "rollout.reason_unsupported",
  timeout: "rollout.reason_timeout",
  download_failed: "rollout.reason_download_failed",
  checksum_mismatch: "rollout.reason_checksum_mismatch",
  verify_failed: "rollout.reason_verify_failed",
  restart_failed: "rollout.reason_restart_failed",
  downgrade_refused: "rollout.reason_downgrade_refused",
  unknown: "rollout.reason_unknown",
};

function reasonLabel(reason) {
  const key = REASON_KEYS[reason];
  return key ? t(key) : reason;
}

/**
 * isTerminalJobState reports whether state is a terminal state
 * (succeeded/failed) — used to decide whether the progress panel
 * should keep auto-polling.
 * @param {string} state
 * @returns {boolean}
 */
export function isTerminalJobState(state) {
  return state === "succeeded" || state === "failed";
}

/**
 * mountRolloutPage renders the Rollout page's full content into
 * container.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @returns {{refresh: () => Promise<void>, teardown: () => void}}
 */
export function mountRolloutPage(container, { announce }) {
  const controller = new AbortController();
  /** @type {ReturnType<typeof setTimeout>|null} */
  let pollTimer = null;
  /** @type {string|null} currently displayed batch id, if any */
  let activeBatchID = null;

  async function refresh() {
    clearChildren(container);
    if (pollTimer) {
      clearTimeout(pollTimer);
      pollTimer = null;
    }

    const root = el("div", { class: "cp-page cp-rollout-page" });
    root.append(el("h1", { class: "cp-page-title", text: t("rollout.title") }));
    root.append(el("p", { class: "cp-page-subtitle", text: t("rollout.subtitle") }));

    let versions, hostsResp, batchesResp;
    try {
      [versions, hostsResp, batchesResp] = await Promise.all([
        getFleetVersions(controller.signal),
        getHosts(controller.signal),
        getUpdateBatches({ limit: 20 }, controller.signal),
      ]);
    } catch (err) {
      if (err?.name === "AbortError") return;
      root.append(errorBanner(describeError(err)));
      container.append(root);
      return;
    }

    const hosts = hostsResp?.hosts || [];
    const batches = batchesResp?.batches || [];
    const nowUnixSeconds = Math.floor(Date.now() / 1000);

    root.append(summaryCards(versions));
    root.append(distributionSection(versions));
    root.append(outdatedHostsSection(versions, nowUnixSeconds, { signal: controller.signal, announce, onBatchCreated: (id) => showBatch(id) }));

    const progressHost = el("div", { class: "cp-rollout-progress-host" });
    root.append(batchHistorySection(batches, progressHost, { signal: controller.signal, announce, showBatch: (id) => showBatch(id) }));
    root.append(agentsSection({ hosts, signal: controller.signal, announce, onBatchCreated: (id) => showBatch(id) }));
    root.append(progressHost);

    container.append(root);
    announce(t("rollout.title"));

    async function showBatch(batchID) {
      activeBatchID = batchID;
      await renderProgress(progressHost, batchID, controller.signal, announce, scheduleNextPoll);
    }

    function scheduleNextPoll(hasPending) {
      if (pollTimer) clearTimeout(pollTimer);
      if (!hasPending || controller.signal.aborted) return;
      pollTimer = setTimeout(async () => {
        if (!activeBatchID || controller.signal.aborted) return;
        await renderProgress(progressHost, activeBatchID, controller.signal, announce, scheduleNextPoll);
      }, PROGRESS_POLL_MS);
    }
  }

  function teardown() {
    if (pollTimer) clearTimeout(pollTimer);
    controller.abort();
  }

  return { refresh, teardown };
}

/** summaryCards builds the four summary cards row. */
function summaryCards(versions) {
  const figures = summaryCardFigures(versions);
  const section = el("section", { class: "cp-rollout-summary-cards" });
  section.append(
    summaryCard(t("rollout.summary_up_to_date"), `${figures.upToDateCount} (${figures.upToDatePercent}%)`),
    summaryCard(t("rollout.summary_outdated"), String(figures.outdatedCount)),
    summaryCard(t("rollout.summary_unknown"), String(figures.unknownCount)),
    summaryCard(t("rollout.summary_capable"), String(figures.capableCount)),
    summaryCard(t("rollout.summary_not_opted_in"), String(figures.notOptedInCount)),
    summaryCard(t("rollout.summary_unsupported"), String(figures.unsupportedCount)),
  );
  return section;
}

function summaryCard(label, value) {
  const card = el("div", { class: "cp-card cp-rollout-summary-card" });
  card.append(el("span", { class: "cp-rollout-summary-value", text: value }));
  card.append(el("span", { class: "cp-rollout-summary-label", text: label }));
  return card;
}

/**
 * distributionSection builds the version-distribution-by-OS stacked
 * bars (pure CSS, accessible: each bar is a labeled group of
 * percentage-width segments, plus a full text-alternative table).
 */
function distributionSection(versions) {
  const section = el("section", { class: "cp-section", attrs: { "aria-labelledby": "cp-rollout-distribution-heading" } });
  section.append(el("h2", { id: "cp-rollout-distribution-heading", text: t("rollout.distribution_title") }));

  const bars = versionDistributionByOS(versions?.distribution || []);
  if (bars.length === 0) {
    section.append(emptyState({ title: t("rollout.distribution_empty"), message: "" }));
    return section;
  }

  const chart = el("div", { class: "cp-rollout-distribution-chart" });
  for (const bar of bars) {
    chart.append(distributionBar(bar));
  }
  section.append(chart);

  section.append(distributionTable(bars));
  return section;
}

/** SEGMENT_PALETTE cycles a small fixed set of classes across a bar's
 * segments so distinct versions are visually distinguishable without
 * per-version color assignment logic. */
const SEGMENT_PALETTE = ["cp-rollout-seg-0", "cp-rollout-seg-1", "cp-rollout-seg-2", "cp-rollout-seg-3", "cp-rollout-seg-4"];

function distributionBar(bar) {
  const segments = segmentPercentages(bar);
  const wrap = el("div", { class: "cp-rollout-distribution-row" });
  wrap.append(el("span", { class: "cp-rollout-distribution-os-label", text: osLabel(bar.os) }));

  const track = el("div", {
    class: "cp-rollout-distribution-track",
    attrs: { role: "img", "aria-label": `${osLabel(bar.os)}: ${segments.map((s) => `${s.version} ${s.percent}%`).join(", ")}` },
  });
  segments.forEach((seg, i) => {
    const fill = el("div", { class: `cp-rollout-distribution-fill ${SEGMENT_PALETTE[i % SEGMENT_PALETTE.length]}` });
    fill.style.width = `${seg.percent}%`;
    fill.title = `${seg.version}: ${seg.count} (${seg.percent}%)`;
    track.append(fill);
  });
  wrap.append(track);
  wrap.append(el("span", { class: "cp-muted-small cp-rollout-distribution-total", text: String(bar.total) }));
  return wrap;
}

/** distributionTable builds the plain-text-table alternative to the
 * bar chart (SPEC-v0.8 §3-1: "접근성 텍스트 포함"). */
function distributionTable(bars) {
  const wrap = el("div", { class: "cp-table-wrap cp-rollout-distribution-table-wrap" });
  const table = el("table", { class: "cp-table", attrs: { "aria-label": t("rollout.distribution_table_caption") } });
  const caption = el("caption", { class: "sr-only", text: t("rollout.distribution_table_caption") });
  table.append(caption);

  const thead = el("thead");
  const headRow = el("tr");
  for (const label of [t("rollout.distribution_col_os"), t("rollout.distribution_col_version"), t("rollout.distribution_col_count"), t("rollout.distribution_col_percent")]) {
    headRow.append(el("th", { text: label }));
  }
  thead.append(headRow);
  table.append(thead);

  const tbody = el("tbody");
  for (const bar of bars) {
    for (const seg of segmentPercentages(bar)) {
      const row = el("tr");
      row.append(el("td", { text: osLabel(bar.os) }));
      row.append(el("td", { text: seg.version }));
      row.append(el("td", { text: String(seg.count) }));
      row.append(el("td", { text: `${seg.percent}%` }));
      tbody.append(row);
    }
  }
  table.append(tbody);
  wrap.append(table);
  return wrap;
}

/** outdatedHostsSection builds the outdated-host list plus the
 * "update all remotely-updatable" action. */
function outdatedHostsSection(versions, nowUnixSeconds, { signal, announce, onBatchCreated }) {
  const section = el("section", { class: "cp-section", attrs: { "aria-labelledby": "cp-rollout-outdated-heading" } });
  const head = el("div", { class: "cp-metric-head" });
  head.append(el("h2", { id: "cp-rollout-outdated-heading", text: t("rollout.outdated_title") }));

  const rows = outdatedHostRows(versions?.hosts || [], nowUnixSeconds);
  const eligibleIDs = remotelyUpdatableOutdatedHostIDs(rows.map((r) => ({ host_id: r.host_id, capability: r.capability })));

  const updateAllBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" }, text: t("rollout.update_all_button") });
  updateAllBtn.disabled = eligibleIDs.length === 0;
  updateAllBtn.addEventListener("click", () => openUpdateAllDialog(eligibleIDs, { signal, announce, onBatchCreated }));
  head.append(updateAllBtn);
  section.append(head);

  if (rows.length === 0) {
    section.append(emptyState({ title: t("rollout.outdated_empty"), message: "" }));
    return section;
  }

  const tableWrap = el("div", { class: "cp-table-wrap" });
  const table = el("table", { class: "cp-table" });
  const thead = el("thead");
  const headRow = el("tr");
  for (const label of [
    t("rollout.outdated_col_host"),
    t("rollout.outdated_col_os"),
    t("rollout.outdated_col_version"),
    t("rollout.outdated_col_behind_for"),
    t("rollout.outdated_col_capability"),
    t("rollout.outdated_col_command"),
  ]) {
    headRow.append(el("th", { text: label }));
  }
  thead.append(headRow);
  table.append(thead);

  const tbody = el("tbody");
  for (const row of rows) {
    tbody.append(outdatedHostRow(row));
  }
  table.append(tbody);
  tableWrap.append(table);
  section.append(tableWrap);
  return section;
}

function outdatedHostRow(row) {
  const tr = el("tr");
  tr.append(el("td", { text: row.hostname || row.host_id }));
  tr.append(el("td", { text: osLabel(row.os) }));
  tr.append(el("td", { text: row.version || t("rollout.os_unknown") }));
  tr.append(el("td", { text: behindForText(row.behindForSeconds) }));

  const capCell = el("td");
  const capClass = row.capability === "capable" ? "cp-chip-ok" : "cp-chip-other";
  capCell.append(el("span", { class: `cp-chip ${capClass}`, text: capabilityLabel(row.capability) }));
  tr.append(capCell);

  const cmdCell = el("td");
  const cmdRow = el("div", { class: "cp-updates-manual-command" });
  cmdRow.append(el("code", { class: "cp-code-inline", text: row.manual_command || "" }));
  const copyBtn = el("button", {
    class: "cp-btn cp-btn-secondary cp-btn-sm",
    attrs: { type: "button", "aria-label": `${t("rollout.copy_command")}: ${row.hostname || row.host_id}` },
  });
  copyBtn.append(icon("copy"));
  copyBtn.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(row.manual_command || "");
      showToast({ message: t("rollout.copy_command_success"), variant: "success" });
    } catch {
      showToast({ message: t("rollout.copy_command_failure"), variant: "error" });
    }
  });
  cmdRow.append(copyBtn);
  cmdCell.append(cmdRow);
  tr.append(cmdCell);
  return tr;
}

/** openUpdateAllDialog opens the confirm dialog for the "update all
 * remotely-updatable outdated hosts" action, with a parallelism
 * choice. */
function openUpdateAllDialog(eligibleIDs, { signal, announce, onBatchCreated }) {
  if (eligibleIDs.length === 0) {
    showToast({ message: t("rollout.update_all_none_eligible"), variant: "info" });
    return;
  }

  const { dialog, body, open, close } = createDialog({
    titleId: "cp-rollout-update-all-title",
    title: t("rollout.update_all_dialog_title"),
    description: t("rollout.update_all_dialog_body", { count: eligibleIDs.length }),
  });

  const { node: parallelNode, select: parallelSelect } = selectField({
    id: "cp-rollout-update-all-parallel",
    label: t("rollout.update_all_dialog_parallel_label"),
    options: parallelismOptionsLocalized(),
    value: "3",
  });
  body.append(parallelNode);

  const cancelBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: t("rollout.update_all_dialog_cancel") });
  cancelBtn.addEventListener("click", () => close());

  const confirmBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" }, text: t("rollout.update_all_dialog_confirm") });
  confirmBtn.addEventListener("click", async () => {
    confirmBtn.disabled = true;
    try {
      const maxParallel = Number(parallelSelect.value) || 3;
      const batch = await createUpdateBatch({ hostIDs: eligibleIDs, target: "latest", maxParallel }, signal);
      announce?.(t("rollout.update_all_started", { count: eligibleIDs.length }));
      close();
      onBatchCreated(batch.batch_id);
    } catch (err) {
      if (err?.name === "AbortError") return;
      showToast({ message: t("rollout.batch_create_failed", { error: describeError(err) }), variant: "error" });
    } finally {
      confirmBtn.disabled = false;
    }
  });

  body.append(buildDialogActions([cancelBtn, confirmBtn]));
  document.body.append(dialog);
  open();
}

function parallelismOptionsLocalized() {
  const keyByValue = { 1: "rollout.parallel_one_at_a_time", 3: "rollout.parallel_three_at_a_time", 5: "rollout.parallel_five_at_a_time", 10: "rollout.parallel_ten_at_a_time" };
  return parallelismOptions().map((o) => ({ value: String(o.value), label: t(keyByValue[o.value]) }));
}

/** batchHistorySection builds the batch-history table; selecting a
 * batch shows the existing progress panel. */
function batchHistorySection(batches, progressHost, { signal, announce, showBatch }) {
  const section = el("section", { class: "cp-section", attrs: { "aria-labelledby": "cp-rollout-batch-history-heading" } });
  section.append(el("h2", { id: "cp-rollout-batch-history-heading", text: t("rollout.batch_history_title") }));

  if (batches.length === 0) {
    section.append(emptyState({ title: t("rollout.batch_history_empty"), message: "" }));
    return section;
  }

  const tableWrap = el("div", { class: "cp-table-wrap" });
  const table = el("table", { class: "cp-table" });
  const thead = el("thead");
  const headRow = el("tr");
  for (const label of [
    t("rollout.batch_col_batch"),
    t("rollout.batch_col_target"),
    t("rollout.batch_col_status"),
    t("rollout.batch_col_succeeded"),
    t("rollout.batch_col_failed"),
    t("rollout.batch_col_pending"),
    t("rollout.batch_col_duration"),
    t("rollout.batch_col_failure_reasons"),
    "",
  ]) {
    headRow.append(el("th", { text: label }));
  }
  thead.append(headRow);
  table.append(thead);

  const tbody = el("tbody");
  for (const batch of batches) {
    tbody.append(batchRow(batch, { signal, announce, showBatch }));
  }
  table.append(tbody);
  tableWrap.append(table);
  section.append(tableWrap);
  return section;
}

function batchRow(batch, { showBatch }) {
  const tr = el("tr");
  tr.append(el("td", { text: batch.batch_id }));
  tr.append(el("td", { text: batch.target }));

  const statusCell = el("td");
  const statusClass = batch.pending > 0 ? "cp-chip-warning" : batch.failed > 0 ? "cp-chip-critical" : "cp-chip-ok";
  statusCell.append(el("span", { class: `cp-chip ${statusClass}`, text: batchStatusText(batch) }));
  tr.append(statusCell);

  tr.append(el("td", { text: String(batch.succeeded) }));
  tr.append(el("td", { text: String(batch.failed) }));
  tr.append(el("td", { text: String(batch.pending) }));
  tr.append(el("td", { text: batchDurationText(batch) }));
  tr.append(el("td", { text: failureReasonSummary(batch.failure_reasons) }));

  const actionCell = el("td");
  const viewBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" }, text: t("rollout.view_progress") });
  viewBtn.addEventListener("click", () => showBatch(batch.batch_id));
  actionCell.append(viewBtn);
  tr.append(actionCell);

  return tr;
}

/** agentsSection builds the per-host capability table with batch
 * creation controls, absorbed from the former pages/updates.js. */
function agentsSection({ hosts, signal, announce, onBatchCreated }) {
  const section = el("section", { class: "cp-section", attrs: { "aria-labelledby": "cp-rollout-agents-heading" } });
  section.append(el("h2", { id: "cp-rollout-agents-heading", text: t("rollout.agents_title") }));

  if (hosts.length === 0) {
    section.append(emptyState({ title: t("rollout.agents_empty_title"), message: t("rollout.agents_empty_message") }));
    return section;
  }

  const selected = new Set();

  const tableWrap = el("div", { class: "cp-table-wrap" });
  const table = el("table", { class: "cp-table" });
  const thead = el("thead");
  const headRow = el("tr");
  headRow.append(el("th", { text: t("rollout.col_select") }));
  for (const label of [t("rollout.col_host"), t("rollout.col_os"), t("rollout.col_version"), t("rollout.col_update_available"), t("rollout.col_remote_update")]) {
    headRow.append(el("th", { text: label }));
  }
  thead.append(headRow);
  table.append(thead);

  const tbody = el("tbody");
  const checkboxes = [];
  for (const h of hosts) {
    const cap = h.host.remote_update || {};
    const eligible = cap.supported && cap.opted_in;
    const row = el("tr");

    const checkboxCell = el("td");
    const checkbox = /** @type {HTMLInputElement} */ (el("input", { attrs: { type: "checkbox", "aria-label": t("rollout.select_host", { name: h.host.hostname }) } }));
    checkbox.disabled = !eligible;
    checkbox.addEventListener("change", () => {
      if (checkbox.checked) selected.add(h.host.id);
      else selected.delete(h.host.id);
      updateActionState();
    });
    checkboxes.push({ checkbox, host: h, eligible });
    checkboxCell.append(checkbox);
    row.append(checkboxCell);

    row.append(el("td", { text: h.host.hostname }));
    row.append(el("td", { text: osLabel(cap.platform || h.host.os) }));
    row.append(el("td", { text: h.host.agent_version || t("rollout.os_unknown") }));
    row.append(el("td", { text: h.update?.available ? `${t("rollout.summary_outdated")} (${h.update.latest})` : "\u2014" }));
    row.append(el("td", { children: [capabilityCell(cap, h)] }));

    tbody.append(row);
  }
  table.append(tbody);
  tableWrap.append(table);
  section.append(tableWrap);

  const actions = el("div", { class: "cp-updates-actions" });

  const { node: targetNode, select: targetSelect } = selectField({
    id: "cp-rollout-target",
    label: t("rollout.target_label"),
    options: [{ value: "latest", label: t("rollout.target_latest") }],
    value: "latest",
  });
  actions.append(targetNode);

  const { node: parallelNode, select: parallelSelect } = selectField({
    id: "cp-rollout-parallel",
    label: t("rollout.parallel_label"),
    options: parallelismOptionsLocalized(),
    value: "3",
  });
  actions.append(parallelNode);

  const updateSelectedBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" }, text: t("rollout.update_selected") });
  updateSelectedBtn.disabled = true;
  const updateOutdatedBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: t("rollout.update_all_outdated") });

  function updateActionState() {
    updateSelectedBtn.disabled = selected.size === 0;
  }

  async function submitBatch(hostIDs) {
    if (hostIDs.length === 0) return;
    const target = targetSelect.value || "latest";
    const maxParallel = Number(parallelSelect.value) || 3;
    updateSelectedBtn.disabled = true;
    updateOutdatedBtn.disabled = true;
    try {
      const batch = await createUpdateBatch({ hostIDs, target, maxParallel }, signal);
      announce?.(t("rollout.batch_created", { count: hostIDs.length }));
      onBatchCreated(batch.batch_id);
    } catch (err) {
      if (err?.name === "AbortError") return;
      showToast({ message: t("rollout.batch_create_failed", { error: describeError(err) }), variant: "error" });
    } finally {
      updateActionState();
      updateOutdatedBtn.disabled = false;
    }
  }

  updateSelectedBtn.addEventListener("click", () => submitBatch(Array.from(selected)));
  updateOutdatedBtn.addEventListener("click", () => {
    const outdatedEligible = checkboxes.filter((c) => c.eligible && c.host.update?.available).map((c) => c.host.host.id);
    submitBatch(outdatedEligible);
  });

  actions.append(updateSelectedBtn, updateOutdatedBtn);
  section.append(actions);
  return section;
}

function capabilityCell(cap, host) {
  const wrap = el("div", { class: "cp-updates-capability" });
  if (cap.supported && cap.opted_in) {
    wrap.append(el("span", { class: "cp-chip cp-chip-ok", text: t("rollout.ready_chip") }));
    return wrap;
  }
  const reasonText = REASON_KEYS[cap.reason] ? reasonLabel(cap.reason) : cap.opted_in ? t("rollout.reason_generic_not_supported") : t("rollout.reason_generic_opt_in_required");
  wrap.append(el("span", { class: "cp-chip cp-chip-other", text: t("rollout.not_available_chip"), attrs: { title: reasonText } }));

  const command = host?.update?.command || "sudo cloud-pulse-agent update";
  const commandRow = el("div", { class: "cp-updates-manual-command" });
  commandRow.append(el("code", { class: "cp-code-inline", text: command }));
  const copyBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button", "aria-label": `${t("rollout.copy_command")}: ${host.host.hostname}` } });
  copyBtn.append(icon("copy"));
  copyBtn.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(command);
      showToast({ message: t("rollout.copy_command_success"), variant: "success" });
    } catch {
      showToast({ message: t("rollout.copy_command_failure"), variant: "error" });
    }
  });
  commandRow.append(copyBtn);
  wrap.append(commandRow);
  return wrap;
}

/**
 * renderProgress fetches and renders a batch's job list, then invokes
 * onDone(hasPending) so the caller can schedule (or not) the next poll.
 * Absorbed unchanged (behaviorally) from the former pages/updates.js.
 */
async function renderProgress(host, batchID, signal, announce, onDone) {
  let resp;
  try {
    resp = await getUpdateJobs(batchID, signal);
  } catch (err) {
    if (err?.name === "AbortError") return;
    clearChildren(host);
    host.append(errorBanner(describeError(err)));
    onDone(false);
    return;
  }
  const jobs = resp?.jobs || [];
  clearChildren(host);
  if (jobs.length === 0) {
    onDone(false);
    return;
  }

  const section = el("section", { class: "cp-section", attrs: { "aria-labelledby": "cp-rollout-progress-heading" } });
  const head = el("div", { class: "cp-metric-head" });
  head.append(el("h2", { id: "cp-rollout-progress-heading", text: t("rollout.progress_title", { batch_id: batchID }) }));

  const counts = { queued: 0, in_progress: 0, succeeded: 0, failed: 0 };
  for (const j of jobs) counts[j.state] = (counts[j.state] || 0) + 1;
  head.append(
    el("span", {
      class: "cp-muted-small",
      text: t("rollout.progress_summary", { succeeded: counts.succeeded, failed: counts.failed, in_progress: counts.in_progress, queued: counts.queued }),
    }),
  );

  const cancelBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" }, text: t("rollout.cancel_queued") });
  cancelBtn.addEventListener("click", async () => {
    cancelBtn.disabled = true;
    try {
      const result = await cancelUpdateBatch(batchID, signal);
      announce?.(t("rollout.canceled_jobs", { count: result.canceled }));
      await renderProgress(host, batchID, signal, announce, onDone);
    } catch (err) {
      if (err?.name === "AbortError") return;
      showToast({ message: t("rollout.cancel_failed", { error: describeError(err) }), variant: "error" });
    } finally {
      cancelBtn.disabled = false;
    }
  });
  head.append(cancelBtn);
  section.append(head);

  const list = el("ul", { class: "cp-updates-job-list", attrs: { role: "list" } });
  for (const job of jobs) {
    list.append(jobRow(job, signal, announce, () => renderProgress(host, batchID, signal, announce, onDone)));
  }
  section.append(list);
  host.append(section);

  const hasPending = jobs.some((j) => !isTerminalJobState(j.state));
  onDone(hasPending);
}

/** jobRow builds one host's job row in the progress panel. */
function jobRow(job, signal, announce, onRetried) {
  const row = el("li", { class: "cp-updates-job-row" });
  row.append(el("span", { class: `cp-chip ${JOB_STATE_CLASSES[job.state] || "cp-chip-other"}`, text: JOB_STATE_KEYS[job.state] ? t(JOB_STATE_KEYS[job.state]) : job.state }));
  row.append(el("span", { class: "cp-updates-job-host", text: job.host_id }));
  row.append(el("span", { class: "cp-muted-small", text: t("rollout.updated_relative", { time: formatRelativeTimeFromUnixSeconds(job.updated_at) }) }));

  if (job.state === "failed") {
    const reasonText = job.reason ? reasonLabel(job.reason) : job.error || t("rollout.reason_unknown");
    row.append(el("span", { class: "cp-updates-job-reason", text: reasonText }));

    if (job.reason !== "not_enabled" && job.reason !== "unsupported") {
      const retryBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" }, text: t("rollout.retry") });
      retryBtn.addEventListener("click", async () => {
        retryBtn.disabled = true;
        try {
          await retryUpdateJob(job.id, signal);
          announce?.(t("rollout.retrying", { host_id: job.host_id }));
          await onRetried();
        } catch (err) {
          if (err?.name === "AbortError") return;
          showToast({ message: t("rollout.retry_failed", { error: describeError(err) }), variant: "error" });
        } finally {
          retryBtn.disabled = false;
        }
      });
      row.append(retryBtn);
    }
  }

  return row;
}

function describeError(err) {
  if (err instanceof ApiError) return err.message;
  return String(err?.message || err);
}
