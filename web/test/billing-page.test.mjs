// billing-page.test.mjs — regression coverage for Settings → Billing DOM rendering.
// This deliberately uses a tiny local DOM fake rather than a browser dependency;
// its fixed clock makes the audit timestamp assertion timezone-independent.
import { test } from "node:test";
import assert from "node:assert/strict";

function fakeDocument() {
  return {
    createElement(tagName) {
      return {
        tagName,
        children: [],
        attributes: {},
        className: "",
        textContent: "",
        append(...children) {
          this.children.push(...children);
        },
        setAttribute(name, value) {
          this.attributes[name] = String(value);
        },
      };
    },
    createTextNode(text) {
      return { textContent: String(text) };
    },
  };
}

test("auditRow renders a Unix-second timestamp through the relative-time formatter", async () => {
  const previousDocument = globalThis.document;
  globalThis.document = fakeDocument();
  try {
    const { auditRow } = await import("../assets/js/pages/settings/billing.js");
    const thenSeconds = 1_700_000_000;
    const row = auditRow({
      at: thenSeconds,
      actor: "admin",
      action: "update",
      entity_type: "billing_interval",
      entity_id: 0,
      before_json: '"24h"',
      after_json: '"6h"',
    }, (thenSeconds + 60) * 1000);

    assert.equal(row.children[0].textContent, "1m ago");
    assert.equal(row.children[1].textContent, "admin");
    assert.equal(row.children[4].children[0].textContent, '"24h"');
    assert.equal(row.children[4].children[2].textContent, '"6h"');
  } finally {
    if (previousDocument === undefined) delete globalThis.document;
    else globalThis.document = previousDocument;
  }
});
