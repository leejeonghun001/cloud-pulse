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

  const page = el("div", { class: "cp-auth-page" });
  const card = el("div", { class: "cp-auth-card" });

  const brand = el("div", { class: "cp-auth-brand" });
  brand.append(icon("activity"), el("h1", { text: "cloud-pulse" }));
  card.append(brand);
  card.append(el("p", { class: "cp-page-subtitle", text: "Sign in to your hub", attrs: { style: "text-align:center" } }));

  const form = el("form", { class: "cp-auth-form" });

  const usernameField = el("div", { class: "cp-field" });
  usernameField.append(el("label", { class: "cp-label", attrs: { for: "cp-login-username" }, text: "Username" }));
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
    label: "Password",
    autocomplete: "current-password",
  });
  form.append(pwNode);

  const errorEl = el("p", { class: "cp-error-text", attrs: { role: "alert" } });
  form.append(errorEl);

  const submitRow = el("div", { class: "cp-auth-submit-row" });
  const submitBtn = /** @type {HTMLButtonElement} */ (
    el("button", { class: "cp-btn cp-btn-primary cp-btn-block", attrs: { type: "submit" }, text: "Sign in" })
  );
  submitRow.append(submitBtn);
  form.append(submitRow);

  card.append(form);
  card.append(el("p", { class: "cp-auth-footer", text: "cloud-pulse hub" }));
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
      submitBtn.append(el("span", { class: "cp-spinner" }), el("span", { text: "Signing in…" }));
    } else {
      submitBtn.textContent = "Sign in";
    }
  }

  function startRateLimitCountdown(seconds) {
    clearCountdown();
    let remaining = Math.max(1, Math.round(seconds));
    submitBtn.disabled = true;
    const render = () => {
      errorEl.textContent = `Too many attempts. Try again in ${remaining}s.`;
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
        errorEl.textContent = err.code === "invalid_credentials" ? "Invalid username or password." : err.message;
      } else {
        errorEl.textContent = "Unable to reach the hub. Check your connection and try again.";
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
    title: "Set a new password",
    description: "You're signing in with the default password. Choose a new one to continue.",
    nonDismissable: true,
  });

  const form = el("form");
  form.addEventListener("submit", (ev) => ev.preventDefault());

  const needsCurrent = !currentPassword;
  let currentInput = null;
  if (needsCurrent) {
    const { node, input } = passwordField({ id: "cp-forced-current", label: "Current password", autocomplete: "current-password" });
    form.append(node);
    currentInput = input;
  }

  const { node: newNode, input: newInput } = passwordField({
    id: "cp-forced-new",
    label: "New password",
    autocomplete: "new-password",
  });
  const { node: confirmNode, input: confirmInput } = passwordField({
    id: "cp-forced-confirm",
    label: "Confirm new password",
    autocomplete: "new-password",
  });
  form.append(newNode, confirmNode);

  const checklist = passwordChecklist();
  const strength = passwordStrengthMeter();
  form.append(checklist.node, strength.node);

  const errorEl = el("p", { class: "cp-error-text", attrs: { role: "alert" } });
  form.append(errorEl);

  const submitBtn = /** @type {HTMLButtonElement} */ (
    el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "submit" }, text: "Set password" })
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
      errorEl.textContent = "Please satisfy every password requirement.";
      return;
    }
    if (newInput.value !== confirmInput.value) {
      errorEl.textContent = "Passwords do not match.";
      return;
    }
    submitBtn.disabled = true;
    try {
      const resp = await changePassword(cur, newInput.value);
      setSessionToken(resp.token);
      close();
      showToast({ message: "Password updated.", variant: "success" });
      onDone(resp);
    } catch (err) {
      if (err instanceof ApiError) {
        errorEl.textContent = err.code === "invalid_credentials" ? "Current password is incorrect." : err.message;
      } else {
        errorEl.textContent = "Unable to reach the hub. Try again.";
      }
    } finally {
      submitBtn.disabled = false;
    }
  });

  open();
}
