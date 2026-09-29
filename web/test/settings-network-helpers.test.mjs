// settings-network-helpers.test.mjs — unit tests for
// assets/js/pages/settings/network-helpers.js.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  isUnspecifiedOrMulticast,
  looksLikeIP,
  validateCustomAddress,
  validatePort,
  validateCIDREntry,
  normalizeCIDRList,
  buildNetworkConfigFromSelection,
  describeNetworkConfig,
  diffNetworkConfig,
  suggestionsForInterfaces,
  isAllowlistCovering,
  formatCountdown,
  ERROR_MESSAGES,
  describeNetworkError,
} from "../assets/js/pages/settings/network-helpers.js";

test("isUnspecifiedOrMulticast: detects unspecified addresses", () => {
  assert.equal(isUnspecifiedOrMulticast("0.0.0.0"), true);
  assert.equal(isUnspecifiedOrMulticast("::"), true);
  assert.equal(isUnspecifiedOrMulticast("127.0.0.1"), false);
});

test("isUnspecifiedOrMulticast: detects IPv4 multicast range", () => {
  assert.equal(isUnspecifiedOrMulticast("224.0.0.1"), true);
  assert.equal(isUnspecifiedOrMulticast("239.255.255.255"), true);
  assert.equal(isUnspecifiedOrMulticast("223.255.255.255"), false);
  assert.equal(isUnspecifiedOrMulticast("240.0.0.0"), false);
});

test("isUnspecifiedOrMulticast: detects IPv6 multicast (ff00::/8)", () => {
  assert.equal(isUnspecifiedOrMulticast("ff02::1"), true);
  assert.equal(isUnspecifiedOrMulticast("fd7a:115c:a1e0::22e:c03d"), false);
});

test("looksLikeIP: accepts valid IPv4/IPv6, rejects garbage", () => {
  assert.equal(looksLikeIP("192.168.100.2"), true);
  assert.equal(looksLikeIP("100.95.192.60"), true);
  assert.equal(looksLikeIP("fd7a:115c:a1e0::22e:c03d"), true);
  assert.equal(looksLikeIP("999.1.1.1"), false);
  assert.equal(looksLikeIP("not-an-ip"), false);
  assert.equal(looksLikeIP(""), false);
});

test("validateCustomAddress: required, syntactic, and semantic checks", () => {
  assert.equal(validateCustomAddress(""), "Address is required.");
  assert.equal(validateCustomAddress("nope"), "Not a valid IP address.");
  assert.equal(validateCustomAddress("0.0.0.0"), "Address must not be unspecified or multicast.");
  assert.equal(validateCustomAddress("224.0.0.1"), "Address must not be unspecified or multicast.");
  assert.equal(validateCustomAddress("127.0.0.2"), "");
  assert.equal(validateCustomAddress("  100.95.192.60  "), "");
});

test("validatePort: bounds 1024..65535", () => {
  assert.equal(validatePort(8090), "");
  assert.equal(validatePort(1024), "");
  assert.equal(validatePort(65535), "");
  assert.notEqual(validatePort(1023), "");
  assert.notEqual(validatePort(65536), "");
  assert.notEqual(validatePort(80), "");
  assert.notEqual(validatePort("abc"), "");
  assert.notEqual(validatePort(8090.5), "");
});

test("validateCIDREntry: '*' always valid alone; otherwise IP/CIDR shape", () => {
  assert.equal(validateCIDREntry("*"), "");
  assert.equal(validateCIDREntry("100.64.0.0/10"), "");
  assert.equal(validateCIDREntry("127.0.0.1"), "");
  assert.equal(validateCIDREntry("fd7a:115c:a1e0::/48"), "");
  assert.notEqual(validateCIDREntry(""), "");
  assert.notEqual(validateCIDREntry("not-a-cidr"), "");
  assert.notEqual(validateCIDREntry("192.168.1.0/33"), "");
  assert.notEqual(validateCIDREntry("fd7a::/200"), "");
});

test("normalizeCIDRList: '*' must be alone; validates every entry", () => {
  assert.deepEqual(normalizeCIDRList(["*"]), { ok: true, error: "" });
  assert.deepEqual(normalizeCIDRList(["127.0.0.0/8", "100.64.0.0/10"]), { ok: true, error: "" });
  assert.equal(normalizeCIDRList([]).ok, false);
  assert.equal(normalizeCIDRList(["*", "127.0.0.0/8"]).ok, false);
  assert.equal(normalizeCIDRList(["not-a-cidr"]).ok, false);
});

test("buildNetworkConfigFromSelection: 'all' mode ignores selectedAddresses", () => {
  const cfg = buildNetworkConfigFromSelection({
    allInterfaces: true,
    selectedAddresses: ["192.168.100.2"],
    port: 8090,
    allowedCIDRs: ["*"],
  });
  assert.deepEqual(cfg, { mode: "all", addresses: [], port: 8090, allowed_cidrs: ["*"] });
});

