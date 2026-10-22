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
import { disclosure } from "../../ui/disclosure.js";
import { createDialog, buildDialogActions } from "../../ui/dialog.js";
import { selectField } from "../../ui/select.js";
import { showToast } from "../../ui/toast.js";
import { t, formatDateTimeLocalized } from "../../core/i18n.js";
import {
  getNotifyChannels,
  getChannelsHealth,
  createNotifyChannel,
  updateNotifyChannel,
  deleteNotifyChannel,
  testNotifyChannel,
  testDraftNotifyChannel,
  ApiError,
} from "../../core/api.js";
import { CHANNEL_TYPES, channelTypeByValue, validateChannelConfig, defaultChannelName } from "../../core/notify-guides.js";
import { diagnosisViewModel } from "../../core/diagnosis-view.js";
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
  let activeTab = tabFromHash(globalThis.location?.hash || "");
  let activeHandle = null;
  let renderController = null;
  let generation = 0;

  async function refresh() {
    if (controller.signal.aborted) return;
    const current = ++generation;
    renderController?.abort();
    activeHandle?.teardown?.();
    activeHandle = null;
    renderController = new AbortController();
    const signal = renderController.signal;
    clearChildren(container);

    const tabRow = el("div", { class: "cp-tabs cp-notify-tabs", attrs: { role: "tablist", "aria-label": t("settings.notifications.section_title") } });
    for (const tab of SUB_TABS) {
      const btn = el("button", {
        class: `cp-tab${tab.id === activeTab ? " cp-tab-active" : ""}`,
        attrs: { id: `cp-notify-tab-${tab.id}`, type: "button", role: "tab", tabindex: tab.id === activeTab ? "0" : "-1", "aria-controls": "cp-notify-panel", "aria-selected": String(tab.id === activeTab) },
        text: t(tab.labelKey),
      });
      btn.addEventListener("click", () => {
        if (tab.id === activeTab) return;
        activeTab = tab.id;
        // Keep the tab in the URL (bookmarkable, and lets ui-audit open
        // it) without re-dispatching the router.
        globalThis.history?.replaceState(null, "", hashForTab(globalThis.location?.hash || "", tab.id));
        const pending = refresh();
        document.getElementById(`cp-notify-tab-${tab.id}`)?.focus();
        return pending;
      });
      btn.addEventListener("keydown", (event) => {
        const keys = ["ArrowLeft", "ArrowRight", "Home", "End"];
        if (!keys.includes(event.key)) return;
        event.preventDefault();
        const next = event.key === "Home" ? "channels" : event.key === "End" ? "health" : activeTab === "channels" ? "health" : "channels";
        return document.getElementById(`cp-notify-tab-${next}`)?.click();
      });
      tabRow.append(btn);
    }
    container.append(tabRow);

    const tabContent = el("div", { class: "cp-tab-content", attrs: { id: "cp-notify-panel", role: "tabpanel", "aria-labelledby": `cp-notify-tab-${activeTab}` } });
    container.append(tabContent);

    if (activeTab === "health") {
      activeHandle = mountChannelHealthTab(tabContent, { announce });
      await activeHandle.refresh();
      return;
    }

    activeHandle = null;
    let channels, health;
    try {
      [channels, health] = await Promise.all([
        getNotifyChannels(signal),
        getChannelsHealth(signal).catch(() => null),
      ]);
    } catch (err) {
      if (err?.name === "AbortError") return;
      if (current === generation && !signal.aborted) tabContent.append(errorBanner(describeError(err)));
      return;
    }

    if (current !== generation || signal.aborted) return;
    tabContent.append(channelsCard({ channels, health, signal, announce, onChanged: refresh }));
  }

  function teardown() {
    controller.abort();
    generation++;
    renderController?.abort();
    activeHandle?.teardown?.();
  }

  return { refresh, teardown };
}

