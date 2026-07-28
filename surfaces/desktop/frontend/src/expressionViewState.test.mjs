import assert from "node:assert/strict";
import test from "node:test";

import {
  appendExpressionPart,
  expressionPartFromTurn,
  historyExpressionParts,
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

test("history expression preserves utterance sticker utterance order", () => {
  const parts = historyExpressionParts({
    id: "message-1",
    content: "legacy projection",
    parts: [
      { kind: "utterance", text: "先这样" },
      { kind: "sticker", sticker: { id: "sticker-1", description: "开心点头", mimeType: "image/png" } },
      { kind: "utterance", text: "再继续" },
    ],
  });
  assert.deepEqual(parts.map((part) => part.kind), ["utterance", "sticker", "utterance"]);
  assert.equal(parts[1].description, "开心点头");
});

test("pure sticker history remains visible when content is empty", () => {
  const parts = historyExpressionParts({
    id: "message-2",
    content: "",
    parts: [{ kind: "sticker", sticker: { description: "无语摊手" } }],
  });
  assert.deepEqual(parts, [{
    key: "message-2:0",
    kind: "sticker",
    description: "无语摊手",
  }]);
});

test("legacy text history falls back to content without inventing sticker parts", () => {
  const parts = historyExpressionParts({
    id: "message-3",
    content: "旧消息",
    parts: [],
  });
  assert.deepEqual(parts, [{
    key: "message-3:legacy",
    kind: "utterance",
    text: "旧消息",
  }]);
});
