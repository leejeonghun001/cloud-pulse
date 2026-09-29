// settings.js — builds the hub Settings page (#/settings): agent
// enrollment (token reveal/copy + install command), per-host traffic
// limits editor, alert webhook editor + test, and read-only hub info.
// Also renders the "admin disabled" explanatory panel when the hub has
// no CP_UI_TOKEN configured (GET /api/v1/settings -> 403
// admin_disabled).
import { el, clearChildren } from "./components.js";
import { formatBytes } from "./format.js";
import { parseLimitGiB, formatLimitGiB } from "./limits.js";
import {
  getSettings,
  getAgentToken,
  setHostLimits,
  setAlertWebhook,
  testAlertWebhook,
  AdminError,
} from "./api.js";

/**
 * copyToClipboard copies text to the clipboard using the async
 * Clipboard API when available (secure context + permission), falling
 * back to selecting the given text-holding input/textarea element so the
 * user can press Ctrl+C themselves. Returns a status string suitable for
 * an aria-live announcement.
 * @param {string} text
 * @param {HTMLInputElement|HTMLTextAreaElement} [selectEl] element whose
 *   content mirrors text, selected as a fallback when the Clipboard API
 *   is unavailable or rejects.
 * @returns {Promise<"copied"|"select-fallback">}
 */
export async function copyToClipboard(text, selectEl) {
  try {
    if (globalThis.navigator?.clipboard?.writeText) {
      await globalThis.navigator.clipboard.writeText(text);
      return "copied";
    }
  } catch {
    // Fall through to the selection fallback (permission denied,
    // insecure context, etc.).
  }
  if (selectEl) {
    selectEl.removeAttribute("hidden");
    selectEl.focus();
    selectEl.select();
  }
  return "select-fallback";
}

/**
 * buildSettingsPage renders the full settings page into a fresh element
 * and wires up its own data loading/actions. Errors while loading are
 * handled internally (admin_disabled panel, 401 is handled by
 * api.js's token-dialog retry upstream).
 * @param {Object} opts
 * @param {AbortSignal} opts.signal
 * @param {(text: string) => void} opts.announce aria-live announcer
 * @param {string} [opts.focusHostID] host_id to scroll/focus into view
 *   once the limits table renders (from an "Edit limits" deep link).
 * @returns {Promise<HTMLElement>}
 */
export async function buildSettingsPage({ signal, announce, focusHostID }) {
  const root = el("div", { class: "cp-settings-page" });
  root.append(el("h1", { class: "cp-page-title", text: "Settings" }));

  const body = el("div", { class: "cp-settings-body" });
  root.append(body);

  let view;
  try {
    view = await getSettings(signal);
  } catch (err) {
    if (err?.name === "AbortError") return root;
    if (err instanceof AdminError && err.code === "admin_disabled") {
      body.append(adminDisabledPanel());
      return root;
    }
    throw err;
  }

  body.append(agentEnrollmentCard({ signal, announce }));
  body.append(trafficLimitsCard({ view, signal, announce, focusHostID }));
  body.append(alertsCard({ view, signal, announce }));
  body.append(hubInfoCard(view));

  return root;
}

/**
 * adminDisabledPanel builds the explanatory panel shown when the hub
 * has no CP_UI_TOKEN configured (settings/admin endpoints return 403
 * admin_disabled), with instructions to enable it.
 * @returns {HTMLElement}
 */
function adminDisabledPanel() {
  const card = el("div", { class: "cp-card cp-admin-disabled" });
  card.append(el("h2", { text: "Settings are disabled" }));
  card.append(
    el("p", {
      text:
        "This hub has no CP_UI_TOKEN configured, so the agent token and " +
        "settings (traffic limits, alert webhook) can't be exposed here " +
        "— there is no authenticated way to reach them without one.",
    }),
  );
  card.append(el("h3", { text: "To enable it" }));
  const steps = el("ol", { class: "cp-admin-disabled-steps" });
  steps.append(
    el("li", { text: "Generate a token: cloud-pulse-hub -gen-token" }),
    el("li", { text: "Add CP_UI_TOKEN=<token> to /etc/cloud-pulse/hub.env" }),
    el("li", { text: "Restart the hub: sudo systemctl restart cloud-pulse-hub" }),
  );
  card.append(steps);
  card.append(
    el("p", {
      class: "cp-muted-small",
      text: "Or re-run install-hub.sh with a flag that supplies/generates a UI token (see its --help output).",
    }),
  );
  return card;
}

// ---------------------------------------------------------------------------
// Agent enrollment card: reveal/copy token + install command
// ---------------------------------------------------------------------------

