// storageusage.test.mjs — unit tests for assets/js/core/storageusage.js
// (SPEC-v0.7 §3 storage-account display helpers).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  STATUS_LABELS,
  PROVIDER_LABELS,
  providerLabel,
  storageStatusLabel,
  storageStatusChipClass,
  storageUsedPercent,
} from "../assets/js/core/storageusage.js";

test("providerLabel: known providers", () => {
  assert.equal(providerLabel("googledrive"), "Google Drive");
  assert.equal(providerLabel("dropbox"), "Dropbox");
});

test("providerLabel: unrecognized falls back to raw value", () => {
  assert.equal(providerLabel("onedrive"), "onedrive");
});

test("storageStatusLabel: missing snapshot means pending connection", () => {
  assert.equal(storageStatusLabel(null), "Pending connection");
  assert.equal(storageStatusLabel(undefined), "Pending connection");
});

test("storageStatusLabel: known statuses", () => {
  for (const [status, label] of Object.entries(STATUS_LABELS)) {
    assert.equal(storageStatusLabel({ status }), label);
  }
});

test("storageStatusLabel: unrecognized status falls back to raw value", () => {
  assert.equal(storageStatusLabel({ status: "weird" }), "weird");
});

test("storageStatusChipClass: ok is green, pending/missing is gray, everything else is amber", () => {
  assert.equal(storageStatusChipClass({ status: "ok" }), "cp-chip cp-chip-ok");
  assert.equal(storageStatusChipClass({ status: "pending_oauth" }), "cp-chip cp-chip-other");
  assert.equal(storageStatusChipClass(null), "cp-chip cp-chip-other");
  assert.equal(storageStatusChipClass({ status: "auth_failed" }), "cp-chip cp-chip-warning");
  assert.equal(storageStatusChipClass({ status: "error" }), "cp-chip cp-chip-warning");
  assert.equal(storageStatusChipClass({ status: "not_configured" }), "cp-chip cp-chip-warning");
  assert.equal(storageStatusChipClass({ status: "permission_denied" }), "cp-chip cp-chip-warning");
});

test("storageUsedPercent: computes used/limit as a percentage", () => {
  assert.equal(storageUsedPercent({ used_bytes: 50, limit_bytes: 100, unlimited: false }), 50);
  assert.equal(storageUsedPercent({ used_bytes: 95, limit_bytes: 100, unlimited: false }), 95);
});

test("storageUsedPercent: unlimited or zero limit is always 0", () => {
  assert.equal(storageUsedPercent({ used_bytes: 50, limit_bytes: 100, unlimited: true }), 0);
  assert.equal(storageUsedPercent({ used_bytes: 50, limit_bytes: 0, unlimited: false }), 0);
  assert.equal(storageUsedPercent(null), 0);
  assert.equal(storageUsedPercent(undefined), 0);
});

test("PROVIDER_LABELS/STATUS_LABELS cover the models enums exactly", () => {
  assert.deepEqual(Object.keys(PROVIDER_LABELS), ["googledrive", "dropbox"]);
  assert.deepEqual(Object.keys(STATUS_LABELS), ["ok", "pending_oauth", "not_configured", "auth_failed", "permission_denied", "error"]);
});
