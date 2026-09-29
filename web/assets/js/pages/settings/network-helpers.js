// network-helpers.js — pure helpers for the Network settings section:
// building the desired NetworkConfig from the adapters UI's checkbox
// state, diffing two configs for the confirm dialog, validating an
// allowlist CIDR/IP entry client-side (mirrors the hub's
// config.ParseAllowedCIDRsList/validateNetworkRequest rules closely
// enough to give instant feedback, but the hub is always the
// authority — every mutation still round-trips through PUT/DELETE
// /api/v1/settings/network), and rendering a live countdown string for
// the pending-change banner. No DOM access, so these are unit-testable
// with `node --test` without a browser.

/**
 * isUnspecifiedOrMulticast reports whether an IP string, if parseable,
 * is an unspecified ("0.0.0.0"/"::") or multicast address — both
 * rejected by the hub's PUT /api/v1/settings/network validation for
 * "custom" mode addresses. Returns false for anything unparseable (the
 * hub's own parse step is the source of truth for "is this a valid
 * IP at all"; this only adds the extra semantic checks).
 * @param {string} ip
 * @returns {boolean}
 */
export function isUnspecifiedOrMulticast(ip) {
  const v = ip.trim();
  if (v === "0.0.0.0" || v === "::" || v === "0:0:0:0:0:0:0:0") return true;
  // IPv4 multicast: 224.0.0.0/4 (first octet 224-239).
  const v4 = v.match(/^(\d{1,3})\.\d{1,3}\.\d{1,3}\.\d{1,3}$/);
  if (v4) {
    const first = Number(v4[1]);
    return first >= 224 && first <= 239;
  }
  // IPv6 multicast: ff00::/8.
  if (/^ff/i.test(v)) return true;
  return false;
}

/**
 * looksLikeIP does a permissive syntactic check for an IPv4 or bracket-
 * free IPv6 literal — good enough for enabling/disabling a submit
 * button instantly; the hub still re-validates authoritatively.
 * @param {string} ip
 * @returns {boolean}
 */
export function looksLikeIP(ip) {
  const v = ip.trim();
  if (!v) return false;
  if (/^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(v)) {
    return v.split(".").every((seg) => Number(seg) <= 255);
  }
  // Permissive IPv6 check: hex groups and at most one "::".
  return /^[0-9a-fA-F:]+$/.test(v) && v.includes(":");
}

/**
 * validateCustomAddress validates one address entry for "custom" mode,
 * returning an error string ("" when valid). Mirrors
 * validateNetworkRequest's per-address checks in
 * internal/hub/networkroutes.go.
 * @param {string} ip
 * @returns {string}
 */
export function validateCustomAddress(ip) {
  const v = ip.trim();
  if (!v) return "Address is required.";
  if (!looksLikeIP(v)) return "Not a valid IP address.";
  if (isUnspecifiedOrMulticast(v)) return "Address must not be unspecified or multicast.";
  return "";
}

/**
 * validatePort validates the port field. Mirrors minNetworkPort/
 * maxNetworkPort (1024..65535) in internal/hub/networkroutes.go.
 * @param {number|string} port
 * @returns {string} error string, "" when valid
 */
export function validatePort(port) {
  const n = Number(port);
  if (!Number.isInteger(n)) return "Port must be a whole number.";
  if (n < 1024 || n > 65535) return "Port must be between 1024 and 65535.";
  return "";
}

/**
 * validateCIDREntry validates one allowlist entry ("*" or a bare
 * IP/CIDR). Purely syntactic — the hub's config.ParseAllowedCIDRsList
 * is authoritative.
 * @param {string} entry
 * @returns {string} error string, "" when valid
 */
export function validateCIDREntry(entry) {
  const v = entry.trim();
  if (!v) return "Entry is required.";
  if (v === "*") return "";
  const parts = v.split("/");
  if (parts.length > 2) return "Not a valid CIDR.";
  const ip = parts[0];
  if (!looksLikeIP(ip)) return "Not a valid IP or CIDR.";
  if (parts.length === 2) {
    const prefix = Number(parts[1]);
    const maxPrefix = ip.includes(":") ? 128 : 32;
    if (!Number.isInteger(prefix) || prefix < 0 || prefix > maxPrefix) {
      return `Prefix length must be between 0 and ${maxPrefix}.`;
    }
  }
  return "";
}

/**
 * normalizeCIDRList validates a whole allowlist per the hub's "*"-alone
 * rule (validateNetworkRequest: if any entry is "*", it must be the
 * only entry) and returns {ok, error} without mutating the input.
 * @param {string[]} entries
 * @returns {{ok: boolean, error: string}}
 */
export function normalizeCIDRList(entries) {
  if (entries.length === 0) return { ok: false, error: "At least one allowlist entry is required." };
  const stars = entries.filter((e) => e === "*").length;
  if (stars > 0 && entries.length > 1) {
    return { ok: false, error: '"*" must be the only entry when present.' };
  }
  for (const e of entries) {
    const err = validateCIDREntry(e);
    if (err) return { ok: false, error: `"${e}": ${err}` };
  }
  return { ok: true, error: "" };
}

/**
 * buildNetworkConfigFromSelection builds the putNetworkRequest body
 * from the Network page's UI state: whether "all interfaces" is
 * checked, the set of individually-checked addresses otherwise, the
 * port, and the current allowlist chip list.
 * @param {Object} opts
 * @param {boolean} opts.allInterfaces
 * @param {string[]} opts.selectedAddresses
 * @param {number} opts.port
 * @param {string[]} opts.allowedCIDRs
 * @returns {{mode: "all"|"custom", addresses: string[], port: number, allowed_cidrs: string[]}}
 */
