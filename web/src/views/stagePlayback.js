export const NEXT_ACTION = Object.freeze({
  COMPLETE_TYPEWRITER: "complete-typewriter",
  NEXT_LINE: "next-line",
  ADVANCE_NODE: "advance-node",
  WAIT_NEXT_NODE: "wait-next-node",
  NONE: "none",
});

export function deriveStagePlaybackState(options = {}) {
  const {
    busy = false,
    hasNextNode = false,
    lineCount = 0,
    lineIndex = 0,
    stageWaiting = false,
    typewriterDone = true,
  } = options;

  const normalizedLineCount = Math.max(0, Number(lineCount) || 0);
  const normalizedLineIndex = clampLineIndex(lineIndex, normalizedLineCount);
  const hasDialogueLine = normalizedLineCount > 0;
  const canCompleteTypewriter = hasDialogueLine && !typewriterDone;
  const hasNextLocalLine = hasDialogueLine && normalizedLineIndex < normalizedLineCount - 1;
  const isWaitingForNextNode = Boolean(stageWaiting && hasNextNode && !canCompleteTypewriter && !hasNextLocalLine);

  const nextAction = resolveNextAction({
    busy,
    canCompleteTypewriter,
    hasNextLocalLine,
    hasNextNode,
    stageWaiting,
  });

  return {
    advanceDisabled: busy || nextAction === NEXT_ACTION.WAIT_NEXT_NODE || nextAction === NEXT_ACTION.NONE,
    advanceLabel: isWaitingForNextNode ? "准备中" : "下一句",
    canCompleteTypewriter,
    canRequestNextNode: Boolean(hasNextNode && !stageWaiting && !busy && !canCompleteTypewriter && !hasNextLocalLine),
    hasDialogueLine,
    hasNextLocalLine,
    isWaitingForNextNode,
    lineCount: normalizedLineCount,
    lineIndex: normalizedLineIndex,
    nextAction,
    shouldShowAdvance: canCompleteTypewriter || hasNextLocalLine || Boolean(hasNextNode),
  };
}

function resolveNextAction({ busy, canCompleteTypewriter, hasNextLocalLine, hasNextNode, stageWaiting }) {
  if (busy) return NEXT_ACTION.NONE;
  if (canCompleteTypewriter) return NEXT_ACTION.COMPLETE_TYPEWRITER;
  if (hasNextLocalLine) return NEXT_ACTION.NEXT_LINE;
  if (stageWaiting && hasNextNode) return NEXT_ACTION.WAIT_NEXT_NODE;
  if (hasNextNode) return NEXT_ACTION.ADVANCE_NODE;
  return NEXT_ACTION.NONE;
}

function clampLineIndex(value, lineCount) {
  const index = Math.max(0, Number(value) || 0);
  if (lineCount <= 0) return 0;
  return Math.min(index, lineCount - 1);
}
