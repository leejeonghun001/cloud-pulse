// settings/traffic.js — Traffic limits section: per-host outbound/
// inbound egress limit overrides (GiB units), restyled from v0.3.x's
// single-page settings into its own sidebar section.
import { el, clearChildren } from "../../ui/components.js";
import { formatBytes } from "../../core/format.js";
import { parseLimitGiB, formatLimitGiB } from "../../core/limits.js";
import { getSettings, setHostLimits } from "../../core/api.js";

/**
 * mountTrafficSection renders the Traffic limits section into
 * container.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @param {string} [ctx.focusHostID]
 * @returns {{refresh: () => Promise<void>, teardown: () => void}}
 */
export function mountTrafficSection(container, { announce, focusHostID }) {
  const controller = new AbortController();

  async function refresh() {
    clearChildren(container);

    let view;
    try {
      view = await getSettings(controller.signal);
    } catch (err) {
      if (err?.name === "AbortError") return;
      throw err;
    }

    container.append(trafficLimitsCard({ view, signal: controller.signal, announce, focusHostID }));

    if (focusHostID) {
      const row = container.querySelector(`#cp-limits-row-${cssEscape(focusHostID)}`);
      row?.scrollIntoView({ block: "center" });
    }
  }

  function teardown() {
    controller.abort();
  }

  return { refresh, teardown };
}

function cssEscape(s) {
  return globalThis.CSS?.escape ? CSS.escape(s) : s.replace(/[^A-Za-z0-9_-]/g, "\\$&");
}

function trafficLimitsCard({ view, signal, announce, focusHostID }) {
  const card = el("div", { class: "cp-card", attrs: { id: "cp-settings-limits" } });
  card.append(el("h2", { class: "cp-card-title", text: "Traffic limits" }));
  card.append(
    el("p", {
      class: "cp-muted-small",
      text: "Values are in GiB (2^30 bytes), matching the agent's CP_EGRESS_LIMIT_GB units. Leave blank to use the agent default; enter 0 for explicitly unlimited.",
    }),
  );

  if (view.hosts.length === 0) {
    card.append(el("p", { class: "cp-muted", text: "No hosts reporting yet." }));
    return card;
  }

  const tableWrap = el("div", { class: "cp-table-wrap" });
  const table = el("table", { class: "cp-table" });
  const thead = el("thead");
  thead.append(
    el("tr", {
      children: ["Host", "Provider", "Agent default", "Outbound override (GiB)", "Inbound override (GiB)", "Effective", ""].map(
        (t) => el("th", { text: t }),
      ),
    }),
  );
  table.append(thead);
  const tbody = el("tbody");

  for (const h of view.hosts) {
    tbody.append(hostLimitsRow({ host: h, signal, announce, focusHostID }));
  }
  table.append(tbody);
  tableWrap.append(table);
  card.append(tableWrap);
  return card;
}

function hostLimitsRow({ host, signal, announce, focusHostID }) {
  const rowID = `cp-limits-row-${host.host_id}`;
  const tr = el("tr", { attrs: { id: rowID } });
  if (host.host_id === focusHostID) {
    tr.classList.add("cp-row-highlighted");
  }

  tr.append(el("td", { text: host.hostname }));
  tr.append(el("td", { text: host.provider }));
  tr.append(el("td", { text: host.agent_egress_limit_bytes === 0 ? "Unlimited" : formatBytes(host.agent_egress_limit_bytes) }));

  const egressInput = /** @type {HTMLInputElement} */ (
    el("input", {
      class: "cp-input cp-limit-input",
      attrs: {
        type: "text",
        inputmode: "decimal",
        value: formatLimitGiB(host.egress_limit_bytes),
        "aria-label": `${host.hostname} outbound limit override in GiB`,
      },
    })
  );
  tr.append(el("td", { children: [egressInput] }));

  const ingressInput = /** @type {HTMLInputElement} */ (
    el("input", {
      class: "cp-input cp-limit-input",
      attrs: {
        type: "text",
        inputmode: "decimal",
        value: formatLimitGiB(host.ingress_limit_bytes),
        "aria-label": `${host.hostname} inbound limit override in GiB`,
      },
    })
  );
  tr.append(el("td", { children: [ingressInput] }));

  const effective = el("td", {
    text: `↑ ${host.effective_egress_limit_bytes === 0 ? "unlimited" : formatBytes(host.effective_egress_limit_bytes)} · ↓ ${host.effective_ingress_limit_bytes === 0 ? "unlimited" : formatBytes(host.effective_ingress_limit_bytes)}`,
  });
  tr.append(effective);

  const actionsCell = el("td", { class: "cp-limits-actions" });
  const status = el("span", { class: "cp-muted-small", attrs: { role: "status", "aria-live": "polite" } });
  const saveBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: "Save" });
  const resetBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: "Reset" });

  resetBtn.addEventListener("click", () => {
    egressInput.value = formatLimitGiB(host.egress_limit_bytes);
    ingressInput.value = formatLimitGiB(host.ingress_limit_bytes);
    status.textContent = "Reverted to last saved values.";
  });

  saveBtn.addEventListener("click", async () => {
    const parsedEgress = parseLimitGiB(egressInput.value);
    const parsedIngress = parseLimitGiB(ingressInput.value);
    if (!parsedEgress.ok) {
      status.textContent = `Outbound: ${parsedEgress.error}`;
      return;
    }
    if (!parsedIngress.ok) {
      status.textContent = `Inbound: ${parsedIngress.error}`;
      return;
    }

    saveBtn.disabled = true;
    status.textContent = "Saving…";
    try {
      const updated = await setHostLimits(
        host.host_id,
        { egressLimitBytes: parsedEgress.bytes, ingressLimitBytes: parsedIngress.bytes },
        signal,
      );
      host.egress_limit_bytes = updated.egress_limit_bytes;
      host.ingress_limit_bytes = updated.ingress_limit_bytes;
      status.textContent = "Saved.";
      announce(`Saved traffic limits for ${host.hostname}.`);
    } catch (err) {
      if (err?.name === "AbortError") return;
      status.textContent = `Save failed: ${err.message}`;
    } finally {
      saveBtn.disabled = false;
    }
  });

  actionsCell.append(saveBtn, resetBtn, status);
  tr.append(actionsCell);
  return tr;
}
