// hosttable.js — pure helpers for the overview page's "All Systems"
// table: column definitions, per-row accessor values (for table.js's
// sortRows), free-text filtering, and threshold-based color levels for
// CPU/Memory/Disk bars. No DOM access here so these are covered by
// node --test without a browser environment; DOM building lives in
// pages/overview.js.

/**
 * @typedef {Object} ColumnDef
 * @property {string} key stable identifier, used for sort state + the
 *   columns dropdown's persisted visible-set.
 * @property {string} label column header text.
 * @property {boolean} sortable whether this column has a sort accessor.
 * @property {boolean} alwaysVisible true for columns the columns
 *   dropdown can't hide (System).
 */

/** COLUMNS is the full, ordered set of overview table columns. */
export const COLUMNS = [
  { key: "system", label: "System", sortable: true, alwaysVisible: true },
  { key: "cpu", label: "CPU", sortable: true, alwaysVisible: false },
  { key: "memory", label: "Memory", sortable: true, alwaysVisible: false },
  { key: "disk", label: "Disk", sortable: true, alwaysVisible: false },
  { key: "net", label: "Net", sortable: false, alwaysVisible: false },
  { key: "outbound", label: "Outbound (mo.)", sortable: true, alwaysVisible: false },
  { key: "inbound", label: "Inbound (mo.)", sortable: true, alwaysVisible: false },
  { key: "load", label: "Load", sortable: true, alwaysVisible: false },
  { key: "agent", label: "Agent", sortable: false, alwaysVisible: false },
  { key: "lastSeen", label: "Last seen", sortable: true, alwaysVisible: false },
];

/** DEFAULT_VISIBLE_COLUMNS is the initial columns-dropdown selection
 * (every column shown by default). */
export function defaultVisibleColumns() {
  return COLUMNS.map((c) => c.key);
}

/**
 * columnAccessors maps sortable column keys to a (row) => value
 * function for table.js's sortRows, operating directly on
 * models.HostSummary JSON rows.
 * @type {Object<string, (row: any) => (string|number)>}
 */
export const columnAccessors = {
  system: (row) => (row.host?.hostname || "").toLowerCase(),
  cpu: (row) => row.latest?.cpu_percent ?? -1,
  memory: (row) => row.latest?.mem_used_percent ?? -1,
  disk: (row) => row.latest?.disk_used_percent ?? -1,
  outbound: (row) => row.egress?.tx_bytes ?? 0,
  inbound: (row) => row.egress?.rx_bytes ?? 0,
  load: (row) => row.latest?.load1 ?? -1,
  lastSeen: (row) => row.last_seen ?? 0,
};

/**
 * thresholdLevel classifies a percent value into a color level using
 * the spec's thresholds: green < 65%, amber < 90%, red >= 90%.
 * @param {number} percent
 * @returns {"ok"|"warning"|"critical"}
 */
export function thresholdLevel(percent) {
  const n = Number(percent);
  if (!Number.isFinite(n) || n < 65) return "ok";
  if (n < 90) return "warning";
  return "critical";
}

/**
 * thresholdBarClass maps thresholdLevel's result to a cp-bar-* class
 * used by ui/components.js's progressBar.
 * @param {number} percent
 * @returns {string}
 */
export function thresholdBarClass(percent) {
  switch (thresholdLevel(percent)) {
    case "warning":
      return "cp-bar-warning";
    case "critical":
      return "cp-bar-critical";
    default:
      return "cp-bar-ok";
  }
}

/**
 * matchesFilter reports whether a host row matches a free-text filter
 * query, case-insensitively, against hostname, host id, provider, and
 * status. An empty/whitespace query matches everything.
 * @param {any} row models.HostSummary JSON
 * @param {string} query
 * @returns {boolean}
 */
export function matchesFilter(row, query) {
  const q = String(query ?? "").trim().toLowerCase();
  if (q === "") return true;
  const haystack = [row.host?.hostname, row.host?.id, row.host?.provider, row.status, row.host?.os, row.host?.arch]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
  return haystack.includes(q);
}

/**
 * filterRows returns the subset of rows matching a free-text query.
 * @param {Array} rows
 * @param {string} query
 * @returns {Array}
 */
export function filterRows(rows, query) {
  return rows.filter((r) => matchesFilter(r, query));
}

/**
 * agentDotLevel decides the agent-version status dot color for a host
 * summary's `update` field: green ("ok") when current/unknown, amber
 * ("warning") when a newer version is available.
 * @param {{available?: boolean}|null|undefined} update
 * @returns {"ok"|"warning"}
 */
export function agentDotLevel(update) {
  return update?.available ? "warning" : "ok";
}

/**
 * sortStorageKey / columnsStorageKey are the localStorage keys used to
 * persist the overview table's sort state, visible-columns set, and
 * view mode (table|grid) across page loads.
 */
export const SORT_STORAGE_KEY = "cp_overview_sort";
export const COLUMNS_STORAGE_KEY = "cp_overview_columns";
export const VIEW_STORAGE_KEY = "cp_overview_view";

/**
 * normalizeVisibleColumns filters a persisted columns array down to
 * currently-known, non-always-visible keys, falling back to the full
 * default set when the input is empty/invalid (e.g. corrupted storage,
 * or a column removed in a later release).
 * @param {any} persisted
 * @returns {string[]}
 */
export function normalizeVisibleColumns(persisted) {
  if (!Array.isArray(persisted)) return defaultVisibleColumns();
  const known = new Set(COLUMNS.map((c) => c.key));
  const filtered = persisted.filter((k) => known.has(k));
  if (filtered.length === 0) return defaultVisibleColumns();
  // Always-visible columns are implicitly included regardless of what
  // was persisted.
  const alwaysOn = COLUMNS.filter((c) => c.alwaysVisible).map((c) => c.key);
  return Array.from(new Set([...alwaysOn, ...filtered]));
}
