// channel-health-banner.test.mjs — pure render + polling-driver tests
// for the app-shell "channel failing, no fallback available" banner
// (SPEC-v0.8 §3-2). Uses a tiny local DOM fake (no jsdom dependency,
// matching this codebase's other DOM-touching unit tests, e.g.
// billing-page.test.mjs) rather than querySelector/dispatchEvent,
// which the fake doesn't implement.
import { test } from "node:test";
import assert from "node:assert/strict";
import { setCatalogs, setLang } from "../assets/js/core/i18n.js";

setCatalogs({
  en: {
    health: {
      banner_message: { one: 'Channel "{name}" is failing.', other: "{count} channels are failing." },
      banner_dismiss: "Dismiss",
      banner_view: "View channel health",
    },
  },
  ko: {},
});
setLang("en");

function fakeDocument() {
  function makeNode(tagName) {
    return {
      tagName,
      children: [],
      attributes: {},
      listeners: {},
      className: "",
      textContent: "",
      append(...kids) {
        this.children.push(...kids);
      },
      setAttribute(name, value) {
        this.attributes[name] = String(value);
      },
      addEventListener(type, handler) {
        this.listeners[type] = handler;
      },
    };
  }
  return {
    createElement(tagName) {
      return makeNode(tagName);
    },
    createElementNS(_ns, tagName) {
      return makeNode(tagName);
    },
    createTextNode(text) {
      return { textContent: String(text) };
    },
  };
}

/** allText concatenates a fake node's own textContent plus every
 * descendant's, depth-first — a cheap substitute for
 * `node.textContent` on a real DOM element. */
function allText(node) {
  let out = node.textContent || "";
  for (const child of node.children || []) out += allText(child);
  return out;
}

/** findButtons collects every fake <button> node under root,
 * depth-first. */
function findButtons(node, out = []) {
  if (node.tagName === "button") out.push(node);
  for (const child of node.children || []) findButtons(child, out);
  return out;
}

async function withFakeDocument(fn) {
  const previous = globalThis.document;
  globalThis.document = fakeDocument();
  try {
    return await fn();
  } finally {
    if (previous === undefined) delete globalThis.document;
    else globalThis.document = previous;
  }
}

// --- channelHealthBanner (pure render) ---

test("channelHealthBanner returns null for an empty/missing list", async () => {
  await withFakeDocument(async () => {
    const { channelHealthBanner } = await import("../assets/js/ui/channel-health-banner.js");
    assert.equal(channelHealthBanner([]), null);
    assert.equal(channelHealthBanner(undefined), null);
    assert.equal(channelHealthBanner(null), null);
  });
});

test("channelHealthBanner renders the singular message for one channel", async () => {
  await withFakeDocument(async () => {
    const { channelHealthBanner } = await import("../assets/js/ui/channel-health-banner.js");
    const node = channelHealthBanner(["discord-ops"]);
    assert.ok(node);
    assert.match(allText(node), /discord-ops/);
  });
});

test("channelHealthBanner renders the plural message for multiple channels", async () => {
  await withFakeDocument(async () => {
    const { channelHealthBanner } = await import("../assets/js/ui/channel-health-banner.js");
    const node = channelHealthBanner(["a", "b", "c"]);
    assert.match(allText(node), /3 channels are failing/);
  });
});

test("channelHealthBanner includes a dismiss button only when onDismiss is given", async () => {
  await withFakeDocument(async () => {
    const { channelHealthBanner } = await import("../assets/js/ui/channel-health-banner.js");
    const withDismiss = channelHealthBanner(["a"], () => {});
    assert.equal(findButtons(withDismiss).length, 1);

    const withoutDismiss = channelHealthBanner(["a"]);
    assert.equal(findButtons(withoutDismiss).length, 0);
  });
});

test("channelHealthBanner's dismiss button invokes onDismiss when its click listener fires", async () => {
  await withFakeDocument(async () => {
    const { channelHealthBanner } = await import("../assets/js/ui/channel-health-banner.js");
    let called = false;
    const node = channelHealthBanner(["a"], () => {
      called = true;
    });
    const [btn] = findButtons(node);
    btn.listeners.click();
    assert.ok(called);
  });
});

// --- startChannelHealthBannerPolling (driver) ---

