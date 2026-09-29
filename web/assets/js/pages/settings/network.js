// settings/network.js — Network section: adapters with per-address
// listen checkboxes, an "all interfaces" option, port input, an access
// allowlist editor (chips + one-click per-adapter suggestions), live
// listener status, context warnings (which address the admin is
// connected through, which agents would be disconnected, whether LAN
// clients are blocked), a Save flow that opens a confirm dialog showing
// the diff, a pending-change banner with a live countdown and Keep/
// Revert actions plus the new candidate URLs, and "Reset to hub.env
// defaults". See SPEC-v0.4 §2 and network-helpers.js for the pure
// logic this renders.
import { el, clearChildren } from "../../ui/components.js";
import { icon } from "../../ui/icons.js";
import { createDialog, buildDialogActions } from "../../ui/dialog.js";
import { showToast } from "../../ui/toast.js";
import {
  getNetworkState,
  putNetworkConfig,
  confirmNetworkChange,
  revertNetworkChange,
  deleteNetworkConfig,
  ApiError,
} from "../../core/api.js";
import {
  validateCustomAddress,
  validatePort,
  normalizeCIDRList,
  buildNetworkConfigFromSelection,
  describeNetworkConfig,
  diffNetworkConfig,
  suggestionsForInterfaces,
  isAllowlistCovering,
  formatCountdown,
  describeNetworkError,
} from "./network-helpers.js";

const COUNTDOWN_TICK_MS = 1000;

/**
 * mountNetworkSection renders the Network section into container.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @returns {{refresh: () => Promise<void>, teardown: () => void}}
 */
export function mountNetworkSection(container, { announce }) {
  const controller = new AbortController();
  let countdownTimer = null;

  function clearCountdownTimer() {
    if (countdownTimer) {
      clearInterval(countdownTimer);
      countdownTimer = null;
    }
  }

  async function refresh() {
    clearCountdownTimer();
    clearChildren(container);

    let state;
    try {
      state = await getNetworkState(controller.signal);
    } catch (err) {
      if (err?.name === "AbortError") return;
      container.append(el("p", { class: "cp-error-text", text: `Failed to load network settings: ${err.message}` }));
      return;
    }

    if (state.pending) {
      container.append(
        pendingBanner({
          state,
          signal: controller.signal,
          announce,
          onSettled: refresh,
          registerTicker: (fn) => {
            countdownTimer = setInterval(fn, COUNTDOWN_TICK_MS);
          },
        }),
      );
    }

    container.append(contextCard(state));
    container.append(
      configCard({
        state,
        signal: controller.signal,
        announce,
        onApplied: refresh,
      }),
    );
    container.append(listenerStatusCard(state));

    if (state.interfaces_error) {
      container.append(
        el("p", { class: "cp-hint", text: `Could not enumerate network adapters: ${state.interfaces_error}` }),
      );
    }
  }

  function teardown() {
    clearCountdownTimer();
    controller.abort();
  }

  return { refresh, teardown };
}

// ---------------------------------------------------------------------------
// Pending-change banner
// ---------------------------------------------------------------------------

