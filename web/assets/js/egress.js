// egress.js — builds the overview page's per-host monthly egress
// section: a month selector (prev/next + <input type=month>) and a
// list of per-host egress rows with progress bars and level chips.
import { el, progressBar, egressLevelChip, levelClassForBar } from "./components.js";
import { formatBytes } from "./format.js";

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
 * buildEgressSection renders the egress month selector + host list.
 * @param {Object} opts
 * @param {string} opts.month "YYYY-MM"
 * @param {Array} opts.hosts entries from GET /api/v1/egress: {host_id, hostname, egress}
 * @param {(month: string) => void} opts.onMonthChange
 * @returns {HTMLElement}
 */
export function buildEgressSection({ month, hosts, onMonthChange }) {
  const section = el("section", { class: "cp-section", attrs: { "aria-labelledby": "cp-egress-heading" } });
  section.append(el("h2", { id: "cp-egress-heading", text: "Monthly egress" }));

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
  section.append(controls);

  if (hosts.length === 0) {
    section.append(el("p", { class: "cp-muted", text: "No egress data recorded for this month." }));
    return section;
  }

  const list = el("div", { class: "cp-egress-list", attrs: { role: "list" } });
  for (const entry of hosts) {
    list.append(egressRow(entry));
  }
  section.append(list);
  return section;
}

/**
 * egressRow builds a single host's egress row.
 * @param {{host_id: string, hostname: string, egress: Object}} entry
 * @returns {HTMLElement}
 */
function egressRow(entry) {
  const { hostname, egress } = entry;
  const row = el("div", { class: "cp-egress-row", attrs: { role: "listitem" } });

  const head = el("div", { class: "cp-egress-row-head" });
  head.append(
    el("a", { text: hostname, attrs: { href: `#/host/${encodeURIComponent(entry.host_id)}` } }),
    egressLevelChip(egress.level),
  );
  row.append(head);

  if (egress.limit_bytes === 0) {
    row.append(el("p", { class: "cp-egress-unlimited", text: `No limit · ${formatBytes(egress.tx_bytes)} used` }));
    row.append(el("p", { class: "cp-muted-small", text: `Rx: ${formatBytes(egress.rx_bytes)}` }));
    return row;
  }

  row.append(
    progressBar({
      value: egress.tx_bytes,
      max: egress.limit_bytes,
      label: `${hostname} monthly egress usage`,
      levelClass: levelClassForBar(egress.level),
      valueText: `${formatBytes(egress.tx_bytes)} / ${formatBytes(egress.limit_bytes)}`,
    }),
  );
  row.append(
    el("p", {
      class: "cp-muted-small",
      text: `Rx: ${formatBytes(egress.rx_bytes)} · Projected: ${formatBytes(egress.projected_tx_bytes)}`,
    }),
  );
  return row;
}
