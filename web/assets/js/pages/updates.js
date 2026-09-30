// pages/updates.js — #/updates page (SPEC-v0.6 §2): agent list with
// remote-update capability/opt-in status, multi-select batch update
// creation, and a live progress panel for in-flight update jobs.
import { el, clearChildren, emptyState, errorBanner } from "../ui/components.js";
import { icon } from "../ui/icons.js";
import { showToast } from "../ui/toast.js";
import { selectField } from "../ui/select.js";
import { formatRelativeTimeFromUnixSeconds } from "../core/format.js";
import { getHosts, getUpdateJobs, createUpdateBatch, retryUpdateJob, cancelUpdateBatch, ApiError } from "../core/api.js";

/** PROGRESS_POLL_MS is the auto-refresh interval for the progress
 * panel while a batch has any non-terminal job (SPEC-v0.6 §2: "5초
 * 간격 자동 갱신"). */
export const PROGRESS_POLL_MS = 5000;

/** JOB_STATE_LABELS/CLASSES map a models.UpdateJobState to display
 * text and a chip color class. */
const JOB_STATE_LABELS = { queued: "Queued", in_progress: "In progress", succeeded: "Succeeded", failed: "Failed" };
const JOB_STATE_CLASSES = { queued: "cp-chip-other", in_progress: "cp-chip-warning", succeeded: "cp-chip-ok", failed: "cp-chip-critical" };

/** PLATFORM_LABELS maps a models.AgentOS ("linux"|"darwin"|"windows")
 * to its display name for the Updates page's OS column (SPEC-v0.7 §1);
 * an empty/unrecognized platform (older agent that predates reporting
 * it) shows as "Unknown". */
const PLATFORM_LABELS = { linux: "Linux", darwin: "macOS", windows: "Windows" };

/**
 * platformLabel renders a models.RemoteUpdateCapability.Platform value
 * ("linux"|"darwin"|"windows"|"") for display, falling back to
 * "Unknown" for an empty/unrecognized value (an older agent build that
 * predates reporting its OS family at all).
 * @param {string} platform
 * @returns {string}
 */
export function platformLabel(platform) {
  return PLATFORM_LABELS[platform] || "Unknown";
}

/** REASON_LABELS maps a models.UpdateJobReason to a short human
 * message shown next to a failed job. */
const REASON_LABELS = {
  not_enabled: "Not opted in on this agent (CP_REMOTE_UPDATE=off).",
  unsupported: "Not supported (needs Linux + systemd + agent v0.6.0+).",
  timeout: "Timed out waiting for the agent to report back.",
  download_failed: "Failed to download the release asset.",
  checksum_mismatch: "Downloaded asset checksum mismatch.",
  verify_failed: "New binary failed its own -version check.",
  restart_failed: "Binary replaced but the service failed to restart.",
  downgrade_refused: "Refused: target version is not newer than current.",
  unknown: "Unknown error.",
};

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
 * manualUpdateCommand returns the ready-to-run manual update command
 * for a host that can't (or shouldn't yet) use remote update — the
 * hub already computes this OS-aware per SPEC-v0.7 §1 (Linux/macOS:
 * `sudo cloud-pulse-agent update`; Windows: no `sudo`, "run as
 * Administrator" instead; a pre-v0.3.0 agent on any OS: the legacy
 * installer one-liner) and returns it as `host.update.command` — this
 * helper just falls back to a generic hint when that field is absent
 * (a host that has never reported at all, so `update` itself is null).
 * @param {{update?: {command?: string}|null}} host a GET /api/v1/hosts
 *   entry ({host, update, ...})
 * @returns {string}
 */
export function manualUpdateCommand(host) {
  const command = host?.update?.command;
  if (command) return command;
  return "sudo cloud-pulse-agent update";
}

/**
 * mountUpdatesPage renders the Updates page into container.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @returns {{refresh: () => Promise<void>, teardown: () => void}}
 */
