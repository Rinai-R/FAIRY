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
  speechBubbleVisible,
} from "../speechObserver.mjs";
import {
  createSpeechPlaybackState,
  playSpeechDataUrl,
  reduceSpeechPlayback,
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
        setPlayback((prev) => reduceSpeechPlayback(prev, event));
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

  const visible = !dismissed && speechBubbleVisible(observer);

  useEffect(() => {
    if (visible === shownRef.current) return;
    shownRef.current = visible;
    const operation = visible ? expandCompanionForSpeech : restoreCompanionAfterSpeech;
    operation().catch(() => {});
  }, [visible]);

  useEffect(() => {
    if (!playback.dataUrl || playback.played) return;
    let cancelled = false;
    playSpeechDataUrl(playback.dataUrl)
      .catch(() => {})
      .finally(() => {
        if (!cancelled) setPlayback((prev) => prev.dataUrl === playback.dataUrl ? Object.freeze({ ...prev, played: true }) : prev);
      });
    return () => {
      cancelled = true;
    };
  }, [playback.dataUrl, playback.played]);

  const handleFaded = useCallback(() => {
    setDismissed(true);
  }, []);

  if (!visible) return null;

  return (
    <div className="fairy-speech-surface">
      <CharacterSpeechBubble
        targetText={observer.draft}
        waiting={observer.waiting}
        onFaded={handleFaded}
      />
    </div>
  );
}
