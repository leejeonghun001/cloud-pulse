import { test } from "node:test";
import assert from "node:assert/strict";
import { activePrimaryNavKey } from "../assets/js/ui/shell.js";

test("primary nav active mapping includes nested and legacy routes", () => {
  const cases = [
    ["#/", "overview"], ["#/host/node-1", "overview"], ["#/alerts?state=firing", "alerts"],
    ["#/costs", "costs"], ["#/rollout", "rollout"], ["#/updates", "rollout"],
    ["#/compare?hosts=a,b", "compare"], ["#/settings/security", null],
  ];
  for (const [route, expected] of cases) assert.equal(activePrimaryNavKey(route), expected, route);
});