function pendingBanner({ state, signal, announce, onSettled, registerTicker }) {
  const banner = el("div", { class: "cp-network-pending-banner", attrs: { role: "alert" } });
  banner.append(icon("triangleAlert"));

  const body = el("div", { class: "cp-network-pending-body" });
  body.append(
    el("p", {
      class: "cp-network-pending-title",
      text: "A network change is pending confirmation.",
    }),
  );

  const countdownEl = el("p", { class: "cp-network-pending-countdown" });
  body.append(countdownEl);

  function renderCountdown() {
    const remaining = formatCountdown(state.pending.deadline);
    countdownEl.textContent =
      remaining === "expired"
        ? "Reverting now…"
        : `Auto-reverts in ${remaining} unless confirmed.`;
  }
  renderCountdown();
  registerTicker(renderCountdown);

  if (state.pending.urls.length > 0) {
    const urlsList = el("ul", { class: "cp-network-pending-urls" });
    for (const u of state.pending.urls) {
      const li = el("li");
      li.append(el("a", { class: "cp-link", attrs: { href: u, target: "_blank", rel: "noopener noreferrer" }, text: u }));
      urlsList.append(li);
    }
    body.append(el("p", { class: "cp-muted-small", text: "Open the change from one of these addresses, then confirm it there:" }));
    body.append(urlsList);
  }

  const actions = el("div", { class: "cp-network-pending-actions" });
  const keepBtn = el("button", { class: "cp-btn cp-btn-primary cp-btn-sm", attrs: { type: "button" }, text: "Keep changes" });
  const revertBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" }, text: "Revert" });
  const status = el("span", { class: "cp-muted-small", attrs: { role: "status", "aria-live": "polite" } });

  keepBtn.addEventListener("click", async () => {
    keepBtn.disabled = true;
    revertBtn.disabled = true;
    try {
      await confirmNetworkChange(signal);
      showToast({ message: "Network configuration confirmed.", variant: "success" });
      announce("Network configuration confirmed.");
      onSettled();
    } catch (err) {
      if (err?.name === "AbortError") return;
      status.textContent = describeNetworkError(err);
      keepBtn.disabled = false;
      revertBtn.disabled = false;
    }
  });

  revertBtn.addEventListener("click", async () => {
    keepBtn.disabled = true;
    revertBtn.disabled = true;
    try {
      await revertNetworkChange(signal);
      showToast({ message: "Network configuration reverted.", variant: "info" });
      announce("Network configuration reverted.");
      onSettled();
    } catch (err) {
      if (err?.name === "AbortError") return;
      status.textContent = describeNetworkError(err);
      keepBtn.disabled = false;
      revertBtn.disabled = false;
    }
  });

  actions.append(keepBtn, revertBtn, status);
  body.append(actions);
  banner.append(body);
  return banner;
}

// ---------------------------------------------------------------------------
// Context card: current client connection, agent connections, warnings
// ---------------------------------------------------------------------------

function contextCard(state) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { class: "cp-card-title", text: "Connection context" }));

  const dl = el("dl", { class: "cp-host-detail-meta" });
  const pair = el("div", { class: "cp-host-detail-meta-pair" });
  pair.append(
    el("dt", { text: "You are connected via" }),
    el("dd", { text: `${state.client.local_addr || "unknown"} (from ${state.client.ip})` }),
  );
  dl.append(pair);
  card.append(dl);

  if (state.agents && state.agents.length > 0) {
    card.append(
      el("p", {
        class: "cp-muted-small",
        text: `${state.agents.length} agent${state.agents.length === 1 ? "" : "s"} last connected via: ${state.agents
          .map((a) => `${a.hostname} (${a.local_addr})`)
          .join(", ")}.`,
      }),
    );
  }

  const allowAll = (state.config.allowed_cidrs || []).includes("*");
  const nonLoopbackNonLinkLocalAddrs = (state.interfaces || []).flatMap((iface) =>
    (iface.addresses || [])
      .filter((a) => a.scope === "global")
      .map((a) => ({ ...a, ifaceName: iface.name, ifaceKind: iface.kind })),
  );
  if (!allowAll) {
    const uncovered = nonLoopbackNonLinkLocalAddrs.filter(
      (a) => !isAllowlistCovering(state.config.allowed_cidrs || [], a.suggested_cidr),
    );
    const lanUncovered = uncovered.filter((a) => a.ifaceKind === "physical");
    if (lanUncovered.length > 0) {
      card.append(
        el("p", {
          class: "cp-hint cp-network-warning",
          text: `LAN clients on ${lanUncovered.map((a) => a.ifaceName).join(", ")} are blocked by the current allowlist.`,
        }),
      );
    }
  }

  return card;
}

// ---------------------------------------------------------------------------
// Listen + allowlist configuration card
// ---------------------------------------------------------------------------

