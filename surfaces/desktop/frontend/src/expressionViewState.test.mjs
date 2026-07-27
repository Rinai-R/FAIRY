import assert from "node:assert/strict";
import test from "node:test";

import {
  appendExpressionPart,
  expressionPartFromTurn,
  markStickerUnavailable,
} from "./expressionViewState.mjs";

test("expression parts preserve utterance sticker utterance order", () => {
  const turns = [
    { type: "beat.ready", turnId: "t1", beat: { beatId: "b1", part: { kind: "utterance", text: "先说" } } },
    {
      type: "beat.ready",
      turnId: "t1",
      beat: {
        beatId: "b2",
        part: { kind: "sticker", sticker: { description: "开心" } },
        stickerUrl: "/characters/stickers/asset-1",
      },
    },
    { type: "beat.ready", turnId: "t1", beat: { beatId: "b3", part: { kind: "utterance", text: "再说" } } },
  ];
  const parts = turns.reduce((current, turn) => appendExpressionPart(current, expressionPartFromTurn(turn)), []);
  assert.deepEqual(parts.map((part) => part.kind), ["utterance", "sticker", "utterance"]);
  assert.equal(parts[1].url, "/characters/stickers/asset-1");
});

test("sticker rejects uncontrolled URLs and exposes unavailable state", () => {
  const part = expressionPartFromTurn({
    type: "beat.ready",
    turnId: "t1",
    beat: {
      beatId: "b1",
      part: { kind: "sticker", sticker: { description: "拒绝外链" } },
      stickerUrl: "https://example.com/sticker.png",
    },
  });
  assert.equal(part.url, "");
  assert.equal(part.unavailable, true);
  assert.equal(part.error, "表情包不可用");
});

test("failed image load replaces the image with an explicit unavailable state", () => {
  const parts = [{
    key: "t1:b1",
    kind: "sticker",
    url: "/characters/stickers/asset-1",
    unavailable: false,
  }];
  const next = markStickerUnavailable(parts, "t1:b1", "图片加载失败");
  assert.equal(next[0].url, "");
  assert.equal(next[0].unavailable, true);
  assert.equal(next[0].error, "图片加载失败");
});
