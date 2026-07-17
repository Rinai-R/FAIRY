import { ExternalLinkIcon, MagicWandIcon } from "@radix-ui/react-icons";
import { ScrollArea, Text } from "@radix-ui/themes";
import { useEffect, useLayoutEffect, useRef, useState } from "react";

import {
  createTypewriterState,
  holdBackMatchingAssistant,
  isTypewriterCaughtUp,
  setTypewriterTarget,
  tickTypewriter,
  typewriterPartialActive,
} from "../typewriter.mjs";

// ~50 码点/秒。
const TYPEWRITER_CHARS_PER_TICK = 1;
const TYPEWRITER_TICK_MS = 20;

export function Transcript({
  characterName,
  transcript,
  responseDraft,
  sessionState,
}) {
  const endRef = useRef(null);
  const [typewriter, setTypewriter] = useState(() => createTypewriterState());
  const draftActive = responseDraft.length > 0;
  const terminal = sessionState === "failed" || sessionState === "interrupted";
  const caughtUp = isTypewriterCaughtUp(typewriter);
  const showPartial = typewriterPartialActive(typewriter, draftActive);
  const displayTranscript = holdBackMatchingAssistant(
    transcript,
    typewriter.target,
    draftActive,
    caughtUp || typewriter.target.length === 0,
  );

  useLayoutEffect(() => {
    if (draftActive) {
      setTypewriter((prev) => setTypewriterTarget(prev, responseDraft));
      return;
    }
    if (terminal) {
      setTypewriter(createTypewriterState());
      return;
    }
    setTypewriter((prev) => {
      if (prev.target.length === 0) {
        return prev;
      }
      if (isTypewriterCaughtUp(prev)) {
        return createTypewriterState();
      }
      return prev;
    });
  }, [responseDraft, draftActive, terminal]);

  useEffect(() => {
    if (terminal) {
      return undefined;
    }
    if (caughtUp) {
      if (!draftActive && typewriter.target.length > 0) {
        setTypewriter(createTypewriterState());
      }
      return undefined;
    }
    if (typewriter.target.length === 0) {
      return undefined;
    }
    const id = setInterval(() => {
      setTypewriter((prev) => tickTypewriter(prev, TYPEWRITER_CHARS_PER_TICK));
    }, TYPEWRITER_TICK_MS);
    return () => clearInterval(id);
  }, [typewriter.target, typewriter.visible, caughtUp, draftActive, terminal]);

  useEffect(() => {
    endRef.current?.scrollIntoView({ block: "nearest" });
  }, [displayTranscript, typewriter.visible, showPartial, sessionState]);

  const waiting = !showPartial
    && responseDraft.length === 0
    && ["interpreting", "gathering", "planning", "responding"].includes(sessionState);

  return (
    <ScrollArea className="fairy-transcript" type="auto" scrollbars="vertical">
      <div role="log" aria-live="polite" aria-label="对话消息">
        {displayTranscript.length === 0 && !showPartial && !waiting ? (
          <div className="fairy-transcript__empty">
            <MagicWandIcon aria-hidden="true" />
            <Text as="p" size="2">说一句此刻最自然的话就好。</Text>
          </div>
        ) : null}

        {displayTranscript.map((message, index) => {
          const assistant = message.role === "assistant";
          return (
            <article
              className={`fairy-message fairy-message--${assistant ? "assistant" : "user"}`}
              aria-label={`${assistant ? characterName : "你"}说：${message.text}`}
              key={`${message.role}-${index}-${message.text}`}
            >
              <p>{message.text}</p>
              {assistant && message.sources?.length > 0 ? (
                <details className="fairy-message__sources">
                  <summary>{message.sources.length} 个来源</summary>
                  <ol>
                    {message.sources.map((source) => (
                      <li key={`${source.rank}-${source.url}`}>
                        <a
                          href={source.url}
                          target="_blank"
                          rel="noreferrer"
                          title={source.snippet}
                        >
                          <span>{source.title}</span>
                          <ExternalLinkIcon aria-hidden="true" />
                        </a>
                      </li>
                    ))}
                  </ol>
                </details>
              ) : null}
            </article>
          );
        })}

        {showPartial ? (
          <article
            className="fairy-message fairy-message--assistant fairy-message--partial"
            aria-label={`${characterName}的未完成回复：${typewriter.target}`}
            aria-busy={!caughtUp && !terminal}
          >
            <p>{typewriter.visible}</p>
            <Text as="span" size="1" color="gray">
              {terminal ? "回复未完成" : "正在回复"}
            </Text>
          </article>
        ) : null}

        {waiting ? (
          <div className="fairy-typing" aria-label={`${characterName}正在想`} aria-busy="true">
            <span /><span /><span />
          </div>
        ) : null}
        <div ref={endRef} />
      </div>
    </ScrollArea>
  );
}
