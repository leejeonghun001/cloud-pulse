// notify-guides.js — pure per-platform setup content and field
// definitions for the Notifications "Add channel" dialog (SPEC-v0.5
// §D): step-by-step guides with exact UI paths, the Config fields each
// platform's channelRequest.config needs, and lightweight client-side
// field validation mirroring the notify package's Sender constructors.
// No DOM access here so this module is fully covered by node --test
// without a browser; the settings UI renders CHANNEL_TYPES' data with
// its own DOM builders.
import { t } from "./i18n.js";

/**
 * CHANNEL_TYPES lists every models.NotifyChannelType in display order
 * with: a label, its Config field definitions (key, label, type
 * "text"|"password"|"url", placeholder, secret flag — mirrors
 * models.NotifyChannelType.SecretFields()), and a numbered step-by-step
 * guide with exact UI paths per SPEC-v0.5 §D.
 *
 * `label` is the platform's product name (Discord/Telegram/WhatsApp)
 * and is intentionally a plain string, never translated — every other
 * user-visible string here (field labels/placeholders, guide steps) is
 * a getter routed through t() so it re-resolves against the *current*
 * language every time it's read, since this module's exports are
 * static top-level data evaluated once at import time.
 * @type {{
 *   value: string,
 *   label: string,
 *   fields: {key: string, label: string, type: string, placeholder: string, secret: boolean, required: boolean}[],
 *   steps: string[],
 * }[]}
 */
export const CHANNEL_TYPES = [
  {
    value: "discord",
    label: "Discord",
    get fields() {
      return [
        {
          key: "webhook_url",
          label: t("notify.discord_field_webhook_url"),
          type: "password",
          placeholder: "https://discord.com/api/webhooks/…",
          secret: true,
          required: true,
        },
      ];
    },
    get steps() {
      return [
        t("notify.discord_step_1"),
        t("notify.discord_step_2"),
        t("notify.discord_step_3"),
        t("notify.discord_step_4"),
      ];
    },
  },
  {
    value: "telegram",
    label: "Telegram",
    get fields() {
      return [
        { key: "bot_token", label: t("notify.telegram_field_bot_token"), type: "password", placeholder: "123456:TEST-TOKEN", secret: true, required: true },
        { key: "chat_id", label: t("notify.telegram_field_chat_id"), type: "text", placeholder: "-1001234567890", secret: false, required: true },
        { key: "message_thread_id", label: t("notify.telegram_field_message_thread_id"), type: "text", placeholder: "", secret: false, required: false },
      ];
    },
    get steps() {
      return [
        t("notify.telegram_step_1"),
        t("notify.telegram_step_2"),
        t("notify.telegram_step_3"),
        t("notify.telegram_step_4"),
        t("notify.telegram_step_5"),
      ];
    },
  },
  {
    value: "whatsapp",
    label: "WhatsApp",
    get fields() {
      return [
        { key: "access_token", label: t("notify.whatsapp_field_access_token"), type: "password", placeholder: "", secret: true, required: true },
        { key: "phone_number_id", label: t("notify.whatsapp_field_phone_number_id"), type: "text", placeholder: "", secret: false, required: true },
        { key: "to", label: t("notify.whatsapp_field_to"), type: "text", placeholder: "15551234567", secret: false, required: true },
        { key: "template_name", label: t("notify.whatsapp_field_template_name"), type: "text", placeholder: "", secret: false, required: false },
        { key: "template_lang", label: t("notify.whatsapp_field_template_lang"), type: "text", placeholder: "en_US", secret: false, required: false },
      ];
    },
    get steps() {
      return [
        t("notify.whatsapp_step_1"),
        t("notify.whatsapp_step_2"),
        t("notify.whatsapp_step_3"),
        t("notify.whatsapp_step_4"),
        t("notify.whatsapp_step_5"),
        t("notify.whatsapp_step_6"),
      ];
    },
  },
  {
    value: "webhook",
    get label() {
      return t("notify.webhook_label");
    },
    get fields() {
      return [
        { key: "url", label: t("notify.webhook_field_url"), type: "password", placeholder: "https://hooks.example.com/…", secret: false, required: true },
        { key: "include_image", label: t("notify.webhook_field_include_image"), type: "checkbox", placeholder: "", secret: false, required: false },
      ];
    },
    get steps() {
      return [
        t("notify.webhook_step_1"),
        t("notify.webhook_step_2"),
        t("notify.webhook_step_3"),
        t("notify.webhook_step_4"),
      ];
    },
  },
];

/**
 * channelTypeByValue looks up a CHANNEL_TYPES entry by its value,
 * returning undefined for an unknown type.
 * @param {string} type
 * @returns {typeof CHANNEL_TYPES[number]|undefined}
 */
export function channelTypeByValue(type) {
  return CHANNEL_TYPES.find((t) => t.value === type);
}

/**
 * validateChannelConfig validates a draft channel's Config against its
 * type's field definitions, returning a {field: message} map of errors
 * (empty object = valid). Only checks required-field presence — deeper
 * platform-specific validation (URL host allowlisting, token shape) is
 * the hub/notify package's authority and is surfaced via the "Send
 * test" result instead of duplicated here.
 * @param {string} type
 * @param {Object<string,string>} config
 * @returns {Object<string,string>}
 */
export function validateChannelConfig(type, config) {
  const def = channelTypeByValue(type);
  const errors = {};
  if (!def) {
    errors.type = t("notify.error_choose_platform");
    return errors;
  }
  for (const field of def.fields) {
    if (field.type === "checkbox") continue;
    const value = (config && config[field.key]) || "";
    if (field.required && value.trim() === "") {
      errors[field.key] = t("notify.error_field_required", { field: field.label });
    }
  }
  if (def.value === "discord") {
    const url = (config && config.webhook_url) || "";
    if (url && !/^https:\/\/(discord\.com|discordapp\.com)\/api\/webhooks\//.test(url)) {
      errors.webhook_url = t("notify.error_discord_webhook_url");
    }
  }
  if (def.value === "whatsapp") {
    const to = (config && config.to) || "";
    if (to && !/^[0-9]{6,15}$/.test(to)) {
      errors.to = t("notify.error_whatsapp_recipient");
    }
  }
  return errors;
}

/**
 * defaultChannelName returns a sensible default Name for a new channel
 * of the given type, e.g. "discord" -> "Discord". Falls back to the
 * raw type for an unknown value.
 * @param {string} type
 * @returns {string}
 */
export function defaultChannelName(type) {
  return channelTypeByValue(type)?.label ?? type;
}
