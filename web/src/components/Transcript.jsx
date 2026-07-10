import { MagicWandIcon } from "@radix-ui/react-icons";
import { ScrollArea, Text } from "@radix-ui/themes";
import { useEffect, useRef } from "react";

export function Transcript({
  characterName,
  transcript,
  responseDraft,
  sessionState,
}) {
  const endRef = useRef(null);

  useEffect(() => {
    endRef.current?.scrollIntoView({ block: "nearest" });
  }, [transcript, responseDraft, sessionState]);

  const waiting = responseDraft.length === 0
    && ["interpreting", "planning", "responding"].includes(sessionState);

  return (
    <ScrollArea className="fairy-transcript" type="auto" scrollbars="vertical">
      <div role="log" aria-live="polite" aria-label="对话消息">
        {transcript.length === 0 && responseDraft.length === 0 && !waiting ? (
          <div className="fairy-transcript__empty">
            <MagicWandIcon aria-hidden="true" />
            <Text as="p" size="2">说一句此刻最自然的话就好。</Text>
          </div>
        ) : null}

        {transcript.map((message, index) => {
          const assistant = message.role === "assistant";
          return (
            <article
              className={`fairy-message fairy-message--${assistant ? "assistant" : "user"}`}
              aria-label={`${assistant ? characterName : "你"}说：${message.text}`}
              key={`${message.role}-${index}-${message.text}`}
            >
              <p>{message.text}</p>
            </article>
          );
        })}

        {responseDraft.length > 0 ? (
          <article
            className="fairy-message fairy-message--assistant fairy-message--partial"
            aria-label={`${characterName}的未完成回复：${responseDraft}`}
          >
            <p>{responseDraft}</p>
            <Text as="span" size="1" color="gray">
              {sessionState === "failed" || sessionState === "interrupted"
                ? "回复未完成"
                : "正在回复"}
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
