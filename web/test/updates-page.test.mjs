// updates-page.test.mjs — unit tests for the pure helpers exported by
// assets/js/pages/updates.js (the #/updates remote-agent-update page).
// Named distinctly from the pre-existing updates.test.mjs, which covers
// core/updates.js's unrelated agent-self-update banner helpers.
import { test } from "node:test";
import assert from "node:assert/strict";
import { isTerminalJobState, PROGRESS_POLL_MS, platformLabel, manualUpdateCommand } from "../assets/js/pages/updates.js";

test("isTerminalJobState: succeeded and failed are terminal", () => {
  assert.equal(isTerminalJobState("succeeded"), true);
  assert.equal(isTerminalJobState("failed"), true);
});

test("isTerminalJobState: queued and in_progress are not terminal", () => {
  assert.equal(isTerminalJobState("queued"), false);
  assert.equal(isTerminalJobState("in_progress"), false);
});

test("isTerminalJobState: unknown values are not terminal", () => {
  assert.equal(isTerminalJobState("bogus"), false);
  assert.equal(isTerminalJobState(""), false);
  assert.equal(isTerminalJobState(undefined), false);
});

test("PROGRESS_POLL_MS matches SPEC-v0.6 §2's 5-second auto-refresh", () => {
  assert.equal(PROGRESS_POLL_MS, 5000);
});

test("platformLabel: known OS families", () => {
  assert.equal(platformLabel("linux"), "Linux");
  assert.equal(platformLabel("darwin"), "macOS");
  assert.equal(platformLabel("windows"), "Windows");
});

test("platformLabel: empty/unrecognized falls back to Unknown", () => {
  assert.equal(platformLabel(""), "Unknown");
  assert.equal(platformLabel(undefined), "Unknown");
  assert.equal(platformLabel("freebsd"), "Unknown");
});

test("manualUpdateCommand: prefers the hub-computed OS-aware command", () => {
  assert.equal(manualUpdateCommand({ update: { command: "cloud-pulse-agent update (run as Administrator)" } }), "cloud-pulse-agent update (run as Administrator)");
  assert.equal(manualUpdateCommand({ update: { command: "sudo cloud-pulse-agent update" } }), "sudo cloud-pulse-agent update");
});

test("manualUpdateCommand: falls back to a generic hint when update is absent", () => {
  assert.equal(manualUpdateCommand({ update: null }), "sudo cloud-pulse-agent update");
  assert.equal(manualUpdateCommand({}), "sudo cloud-pulse-agent update");
});
