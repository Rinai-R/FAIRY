export function resolveCharacterImageUrl(imagePath, origin) {
  if (typeof imagePath !== "string" || !imagePath.startsWith("/characters/")) {
    throw new TypeError("image path must use the local character namespace");
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

export function pixelCanvasSize(visual) {
  return Object.freeze({
    width: visual.frame.width * visual.scale,
    height: visual.frame.height * visual.scale,
  });
}

export function pixelTextureScale(visual, texture) {
  if (
    typeof texture?.width !== "number" ||
    typeof texture?.height !== "number" ||
    texture.width <= 0 ||
    texture.height <= 0
  ) {
    throw new TypeError("texture dimensions are required");
  }
  return Object.freeze({
    x: (visual.frame.width / texture.width) * visual.scale,
    y: (visual.frame.height / texture.height) * visual.scale,
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
