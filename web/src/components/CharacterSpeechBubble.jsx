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
} from "../speechBubbleState.mjs";

const TYPEWRITER_CHARS_PER_TICK = 1;
const TYPEWRITER_TICK_MS = 20;

/**
 * Assistant-only light speech bubble beside the character.
 * Waiting state stays until reply text arrives; then typewriter + dwell fade.
 */
export function CharacterSpeechBubble({
  targetText,
  waiting,
  characterName,
  onFaded,
}) {
  const bubbleRef = useRef(null);
  const [typewriter, setTypewriter] = useState(() => createTypewriterState());
  const [bubble, setBubble] = useState(() => createSpeechBubbleState());
  const hasTarget = typeof targetText === "string" && targetText.length > 0;
  const caughtUp = isTypewriterCaughtUp(typewriter)
    && typewriter.target.length > 0
    && typewriter.target === bubble.target;
  const visible = hasTarget || waiting;

  useLayoutEffect(() => {
    if (!hasTarget) {
      // Keep mounted waiting chrome; only clear typed target.
      if (!waiting) {
        setBubble(createSpeechBubbleState());
        setTypewriter(createTypewriterState());
      }
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
    if (!hasTarget || waiting || bubble.fading || !caughtUp) return undefined;
    const dwell = setTimeout(() => {
      setBubble((prev) => reduceSpeechBubbleState(prev, { type: "start_fade", at: Date.now() }));
    }, SPEECH_BUBBLE_FADE_AFTER_MS);
    return () => clearTimeout(dwell);
  }, [hasTarget, waiting, bubble.fading, caughtUp]);

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
      className={`fairy-speech-bubble${bubble.fading ? " is-fading" : ""}${waiting && !hasTarget ? " is-waiting" : ""}`}
      aria-label={characterName ? `${characterName}说` : "角色回复"}
      aria-busy={waiting && !hasTarget}
      initial={false}
      animate={{ opacity: bubble.fading ? 0 : 1, y: 0, scale: 1 }}
      transition={{ duration: bubble.fading ? 0.45 : 0.18 }}
    >
      {waiting && !hasTarget ? (
        <div className="fairy-speech-bubble__waiting" aria-hidden="true">
          <span /><span /><span />
        </div>
      ) : null}
      {typewriter.visible.length > 0 ? <p>{typewriter.visible}</p> : null}
    </motion.aside>
  );
}