export function mountUpdatesPage(container, { announce }) {
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

    const root = el("div", { class: "cp-updates-page" });
    root.append(el("h1", { class: "cp-page-title", text: "Updates" }));
    root.append(
      el("p", {
        class: "cp-page-subtitle",
        text: "Remotely update opted-in agents to the latest (or a chosen) release.",
      }),
    );

    let hostsResp;
    try {
      hostsResp = await getHosts(controller.signal);
    } catch (err) {
      if (err?.name === "AbortError") return;
      root.append(errorBanner(describeError(err)));
      container.append(root);
      return;
    }
    const hosts = hostsResp?.hosts || [];

    const progressHost = el("div", { class: "cp-updates-progress-host" });

    root.append(agentTable({ hosts, signal: controller.signal, announce, onBatchCreated: (batchID) => showBatch(batchID) }));
    root.append(progressHost);
    container.append(root);

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

    if (activeBatchID) {
      await showBatch(activeBatchID);
    } else {
      // No batch created yet this page load — still show the most
      // recent jobs (if any) so navigating away and back doesn't lose
      // visibility into an in-flight batch.
      try {
        const all = await getUpdateJobs(undefined, controller.signal);
        const jobs = all?.jobs || [];
        if (jobs.length > 0) {
          const mostRecentBatch = jobs.reduce((a, b) => (a.created_at > b.created_at ? a : b)).batch_id;
          await showBatch(mostRecentBatch);
        }
      } catch (err) {
        if (err?.name !== "AbortError") {
          // Non-fatal: the page still works without a progress panel.
        }
      }
    }
  }

  function teardown() {
    if (pollTimer) clearTimeout(pollTimer);
    controller.abort();
  }

  return { refresh, teardown };
}

/**
 * agentTable builds the host list with capability badges, checkboxes,
 * and the "Update selected" / "Update all outdated" action row.
 */
function agentTable({ hosts, signal, announce, onBatchCreated }) {
  const section = el("section", { class: "cp-section", attrs: { "aria-labelledby": "cp-updates-agents-heading" } });
  section.append(el("h2", { id: "cp-updates-agents-heading", text: "Agents" }));

  if (hosts.length === 0) {
    section.append(emptyState({ title: "No hosts yet", message: "Agents will appear here once they report to the hub." }));
    return section;
  }

  /** @type {Set<string>} */
  const selected = new Set();

  const table = el("table", { class: "cp-table" });
  const thead = el("thead");
  const headRow = el("tr");
  headRow.append(el("th", { text: "" }));
  for (const label of ["Host", "OS", "Version", "Update available", "Remote update"]) {
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
    const checkbox = /** @type {HTMLInputElement} */ (el("input", { attrs: { type: "checkbox", "aria-label": `Select ${h.host.hostname}` } }));
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
    row.append(el("td", { text: platformLabel(cap.platform || h.host.os) }));
    row.append(el("td", { text: h.host.agent_version || "unknown" }));
    row.append(el("td", { text: h.update?.available ? `Yes (${h.update.latest})` : "No" }));
    row.append(el("td", { children: [capabilityCell(cap, h)] }));

    tbody.append(row);
  }
  table.append(tbody);
  const tableWrap = el("div", { class: "cp-table-wrap" });
  tableWrap.append(table);
  section.append(tableWrap);

  const actions = el("div", { class: "cp-updates-actions" });

  const { node: targetNode, select: targetSelect } = selectField({
    id: "cp-updates-target",
    label: "Target",
    options: [
      { value: "latest", label: "Latest release" },
    ],
    value: "latest",
  });
  actions.append(targetNode);

  const { node: parallelNode, select: parallelSelect } = selectField({
    id: "cp-updates-parallel",
    label: "Parallel",
    options: [
      { value: "1", label: "1 at a time" },
      { value: "3", label: "3 at a time (default)" },
      { value: "5", label: "5 at a time" },
      { value: "10", label: "10 at a time" },
    ],
    value: "3",
  });
  actions.append(parallelNode);

  const updateSelectedBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" }, text: "Update selected" });
  updateSelectedBtn.disabled = true;
  const updateOutdatedBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: "Update all outdated" });

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
      announce?.(`Created update batch for ${hostIDs.length} host(s).`);
      onBatchCreated(batch.batch_id);
    } catch (err) {
      if (err?.name === "AbortError") return;
      showToast({ message: `Failed to create update batch: ${describeError(err)}`, variant: "error" });
    } finally {
      updateActionState();
      updateOutdatedBtn.disabled = false;
    }
  }

  updateSelectedBtn.addEventListener("click", () => submitBatch(Array.from(selected)));
  updateOutdatedBtn.addEventListener("click", () => {
    const outdatedEligible = checkboxes
      .filter((c) => c.eligible && c.host.update?.available)
      .map((c) => c.host.host.id);
    submitBatch(outdatedEligible);
  });

  actions.append(updateSelectedBtn, updateOutdatedBtn);
  section.append(actions);
  return section;
}

