// notifications-tabs.test.mjs — URL-addressable Notifications sub-tabs.
import { test } from "node:test";
import assert from "node:assert/strict";
import { tabFromHash, hashForTab } from "../assets/js/pages/settings/notifications.js";

test("tabFromHash reads ?tab= and defaults to channels", () => {
  assert.equal(tabFromHash("#/settings/notifications"), "channels");
  assert.equal(tabFromHash("#/settings/notifications?tab=health"), "health");
  assert.equal(tabFromHash("#/settings/notifications?tab=bogus"), "channels");
  assert.equal(tabFromHash(""), "channels");
});

test("hashForTab sets or removes the tab parameter", () => {
  assert.equal(hashForTab("#/settings/notifications", "health"), "#/settings/notifications?tab=health");
  assert.equal(hashForTab("#/settings/notifications?tab=health", "channels"), "#/settings/notifications");
  assert.equal(hashForTab("#/settings/notifications?x=1&tab=health", "channels"), "#/settings/notifications?x=1");
  assert.equal(hashForTab("", "health"), "#/settings/notifications?tab=health");
});
