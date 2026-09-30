// settings/notifications.js — Notifications section (SPEC-v0.5 §D):
// notify channel list (type icon, name, enabled toggle, last test
// result) plus an "Add channel" dialog with a platform picker, a
// step-by-step setup guide panel per platform (exact UI paths, copy
// buttons), inline field validation, and "Send test" with the delivery
// result shown before saving. Replaces the v0.4.x single alert-webhook
// editor (the old PUT /api/v1/settings/alerts endpoint keeps working
// server-side, mapped onto a "Default webhook" channel, but this page
// now manages channels directly).
import { el, clearChildren, emptyState, errorBanner } from "../../ui/components.js";
import { icon } from "../../ui/icons.js";
import { createDialog, buildDialogActions } from "../../ui/dialog.js";
import { selectField } from "../../ui/select.js";
import { showToast } from "../../ui/toast.js";
import {
  getNotifyChannels,
  createNotifyChannel,
  updateNotifyChannel,
  deleteNotifyChannel,
  testNotifyChannel,
  testDraftNotifyChannel,
  ApiError,
} from "../../core/api.js";
import { CHANNEL_TYPES, channelTypeByValue, validateChannelConfig, defaultChannelName } from "../../core/notify-guides.js";
import { diagnosisViewModel, diagnosisSummaryText } from "../../core/diagnosis-view.js";
import {
  checklistByValue,
  loadChecklistProgress,
  saveChecklistProgress,
  checklistCompletionCount,
} from "../../core/notify-checklists.js";

const CHANNEL_TYPE_ICONS = { discord: "zap", telegram: "send", whatsapp: "smartphone", webhook: "webhook" };

/**
 * mountNotificationsSection renders the Notifications section into
 * container.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @returns {{refresh: () => Promise<void>, teardown: () => void}}
 */
export function mountNotificationsSection(container, { announce }) {
  const controller = new AbortController();

  async function refresh() {
    clearChildren(container);

    let channels;
    try {
      channels = await getNotifyChannels(controller.signal);
    } catch (err) {
      if (err?.name === "AbortError") return;
      container.append(errorBanner(describeError(err)));
      return;
    }

    container.append(channelsCard({ channels, signal: controller.signal, announce, onChanged: refresh }));
  }

  function teardown() {
    controller.abort();
  }

  return { refresh, teardown };
}

function channelsCard({ channels, signal, announce, onChanged }) {
  const card = el("div", { class: "cp-card" });
  const head = el("div", { class: "cp-metric-head" });
  head.append(el("h2", { class: "cp-card-title", text: "Notification channels" }));
  const addBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" } });
  addBtn.append(icon("plus"), el("span", { text: "Add channel" }));
  addBtn.addEventListener("click", () => openChannelDialog({ signal, announce, onChanged }));
  head.append(addBtn);
  card.append(head);
  card.append(
    el("p", {
      class: "cp-muted-small",
      text: "Discord, Telegram, WhatsApp, or a generic webhook — alert rules deliver to any channel listed here.",
    }),
  );

  if (channels.length === 0) {
    card.append(
      emptyState({
        title: "No notification channels yet",
        message: "Add a Discord, Telegram, WhatsApp, or webhook channel to start receiving alerts.",
        iconName: "bell",
      }),
    );
    return card;
  }

  const list = el("ul", { class: "cp-channel-list", attrs: { role: "list" } });
  for (const ch of channels) {
    list.append(channelRow({ channel: ch, signal, announce, onChanged }));
  }
  card.append(list);
  return card;
}

