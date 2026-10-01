// settings/notifications.js — Notifications section (SPEC-v0.5 §D):
// notify channel list (type icon, name, enabled toggle, last test
// result) plus an "Add channel" dialog with a platform picker, a
// step-by-step setup guide panel per platform (exact UI paths, copy
// buttons), inline field validation, and "Send test" with the delivery
// result shown before saving. Replaces the v0.4.x single alert-webhook
// editor (the old PUT /api/v1/settings/alerts endpoint keeps working
// server-side, mapped onto a "Default webhook" channel, but this page
// now manages channels directly).
//
// SPEC-v0.8 §3-2 added a "Health" sub-tab (see
// pages/settings/notifications-health.js) alongside this section's own
// channel list/dialog ("Channels" sub-tab) — both share one
// mountNotificationsSection entry point, switching via an in-page tab
// row rather than a second route/nav entry, per the prep contract's
// "pick one and document it" note.
import { el, clearChildren, emptyState, errorBanner } from "../../ui/components.js";
import { icon } from "../../ui/icons.js";
import { createDialog, buildDialogActions } from "../../ui/dialog.js";
import { selectField } from "../../ui/select.js";
import { showToast } from "../../ui/toast.js";
import { t } from "../../core/i18n.js";
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
import { mountChannelHealthTab } from "./notifications-health.js";

const CHANNEL_TYPE_ICONS = { discord: "zap", telegram: "send", whatsapp: "smartphone", webhook: "webhook" };

/** SUB_TABS lists this section's own in-page tab row: "Channels" (the
 * list/dialog this file has always owned) and "Health" (SPEC-v0.8
 * §3-2's report, delegated to notifications-health.js). */
const SUB_TABS = [
  { id: "channels", labelKey: "settings.notifications.tab_channels" },
  { id: "health", labelKey: "settings.notifications.tab_health" },
];

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
  let activeTab = "channels";
  let activeHandle = null;

  async function refresh() {
    clearChildren(container);

    const tabRow = el("div", { class: "cp-tabs", attrs: { role: "tablist" } });
    for (const tab of SUB_TABS) {
      const btn = el("button", {
        class: `cp-tab${tab.id === activeTab ? " cp-tab-active" : ""}`,
        attrs: { type: "button", role: "tab", "aria-selected": String(tab.id === activeTab) },
        text: t(tab.labelKey),
      });
      btn.addEventListener("click", () => {
        if (tab.id === activeTab) return;
        activeTab = tab.id;
        refresh();
      });
      tabRow.append(btn);
    }
    container.append(tabRow);

    const tabContent = el("div", { class: "cp-tab-content" });
    container.append(tabContent);

    if (activeTab === "health") {
      activeHandle = mountChannelHealthTab(tabContent, { announce });
      await activeHandle.refresh();
      return;
    }

    activeHandle = null;
    let channels;
    try {
      channels = await getNotifyChannels(controller.signal);
    } catch (err) {
      if (err?.name === "AbortError") return;
      tabContent.append(errorBanner(describeError(err)));
      return;
    }

    tabContent.append(channelsCard({ channels, signal: controller.signal, announce, onChanged: refresh }));
  }

  function teardown() {
    controller.abort();
    activeHandle?.teardown?.();
  }

  return { refresh, teardown };
}

