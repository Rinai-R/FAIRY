import { useCallback, useEffect, useRef, useState } from "react";

import { CharacterSpeechBubble } from "../components/CharacterSpeechBubble.jsx";
import { listenWailsHarnessEvents } from "../wailsClient.mjs";
import {
  expandCompanionForSpeech,
  restoreCompanionAfterSpeech,
} from "../desktopClient.js";
import {
  createSpeechObserver,
  reduceSpeechObserver,
  revealSpeechObserverThrough,
  speechBubbleVisible,
} from "../speechObserver.mjs";
import {
  SPEECH_BUBBLE_FADE_AFTER_MS,
  SPEECH_BUBBLE_POST_AUDIO_FADE_MS,
} from "../speechBubbleState.mjs";
import {
  advanceSpeechPlayback,
  createSpeechPlaybackState,
  currentSpeechSegment,
  playSpeechDataUrl,
  reduceSpeechPlayback,
  speechBubbleMayFade,
} from "../speechPlayback.mjs";

/**
 * Standalone transparent, click-through window that floats the character's
 * speech bubble above the companion window. It owns the bubble lifecycle:
 * it rebuilds the streamed reply from globally broadcast harness events and
 * asks the desktop service to show/hide (and position) its own window.
 */
export function SpeechBubbleApp() {
  const [observer, setObserver] = useState(createSpeechObserver);
  const [playback, setPlayback] = useState(createSpeechPlaybackState);
  const [dismissed, setDismissed] = useState(true);
  const shownRef = useRef(false);

  useEffect(() => {
    let cancelled = false;
    let unlisten = () => {};
    listenWailsHarnessEvents(
      (event) => {
        setObserver((prev) => {
          if (event.turnId !== prev.turnId) setDismissed(false);
          return reduceSpeechObserver(prev, event);
        });
        setPlayback((prev) => {
          const base = event.turnId !== prev.turnId ? createSpeechPlaybackState() : prev;
          return reduceSpeechPlayback(base, event);
        });
      },
      () => {},
    )
      .then((off) => {
        if (cancelled) {
          off();
          return;
        }
        unlisten = off;
      })
      .catch(() => {});
    return () => {
      cancelled = true;
      unlisten();
    };
  }, []);

  // Late TTS beats after a premature fade must bring the bubble back.
  useEffect(() => {
    if (playback.dataUrl) setDismissed(false);
  }, [playback.dataUrl]);

  const visible = !dismissed && speechBubbleVisible(observer);
  const mayFade = speechBubbleMayFade(playback);
  const fadeAfterMs = playback.played
    ? SPEECH_BUBBLE_POST_AUDIO_FADE_MS
    : SPEECH_BUBBLE_FADE_AFTER_MS;

  useEffect(() => {
    if (visible === shownRef.current) return;
    shownRef.current = visible;
    const operation = visible ? expandCompanionForSpeech : restoreCompanionAfterSpeech;
    operation().catch(() => {});
  }, [visible]);

  useEffect(() => {
    if (!playback.dataUrl || playback.played) return;
    let cancelled = false;
    const activeUrl = playback.dataUrl;
    const segment = currentSpeechSegment(playback);
    // Reveal the bubble for the reply chain this audio belongs to. Mid-ReAct
    // utterance audio (chainIndex < 0) is its own line and must not advance the
    // reply-chain reveal.
    const chainIndex = segment && Number.isSafeInteger(segment.chainIndex)
      ? segment.chainIndex
      : null;
    if (chainIndex !== null && chainIndex >= 0) {
      setObserver((prev) => revealSpeechObserverThrough(prev, chainIndex));
    }
    const playing = playSpeechDataUrl(activeUrl);
    playing
      .catch(() => {})
      .finally(() => {
        if (!cancelled) {
          setPlayback((prev) => (prev.dataUrl === activeUrl ? advanceSpeechPlayback(prev) : prev));
        }
      });
    return () => {
      cancelled = true;
      playing.stop?.();
    };
  }, [playback.dataUrl, playback.played, playback.playIndex]);

  // Without TTS, still reveal chains one beat at a time instead of dumping all text.
  useEffect(() => {
    if (playback.hold || playback.played) return undefined;
    if (!observer.chains.length) return undefined;
    if (observer.revealThrough >= observer.chains.length - 1) return undefined;
    const id = setTimeout(() => {
      setObserver((prev) => revealSpeechObserverThrough(prev, prev.revealThrough + 1));
    }, 1600);
    return () => clearTimeout(id);
  }, [
    playback.hold,
    playback.played,
    observer.chains.length,
    observer.revealThrough,
  ]);

  const handleFaded = useCallback(() => {
    setDismissed(true);
  }, []);

  if (!visible) return null;

  return (
    <div className="fairy-speech-surface">
      <CharacterSpeechBubble
        targetText={observer.draft}
        waiting={observer.waiting}
        mayFade={mayFade}
        fadeAfterMs={fadeAfterMs}
        onFaded={handleFaded}
      />
    </div>
  );
}
