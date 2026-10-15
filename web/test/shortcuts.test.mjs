import { test } from "node:test";
import assert from "node:assert/strict";
import { registerShellShortcuts, shortcutAction } from "../assets/js/ui/shortcuts.js";

function key(key, options = {}) {
  const codes = { g: "KeyG", o: "KeyO", a: "KeyA", c: "KeyC", r: "KeyR", m: "KeyM", s: "KeyS", k: "KeyK" };
  return { key, code: codes[key] || "", ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, prevented: false, preventDefault() { this.prevented = true; }, ...options };
}

test("shortcut action ignores typing targets and non-palette modifiers", () => {
  const input = { matches: () => true, isContentEditable: false };
  assert.equal(shortcutAction(key("g"), input), null);
  assert.equal(shortcutAction(key("g", { altKey: true }), null), null);
  assert.equal(shortcutAction(key("?", { ctrlKey: true }), null), null);
  assert.equal(shortcutAction(key("k", { ctrlKey: true }), input), "palette");
});

test("question mark accepts Shift while composition, handled events, and dialogs suppress shortcuts", () => {
  assert.equal(shortcutAction(key("?", { shiftKey: true }), null), "shortcuts");
  assert.equal(shortcutAction(key("?", { defaultPrevented: true }), null), null);
  assert.equal(shortcutAction(key("?", { isComposing: true }), null), null);
  const dialogTarget = { closest: (selector) => selector === "dialog" ? {} : null };
  assert.equal(shortcutAction(key("/", { target: dialogTarget }), null), null);
  const modalDocument = { querySelectorAll: () => [{ matches: (selector) => selector === ":modal" }] };
  assert.equal(shortcutAction(key("/"), null, modalDocument), null);
});

test("g then a navigates while unrelated bare keys do not", () => {
  const previous = globalThis.document;
  const listeners = {};
  globalThis.document = {
    activeElement: null,
    addEventListener(type, fn) { listeners[type] = fn; },
    removeEventListener(type) { delete listeners[type]; },
  };
  try {
    const navigated = [];
    const stop = registerShellShortcuts({ openPalette() {}, openShortcuts() {}, navigate: (route) => navigated.push(route) });
    listeners.keydown(key("a"));
    listeners.keydown(key("g"));
    listeners.keydown(key("a"));
    assert.deepEqual(navigated, ["#/alerts"]);
    stop();
  } finally {
    if (previous === undefined) delete globalThis.document;
    else globalThis.document = previous;
  }
});

test("letter shortcuts use physical key codes and an expired g prefix does not navigate", () => {
  const previousDocument = globalThis.document;
  const previousSetTimeout = globalThis.setTimeout;
  const previousClearTimeout = globalThis.clearTimeout;
  const listeners = {};
  let expire;
  globalThis.document = {
    activeElement: null,
    addEventListener(type, fn) { listeners[type] = fn; },
    removeEventListener(type) { delete listeners[type]; },
  };
  globalThis.setTimeout = (fn) => { expire = fn; return 1; };
  globalThis.clearTimeout = () => {};
  try {
    const navigated = [];
    const stop = registerShellShortcuts({ openPalette() {}, openShortcuts() {}, navigate: (route) => navigated.push(route) });
    listeners.keydown(key("ㅎ", { code: "KeyG" }));
    listeners.keydown(key("ㅁ", { code: "KeyA" }));
    assert.deepEqual(navigated, ["#/alerts"]);
    listeners.keydown(key("g"));
    expire();
    listeners.keydown(key("a"));
    assert.deepEqual(navigated, ["#/alerts"]);
    stop();
  } finally {
    globalThis.setTimeout = previousSetTimeout;
    globalThis.clearTimeout = previousClearTimeout;
    if (previousDocument === undefined) delete globalThis.document;
    else globalThis.document = previousDocument;
  }
});

test("question mark opens the shortcuts dialog", () => {
  const previous = globalThis.document;
  const listeners = {};
  globalThis.document = {
    activeElement: null,
    addEventListener(type, fn) { listeners[type] = fn; },
    removeEventListener(type) { delete listeners[type]; },
  };
  try {
    let opened = 0;
    const stop = registerShellShortcuts({ openPalette() {}, openShortcuts() { opened++; }, navigate() {} });
    const event = key("?", { shiftKey: true });
    listeners.keydown(event);
    assert.equal(opened, 1);
    assert.equal(event.prevented, true);
    stop();
  } finally {
    if (previous === undefined) delete globalThis.document;
    else globalThis.document = previous;
  }
});

test("Ctrl+K opens the palette by physical key and by the key labelled K", () => {
  // Dvorak: the key labelled K reports key "k" but code "KeyV".
  assert.equal(shortcutAction(key("k", { ctrlKey: true, code: "KeyV" }), null), "palette");
  // Korean IME: physical K reports key "ㅏ" but code "KeyK".
  assert.equal(shortcutAction(key("ㅏ", { ctrlKey: true, code: "KeyK" }), null), "palette");
});
