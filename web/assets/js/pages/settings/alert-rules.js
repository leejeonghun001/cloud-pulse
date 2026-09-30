// settings/alert-rules.js — Alert rules section (SPEC-v0.5 §D): rule
// list with enabled toggle + human summary sentence, and an editor
// dialog with one-click presets, metric/host/operator/threshold/
// duration/cooldown/notify-resolved/channels fields, and a live
// "would fire now" preview per host (POST .../{id}/preview — only
// available once the rule has been saved, since the hub's preview
// endpoint operates on a stored rule id).
import { el, clearChildren, emptyState, errorBanner, statusDot } from "../../ui/components.js";
import { icon } from "../../ui/icons.js";
import { createDialog, buildDialogActions } from "../../ui/dialog.js";
import { selectField } from "../../ui/select.js";
import { showToast } from "../../ui/toast.js";
import {
  getAlertRules,
  createAlertRule,
  updateAlertRule,
  deleteAlertRule,
  previewAlertRule,
  getNotifyChannels,
  getHosts,
  getStorageAccounts,
  ApiError,
} from "../../core/api.js";
import {
  METRICS,
  OPERATORS,
  PRESETS,
  DEFAULT_COOLDOWN_SEC,
  ruleSummarySentence,
  validateRuleForm,
  durationSecFromParts,
  bestDurationParts,
  isStorageScopedMetric,
} from "../../core/alerts.js";

const DURATION_UNITS = [
  { value: "seconds", label: "seconds" },
  { value: "minutes", label: "minutes" },
  { value: "hours", label: "hours" },
];

/**
 * mountAlertRulesSection renders the Alert rules section into
 * container.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @returns {{refresh: () => Promise<void>, teardown: () => void}}
 */
export function mountAlertRulesSection(container, { announce }) {
  const controller = new AbortController();

  async function refresh() {
    clearChildren(container);

    let rules, channels, hostsResp, storageAccountsResp;
    try {
      [rules, channels, hostsResp, storageAccountsResp] = await Promise.all([
        getAlertRules(controller.signal),
        getNotifyChannels(controller.signal),
        getHosts(controller.signal),
        getStorageAccounts(controller.signal).catch(() => ({ accounts: [] })),
      ]);
    } catch (err) {
      if (err?.name === "AbortError") return;
      container.append(errorBanner(describeError(err)));
      return;
    }

    const hosts = (hostsResp.hosts || []).map((h) => ({ id: h.host.id, hostname: h.host.hostname }));
    const storageAccounts = (storageAccountsResp.accounts || []).map((a) => ({ id: String(a.id), name: a.name }));
    container.append(rulesCard({ rules, channels, hosts, storageAccounts, signal: controller.signal, announce, onChanged: refresh }));
  }

  function teardown() {
    controller.abort();
  }

  return { refresh, teardown };
}

function rulesCard({ rules, channels, hosts, storageAccounts, signal, announce, onChanged }) {
  const card = el("div", { class: "cp-card" });
  const head = el("div", { class: "cp-metric-head" });
  head.append(el("h2", { class: "cp-card-title", text: "Alert rules" }));
  const addBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" } });
  addBtn.append(icon("plus"), el("span", { text: "Add rule" }));
  addBtn.addEventListener("click", () => openRuleDialog({ channels, hosts, storageAccounts, signal, announce, onChanged }));
  head.append(addBtn);
  card.append(head);

  if (rules.length === 0) {
    card.append(
      emptyState({
        title: "No alert rules yet",
        message: "Add a rule (or start from a preset) to be notified when a host crosses a threshold.",
        iconName: "triangleAlert",
      }),
    );
    return card;
  }

  const hostnameByID = (id) => hosts.find((h) => h.id === id)?.hostname;
  const channelNameByID = (id) => channels.find((c) => c.id === id)?.name;
  const storageAccountNameByID = (id) => storageAccounts.find((a) => a.id === id)?.name;

  const list = el("ul", { class: "cp-rule-list", attrs: { role: "list" } });
  for (const rule of rules) {
    list.append(ruleRow({ rule, channels, hosts, storageAccounts, hostnameByID, channelNameByID, storageAccountNameByID, signal, announce, onChanged }));
  }
  card.append(list);
  return card;
}

