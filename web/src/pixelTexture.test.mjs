import test from "node:test";
import assert from "node:assert/strict";

import {
  hasVisualState,
  pixelCanvasSize,
  pixelTextureScale,
  resolveRenderablePixelTexture,
  resolveCharacterImageUrl,
  selectVisualStateImage,
} from "./pixelTexture.mjs";

function visual() {
  return {
    frame: { width: 16, height: 16 },
    scale: 6,
    states: [
      {
        id: "idle",
        description: "Quiet standing pose.",
        imagePath: "/characters/atri/idle.png",
      },
      {
        id: "happy",
        description: "Happy response pose.",
        imagePath: "/characters/atri/happy.png",
      },
    ],
  };
}

test("state image selection uses only declared states", () => {
  const selected = selectVisualStateImage(visual(), "happy");

  assert.equal(selected.imagePath, "/characters/atri/happy.png");
  assert.equal(hasVisualState(visual(), "idle"), true);
  assert.equal(hasVisualState(visual(), "thinking"), false);
  assert.throws(() => selectVisualStateImage(visual(), "thinking"), /not declared/);
  assert.throws(() => selectVisualStateImage(null, "idle"), /visual pack/);
});

test("canvas size follows the verified frame scale", () => {
  assert.deepEqual(pixelCanvasSize(visual()), { width: 96, height: 96 });
  assert.deepEqual(pixelCanvasSize(visual(), 1.69), { width: 162, height: 162 });
  assert.throws(() => pixelCanvasSize(visual(), 0), /display scale/);
});

test("texture scale maps high-resolution assets back to logical frame size", () => {
  assert.deepEqual(
    pixelTextureScale(visual(), { width: 64, height: 64 }),
    { x: 1.5, y: 1.5 },
  );
  assert.deepEqual(
    pixelTextureScale({ ...visual(), frame: { width: 128, height: 192 }, scale: 1 }, {
      width: 512,
      height: 768,
    }),
    { x: 0.25, y: 0.25 },
  );
  assert.deepEqual(
    pixelTextureScale({ ...visual(), frame: { width: 128, height: 192 }, scale: 1 }, {
      width: 512,
      height: 768,
    }, 1.69),
    { x: 0.4225, y: 0.4225 },
  );
  assert.throws(() => pixelTextureScale(visual(), { width: 0, height: 64 }), /dimensions/);
  assert.throws(() => pixelTextureScale(visual(), { width: 64, height: 64 }, Number.NaN), /display scale/);
});

test("renderable texture keeps the last loaded image during state transitions", () => {
  const loaded = Object.freeze({
    imageUrl: "tauri://localhost/characters/atri/idle.png",
    texture: Object.freeze({ width: 512, height: 768 }),
  });

  assert.equal(resolveRenderablePixelTexture(null), null);
  assert.equal(resolveRenderablePixelTexture(loaded), loaded);
  assert.throws(() => resolveRenderablePixelTexture({ imageUrl: "" }), /loaded pixel texture/);
});

test("image URL keeps the Tauri localhost origin", () => {
  assert.equal(
    resolveCharacterImageUrl("/characters/atri/idle.png", "tauri://localhost"),
    "tauri://localhost/characters/atri/idle.png",
  );
  assert.equal(
    resolveCharacterImageUrl("/characters/atri/idle.png", "https://fairy.example"),
    "https://fairy.example/characters/atri/idle.png",
  );
  assert.equal(
    resolveCharacterImageUrl("fairy-character://localhost/fairy.atri/idle.png", "tauri://localhost"),
    "fairy-character://localhost/fairy.atri/idle.png",
  );
  assert.equal(
    resolveCharacterImageUrl("http://fairy-character.localhost/fairy.atri/idle.png", "tauri://localhost"),
    "fairy-character://localhost/fairy.atri/idle.png",
  );
  assert.throws(
    () => resolveCharacterImageUrl("https://example.com/remote.png", "tauri://localhost"),
    /local character namespace/,
  );
});