function channelsCard({ channels, health, signal, announce, onChanged }) {
  const card = el("div", { class: "cp-card cp-notify-card" });
  const head = el("div", { class: "cp-notify-header" });
  const titleGroup = el("div", { class: "cp-notify-title-group" });
  titleGroup.append(el("h2", { class: "cp-card-title", text: t("settings.notifications.section_title") }));
  titleGroup.append(el("p", { class: "cp-muted-small", text: t("settings.notifications.section_description") }));
  head.append(titleGroup);
  const addBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" } });
  addBtn.append(icon("plus"), el("span", { text: t("settings.notifications.add_channel") }));
  addBtn.addEventListener("click", () => openChannelDialog({ signal, announce, onChanged }));
  head.append(addBtn);
  card.append(head);
  card.append(disclosure({
    id: "notify-help",
    summary: t("settings.notifications.help_summary"),
    children: [
      el("p", { class: "cp-notify-source-note", text: t("settings.notifications.health_source_note") }),
      el("p", { class: "cp-muted-small", text: t("settings.notifications.saved_test_idle") }),
    ],
  }).node);

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
    list.append(channelRow({ channel: ch, health: health?.channels?.find((entry) => entry.channel_id === ch.id), healthAvailable: Boolean(health), signal, announce, onChanged }));
  }
  card.append(list);
  return card;
}

function channelRow({ channel, health, healthAvailable, signal, announce, onChanged }) {
  const row = el("li", { class: "cp-channel-row" });
  const typeDef = channelTypeByValue(channel.type);
  const mark = el("div", { class: "cp-notify-platform-mark", attrs: { "aria-hidden": "true" } });
  mark.append(icon(CHANNEL_TYPE_ICONS[channel.type] || "bell"));
  row.append(mark);

  const info = el("div", { class: "cp-channel-row-info" });
  info.append(el("span", { class: "cp-channel-row-name", text: channel.name }));
  info.append(el("span", { class: "cp-notify-destination", text: `${typeDef?.label || channel.type} · ${destinationText(channel)}` }));
  row.append(info);

  row.append(
    el("span", {
      class: `cp-chip ${channel.enabled ? "cp-chip-ok" : "cp-chip-other"}`,
      text: channel.enabled ? t("settings.notifications.enabled") : t("settings.notifications.disabled"),
    }),
  );

  const history = el("div", { class: "cp-notify-health-summary" });
  const stateKey = ["healthy", "degraded", "failing", "unknown"].includes(health?.state) ? `health.state_${health.state}` : "health.state_unknown";
  history.append(el("span", { class: "cp-notify-health-label", text: t("settings.notifications.recorded_health", { state: healthAvailable ? t(stateKey) : t("settings.notifications.health_unavailable") }) }));
  const latest = Math.max(health?.last_synthetic?.finished_at || health?.last_synthetic?.started_at || 0, health?.stats_30d?.last_success_at || 0, health?.stats_30d?.last_failure_at || 0);
  history.append(el("span", { class: "cp-muted-small", text: !healthAvailable ? t("settings.notifications.health_load_hint") : latest ? t("settings.notifications.health_last_activity", { when: formatDateTimeLocalized(latest * 1000, { dateStyle: "medium", timeStyle: "short" }) }) : t("settings.notifications.health_no_activity") }));

  const status = el("div", { class: "cp-channel-row-status", attrs: { role: "status", "aria-live": "polite", "aria-atomic": "true" } });
  // Empty until a test runs; what a test does is explained once, in the
  // card's help section.

  const testBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" } });
  const testLabel = el("span", { text: t("settings.notifications.send_test") });
  testBtn.setAttribute("aria-label", t("settings.notifications.send_saved_test_aria", { name: channel.name }));
  testBtn.append(icon("send"), testLabel);
  testBtn.addEventListener("click", async () => {
    if (testBtn.disabled || signal.aborted) return;
    testBtn.disabled = true;
    editBtn.disabled = true;
    deleteBtn.disabled = true;
    testLabel.textContent = t("settings.notifications.sending");
    status.setAttribute("aria-busy", "true");
    clearChildren(status);
    status.append(el("p", { class: "cp-muted-small", text: t("settings.notifications.sending_saved_test") }));
    try {
      const result = await testNotifyChannel(channel.id, signal);
      if (signal.aborted) return;
      clearChildren(status);
      status.append(testResultBanner(result, channel.type, {}, []));
      announce?.(result.ok ? t("settings.notifications.test_accepted") : t("settings.notifications.test_notification_failed"));
    } catch (err) {
      if (err?.name === "AbortError" || signal.aborted) return;
      clearChildren(status);
      status.append(errorBanner(describeError(err)));
      announce?.(describeError(err));
    } finally {
      status.setAttribute("aria-busy", "false");
      testBtn.disabled = false;
      editBtn.disabled = false;
      deleteBtn.disabled = false;
      testLabel.textContent = t("settings.notifications.send_test");
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
      announce?.(t("settings.notifications.deleted_channel", { name: channel.name }));
      onChanged();
    } catch (err) {
      if (err?.name === "AbortError") return;
      showToast({ message: t("settings.notifications.delete_failed", { error: describeError(err) }), variant: "error" });
    }
  });

  const actions = el("div", { class: "cp-channel-row-actions" });
  actions.append(testBtn, editBtn, deleteBtn);
  row.append(history, actions, status);
  return row;
}