function configCard({ state, signal, announce, onApplied }) {
  const card = el("div", { class: "cp-card cp-network-config-card" });
  card.append(el("h2", { class: "cp-card-title", text: "Listen addresses & access" }));
  card.append(
    el("p", {
      class: "cp-muted-small",
      text: `Source: ${state.source === "hub" ? "hub-managed (overrides CP_LISTEN/CP_ALLOWED_CIDRS)" : "environment (CP_LISTEN/CP_ALLOWED_CIDRS)"}.`,
    }),
  );

  const allInterfaces = state.config.mode === "all";
  const selected = new Set(allInterfaces ? [] : state.config.addresses || []);

  // -- Adapters -------------------------------------------------------
  const adaptersSection = el("div", { class: "cp-network-adapters" });
  adaptersSection.append(el("h3", { class: "cp-network-subheading", text: "Listen on" }));

  const allRow = el("label", { class: "cp-checkbox-row cp-network-all-row" });
  const allCheckbox = /** @type {HTMLInputElement} */ (
    el("input", { attrs: { type: "checkbox", ...(allInterfaces ? { checked: "" } : {}) } })
  );
  allRow.append(allCheckbox, el("span", { text: "All interfaces (0.0.0.0 / ::)" }));
  adaptersSection.append(allRow);

  const adapterList = el("div", { class: "cp-network-adapter-list" });
  const addressCheckboxes = new Map();

  for (const iface of state.interfaces || []) {
    const ifaceCard = el("div", { class: "cp-network-adapter" });
    const header = el("div", { class: "cp-network-adapter-header" });
    header.append(
      el("span", { class: "cp-network-adapter-name", text: iface.name }),
      el("span", { class: `cp-chip cp-network-kind-${iface.kind}`, text: iface.kind }),
      el("span", { class: `cp-chip ${iface.up ? "cp-chip-ok" : "cp-chip-critical"}`, text: iface.up ? "up" : "down" }),
    );
    ifaceCard.append(header);

    for (const addr of iface.addresses || []) {
      const isLinkLocal = addr.scope === "link-local";
      const row = el("label", { class: `cp-checkbox-row cp-network-addr-row${isLinkLocal ? " cp-network-addr-muted" : ""}` });
      const checkbox = /** @type {HTMLInputElement} */ (
        el("input", {
          attrs: {
            type: "checkbox",
            ...(selected.has(addr.ip) ? { checked: "" } : {}),
            ...(isLinkLocal ? { disabled: "" } : {}),
          },
        })
      );
      if (!isLinkLocal) addressCheckboxes.set(addr.ip, checkbox);
      row.append(
        checkbox,
        el("span", { class: "cp-tabular", text: `${addr.ip}/${addr.prefix_len}` }),
        el("span", { class: "cp-muted-small", text: `${addr.family} · ${addr.scope}` }),
      );
      ifaceCard.append(row);
    }
    adapterList.append(ifaceCard);
  }
  adaptersSection.append(adapterList);
  card.append(adaptersSection);

  function setAdapterListDisabled(disabled) {
    for (const cb of addressCheckboxes.values()) cb.disabled = disabled;
  }
  allCheckbox.addEventListener("change", () => setAdapterListDisabled(allCheckbox.checked));
  setAdapterListDisabled(allCheckbox.checked);

  // -- Port -------------------------------------------------------------
  const portField = el("div", { class: "cp-field cp-network-port-field" });
  portField.append(el("label", { class: "cp-label", attrs: { for: "cp-network-port" }, text: "Port" }));
  const portInput = /** @type {HTMLInputElement} */ (
    el("input", {
      class: "cp-input cp-input-sm",
      attrs: { id: "cp-network-port", type: "number", min: "1024", max: "65535", value: String(state.config.port) },
    })
  );
  portField.append(portInput);
  card.append(portField);

  // -- Allowlist --------------------------------------------------------
  const allowSection = el("div", { class: "cp-network-allowlist" });
  allowSection.append(el("h3", { class: "cp-network-subheading", text: "Access allowlist" }));
  allowSection.append(
    el("p", { class: "cp-muted-small", text: "Reflects the currently effective allowlist. \"*\" alone allows any client." }),
  );

  const chipsList = el("div", { class: "cp-network-chip-list" });
  const allowed = (state.config.allowed_cidrs || []).slice();

  function renderChips() {
    clearChildren(chipsList);
    for (const entry of allowed) {
      const chip = el("span", { class: "cp-chip cp-network-allow-chip", text: entry });
      const removeBtn = el("button", { class: "cp-network-chip-remove", attrs: { type: "button", "aria-label": `Remove ${entry}` } });
      removeBtn.append(icon("x"));
      removeBtn.addEventListener("click", () => {
        const idx = allowed.indexOf(entry);
        if (idx !== -1) allowed.splice(idx, 1);
        renderChips();
        renderSuggestions();
      });
      chip.append(removeBtn);
      chipsList.append(chip);
    }
    if (allowed.length === 0) {
      chipsList.append(el("span", { class: "cp-muted-small", text: "No entries — a save will be rejected." }));
    }
  }
  renderChips();
  allowSection.append(chipsList);

  const addRow = el("div", { class: "cp-settings-row" });
  const addInput = /** @type {HTMLInputElement} */ (
    el("input", { class: "cp-input cp-input-sm", attrs: { type: "text", placeholder: "e.g. 192.168.1.0/24 or *", "aria-label": "Add allowlist entry" } })
  );
  const addBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" }, text: "Add" });
  const addError = el("p", { class: "cp-error-text" });
  addBtn.addEventListener("click", () => {
    const v = addInput.value.trim();
    if (!v) return;
    if (allowed.includes(v)) {
      addInput.value = "";
      return;
    }
    allowed.push(v);
    addInput.value = "";
    addError.textContent = "";
    renderChips();
    renderSuggestions();
  });
  addInput.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter") {
      ev.preventDefault();
      addBtn.click();
    }
  });
  addRow.append(addInput, addBtn);
  allowSection.append(addRow);
  allowSection.append(addError);

  const suggestionsRow = el("div", { class: "cp-network-suggestions" });
  allowSection.append(suggestionsRow);

  function renderSuggestions() {
    clearChildren(suggestionsRow);
    const suggestions = suggestionsForInterfaces(state.interfaces || []);
    for (const s of suggestions) {
      if (isAllowlistCovering(allowed, s.cidr)) continue;
      const btn = el("button", { class: "cp-btn cp-btn-ghost cp-btn-sm", attrs: { type: "button" }, text: s.label });
      btn.addEventListener("click", () => {
        allowed.push(s.cidr);
        renderChips();
        renderSuggestions();
      });
      suggestionsRow.append(btn);
    }
  }
  renderSuggestions();

  card.append(allowSection);

  // -- Save / Reset -------------------------------------------------------
  const actionsRow = el("div", { class: "cp-network-actions" });
  const saveBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" }, text: "Save changes" });
  const resetBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: "Reset to hub.env defaults" });
  const saveStatus = el("span", { class: "cp-muted-small", attrs: { role: "status", "aria-live": "polite" } });
  actionsRow.append(saveBtn, resetBtn, saveStatus);
  card.append(actionsRow);

  saveBtn.addEventListener("click", () => {
    saveStatus.textContent = "";
    const selectedAddresses = Array.from(addressCheckboxes.entries())
      .filter(([, cb]) => cb.checked)
      .map(([ip]) => ip);

    if (!allCheckbox.checked) {
      if (selectedAddresses.length === 0) {
        saveStatus.textContent = "Select at least one address, or choose \"All interfaces\".";
        return;
      }
      for (const ip of selectedAddresses) {
        const err = validateCustomAddress(ip);
        if (err) {
          saveStatus.textContent = `${ip}: ${err}`;
          return;
        }
      }
    }

    const portErr = validatePort(portInput.valueAsNumber || Number(portInput.value));
    if (portErr) {
      saveStatus.textContent = portErr;
      return;
    }

    const cidrCheck = normalizeCIDRList(allowed);
    if (!cidrCheck.ok) {
      saveStatus.textContent = cidrCheck.error;
      return;
    }

    const nextConfig = buildNetworkConfigFromSelection({
      allInterfaces: allCheckbox.checked,
      selectedAddresses,
      port: portInput.valueAsNumber || Number(portInput.value),
      allowedCIDRs: allowed,
    });

    const diff = diffNetworkConfig(state.config, nextConfig);
    if (diff.length === 0) {
      saveStatus.textContent = "No changes to save.";
      return;
    }

    openConfirmDialog({
      previous: state.config,
      next: nextConfig,
      diff,
      signal,
      announce,
      onApplied,
    });
  });

  resetBtn.addEventListener("click", async () => {
    resetBtn.disabled = true;
    saveStatus.textContent = "Resetting…";
    try {
      await deleteNetworkConfig(signal);
      showToast({ message: "Reverted to hub.env defaults.", variant: "success" });
      announce("Network settings reverted to environment defaults.");
      onApplied();
    } catch (err) {
      if (err?.name === "AbortError") return;
      saveStatus.textContent = describeNetworkError(err);
    } finally {
      resetBtn.disabled = false;
    }
  });

  return card;
}

