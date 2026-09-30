// notify-checklists.js — pure per-platform "real account verification"
// checklist content (SPEC-v0.6 §4's 설정 가이드 체크리스트) plus the
// localStorage key-naming and progress-shape helpers the Settings ->
// Notifications page uses to persist a channel's checked/unchecked
// state and last-successful-Send-test timestamp across sessions.
//
// No DOM access here (mirrors notify-guides.js's separation of pure
// data from rendering) so this module is fully covered by
// `node --test` without a browser; the settings UI renders
// CHECKLISTS' data with its own DOM builders and reads/writes progress
// via loadChecklistProgress/saveChecklistProgress.

/**
 * CHECKLISTS lists the real-account verification checklist for each
 * platform that has one (discord, telegram, whatsapp — the generic
 * "webhook" channel type has no platform account to verify against,
 * so it is intentionally absent here). Each item is a short, concrete,
 * checkable step per SPEC-v0.6 §4.
 * @type {{
 *   value: string,
 *   label: string,
 *   items: {id: string, text: string}[],
 * }[]}
 */
export const CHECKLISTS = [
  {
    value: "discord",
    label: "Discord",
    items: [
      { id: "webhook_permission", text: "Confirm you have the Manage Webhooks permission on the channel the webhook was created in." },
      { id: "url_format", text: "Confirm the URL matches https://discord.com/api/webhooks/<id>/<token>." },
      { id: "send_test_image", text: "Send test and confirm the image embed actually appears in the Discord channel." },
      { id: "regen_invalidates", text: "Remember: deleting or regenerating the webhook invalidates this URL immediately." },
    ],
  },
  {
    value: "telegram",
    label: "Telegram",
    items: [
      { id: "bot_token_issued", text: "Issue a bot token via @BotFather." },
      { id: "bot_added", text: "Invite the bot to the group (or add it as an admin for a channel)." },
      { id: "chat_id_confirmed", text: "Message the bot first, then confirm chat_id via getUpdates (negative for groups/channels)." },
      { id: "thread_id_confirmed", text: "For a topic-enabled group, confirm the message thread id." },
      { id: "send_test_photo", text: "Send test and confirm the photo and caption both arrive." },
    ],
  },
  {
    value: "whatsapp",
    label: "WhatsApp",
    items: [
      { id: "token_type_confirmed", text: "Confirm the access token type: temporary (24h) vs. a permanent System User token." },
      { id: "phone_number_id_confirmed", text: "Confirm the Phone number ID from API Setup." },
      { id: "recipient_allowed", text: "Add the recipient to the test-number allow list (development mode) as E.164 digits." },
      { id: "template_approved", text: "Confirm the image-header template's approval status and language code." },
      { id: "send_test_outside_window", text: "Send test via the template path from outside the 24-hour customer-service window." },
    ],
  },
];

/**
 * checklistByValue looks up a CHECKLISTS entry by its value, returning
 * undefined for a type with no checklist (e.g. "webhook").
 * @param {string} type
 * @returns {typeof CHECKLISTS[number]|undefined}
 */
export function checklistByValue(type) {
  return CHECKLISTS.find((c) => c.value === type);
}

/**
 * checklistStorageKey returns the localStorage key a channel's
 * checklist progress is stored under, namespaced by both platform type
 * and the saved channel's id (a draft/unsaved channel has no id yet —
 * callers should use "draft" for id in that case) so two channels of
 * the same platform track their checklists independently.
 * @param {string} type
 * @param {string|number} id
 * @returns {string}
 */
export function checklistStorageKey(type, id) {
  return `cloud-pulse:notify-checklist:${type}:${id}`;
}

/**
 * defaultChecklistProgress returns an empty progress object for type:
 * every item id mapped to false, plus a null lastTestSuccessAt.
 * Returns an object with no "items" key for a type with no checklist.
 * @param {string} type
 * @returns {{items: Object<string,boolean>, lastTestSuccessAt: number|null}}
 */
export function defaultChecklistProgress(type) {
  const checklist = checklistByValue(type);
  const items = {};
  if (checklist) {
    for (const item of checklist.items) {
      items[item.id] = false;
    }
  }
  return { items, lastTestSuccessAt: null };
}

/**
 * loadChecklistProgress reads and parses a channel's checklist
 * progress from storage (typically window.localStorage), merging in
 * any checklist item ids that didn't exist in the stored value yet
 * (e.g. after an app upgrade added a new item) as unchecked, and
 * dropping stored ids that no longer correspond to a current item.
 * Returns defaultChecklistProgress(type) if storage has nothing saved
 * yet, or if the saved value fails to parse as JSON.
 * @param {{getItem: (key: string) => string|null}} storage
 * @param {string} type
 * @param {string|number} id
 * @returns {{items: Object<string,boolean>, lastTestSuccessAt: number|null}}
 */
export function loadChecklistProgress(storage, type, id) {
  const fallback = defaultChecklistProgress(type);
  const raw = storage.getItem(checklistStorageKey(type, id));
  if (!raw) return fallback;

  let parsed;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return fallback;
  }
  if (!parsed || typeof parsed !== "object") return fallback;

  const items = {};
  for (const key of Object.keys(fallback.items)) {
    items[key] = Boolean(parsed.items && parsed.items[key]);
  }
  const lastTestSuccessAt =
    typeof parsed.lastTestSuccessAt === "number" && Number.isFinite(parsed.lastTestSuccessAt) ? parsed.lastTestSuccessAt : null;
  return { items, lastTestSuccessAt };
}

/**
 * saveChecklistProgress writes progress for a channel to storage
 * (typically window.localStorage) as JSON.
 * @param {{setItem: (key: string, value: string) => void}} storage
 * @param {string} type
 * @param {string|number} id
 * @param {{items: Object<string,boolean>, lastTestSuccessAt: number|null}} progress
 * @returns {void}
 */
export function saveChecklistProgress(storage, type, id, progress) {
  storage.setItem(checklistStorageKey(type, id), JSON.stringify(progress));
}

/**
 * checklistCompletionCount reports how many of a checklist's items are
 * checked in progress, and the total item count, e.g. {checked: 2,
 * total: 4}. Returns {checked: 0, total: 0} for a type with no
 * checklist.
 * @param {string} type
 * @param {{items: Object<string,boolean>}} progress
 * @returns {{checked: number, total: number}}
 */
export function checklistCompletionCount(type, progress) {
  const checklist = checklistByValue(type);
  if (!checklist) return { checked: 0, total: 0 };
  let checked = 0;
  for (const item of checklist.items) {
    if (progress.items && progress.items[item.id]) checked += 1;
  }
  return { checked, total: checklist.items.length };
}