function channelsCard({ channels, signal, announce, onChanged }) {
  const card = el("div", { class: "cp-card" });
  const head = el("div", { class: "cp-metric-head" });
  head.append(el("h2", { class: "cp-card-title", text: t("settings.notifications.section_title") }));
  const addBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" } });
  addBtn.append(icon("plus"), el("span", { text: t("settings.notifications.add_channel") }));
  addBtn.addEventListener("click", () => openChannelDialog({ signal, announce, onChanged }));
  head.append(addBtn);
  card.append(head);
  card.append(
    el("p", {
      class: "cp-muted-small",
      text: t("settings.notifications.section_description"),
    }),
  );

  if (channels.length === 0) {
    card.append(
      emptyState({
        title: t("settings.notifications.empty_title"),
        message: t("settings.notifications.empty_message"),
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

  row.append(
    el("span", {
      class: `cp-chip ${channel.enabled ? "cp-chip-ok" : "cp-chip-other"}`,
      text: channel.enabled ? t("settings.notifications.enabled") : t("settings.notifications.disabled"),
    }),
  );

  const status = el("span", { class: "cp-muted-small cp-channel-row-status", attrs: { role: "status", "aria-live": "polite" } });

  const testBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" } });
  testBtn.append(icon("send"), el("span", { text: t("settings.notifications.send_test") }));
  testBtn.addEventListener("click", async () => {
    testBtn.disabled = true;
    status.textContent = t("settings.notifications.sending");
    status.title = "";
    try {
      const result = await testNotifyChannel(channel.id, signal);
      status.textContent = result.ok
        ? t("settings.notifications.test_delivered")
        : t("settings.notifications.test_failed", { summary: diagnosisSummaryText(result.diagnosis) });
      status.title = result.ok ? "" : result.diagnosis?.detail || "";
      if (result.ok) recordChecklistTestSuccess(channel.type, channel.id);
      announce?.(status.textContent);
    } catch (err) {
      if (err?.name === "AbortError") return;
      status.textContent = t("settings.notifications.test_failed", { summary: describeError(err) });
    } finally {
      testBtn.disabled = false;
    }
  });

  const editBtn = el("button", {
    class: "cp-btn cp-btn-secondary cp-btn-sm cp-btn-icon",
    attrs: { type: "button", "aria-label": t("settings.notifications.edit_channel_aria", { name: channel.name }) },
  });
  editBtn.append(icon("pencil"));
  editBtn.addEventListener("click", () => openChannelDialog({ channel, signal, announce, onChanged }));

  const deleteBtn = el("button", {
    class: "cp-btn cp-btn-danger cp-btn-sm cp-btn-icon",
    attrs: { type: "button", "aria-label": t("settings.notifications.delete_channel_aria", { name: channel.name }) },
  });
  deleteBtn.append(icon("trash"));
  deleteBtn.addEventListener("click", async () => {
    if (!globalThis.confirm(t("settings.notifications.delete_confirm", { name: channel.name }))) return;
    try {
      await deleteNotifyChannel(channel.id, signal);
      announce?.(t("settings.notifications.saved_channel", { name: channel.name }));
      onChanged();
    } catch (err) {
      if (err?.name === "AbortError") return;
      showToast({ message: t("settings.notifications.delete_failed", { error: describeError(err) }), variant: "error" });
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
    title: isEdit
      ? t("settings.notifications.dialog_title_edit", { name: channel.name })
      : t("settings.notifications.dialog_title_add"),
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
      label: t("settings.notifications.platform_label"),
      options: CHANNEL_TYPES.map((tDef) => ({ value: tDef.value, label: tDef.label })),
      value: draft.type,
    });
    typeSelect.addEventListener("change", () => {
      draft.type = typeSelect.value;
      draft.config = {};
      if (!draft.name || CHANNEL_TYPES.some((tDef) => tDef.label === draft.name)) {
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
  nameField.append(el("label", { class: "cp-label", attrs: { for: "cp-channel-name" }, text: t("settings.notifications.name_label") }));
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
  enabledField.append(enabledInput, el("span", { text: t("settings.notifications.enabled") }));
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
    guideCol.append(
      el("h3", { class: "cp-channel-guide-title", text: t("settings.notifications.setup_guide_title", { platform: def?.label || draft.type }) }),
    );
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
  testBtn.append(icon("send"), el("span", { text: t("settings.notifications.send_test") }));
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
    testResultHost.append(el("p", { class: "cp-muted-small", text: t("settings.notifications.sending_test") }));
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
      announce?.(result.ok ? t("settings.notifications.test_notification_delivered") : t("settings.notifications.test_notification_failed"));
    } catch (err) {
      clearChildren(testResultHost);
      testResultHost.append(errorBanner(t("settings.notifications.test_failed", { summary: describeError(err) })));
    } finally {
      testBtn.disabled = false;
    }
  });
  formCol.append(testBtn);

  const cancelBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: t("settings.notifications.cancel") });
  cancelBtn.addEventListener("click", () => close());

  const saveBtn = el("button", {
    class: "cp-btn cp-btn-primary",
    attrs: { type: "button" },
    text: isEdit ? t("settings.notifications.save_changes") : t("settings.notifications.add_channel_submit"),
  });
  saveBtn.addEventListener("click", async () => {
    const errors = validateChannelConfig(draft.type, draft.config);
    clearChildren(errorsHost);
    if (!draft.name || draft.name.trim() === "") errors.name = t("settings.notifications.unnamed_field_error");
    if (Object.keys(errors).length > 0) {
      renderErrors(errorsHost, errors);
      return;
    }
    saveBtn.disabled = true;
    try {
      const payload = { name: draft.name, type: draft.type, enabled: draft.enabled, config: draft.config };
      if (isEdit) {
        await updateNotifyChannel(channel.id, payload, signal);
        announce?.(t("settings.notifications.saved_channel", { name: draft.name }));
      } else {
        await createNotifyChannel(payload, signal);
        announce?.(t("settings.notifications.added_channel", { name: draft.name }));
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
        placeholder: isSecretPreserved ? t("settings.notifications.secret_field_unchanged_placeholder") : field.placeholder,
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
    wrap.append(el("p", { class: "cp-muted-small", text: t("settings.notifications.secret_field_hint") }));
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
  const titleEl = el("h4", { class: "cp-channel-checklist-title", text: t("settings.notifications.checklist_title", { checked, total }) });
  wrap.append(titleEl);

  const list = el("ul", { class: "cp-channel-checklist-list", attrs: { role: "list" } });
  for (const item of checklist.items) {
    const li = el("li", { class: "cp-checkbox-field" });
    const checkbox = /** @type {HTMLInputElement} */ (el("input", { attrs: { type: "checkbox", id: `cp-checklist-${type}-${id}-${item.id}` } }));
    checkbox.checked = Boolean(progress.items[item.id]);
    checkbox.addEventListener("change", () => {
      progress.items[item.id] = checkbox.checked;
      persistChecklist(type, id, progress);
      const counts = checklistCompletionCount(type, progress);
      titleEl.textContent = t("settings.notifications.checklist_title", { checked: counts.checked, total: counts.total });
    });
    li.append(checkbox, el("label", { attrs: { for: checkbox.id }, text: item.text }));
    list.append(li);
  }
  wrap.append(list);

  if (progress.lastTestSuccessAt) {
    wrap.append(
      el("p", {
        class: "cp-muted-small",
        text: t("settings.notifications.checklist_last_test", { when: new Date(progress.lastTestSuccessAt).toLocaleString() }),
      }),
    );
  }

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
    wrap.append(el("span", { text: result.delivery || t("settings.notifications.test_notification_delivered") }));
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
        text: t("settings.notifications.documentation_link"),
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
