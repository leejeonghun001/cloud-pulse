// notify-guides.js — pure per-platform setup content and field
// definitions for the Notifications "Add channel" dialog (SPEC-v0.5
// §D): step-by-step guides with exact UI paths, the Config fields each
// platform's channelRequest.config needs, and lightweight client-side
// field validation mirroring the notify package's Sender constructors.
// No DOM access here so this module is fully covered by node --test
// without a browser; the settings UI renders CHANNEL_TYPES' data with
// its own DOM builders.

/**
 * CHANNEL_TYPES lists every models.NotifyChannelType in display order
 * with: a label, its Config field definitions (key, label, type
 * "text"|"password"|"url", placeholder, secret flag — mirrors
 * models.NotifyChannelType.SecretFields()), and a numbered step-by-step
 * guide with exact UI paths per SPEC-v0.5 §D.
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
    fields: [
      {
        key: "webhook_url",
        label: "Webhook URL",
        type: "password",
        placeholder: "https://discord.com/api/webhooks/…",
        secret: true,
        required: true,
      },
    ],
    steps: [
      "Open your Discord server, then go to Server Settings → Integrations → Webhooks.",
      "Click New Webhook (or select an existing one), pick the channel it should post to.",
      "Click Copy Webhook URL.",
      "Paste it into the Webhook URL field below, then Send test.",
    ],
  },
  {
    value: "telegram",
    label: "Telegram",
    fields: [
      { key: "bot_token", label: "Bot token", type: "password", placeholder: "123456:TEST-TOKEN", secret: true, required: true },
      { key: "chat_id", label: "Chat ID", type: "text", placeholder: "-1001234567890", secret: false, required: true },
      { key: "message_thread_id", label: "Message thread ID (optional)", type: "text", placeholder: "", secret: false, required: false },
    ],
    steps: [
      "In Telegram, message @BotFather and send /newbot, then follow its prompts.",
      "@BotFather replies with your bot token — copy it into the Bot token field below.",
      "Add the bot to the chat/group/channel you want alerts posted to.",
      "Send any message in that chat, then open https://api.telegram.org/bot<token>/getUpdates in a browser (with your token substituted) to find \"chat\":{\"id\": …} — copy that number into Chat ID.",
      "Send test to confirm delivery.",
    ],
  },
  {
    value: "whatsapp",
    label: "WhatsApp",
    fields: [
      { key: "access_token", label: "Access token", type: "password", placeholder: "", secret: true, required: true },
      { key: "phone_number_id", label: "Phone number ID", type: "text", placeholder: "", secret: false, required: true },
      { key: "to", label: "Recipient (E.164 digits)", type: "text", placeholder: "15551234567", secret: false, required: true },
      { key: "template_name", label: "Template name (recommended)", type: "text", placeholder: "", secret: false, required: false },
      { key: "template_lang", label: "Template language", type: "text", placeholder: "en_US", secret: false, required: false },
    ],
    steps: [
      "Go to Meta for Developers → My Apps → create (or open) an app, then add the WhatsApp product.",
      "Under WhatsApp → API Setup, copy a temporary (or generate a permanent) access token into Access token below.",
      "On the same page, copy the Phone number ID into the field below.",
      "Add your own number as a recipient (API Setup → \"To\") and enter it, digits only with country code, in Recipient below.",
      "Create and get approval for a message template with an image header (recommended) — enter its name in Template name. Without a template, alerts only deliver inside the 24-hour customer-service window after the recipient last messaged your business number.",
      "Send test to confirm delivery.",
    ],
  },
  {
    value: "webhook",
    label: "Webhook (generic)",
    fields: [
      { key: "webhook_url", label: "Webhook URL", type: "password", placeholder: "https://hooks.example.com/…", secret: false, required: true },
      { key: "include_image", label: "Include chart image (base64)", type: "checkbox", placeholder: "", secret: false, required: false },
    ],
    steps: [
      "Any endpoint that accepts a Slack- or Discord-compatible JSON POST body works here (custom scripts, Zapier, n8n, ...).",
      "Paste its URL into Webhook URL below.",
      "Enable \"Include chart image\" if your endpoint can handle a base64-encoded PNG field (image_png_base64); leave it off for plain-text-only receivers.",
      "Send test to confirm delivery.",
    ],
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
    errors.type = "Choose a platform.";
    return errors;
  }
  for (const field of def.fields) {
    if (field.type === "checkbox") continue;
    const value = (config && config[field.key]) || "";
    if (field.required && value.trim() === "") {
      errors[field.key] = `${field.label} is required.`;
    }
  }
  if (def.value === "discord") {
    const url = (config && config.webhook_url) || "";
    if (url && !/^https:\/\/(discord\.com|discordapp\.com)\/api\/webhooks\//.test(url)) {
      errors.webhook_url = "Must be a discord.com or discordapp.com webhook URL.";
    }
  }
  if (def.value === "whatsapp") {
    const to = (config && config.to) || "";
    if (to && !/^[0-9]{6,15}$/.test(to)) {
      errors.to = "Must be digits only (E.164 without the leading +), e.g. 15551234567.";
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