// ---------------------------------------------------------------------------
// Confirm dialog
// ---------------------------------------------------------------------------

function openConfirmDialog({ previous, next, diff, signal, announce, onApplied }) {
  const { dialog, body, open, close } = createDialog({
    titleId: "cp-network-confirm-title",
    title: "Confirm network configuration change",
    description: "Review the changes before applying them.",
    wide: true,
  });

  const diffList = el("dl", { class: "cp-host-detail-meta" });
  diffList.append(
    el("div", { class: "cp-host-detail-meta-pair", children: [el("dt", { text: "Current" }), el("dd", { text: describeNetworkConfig(previous) })] }),
    el("div", { class: "cp-host-detail-meta-pair", children: [el("dt", { text: "New" }), el("dd", { text: describeNetworkConfig(next) })] }),
  );
  for (const d of diff) {
    const pair = el("div", { class: "cp-host-detail-meta-pair" });
    pair.append(el("dt", { text: d.field }), el("dd", { text: `${d.from} → ${d.to}` }));
    diffList.append(pair);
  }
  body.append(diffList);
  body.append(
    el("p", {
      class: "cp-hint",
      text:
        "If this would stop serving your current connection, the change stays pending: the hub keeps the old address(es) reachable too until you confirm from one of the new ones, or it auto-reverts after 2 minutes.",
    }),
  );

  const errorEl = el("p", { class: "cp-error-text", attrs: { role: "alert" } });
  body.append(errorEl);

  const applyBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" }, text: "Apply" });
  const cancelBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: "Cancel" });
  cancelBtn.addEventListener("click", () => close());
  body.append(buildDialogActions([cancelBtn, applyBtn]));

  applyBtn.addEventListener("click", async () => {
    applyBtn.disabled = true;
    errorEl.textContent = "";
    try {
      const state = await putNetworkConfig(next, signal);
      close();
      if (state.pending) {
        showToast({ message: "Change applied; confirm from the new address to keep it.", variant: "info" });
        announce("Network change is pending confirmation.");
      } else {
        showToast({ message: "Network configuration saved.", variant: "success" });
        announce("Network configuration saved.");
      }
      onApplied();
    } catch (err) {
      if (err?.name === "AbortError") return;
      errorEl.textContent = describeNetworkError(err);
      applyBtn.disabled = false;
    }
  });

  open();
  return { dialog, close };
}

