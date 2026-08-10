import { isWailsRuntime } from "./runtimeEnv.mjs";

function fairyCharacterRelativePath(imagePath) {
  if (imagePath.startsWith("fairy-character://localhost/")) {
    return imagePath.slice("fairy-character://localhost/".length);
  }
  if (imagePath.startsWith("http://fairy-character.localhost/")) {
    return imagePath.slice("http://fairy-character.localhost/".length);
  }
  return null;
}

export function resolveCharacterImageUrl(imagePath, origin) {
  if (
    typeof imagePath !== "string" ||
    (
      !imagePath.startsWith("/characters/") &&
      !imagePath.startsWith("fairy-character://localhost/") &&
      !imagePath.startsWith("http://fairy-character.localhost/")
    )
  ) {
    throw new TypeError("image path must use the local character namespace");
  }
  const relative = fairyCharacterRelativePath(imagePath);
  if (relative !== null) {
    if (isWailsRuntime()) {
      if (typeof origin !== "string" || origin.length === 0) {
        throw new TypeError("application origin is required");
      }
      return new URL(`/fairy-character/${relative}`, `${origin}/`).href;
    }
    return `fairy-character://localhost/${relative}`;
  }
  if (typeof origin !== "string" || origin.length === 0) {
    throw new TypeError("application origin is required");
  }
  return new URL(imagePath, `${origin}/`).href;
}

export function selectVisualStateImage(visual, visualState) {
  if (visual === null || typeof visual !== "object") {
    throw new TypeError("verified visual pack is required");
  }
  if (typeof visualState !== "string" || visualState.length === 0) {
    throw new TypeError("visual state is required");
  }
  if (!Array.isArray(visual.states)) {
    throw new TypeError("visual states must be an array");
  }
  const state = visual.states.find((candidate) => candidate.id === visualState);
  if (state === undefined) {
    throw new TypeError("visual state is not declared by the active pack");
  }
  return state;
}

export function hasVisualState(visual, visualState) {
  return Boolean(
    visual &&
      Array.isArray(visual.states) &&
      visual.states.some((candidate) => candidate.id === visualState),
  );
}

function normalizeDisplayScale(displayScale) {
  if (
    typeof displayScale !== "number" ||
    !Number.isFinite(displayScale) ||
    displayScale <= 0
  ) {
    throw new TypeError("pixel display scale must be positive");
  }
  return displayScale;
}

export function pixelCanvasSize(visual, displayScale = 1) {
  const scale = visual.scale * normalizeDisplayScale(displayScale);
  return Object.freeze({
    width: Math.round(visual.frame.width * scale),
    height: Math.round(visual.frame.height * scale),
  });
}

export function pixelTextureScale(visual, texture, displayScale = 1) {
  if (
    typeof texture?.width !== "number" ||
    typeof texture?.height !== "number" ||
    texture.width <= 0 ||
    texture.height <= 0
  ) {
    throw new TypeError("texture dimensions are required");
  }
  const scale = visual.scale * normalizeDisplayScale(displayScale);
  return Object.freeze({
    x: (visual.frame.width / texture.width) * scale,
    y: (visual.frame.height / texture.height) * scale,
  });
}

export function resolveRenderablePixelTexture(loaded) {
  if (loaded === null) return null;
  if (
    loaded === undefined ||
    typeof loaded !== "object" ||
    typeof loaded.imageUrl !== "string" ||
    loaded.imageUrl.length === 0 ||
    loaded.texture === undefined
  ) {
    throw new TypeError("loaded pixel texture must include imageUrl and texture");
  }
  return loaded;
}
