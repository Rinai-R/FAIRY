import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { motion } from "motion/react";

import {
  createTypewriterState,
  isTypewriterCaughtUp,
  setTypewriterTarget,
  tickTypewriter,
} from "../typewriter.mjs";
import {
  SPEECH_BUBBLE_FADE_AFTER_MS,
  createSpeechBubbleState,
  reduceSpeechBubbleState,
  speechBubbleSurface,
} from "../speechBubbleState.mjs";

const TYPEWRITER_CHARS_PER_TICK = 1;
const TYPEWRITER_TICK_MS = 20;

/**
 * Assistant-only light speech bubble beside the character.
 * Waiting state stays until reply text arrives; then typewriter + dwell fade.
 * When mayFade is false (TTS still playing or failed), the bubble stays up.
 */
export function CharacterSpeechBubble({
  targetText,
  waiting,
  characterName,
  mayFade = true,
  fadeAfterMs = SPEECH_BUBBLE_FADE_AFTER_MS,
  onFaded,
}) {
  const bubbleRef = useRef(null);
  const [typewriter, setTypewriter] = useState(() => createTypewriterState());
  const [bubble, setBubble] = useState(() => createSpeechBubbleState());
  const hasTarget = typeof targetText === "string" && targetText.length > 0;
  const surface = speechBubbleSurface(targetText, waiting);
  const caughtUp = isTypewriterCaughtUp(typewriter)
    && typewriter.target.length > 0
    && typewriter.target === bubble.target;
  const visible = surface.mode !== "hidden";

  useLayoutEffect(() => {
    if (!hasTarget) {
      // Always drop the previous typewriter when the target is empty. A new turn
      // sets waiting=true with draft="" (thinking dots); keeping the old visible
      // text under those dots is the "speaking then send" stuck-bubble bug.
      setBubble(createSpeechBubbleState());
      setTypewriter(createTypewriterState());
      return;
    }
    setBubble((prev) => reduceSpeechBubbleState(prev, { type: "set_target", target: targetText }));
    setTypewriter((prev) => setTypewriterTarget(prev, targetText));
  }, [hasTarget, targetText, waiting]);

  useEffect(() => {
    if (!hasTarget || bubble.fading) return undefined;
    if (isTypewriterCaughtUp(typewriter)) return undefined;
    const id = setInterval(() => {
      setTypewriter((prev) => tickTypewriter(prev, TYPEWRITER_CHARS_PER_TICK));
    }, TYPEWRITER_TICK_MS);
    return () => clearInterval(id);
  }, [hasTarget, bubble.fading, typewriter.target, typewriter.visible]);

  useEffect(() => {
    if (!hasTarget || waiting || bubble.fading || !caughtUp || !mayFade) return undefined;
    const dwellMs = typeof fadeAfterMs === "number" && fadeAfterMs >= 0
      ? fadeAfterMs
      : SPEECH_BUBBLE_FADE_AFTER_MS;
    const dwell = setTimeout(() => {
      setBubble((prev) => reduceSpeechBubbleState(prev, { type: "start_fade", at: Date.now() }));
    }, dwellMs);
    return () => clearTimeout(dwell);
  }, [hasTarget, waiting, bubble.fading, caughtUp, mayFade, fadeAfterMs]);

  useLayoutEffect(() => {
    const el = bubbleRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [typewriter.visible]);

  useEffect(() => {
    if (!bubble.fading) return undefined;
    const id = setTimeout(() => {
      setBubble(createSpeechBubbleState());
      setTypewriter(createTypewriterState());
      onFaded?.();
    }, 480);
    return () => clearTimeout(id);
  }, [bubble.fading, onFaded]);

  if (!visible) return null;

  return (
    <motion.aside
      ref={bubbleRef}
      className={`fairy-speech-bubble${bubble.fading ? " is-fading" : ""}${surface.showWaiting ? " is-waiting" : ""}`}
      aria-label={characterName ? `${characterName}说` : "角色回复"}
      aria-busy={surface.showWaiting}
      initial={false}
      animate={{ opacity: bubble.fading ? 0 : 1, y: 0, scale: 1 }}
      transition={{ duration: bubble.fading ? 0.45 : 0.18 }}
    >
      {surface.showWaiting ? (
        <div className="fairy-speech-bubble__waiting" aria-hidden="true">
          <span /><span /><span />
        </div>
      ) : null}
      {surface.showText && typewriter.visible.length > 0 ? <p>{typewriter.visible}</p> : null}
    </motion.aside>
  );
}
