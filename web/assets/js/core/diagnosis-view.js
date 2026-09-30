// diagnosis-view.js — pure helpers turning a models.Diagnosis (SPEC-v0.6
// §4) into a view model for the notify-channel dialog and channel-list
// tooltip: which Config field (if any) a given diagnosis code should
// highlight with aria-invalid + focus, and the compact one-line summary
// shown in the channel list. No DOM access here so this is fully
// covered by `node --test` without a browser; the settings UI
// (pages/settings/notifications.js) is the sole DOM consumer.

/**
 * DIAGNOSIS_FIELD_BY_CODE maps a models.DiagnosisCode to the Config
 * field key (matching core/notify-guides.js's CHANNEL_TYPES[].fields[].key)
 * that most likely needs correcting, per platform. A code absent from
 * this map (e.g. a transport-level failure like dns_failure/timeout,
 * which isn't any one field's fault) highlights no field.
 * @type {Record<string, Record<string, string>>}
 */
const DIAGNOSIS_FIELD_BY_CODE = {
  discord_webhook_not_found: { discord: "webhook_url" },
  discord_unauthorized: { discord: "webhook_url" },
  telegram_unauthorized: { telegram: "bot_token" },
  telegram_chat_not_found: { telegram: "chat_id" },
  telegram_bot_blocked: { telegram: "chat_id" },
  telegram_not_member: { telegram: "chat_id" },
  telegram_thread_not_found: { telegram: "message_thread_id" },
  whatsapp_token_invalid: { whatsapp: "access_token" },
  whatsapp_permission: { whatsapp: "access_token" },
  whatsapp_recipient_not_allowed: { whatsapp: "to" },
  whatsapp_window_closed: { whatsapp: "template_name" },
  whatsapp_template_missing: { whatsapp: "template_name" },
  whatsapp_media_failed: {},
  invalid_config: {},
};

/**
 * fieldToHighlight returns the Config field key that should receive
 * aria-invalid + focus for a given (platform, diagnosis code)
 * combination, or "" if no specific field applies (network-level
 * failures, unmapped/unknown codes, or a platform this code doesn't
 * apply to).
 * @param {string} platform "discord"|"telegram"|"whatsapp"|"webhook"
 * @param {string|null|undefined} code a models.DiagnosisCode value
 * @returns {string}
 */
export function fieldToHighlight(platform, code) {
  if (!code) return "";
  const byPlatform = DIAGNOSIS_FIELD_BY_CODE[code];
  if (!byPlatform) return "";
  return byPlatform[platform] || "";
}

/**
 * diagnosisViewModel builds the full view model for the red diagnosis
 * card shown after a failed Send test (SPEC-v0.6 §4): title/detail/hint/
 * docs link (straight passthrough from the API's Diagnosis, each
 * defaulted to a safe non-empty string so the card is never blank even
 * if the backend omits a field) plus which field to highlight for the
 * given platform.
 * @param {string} platform
 * @param {{code?: string, title?: string, detail?: string, hint?: string, docs_url?: string}|null|undefined} diagnosis
 * @returns {{code: string, title: string, detail: string, hint: string, docsUrl: string, fieldToHighlight: string}}
 */
export function diagnosisViewModel(platform, diagnosis) {
  const code = diagnosis?.code || "";
  return {
    code,
    title: diagnosis?.title || "Send test failed",
    detail: diagnosis?.detail || "",
    hint: diagnosis?.hint || "",
    docsUrl: diagnosis?.docs_url || "",
    fieldToHighlight: fieldToHighlight(platform, code),
  };
}

/**
 * diagnosisSummaryText renders a compact one-line summary of a
 * diagnosis for the channel list row's last-test-result text/tooltip,
 * e.g. "Connection refused — check firewall/proxy/outbound rules.".
 * Falls back to "unknown error" if diagnosis is missing entirely (all
 * fields absent) — should not happen per the API contract, but keeps
 * this resilient to an unexpected response shape.
 * @param {{title?: string, hint?: string}|null|undefined} diagnosis
 * @returns {string}
 */
export function diagnosisSummaryText(diagnosis) {
  if (!diagnosis) return "unknown error";
  const title = diagnosis.title || "";
  const hint = diagnosis.hint || "";
  if (title && hint) return `${title} — ${hint}`;
  return title || hint || "unknown error";
}
