// egress.js — builds the overview page's per-host monthly egress
// section: a segmented Outbound/Inbound control, a month selector
// (prev/next + <input type=month>), and a list of per-host egress rows
// with progress bars and level chips for the selected direction (with
// the other direction's total shown as secondary text).
import { el, progressBar, egressLevelChip, levelClassForBar, hubOverrideBadge } from "../ui/components.js";
import { formatBytes } from "../core/format.js";

/** DIRECTION_STORAGE_KEY is the localStorage key remembering the last
 * selected egress direction across page loads. */
const DIRECTION_STORAGE_KEY = "cp_egress_dir";

/**
 * shiftMonth returns the "YYYY-MM" string one month before/after month.
 * @param {string} month "YYYY-MM"
 * @param {number} delta -1 or +1
 * @returns {string}
 */
export function shiftMonth(month, delta) {
  const [y, m] = month.split("-").map(Number);
  const d = new Date(Date.UTC(y, m - 1 + delta, 1));
  const yy = d.getUTCFullYear();
  const mm = String(d.getUTCMonth() + 1).padStart(2, "0");
  return `${yy}-${mm}`;
}

/**
 * currentMonth returns the current UTC calendar month as "YYYY-MM".
 * @returns {string}
 */
export function currentMonth() {
  const now = new Date();
  return `${now.getUTCFullYear()}-${String(now.getUTCMonth() + 1).padStart(2, "0")}`;
}

/**
 * getStoredDirection returns the last-remembered egress direction
 * ("out"|"in") from localStorage, defaulting to "out" when unset or
 * unavailable (private mode, disabled storage).
 * @returns {"out"|"in"}
 */
export function getStoredDirection() {
  try {
    const v = globalThis.localStorage?.getItem(DIRECTION_STORAGE_KEY);
    return v === "in" ? "in" : "out";
  } catch {
    return "out";
  }
}

/**
 * setStoredDirection persists dir ("out"|"in") to localStorage so the
 * egress section remembers the user's last choice across page loads.
 * Silently no-ops if localStorage is unavailable.
 * @param {"out"|"in"} dir
 */
export function setStoredDirection(dir) {
  try {
    globalThis.localStorage?.setItem(DIRECTION_STORAGE_KEY, dir);
  } catch {
    // No persistence available; the in-memory selection for this page
    // load still works.
  }
}

/**
 * buildEgressSection renders the direction segmented control, month
 * selector, and per-host list.
 * @param {Object} opts
 * @param {string} opts.month "YYYY-MM"
 * @param {"out"|"in"} opts.direction
 * @param {Array} opts.hosts entries from GET /api/v1/egress: {host_id, hostname, egress}
 * @param {(month: string) => void} opts.onMonthChange
 * @param {(direction: "out"|"in") => void} opts.onDirectionChange
 * @returns {HTMLElement}
 */
export function buildEgressSection({ month, direction, hosts, onMonthChange, onDirectionChange }) {
  const section = el("section", { class: "cp-section", attrs: { "aria-labelledby": "cp-egress-heading" } });
  section.append(el("h2", { id: "cp-egress-heading", text: "Monthly egress" }));

  const topRow = el("div", { class: "cp-egress-top-row" });

  const segmented = el("div", {
    class: "cp-segmented",
    attrs: { role: "group", "aria-label": "Egress direction" },
  });
  const outBtn = el("button", {
    class: "cp-btn cp-segmented-btn",
    attrs: { type: "button", "aria-pressed": direction === "out" ? "true" : "false" },
    text: "Outbound",
  });
  const inBtn = el("button", {
    class: "cp-btn cp-segmented-btn",
    attrs: { type: "button", "aria-pressed": direction === "in" ? "true" : "false" },
    text: "Inbound",
  });
  outBtn.addEventListener("click", () => onDirectionChange("out"));
  inBtn.addEventListener("click", () => onDirectionChange("in"));
  segmented.append(outBtn, inBtn);
  topRow.append(segmented);

  const controls = el("div", { class: "cp-month-controls" });
  const prevBtn = el("button", {
    class: "cp-btn cp-btn-icon",
    attrs: { type: "button", "aria-label": "Previous month" },
    text: "‹",
  });
  const nextBtn = el("button", {
    class: "cp-btn cp-btn-icon",
    attrs: { type: "button", "aria-label": "Next month" },
    text: "›",
  });
  const monthInput = /** @type {HTMLInputElement} */ (
    el("input", {
      class: "cp-input",
      attrs: { type: "month", value: month, "aria-label": "Select month" },
    })
  );

  prevBtn.addEventListener("click", () => onMonthChange(shiftMonth(month, -1)));
  nextBtn.addEventListener("click", () => onMonthChange(shiftMonth(month, 1)));
  monthInput.addEventListener("change", () => {
    if (monthInput.value) onMonthChange(monthInput.value);
  });

  controls.append(prevBtn, monthInput, nextBtn);
  topRow.append(controls);
  section.append(topRow);

  if (hosts.length === 0) {
    section.append(el("p", { class: "cp-muted", text: "No egress data recorded for this month." }));
    return section;
  }

  const list = el("div", { class: "cp-egress-list", attrs: { role: "list" } });
  for (const entry of hosts) {
    list.append(egressRow(entry, direction));
  }
  section.append(list);
  return section;
}

/**
 * egressRow builds a single host's egress row for the selected
 * direction, showing the other direction's total as secondary text.
 * @param {{host_id: string, hostname: string, egress: Object}} entry
 * @param {"out"|"in"} direction
 * @returns {HTMLElement}
 */
function egressRow(entry, direction) {
  const { hostname, egress } = entry;
  const row = el("div", { class: "cp-egress-row", attrs: { role: "listitem" } });

  const isOut = direction === "out";
  const bytes = isOut ? egress.tx_bytes : egress.rx_bytes;
  const limitBytes = isOut ? egress.limit_bytes : egress.rx_limit_bytes;
  const level = isOut ? egress.level : egress.rx_level;
  const projectedBytes = isOut ? egress.projected_tx_bytes : egress.projected_rx_bytes;
  const hubOverride = (isOut ? egress.limit_source : egress.rx_limit_source) === "hub";
  const otherLabel = isOut ? "Inbound" : "Outbound";
  const otherBytes = isOut ? egress.rx_bytes : egress.tx_bytes;

  const head = el("div", { class: "cp-egress-row-head" });
  const nameGroup = el("span", { class: "cp-egress-direction-label-group" });
  nameGroup.append(el("a", { text: hostname, attrs: { href: `#/host/${encodeURIComponent(entry.host_id)}` } }));
  if (hubOverride) nameGroup.append(hubOverrideBadge());
  head.append(nameGroup, egressLevelChip(level));
  row.append(head);

  if (limitBytes === 0) {
    row.append(el("p", { class: "cp-egress-unlimited", text: `No limit · ${formatBytes(bytes)} used` }));
    row.append(el("p", { class: "cp-muted-small", text: `${otherLabel}: ${formatBytes(otherBytes)}` }));
    return row;
  }

  row.append(
    progressBar({
      value: bytes,
      max: limitBytes,
      label: `${hostname} monthly ${isOut ? "outbound" : "inbound"} usage`,
      levelClass: levelClassForBar(level),
      valueText: `${formatBytes(bytes)} / ${formatBytes(limitBytes)}`,
    }),
  );
  row.append(
    el("p", {
      class: "cp-muted-small",
      text: `${otherLabel}: ${formatBytes(otherBytes)} · Projected: ${formatBytes(projectedBytes)}`,
    }),
  );
  return row;
}
