const VISUAL_LAYOUT_PRESETS = Object.freeze({
  full_body: Object.freeze({
    stage: Object.freeze({
      left: "72%",
      top: "7vh",
      height: "86vh",
      translate_x: "-50%",
      translate_y: "0",
      scale: "1"
    }),
    home: Object.freeze({
      left: "50%",
      bottom: "128px",
      height: "74%",
      translate_x: "-50%",
      translate_y: "0",
      scale: "1"
    }),
    director: Object.freeze({
      left: "50%",
      top: "72px",
      height: "76%",
      translate_x: "-50%",
      translate_y: "0",
      scale: "1"
    })
  }),
  bust: Object.freeze({
    stage: Object.freeze({
      left: "72%",
      top: "12vh",
      height: "62vh",
      translate_x: "-50%",
      translate_y: "0",
      scale: "1"
    }),
    home: Object.freeze({
      left: "50%",
      bottom: "172px",
      height: "56%",
      translate_x: "-50%",
      translate_y: "0",
      scale: "1"
    }),
    director: Object.freeze({
      left: "50%",
      top: "108px",
      height: "58%",
      translate_x: "-50%",
      translate_y: "0",
      scale: "1"
    })
  })
});

const DEFAULT_PRESET = "full_body";

export function resolveCharacterPortrait(character, expression, mood) {
  const assets = character?.assets || {};
  const moodAsset = assets.moods?.[expression] || assets.moods?.[mood] || null;
  return {
    url: moodAsset?.portrait_url || assets.portrait_url || character?.avatar_url || "",
    layout: mergeVisualLayout(assets.visual_layout || assets.layout, moodAsset?.visual_layout || moodAsset?.layout)
  };
}

export function characterVisualStyle(layout, surface) {
  const resolved = resolveVisualLayout(layout);
  const view = resolved[surface] || {};
  const prefix = surface === "home" || surface === "director" ? surface : "stage";
  const style = {};
  setCSSVariable(style, `--character-${prefix}-left`, view.left);
  setCSSVariable(style, `--character-${prefix}-top`, view.top);
  setCSSVariable(style, `--character-${prefix}-bottom`, view.bottom);
  setCSSVariable(style, `--character-${prefix}-height`, view.height);
  setCSSVariable(style, `--character-${prefix}-translate-x`, view.translate_x);
  setCSSVariable(style, `--character-${prefix}-translate-y`, view.translate_y);
  setCSSVariable(style, `--character-${prefix}-scale`, view.scale);
  return style;
}

function mergeVisualLayout(baseLayout, overrideLayout) {
  const base = resolveVisualLayout(baseLayout);
  if (!overrideLayout) return base;
  const override = layoutObject(overrideLayout);
  const preset = override.preset || base.preset || DEFAULT_PRESET;
  return resolveVisualLayout({
    preset,
    stage: { ...base.stage, ...(override.stage || {}) },
    home: { ...base.home, ...(override.home || {}) },
    director: { ...base.director, ...(override.director || {}) }
  });
}

function resolveVisualLayout(layout) {
  const source = layoutObject(layout);
  const presetID = typeof layout === "string" ? layout : source.preset || DEFAULT_PRESET;
  const preset = VISUAL_LAYOUT_PRESETS[presetID] || VISUAL_LAYOUT_PRESETS[DEFAULT_PRESET];
  return {
    preset: VISUAL_LAYOUT_PRESETS[presetID] ? presetID : DEFAULT_PRESET,
    stage: { ...preset.stage, ...(source.stage || {}) },
    home: { ...preset.home, ...(source.home || {}) },
    director: { ...preset.director, ...(source.director || {}) }
  };
}

function layoutObject(layout) {
  if (!layout || typeof layout !== "object" || Array.isArray(layout)) return {};
  return layout;
}

function cssValue(value) {
  if (value === undefined || value === null || value === "") return undefined;
  if (typeof value === "number") return `${value}%`;
  return String(value);
}

function setCSSVariable(style, name, value) {
  const next = cssValue(value);
  if (next !== undefined) style[name] = next;
}