export function buildNetworkConfigFromSelection({ allInterfaces, selectedAddresses, port, allowedCIDRs }) {
  return {
    mode: allInterfaces ? "all" : "custom",
    addresses: allInterfaces ? [] : selectedAddresses.slice(),
    port: Number(port),
    allowed_cidrs: allowedCIDRs.slice(),
  };
}

/**
 * describeNetworkConfig renders a NetworkConfig as a short human string
 * for the confirm dialog's diff, e.g. "All interfaces, port 8090,
 * allow: 100.64.0.0/10, 127.0.0.0/8" or "192.168.100.2, 127.0.0.1, port
 * 8090, allow: *".
 * @param {{mode: string, addresses: string[], port: number, allowed_cidrs: string[]}} cfg
 * @returns {string}
 */
export function describeNetworkConfig(cfg) {
  const listen = cfg.mode === "all" ? "All interfaces" : (cfg.addresses || []).join(", ") || "(no addresses)";
  const allow = (cfg.allowed_cidrs || []).join(", ") || "(none)";
  return `${listen} · port ${cfg.port} · allow: ${allow}`;
}

/**
 * diffNetworkConfig computes a field-by-field diff between two
 * NetworkConfig-shaped objects for the confirm dialog, returning an
 * array of {field, from, to} entries for fields that actually changed
 * (order: mode, addresses, port, allowed_cidrs). Array fields are
 * compared as their joined string representation for both the equality
 * check and the display value.
 * @param {Object} previous
 * @param {Object} next
 * @returns {{field: string, from: string, to: string}[]}
 */
export function diffNetworkConfig(previous, next) {
  const fields = [
    { key: "mode", label: "Listen mode", get: (c) => (c.mode === "all" ? "All interfaces" : "Custom") },
    { key: "addresses", label: "Addresses", get: (c) => (c.mode === "all" ? "—" : (c.addresses || []).join(", ") || "(none)") },
    { key: "port", label: "Port", get: (c) => String(c.port) },
    { key: "allowed_cidrs", label: "Allowlist", get: (c) => (c.allowed_cidrs || []).join(", ") || "(none)" },
  ];
  const out = [];
  for (const f of fields) {
    const from = f.get(previous);
    const to = f.get(next);
    if (from !== to) out.push({ field: f.label, from, to });
  }
  return out;
}

/**
 * suggestionsForInterfaces collects de-duplicated {label, cidr} entries
 * for every non-link-local address across a NetInterface[] list, for
 * the allowlist "one-click suggestion" chips (e.g. "Allow
 * 192.168.100.0/24" per adapter). Loopback/link-local scoped addresses
 * are skipped (loopback is already covered by the default allowlist;
 * link-local addresses aren't meaningfully allowlist-able per-adapter).
 * @param {Array} interfaces models.NetInterface[]
 * @returns {{label: string, cidr: string, interfaceName: string}[]}
 */
export function suggestionsForInterfaces(interfaces) {
  const seen = new Set();
  const out = [];
  for (const iface of interfaces || []) {
    for (const addr of iface.addresses || []) {
      if (addr.scope === "link-local") continue;
      const cidr = addr.suggested_cidr || addr.network;
      if (!cidr || seen.has(cidr)) continue;
      seen.add(cidr);
      out.push({ label: `Allow ${cidr} (${iface.name})`, cidr, interfaceName: iface.name });
    }
  }
  return out;
}

/**
 * isAllowlistCovering reports whether cidrList already contains entry
 * verbatim, or already contains "*" (covers everything). Used to gray
 * out/hide a suggestion chip that's already been added.
 * @param {string[]} cidrList
 * @param {string} entry
 * @returns {boolean}
 */
export function isAllowlistCovering(cidrList, entry) {
  return cidrList.includes("*") || cidrList.includes(entry);
}

/**
 * formatCountdown renders the seconds remaining until a pending
 * network change's deadline as "Xm Ys" (or "Ys" under a minute, or
 * "expired" at/after zero).
 * @param {number} deadlineUnixSeconds
 * @param {number} [nowUnixSeconds] defaults to the real current time
 * @returns {string}
 */
export function formatCountdown(deadlineUnixSeconds, nowUnixSeconds = Math.floor(Date.now() / 1000)) {
  const remaining = Math.max(0, Math.floor(deadlineUnixSeconds - nowUnixSeconds));
  if (remaining <= 0) return "expired";
  const m = Math.floor(remaining / 60);
  const s = remaining % 60;
  return m > 0 ? `${m}m ${s}s` : `${s}s`;
}

/**
 * ERROR_MESSAGES maps the hub's Network settings API error codes
 * (models.APIError.Code) to a clear, user-facing sentence, for the
 * Network page and its confirm dialog. Falls back to the raw `error`
 * message when a code isn't in this map.
 * @type {Object<string, string>}
 */
export const ERROR_MESSAGES = {
  would_lock_out:
    "That allowlist would exclude your own address — you'd be locked out immediately, so the change was not applied.",
  bind_failed:
    "The hub couldn't open a listener on any of the requested addresses. Check that the address/port is available and try again.",
  change_pending:
    "A network change is already pending confirmation. Confirm or revert it before starting another change.",
  no_pending: "There is no pending network change to confirm or revert.",
};

/**
 * describeNetworkError renders a clear message for a thrown ApiError
 * (or any {code, message} shape), using ERROR_MESSAGES when the code is
 * known.
 * @param {{code?: string, message?: string}} err
 * @returns {string}
 */
export function describeNetworkError(err) {
  const code = err?.code || "";
  if (code && ERROR_MESSAGES[code]) return ERROR_MESSAGES[code];
  return err?.message || "Network settings change failed.";
}
