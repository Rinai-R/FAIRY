// The standalone speech-bubble window has no companion session of its own. It
// passively rebuilds just enough turn state (streamed draft + waiting flag) from
// the globally broadcast harness events to render the bubble. Unlike
// reduceCompanionState this is a lenient observer: it never throws on ordering
// and simply tracks the most recent turn.

export function createSpeechObserver() {
  return Object.freeze({
    turnId: null,
    chains: Object.freeze([]),
    revealThrough: -1,
    draft: "",
    speechRequest: null,
    waiting: false,
    active: false,
  });
}

function draftFromChains(chains, revealThrough) {
  // Each reply chain is its own bubble: show only the beat currently being
  // spoken, not the accumulation of every revealed beat.
  if (!Array.isArray(chains) || revealThrough < 0 || revealThrough >= chains.length) {
    return "";
  }
  const chain = chains[revealThrough];
  return chain && typeof chain.text === "string" ? chain.text : "";
}

function upsertChain(chains, index, text, visualState) {
  const next = chains.map((chain) => ({ ...chain }));
  while (next.length <= index) {
    next.push({ index: next.length, text: "", visualState: "idle" });
  }
  next[index] = Object.freeze({
    index,
    text: typeof text === "string" ? text : "",
    visualState: typeof visualState === "string" && visualState ? visualState : "idle",
  });
  return Object.freeze(next);
}

function withRevealedDraft(state, chains, revealThrough) {
  const clamped = Math.min(Math.max(revealThrough, -1), Math.max(chains.length - 1, -1));
  return Object.freeze({
    ...state,
    chains,
    revealThrough: clamped,
    draft: draftFromChains(chains, clamped),
  });
}

/** Advance which reply chains are visible in the bubble (TTS / timed reveal). */
export function revealSpeechObserverThrough(state, index) {
  if (!state || typeof state !== "object") return createSpeechObserver();
  const target = Number.isSafeInteger(index) ? index : -1;
  if (target <= state.revealThrough) return state;
  return withRevealedDraft(state, state.chains, target);
}

// reduceSpeechObserver consumes an already-parsed harness event (the shape
// returned by parseHarnessEvent).
export function reduceSpeechObserver(state, event) {
  const base = event.turnId === state.turnId
    ? state
    : {
      turnId: event.turnId,
      chains: Object.freeze([]),
      revealThrough: -1,
      draft: "",
      speechRequest: null,
      waiting: true,
      active: true,
    };

  const payload = event.payload;
  switch (payload.type) {
    case "utterance": {
      // Legacy progressive text. Prefer beat.ready; ignore bare utterance for
      // bubble reveal so 齐套才揭示 stays intact when both are present.
      return base === state ? state : Object.freeze({ ...base, waiting: base.draft.length === 0, active: true });
    }
    case "beat.ready": {
      const display = typeof payload.displayText === "string" ? payload.displayText : "";
      if (!display) {
        return base === state ? state : Object.freeze(base);
      }
      const kind = payload.kind === "final" ? "final" : "utterance";
      if (kind === "final" && Number.isSafeInteger(payload.chainIndex) && payload.chainIndex >= 0) {
        const chains = upsertChain(base.chains, payload.chainIndex, display, payload.visualState);
        return Object.freeze({
          ...withRevealedDraft(base, chains, payload.chainIndex),
          waiting: false,
          active: true,
        });
      }
      return Object.freeze({
        ...base,
        draft: display,
        waiting: false,
        active: true,
      });
    }
    case "reply_chain": {
      const chains = upsertChain(base.chains, payload.index, payload.text, payload.visualState);
      // Store chains but do not reveal draft yet — beat.ready (final) reveals.
      return Object.freeze({
        ...base,
        chains,
        waiting: base.draft.length === 0 && base.revealThrough < 0,
        active: true,
      });
    }
    case "text_delta": {
      return Object.freeze({
        ...base,
        draft: base.draft + (payload.delta ?? ""),
        waiting: false,
        active: true,
      });
    }
    case "completed": {
      let chains = base.chains;
      if (Array.isArray(payload.chains) && payload.chains.length > 0) {
        chains = Object.freeze(payload.chains.map((chain, index) => Object.freeze({
          index,
          text: chain.text,
          visualState: chain.visualState || "idle",
        })));
      }
      const revealThrough = base.revealThrough >= 0
        ? base.revealThrough
        : (chains.length > 0 ? 0 : -1);
      return Object.freeze({
        ...withRevealedDraft(base, chains, revealThrough),
        speechRequest: null,
        waiting: false,
        active: true,
      });
    }
    case "failed":
      return Object.freeze({
        ...base,
        chains: Object.freeze([]),
        revealThrough: -1,
        draft: "",
        speechRequest: null,
        waiting: false,
        active: false,
      });
    case "state_changed":
      if (event.state === "interrupted") {
        return Object.freeze({
          ...base,
          chains: Object.freeze([]),
          revealThrough: -1,
          draft: "",
          speechRequest: null,
          waiting: false,
          active: false,
        });
      }
      return Object.freeze({
        ...base,
        waiting: base.draft.length === 0 && base.revealThrough < 0,
        active: true,
      });
    case "speech.requested":
      return Object.freeze({
        ...base,
        speechRequest: Object.freeze({
          turnId: event.turnId,
          text: payload.text,
        }),
        waiting: false,
        active: true,
      });
    case "speech.synthesized":
    case "speech.failed":
      return base === state ? state : Object.freeze(base);
    default:
      return base === state ? state : Object.freeze(base);
  }
}

export function speechBubbleVisible(state) {
  return state.active && (state.draft.length > 0 || state.waiting);
}
