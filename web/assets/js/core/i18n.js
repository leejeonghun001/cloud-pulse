// i18n.js — dashboard language preference (auto|en|ko) persisted to
// localStorage "cp_lang", plus the pure t(key, params) lookup used by
// every user-visible string in the dashboard (SPEC-v0.8 §3-3). Mirrors
// theme.js's persistence pattern; unlike theme.js, the active catalog
// itself must be loaded (setCatalogs) before t() can resolve anything
// — main.js does this at boot, before first render (see this file's
// module doc for the full contract).
//
// Catalog shape: a nested object keyed by namespace (e.g.
// {"common": {"cancel": "Cancel"}, "nav": {"overview": "Overview"}}),
// loaded from web/assets/i18n/en.json and ko.json. t() flattens lookups
// via dot-separated keys ("common.cancel"), matching the server-side
// internal/i18n package's flattening convention so the two stay
// conceptually aligned even though they're loaded independently.
//
// Fallback contract (must match internal/i18n.T exactly, see that
// package's doc comment): missing in the active language falls back to
// English; missing everywhere returns the bare key.
//
// Simple plural form: a params object with a numeric `count` selects
// between a key's "one" (count === 1) and "other" (any other count)
// sub-keys, e.g. catalog entry {"host_count": {"one": "{count} host",
// "other": "{count} hosts"}}. This is intentionally simpler than full
// CLDR plural rules (which Korean doesn't need — Korean has no
// grammatical plural distinction, so ko.json's "one"/"other" pair is
// typically identical text) but covers English's one/other split.
import { getItem, setItem } from "./store.js";

const STORAGE_KEY = "cp_lang";

/** @typedef {"auto"|"en"|"ko"} LangPreference */
/** @typedef {"en"|"ko"} ResolvedLang */

const SUPPORTED_LANGS = ["en", "ko"];
const FALLBACK_LANG = "en";

/** catalogs holds the currently loaded language catalogs, keyed by
 * resolved language ("en"/"ko"). Populated once at boot by
 * setCatalogs(); empty catalogs make t() fall through to returning the
 * bare key, which is a safe (if unlocalized) degradation rather than a
 * throw, so a page that renders before boot finishes (shouldn't happen
 * given main.js's ordering, but defensively) doesn't crash. */
let catalogs = { en: {}, ko: {} };
const catalogLoads = new Map();

/**
 * loadCatalogs fetches only the effective language's catalog at boot.
 * The other catalog is fetched on demand by ensureCatalog(), keeping an
 * initial English and Korean visit from paying for both translations.
 * @returns {Promise<void>}
 */
export async function loadCatalogs() {
  await ensureCatalog(getLang());
}

/**
 * ensureCatalog loads a language catalog once. Language controls await this
 * before rerendering, so a switch never briefly renders untranslated keys.
 * @param {ResolvedLang} lang
 * @returns {Promise<void>}
 */
export function ensureCatalog(lang) {
  if (Object.keys(catalogs[lang] || {}).length) return Promise.resolve();
  if (catalogLoads.has(lang)) return catalogLoads.get(lang);
  const load = fetch(`/assets/i18n/${lang}.json`)
    .then((response) => {
      if (!response.ok) throw new Error(`catalog request failed: ${response.status}`);
      return response.json();
    })
    .then((catalog) => {
      catalogs = { ...catalogs, [lang]: flatten(catalog || {}) };
    })
    .finally(() => catalogLoads.delete(lang));
  catalogLoads.set(lang, load);
  return load;
}

/**
 * setCatalogs installs the loaded catalogs. Call once during boot,
 * before mounting any page. Each catalog is a nested namespace object
 * as described in this file's header comment; this function flattens
 * it internally.
 * @param {{en: Object, ko: Object}} loaded
 */
export function setCatalogs(loaded) {
  catalogs = {
    en: flatten(loaded.en || {}),
    ko: flatten(loaded.ko || {}),
  };
  catalogLoads.clear();
}

/**
 * flatten converts a nested namespace object into a flat map of
 * dot-separated keys to either a string template or a
 * {one, other} plural map (the only two leaf shapes a catalog may
 * contain).
 * @param {Object} tree
 * @returns {Object<string, string|{one:string,other:string}>}
 */
function flatten(tree, prefix = "") {
  const out = {};
  for (const [k, v] of Object.entries(tree)) {
    const key = prefix ? `${prefix}.${k}` : k;
    if (typeof v === "string") {
      out[key] = v;
    } else if (v && typeof v === "object" && (typeof v.one === "string" || typeof v.other === "string")) {
      out[key] = v;
    } else if (v && typeof v === "object") {
      Object.assign(out, flatten(v, key));
    }
  }
  return out;
}

/**
 * getLangPreference returns the persisted language preference,
 * defaulting to "auto" when unset or invalid.
 * @returns {LangPreference}
 */
export function getLangPreference() {
  const v = getItem(STORAGE_KEY);
  if (v === "auto" || v === "en" || v === "ko") return v;
  return "auto";
}

/**
 * setLang persists pref as the language preference. Does not itself
 * reload the catalog or re-render — callers apply the change (see
 * main.js's language selector wiring for the "re-render without a full
 * reload when easy, otherwise reload" policy).
 * @param {LangPreference} pref
 */
export function setLang(pref) {
  setItem(STORAGE_KEY, pref);
}