function destinationText(channel) {
  const config = channel.config || {};
  if (channel.type === "telegram" && config.chat_id) {
    const chat = t("settings.notifications.destination_chat", { id: config.chat_id });
    return config.message_thread_id ? `${chat} · ${t("settings.notifications.destination_topic", { id: config.message_thread_id })}` : chat;
  }
  if (channel.type === "whatsapp" && config.to) return t("settings.notifications.destination_recipient", { number: config.to });
  // A Discord URL includes its credential; never derive a visible destination
  // from it. Generic webhook paths/query strings can also contain credentials.
  if (channel.type === "webhook" && config.url) {
    try { return new URL(config.url).hostname; } catch { /* keep private */ }
  }
  return t("settings.notifications.destination_private");
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

  // Stored credentials must never become an input value, even if an upstream
  // response accidentally contains an unredacted value. Blank means preserve
  // on edit; only an explicitly typed replacement is held in the draft.
  const preservedSecrets = new Set();
  for (const field of channelTypeByValue(draft.type)?.fields || []) {
    if (!field.secret) continue;
    if (isEdit && draft.config[field.key]) preservedSecrets.add(field.key);
    delete draft.config[field.key];
  }
  const form = el("form", { class: "cp-notify-form", attrs: { novalidate: "", "aria-label": t("settings.notifications.channel_configuration") } });
  dialog.className += " cp-notify-dialog";
  body.append(form);
  let generation = 0;
  let testController = null;
  let disposed = false;
  let saving = false;

  function dispose() {
    if (disposed) return;
    disposed = true;
    generation++;
    testController?.abort();
    signal.removeEventListener("abort", dismiss);
    for (const input of dialog.querySelectorAll("input")) {
      if (input.getAttribute("type") === "password") input.value = "";
    }
    dialog.remove();
    // Drop user-entered credentials when the dialog leaves the document.
    draft.config = {};
  }
  function dismiss() { close(); dispose(); }
  dialog.addEventListener("close", dispose);
  dialog.addEventListener("cancel", (event) => {
    if (saving) { event.preventDefault(); return; }
    generation++;
    testController?.abort();
  });
  signal.addEventListener("abort", dismiss, { once: true });

  const layout = el("div", { class: "cp-channel-dialog-layout" });
  const formCol = el("div", { class: "cp-channel-dialog-form" });
  const guideCol = el("div", { class: "cp-channel-dialog-guide" });
  layout.append(formCol, guideCol);
  form.append(layout);
  const context = el("div", { class: "cp-notify-context" });
  const contextTitle = el("strong");
  const contextDescription = el("p", { class: "cp-muted-small" });
  context.append(contextTitle, contextDescription);
  formCol.append(context);
  if (!draft.name) draft.name = defaultChannelName(draft.type);
  const initial = JSON.stringify(draft);

  function renderContext() {
    const changed = JSON.stringify(draft) !== initial;
    contextTitle.textContent = t(isEdit && !changed ? "settings.notifications.edit_saved_context" : "settings.notifications.unsaved_context");
    contextDescription.textContent = t(isEdit ? "settings.notifications.edit_test_context" : "settings.notifications.add_test_context");
  }
  renderContext();

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
      preservedSecrets.clear();
      if (!draft.name || CHANNEL_TYPES.some((tDef) => tDef.label === draft.name)) {
        draft.name = defaultChannelName(draft.type);
        nameInput.value = draft.name;
      }
      renderFields();
      renderGuide();
      draftChanged();
    });
    formCol.append(typeNode);
  } else {
    const platform = el("div", { class: "cp-notify-platform-locked" });
    platform.append(el("span", { class: "cp-label", text: t("settings.notifications.platform_label") }));
    platform.append(el("strong", { text: channelTypeByValue(draft.type)?.label || draft.type }));
    platform.append(el("p", { class: "cp-muted-small", text: t("settings.notifications.platform_locked_hint") }));
    formCol.append(platform);
  }

  // --- Name + enabled ---
  const nameField = el("div", { class: "cp-field" });
  nameField.append(el("label", { class: "cp-label", attrs: { for: "cp-channel-name" }, text: t("settings.notifications.name_label") }));
  const nameInput = /** @type {HTMLInputElement} */ (
    el("input", { class: "cp-input", attrs: { id: "cp-channel-name", name: "channel_name", type: "text", maxlength: "200", required: "", value: draft.name || defaultChannelName(draft.type) } })
  );
  nameInput.addEventListener("input", () => {
    draft.name = nameInput.value;
    draftChanged();
  });
  nameField.append(nameInput);
  formCol.append(nameField);
  if (!draft.name) draft.name = defaultChannelName(draft.type);
  renderContext();

  const enabledField = el("label", { class: "cp-checkbox-field" });
  const enabledInput = /** @type {HTMLInputElement} */ (el("input", { attrs: { type: "checkbox" } }));
  enabledInput.checked = draft.enabled;
  enabledInput.addEventListener("change", () => {
    draft.enabled = enabledInput.checked;
    draftChanged();
  });
  enabledField.append(enabledInput, el("span", { text: t("settings.notifications.enabled") }));
  formCol.append(enabledField);
  formCol.append(el("p", { class: "cp-muted-small", text: t("settings.notifications.enabled_test_hint") }));

  const fieldsHost = el("div", { class: "cp-channel-dialog-fields" });
  formCol.append(fieldsHost);

  const errorsHost = el("div", { class: "cp-notify-form-errors", attrs: { id: "cp-notify-form-errors" } });
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
      const { wrap, input } = configFieldInput(field, draft, preservedSecrets.has(field.key), draftChanged);
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

  const testPanel = el("section", { class: "cp-notify-test-panel", attrs: { "aria-labelledby": "cp-notify-test-title" } });
  const testHead = el("div", { class: "cp-notify-test-head" });
  testHead.append(el("h3", { class: "cp-channel-guide-title", attrs: { id: "cp-notify-test-title" }, text: t("settings.notifications.test_current_configuration") }));
  testPanel.append(testHead);
  testPanel.append(el("p", { class: "cp-muted-small", text: t("settings.notifications.test_sample_note") }));
  const testResultHost = el("div", { class: "cp-channel-dialog-test-result", attrs: { role: "status", "aria-live": "polite", "aria-atomic": "true" } });
  testPanel.append(testResultHost);
  formCol.append(testPanel);
  function renderIdle(changed = false) {
    clearChildren(testResultHost);
    const text = changed
      ? t("settings.notifications.test_config_changed")
      : t("settings.notifications.draft_test_idle");
    testResultHost.append(el("p", { class: "cp-muted-small", text }));
  }
  renderIdle();

  const testBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" } });
  const testLabel = el("span", { text: t("settings.notifications.send_test") });
  testBtn.append(icon("send"), testLabel);

  function draftChanged() {
    generation++;
    testController?.abort();
    testController = null;
    testBtn.disabled = false;
    saveBtn.disabled = saving;
    testLabel.textContent = t("settings.notifications.send_test");
    testResultHost.setAttribute("aria-busy", "false");
    clearChildren(errorsHost);
    clearFieldHighlights({ name: nameInput, ...fieldInputs });
    renderIdle(true);
    renderContext();
  }

  function validateForm() {
    const validationConfig = { ...draft.config };
    for (const key of preservedSecrets) {
      if (!validationConfig[key]) validationConfig[key] = key === "webhook_url" ? "https://discord.com/api/webhooks/preserved" : "preserved";
    }
    const errors = validateChannelConfig(draft.type, validationConfig);
    if (!draft.name.trim()) errors.name = t("settings.notifications.unnamed_field_error");
    else if (new TextEncoder().encode(draft.name.trim()).length > 200) errors.name = t("settings.notifications.name_too_long_error");
    for (const field of channelTypeByValue(draft.type)?.fields || []) {
      if (field.secret && draft.config[field.key] === "***") errors[field.key] = t("settings.notifications.secret_placeholder_error");
    }
    clearChildren(errorsHost);
    clearFieldHighlights({ name: nameInput, ...fieldInputs });
    if (!Object.keys(errors).length) return true;
    renderErrors(errorsHost, errors);
    const inputs = { name: nameInput, ...fieldInputs };
    for (const key of Object.keys(errors)) {
      inputs[key]?.setAttribute("aria-invalid", "true");
      inputs[key]?.setAttribute("aria-describedby", "cp-notify-form-errors");
    }
    inputs[Object.keys(errors)[0]]?.focus();
    renderIdle();
    return false;
  }

  testBtn.addEventListener("click", async () => {
    if (testBtn.disabled || saving || disposed || signal.aborted || !validateForm()) return;
    const current = ++generation;
    testController?.abort();
    const requestController = new AbortController();
    testController = requestController;
    const snapshot = { name: draft.name.trim(), type: draft.type, config: { ...draft.config }, ...(isEdit ? { existing_channel_id: channel.id } : {}) };
    const secrets = sensitiveValues(snapshot.type, snapshot.config);
    testBtn.disabled = true;
    saveBtn.disabled = true;
    testLabel.textContent = t("settings.notifications.sending");
    testResultHost.setAttribute("aria-busy", "true");
    clearChildren(testResultHost);
    clearFieldHighlights(fieldInputs);
    testResultHost.append(el("p", { class: "cp-muted-small", text: t("settings.notifications.sending_test") }));
    try {
      // Always test the current form. The backend merges only omitted stored
      // secrets by ID, never a stale saved destination or a placeholder token.
      const result = await testDraftNotifyChannel(snapshot, requestController.signal);
      if (disposed || signal.aborted || current !== generation || requestController.signal.aborted) return;
      clearChildren(testResultHost);
      testResultHost.append(testResultBanner(result, snapshot.type, fieldInputs, secrets));
      announce?.(result.ok ? t("settings.notifications.test_accepted") : t("settings.notifications.test_notification_failed"));
    } catch (err) {
      if (disposed || signal.aborted || current !== generation || requestController.signal.aborted || err?.name === "AbortError") return;
      clearChildren(testResultHost);
      testResultHost.append(errorBanner(describeError(err)));
      if (err instanceof ApiError && err.status === 400 && err.details) {
        const inputs = { name: nameInput, ...fieldInputs };
        for (const key of Object.keys(err.details)) inputs[key]?.setAttribute("aria-invalid", "true");
        inputs[Object.keys(err.details)[0]]?.focus();
      }
      announce?.(describeError(err));
    } finally {
      if (!disposed && current === generation) {
        testController = null;
        testBtn.disabled = false;
        saveBtn.disabled = saving;
        testLabel.textContent = t("settings.notifications.send_test");
        testResultHost.setAttribute("aria-busy", "false");
      }
    }
  });
  testHead.append(testBtn);

  const cancelBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: t("settings.notifications.cancel") });
  cancelBtn.addEventListener("click", dismiss);

  const saveBtn = el("button", {
    class: "cp-btn cp-btn-primary",
    attrs: { type: "submit" },
    text: isEdit ? t("settings.notifications.save_changes") : t("settings.notifications.add_channel_submit"),
  });
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (saving || saveBtn.disabled || disposed || signal.aborted || !validateForm()) return;
    generation++;
    testController?.abort();
    saving = true;
    renderIdle();
    saveBtn.disabled = true;
    testBtn.disabled = true;
    cancelBtn.disabled = true;
    form.setAttribute("aria-busy", "true");
    const controls = form.querySelectorAll("input, select");
    for (const control of controls) control.disabled = true;
    const payload = { name: draft.name.trim(), type: draft.type, enabled: draft.enabled, config: { ...draft.config } };
    try {
      if (isEdit) {
        await updateNotifyChannel(channel.id, payload, signal);
      } else {
        await createNotifyChannel(payload, signal);
      }
      if (disposed || signal.aborted) return;
      announce?.(t(isEdit ? "settings.notifications.saved_channel" : "settings.notifications.added_channel", { name: payload.name }));
      dismiss();
      onChanged();
    } catch (err) {
      if (err?.name === "AbortError" || disposed || signal.aborted) return;
      renderErrors(errorsHost, apiErrorDetails(err));
    } finally {
      saving = false;
      saveBtn.disabled = false;
      testBtn.disabled = false;
      cancelBtn.disabled = false;
      form.setAttribute("aria-busy", "false");
      for (const control of controls) control.disabled = false;
    }
  });

  form.append(buildDialogActions([cancelBtn, saveBtn]));
  document.body.append(dialog);
  open();
}

