// settings/security.js — Security section: change-password form (with
// the same live requirement checklist/strength meter as the forced
// change modal), an active-sessions table with "Sign out other
// sessions", and a read-only API token (CP_UI_TOKEN) status panel.
import { el, clearChildren } from "../../ui/components.js";
import { passwordField, passwordChecklist, passwordStrengthMeter } from "../../ui/password-field.js";
import { isPasswordValid } from "../../core/password-policy.js";
import { formatRelativeTimeFromUnixSeconds } from "../../core/format.js";
import { getSessions, revokeOtherSessions, changePassword, getSettings, ApiError, localizeApiError } from "../../core/api.js";
import { setSessionToken } from "../../core/auth.js";
import { showToast } from "../../ui/toast.js";
import { t } from "../../core/i18n.js";

/**
 * mountSecuritySection renders the Security section into container.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @returns {{refresh: () => Promise<void>, teardown: () => void}}
 */
export function mountSecuritySection(container, { announce }) {
  const controller = new AbortController();

  async function refresh() {
    clearChildren(container);

    container.append(changePasswordCard({ signal: controller.signal, announce }));

    const sessionsCard = el("div", { class: "cp-card" });
    container.append(sessionsCard);
    await renderSessionsCard(sessionsCard, { signal: controller.signal, announce });

    try {
      const view = await getSettings(controller.signal);
      container.append(apiTokenCard(view));
    } catch (err) {
      if (err?.name === "AbortError") return;
      // Non-fatal: the rest of the section still renders.
    }
  }

  function teardown() {
    controller.abort();
  }

  return { refresh, teardown };
}

// ---------------------------------------------------------------------------
// Change password card
// ---------------------------------------------------------------------------

function changePasswordCard({ signal, announce }) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { class: "cp-card-title", text: t("security.change_password_title") }));
  card.append(el("p", { class: "cp-muted-small", text: t("security.change_password_description") }));

  const form = el("form", { class: "cp-settings-form" });
  form.addEventListener("submit", (ev) => ev.preventDefault());

  const { node: curNode, input: curInput } = passwordField({
    id: "cp-security-current",
    label: t("login.current_password_label"),
    autocomplete: "current-password",
  });
  const { node: newNode, input: newInput } = passwordField({
    id: "cp-security-new",
    label: t("login.new_password_label"),
    autocomplete: "new-password",
  });
  const { node: confirmNode, input: confirmInput } = passwordField({
    id: "cp-security-confirm",
    label: t("login.confirm_password_label"),
    autocomplete: "new-password",
  });
  form.append(curNode, newNode, confirmNode);

  const checklist = passwordChecklist();
  const strength = passwordStrengthMeter();
  form.append(checklist.node, strength.node);

  const errorEl = el("p", { class: "cp-error-text", attrs: { role: "alert" } });
  form.append(errorEl);

  const submitBtn = /** @type {HTMLButtonElement} */ (
    el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "submit" }, text: t("security.update_password") })
  );
  form.append(submitBtn);

  function refreshLive() {
    checklist.update(newInput.value, curInput.value);
    strength.update(newInput.value);
  }
  curInput.addEventListener("input", refreshLive);
  newInput.addEventListener("input", refreshLive);
  refreshLive();

  form.addEventListener("submit", async () => {
    errorEl.textContent = "";
    if (!isPasswordValid(newInput.value, curInput.value)) {
      errorEl.textContent = t("login.password_requirements_unmet");
      return;
    }
    if (newInput.value !== confirmInput.value) {
      errorEl.textContent = t("login.passwords_do_not_match");
      return;
    }
    submitBtn.disabled = true;
    try {
      const resp = await changePassword(curInput.value, newInput.value, signal);
      setSessionToken(resp.token);
      curInput.value = "";
      newInput.value = "";
      confirmInput.value = "";
      refreshLive();
      showToast({ message: t("security.password_updated_toast"), variant: "success" });
      announce(t("login.password_updated"));
    } catch (err) {
      if (err?.name === "AbortError") return;
      if (err instanceof ApiError) {
        errorEl.textContent = err.code === "invalid_credentials" ? t("login.current_password_incorrect") : localizeApiError(err);
      } else {
        errorEl.textContent = t("login.unreachable_retry");
      }
    } finally {
      submitBtn.disabled = false;
    }
  });

  card.append(form);
  return card;
}

