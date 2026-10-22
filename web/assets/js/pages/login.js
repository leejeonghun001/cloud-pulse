// login.js — #/login page: centered card, logo, username prefilled
// "admin", password with show/hide, submit spinner, inline error,
// rate-limit countdown from retry_after_seconds, return-to the
// originally requested route. Also owns the forced change-password
// modal shown after a login whose response has
// must_change_password: true (or a 403 password_change_required from
// any later API call, wired by the shell via api.js's
// onPasswordChangeRequired hook).
import { el, clearChildren } from "../ui/components.js";
import { icon } from "../ui/icons.js";
import { passwordField, passwordChecklist, passwordStrengthMeter } from "../ui/password-field.js";
import { createDialog, buildDialogActions } from "../ui/dialog.js";
import { isPasswordValid } from "../core/password-policy.js";
import { login, changePassword, ApiError, RateLimitError } from "../core/api.js";
import { setSessionToken } from "../core/auth.js";
import { t, htmlLang } from "../core/i18n.js";
import { languageSelector } from "../ui/language-select.js";
import { showToast } from "../ui/toast.js";

/**
 * mountLoginPage renders the login page into container.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(loginResp: Object) => void} ctx.onLoggedIn called with
 *   models.LoginResponse once a session token has been stored; the
 *   shell navigates to the return-to route and refreshes auth state.
 * @returns {{teardown: () => void}}
 */
export function mountLoginPage(container, { onLoggedIn }) {
  clearChildren(container);
  document.documentElement.lang = htmlLang();

  const page = el("div", { class: "cp-auth-page" });
  const card = el("div", { class: "cp-auth-card" });

  const brand = el("div", { class: "cp-auth-brand" });
  brand.append(icon("activity"), el("h1", { text: "cloud-pulse" }));
  card.append(brand);
  card.append(el("p", { class: "cp-page-subtitle", text: t("login.subtitle"), attrs: { style: "text-align:center" } }));

  const form = el("form", { class: "cp-auth-form" });

  const usernameField = el("div", { class: "cp-field" });
  usernameField.append(el("label", { class: "cp-label", attrs: { for: "cp-login-username" }, text: t("login.username_label") }));
  const usernameInput = /** @type {HTMLInputElement} */ (
    el("input", {
      class: "cp-input",
      attrs: { id: "cp-login-username", name: "username", type: "text", autocomplete: "username", required: "", value: "admin" },
    })
  );
  usernameField.append(usernameInput);
  form.append(usernameField);

  const { node: pwNode, input: passwordInput } = passwordField({
    id: "cp-login-password",
    label: t("login.password_label"),
    autocomplete: "current-password",
  });
  form.append(pwNode);

  const errorEl = el("p", { class: "cp-error-text", attrs: { role: "alert" } });
  form.append(errorEl);

  const submitRow = el("div", { class: "cp-auth-submit-row" });
  const submitBtn = /** @type {HTMLButtonElement} */ (
    el("button", { class: "cp-btn cp-btn-primary cp-btn-block", attrs: { type: "submit" }, text: t("login.sign_in") })
  );
  submitRow.append(submitBtn);
  form.append(submitRow);

  card.append(form);

  const langRow = el("div", { class: "cp-auth-language-row" });
  const { node: langNode } = languageSelector({
    onChange: () => {
      // Cheap to re-render in place: the login page owns no
      // in-flight requests or timers worth preserving across a
      // language switch (unlike an authenticated page's auto-refresh
      // loop), so a full remount is simplest and correct.
      mountLoginPage(container, { onLoggedIn });
    },
  });
  langRow.append(langNode);
  card.append(langRow);

  card.append(el("p", { class: "cp-auth-footer", text: t("login.footer") }));
  page.append(card);
  container.append(page);

  let countdownTimer = null;
  function clearCountdown() {
    if (countdownTimer) {
      clearInterval(countdownTimer);
      countdownTimer = null;
    }
  }

  function setSubmitting(submitting) {
    submitBtn.disabled = submitting;
    clearChildren(submitBtn);
    if (submitting) {
      submitBtn.append(el("span", { class: "cp-spinner" }), el("span", { text: t("login.signing_in") }));
    } else {
      submitBtn.textContent = t("login.sign_in");
    }
  }

  function startRateLimitCountdown(seconds) {
    clearCountdown();
    let remaining = Math.max(1, Math.round(seconds));
    submitBtn.disabled = true;
    const render = () => {
      errorEl.textContent = t("login.rate_limited", { seconds: remaining });
    };
    render();
    countdownTimer = setInterval(() => {
      remaining -= 1;
      if (remaining <= 0) {
        clearCountdown();
        errorEl.textContent = "";
        submitBtn.disabled = false;
        return;
      }
      render();
    }, 1000);
  }

  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    errorEl.textContent = "";
    clearCountdown();
    setSubmitting(true);
    try {
      const resp = await login(usernameInput.value.trim(), passwordInput.value);
      setSessionToken(resp.token);
      if (resp.must_change_password) {
        openForcedChangeModal({
          currentPassword: passwordInput.value,
          loginResp: resp,
          onDone: (finalResp) => onLoggedIn(finalResp),
        });
        return;
      }
      onLoggedIn(resp);
    } catch (err) {
      if (err instanceof RateLimitError) {
        startRateLimitCountdown(err.retryAfterSeconds || 60);
      } else if (err instanceof ApiError) {
        errorEl.textContent = err.code === "invalid_credentials" ? t("login.invalid_credentials") : err.message;
      } else {
        errorEl.textContent = t("login.unreachable");
      }
    } finally {
      if (!countdownTimer) setSubmitting(false);
    }
  });

  function teardown() {
    clearCountdown();
  }

  return { teardown };
}

