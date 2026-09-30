// settings/agents.js — Agents section: agent token reveal/copy + a
// ready-to-run install command, and a list of enrolled agents with
// their reported version and any available self-update command.
import { el, clearChildren } from "../../ui/components.js";
import { agentVersionText, hostUpdateBadgeText } from "../../core/updates.js";
import { formatRelativeTimeFromUnixSeconds } from "../../core/format.js";
import { getAgentToken, getHosts, localizeApiError } from "../../core/api.js";
import { copyToClipboard } from "./index.js";
import { t } from "../../core/i18n.js";

/**
 * mountAgentsSection renders the Agents section into container.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @returns {{refresh: () => Promise<void>, teardown: () => void}}
 */
export function mountAgentsSection(container, { announce }) {
  const controller = new AbortController();

  async function refresh() {
    clearChildren(container);
    container.append(agentEnrollmentCard({ signal: controller.signal, announce }));

    const listCard = el("div", { class: "cp-card" });
    container.append(listCard);
    await renderAgentListCard(listCard, { signal: controller.signal });
  }

  function teardown() {
    controller.abort();
  }

  return { refresh, teardown };
}

// ---------------------------------------------------------------------------
// Agent enrollment card: reveal/copy token + install command
// ---------------------------------------------------------------------------

function agentEnrollmentCard({ signal, announce }) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { class: "cp-card-title", text: t("agents.enrollment_title") }));
  card.append(
    el("p", {
      class: "cp-muted-small",
      text: t("agents.enrollment_description"),
    }),
  );

  const tokenRow = el("div", { class: "cp-settings-row" });
  const tokenInput = /** @type {HTMLInputElement} */ (
    el("input", {
      class: "cp-input cp-token-reveal-input",
      attrs: { type: "text", readonly: "", value: "", "aria-label": t("agents.agent_token"), hidden: "" },
    })
  );
  const revealBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: t("agents.reveal") });
  const copyTokenBtn = el("button", {
    class: "cp-btn cp-btn-secondary",
    attrs: { type: "button", hidden: "" },
    text: t("common.copy"),
  });
  const hideBtn = el("button", {
    class: "cp-btn cp-btn-secondary",
    attrs: { type: "button", hidden: "" },
    text: t("agents.hide_again"),
  });
  const tokenStatus = el("span", { class: "cp-muted-small", attrs: { role: "status", "aria-live": "polite" } });

  let installCommand = "";
  const installBlock = el("pre", { class: "cp-code-block cp-code-block-wrap", attrs: { hidden: "" } });
  const installCode = el("code", { text: "" });
  installBlock.append(installCode);
  const copyInstallBtn = el("button", {
    class: "cp-btn cp-btn-secondary",
    attrs: { type: "button", hidden: "" },
    text: t("agents.copy_install_command"),
  });
  const installFallback = /** @type {HTMLTextAreaElement} */ (
    el("textarea", {
      class: "cp-input cp-install-fallback",
      attrs: { readonly: "", rows: "2", hidden: "", "aria-label": t("agents.install_command") },
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

      tokenStatus.textContent = t("agents.token_revealed");
      announce(t("agents.agent_token_revealed"));
    } catch (err) {
      if (err?.name === "AbortError") return;
      tokenStatus.textContent = t("agents.failed_to_reveal_token", { reason: localizeApiError(err) });
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
    tokenStatus.textContent = t("agents.token_hidden");
  });

  copyTokenBtn.addEventListener("click", async () => {
    const result = await copyToClipboard(tokenInput.value, tokenInput);
    tokenStatus.textContent = result === "copied" ? t("agents.token_copied") : t("agents.press_ctrl_c");
  });

  copyInstallBtn.addEventListener("click", async () => {
    const result = await copyToClipboard(installCommand, installFallback);
    tokenStatus.textContent =
      result === "copied" ? t("agents.install_command_copied") : t("agents.press_ctrl_c");
  });

  tokenRow.append(revealBtn, tokenInput, copyTokenBtn, hideBtn, tokenStatus);
  card.append(tokenRow);
  card.append(installBlock);
  card.append(copyInstallBtn);
  card.append(installFallback);

  return card;
}

// ---------------------------------------------------------------------------
// Agent list card
// ---------------------------------------------------------------------------

async function renderAgentListCard(card, { signal }) {
  clearChildren(card);
  card.append(el("h2", { class: "cp-card-title", text: t("agents.agents_title") }));

  let hosts;
  try {
    const data = await getHosts(signal);
    hosts = data.hosts || [];
  } catch (err) {
    if (err?.name === "AbortError") return;
    card.append(el("p", { class: "cp-error-text", text: t("agents.failed_to_load_agents", { reason: localizeApiError(err) }) }));
    return;
  }

  if (hosts.length === 0) {
    card.append(el("p", { class: "cp-muted", text: t("agents.no_agents_reporting_yet") }));
    return;
  }

  const tableWrap = el("div", { class: "cp-table-wrap" });
  const table = el("table", { class: "cp-table" });
  const thead = el("thead");
  thead.append(
    el("tr", { children: [t("agents.col_host"), t("agents.col_provider"), t("agents.col_agent_version"), t("agents.col_last_seen"), t("agents.col_update")].map((tx) => el("th", { text: tx })) }),
  );
  table.append(thead);
  const tbody = el("tbody");
  for (const h of hosts) {
    const tr = el("tr");
    tr.append(el("td", { text: h.host.hostname }));
    tr.append(el("td", { text: h.host.provider || t("agents.provider_other") }));
    tr.append(el("td", { text: agentVersionText(h.host.agent_version) }));
    tr.append(
      el("td", {
        text: h.last_seen ? formatRelativeTimeFromUnixSeconds(h.last_seen) : t("settings.never"),
      }),
    );

    const updateCell = el("td");
    const badge = hostUpdateBadgeText(h.update);
    if (badge) {
      const chip = el("span", { class: "cp-chip cp-chip-warning", attrs: { title: badge.label }, text: badge.text });
      updateCell.append(chip);
      if (h.update?.command) {
        updateCell.append(el("code", { class: "cp-code-inline", text: h.update.command }));
      }
    } else {
      updateCell.append(el("span", { class: "cp-muted-small", text: t("agents.up_to_date") }));
    }
    tr.append(updateCell);
    tbody.append(tr);
  }
  table.append(tbody);
  tableWrap.append(table);
  card.append(tableWrap);
}
