// settings/security.js — Security section: change-password form (with
// the same live requirement checklist/strength meter as the forced
// change modal), an active-sessions table with "Sign out other
// sessions", and a read-only API token (CP_UI_TOKEN) status panel.
import { el, clearChildren } from "../../ui/components.js";
import { passwordField, passwordChecklist, passwordStrengthMeter } from "../../ui/password-field.js";
import { isPasswordValid } from "../../core/password-policy.js";
import { formatRelativeTimeFromUnixSeconds } from "../../core/format.js";
import { getSessions, revokeOtherSessions, changePassword, getSettings, ApiError } from "../../core/api.js";
import { setSessionToken } from "../../core/auth.js";
import { showToast } from "../../ui/toast.js";

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
  card.append(el("h2", { class: "cp-card-title", text: "Change password" }));
  card.append(el("p", { class: "cp-muted-small", text: "Signs you out of every other session on success." }));

  const form = el("form", { class: "cp-settings-form" });
  form.addEventListener("submit", (ev) => ev.preventDefault());

  const { node: curNode, input: curInput } = passwordField({
    id: "cp-security-current",
    label: "Current password",
    autocomplete: "current-password",
  });
  const { node: newNode, input: newInput } = passwordField({
    id: "cp-security-new",
    label: "New password",
    autocomplete: "new-password",
  });
  const { node: confirmNode, input: confirmInput } = passwordField({
    id: "cp-security-confirm",
    label: "Confirm new password",
    autocomplete: "new-password",
  });
  form.append(curNode, newNode, confirmNode);

  const checklist = passwordChecklist();
  const strength = passwordStrengthMeter();
  form.append(checklist.node, strength.node);

  const errorEl = el("p", { class: "cp-error-text", attrs: { role: "alert" } });
  form.append(errorEl);

  const submitBtn = /** @type {HTMLButtonElement} */ (
    el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "submit" }, text: "Update password" })
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
      errorEl.textContent = "Please satisfy every password requirement.";
      return;
    }
    if (newInput.value !== confirmInput.value) {
      errorEl.textContent = "Passwords do not match.";
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
      showToast({ message: "Password updated. Other sessions were signed out.", variant: "success" });
      announce("Password updated.");
    } catch (err) {
      if (err?.name === "AbortError") return;
      if (err instanceof ApiError) {
        errorEl.textContent = err.code === "invalid_credentials" ? "Current password is incorrect." : err.message;
      } else {
        errorEl.textContent = "Unable to reach the hub. Try again.";
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
  card.append(el("h2", { class: "cp-card-title", text: "Active sessions" }));

  let sessions;
  try {
    sessions = await getSessions(signal);
  } catch (err) {
    if (err?.name === "AbortError") return;
    card.append(el("p", { class: "cp-error-text", text: `Failed to load sessions: ${err.message}` }));
    return;
  }

  const revokeBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" }, text: "Sign out other sessions" });
  const status = el("span", { class: "cp-muted-small", attrs: { role: "status", "aria-live": "polite" } });
  const headerRow = el("div", { class: "cp-settings-row" });
  headerRow.append(revokeBtn, status);
  card.append(headerRow);

  revokeBtn.addEventListener("click", async () => {
    revokeBtn.disabled = true;
    try {
      const result = await revokeOtherSessions(signal);
      status.textContent = `Signed out ${result.revoked} other session${result.revoked === 1 ? "" : "s"}.`;
      announce(status.textContent);
      await renderSessionsCard(card, { signal, announce });
    } catch (err) {
      if (err?.name === "AbortError") return;
      status.textContent = `Failed: ${err.message}`;
    } finally {
      revokeBtn.disabled = false;
    }
  });

  if (sessions.length === 0) {
    card.append(el("p", { class: "cp-muted", text: "No active sessions." }));
    return;
  }

  const tableWrap = el("div", { class: "cp-table-wrap" });
  const table = el("table", { class: "cp-table" });
  const thead = el("thead");
  thead.append(
    el("tr", { children: ["Session", "Created", "Last seen", "Expires", "Remote", ""].map((t) => el("th", { text: t })) }),
  );
  table.append(thead);
  const tbody = el("tbody");
  for (const s of sessions) {
    const tr = el("tr");
    if (s.current) tr.classList.add("cp-row-highlighted");
    tr.append(el("td", { text: s.current ? `${s.id} (this session)` : s.id }));
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
  card.append(el("h2", { class: "cp-card-title", text: "API token" }));
  const badge = el("span", {
    class: `cp-chip ${view.ui_auth_enabled ? "cp-chip-ok" : "cp-chip-warning"}`,
    text: view.ui_auth_enabled ? "Configured" : "Not configured",
  });
  card.append(badge);
  card.append(
    el("p", {
      class: "cp-muted-small",
      text: "CP_UI_TOKEN is an optional static bearer token for scripts and CI (curl, agents don't use it) — full read/admin access, never subject to the must-change-password gate. It never bypasses this dashboard's own sign-in. Set or rotate it in hub.env and restart the service.",
    }),
  );
  return card;
}