function ruleRow({ rule, channels, hosts, storageAccounts, hostnameByID, channelNameByID, storageAccountNameByID, signal, announce, onChanged }) {
  const row = el("li", { class: "cp-rule-row" });
  const top = el("div", { class: "cp-rule-row-top" });
  top.append(statusDot(rule.enabled ? "up" : "down"));
  top.append(el("span", { class: "cp-rule-row-name", text: rule.name }));
  top.append(el("span", { class: `cp-chip ${rule.enabled ? "cp-chip-ok" : "cp-chip-other"}`, text: rule.enabled ? "Enabled" : "Disabled" }));
  row.append(top);

  row.append(el("p", { class: "cp-muted-small", text: ruleSummarySentence(rule, { hostnameByID, channelNameByID, storageAccountNameByID }) }));

  const actions = el("div", { class: "cp-rule-row-actions" });
  const editBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" } });
  editBtn.append(icon("pencil"), el("span", { text: "Edit" }));
  editBtn.addEventListener("click", () => openRuleDialog({ rule, channels, hosts, storageAccounts, signal, announce, onChanged }));

  const deleteBtn = el("button", { class: "cp-btn cp-btn-danger cp-btn-sm", attrs: { type: "button" } });
  deleteBtn.append(icon("trash"), el("span", { text: "Delete" }));
  deleteBtn.addEventListener("click", async () => {
    if (!globalThis.confirm(`Delete rule "${rule.name}"?`)) return;
    try {
      await deleteAlertRule(rule.id, signal);
      announce?.(`Deleted rule ${rule.name}.`);
      onChanged();
    } catch (err) {
      if (err?.name === "AbortError") return;
      showToast({ message: `Delete failed: ${describeError(err)}`, variant: "error" });
    }
  });

  actions.append(editBtn, deleteBtn);
  row.append(actions);
  return row;
}

/**
 * openRuleDialog opens the Add/Edit rule dialog.
 * @param {Object} opts
 * @param {Object} [opts.rule] existing models.AlertRule (edit mode)
 * @param {Object[]} opts.channels
 * @param {Object[]} opts.hosts
 * @param {Object[]} [opts.storageAccounts] {id, name} pairs for the
 *   storage_usage_pct metric's account-scoped selector
 * @param {AbortSignal} opts.signal
 * @param {(text: string) => void} [opts.announce]
 * @param {() => void} opts.onChanged
 */
