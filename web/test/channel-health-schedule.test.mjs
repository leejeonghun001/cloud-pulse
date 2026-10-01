// channel-health-schedule.test.mjs — pure UTC<->local weekday/minute
// conversion tests. Deliberately TZ-independent: this test runner's
// own TZ is unknown/uncontrolled (no lever to set process.env.TZ after
// startup and have V8 pick it up reliably across platforms), so
// assertions check the round-trip property (localToUtc(utcToLocal(x))
// === x) which holds regardless of the host's offset, plus one
// self-consistent check computed from the *same* Date APIs the module
// itself uses (never a hardcoded "assume UTC+9" expectation).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  utcToLocal,
  localToUtc,
  formatMinuteOfDay,
  parseMinuteOfDay,
  WEEKDAY_LABEL_KEYS,
} from "../assets/js/core/channel-health-schedule.js";

test("utcToLocal -> localToUtc round-trips for every weekday and several minutes", () => {
  const ref = new Date("2026-06-15T12:00:00Z"); // arbitrary fixed reference
  for (let weekday = 0; weekday <= 6; weekday++) {
    for (const minute of [0, 1, 359, 540, 719, 1439]) {
      const local = utcToLocal(weekday, minute, ref);
      const back = localToUtc(local.weekday, local.minuteOfDay, ref);
      assert.equal(back.weekday, weekday, `weekday round-trip failed for (${weekday}, ${minute})`);
      assert.equal(back.minuteOfDay, minute, `minute round-trip failed for (${weekday}, ${minute})`);
    }
  }
});

test("localToUtc -> utcToLocal round-trips (the inverse direction)", () => {
  const ref = new Date("2026-01-10T00:00:00Z");
  for (let weekday = 0; weekday <= 6; weekday++) {
    for (const minute of [0, 30, 600, 1000, 1438]) {
      const utc = localToUtc(weekday, minute, ref);
      const back = utcToLocal(utc.weekday, utc.minuteOfDay, ref);
      assert.equal(back.weekday, weekday);
      assert.equal(back.minuteOfDay, minute);
    }
  }
});

test("utcToLocal matches a self-consistent offset computed the same way the module does", () => {
  // Build the expected local time by constructing the exact UTC
  // instant for (weekday=Wednesday, 12:00 UTC) near ref, then reading
  // it back via the native Date local getters — the same primitives
  // channel-health-schedule.js itself uses internally, so this proves
  // utcToLocal's output matches native Date conversion without
  // asserting a specific numeric offset.
  const ref = new Date("2026-06-17T00:00:00Z"); // a Wednesday
  const instant = new Date(Date.UTC(2026, 5, 17, 12, 0, 0)); // Wed 12:00 UTC
  const want = { weekday: instant.getDay(), minuteOfDay: instant.getHours() * 60 + instant.getMinutes() };

  const got = utcToLocal(3, 720, ref); // Wednesday=3, 12:00=720
  assert.deepEqual(got, want);
});

test("utcToLocal never returns an out-of-range weekday or minute", () => {
  const ref = new Date();
  for (let weekday = 0; weekday <= 6; weekday++) {
    for (const minute of [0, 719, 1439]) {
      const { weekday: w, minuteOfDay: m } = utcToLocal(weekday, minute, ref);
      assert.ok(w >= 0 && w <= 6, `weekday out of range: ${w}`);
      assert.ok(m >= 0 && m <= 1439, `minuteOfDay out of range: ${m}`);
    }
  }
});

test("formatMinuteOfDay renders zero-padded HH:MM", () => {
  assert.equal(formatMinuteOfDay(0), "00:00");
  assert.equal(formatMinuteOfDay(5), "00:05");
  assert.equal(formatMinuteOfDay(540), "09:00");
  assert.equal(formatMinuteOfDay(1439), "23:59");
});

test("parseMinuteOfDay round-trips with formatMinuteOfDay", () => {
  for (const m of [0, 5, 60, 540, 1439]) {
    assert.equal(parseMinuteOfDay(formatMinuteOfDay(m)), m);
  }
});

test("parseMinuteOfDay rejects malformed or out-of-range input", () => {
  assert.equal(parseMinuteOfDay(""), null);
  assert.equal(parseMinuteOfDay("garbage"), null);
  assert.equal(parseMinuteOfDay("24:00"), null);
  assert.equal(parseMinuteOfDay("12:60"), null);
  assert.equal(parseMinuteOfDay("-1:00"), null);
});

test("WEEKDAY_LABEL_KEYS has exactly 7 entries, Sunday-first", () => {
  assert.equal(WEEKDAY_LABEL_KEYS.length, 7);
  assert.equal(WEEKDAY_LABEL_KEYS[0], "sun");
  assert.equal(WEEKDAY_LABEL_KEYS[6], "sat");
});
