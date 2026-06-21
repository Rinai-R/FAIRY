import assert from "node:assert/strict";
import { APP_VIEWS, hashForView, normalizeAppView, viewFromLocationHash } from "./navigation.js";

assert.deepEqual(
  APP_VIEWS,
  ["dashboard", "history", "director", "config", "logs", "stage"],
  "导航视图白名单应覆盖所有工作台页面和舞台",
);

assert.equal(normalizeAppView("logs"), "logs");
assert.equal(normalizeAppView("#history"), "history");
assert.equal(normalizeAppView("unknown"), "dashboard");
assert.equal(normalizeAppView(""), "dashboard");

assert.equal(viewFromLocationHash("#stage"), "stage");
assert.equal(viewFromLocationHash("#dashboard"), "dashboard");
assert.equal(viewFromLocationHash("#/logs"), "dashboard", "不支持的 hash 形态应回到首页");

assert.equal(hashForView("logs"), "#logs");
assert.equal(hashForView("bad"), "#dashboard");

console.log("navigation tests passed");