test("buildNetworkConfigFromSelection: 'custom' mode carries selected addresses", () => {
  const cfg = buildNetworkConfigFromSelection({
    allInterfaces: false,
    selectedAddresses: ["127.0.0.1", "100.95.192.60"],
    port: 8090,
    allowedCIDRs: ["127.0.0.0/8", "100.64.0.0/10"],
  });
  assert.deepEqual(cfg, {
    mode: "custom",
    addresses: ["127.0.0.1", "100.95.192.60"],
    port: 8090,
    allowed_cidrs: ["127.0.0.0/8", "100.64.0.0/10"],
  });
});

test("describeNetworkConfig: renders a compact human summary", () => {
  assert.equal(
    describeNetworkConfig({ mode: "all", addresses: [], port: 8090, allowed_cidrs: ["*"] }),
    "All interfaces · port 8090 · allow: *",
  );
  assert.equal(
    describeNetworkConfig({ mode: "custom", addresses: ["127.0.0.1"], port: 8090, allowed_cidrs: ["127.0.0.0/8"] }),
    "127.0.0.1 · port 8090 · allow: 127.0.0.0/8",
  );
});

test("diffNetworkConfig: only reports changed fields", () => {
  const previous = { mode: "custom", addresses: ["127.0.0.1"], port: 8090, allowed_cidrs: ["127.0.0.0/8"] };
  const next = { mode: "custom", addresses: ["127.0.0.1", "127.0.0.2"], port: 8090, allowed_cidrs: ["127.0.0.0/8"] };
  const diff = diffNetworkConfig(previous, next);
  assert.equal(diff.length, 1);
  assert.equal(diff[0].field, "Addresses");
  assert.equal(diff[0].from, "127.0.0.1");
  assert.equal(diff[0].to, "127.0.0.1, 127.0.0.2");
});

test("diffNetworkConfig: empty when nothing changed", () => {
  const cfg = { mode: "all", addresses: [], port: 8090, allowed_cidrs: ["*"] };
  assert.deepEqual(diffNetworkConfig(cfg, { ...cfg }), []);
});

test("diffNetworkConfig: reports mode + port + allowlist changes together", () => {
  const previous = { mode: "all", addresses: [], port: 8090, allowed_cidrs: ["*"] };
  const next = { mode: "custom", addresses: ["192.168.100.2"], port: 9090, allowed_cidrs: ["192.168.100.0/24"] };
  const diff = diffNetworkConfig(previous, next);
  const fields = diff.map((d) => d.field);
  assert.ok(fields.includes("Listen mode"));
  assert.ok(fields.includes("Addresses"));
  assert.ok(fields.includes("Port"));
  assert.ok(fields.includes("Allowlist"));
});

test("suggestionsForInterfaces: dedupes and skips link-local", () => {
  const interfaces = [
    {
      name: "wlan0",
      addresses: [
        { scope: "global", suggested_cidr: "192.168.100.0/24", network: "192.168.100.0/24" },
        { scope: "link-local", suggested_cidr: "169.254.0.0/16", network: "169.254.0.0/16" },
      ],
    },
    {
      name: "tailscale0",
      addresses: [{ scope: "global", suggested_cidr: "100.64.0.0/10", network: "100.95.192.60/32" }],
    },
    {
      name: "eth1",
      addresses: [{ scope: "global", suggested_cidr: "192.168.100.0/24", network: "192.168.100.0/24" }],
    },
  ];
  const suggestions = suggestionsForInterfaces(interfaces);
  assert.equal(suggestions.length, 2);
  assert.equal(suggestions[0].cidr, "192.168.100.0/24");
  assert.equal(suggestions[0].label, "Allow 192.168.100.0/24 (wlan0)");
  assert.equal(suggestions[1].cidr, "100.64.0.0/10");
});

test("isAllowlistCovering: '*' covers everything; exact match covers itself", () => {
  assert.equal(isAllowlistCovering(["*"], "127.0.0.0/8"), true);
  assert.equal(isAllowlistCovering(["127.0.0.0/8"], "127.0.0.0/8"), true);
  assert.equal(isAllowlistCovering(["127.0.0.0/8"], "100.64.0.0/10"), false);
});

test("formatCountdown: renders minutes+seconds, seconds-only, and expired", () => {
  assert.equal(formatCountdown(1000, 850), "2m 30s");
  assert.equal(formatCountdown(1000, 990), "10s");
  assert.equal(formatCountdown(1000, 1000), "expired");
  assert.equal(formatCountdown(1000, 1500), "expired");
});

test("describeNetworkError: maps known codes, falls back to message", () => {
  assert.equal(describeNetworkError({ code: "would_lock_out" }), ERROR_MESSAGES.would_lock_out);
  assert.equal(describeNetworkError({ code: "bind_failed" }), ERROR_MESSAGES.bind_failed);
  assert.equal(describeNetworkError({ code: "change_pending" }), ERROR_MESSAGES.change_pending);
  assert.equal(describeNetworkError({ code: "no_pending" }), ERROR_MESSAGES.no_pending);
  assert.equal(describeNetworkError({ code: "unknown_code", message: "raw message" }), "raw message");
  assert.equal(describeNetworkError({}), "Network settings change failed.");
});
