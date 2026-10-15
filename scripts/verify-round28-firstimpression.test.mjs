import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { Script } from "node:vm";
import { ROUND28_ROOT } from "./verify-round28-safety.mjs";
import { flattenDelta, installGuardExpression, requestPolicy, validateCapturePlan, validateLocaleDeltas } from "./verify-round28-firstimpression.mjs";

test("network policy only permits owned loopback reads/auth and mocks test POSTs", () => {
  assert.equal(requestPolicy("http://127.0.0.1:18190/api/v1/hosts"), "loopback-read-or-auth");
  assert.equal(requestPolicy("/api/v1/auth/login", "POST"), "loopback-read-or-auth");
  assert.equal(requestPolicy("/api/v1/alerts/channels/9001/test", "POST"), "mock-notification-test");
  assert.equal(requestPolicy("/api/v1/alerts/channels/test", "POST"), "mock-notification-test");
  assert.equal(requestPolicy("/api/v1/billing/refresh", "POST"), "blocked-write");
  assert.equal(requestPolicy("/api/v1/alerts/channels/9001", "PUT"), "blocked-write");
  for (const url of ["http://127.0.0.1:8090/", "https://api.telegram.org/", "https://discord.com/", "https://raw.githubusercontent.com/installer", "http://localhost:18190/", "file:///source/photo.png"]) assert.equal(requestPolicy(url), "blocked-outbound");
});

test("locale plan validates EN/KO keys/placeholders and agreed namespaces", () => {
  const result = validateLocaleDeltas([{lane:"A",allowedNamespaces:["nav"],delta:{en:{nav:{show:"Show {name}"}},ko:{nav:{show:"{name} 표시"}}}}]);
  assert.equal(result.totalKeys, 1);
  assert.equal(result.merged.en["nav.show"], "Show {name}");
  assert.throws(() => validateLocaleDeltas([{lane:"A",allowedNamespaces:["nav"],delta:{en:{"nav.show":"Show"},ko:{"overview.show":"표시"}}}]), /key mismatch/);
  assert.throws(() => validateLocaleDeltas([{lane:"A",allowedNamespaces:["nav"],delta:{en:{"nav.show":"{name}"},ko:{"nav.show":"{other}"}}}]), /placeholder mismatch/);
  assert.throws(() => validateLocaleDeltas([{lane:"A",allowedNamespaces:["nav"],delta:{en:{"costs.show":"Show"},ko:{"costs.show":"표시"}}}]), /namespace/);
});

test("locale collisions/prototype keys/nonstring leaves fail, not silently merged", () => {
  const item={lane:"A",allowedNamespaces:["nav"],delta:{en:{"nav.show":"Show"},ko:{"nav.show":"표시"}}};
  assert.throws(() => validateLocaleDeltas([item,{...item,lane:"B"}]), /conflict/);
  assert.throws(() => flattenDelta(JSON.parse('{"__proto__":{"bad":"x"}}')), /unsafe/);
  assert.throws(() => flattenDelta({nav:{bad:123}}), /string leaves/);
  assert.throws(() => flattenDelta({"nav.show":"one",nav:{show:"two"}}), /duplicate/);
});

test("eight-image plan uses existing doc paths, EN/KO/mobile and never publishes", () => {
  const plan=JSON.parse(readFileSync(join(ROUND28_ROOT,"capture-plan.json"),"utf8"));
  const result=validateCapturePlan(plan);
  assert.equal(result.shots,8);
  assert.ok(Array.isArray(result.pendingSelectors));
  assert.ok(plan.captures.some(shot=>shot.lang==="ko"));
  assert.ok(plan.captures.some(shot=>shot.width===360));
  assert.throws(() => validateCapturePlan({...plan,publication:{enabled:true}}), /nonpublishing/);
  assert.throws(() => validateCapturePlan({...plan,hub:"http://127.0.0.1:8090"}), /loopback/);
});

test("browser safety guard compiles and contains upload/outbound/write protections", () => {
  const expression=installGuardExpression("http://127.0.0.1:18190","notification-accepted");
  assert.doesNotThrow(() => new Script(expression));
  assert.match(expression,/STOP: original\/source file upload observed/);
  assert.match(expression,/Round28 blocked outbound request/);
  assert.match(expression,/Round28 blocked non-auth write/);
  assert.match(expression,/accepted-not-delivery-verified/);
  assert.match(expression,/existing_channel_id/);
});

test("legacy unsafe diagnosis capture is explicitly not reused", () => {
  const plan=JSON.parse(readFileSync(join(ROUND28_ROOT,"capture-plan.json"),"utf8"));
  assert.ok(plan.captures.filter(shot=>shot.id.includes("notification")).every(shot=>shot.fixture.includes("browser-only")));
  const mergePlan=JSON.parse(readFileSync(join(ROUND28_ROOT,"locale-integration-plan.json"),"utf8"));
  assert.doesNotMatch(mergePlan.cssCommand,/npx|npm install|sudo/);
  assert.match(mergePlan.cssCommand,/node_modules\/\.bin\/tailwindcss/);
});