// ---------------------------------------------------------------------------
// Sessions card
// ---------------------------------------------------------------------------

async function renderSessionsCard(card, { signal, announce }) {
  clearChildren(card);
  card.append(el("h2", { class: "cp-card-title", text: t("security.active_sessions_title") }));

  let sessions;
  try {
    sessions = await getSessions(signal);
  } catch (err) {
    if (err?.name === "AbortError") return;
    card.append(el("p", { class: "cp-error-text", text: t("security.failed_to_load_sessions", { reason: localizeApiError(err) }) }));
    return;
  }

  const revokeBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" }, text: t("security.sign_out_other_sessions") });
  const status = el("span", { class: "cp-muted-small", attrs: { role: "status", "aria-live": "polite" } });
  const headerRow = el("div", { class: "cp-settings-row" });
  headerRow.append(revokeBtn, status);
  card.append(headerRow);

  revokeBtn.addEventListener("click", async () => {
    revokeBtn.disabled = true;
    try {
      const result = await revokeOtherSessions(signal);
      status.textContent = t("security.signed_out_sessions", { count: result.revoked });
      announce(status.textContent);
      await renderSessionsCard(card, { signal, announce });
    } catch (err) {
      if (err?.name === "AbortError") return;
      status.textContent = t("security.revoke_failed", { reason: localizeApiError(err) });
    } finally {
      revokeBtn.disabled = false;
    }
  });

  if (sessions.length === 0) {
    card.append(el("p", { class: "cp-muted", text: t("security.no_active_sessions") }));
    return;
  }

  const tableWrap = el("div", { class: "cp-table-wrap" });
  const table = el("table", { class: "cp-table" });
  const thead = el("thead");
  thead.append(
    el("tr", { children: [t("security.col_session"), t("security.col_created"), t("security.col_last_seen"), t("security.col_expires"), t("security.col_remote"), ""].map((tx) => el("th", { text: tx })) }),
  );
  table.append(thead);
  const tbody = el("tbody");
  for (const s of sessions) {
    const tr = el("tr");
    if (s.current) tr.classList.add("cp-row-highlighted");
    tr.append(el("td", { text: s.current ? t("security.this_session", { id: s.id }) : s.id }));
    tr.append(el("td", { text: formatRelativeTimeFromUnixSeconds(s.created_at) }));
    tr.append(el("td", { text: formatRelativeTimeFromUnixSeconds(s.last_seen) }));
    tr.append(el("td", { text: formatRelativeTimeFromUnixSeconds(s.expires_at) }));
    tr.append(el("td", { text: s.remote || "—" }));
    tr.append(el("td", { text: s.user_agent ? truncate(s.user_agent, 40) : "—" }));
    tbody.append(tr);
  }
  table.append(tbody);
  tableWrap.append(table);
  card.append(tableWrap);
}

function truncate(s, max) {
  return s.length > max ? `${s.slice(0, max - 1)}…` : s;
}

// ---------------------------------------------------------------------------
// API token status card
// ---------------------------------------------------------------------------

function apiTokenCard(view) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { class: "cp-card-title", text: t("security.api_token_title") }));
  const badge = el("span", {
    class: `cp-chip ${view.ui_auth_enabled ? "cp-chip-ok" : "cp-chip-warning"}`,
    text: view.ui_auth_enabled ? t("settings.configured") : t("settings.not_configured_lower"),
  });
  card.append(badge);
  card.append(
    el("p", {
      class: "cp-muted-small",
      text: t("security.api_token_description"),
    }),
  );
  return card;
}
