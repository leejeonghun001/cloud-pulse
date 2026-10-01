// router-deeplink.test.mjs — pathToHashRoute (hub-served deep links).
import { test } from "node:test";
import assert from "node:assert/strict";
import { pathToHashRoute } from "../assets/js/core/router.js";

test("pathToHashRoute: root URL is left alone", () => {
  assert.equal(pathToHashRoute("/", "", ""), null);
  assert.equal(pathToHashRoute("/", "", "#/alerts"), null);
  assert.equal(pathToHashRoute("", "", ""), null);
});

test("pathToHashRoute: path becomes the hash route", () => {
  assert.equal(pathToHashRoute("/login", "", ""), "/#/login");
  assert.equal(pathToHashRoute("/settings/network", "", ""), "/#/settings/network");
  assert.equal(pathToHashRoute("/login/", "", ""), "/#/login");
  assert.equal(pathToHashRoute("/login", "?next=%23%2Falerts", ""), "/#/login?next=%23%2Falerts");
});

test("pathToHashRoute: an existing hash wins over the path", () => {
  assert.equal(pathToHashRoute("/login", "", "#/alerts"), "/#/alerts");
  assert.equal(pathToHashRoute("/login", "", "#"), "/#/login");
});

test("isRouteHash: in-page anchors are not routes", async () => {
  const { isRouteHash } = await import("../assets/js/core/router.js");
  assert.equal(isRouteHash(""), true);
  assert.equal(isRouteHash("#"), true);
  assert.equal(isRouteHash("#/settings/network"), true);
  assert.equal(isRouteHash("#cp-main"), false);
});