function channelRow({ channel, signal, announce, onChanged }) {
  const row = el("li", { class: "cp-channel-row" });
  const typeDef = channelTypeByValue(channel.type);
  row.append(icon(CHANNEL_TYPE_ICONS[channel.type] || "bell"));

  const info = el("div", { class: "cp-channel-row-info" });
  info.append(el("span", { class: "cp-channel-row-name", text: channel.name }));
  info.append(el("span", { class: "cp-muted-small", text: typeDef?.label || channel.type }));
  row.append(info);

  row.append(el("span", { class: `cp-chip ${channel.enabled ? "cp-chip-ok" : "cp-chip-other"}`, text: channel.enabled ? "Enabled" : "Disabled" }));

  const status = el("span", { class: "cp-muted-small cp-channel-row-status", attrs: { role: "status", "aria-live": "polite" } });

  const testBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" } });
  testBtn.append(icon("send"), el("span", { text: "Send test" }));
  testBtn.addEventListener("click", async () => {
    testBtn.disabled = true;
    status.textContent = "Sending…";
    status.title = "";
    try {
      const result = await testNotifyChannel(channel.id, signal);
      status.textContent = result.ok ? "Test delivered." : `Test failed: ${diagnosisSummaryText(result.diagnosis)}`;
      status.title = result.ok ? "" : result.diagnosis?.detail || "";
      if (result.ok) recordChecklistTestSuccess(channel.type, channel.id);
      announce?.(status.textContent);
    } catch (err) {
      if (err?.name === "AbortError") return;
      status.textContent = `Test failed: ${describeError(err)}`;
    } finally {
      testBtn.disabled = false;
    }
  });

  const editBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm cp-btn-icon", attrs: { type: "button", "aria-label": `Edit ${channel.name}` } });
  editBtn.append(icon("pencil"));
  editBtn.addEventListener("click", () => openChannelDialog({ channel, signal, announce, onChanged }));

  const deleteBtn = el("button", { class: "cp-btn cp-btn-danger cp-btn-sm cp-btn-icon", attrs: { type: "button", "aria-label": `Delete ${channel.name}` } });
  deleteBtn.append(icon("trash"));
  deleteBtn.addEventListener("click", async () => {
    if (!globalThis.confirm(`Delete channel "${channel.name}"? Rules referencing it will simply skip it.`)) return;
    try {
      await deleteNotifyChannel(channel.id, signal);
      announce?.(`Deleted channel ${channel.name}.`);
      onChanged();
    } catch (err) {
      if (err?.name === "AbortError") return;
      showToast({ message: `Delete failed: ${describeError(err)}`, variant: "error" });
    }
  });

  const actions = el("div", { class: "cp-channel-row-actions" });
  actions.append(testBtn, editBtn, deleteBtn);
  row.append(actions, status);
  return row;
}

/**
 * openChannelDialog opens the Add/Edit channel dialog: a platform
 * picker (add mode only — the type can't change on edit), a
 * step-by-step guide panel that updates live as the platform changes,
 * the platform's Config fields, and a "Send test"/"Save" action pair.
 * @param {Object} opts
 * @param {Object} [opts.channel] existing models.NotifyChannel (edit
 *   mode) — omitted for "Add channel".
 * @param {AbortSignal} opts.signal
 * @param {(text: string) => void} [opts.announce]
 * @param {() => void} opts.onChanged called after a successful save/delete
 */
