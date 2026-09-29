// settings/notifications.js — Notifications section: alert webhook URL
// editor (Slack/Discord-compatible) + a "send test" action, restyled
// from v0.3.x's single-page settings into its own sidebar section.
import { el, clearChildren } from "../../ui/components.js";
import { getSettings, setAlertWebhook, testAlertWebhook } from "../../core/api.js";

/**
 * mountNotificationsSection renders the Notifications section into
 * container.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @returns {{refresh: () => Promise<void>, teardown: () => void}}
 */
export function mountNotificationsSection(container, { announce }) {
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

    container.append(alertsCard({ view, signal: controller.signal, announce }));
  }

  function teardown() {
    controller.abort();
  }

  return { refresh, teardown };
}

function alertsCard({ view, signal, announce }) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { class: "cp-card-title", text: "Alert webhook" }));
  card.append(
    el("p", {
      class: "cp-muted-small",
      text: "Slack- or Discord-compatible webhook URL for egress threshold alerts (80%/95%/100% of a host's outbound and inbound limits).",
    }),
  );

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
