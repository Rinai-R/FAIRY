export function expressionPartFromTurn(turn) {
  if (turn?.type !== "beat.ready" || !turn.beat) return null;
  const beat = turn.beat;
  const identity = `${turn.turnId || ""}:${beat.beatId || ""}`;
  if (beat.part?.kind === "utterance") {
    const text = typeof beat.part.text === "string" ? beat.part.text.trim() : "";
    if (!text) return null;
    return { key: identity, kind: "utterance", text };
  }
  if (beat.part?.kind === "sticker") {
    const controlledURL = typeof beat.stickerUrl === "string"
      && beat.stickerUrl.startsWith("/characters/stickers/")
      ? beat.stickerUrl
      : "";
    return {
      key: identity,
      kind: "sticker",
      turnId: turn.turnId || "",
      beatId: beat.beatId || "",
      description: beat.part.sticker?.description || "表情包",
      url: controlledURL,
      unavailable: beat.stickerUnavailable === true || !controlledURL,
      error: beat.stickerError || (!controlledURL ? "表情包不可用" : ""),
    };
  }
  const legacyText = typeof beat.displayText === "string" ? beat.displayText.trim() : "";
  if (!legacyText) return null;
  return { key: identity, kind: "utterance", text: legacyText };
}

export function appendExpressionPart(parts, next) {
  if (!next) return Array.isArray(parts) ? parts : [];
  const current = Array.isArray(parts) ? parts : [];
  if (next.key && current.some((part) => part.key === next.key)) return current;
  return [...current, next];
}

export function markStickerUnavailable(parts, key, error = "表情包不可用") {
  return (Array.isArray(parts) ? parts : []).map((part) => (
    part.key === key && part.kind === "sticker"
      ? { ...part, unavailable: true, error, url: "" }
      : part
  ));
}

export function historyExpressionParts(message) {
  const messageID = typeof message?.id === "string" ? message.id : "message";
  const source = Array.isArray(message?.parts) ? message.parts : [];
  const parts = source.flatMap((part, index) => {
    const key = `${messageID}:${index}`;
    if (part?.kind === "utterance") {
      const text = typeof part.text === "string" ? part.text.trim() : "";
      return text ? [{ key, kind: "utterance", text }] : [];
    }
    if (part?.kind === "sticker") {
      const description = typeof part.sticker?.description === "string"
        ? part.sticker.description.trim()
        : "";
      return [{ key, kind: "sticker", description: description || "表情包" }];
    }
    return [];
  });
  if (parts.length > 0) return parts;

  const legacyText = typeof message?.content === "string" ? message.content.trim() : "";
  return legacyText
    ? [{ key: `${messageID}:legacy`, kind: "utterance", text: legacyText }]
    : [];
}
