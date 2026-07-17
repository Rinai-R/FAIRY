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

/**
 * Standalone transparent, click-through window that floats the character's
 * speech bubble above the companion window. It owns the bubble lifecycle:
 * it rebuilds the streamed reply from globally broadcast harness events and
 * asks the desktop service to show/hide (and position) its own window.
 */
export function SpeechBubbleApp() {
  const [observer, setObserver] = useState(createSpeechObserver);
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