/**
 * agentEnrollmentCard builds the token reveal/copy + install command
 * card. The full token is only fetched from the hub when "Reveal" is
 * clicked (never pre-fetched), matching the admin endpoint's intent of
 * never exposing the token more than necessary.
 * @param {Object} opts
 * @param {AbortSignal} opts.signal
 * @param {(text: string) => void} opts.announce
 * @returns {HTMLElement}
 */
function agentEnrollmentCard({ signal, announce }) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { text: "Agent enrollment" }));
  card.append(
    el("p", {
      class: "cp-muted-small",
      text: "Reveal the agent token to enroll a new host, or copy the ready-to-run install command below.",
    }),
  );

  const tokenRow = el("div", { class: "cp-settings-row" });
  const tokenInput = /** @type {HTMLInputElement} */ (
    el("input", {
      class: "cp-input cp-token-reveal-input",
      attrs: { type: "text", readonly: "", value: "", "aria-label": "Agent token", hidden: "" },
    })
  );
  const revealBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: "Reveal" });
  const copyTokenBtn = el("button", {
    class: "cp-btn cp-btn-secondary",
    attrs: { type: "button", hidden: "" },
    text: "Copy",
  });
  const hideBtn = el("button", {
    class: "cp-btn cp-btn-secondary",
    attrs: { type: "button", hidden: "" },
    text: "Hide again",
  });
  const tokenStatus = el("span", { class: "cp-muted-small", attrs: { role: "status", "aria-live": "polite" } });

  let installCommand = "";
  const installBlock = el("pre", { class: "cp-code-block cp-code-block-wrap", attrs: { hidden: "" } });
  const installCode = el("code", { text: "" });
  installBlock.append(installCode);
  const copyInstallBtn = el("button", {
    class: "cp-btn cp-btn-secondary",
    attrs: { type: "button", hidden: "" },
    text: "Copy install command",
  });
  const installFallback = /** @type {HTMLTextAreaElement} */ (
    el("textarea", {
      class: "cp-input cp-install-fallback",
      attrs: { readonly: "", rows: "2", hidden: "", "aria-label": "Install command" },
    })
  );

  revealBtn.addEventListener("click", async () => {
    revealBtn.disabled = true;
    try {
      const data = await getAgentToken(signal);
      tokenInput.value = data.agent_token;
      tokenInput.removeAttribute("hidden");
      copyTokenBtn.removeAttribute("hidden");
      hideBtn.removeAttribute("hidden");
      revealBtn.setAttribute("hidden", "");

      installCommand = data.install_command;
      installCode.textContent = installCommand;
      installFallback.value = installCommand;
      installBlock.removeAttribute("hidden");
      copyInstallBtn.removeAttribute("hidden");

      tokenStatus.textContent = "Token revealed.";
      announce("Agent token revealed.");
    } catch (err) {
      if (err?.name === "AbortError") return;
      tokenStatus.textContent = `Failed to reveal token: ${err.message}`;
    } finally {
      revealBtn.disabled = false;
    }
  });

  hideBtn.addEventListener("click", () => {
    tokenInput.value = "";
    tokenInput.setAttribute("hidden", "");
    copyTokenBtn.setAttribute("hidden", "");
    hideBtn.setAttribute("hidden", "");
    revealBtn.removeAttribute("hidden");
    installCommand = "";
    installCode.textContent = "";
    installFallback.value = "";
    installBlock.setAttribute("hidden", "");
    copyInstallBtn.setAttribute("hidden", "");
    installFallback.setAttribute("hidden", "");
    tokenStatus.textContent = "Token hidden.";
  });

  copyTokenBtn.addEventListener("click", async () => {
    const result = await copyToClipboard(tokenInput.value, tokenInput);
    tokenStatus.textContent = result === "copied" ? "Token copied to clipboard." : "Press Ctrl+C to copy.";
  });

  copyInstallBtn.addEventListener("click", async () => {
    const result = await copyToClipboard(installCommand, installFallback);
    tokenStatus.textContent =
      result === "copied" ? "Install command copied to clipboard." : "Press Ctrl+C to copy.";
  });

  tokenRow.append(revealBtn, tokenInput, copyTokenBtn, hideBtn, tokenStatus);
  card.append(tokenRow);
  card.append(installBlock);
  card.append(copyInstallBtn);
  card.append(installFallback);

  return card;
}

// ---------------------------------------------------------------------------
// Traffic limits card
// ---------------------------------------------------------------------------

/**
 * trafficLimitsCard builds the per-host outbound/inbound limit override
 * table, with a Save + Reset button and inline status per row.
 * @param {Object} opts
 * @param {Object} opts.view models.SettingsView JSON
 * @param {AbortSignal} opts.signal
 * @param {(text: string) => void} opts.announce
 * @param {string} [opts.focusHostID]
 * @returns {HTMLElement}
 */
