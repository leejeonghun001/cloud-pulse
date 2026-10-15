import test from "node:test";
import assert from "node:assert/strict";
import { CLOUD_SUMMARY_REFRESH_MS, isCloudSummaryFresh } from "../assets/js/pages/overview.js";

test("cloud summary cache is reused for sixty seconds only when the selected month matches", () => {
  const snapshot = { month: "2026-10" };
  const at = 1_000_000;
  assert.equal(isCloudSummaryFresh(snapshot, at, "2026-10", at + CLOUD_SUMMARY_REFRESH_MS - 1), true);
  assert.equal(isCloudSummaryFresh(snapshot, at, "2026-10", at + CLOUD_SUMMARY_REFRESH_MS), false);
  assert.equal(isCloudSummaryFresh(snapshot, at, "2026-09", at + 1), false);
});
