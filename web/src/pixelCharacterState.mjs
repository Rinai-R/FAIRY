const SESSION_STATES = new Set([
  "idle",
  "interpreting",
  "gathering",
  "planning",
  "responding",
  "completed",
  "interrupted",
  "failed",
]);
const IDLE_ACTIONS = new Set(["idle"]);
const EMOTIONS = new Set(["neutral", "happy", "sad"]);
const DIRECTIONS = new Set(["left", "right"]);
const STATE_IMAGE_DISPLACEMENT_PX = 0;

function assertBoolean(value, label) {
  if (typeof value !== "boolean") throw new TypeError(`${label} must be a boolean`);
  return value;
}

function assertEnum(value, allowed, label) {
  if (!allowed.has(value)) throw new TypeError(`${label} is unsupported`);
  return value;
}

function freezeAvailableStates(value) {
  if (!Array.isArray(value) || value.length < 1 || value.length > 16) {
    throw new TypeError("context.availableStates must contain 1-16 entries");
  }
  const states = Object.freeze(value.map((state, index) => {
    if (typeof state !== "string" || state.length === 0) {
      throw new TypeError(`context.availableStates[${index}] must be a visual state id`);
    }
    return state;
  }));
  if (new Set(states).size !== states.length || !states.includes("idle")) {
    throw new TypeError("context.availableStates must include unique idle state");
  }
  return states;
}

function freezeContext(context) {
  if (context === null || typeof context !== "object" || Array.isArray(context)) {
    throw new TypeError("pixel character context must be an object");
  }
  const actual = Object.keys(context).sort();
  const expected = [
    "availableStates",
    "chatOpen",
    "dragging",
    "petVisible",
    "reducedMotion",
    "sessionState",
    "settingsOpen",
    "submitting",
  ].sort();
  if (actual.length !== expected.length || actual.some((key, index) => key !== expected[index])) {
    throw new TypeError("pixel character context has an invalid field set");
  }
  return Object.freeze({
    availableStates: freezeAvailableStates(context.availableStates),
    chatOpen: assertBoolean(context.chatOpen, "context.chatOpen"),
    dragging: assertBoolean(context.dragging, "context.dragging"),
    petVisible: assertBoolean(context.petVisible, "context.petVisible"),
    reducedMotion: assertBoolean(context.reducedMotion, "context.reducedMotion"),
    sessionState: assertEnum(context.sessionState, SESSION_STATES, "context.sessionState"),
    settingsOpen: assertBoolean(context.settingsOpen, "context.settingsOpen"),
    submitting: assertBoolean(context.submitting, "context.submitting"),
  });
}

function defaultContext() {
  return freezeContext({
    availableStates: ["idle"],
    chatOpen: false,
    dragging: false,
    petVisible: true,
    reducedMotion: false,
    sessionState: "idle",
    settingsOpen: false,
    submitting: false,
  });
}

export function canRunPixelAutonomy(context) {
  freezeContext(context);
  return false;
}

function chooseAvailable(context, preferred) {
  return context.availableStates.includes(preferred) ? preferred : "idle";
}

export function selectPixelVisualState(state) {
  if (state.context.dragging) return chooseAvailable(state.context, "dragged");
  if (state.explicitVisualState !== null) {
    return chooseAvailable(state.context, state.explicitVisualState);
  }
  if (state.emotion !== "neutral") return chooseAvailable(state.context, state.emotion);
  return "idle";
}

export function shouldApplyReplyVisualState(event) {
  return event?.payload?.type === "completed";
}

function finalize(state) {
  const visualState = selectPixelVisualState(state);
  return Object.freeze({ ...state, visualState, displacement: 0 });
}

export function createPixelCharacterState(context = defaultContext()) {
  return finalize({
    context: freezeContext(context),
    idleAction: "idle",
    emotion: "neutral",
    direction: "right",
    visualState: "idle",
    explicitVisualState: null,
    displacement: 0,
  });
}

export function reducePixelCharacterState(state, action) {
  if (state === null || typeof state !== "object" || Object.isFrozen(state) === false) {
    throw new TypeError("pixel character state must be an immutable state");
  }
  if (action === null || typeof action !== "object" || Array.isArray(action)) {
    throw new TypeError("pixel character action must be an object");
  }
  switch (action.type) {
    case "context_changed": {
      const context = freezeContext(action.context);
      return finalize({
        ...state,
        context,
        idleAction: "idle",
      });
    }
    case "idle_started": {
      const idleAction = assertEnum(action.action, IDLE_ACTIONS, "idle action");
      const direction = action.direction === undefined
        ? state.direction
        : assertEnum(action.direction, DIRECTIONS, "idle direction");
      if (!canRunPixelAutonomy(state.context)) return state;
      return finalize({ ...state, idleAction, direction });
    }
    case "idle_finished":
      return finalize({ ...state, idleAction: "idle" });
    case "emotion_changed":
      return finalize({
        ...state,
        emotion: assertEnum(action.emotion, EMOTIONS, "pixel emotion"),
      });
    case "visual_state_changed": {
      if (typeof action.visualState !== "string" || action.visualState.length === 0) {
        throw new TypeError("visual state must be a non-empty string");
      }
      return finalize({ ...state, explicitVisualState: action.visualState });
    }
    default:
      throw new TypeError(`unsupported pixel character action: ${String(action.type)}`);
  }
}

export const PIXEL_STATE_IMAGE_DISPLACEMENT_PX = STATE_IMAGE_DISPLACEMENT_PX;
