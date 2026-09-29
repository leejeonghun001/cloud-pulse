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
    try {
      const result = await testNotifyChannel(channel.id, signal);
      status.textContent = result.ok ? "Test delivered." : `Test failed: ${result.error || "unknown error"}`;
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

  function renderFields() {
    clearChildren(fieldsHost);
    const def = channelTypeByValue(draft.type);
    if (!def) return;
    for (const field of def.fields) {
      fieldsHost.append(configFieldInput(field, draft, isEdit));
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
    testResultHost.append(el("p", { class: "cp-muted-small", text: "Sending test notification…" }));
    try {
      const result = isEdit && !hasUnsavedSecretChanges(channel, draft)
        ? await testNotifyChannel(channel.id, signal)
        : await testDraftNotifyChannel({ name: draft.name, type: draft.type, enabled: true, config: draft.config }, signal);
      clearChildren(testResultHost);
      testResultHost.append(testResultBanner(result));
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
    return wrap;
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
  });
  wrap.append(input);
  if (isSecretPreserved) {
    wrap.append(el("p", { class: "cp-muted-small", text: "Leave blank to keep the currently stored value." }));
  }
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

function testResultBanner(result) {
  const wrap = el("div", { class: `cp-alerts-test-result ${result.ok ? "cp-alerts-test-ok" : "cp-alerts-test-fail"}` });
  wrap.append(icon(result.ok ? "circleCheck" : "circleX"));
  wrap.append(el("span", { text: result.ok ? "Delivered successfully." : `Failed: ${result.error || "unknown error"}` }));
  return wrap;
}

function describeError(err) {
  if (err instanceof ApiError) return err.message;
  return String(err?.message || err);
}
