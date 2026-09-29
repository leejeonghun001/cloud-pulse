// updates.test.mjs — unit tests for assets/js/updates.js (D-U6 pure
// helpers: update banner visibility/dismissal, host badge/panel text,
// hub info fields).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  shouldShowUpdateBanner,
  bannerText,
  hostUpdateBadgeText,
  agentVersionText,
  outdatedAgentCount,
  agentUpdatePanelText,
  hubInfoUpdateFields,
  LEGACY_AGENT_EXPLANATION,
  VERSION_REFRESH_INTERVAL_MS,
} from "../assets/js/core/updates.js";

test("shouldShowUpdateBanner: false when update_available is false", () => {
  assert.equal(shouldShowUpdateBanner({ update_available: false, latest_version: "v0.3.1" }, null), false);
});

test("shouldShowUpdateBanner: false when versionInfo is missing/null", () => {
  assert.equal(shouldShowUpdateBanner(null, null), false);
  assert.equal(shouldShowUpdateBanner(undefined, null), false);
});

test("shouldShowUpdateBanner: false when latest_version is empty", () => {
  assert.equal(shouldShowUpdateBanner({ update_available: true, latest_version: "" }, null), false);
});

test("shouldShowUpdateBanner: true when update available and nothing dismissed", () => {
  assert.equal(shouldShowUpdateBanner({ update_available: true, latest_version: "v0.3.1" }, null), true);
  assert.equal(shouldShowUpdateBanner({ update_available: true, latest_version: "v0.3.1" }, ""), true);
});

test("shouldShowUpdateBanner: false when the exact latest tag was dismissed", () => {
  assert.equal(shouldShowUpdateBanner({ update_available: true, latest_version: "v0.3.1" }, "v0.3.1"), false);
});

test("shouldShowUpdateBanner: true again when a newer tag appears after a dismissal", () => {
  assert.equal(shouldShowUpdateBanner({ update_available: true, latest_version: "v0.3.2" }, "v0.3.1"), true);
});

test("bannerText: builds headline and command from versionInfo", () => {
  const result = bannerText({
    version: "v0.3.0",
    latest_version: "v0.3.1",
    update_command: "sudo cloud-pulse-hub update",
  });
  assert.equal(result.headline, "cloud-pulse v0.3.1 is available — hub is running v0.3.0");
  assert.equal(result.command, "sudo cloud-pulse-hub update");
});

test("bannerText: falls back to 'unknown' for missing version fields", () => {
  const result = bannerText({});
  assert.equal(result.headline, "cloud-pulse unknown is available — hub is running unknown");
  assert.equal(result.command, "");
});

test("hostUpdateBadgeText: null when update is missing", () => {
  assert.equal(hostUpdateBadgeText(null), null);
  assert.equal(hostUpdateBadgeText(undefined), null);
});

test("hostUpdateBadgeText: null when update.available is false", () => {
  assert.equal(hostUpdateBadgeText({ available: false, latest: "v0.3.1" }), null);
});

test("hostUpdateBadgeText: builds text + label with the latest version", () => {
  const result = hostUpdateBadgeText({ available: true, latest: "v0.3.1" });
  assert.equal(result.text, "update available");
  assert.equal(result.label, "A newer agent version (v0.3.1) is available");
});

test("hostUpdateBadgeText: falls back label when latest is missing", () => {
  const result = hostUpdateBadgeText({ available: true, latest: "" });
  assert.equal(result.label, "A newer agent version (a newer version) is available");
});

test("agentVersionText: renders 'agent vX'", () => {
  assert.equal(agentVersionText("v0.3.0"), "agent v0.3.0");
});

test("agentVersionText: falls back to 'unknown' for missing version", () => {
  assert.equal(agentVersionText(""), "agent unknown");
  assert.equal(agentVersionText(null), "agent unknown");
  assert.equal(agentVersionText(undefined), "agent unknown");
});

test("outdatedAgentCount: counts hosts with update.available true", () => {
  const hosts = [
    { update: { available: true } },
    { update: { available: false } },
    { update: null },
    {},
    { update: { available: true } },
  ];
  assert.equal(outdatedAgentCount(hosts), 2);
});

test("outdatedAgentCount: 0 for empty/non-array input", () => {
  assert.equal(outdatedAgentCount([]), 0);
  assert.equal(outdatedAgentCount(null), 0);
  assert.equal(outdatedAgentCount(undefined), 0);
});

test("agentUpdatePanelText: null when update is missing", () => {
  assert.equal(agentUpdatePanelText(null), null);
  assert.equal(agentUpdatePanelText(undefined), null);
});

test("agentUpdatePanelText: available + self_update (current agent)", () => {
  const result = agentUpdatePanelText({
    available: true,
    latest: "v0.3.1",
    self_update: true,
    command: "sudo cloud-pulse-agent update",
  });
  assert.equal(result.headline, "A newer agent version (v0.3.1) is available.");
  assert.equal(result.command, "sudo cloud-pulse-agent update");
  assert.equal(result.showLegacyNote, false);
});

test("agentUpdatePanelText: legacy agent (no self_update) shows the legacy note", () => {
  const result = agentUpdatePanelText({
    available: true,
    latest: "v0.3.1",
    self_update: false,
    command:
      "curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.sh | sudo bash",
  });
  assert.equal(result.showLegacyNote, true);
  assert.ok(result.command.includes("install-agent.sh"));
});

test("agentUpdatePanelText: up to date renders the up-to-date headline", () => {
  const result = agentUpdatePanelText({ available: false, latest: "v0.3.0", self_update: true, command: "" });
  assert.equal(result.headline, "This agent is up to date.");
});

test("LEGACY_AGENT_EXPLANATION mentions the built-in updater", () => {
  assert.match(LEGACY_AGENT_EXPLANATION, /built-in updater/);
  assert.match(LEGACY_AGENT_EXPLANATION, /sudo cloud-pulse-agent update/);
});

test("hubInfoUpdateFields: maps versionInfo fields with fallbacks", () => {
  const result = hubInfoUpdateFields({
    latest_version: "v0.3.1",
    update_available: true,
    update_check_enabled: true,
    checked_at: 1700000000,
    check_error: "",
  });
  assert.deepEqual(result, {
    latest: "v0.3.1",
    checkEnabled: true,
    checkedAtUnixSeconds: 1700000000,
    checkError: "",
  });
});

test("hubInfoUpdateFields: defaults when versionInfo has no update data yet", () => {
  const result = hubInfoUpdateFields({ version: "v0.3.0", commit: "abc", date: "2024-01-01" });
  assert.deepEqual(result, {
    latest: "unknown",
    checkEnabled: false,
    checkedAtUnixSeconds: null,
    checkError: "",
  });
});

test("hubInfoUpdateFields: propagates check_error when present", () => {
  const result = hubInfoUpdateFields({ check_error: "dial tcp: timeout", checked_at: 5 });
  assert.equal(result.checkError, "dial tcp: timeout");
  assert.equal(result.checkedAtUnixSeconds, 5);
});

test("VERSION_REFRESH_INTERVAL_MS is 30 minutes", () => {
  assert.equal(VERSION_REFRESH_INTERVAL_MS, 30 * 60 * 1000);
});
