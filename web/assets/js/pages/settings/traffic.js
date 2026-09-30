// settings/traffic.js — Traffic limits section: per-host outbound/
// inbound egress limit overrides (GiB units), restyled from v0.3.x's
// single-page settings into its own sidebar section.
import { el, clearChildren } from "../../ui/components.js";
import { formatBytes } from "../../core/format.js";
import { parseLimitGiB, formatLimitGiB } from "../../core/limits.js";
import { getSettings, setHostLimits } from "../../core/api.js";
import { t } from "../../core/i18n.js";

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
  card.append(el("h2", { class: "cp-card-title", text: t("traffic.title") }));
  card.append(
    el("p", {
      class: "cp-muted-small",
      text: t("traffic.description"),
    }),
  );

  if (view.hosts.length === 0) {
    card.append(el("p", { class: "cp-muted", text: t("traffic.no_hosts_reporting_yet") }));
    return card;
  }

  const tableWrap = el("div", { class: "cp-table-wrap" });
  const table = el("table", { class: "cp-table" });
  const thead = el("thead");
  thead.append(
    el("tr", {
      children: [t("traffic.col_host"), t("traffic.col_provider"), t("traffic.col_agent_default"), t("traffic.col_outbound_override"), t("traffic.col_inbound_override"), t("traffic.col_effective"), ""].map(
        (tx) => el("th", { text: tx }),
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
  tr.append(el("td", { text: host.agent_egress_limit_bytes === 0 ? t("traffic.unlimited") : formatBytes(host.agent_egress_limit_bytes) }));

  const egressInput = /** @type {HTMLInputElement} */ (
    el("input", {
      class: "cp-input cp-limit-input",
      attrs: {
        type: "text",
        inputmode: "decimal",
        value: formatLimitGiB(host.egress_limit_bytes),
        "aria-label": t("traffic.outbound_override_aria_label", { hostname: host.hostname }),
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
        "aria-label": t("traffic.inbound_override_aria_label", { hostname: host.hostname }),
      },
    })
  );
  tr.append(el("td", { children: [ingressInput] }));

  const effective = el("td", {
    text: t("traffic.effective_value", {
      outbound: host.effective_egress_limit_bytes === 0 ? t("traffic.unlimited_lower") : formatBytes(host.effective_egress_limit_bytes),
      inbound: host.effective_ingress_limit_bytes === 0 ? t("traffic.unlimited_lower") : formatBytes(host.effective_ingress_limit_bytes),
    }),
  });
  tr.append(effective);

  const actionsCell = el("td", { class: "cp-limits-actions" });
  const status = el("span", { class: "cp-muted-small", attrs: { role: "status", "aria-live": "polite" } });
  const saveBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: t("common.save") });
  const resetBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: t("traffic.reset") });

  resetBtn.addEventListener("click", () => {
    egressInput.value = formatLimitGiB(host.egress_limit_bytes);
    ingressInput.value = formatLimitGiB(host.ingress_limit_bytes);
    status.textContent = t("traffic.reverted_to_saved");
  });

  saveBtn.addEventListener("click", async () => {
    const parsedEgress = parseLimitGiB(egressInput.value);
    const parsedIngress = parseLimitGiB(ingressInput.value);
    if (!parsedEgress.ok) {
      status.textContent = t("traffic.outbound_error", { error: parsedEgress.error });
      return;
    }
    if (!parsedIngress.ok) {
      status.textContent = t("traffic.inbound_error", { error: parsedIngress.error });
      return;
    }

    saveBtn.disabled = true;
    status.textContent = t("traffic.saving");
    try {
      const updated = await setHostLimits(
        host.host_id,
        { egressLimitBytes: parsedEgress.bytes, ingressLimitBytes: parsedIngress.bytes },
        signal,
      );
      host.egress_limit_bytes = updated.egress_limit_bytes;
      host.ingress_limit_bytes = updated.ingress_limit_bytes;
      status.textContent = t("traffic.saved");
      announce(t("traffic.saved_for_host", { hostname: host.hostname }));
    } catch (err) {
      if (err?.name === "AbortError") return;
      status.textContent = t("traffic.save_failed", { error: err.message });
    } finally {
      saveBtn.disabled = false;
    }
  });

  actionsCell.append(saveBtn, resetBtn, status);
  tr.append(actionsCell);
  return tr;
}
