// notify-guides.test.mjs — unit tests for assets/js/core/notify-guides.js
// (per-platform notify-channel setup guides and draft-config validation).
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { setCatalogs, setLang } from "../assets/js/core/i18n.js";
import {
  CHANNEL_TYPES,
  channelTypeByValue,
  validateChannelConfig,
  defaultChannelName,
} from "../assets/js/core/notify-guides.js";

// A persistent fake localStorage (installed once, for the whole file
// rather than per-test) so setLang("en") below stays in effect for
// every test — CHANNEL_TYPES' fields/steps are getters that re-resolve
// t() against whatever language is active at read time.
globalThis.localStorage = (() => {
  const store = new Map();
  return {
    getItem: (k) => (store.has(k) ? store.get(k) : null),
    setItem: (k, v) => store.set(k, String(v)),
    removeItem: (k) => store.delete(k),
  };
})();

const assetsDir = fileURLToPath(new URL("../assets/i18n/", import.meta.url));
setCatalogs({
  en: JSON.parse(readFileSync(`${assetsDir}en.json`, "utf-8")),
  ko: JSON.parse(readFileSync(`${assetsDir}ko.json`, "utf-8")),
});
setLang("en");

test("CHANNEL_TYPES covers every models.NotifyChannelType value", () => {
  assert.deepEqual(CHANNEL_TYPES.map((t) => t.value), ["discord", "telegram", "whatsapp", "webhook"]);
});

test("every channel type has a non-empty label, at least one field, and at least one guide step", () => {
  for (const type of CHANNEL_TYPES) {
    assert.ok(type.label.length > 0, `${type.value} needs a label`);
    assert.ok(type.fields.length > 0, `${type.value} needs fields`);
    assert.ok(type.steps.length > 0, `${type.value} needs steps`);
    for (const step of type.steps) {
      assert.equal(typeof step, "string");
      assert.ok(step.length > 0);
    }
  }
});

test("discord/telegram/whatsapp secret fields match models.NotifyChannelType.SecretFields", () => {
  const secretKeys = (type) =>
    channelTypeByValue(type)
      .fields.filter((f) => f.secret)
      .map((f) => f.key);
  assert.deepEqual(secretKeys("discord"), ["webhook_url"]);
  assert.deepEqual(secretKeys("telegram"), ["bot_token"]);
  assert.deepEqual(secretKeys("whatsapp"), ["access_token"]);
  assert.deepEqual(secretKeys("webhook"), []);
});

test("channelTypeByValue: unknown type returns undefined", () => {
  assert.equal(channelTypeByValue("bogus"), undefined);
});

test("validateChannelConfig: unknown type reports a type error", () => {
  const errors = validateChannelConfig("bogus", {});
  assert.ok(errors.type);
});

test("validateChannelConfig: discord requires webhook_url", () => {
  const errors = validateChannelConfig("discord", {});
  assert.ok(errors.webhook_url);
});

test("validateChannelConfig: discord rejects a non-discord host", () => {
  const errors = validateChannelConfig("discord", { webhook_url: "https://evil.example.com/webhooks/1" });
  assert.match(errors.webhook_url, /discord/);
});

test("validateChannelConfig: discord accepts discord.com and discordapp.com", () => {
  assert.deepEqual(validateChannelConfig("discord", { webhook_url: "https://discord.com/api/webhooks/1/token" }), {});
  assert.deepEqual(validateChannelConfig("discord", { webhook_url: "https://discordapp.com/api/webhooks/1/token" }), {});
});

test("validateChannelConfig: telegram requires bot_token and chat_id, thread id optional", () => {
  let errors = validateChannelConfig("telegram", {});
  assert.ok(errors.bot_token);
  assert.ok(errors.chat_id);

  errors = validateChannelConfig("telegram", { bot_token: "123456:TEST-TOKEN", chat_id: "-100123" });
  assert.deepEqual(errors, {});
});

test("validateChannelConfig: whatsapp requires access_token/phone_number_id/to; template optional", () => {
  let errors = validateChannelConfig("whatsapp", {});
  assert.ok(errors.access_token);
  assert.ok(errors.phone_number_id);
  assert.ok(errors.to);

  errors = validateChannelConfig("whatsapp", { access_token: "t", phone_number_id: "1", to: "15551234567" });
  assert.deepEqual(errors, {});
});

test("validateChannelConfig: whatsapp rejects a 'to' with a leading + or punctuation", () => {
  const errors = validateChannelConfig("whatsapp", { access_token: "t", phone_number_id: "1", to: "+1 (555) 123-4567" });
  assert.ok(errors.to);
});

test("validateChannelConfig: webhook uses backend-required url key; checkbox field is never required", () => {
  let errors = validateChannelConfig("webhook", {});
  assert.ok(errors.url);
  // internal/notify/webhook.go validates config["url"], not webhook_url.
  errors = validateChannelConfig("webhook", { webhook_url: "https://hooks.example.com/x" });
  assert.ok(errors.url);
  errors = validateChannelConfig("webhook", { url: "https://hooks.example.com/x" });
  assert.deepEqual(errors, {});
});

test("defaultChannelName", () => {
  assert.equal(defaultChannelName("discord"), "Discord");
  assert.equal(defaultChannelName("webhook"), "Webhook (generic)");
  assert.equal(defaultChannelName("bogus"), "bogus");
});

test("CHANNEL_TYPES fields/steps render Korean text when lang is ko", () => {
  setLang("ko");
  try {
    const discord = channelTypeByValue("discord");
    assert.equal(discord.fields[0].label, "웹훅 URL");
    assert.equal(discord.steps[0], "Discord 서버에서 서버 설정 → 연동 → 웹훅으로 이동하세요.");
    assert.equal(defaultChannelName("webhook"), "Webhook (범용)");
  } finally {
    setLang("en");
  }
});
