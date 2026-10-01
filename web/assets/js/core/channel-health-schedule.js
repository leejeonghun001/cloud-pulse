// channel-health-schedule.js — pure helpers converting between the
// UTC weekday/minute-of-day a channel's synthetic-test schedule is
// stored as (SPEC-v0.8 §3-2: "store UTC minute-of-day; display in the
// browser's local time") and the browser's local wall-clock
// equivalent shown in the channel dialog's weekday/time picker. No DOM
// access here — pages/settings/notifications-health.js is the sole
// consumer — so this is fully covered by `node --test` without a
// browser.
//
// Weekday convention matches JS's own Date.getDay()/getUTCDay():
// 0 = Sunday .. 6 = Saturday, identical to the server's
// models.ChannelHealthSettings.Weekday convention (see
// internal/storage/migrations/0007_v08.sql's 3-2 section).

/**
 * WEEKDAY_LABELS_KEYS lists the i18n key suffix for each weekday index
 * (0=Sunday..6=Saturday), for building a <select> of weekday options
 * via t(`health.weekday_${WEEKDAY_LABEL_KEYS[i]}`).
 */
export const WEEKDAY_LABEL_KEYS = ["sun", "mon", "tue", "wed", "thu", "fri", "sat"];

/**
 * utcToLocal converts a UTC (weekday, minuteOfDay) pair to the
 * equivalent local wall-clock (weekday, minuteOfDay) pair, using a
 * fixed reference date so the conversion is a pure function of its
 * inputs plus the browser's current UTC offset — not "today's date,"
 * which would make the result depend on when the function happens to
 * be called. The offset is taken from `referenceDate` (defaulting to
 * the current moment), matching how a real browser's Date object
 * reports the *current* UTC offset (DST-aware) at the time of
 * display; SPEC-v0.8 §3-2's storage contract is explicitly DST-
 * independent server-side, but the display conversion necessarily
 * reflects whatever offset is in effect for the admin viewing the
 * dialog right now, exactly like any other "shown in your local time"
 * UI convention (e.g. a calendar app).
 * @param {number} weekday 0-6 (Sunday-first), UTC
 * @param {number} minuteOfDay 0-1439, UTC
 * @param {Date} [referenceDate] used only for its UTC-offset-at-that-
 *   instant; defaults to `new Date()`.
 * @returns {{weekday: number, minuteOfDay: number}}
 */
export function utcToLocal(weekday, minuteOfDay, referenceDate = new Date()) {
  const utcMs = utcWeekdayMinuteToReferenceMs(weekday, minuteOfDay, referenceDate);
  const local = new Date(utcMs);
  return { weekday: local.getDay(), minuteOfDay: local.getHours() * 60 + local.getMinutes() };
}

/**
 * localToUtc is utcToLocal's inverse: converts a local wall-clock
 * (weekday, minuteOfDay) pair to the equivalent UTC pair for storage.
 * @param {number} weekday 0-6 (Sunday-first), local
 * @param {number} minuteOfDay 0-1439, local
 * @param {Date} [referenceDate]
 * @returns {{weekday: number, minuteOfDay: number}}
 */
export function localToUtc(weekday, minuteOfDay, referenceDate = new Date()) {
  const localMs = localWeekdayMinuteToReferenceMs(weekday, minuteOfDay, referenceDate);
  const utc = new Date(localMs);
  return { weekday: utc.getUTCDay(), minuteOfDay: utc.getUTCHours() * 60 + utc.getUTCMinutes() };
}

/**
 * utcWeekdayMinuteToReferenceMs builds an epoch-ms timestamp for
 * (weekday, minuteOfDay) UTC, anchored to the same UTC calendar week
 * as referenceDate (so the result is always within +/-3 days of "now,"
 * keeping the UTC offset used by utcToLocal's Date conversion
 * consistent with what a user would see today rather than some
 * arbitrary historical date whose offset might differ, e.g. across a
 * DST boundary that has since passed).
 */
function utcWeekdayMinuteToReferenceMs(weekday, minuteOfDay, referenceDate) {
  const refUtcDay = referenceDate.getUTCDay();
  const daysDelta = weekday - refUtcDay;
  const base = Date.UTC(referenceDate.getUTCFullYear(), referenceDate.getUTCMonth(), referenceDate.getUTCDate());
  return base + daysDelta * 86400000 + minuteOfDay * 60000;
}

/**
 * localWeekdayMinuteToReferenceMs is utcWeekdayMinuteToReferenceMs's
 * local-time equivalent, anchored to referenceDate's local calendar
 * week.
 */
function localWeekdayMinuteToReferenceMs(weekday, minuteOfDay, referenceDate) {
  const refLocalDay = referenceDate.getDay();
  const daysDelta = weekday - refLocalDay;
  const base = new Date(referenceDate.getFullYear(), referenceDate.getMonth(), referenceDate.getDate());
  return base.getTime() + daysDelta * 86400000 + minuteOfDay * 60000;
}

/**
 * formatMinuteOfDay renders a 0-1439 minute-of-day value as "HH:MM"
 * (24-hour, zero-padded) for a <input type="time"> value or a plain
 * text label.
 * @param {number} minuteOfDay
 * @returns {string}
 */
export function formatMinuteOfDay(minuteOfDay) {
  const h = Math.floor(minuteOfDay / 60) % 24;
  const m = minuteOfDay % 60;
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}`;
}

/**
 * parseMinuteOfDay parses an "HH:MM" string (as produced by an
 * <input type="time">) into a 0-1439 minute-of-day value, or null if
 * the string isn't well-formed.
 * @param {string} hhmm
 * @returns {number|null}
 */
export function parseMinuteOfDay(hhmm) {
  const match = /^(\d{1,2}):(\d{2})$/.exec(String(hhmm || "").trim());
  if (!match) return null;
  const h = Number(match[1]);
  const m = Number(match[2]);
  if (h < 0 || h > 23 || m < 0 || m > 59) return null;
  return h * 60 + m;
}