function openRuleDialog({ rule, channels, hosts, storageAccounts = [], signal, announce, onChanged }) {
  const isEdit = Boolean(rule);
  const { dialog, body, open, close } = createDialog({
    titleId: "cp-rule-dialog-title",
    title: isEdit ? `Edit rule: ${rule.name}` : "Add alert rule",
    wide: true,
  });

  const initialDuration = bestDurationParts(rule?.duration_sec ?? 0);
  const initialCooldown = bestDurationParts(rule?.cooldown_sec ?? DEFAULT_COOLDOWN_SEC);

  const draft = {
    name: rule?.name || "",
    enabled: rule?.enabled ?? true,
    metric: rule?.metric || "cpu",
    hostID: rule?.host_id || "",
    operator: rule?.operator || ">",
    threshold: rule?.threshold ?? 90,
    durationValue: initialDuration.value,
    durationUnit: initialDuration.unit,
    cooldownValue: initialCooldown.value,
    cooldownUnit: initialCooldown.unit,
    notifyResolved: rule?.notify_resolved ?? false,
    channelIDs: rule?.channel_ids ? [...rule.channel_ids] : [],
  };

  const hostnameByID = (id) => hosts.find((h) => h.id === id)?.hostname;
  const storageAccountNameByID = (id) => storageAccounts.find((a) => a.id === id)?.name;
  const channelNameByID = (id) => channels.find((c) => c.id === id)?.name;

  // --- Presets (add mode only) ---
  if (!isEdit) {
    const presetRow = el("div", { class: "cp-rule-preset-row" });
    presetRow.append(el("span", { class: "cp-muted-small", text: "Presets:" }));
    for (const preset of PRESETS) {
      const btn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" }, text: preset.label });
      btn.addEventListener("click", () => {
        const parts = bestDurationParts(preset.rule.duration_sec);
        Object.assign(draft, {
          name: preset.rule.name,
          metric: preset.rule.metric,
          hostID: preset.rule.host_id,
          operator: preset.rule.operator,
          threshold: preset.rule.threshold,
          durationValue: parts.value,
          durationUnit: parts.unit,
        });
        rerenderForm();
      });
      presetRow.append(btn);
    }
    body.append(presetRow);
  }

  const formHost = el("div", { class: "cp-rule-dialog-form" });
  const summaryHost = el("div", { class: "cp-rule-dialog-summary" });
  const previewHost = el("div", { class: "cp-rule-dialog-preview" });
  const errorsHost = el("div");
  body.append(formHost, summaryHost, errorsHost);
  if (isEdit) body.append(previewHost);

  function updateSummary() {
    clearChildren(summaryHost);
    const rulePreview = {
      metric: draft.metric,
      host_id: draft.hostID,
      operator: draft.operator,
      threshold: draft.threshold,
      duration_sec: durationSecFromParts(draft.durationValue, draft.durationUnit),
      channel_ids: draft.channelIDs,
    };
    summaryHost.append(
      el("p", { class: "cp-rule-summary-sentence", text: ruleSummarySentence(rulePreview, { hostnameByID, channelNameByID, storageAccountNameByID }) }),
    );
  }

  function rerenderForm() {
    clearChildren(formHost);
    formHost.append(buildRuleForm(draft, hosts, storageAccounts, channels, updateSummary, rerenderForm));
    updateSummary();
  }
  rerenderForm();

  if (isEdit) {
    const previewBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" } });
    previewBtn.append(icon("zap"), el("span", { text: "Preview: would this fire now?" }));
    previewBtn.addEventListener("click", async () => {
      previewBtn.disabled = true;
      clearChildren(previewHost);
      previewHost.append(el("p", { class: "cp-muted-small", text: "Checking current values…" }));
      try {
        const result = await previewAlertRule(rule.id, signal);
        clearChildren(previewHost);
        previewHost.append(renderPreviewResult(result, hostnameByID));
      } catch (err) {
        clearChildren(previewHost);
        previewHost.append(errorBanner(`Preview failed: ${describeError(err)}`));
      } finally {
        previewBtn.disabled = false;
      }
    });
    body.append(previewBtn);
  }

  const cancelBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: "Cancel" });
  cancelBtn.addEventListener("click", () => close());

  const saveBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" }, text: isEdit ? "Save changes" : "Add rule" });
  saveBtn.addEventListener("click", async () => {
    const durationSec = durationSecFromParts(draft.durationValue, draft.durationUnit);
    const cooldownSec = durationSecFromParts(draft.cooldownValue, draft.cooldownUnit);
    const errors = validateRuleForm({
      name: draft.name,
      metric: draft.metric,
      operator: draft.operator,
      threshold: draft.threshold,
      durationSec,
      cooldownSec,
    });
    clearChildren(errorsHost);
    if (Object.keys(errors).length > 0) {
      renderErrors(errorsHost, errors);
      return;
    }

    const payload = {
      name: draft.name,
      enabled: draft.enabled,
      metric: draft.metric,
      host_id: draft.hostID,
      operator: draft.operator,
      threshold: draft.metric === "host_down" ? 0 : draft.threshold,
      duration_sec: durationSec,
      cooldown_sec: cooldownSec,
      notify_resolved: draft.notifyResolved,
      channel_ids: draft.channelIDs,
    };

    saveBtn.disabled = true;
    try {
      if (isEdit) {
        await updateAlertRule(rule.id, payload, signal);
        announce?.(`Saved rule ${draft.name}.`);
      } else {
        await createAlertRule(payload, signal);
        announce?.(`Added rule ${draft.name}.`);
      }
      close();
      onChanged();
    } catch (err) {
      if (err?.name === "AbortError") return;
      renderErrors(errorsHost, apiErrorDetails(err));
    } finally {
      saveBtn.disabled = false;
    }
  });

  body.append(buildDialogActions([cancelBtn, saveBtn]));
  document.body.append(dialog);
  open();
}