// ---------------------------------------------------------------------------
// Listener status card
// ---------------------------------------------------------------------------

function listenerStatusCard(state) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { class: "cp-card-title", text: "Listener status" }));

  if (!state.listeners || state.listeners.length === 0) {
    card.append(el("p", { class: "cp-muted", text: "No listener status reported." }));
    return card;
  }

  const tableWrap = el("div", { class: "cp-table-wrap" });
  const table = el("table", { class: "cp-table" });
  const thead = el("thead");
  thead.append(el("tr", { children: ["Address", "Status", "Detail"].map((t) => el("th", { text: t })) }));
  table.append(thead);
  const tbody = el("tbody");
  for (const st of state.listeners) {
    const tr = el("tr");
    tr.append(el("td", { class: "cp-tabular", text: st.addr }));
    const chipClass = st.status === "listening" ? "cp-chip-ok" : st.status === "waiting" ? "cp-chip-warning" : "cp-chip-exceeded";
    tr.append(el("td", { children: [el("span", { class: `cp-chip ${chipClass}`, text: st.status })] }));
    tr.append(el("td", { class: "cp-muted-small", text: st.error || "—" }));
    tbody.append(tr);
  }
  table.append(tbody);
  tableWrap.append(table);
  card.append(tableWrap);
  return card;
}
