// disclosure.test.mjs — ui/disclosure.js: native <details> wrapper that
// remembers its open state across re-renders.
import { test } from "node:test";
import assert from "node:assert/strict";

class Node {
  constructor(tag) { this.tagName = tag; this.children = []; this.attributes = {}; this.listeners = {}; this.className = ""; this.textContent = ""; }
  append(...c) { this.children.push(...c); }
  setAttribute(k, v) { this.attributes[k] = String(v); }
  removeAttribute(k) { delete this.attributes[k]; }
  get open() { return "open" in this.attributes; }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  dispatch(type) { for (const fn of this.listeners[type] || []) fn(); }
}
globalThis.document = { createElement: (tag) => new Node(tag), createElementNS: (_ns, tag) => new Node(tag), createTextNode: (text) => ({ textContent: text }) };
const store = new Map();
globalThis.localStorage = { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, String(v)), removeItem: (k) => store.delete(k) };

const { disclosure } = await import("../assets/js/ui/disclosure.js");

test("disclosure starts closed unless asked, and remembers the user's choice by id", () => {
  const first = disclosure({ id: "x", summary: "OCI" });
  assert.equal(first.node.tagName, "details");
  assert.equal(first.node.open, false);
  assert.equal(first.node.children[0].tagName, "summary");

  first.node.setAttribute("open", "");
  first.node.dispatch("toggle");
  assert.equal(disclosure({ id: "x", summary: "OCI" }).node.open, true, "a re-render keeps it open");

  first.node.removeAttribute("open");
  first.node.dispatch("toggle");
  assert.equal(disclosure({ id: "x", summary: "OCI", open: true }).node.open, false, "a remembered close beats the default");

  assert.equal(disclosure({ summary: "no id", open: true }).node.open, true);
});

test("disclosure setSummary and setOpen", () => {
  const d = disclosure({ summary: "a" });
  d.setSummary("b");
  assert.equal(d.node.children[0].children[1].textContent, "b");
  d.setOpen(true);
  assert.equal(d.node.open, true);
});
