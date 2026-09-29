// store.js — thin, safe localStorage wrapper used for small UI
// preferences (theme lives in theme.js; session token lives in
// auth.js). Every read/write is wrapped in try/catch so private-mode or
// storage-disabled browsers degrade to "preference doesn't persist"
// rather than throwing.

/**
 * getItem reads a raw string value, or fallback if unset/unavailable.
 * @param {string} key
 * @param {string} [fallback]
 * @returns {string|undefined}
 */
export function getItem(key, fallback) {
  try {
    const v = globalThis.localStorage?.getItem(key);
    return v === null || v === undefined ? fallback : v;
  } catch {
    return fallback;
  }
}

/**
 * setItem writes a raw string value. No-ops silently on failure.
 * @param {string} key
 * @param {string} value
 */
export function setItem(key, value) {
  try {
    globalThis.localStorage?.setItem(key, value);
  } catch {
    // Unavailable storage: the preference just won't persist.
  }
}

/**
 * removeItem deletes a stored key. No-ops silently on failure.
 * @param {string} key
 */
export function removeItem(key) {
  try {
    globalThis.localStorage?.removeItem(key);
  } catch {
    // Unavailable storage: nothing to remove anyway.
  }
}

/**
 * getJSON reads and parses a JSON value, returning fallback on any
 * missing key, storage error, or parse error.
 * @param {string} key
 * @param {*} fallback
 * @returns {*}
 */
export function getJSON(key, fallback) {
  const raw = getItem(key);
  if (raw === undefined) return fallback;
  try {
    return JSON.parse(raw);
  } catch {
    return fallback;
  }
}

/**
 * setJSON serializes and writes a JSON value. No-ops silently on
 * failure (including a value that can't be serialized).
 * @param {string} key
 * @param {*} value
 */
export function setJSON(key, value) {
  try {
    setItem(key, JSON.stringify(value));
  } catch {
    // Non-serializable value: nothing sane to persist.
  }
}
