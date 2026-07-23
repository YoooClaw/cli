"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");

const {
  missingBinaryMessage,
  resolveOpenHarmonyBinary,
} = require("../bin/yc.js");

test("OpenHarmony prefers an explicitly configured HNP binary", () => {
  const expected = "/data/app/custom/yoooclaw";
  assert.equal(
    resolveOpenHarmonyBinary(
      { YOOOCLAW_NATIVE_BIN: expected },
      "0.6.3",
      (candidate) => candidate === expected,
    ),
    expected,
  );
});

test("OpenHarmony resolves a private HNP installed for this version", () => {
  const expected = "/private/yoooclaw.org/yoooclaw_0.6.3/bin/yoooclaw";
  assert.equal(
    resolveOpenHarmonyBinary(
      { HNP_PRIVATE_HOME: "/private" },
      "0.6.3",
      (candidate) => candidate === expected,
    ),
    expected,
  );
});

test("OpenHarmony resolves the private HNP bin link", () => {
  const expected = "/private/bin/yoooclaw-native";
  assert.equal(
    resolveOpenHarmonyBinary(
      { HNP_PRIVATE_HOME: "/private" },
      "0.6.3",
      (candidate) => candidate === expected,
    ),
    expected,
  );
});

test("OpenHarmony error explains the HNP requirement", () => {
  const message = missingBinaryMessage("openharmony", "arm64");
  assert.match(message, /HNP/);
  assert.match(message, /YOOOCLAW_NATIVE_BIN/);
  assert.doesNotMatch(message, /请尝试重新安装/);
});