/**
 * detectAutoLang resolves "auto" against navigator.language, returning
 * "ko" for any Korean variant (ko, ko-KR, ...) and "en" otherwise —
 * English is the fallback for every locale this dashboard doesn't yet
 * ship a catalog for, not just unset/unknown ones.
 * @returns {ResolvedLang}
 */
export function detectAutoLang() {
  try {
    const nav = globalThis.navigator?.language || "";
    if (nav.toLowerCase().startsWith("ko")) return "ko";
  } catch {
    // navigator unavailable (non-browser test environment): fall
    // through to the English default.
  }
  return FALLBACK_LANG;
}

/**
 * getLang resolves the current effective language: the persisted
 * preference if it's a concrete language, or detectAutoLang() when the
 * preference is "auto".
 * @returns {ResolvedLang}
 */
export function getLang() {
  const pref = getLangPreference();
  return resolveLangPreference(pref);
}

/** Resolves a candidate preference before it is persisted. Language controls
 * use this to load the destination catalog without exposing untranslated keys
 * during a failed switch. */
export function resolveLangPreference(pref) {
  if (pref === "auto") return detectAutoLang();
  return pref;
}

/** SUPPORTED_LANGUAGES lists every resolved language this dashboard
 * ships a catalog for, for the language selector to enumerate. */
export const SUPPORTED_LANGUAGES = SUPPORTED_LANGS;

/**
 * t resolves key (dot-separated namespace path, e.g. "nav.overview")
 * against the current language's catalog (getLang()), substituting
 * {name}-style placeholders from params. If params has a numeric
 * `count`, and the resolved catalog entry is a {one, other} plural map
 * rather than a plain string, "one" is used when count === 1 and
 * "one" is absent from neither language for grammatical-number-free
 * languages like Korean (ko.json may repeat the same text under both
 * sub-keys) — the shape is still {one, other} for catalog-parity
 * purposes.
 *
 * Fallback order: current language -> English -> the bare key.
 * @param {string} key
 * @param {Object<string, string|number>} [params]
 * @returns {string}
 */
export function t(key, params) {
  const lang = getLang();
  let entry = catalogs[lang]?.[key];
  if (entry === undefined && lang !== FALLBACK_LANG && !Object.keys(catalogs[FALLBACK_LANG] || {}).length) {
    // Catalog parity normally makes this path unnecessary. If a newer
    // Korean catalog temporarily lacks a key, start loading the English
    // fallback without blocking a synchronous render.
    void ensureCatalog(FALLBACK_LANG);
  }
  if (entry === undefined) entry = catalogs[FALLBACK_LANG]?.[key];
  if (entry === undefined) return key;

  let template = entry;
  if (typeof entry === "object") {
    const count = params && typeof params.count === "number" ? params.count : undefined;
    template = count === 1 ? entry.one : entry.other;
    if (template === undefined) template = entry.other ?? entry.one ?? key;
  }
  return applyParams(String(template), params);
}

/**
 * applyParams substitutes {name} placeholders in template from params.
 * A placeholder with no matching param is left as-is (matches
 * internal/i18n's applyParams behavior, so a missing param is visibly
 * obvious rather than silently vanishing).
 * @param {string} template
 * @param {Object<string, string|number>} [params]
 * @returns {string}
 */
function applyParams(template, params) {
  if (!params) return template;
  return template.replace(/\{([a-zA-Z0-9_]+)\}/g, (match, name) => {
    if (Object.prototype.hasOwnProperty.call(params, name)) return String(params[name]);
    return match;
  });
}

/**
 * htmlLang returns the BCP-47 language tag t()'s current language
 * should be reflected as on <html lang="...">.
 * @returns {string}
 */
export function htmlLang() {
  return getLang() === "ko" ? "ko" : "en";
}

/**
 * formatNumberLocalized formats n with the current language's Intl
 * locale conventions (thousands separators etc.), falling back to a
 * plain string on an unsupported environment.
 * @param {number} n
 * @param {Intl.NumberFormatOptions} [options]
 * @returns {string}
 */
export function formatNumberLocalized(n, options) {
  try {
    return new Intl.NumberFormat(intlLocale(), options).format(n);
  } catch {
    return String(n);
  }
}

/**
 * formatDateTimeLocalized formats a Date (or epoch-ms number) with the
 * current language's Intl locale conventions.
 * @param {Date|number} value
 * @param {Intl.DateTimeFormatOptions} [options]
 * @returns {string}
 */
export function formatDateTimeLocalized(value, options) {
  const date = value instanceof Date ? value : new Date(value);
  try {
    return new Intl.DateTimeFormat(intlLocale(), options).format(date);
  } catch {
    return date.toISOString();
  }
}

/**
 * formatRelativeTimeLocalized formats the signed difference (value -
 * to, in the given unit) as a localized relative-time string (e.g. "3
 * minutes ago" / "3분 전").
 * @param {number} value
 * @param {Intl.RelativeTimeFormatUnit} unit
 * @param {Intl.RelativeTimeFormatOptions} [options]
 * @returns {string}
 */
export function formatRelativeTimeLocalized(value, unit, options) {
  try {
    return new Intl.RelativeTimeFormat(intlLocale(), options).format(value, unit);
  } catch {
    return `${value} ${unit}`;
  }
}

/** intlLocale maps the resolved language to a BCP-47 locale tag for
 * the Intl.* constructors above. */
function intlLocale() {
  return getLang() === "ko" ? "ko-KR" : "en-US";
}