/**
 * capabilityCell builds the "remote update" column's contents for one
 * host: a green "Ready" chip when supported+opted-in, otherwise an
 * explanatory chip plus (SPEC-v0.7 §1) that host's own OS-aware manual
 * update command as a fallback — copy button included, since an
 * ineligible host is still updatable by hand.
 * @param {Object} cap models.RemoteUpdateCapability
 * @param {Object} host a GET /api/v1/hosts entry ({host, update, ...})
 * @returns {HTMLElement}
 */
function capabilityCell(cap, host) {
  const wrap = el("div", { class: "cp-updates-capability" });
  if (cap.supported && cap.opted_in) {
    wrap.append(el("span", { class: "cp-chip cp-chip-ok", text: "Ready" }));
    return wrap;
  }
  const reasonText = REASON_LABELS[cap.reason] || (cap.opted_in ? "Not supported." : "Opt-in required (--remote-update).");
  wrap.append(el("span", { class: "cp-chip cp-chip-other", text: "Not available", attrs: { title: reasonText } }));

  const command = manualUpdateCommand(host);
  const commandRow = el("div", { class: "cp-updates-manual-command" });
  commandRow.append(el("code", { class: "cp-code-inline", text: command }));
  const copyBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button", "aria-label": `Copy manual update command for ${host.host.hostname}` } });
  copyBtn.append(icon("copy"));
  copyBtn.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(command);
      showToast({ message: "Command copied.", variant: "success" });
    } catch {
      showToast({ message: "Copy failed — select and copy manually.", variant: "error" });
    }
  });
  commandRow.append(copyBtn);
  wrap.append(commandRow);
  return wrap;
}

/**
 * renderProgress fetches and renders a batch's job list, then invokes
 * onDone(hasPending) so the caller can schedule (or not) the next poll.
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

  const section = el("section", { class: "cp-section", attrs: { "aria-labelledby": "cp-updates-progress-heading" } });
  const head = el("div", { class: "cp-metric-head" });
  head.append(el("h2", { id: "cp-updates-progress-heading", text: `Batch progress · ${batchID}` }));

  const counts = { queued: 0, in_progress: 0, succeeded: 0, failed: 0 };
  for (const j of jobs) counts[j.state] = (counts[j.state] || 0) + 1;
  head.append(
    el("span", {
      class: "cp-muted-small",
      text: `${counts.succeeded} succeeded · ${counts.failed} failed · ${counts.in_progress} in progress · ${counts.queued} queued`,
    }),
  );

  const cancelBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" }, text: "Cancel queued" });
  cancelBtn.addEventListener("click", async () => {
    cancelBtn.disabled = true;
    try {
      const result = await cancelUpdateBatch(batchID, signal);
      announce?.(`Canceled ${result.canceled} queued job(s).`);
      await renderProgress(host, batchID, signal, announce, onDone);
    } catch (err) {
      if (err?.name === "AbortError") return;
      showToast({ message: `Cancel failed: ${describeError(err)}`, variant: "error" });
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
  row.append(el("span", { class: `cp-chip ${JOB_STATE_CLASSES[job.state] || "cp-chip-other"}`, text: JOB_STATE_LABELS[job.state] || job.state }));
  row.append(el("span", { class: "cp-updates-job-host", text: job.host_id }));
  row.append(el("span", { class: "cp-muted-small", text: `Updated ${formatRelativeTimeFromUnixSeconds(job.updated_at)}` }));

  if (job.state === "failed") {
    const reasonText = REASON_LABELS[job.reason] || job.error || "Failed.";
    row.append(el("span", { class: "cp-updates-job-reason", text: reasonText }));

    if (job.reason !== "not_enabled" && job.reason !== "unsupported") {
      const retryBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" }, text: "Retry" });
      retryBtn.addEventListener("click", async () => {
        retryBtn.disabled = true;
        try {
          await retryUpdateJob(job.id, signal);
          announce?.(`Retrying update for ${job.host_id}.`);
          await onRetried();
        } catch (err) {
          if (err?.name === "AbortError") return;
          showToast({ message: `Retry failed: ${describeError(err)}`, variant: "error" });
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