function buildRuleForm(draft, hosts, storageAccounts, channels, onChange, onFullChange = onChange) {
  const frag = document.createDocumentFragment();

  const nameField = el("div", { class: "cp-field" });
  nameField.append(el("label", { class: "cp-label", attrs: { for: "cp-rule-name" }, text: "Name" }));
  const nameInput = /** @type {HTMLInputElement} */ (
    el("input", { class: "cp-input", attrs: { id: "cp-rule-name", type: "text", value: draft.name } })
  );
  nameInput.addEventListener("input", () => {
    draft.name = nameInput.value;
    onChange();
  });
  nameField.append(nameInput);
  frag.append(nameField);

  const enabledField = el("label", { class: "cp-checkbox-field" });
  const enabledInput = /** @type {HTMLInputElement} */ (el("input", { attrs: { type: "checkbox" } }));
  enabledInput.checked = draft.enabled;
  enabledInput.addEventListener("change", () => {
    draft.enabled = enabledInput.checked;
  });
  enabledField.append(enabledInput, el("span", { text: "Enabled" }));
  frag.append(enabledField);

  const row1 = el("div", { class: "cp-rule-form-row" });
  const { node: metricNode, select: metricSelect } = selectField({
    id: "cp-rule-metric",
    label: "Metric",
    options: METRICS.map((m) => ({ value: m.value, label: m.label })),
    value: draft.metric,
  });
  metricSelect.addEventListener("change", () => {
    draft.metric = metricSelect.value;
    draft.hostID = "";
    onFullChange();
  });
  row1.append(metricNode);

  const scopeIsStorage = isStorageScopedMetric(draft.metric);
  const hostOptions = scopeIsStorage
    ? [{ value: "", label: "All connected storage accounts" }, ...storageAccounts.map((a) => ({ value: a.id, label: a.name }))]
    : [{ value: "", label: "All hosts" }, ...hosts.map((h) => ({ value: h.id, label: h.hostname }))];
  const { node: hostNode, select: hostSelect } = selectField({
    id: "cp-rule-host",
    label: "Scope",
    options: hostOptions,
    value: draft.hostID,
  });
  hostSelect.addEventListener("change", () => {
    draft.hostID = hostSelect.value;
    onChange();
  });
  row1.append(hostNode);
  frag.append(row1);

  const row2 = el("div", { class: "cp-rule-form-row" });
  const { node: opNode, select: opSelect } = selectField({
    id: "cp-rule-operator",
    label: "Operator",
    options: OPERATORS.map((o) => ({ value: o.value, label: o.label })),
    value: draft.operator,
  });
  opSelect.addEventListener("change", () => {
    draft.operator = opSelect.value;
    onChange();
  });
  row2.append(opNode);

  if (draft.metric !== "host_down") {
    const thresholdField = el("div", { class: "cp-field" });
    thresholdField.append(el("label", { class: "cp-label", attrs: { for: "cp-rule-threshold" }, text: "Threshold" }));
    const thresholdInput = /** @type {HTMLInputElement} */ (
      el("input", { class: "cp-input", attrs: { id: "cp-rule-threshold", type: "number", step: "0.1", min: "0", value: String(draft.threshold) } })
    );
    thresholdInput.addEventListener("input", () => {
      draft.threshold = Number(thresholdInput.value);
      onChange();
    });
    thresholdField.append(thresholdInput);
    row2.append(thresholdField);
  }
  frag.append(row2);

  frag.append(durationField("cp-rule-duration", "Duration (sustained for)", draft, "durationValue", "durationUnit", onChange));
  frag.append(durationField("cp-rule-cooldown", "Cooldown (re-notify while still firing)", draft, "cooldownValue", "cooldownUnit", onChange));

  const notifyResolvedField = el("label", { class: "cp-checkbox-field" });
  const notifyResolvedInput = /** @type {HTMLInputElement} */ (el("input", { attrs: { type: "checkbox" } }));
  notifyResolvedInput.checked = draft.notifyResolved;
  notifyResolvedInput.addEventListener("change", () => {
    draft.notifyResolved = notifyResolvedInput.checked;
  });
  notifyResolvedField.append(notifyResolvedInput, el("span", { text: "Also notify when resolved" }));
  frag.append(notifyResolvedField);

  frag.append(channelsMultiSelect(draft, channels, onChange));

  return frag;
}