/**
 * openForcedChangeModal shows the non-dismissable "Set a new password"
 * modal after a login (or a later 403 password_change_required)
 * indicates the account must change its password. currentPassword is
 * kept in memory only (never persisted) so the user isn't asked to
 * retype it when it's already known (fresh login); pass "" when it
 * isn't known (a 403 triggered mid-session) to force the current
 * -password field to be filled in.
 * @param {Object} opts
 * @param {string} opts.currentPassword
 * @param {Object} [opts.loginResp] the triggering LoginResponse, if any
 * @param {(finalResp: Object) => void} opts.onDone called with the
 *   fresh LoginResponse from POST /api/v1/auth/password once the
 *   password has been changed successfully.
 */
export function openForcedChangeModal({ currentPassword, onDone }) {
  const { dialog, body, open, close } = createDialog({
    titleId: "cp-forced-change-title",
    title: t("login.forced_change_title"),
    description: t("login.forced_change_description"),
    nonDismissable: true,
  });

  const form = el("form");
  form.addEventListener("submit", (ev) => ev.preventDefault());

  const needsCurrent = !currentPassword;
  let currentInput = null;
  if (needsCurrent) {
    const { node, input } = passwordField({ id: "cp-forced-current", label: t("login.current_password_label"), autocomplete: "current-password" });
    form.append(node);
    currentInput = input;
  }

  const { node: newNode, input: newInput } = passwordField({
    id: "cp-forced-new",
    label: t("login.new_password_label"),
    autocomplete: "new-password",
  });
  const { node: confirmNode, input: confirmInput } = passwordField({
    id: "cp-forced-confirm",
    label: t("login.confirm_password_label"),
    autocomplete: "new-password",
  });
  form.append(newNode, confirmNode);

  const checklist = passwordChecklist();
  const strength = passwordStrengthMeter();
  form.append(checklist.node, strength.node);

  const errorEl = el("p", { class: "cp-error-text", attrs: { role: "alert" } });
  form.append(errorEl);

  const submitBtn = /** @type {HTMLButtonElement} */ (
    el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "submit" }, text: t("login.set_password") })
  );
  form.append(buildDialogActions([submitBtn]));

  body.append(form);

  function refreshLive() {
    const cur = needsCurrent ? currentInput.value : currentPassword;
    checklist.update(newInput.value, cur);
    strength.update(newInput.value);
  }
  newInput.addEventListener("input", refreshLive);
  if (currentInput) currentInput.addEventListener("input", refreshLive);
  refreshLive();

  form.addEventListener("submit", async () => {
    errorEl.textContent = "";
    const cur = needsCurrent ? currentInput.value : currentPassword;
    if (!isPasswordValid(newInput.value, cur)) {
      errorEl.textContent = t("login.password_requirements_unmet");
      checklist.reveal();
      return;
    }
    if (newInput.value !== confirmInput.value) {
      errorEl.textContent = t("login.passwords_do_not_match");
      return;
    }
    submitBtn.disabled = true;
    try {
      const resp = await changePassword(cur, newInput.value);
      setSessionToken(resp.token);
      close();
      showToast({ message: t("login.password_updated"), variant: "success" });
      onDone(resp);
    } catch (err) {
      if (err instanceof ApiError) {
        errorEl.textContent = err.code === "invalid_credentials" ? t("login.current_password_incorrect") : err.message;
      } else {
        errorEl.textContent = t("login.unreachable_retry");
      }
    } finally {
      submitBtn.disabled = false;
    }
  });

  open();
}
