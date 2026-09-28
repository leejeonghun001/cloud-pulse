import test from "node:test";
import assert from "node:assert/strict";

import { runHostDetailRefresh } from "../assets/js/refresh.js";

test("missing host stops the detail refresh chain", async () => {
  let metricsCalls = 0;
  let scheduleCalls = 0;

  await runHostDetailRefresh({
    isActive: () => true,
    loadSummary: async () => false,
    loadMetrics: async () => {
      metricsCalls += 1;
    },
    rescheduleOnly: false,
    schedule: () => {
      scheduleCalls += 1;
    },
  });

  assert.equal(metricsCalls, 0);
  assert.equal(scheduleCalls, 0);
});

test("available host loads metrics and re-arms detail refresh", async () => {
  let metricsCalls = 0;
  let scheduleCalls = 0;

  await runHostDetailRefresh({
    isActive: () => true,
    loadSummary: async () => true,
    loadMetrics: async () => {
      metricsCalls += 1;
    },
    rescheduleOnly: false,
    schedule: () => {
      scheduleCalls += 1;
    },
  });

  assert.equal(metricsCalls, 1);
  assert.equal(scheduleCalls, 1);
});