function trafficLimitsCard({ view, signal, announce, focusHostID }) {
  const card = el("div", { class: "cp-card", attrs: { id: "cp-settings-limits" } });
  card.append(el("h2", { text: "Traffic limits" }));
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
  card.append(table);
  return card;
}

/**
 * hostLimitsRow builds one host's editable limits row.
 * @param {Object} opts
 * @param {Object} opts.host models.HostLimitsView JSON
 * @param {AbortSignal} opts.signal
 * @param {(text: string) => void} opts.announce
 * @param {string} [opts.focusHostID]
 * @returns {HTMLElement}
 */
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

// ---------------------------------------------------------------------------
// Alerts card
// ---------------------------------------------------------------------------

/**
 * alertsCard builds the alert webhook URL editor + test-send button.
 * @param {Object} opts
 * @param {Object} opts.view models.SettingsView JSON
 * @param {AbortSignal} opts.signal
 * @param {(text: string) => void} opts.announce
 * @returns {HTMLElement}
 */
function alertsCard({ view, signal, announce }) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { text: "Alerts" }));

  const sourceLabels = { hub: "hub override", env: "environment (CP_ALERT_WEBHOOK_URL)", none: "none configured" };
  const sourceLabel = el("p", {
    class: "cp-muted-small",
    text: `Current source: ${sourceLabels[view.alert_webhook_source] || view.alert_webhook_source}`,
  });
  card.append(sourceLabel);

  const row = el("div", { class: "cp-settings-row" });
  const urlInput = /** @type {HTMLInputElement} */ (
    el("input", {
      class: "cp-input cp-webhook-input",
      attrs: {
        type: "url",
        value: view.alert_webhook_url || "",
        placeholder: "https://hooks.example.com/...",
        "aria-label": "Alert webhook URL",
      },
    })
  );
  const saveBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: "Save" });
  const clearBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: "Clear" });
  const testBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: "Send test" });
  const status = el("span", { class: "cp-muted-small", attrs: { role: "status", "aria-live": "polite" } });

  saveBtn.addEventListener("click", async () => {
    saveBtn.disabled = true;
    status.textContent = "Saving…";
    try {
      const updated = await setAlertWebhook(urlInput.value.trim(), signal);
      view.alert_webhook_url = updated.alert_webhook_url;
      view.alert_webhook_source = updated.alert_webhook_source;
      sourceLabel.textContent = `Current source: ${sourceLabels[view.alert_webhook_source] || view.alert_webhook_source}`;
      status.textContent = "Saved.";
      announce("Saved alert webhook URL.");
    } catch (err) {
      if (err?.name === "AbortError") return;
      status.textContent = `Save failed: ${err.message}`;
    } finally {
      saveBtn.disabled = false;
    }
  });

  clearBtn.addEventListener("click", async () => {
    urlInput.value = "";
    saveBtn.click();
  });

  testBtn.addEventListener("click", async () => {
    testBtn.disabled = true;
    status.textContent = "Sending test notification…";
    try {
      await testAlertWebhook(signal);
      status.textContent = "Test notification sent.";
      announce("Test notification sent.");
    } catch (err) {
      if (err?.name === "AbortError") return;
      status.textContent = `Test failed: ${err.message}`;
    } finally {
      testBtn.disabled = false;
    }
  });

  row.append(urlInput, saveBtn, clearBtn, testBtn);
  card.append(row);
  card.append(status);
  return card;
}

// ---------------------------------------------------------------------------
// Hub info card
// ---------------------------------------------------------------------------

/**
 * hubInfoCard builds the read-only hub info card: version, allowed
 * CIDRs, offline threshold, cloud interval, and whether UI auth is on.
 * @param {Object} view models.SettingsView JSON
 * @returns {HTMLElement}
 */
function hubInfoCard(view) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { text: "Hub info" }));

  const dl = el("dl", { class: "cp-host-detail-meta" });
  const entries = [
    ["Version", view.version || "unknown"],
    ["Allowed CIDRs", view.allowed_cidrs.join(", ") || "none"],
    ["Offline after", `${view.offline_after_seconds}s`],
    ["Cloud interval", `${view.cloud_interval_seconds}s`],
    ["UI auth", view.ui_auth_enabled ? "on" : "off"],
    ["Agent token", view.agent_token_hint],
  ];
  for (const [label, value] of entries) {
    const pair = el("div", { class: "cp-host-detail-meta-pair" });
    pair.append(el("dt", { text: label }), el("dd", { text: value }));
    dl.append(pair);
  }
  card.append(dl);
  return card;
}

/** clearAndRender is a small helper re-exported for main.js's router. */
export function clearAndRender(host, node) {
  clearChildren(host);
  host.append(node);
}
