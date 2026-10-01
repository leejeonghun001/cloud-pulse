// settings/general.js — General settings section: hub build version +
// self-update status, read-only hub info (offline threshold, cloud
// interval), and an Appearance theme selector.
import { el, clearChildren } from "../../ui/components.js";
import { selectField } from "../../ui/select.js";
import { languageSelector } from "../../ui/language-select.js";
import { t } from "../../core/i18n.js";
import { formatRelativeTimeFromUnixSeconds } from "../../core/format.js";
import { hubInfoUpdateFields } from "../../core/updates.js";
import { getSettings, getVersion } from "../../core/api.js";
import { getStoredTheme, setStoredTheme, applyTheme } from "../../core/theme.js";
import { copyToClipboard } from "./index.js";

/**
 * mountGeneralSection renders the General section into container.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @returns {{refresh: () => Promise<void>, teardown: () => void}}
 */
export function mountGeneralSection(container, { announce }) {
  const controller = new AbortController();

  async function refresh() {
    clearChildren(container);

    let view;
    let versionInfo = null;
    try {
      view = await getSettings(controller.signal);
    } catch (err) {
      if (err?.name === "AbortError") return;
      throw err;
    }
    try {
      versionInfo = await getVersion(controller.signal);
    } catch (err) {
      if (err?.name === "AbortError") return;
      // A failed version check shouldn't block the rest of the section.
    }

    container.append(versionCard(view, versionInfo));
    container.append(hubInfoCard(view));
    container.append(appearanceCard({ announce }));
    container.append(languageCard({ announce, onLanguageChange: refresh }));
  }

  function teardown() {
    controller.abort();
  }

  return { refresh, teardown };
}

function versionCard(view, versionInfo) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { class: "cp-card-title", text: t("settings.version_title") }));

  const update = hubInfoUpdateFields(versionInfo || {});
  const dl = el("dl", { class: "cp-host-detail-meta" });
  const entries = [
    [t("settings.running_version"), view.version || t("overview.unknown")],
    [t("settings.latest_known_version"), update.latest],
    [t("settings.update_checks"), update.checkEnabled ? t("settings.enabled") : t("settings.disabled")],
    [
      t("settings.last_checked"),
      update.checkEnabled && update.checkedAtUnixSeconds
        ? formatRelativeTimeFromUnixSeconds(update.checkedAtUnixSeconds)
        : t("settings.never"),
    ],
  ];
  for (const [label, value] of entries) {
    const pair = el("div", { class: "cp-host-detail-meta-pair" });
    pair.append(el("dt", { text: label }), el("dd", { text: value }));
    dl.append(pair);
  }
  card.append(dl);

  if (update.checkError) {
    card.append(el("p", { class: "cp-hint", text: t("settings.last_update_check_failed", { error: update.checkError }) }));
  }

  if (versionInfo?.update_available && versionInfo?.update_command) {
    const row = el("div", { class: "cp-settings-row" });
    const code = el("code", { class: "cp-code-inline", text: versionInfo.update_command });
    const copyBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" }, text: t("common.copy") });
    const status = el("span", { class: "cp-muted-small", attrs: { role: "status", "aria-live": "polite" } });
    copyBtn.addEventListener("click", async () => {
      const result = await copyToClipboard(versionInfo.update_command);
      status.textContent = result === "copied" ? t("settings.copied") : t("settings.select_and_copy");
    });
    row.append(code, copyBtn, status);
    card.append(row);
    if (versionInfo.release_url) {
      const link = el("a", {
        class: "cp-link",
        attrs: { href: versionInfo.release_url, target: "_blank", rel: "noopener noreferrer" },
        text: t("host.release_notes"),
      });
      card.append(link);
    }
  }

  return card;
}

function hubInfoCard(view) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { class: "cp-card-title", text: t("settings.hub_info_title") }));

  const dl = el("dl", { class: "cp-host-detail-meta" });
  const entries = [
    [t("settings.offline_after"), `${view.offline_after_seconds}s`],
    [t("settings.cloud_interval"), `${view.cloud_interval_seconds}s`],
    [t("settings.agent_token"), view.agent_token_hint],
    [t("settings.api_token"), view.ui_auth_enabled ? t("settings.configured") : t("settings.not_configured_lower")],
  ];
  for (const [label, value] of entries) {
    const pair = el("div", { class: "cp-host-detail-meta-pair" });
    pair.append(el("dt", { text: label }), el("dd", { text: value }));
    dl.append(pair);
  }
  card.append(dl);
  return card;
}

function appearanceCard({ announce }) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { class: "cp-card-title", text: t("settings.appearance_title") }));
  card.append(el("p", { class: "cp-muted-small", text: t("settings.appearance_description") }));

  const { node, select } = selectField({
    id: "cp-theme-select",
    label: t("settings.theme"),
    options: [
      { value: "system", get label() { return t("settings.theme_system"); } },
      { value: "light", get label() { return t("settings.theme_light"); } },
      { value: "dark", get label() { return t("settings.theme_dark"); } },
    ],
    value: getStoredTheme(),
  });

  select.addEventListener("change", () => {
    setStoredTheme(select.value);
    applyTheme(select.value);
    announce(t("settings.theme_set_to", { theme: select.value }));
  });

  card.append(node);
  return card;
}

/**
 * languageCard renders the dashboard language selector (SPEC-v0.8
 * §3-3): Auto / English / 한국어, persisted to localStorage "cp_lang".
 * Switching re-renders the General section in place via
 * onLanguageChange (this section has no other in-flight state worth
 * preserving, so a full section refresh — not a full page reload — is
 * the cheap option here).
 * @param {Object} opts
 * @param {(text: string) => void} opts.announce
 * @param {() => void} opts.onLanguageChange
 * @returns {HTMLElement}
 */
function languageCard({ announce, onLanguageChange }) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { class: "cp-card-title", text: t("common.language") }));

  const { node } = languageSelector({
    onChange: () => {
      announce(t("common.language"));
      onLanguageChange();
    },
  });
  card.append(node);
  return card;
}