/**
 * configFieldInput builds one Config field's <label>+<input> pair.
 * @returns {{wrap: HTMLElement, input: HTMLInputElement|null}} input is
 *   null for a checkbox field (checkbox fields aren't diagnosis-
 *   highlight targets in practice, and the caller's fieldInputs map
 *   only needs text/password fields to support aria-invalid + focus).
 */
function configFieldInput(field, draft, isSecretPreserved, onChanged) {
  const wrap = el("div", { class: "cp-field" });
  const inputID = `cp-channel-field-${field.key}`;
  wrap.append(el("label", { class: "cp-label", attrs: { for: inputID }, text: field.label }));

  if (field.type === "checkbox") {
    const checkboxWrap = el("label", { class: "cp-checkbox-field" });
    const checkbox = /** @type {HTMLInputElement} */ (el("input", { attrs: { type: "checkbox", id: inputID } }));
    checkbox.checked = draft.config[field.key] === "true" || draft.config[field.key] === "1";
    checkbox.addEventListener("change", () => {
      draft.config[field.key] = checkbox.checked ? "true" : "false";
      onChanged();
    });
    checkboxWrap.append(checkbox, el("span", { text: field.label }));
    clearChildren(wrap);
    wrap.append(checkboxWrap);
    return { wrap, input: null };
  }

  const existingValue = draft.config[field.key] || "";
  const input = /** @type {HTMLInputElement} */ (
    el("input", {
      class: "cp-input",
      attrs: {
        id: inputID,
        name: field.key,
        type: field.type === "password" ? "password" : "text",
        placeholder: isSecretPreserved ? t("settings.notifications.secret_field_unchanged_placeholder") : field.placeholder,
        ...(field.type === "password" ? { autocomplete: "new-password", autocapitalize: "none", spellcheck: "false" } : {}),
        ...(isSecretPreserved ? { "aria-describedby": `${inputID}-hint` } : {}),
      },
    })
  );
  // Property-only values keep typed credentials out of serialized markup.
  input.value = existingValue;
  input.addEventListener("input", () => {
    if (isSecretPreserved && input.value === "") delete draft.config[field.key];
    else draft.config[field.key] = input.value;
    input.removeAttribute("aria-invalid");
    onChanged();
  });
  wrap.append(input);
  if (isSecretPreserved) {
    wrap.append(el("p", { class: "cp-muted-small", attrs: { id: `${inputID}-hint` }, text: t("settings.notifications.secret_field_hint") }));
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

function apiErrorDetails(err) {
  return { form: describeError(err) };
}

function testResultBanner(result, platform, fieldInputs, secrets) {
  const wrap = el("div", { class: `cp-notify-test-result ${result.ok ? "cp-notify-test-accepted" : "cp-notify-test-failed"}` });
  if (result.ok) {
    wrap.append(icon("circleCheck"));
    const copy = el("div", { class: "cp-notify-result-copy" });
    copy.append(el("strong", { text: t("settings.notifications.test_accepted") }));
    const key = ["telegram", "whatsapp", "discord"].includes(platform) ? `test_accepted_${platform}` : "test_accepted_webhook";
    copy.append(el("p", { text: t(`settings.notifications.${key}`) }));
    copy.append(el("p", { class: "cp-muted-small", text: t("settings.notifications.test_health_unchanged") }));
    wrap.append(copy);
    return wrap;
  }
  wrap.append(diagnosisCard(result.diagnosis, platform, fieldInputs, secrets));
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
function diagnosisCard(diagnosis, platform, fieldInputs, secrets = []) {
  const vm = diagnosisViewModel(platform, diagnosis);

  const card = el("div", { class: "cp-diagnosis-card cp-notify-diagnosis", attrs: { role: "alert" } });
  const head = el("div", { class: "cp-diagnosis-card-head" });
  head.append(icon("circleX"));
  head.append(el("strong", { text: t("settings.notifications.test_notification_failed") }));
  card.append(head);
  const group = diagnosisHintGroup(vm.code, vm.fieldToHighlight);
  card.append(el("p", { text: t(`settings.notifications.test_hint_${group}`) }));
  if (vm.detail) card.append(el("p", { class: "cp-notify-provider-detail", text: t("settings.notifications.provider_detail", { detail: safeDiagnosisText(vm.detail, secrets) }) }));
  const docs = {
    discord: "https://discord.com/developers/docs/resources/webhook",
    telegram: "https://core.telegram.org/bots/api",
    whatsapp: "https://developers.facebook.com/docs/whatsapp/cloud-api/get-started",
  }[platform];
  if (docs) {
    card.append(
      el("a", {
        class: "cp-link",
        attrs: { href: docs, target: "_blank", rel: "noopener noreferrer" },
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

function diagnosisHintGroup(code, field) {
  if (code === "timeout") return "timeout";
  if (code === "rate_limited") return "rate";
  if (code === "tls_error") return "tls";
  if (["dns_failure", "network_unreachable", "connection_refused"].includes(code)) return "network";
  if (code === "whatsapp_permission") return "permission";
  if (code === "whatsapp_media_failed") return "media";
  if (field === "webhook_url") return "webhook";
  if (["bot_token", "access_token"].includes(field)) return "token";
  if (field === "message_thread_id") return "topic";
  if (field === "template_name") return "template";
  if (["chat_id", "to"].includes(field)) return "destination";
  return "config";
}

function sensitiveValues(type, config) {
  return (channelTypeByValue(type)?.fields || [])
    .filter((field) => field.secret || field.type === "password")
    .map((field) => config[field.key]).filter((value) => value && value !== "***");
}

function safeDiagnosisText(text, secrets) {
  let safe = String(text || "");
  for (const secret of secrets) safe = safe.split(secret).join("[redacted]");
  // Additional guards for a malformed provider response, including URLs whose
  // paths/query strings can contain credentials. Never render it as HTML.
  return safe.replace(/https?:\/\/[^\s<>"']+/gi, "[URL redacted]")
    .replace(/\b\d{5,}:[A-Za-z0-9_-]+\b/g, "[redacted]")
    .replace(/\bBearer\s+\S+/gi, "Bearer [redacted]").slice(0, 700);
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
    if (input?.getAttribute?.("aria-describedby") === "cp-notify-form-errors") input.removeAttribute("aria-describedby");
  }
}

function describeError(err) {
  if (err instanceof ApiError) {
    if (err.status === 400) return t("settings.notifications.request_invalid");
    if (err.status === 404) return t("settings.notifications.request_missing");
    if (err.status === 401 || err.status === 403) return t("settings.notifications.request_auth");
    if (err.status === 429) return t("settings.notifications.test_hint_rate");
    if (err.code === "notify_not_configured") return t("settings.notifications.request_not_configured");
    if (err.status >= 500) return t("settings.notifications.request_server");
  }
  // Do not expose arbitrary transport/store text that may include a URL/token.
  return t("settings.notifications.request_network");
}

/**
 * tabFromHash returns the Notifications sub-tab named by the hash's
 * `tab` query parameter ("#/settings/notifications?tab=health"),
 * defaulting to "channels".
 * @param {string} hash
 * @returns {"channels"|"health"}
 */
export function tabFromHash(hash) {
  const q = hash.indexOf("?");
  const tab = q === -1 ? "" : new URLSearchParams(hash.slice(q + 1)).get("tab");
  return SUB_TABS.some((entry) => entry.id === tab) ? /** @type {"channels"|"health"} */ (tab) : "channels";
}

/**
 * hashForTab returns hash with its `tab` query parameter set to tabID
 * ("channels" removes it), keeping the route path and other params.
 * @param {string} hash
 * @param {string} tabID
 * @returns {string}
 */
export function hashForTab(hash, tabID) {
  const q = hash.indexOf("?");
  const route = (q === -1 ? hash : hash.slice(0, q)) || "#/settings/notifications";
  const params = new URLSearchParams(q === -1 ? "" : hash.slice(q + 1));
  if (tabID === "channels") params.delete("tab");
  else params.set("tab", tabID);
  const qs = params.toString();
  return qs ? `${route}?${qs}` : route;
}
