import test from "node:test";
import assert from "node:assert/strict";
import {
  applyFacets, facetCounts, facetsActive, hostNeedsAttention, normalizeFacets, providerKey,
} from "../assets/js/core/hostfilter.js";

const row = (id, provider, status, level = "ok", rxLevel = "ok") => ({
  host: { id, provider }, status, egress: { level, rx_level: rxLevel },
});
const fleet = [
  row("a", "aws", "up"),
  row("b", "aws", "up", "warning"),
  row("c", "oci", "down"),
  row("d", "oci", "up", "ok", "exceeded"),
  row("e", "", "up"),
  row("f", "hetzner", "up"),
];

test("providerKey folds empty and unknown providers into other", () => {
  assert.deepEqual(fleet.map(providerKey), ["aws", "aws", "oci", "oci", "other", "other"]);
});

test("hostNeedsAttention uses only reported state: offline or traffic level warning+", () => {
  assert.deepEqual(fleet.map(hostNeedsAttention), [false, true, true, true, false, false]);
  assert.equal(hostNeedsAttention({ status: "up" }), false, "missing egress is not a problem");
  assert.equal(hostNeedsAttention({ status: "unknown", egress: { level: "unknown" } }), false);
});

test("applyFacets combines status with any-of providers", () => {
  const ids = (rows) => rows.map((r) => r.host.id);
  assert.deepEqual(ids(applyFacets(fleet, {})), ["a", "b", "c", "d", "e", "f"]);
  assert.deepEqual(ids(applyFacets(fleet, { status: "attention" })), ["b", "c", "d"]);
  assert.deepEqual(ids(applyFacets(fleet, { status: "online", providers: ["oci", "other"] })), ["d", "e", "f"]);
  assert.deepEqual(ids(applyFacets(fleet, { status: "offline", providers: ["aws"] })), []);
});

test("facetCounts: status counts respect providers, provider counts respect status, providers stay listed", () => {
  const counts = facetCounts(fleet, { status: "offline", providers: ["oci"] });
  assert.deepEqual(counts.status, { all: 2, online: 1, offline: 1, attention: 2 });
  assert.deepEqual(counts.providers, [
    { key: "aws", count: 0 }, { key: "oci", count: 1 }, { key: "other", count: 0 },
  ]);
});

test("normalizeFacets drops stale values and dedupes", () => {
  assert.deepEqual(normalizeFacets({ status: "bogus", providers: ["oci", "aws", "oci", "gcp"] }), { status: "all", providers: ["aws", "oci"] });
  assert.deepEqual(normalizeFacets(null), { status: "all", providers: [] });
  assert.equal(facetsActive({ status: "all", providers: [] }), false);
  assert.equal(facetsActive({ status: "all", providers: ["aws"] }), true);
  assert.equal(facetsActive({ status: "offline", providers: [] }), true);
});

test("facetCounts keeps fleet providers and pressed providers listed when the text filter narrows rows", () => {
  const awsOnly = fleet.filter((r) => providerKey(r) === "aws");
  const counts = facetCounts(awsOnly, { status: "all", providers: ["oci"] }, fleet);
  assert.deepEqual(counts.providers, [
    { key: "aws", count: 2 }, { key: "oci", count: 0 }, { key: "other", count: 0 },
  ]);
  const pressedOnly = facetCounts(awsOnly, { providers: ["oci"] });
  assert.deepEqual(pressedOnly.providers.map((p) => p.key), ["aws", "oci"], "a pressed chip is listed even if absent from rows");
});