function withFakeFetch(responses, fn) {
  const originalFetch = globalThis.fetch;
  let call = 0;
  globalThis.fetch = async () => {
    const resp = responses[Math.min(call, responses.length - 1)];
    call++;
    if (resp.error) throw resp.error;
    return {
      ok: resp.status < 400,
      status: resp.status,
      headers: { get: () => null },
      json: async () => resp.body,
      text: async () => JSON.stringify(resp.body),
    };
  };
  return fn().finally(() => {
    globalThis.fetch = originalFetch;
  });
}

test("startChannelHealthBannerPolling renders banner_channel_names when banner_needed is true", async () => {
  const { startChannelHealthBannerPolling } = await import("../assets/js/ui/channel-health-banner.js");
  await withFakeFetch([{ status: 200, body: { channels: [], banner_needed: true, banner_channel_names: ["x"] } }], async () => {
    let rendered = null;
    const stop = startChannelHealthBannerPolling((names) => (rendered = names), { intervalMs: 1000000 });
    await new Promise((r) => setTimeout(r, 10));
    assert.deepEqual(rendered, ["x"]);
    stop();
  });
});

test("startChannelHealthBannerPolling renders an empty array when banner_needed is false", async () => {
  const { startChannelHealthBannerPolling } = await import("../assets/js/ui/channel-health-banner.js");
  await withFakeFetch([{ status: 200, body: { channels: [], banner_needed: false } }], async () => {
    let rendered = null;
    const stop = startChannelHealthBannerPolling((names) => (rendered = names), { intervalMs: 1000000 });
    await new Promise((r) => setTimeout(r, 10));
    assert.deepEqual(rendered, []);
    stop();
  });
});

test("startChannelHealthBannerPolling skips polling when isActive returns false", async () => {
  const { startChannelHealthBannerPolling } = await import("../assets/js/ui/channel-health-banner.js");
  await withFakeFetch([{ status: 200, body: { channels: [], banner_needed: true, banner_channel_names: ["x"] } }], async () => {
    let called = false;
    const stop = startChannelHealthBannerPolling(
      () => {
        called = true;
      },
      { intervalMs: 1000000, isActive: () => false },
    );
    await new Promise((r) => setTimeout(r, 10));
    assert.equal(called, false);
    stop();
  });
});

test("startChannelHealthBannerPolling pauses while hidden and refreshes on visibility", async () => {
  const { startChannelHealthBannerPolling } = await import("../assets/js/ui/channel-health-banner.js");
  const previousDocument = globalThis.document;
  const listeners = {};
  globalThis.document = {
    hidden: true,
    addEventListener(type, handler) { listeners[type] = handler; },
    removeEventListener(type) { delete listeners[type]; },
  };
  try {
    await withFakeFetch([{ status: 200, body: { channels: [], banner_needed: false } }], async () => {
      let renders = 0;
      const stop = startChannelHealthBannerPolling(() => { renders++; }, { intervalMs: 1000000 });
      await new Promise((resolve) => setTimeout(resolve, 10));
      assert.equal(renders, 0, "hidden tabs do not start a health request");
      globalThis.document.hidden = false;
      listeners.visibilitychange();
      await new Promise((resolve) => setTimeout(resolve, 10));
      assert.equal(renders, 1, "becoming visible refreshes immediately");
      stop();
    });
  } finally {
    if (previousDocument === undefined) delete globalThis.document;
    else globalThis.document = previousDocument;
  }
});

test("startChannelHealthBannerPolling clears the banner on a 401 (session ended)", async () => {
  const { startChannelHealthBannerPolling } = await import("../assets/js/ui/channel-health-banner.js");
  await withFakeFetch([{ status: 401, body: { error: "session expired" } }], async () => {
    let rendered = "unset";
    const stop = startChannelHealthBannerPolling((names) => (rendered = names), { intervalMs: 1000000 });
    await new Promise((r) => setTimeout(r, 10));
    assert.deepEqual(rendered, []);
    stop();
  });
});

test("startChannelHealthBannerPolling stop() halts further polling", async () => {
  const { startChannelHealthBannerPolling } = await import("../assets/js/ui/channel-health-banner.js");
  await withFakeFetch([{ status: 200, body: { channels: [], banner_needed: false } }], async () => {
    let calls = 0;
    const stop = startChannelHealthBannerPolling(
      () => {
        calls++;
      },
      { intervalMs: 5 },
    );
    await new Promise((r) => setTimeout(r, 12));
    stop();
    const callsAtStop = calls;
    await new Promise((r) => setTimeout(r, 20));
    assert.equal(calls, callsAtStop, "no further calls after stop()");
  });
});