function durationField(idPrefix, label, draft, valueKey, unitKey, onChange) {
  const wrap = el("div", { class: "cp-field cp-rule-duration-field" });
  wrap.append(el("label", { class: "cp-label", attrs: { for: `${idPrefix}-value` }, text: label }));
  const row = el("div", { class: "cp-rule-duration-row" });
  const valueInput = /** @type {HTMLInputElement} */ (
    el("input", { class: "cp-input", attrs: { id: `${idPrefix}-value`, type: "number", min: "0", step: "1", value: String(draft[valueKey]) } })
  );
  valueInput.addEventListener("input", () => {
    draft[valueKey] = Number(valueInput.value);
    onChange();
  });
  row.append(valueInput);
  const { node: unitNode, select: unitSelect } = selectField({
    id: `${idPrefix}-unit`,
    ariaLabel: `${label} unit`,
    options: DURATION_UNITS,
    value: draft[unitKey],
  });
  unitSelect.addEventListener("change", () => {
    draft[unitKey] = unitSelect.value;
    onChange();
  });
  row.append(unitNode);
  wrap.append(row);
  return wrap;
}

function channelsMultiSelect(draft, channels, onChange) {
  const wrap = el("div", { class: "cp-field" });
  wrap.append(el("span", { class: "cp-label", text: "Channels" }));
  if (channels.length === 0) {
    wrap.append(el("p", { class: "cp-muted-small", text: "No channels configured yet — add one in Notifications first." }));
    return wrap;
  }
  const list = el("div", { class: "cp-rule-channels-list" });
  for (const ch of channels) {
    const label = el("label", { class: "cp-checkbox-field" });
    const checkbox = /** @type {HTMLInputElement} */ (el("input", { attrs: { type: "checkbox" } }));
    checkbox.checked = draft.channelIDs.includes(ch.id);
    checkbox.addEventListener("change", () => {
      if (checkbox.checked) {
        if (!draft.channelIDs.includes(ch.id)) draft.channelIDs.push(ch.id);
      } else {
        draft.channelIDs = draft.channelIDs.filter((id) => id !== ch.id);
      }
      onChange();
    });
    label.append(checkbox, el("span", { text: ch.name }));
    list.append(label);
  }
  wrap.append(list);
  return wrap;
}

function renderPreviewResult(result, hostnameByID) {
  const wrap = el("div", { class: "cp-rule-preview-result" });
  const entries = Object.entries(result || {});
  if (entries.length === 0) {
    wrap.append(el("p", { class: "cp-muted-small", text: "No hosts in scope." }));
    return wrap;
  }
  const list = el("ul", { class: "cp-rule-preview-list", attrs: { role: "list" } });
  for (const [hostID, wouldFire] of entries) {
    const item = el("li", { class: "cp-rule-preview-item" });
    item.append(
      el("span", { class: `cp-chip ${wouldFire ? "cp-chip-critical" : "cp-chip-ok"}`, text: wouldFire ? "Would fire" : "OK" }),
      el("span", { text: hostnameByID(hostID) || hostID }),
    );
    list.append(item);
  }
  wrap.append(list);
  return wrap;
}

function renderErrors(host, errors) {
  clearChildren(host);
  const entries = Object.entries(errors || {});
  if (entries.length === 0) return;
  const banner = el("div", { class: "cp-error-banner", attrs: { role: "alert" } });
  const list = el("ul");
  for (const [, message] of entries) {
    list.append(el("li", { text: message }));
  }
  banner.append(list);
  host.append(banner);
}

function apiErrorDetails(err) {
  if (err instanceof ApiError && err.details) return err.details;
  return { form: describeError(err) };
}

function describeError(err) {
  if (err instanceof ApiError) return err.message;
  return String(err?.message || err);
}
