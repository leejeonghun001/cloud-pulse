// diagnosis-view.test.mjs — unit tests for
// assets/js/core/diagnosis-view.js (SPEC-v0.6 §4: diagnosis → view
// model, incl. which Config field to highlight per code/platform).
import { test } from "node:test";
import assert from "node:assert/strict";
import { fieldToHighlight, diagnosisViewModel, diagnosisSummaryText } from "../assets/js/core/diagnosis-view.js";

test("fieldToHighlight: discord webhook-not-found highlights webhook_url", () => {
  assert.equal(fieldToHighlight("discord", "discord_webhook_not_found"), "webhook_url");
});

test("fieldToHighlight: discord unauthorized highlights webhook_url", () => {
  assert.equal(fieldToHighlight("discord", "discord_unauthorized"), "webhook_url");
});

test("fieldToHighlight: telegram unauthorized highlights bot_token", () => {
  assert.equal(fieldToHighlight("telegram", "telegram_unauthorized"), "bot_token");
});

test("fieldToHighlight: telegram chat_not_found highlights chat_id", () => {
  assert.equal(fieldToHighlight("telegram", "telegram_chat_not_found"), "chat_id");
});

test("fieldToHighlight: telegram bot_blocked and not_member both highlight chat_id", () => {
  assert.equal(fieldToHighlight("telegram", "telegram_bot_blocked"), "chat_id");
  assert.equal(fieldToHighlight("telegram", "telegram_not_member"), "chat_id");
});

test("fieldToHighlight: telegram thread_not_found highlights message_thread_id", () => {
  assert.equal(fieldToHighlight("telegram", "telegram_thread_not_found"), "message_thread_id");
});

test("fieldToHighlight: whatsapp token_invalid and permission highlight access_token", () => {
  assert.equal(fieldToHighlight("whatsapp", "whatsapp_token_invalid"), "access_token");
  assert.equal(fieldToHighlight("whatsapp", "whatsapp_permission"), "access_token");
});

test("fieldToHighlight: whatsapp recipient_not_allowed highlights to", () => {
  assert.equal(fieldToHighlight("whatsapp", "whatsapp_recipient_not_allowed"), "to");
});

test("fieldToHighlight: whatsapp window_closed and template_missing highlight template_name", () => {
  assert.equal(fieldToHighlight("whatsapp", "whatsapp_window_closed"), "template_name");
  assert.equal(fieldToHighlight("whatsapp", "whatsapp_template_missing"), "template_name");
});

test("fieldToHighlight: whatsapp media_failed highlights no specific field", () => {
  assert.equal(fieldToHighlight("whatsapp", "whatsapp_media_failed"), "");
});

test("fieldToHighlight: transport-level codes highlight nothing", () => {
  assert.equal(fieldToHighlight("discord", "dns_failure"), "");
  assert.equal(fieldToHighlight("telegram", "timeout"), "");
  assert.equal(fieldToHighlight("whatsapp", "connection_refused"), "");
  assert.equal(fieldToHighlight("webhook", "network_unreachable"), "");
  assert.equal(fieldToHighlight("discord", "tls_error"), "");
  assert.equal(fieldToHighlight("discord", "rate_limited"), "");
});

test("fieldToHighlight: a code for the wrong platform highlights nothing", () => {
  // telegram_chat_not_found is telegram-specific; asking for discord
  // should not accidentally fall through to some other field.
  assert.equal(fieldToHighlight("discord", "telegram_chat_not_found"), "");
});

test("fieldToHighlight: missing/unknown code highlights nothing", () => {
  assert.equal(fieldToHighlight("discord", null), "");
  assert.equal(fieldToHighlight("discord", undefined), "");
  assert.equal(fieldToHighlight("discord", "some_future_code"), "");
});

test("diagnosisViewModel: full passthrough with field highlight resolved", () => {
  const vm = diagnosisViewModel("telegram", {
    code: "telegram_chat_not_found",
    title: "Chat not found",
    detail: "Telegram reported: chat not found",
    hint: "Double-check the Chat ID and message the bot first.",
    docs_url: "https://example.com/docs/telegram",
  });
  assert.equal(vm.code, "telegram_chat_not_found");
  assert.equal(vm.title, "Chat not found");
  assert.equal(vm.detail, "Telegram reported: chat not found");
  assert.equal(vm.hint, "Double-check the Chat ID and message the bot first.");
  assert.equal(vm.docsUrl, "https://example.com/docs/telegram");
  assert.equal(vm.fieldToHighlight, "chat_id");
});

test("diagnosisViewModel: defaults every field to a safe non-empty/blank value when diagnosis is null", () => {
  const vm = diagnosisViewModel("discord", null);
  assert.equal(vm.code, "");
  assert.equal(vm.title, "Send test failed");
  assert.equal(vm.detail, "");
  assert.equal(vm.hint, "");
  assert.equal(vm.docsUrl, "");
  assert.equal(vm.fieldToHighlight, "");
});

test("diagnosisViewModel: defaults missing individual fields even when diagnosis is present", () => {
  const vm = diagnosisViewModel("webhook", { code: "connection_refused" });
  assert.equal(vm.title, "Send test failed");
  assert.equal(vm.detail, "");
  assert.equal(vm.hint, "");
  assert.equal(vm.docsUrl, "");
  assert.equal(vm.fieldToHighlight, "");
});

test("diagnosisSummaryText: title + hint joined with an em dash", () => {
  assert.equal(
    diagnosisSummaryText({ title: "Connection refused", hint: "check firewall/proxy/outbound rules." }),
    "Connection refused — check firewall/proxy/outbound rules.",
  );
});

test("diagnosisSummaryText: title only", () => {
  assert.equal(diagnosisSummaryText({ title: "Chat not found" }), "Chat not found");
});

test("diagnosisSummaryText: hint only (no title)", () => {
  assert.equal(diagnosisSummaryText({ hint: "Retry later." }), "Retry later.");
});

test("diagnosisSummaryText: missing diagnosis entirely", () => {
  assert.equal(diagnosisSummaryText(null), "unknown error");
  assert.equal(diagnosisSummaryText(undefined), "unknown error");
});

test("diagnosisSummaryText: diagnosis object with neither title nor hint", () => {
  assert.equal(diagnosisSummaryText({ code: "platform_error" }), "unknown error");
});
