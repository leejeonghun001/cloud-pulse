// notify-checklists.test.mjs — unit tests for
// assets/js/core/notify-checklists.js (real-account verification
// checklist data + localStorage progress helpers).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  CHECKLISTS,
  checklistByValue,
  checklistStorageKey,
  defaultChecklistProgress,
  loadChecklistProgress,
  saveChecklistProgress,
  checklistCompletionCount,
} from "../assets/js/core/notify-checklists.js";

// fakeStorage is a minimal Storage-shaped stand-in (get/setItem only)
// so this suite never needs a real browser/jsdom environment.
class FakeStorage {
  constructor() {
    this.map = new Map();
  }
  getItem(key) {
    return this.map.has(key) ? this.map.get(key) : null;
  }
  setItem(key, value) {
    this.map.set(key, String(value));
  }
}

test("CHECKLISTS covers discord/telegram/whatsapp, no generic webhook entry", () => {
  assert.deepEqual(CHECKLISTS.map((c) => c.value), ["discord", "telegram", "whatsapp"]);
});

test("every checklist has a non-empty label and at least one item with non-empty id/text", () => {
  for (const checklist of CHECKLISTS) {
    assert.ok(checklist.label.length > 0, `${checklist.value} needs a label`);
    assert.ok(checklist.items.length > 0, `${checklist.value} needs items`);
    const seenIds = new Set();
    for (const item of checklist.items) {
      assert.equal(typeof item.id, "string");
      assert.ok(item.id.length > 0);
      assert.equal(typeof item.text, "string");
      assert.ok(item.text.length > 0);
      assert.ok(!seenIds.has(item.id), `duplicate item id ${item.id} in ${checklist.value}`);
      seenIds.add(item.id);
    }
  }
});

test("checklistByValue: known type returns the matching entry", () => {
  const discord = checklistByValue("discord");
  assert.ok(discord);
  assert.equal(discord.label, "Discord");
});

test("checklistByValue: unknown/webhook type returns undefined", () => {
  assert.equal(checklistByValue("webhook"), undefined);
  assert.equal(checklistByValue("bogus"), undefined);
});

test("checklistStorageKey namespaces by type and id", () => {
  assert.equal(checklistStorageKey("discord", 1), "cloud-pulse:notify-checklist:discord:1");
  assert.equal(checklistStorageKey("telegram", "draft"), "cloud-pulse:notify-checklist:telegram:draft");
  assert.notEqual(checklistStorageKey("discord", 1), checklistStorageKey("discord", 2));
});

test("defaultChecklistProgress: every item starts unchecked, lastTestSuccessAt null", () => {
  const progress = defaultChecklistProgress("discord");
  const discord = checklistByValue("discord");
  assert.equal(Object.keys(progress.items).length, discord.items.length);
  for (const item of discord.items) {
    assert.equal(progress.items[item.id], false);
  }
  assert.equal(progress.lastTestSuccessAt, null);
});

test("defaultChecklistProgress: unknown type returns empty items, no throw", () => {
  const progress = defaultChecklistProgress("webhook");
  assert.deepEqual(progress.items, {});
  assert.equal(progress.lastTestSuccessAt, null);
});

test("loadChecklistProgress: nothing saved yet returns the default", () => {
  const storage = new FakeStorage();
  const progress = loadChecklistProgress(storage, "telegram", 5);
  assert.deepEqual(progress, defaultChecklistProgress("telegram"));
});

test("save then load round-trips exactly", () => {
  const storage = new FakeStorage();
  const telegram = checklistByValue("telegram");
  const progress = defaultChecklistProgress("telegram");
  progress.items[telegram.items[0].id] = true;
  progress.lastTestSuccessAt = 1700000000;

  saveChecklistProgress(storage, "telegram", 5, progress);
  const loaded = loadChecklistProgress(storage, "telegram", 5);
  assert.deepEqual(loaded, progress);
});

test("loadChecklistProgress: malformed JSON in storage falls back to default rather than throwing", () => {
  const storage = new FakeStorage();
  storage.setItem(checklistStorageKey("discord", 9), "{not valid json");
  const progress = loadChecklistProgress(storage, "discord", 9);
  assert.deepEqual(progress, defaultChecklistProgress("discord"));
});

test("loadChecklistProgress: non-object JSON value falls back to default", () => {
  const storage = new FakeStorage();
  storage.setItem(checklistStorageKey("discord", 9), "42");
  const progress = loadChecklistProgress(storage, "discord", 9);
  assert.deepEqual(progress, defaultChecklistProgress("discord"));
});

test("loadChecklistProgress: stale item ids from an older app version are dropped, new ids default to false", () => {
  const storage = new FakeStorage();
  const key = checklistStorageKey("whatsapp", 3);
  storage.setItem(
    key,
    JSON.stringify({ items: { no_longer_exists: true, token_type_confirmed: true }, lastTestSuccessAt: 42 })
  );
  const progress = loadChecklistProgress(storage, "whatsapp", 3);
  assert.equal(progress.items.no_longer_exists, undefined);
  assert.equal(progress.items.token_type_confirmed, true);
  assert.equal(progress.items.phone_number_id_confirmed, false);
  assert.equal(progress.lastTestSuccessAt, 42);
});

test("loadChecklistProgress: non-numeric lastTestSuccessAt is treated as null", () => {
  const storage = new FakeStorage();
  const key = checklistStorageKey("discord", 1);
  storage.setItem(key, JSON.stringify({ items: {}, lastTestSuccessAt: "not-a-number" }));
  const progress = loadChecklistProgress(storage, "discord", 1);
  assert.equal(progress.lastTestSuccessAt, null);
});

test("checklistCompletionCount: counts checked items against the total", () => {
  const progress = defaultChecklistProgress("discord");
  const discord = checklistByValue("discord");
  progress.items[discord.items[0].id] = true;
  progress.items[discord.items[1].id] = true;

  const { checked, total } = checklistCompletionCount("discord", progress);
  assert.equal(checked, 2);
  assert.equal(total, discord.items.length);
});

test("checklistCompletionCount: unknown type returns zero/zero without throwing", () => {
  assert.deepEqual(checklistCompletionCount("webhook", { items: {} }), { checked: 0, total: 0 });
});

test("draft (unsaved) channel and a saved channel of the same type track progress independently", () => {
  const storage = new FakeStorage();
  const draftProgress = defaultChecklistProgress("discord");
  draftProgress.items[checklistByValue("discord").items[0].id] = true;
  saveChecklistProgress(storage, "discord", "draft", draftProgress);

  const savedProgress = loadChecklistProgress(storage, "discord", 42);
  assert.deepEqual(savedProgress, defaultChecklistProgress("discord"));
});