function openChannelDialog({ channel, signal, announce, onChanged }) {
  const isEdit = Boolean(channel);
  const { dialog, body, open, close } = createDialog({
    titleId: "cp-channel-dialog-title",
    title: isEdit ? `Edit channel: ${channel.name}` : "Add notification channel",
    wide: true,
  });

  const draft = {
    type: channel?.type || "discord",
    name: channel?.name || "",
    enabled: channel?.enabled ?? true,
    config: { ...(channel?.config || {}) },
  };

  const layout = el("div", { class: "cp-channel-dialog-layout" });
  const formCol = el("div", { class: "cp-channel-dialog-form" });
  const guideCol = el("div", { class: "cp-channel-dialog-guide" });
  layout.append(formCol, guideCol);
  body.append(layout);

  // --- Platform picker (add mode only) ---
  if (!isEdit) {
    const { node: typeNode, select: typeSelect } = selectField({
      id: "cp-channel-type",
      label: "Platform",
      options: CHANNEL_TYPES.map((t) => ({ value: t.value, label: t.label })),
      value: draft.type,
    });
    typeSelect.addEventListener("change", () => {
      draft.type = typeSelect.value;
      draft.config = {};
      if (!draft.name || CHANNEL_TYPES.some((t) => t.label === draft.name)) {
        draft.name = defaultChannelName(draft.type);
        nameInput.value = draft.name;
      }
      renderFields();
      renderGuide();
    });
    formCol.append(typeNode);
  }

  // --- Name + enabled ---
  const nameField = el("div", { class: "cp-field" });
  nameField.append(el("label", { class: "cp-label", attrs: { for: "cp-channel-name" }, text: "Name" }));
  const nameInput = /** @type {HTMLInputElement} */ (
    el("input", { class: "cp-input", attrs: { id: "cp-channel-name", type: "text", value: draft.name || defaultChannelName(draft.type) } })
  );
  nameInput.addEventListener("input", () => {
    draft.name = nameInput.value;
  });
  nameField.append(nameInput);
  formCol.append(nameField);
  if (!draft.name) draft.name = defaultChannelName(draft.type);

  const enabledField = el("label", { class: "cp-checkbox-field" });
  const enabledInput = /** @type {HTMLInputElement} */ (el("input", { attrs: { type: "checkbox" } }));
  enabledInput.checked = draft.enabled;
  enabledInput.addEventListener("change", () => {
    draft.enabled = enabledInput.checked;
  });
  enabledField.append(enabledInput, el("span", { text: "Enabled" }));
  formCol.append(enabledField);

  const fieldsHost = el("div", { class: "cp-channel-dialog-fields" });
  formCol.append(fieldsHost);

  const errorsHost = el("div");
  formCol.append(errorsHost);

  /** fieldInputs maps a Config field key to its rendered <input>, so a
   * failed Send test's diagnosis can highlight (aria-invalid + focus)
   * the specific field most likely at fault (SPEC-v0.6 §4). Rebuilt on
   * every renderFields() call (e.g. after switching platform). */
  let fieldInputs = {};

  function renderFields() {
    clearChildren(fieldsHost);
    fieldInputs = {};
    const def = channelTypeByValue(draft.type);
    if (!def) return;
    for (const field of def.fields) {
      const { wrap, input } = configFieldInput(field, draft, isEdit);
      if (input) fieldInputs[field.key] = input;
      fieldsHost.append(wrap);
    }
  }
  renderFields();

  function renderGuide() {
    clearChildren(guideCol);
    const def = channelTypeByValue(draft.type);
    guideCol.append(el("h3", { class: "cp-channel-guide-title", text: `Setting up ${def?.label || draft.type}` }));
    const list = el("ol", { class: "cp-channel-guide-list" });
    for (const step of def?.steps || []) {
      list.append(el("li", { text: step }));
    }
    guideCol.append(list);
    guideCol.append(checklistPanel(draft.type, isEdit ? channel.id : "draft"));
  }
  renderGuide();

  const testResultHost = el("div", { class: "cp-channel-dialog-test-result" });
  formCol.append(testResultHost);

  const testBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" } });
  testBtn.append(icon("send"), el("span", { text: "Send test" }));
  testBtn.addEventListener("click", async () => {
    const errors = validateChannelConfig(draft.type, draft.config);
    clearChildren(errorsHost);
    if (Object.keys(errors).length > 0) {
      renderErrors(errorsHost, errors);
      return;
    }
    testBtn.disabled = true;
    clearChildren(testResultHost);
    clearFieldHighlights(fieldInputs);
    testResultHost.append(el("p", { class: "cp-muted-small", text: "Sending test notification…" }));
    try {
      const result = isEdit && !hasUnsavedSecretChanges(channel, draft)
        ? await testNotifyChannel(channel.id, signal)
        : await testDraftNotifyChannel({ name: draft.name, type: draft.type, enabled: true, config: draft.config }, signal);
      clearChildren(testResultHost);
      testResultHost.append(testResultBanner(result, draft.type, fieldInputs));
      if (result.ok) {
        recordChecklistTestSuccess(draft.type, isEdit ? channel.id : "draft");
        renderGuide();
      }
      announce?.(result.ok ? "Test notification delivered." : "Test notification failed.");
    } catch (err) {
      clearChildren(testResultHost);
      testResultHost.append(errorBanner(`Test failed: ${describeError(err)}`));
    } finally {
      testBtn.disabled = false;
    }
  });
  formCol.append(testBtn);

  const cancelBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: "Cancel" });
  cancelBtn.addEventListener("click", () => close());

  const saveBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" }, text: isEdit ? "Save changes" : "Add channel" });
  saveBtn.addEventListener("click", async () => {
    const errors = validateChannelConfig(draft.type, draft.config);
    clearChildren(errorsHost);
    if (!draft.name || draft.name.trim() === "") errors.name = "Name is required.";
    if (Object.keys(errors).length > 0) {
      renderErrors(errorsHost, errors);
      return;
    }
    saveBtn.disabled = true;
    try {
      const payload = { name: draft.name, type: draft.type, enabled: draft.enabled, config: draft.config };
      if (isEdit) {
        await updateNotifyChannel(channel.id, payload, signal);
        announce?.(`Saved channel ${draft.name}.`);
      } else {
        await createNotifyChannel(payload, signal);
        announce?.(`Added channel ${draft.name}.`);
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

/**
 * hasUnsavedSecretChanges reports whether draft's Config differs from
 * the saved channel's in a way that requires testing the draft
 * (unsaved) config rather than the already-saved channel — i.e. any
 * non-"***"-placeholder value changed. Used so "Send test" on an
 * edited-but-unsaved secret field tests what's actually in the form,
 * not the stale saved value.
 */
function hasUnsavedSecretChanges(channel, draft) {
  for (const [k, v] of Object.entries(draft.config)) {
    if (v !== "***" && v !== (channel.config || {})[k]) return true;
  }
  return false;
}

/**
 * configFieldInput builds one Config field's <label>+<input> pair.
 * @returns {{wrap: HTMLElement, input: HTMLInputElement|null}} input is
 *   null for a checkbox field (checkbox fields aren't diagnosis-
 *   highlight targets in practice, and the caller's fieldInputs map
 *   only needs text/password fields to support aria-invalid + focus).
 */
function configFieldInput(field, draft, isEdit) {
  const wrap = el("div", { class: "cp-field" });
  const inputID = `cp-channel-field-${field.key}`;
  wrap.append(el("label", { class: "cp-label", attrs: { for: inputID }, text: field.label }));

  if (field.type === "checkbox") {
    const checkboxWrap = el("label", { class: "cp-checkbox-field" });
    const checkbox = /** @type {HTMLInputElement} */ (el("input", { attrs: { type: "checkbox", id: inputID } }));
    checkbox.checked = draft.config[field.key] === "true" || draft.config[field.key] === "1";
    checkbox.addEventListener("change", () => {
      draft.config[field.key] = checkbox.checked ? "true" : "false";
    });
    checkboxWrap.append(checkbox, el("span", { text: field.label }));
    clearChildren(wrap);
    wrap.append(checkboxWrap);
    return { wrap, input: null };
  }

  const existingValue = draft.config[field.key] || "";
  const isSecretPreserved = isEdit && field.secret && existingValue === "***";
  const input = /** @type {HTMLInputElement} */ (
    el("input", {
      class: "cp-input",
      attrs: {
        id: inputID,
        type: field.type === "password" ? "password" : "text",
        placeholder: isSecretPreserved ? "Unchanged (hidden)" : field.placeholder,
        value: isSecretPreserved ? "" : existingValue,
      },
    })
  );
  input.addEventListener("input", () => {
    draft.config[field.key] = input.value;
    input.removeAttribute("aria-invalid");
  });
  wrap.append(input);
  if (isSecretPreserved) {
    wrap.append(el("p", { class: "cp-muted-small", text: "Leave blank to keep the currently stored value." }));
  }
  return { wrap, input };
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

/**
 * checklistPanel builds the "Real account verification checklist"
 * shown below a platform's setup guide (SPEC-v0.6 §4): a checkbox per
 * item, persisted to localStorage per (type, id), plus the last
 * successful Send test time if one is recorded. Returns an empty,
 * childless element for a channel type with no checklist (e.g.
 * "webhook").
 * @param {string} type
 * @param {string|number} id "draft" for an unsaved channel
 * @returns {HTMLElement}
 */
function checklistPanel(type, id) {
  const wrap = el("div", { class: "cp-channel-checklist" });
  const checklist = checklistByValue(type);
  if (!checklist) return wrap;

  let progress;
  try {
    progress = loadChecklistProgress(globalThis.localStorage, type, id);
  } catch {
    progress = { items: {}, lastTestSuccessAt: null };
  }

  const { checked, total } = checklistCompletionCount(type, progress);
  wrap.append(el("h4", { class: "cp-channel-checklist-title", text: `Real account verification checklist (${checked}/${total})` }));

  const list = el("ul", { class: "cp-channel-checklist-list", attrs: { role: "list" } });
  for (const item of checklist.items) {
    const li = el("li", { class: "cp-checkbox-field" });
    const checkbox = /** @type {HTMLInputElement} */ (el("input", { attrs: { type: "checkbox", id: `cp-checklist-${type}-${id}-${item.id}` } }));
    checkbox.checked = Boolean(progress.items[item.id]);
    checkbox.addEventListener("change", () => {
      progress.items[item.id] = checkbox.checked;
      persistChecklist(type, id, progress);
      const counts = checklistCompletionCount(type, progress);
      titleEl.textContent = `Real account verification checklist (${counts.checked}/${counts.total})`;
    });
    li.append(checkbox, el("label", { attrs: { for: checkbox.id }, text: item.text }));
    list.append(li);
  }
  wrap.append(list);

  if (progress.lastTestSuccessAt) {
    wrap.append(
      el("p", {
        class: "cp-muted-small",
        text: `Last successful Send test: ${new Date(progress.lastTestSuccessAt).toLocaleString()}.`,
      }),
    );
  }

  const titleEl = wrap.firstChild;
  return wrap;
}

/**
 * persistChecklist best-effort saves progress to localStorage,
 * silently no-opping if storage is unavailable (private mode,
 * disabled storage) — matching this app's other localStorage usages.
 */
function persistChecklist(type, id, progress) {
  try {
    saveChecklistProgress(globalThis.localStorage, type, id, progress);
  } catch {
    // No persistence available for this session.
  }
}

/**
 * recordChecklistTestSuccess stamps the checklist's
 * lastTestSuccessAt to now and persists it, called whenever a Send
 * test for (type, id) succeeds.
 */
function recordChecklistTestSuccess(type, id) {
  if (!checklistByValue(type)) return;
  let progress;
  try {
    progress = loadChecklistProgress(globalThis.localStorage, type, id);
  } catch {
    return;
  }
  progress.lastTestSuccessAt = Date.now();
  persistChecklist(type, id, progress);
}

function apiErrorDetails(err) {
  if (err instanceof ApiError && err.details) return err.details;
  return { form: describeError(err) };
}

function testResultBanner(result, platform, fieldInputs) {
  const wrap = el("div", { class: `cp-alerts-test-result ${result.ok ? "cp-alerts-test-ok" : "cp-alerts-test-fail"}` });
  if (result.ok) {
    wrap.append(icon("circleCheck"));
    wrap.append(el("span", { text: result.delivery || "Delivered successfully." }));
    return wrap;
  }
  wrap.append(diagnosisCard(result.diagnosis, platform, fieldInputs));
  return wrap;
}

/**
 * diagnosisCard builds the red diagnosis card shown on a failed Send
 * test (SPEC-v0.6 §4): title, detail, a hint on how to fix it, and an
 * optional docs link — plus, when the diagnosis code maps to a
 * specific Config field for this platform, marks that field
 * `aria-invalid="true"` and focuses it so both sighted and
 * screen-reader users are pointed at the input most likely at fault.
 * The view-model decision (which code maps to which field) lives in
 * the pure core/diagnosis-view.js helper, covered by node tests.
 * @param {{code?: string, title?: string, detail?: string, hint?: string, docs_url?: string}|null|undefined} diagnosis
 * @param {string} platform "discord"|"telegram"|"whatsapp"|"webhook"
 * @param {Record<string, HTMLInputElement>} fieldInputs
 * @returns {HTMLElement}
 */
function diagnosisCard(diagnosis, platform, fieldInputs) {
  const vm = diagnosisViewModel(platform, diagnosis);

  const card = el("div", { class: "cp-diagnosis-card", attrs: { role: "alert" } });
  const head = el("div", { class: "cp-diagnosis-card-head" });
  head.append(icon("circleX"));
  head.append(el("strong", { text: vm.title }));
  card.append(head);
  if (vm.detail) card.append(el("p", { text: vm.detail }));
  if (vm.hint) card.append(el("p", { class: "cp-muted-small", text: vm.hint }));
  if (vm.docsUrl) {
    card.append(
      el("a", {
        class: "cp-link",
        attrs: { href: vm.docsUrl, target: "_blank", rel: "noopener noreferrer" },
        text: "Documentation",
      }),
    );
  }

  const target = vm.fieldToHighlight && fieldInputs ? fieldInputs[vm.fieldToHighlight] : null;
  if (target) {
    target.setAttribute("aria-invalid", "true");
    target.focus();
  }
  return card;
}

/**
 * clearFieldHighlights removes any aria-invalid marking left over from
 * a previous failed Send test, so a fresh attempt starts from a clean
 * slate (e.g. after switching platform, or before showing a new
 * result).
 * @param {Record<string, HTMLInputElement>} fieldInputs
 */
function clearFieldHighlights(fieldInputs) {
  for (const input of Object.values(fieldInputs || {})) {
    input?.removeAttribute?.("aria-invalid");
  }
}

function describeError(err) {
  if (err instanceof ApiError) return err.message;
  return String(err?.message || err);
}
