// format.js — pure formatting helpers for the cloud-pulse dashboard.
// No DOM access here; every function takes primitives and returns a
// string or number so it can be unit-tested with node --test without a
// browser environment.

/** Binary (IEC) byte unit suffixes, ascending from bytes. */
const IEC_UNITS = ["B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"];

/**
 * clamp restricts n to the inclusive range [min, max].
 * @param {number} n
 * @param {number} min
 * @param {number} max
 * @returns {number}
 */
export function clamp(n, min, max) {
  if (Number.isNaN(n)) return min;
  if (n < min) return min;
  if (n > max) return max;
  return n;
}

/**
 * formatBytes renders a byte count using IEC binary units (1024-based),
 * e.g. formatBytes(1610612736) -> "1.5 GiB". Negative/NaN input is
 * treated as 0.
 * @param {number} bytes
 * @param {number} [decimals=1]
 * @returns {string}
 */
export function formatBytes(bytes, decimals = 1) {
  let n = Number(bytes);
  if (!Number.isFinite(n) || n < 0) n = 0;
  if (n < 1024) return `${Math.round(n)} ${IEC_UNITS[0]}`;

  let unitIndex = 0;
  let value = n;
  while (value >= 1024 && unitIndex < IEC_UNITS.length - 1) {
    value /= 1024;
    unitIndex++;
  }
  return `${value.toFixed(decimals)} ${IEC_UNITS[unitIndex]}`;
}

/**
 * formatBitrate renders a bytes-per-second rate as a human bitrate string
 * using IEC-style binary byte units per second, e.g. "1.2 MiB/s". Mirrors
 * formatBytes so dashboard rate displays are visually consistent with
 * byte totals.
 * @param {number} bytesPerSecond
 * @param {number} [decimals=1]
 * @returns {string}
 */
export function formatBitrate(bytesPerSecond, decimals = 1) {
  return `${formatBytes(bytesPerSecond, decimals)}/s`;
}

/**
 * formatPercent renders a percentage value with fixed decimals, clamped
 * to [0, 100] by default (pass allowOver100 to skip the upper clamp for
 * values like projected egress that may legitimately exceed 100%).
 * @param {number} percent
 * @param {number} [decimals=1]
 * @param {boolean} [allowOver100=false]
 * @returns {string}
 */
export function formatPercent(percent, decimals = 1, allowOver100 = false) {
  let n = Number(percent);
  if (!Number.isFinite(n)) n = 0;
  const max = allowOver100 ? Number.POSITIVE_INFINITY : 100;
  n = clamp(n, 0, max);
  return `${n.toFixed(decimals)}%`;
}

/**
 * formatDuration renders a duration given in seconds as a compact
 * human string, e.g. 90061 -> "1d 1h 1m". Shows at most two units for
 * brevity; zero or negative input renders "0s".
 * @param {number} totalSeconds
 * @returns {string}
 */
export function formatDuration(totalSeconds) {
  let s = Math.floor(Number(totalSeconds));
  if (!Number.isFinite(s) || s <= 0) return "0s";

  const days = Math.floor(s / 86400);
  s -= days * 86400;
  const hours = Math.floor(s / 3600);
  s -= hours * 3600;
  const minutes = Math.floor(s / 60);
  s -= minutes * 60;
  const seconds = s;

  const parts = [];
  if (days > 0) parts.push(`${days}d`);
  if (hours > 0) parts.push(`${hours}h`);
  if (minutes > 0 && days === 0) parts.push(`${minutes}m`);
  if (days === 0 && hours === 0) parts.push(`${seconds}s`);

  return parts.slice(0, 2).join(" ") || "0s";
}

/**
 * formatUptime is an alias of formatDuration kept as a distinct export
 * for call-site clarity (host uptime vs. generic durations).
 * @param {number} totalSeconds
 * @returns {string}
 */
export function formatUptime(totalSeconds) {
  return formatDuration(totalSeconds);
}

/**
 * formatRelativeTime renders the difference between nowMs and thenMs as
 * a compact "x ago"/"in x" string, e.g. "5s ago", "3m ago", "2h ago",
 * "4d ago", "just now" for sub-second differences.
 * @param {number} thenMs epoch milliseconds
 * @param {number} [nowMs=Date.now()] epoch milliseconds
 * @returns {string}
 */
export function formatRelativeTime(thenMs, nowMs = Date.now()) {
  const diffMs = nowMs - thenMs;
  const future = diffMs < 0;
  const absSeconds = Math.floor(Math.abs(diffMs) / 1000);

  if (absSeconds < 1) return "just now";

  let value;
  let unit;
  if (absSeconds < 60) {
    value = absSeconds;
    unit = "s";
  } else if (absSeconds < 3600) {
    value = Math.floor(absSeconds / 60);
    unit = "m";
  } else if (absSeconds < 86400) {
    value = Math.floor(absSeconds / 3600);
    unit = "h";
  } else {
    value = Math.floor(absSeconds / 86400);
    unit = "d";
  }

  return future ? `in ${value}${unit}` : `${value}${unit} ago`;
}

/**
 * formatRelativeTimeFromUnixSeconds is a convenience wrapper around
 * formatRelativeTime for unix-second timestamps (as returned by the hub
 * API), avoiding repeated *1000 conversions at call sites.
 * @param {number} unixSeconds
 * @param {number} [nowMs=Date.now()]
 * @returns {string}
 */
export function formatRelativeTimeFromUnixSeconds(unixSeconds, nowMs = Date.now()) {
  return formatRelativeTime(unixSeconds * 1000, nowMs);
}

/**
 * formatNumber renders an integer count with thousands separators using
 * the browser's locale, e.g. 1234567 -> "1,234,567" (locale-dependent
 * separator). Falls back to a plain string for non-finite input.
 * @param {number} n
 * @returns {string}
 */
export function formatNumber(n) {
  if (!Number.isFinite(n)) return String(n);
  return Math.round(n).toLocaleString();
}

/**
 * formatLoad renders a load-average value with 2 decimal places, e.g.
 * 0.5 -> "0.50".
 * @param {number} load
 * @returns {string}
 */
export function formatLoad(load) {
  const n = Number(load);
  if (!Number.isFinite(n)) return "0.00";
  return n.toFixed(2);
}
