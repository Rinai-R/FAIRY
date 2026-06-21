export const NEXT_ACTION = Object.freeze({
  COMPLETE_TYPEWRITER: "complete-typewriter",
  NEXT_LINE: "next-line",
  ADVANCE_NODE: "advance-node",
  WAIT_NEXT_NODE: "wait-next-node",
  CHOICE_PENDING: "choice-pending",
  NONE: "none",
});

export function deriveStagePlaybackState(options = {}) {
  const {
    busy = false,
    hasChoices = false,
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
  const atNodeBoundary = !canCompleteTypewriter && !hasNextLocalLine;
  const isWaitingForNextNode = Boolean(stageWaiting && (hasNextNode || hasChoices) && atNodeBoundary);
  const isChoicePending = Boolean(hasChoices && !stageWaiting && atNodeBoundary);

  const nextAction = resolveNextAction({
    busy,
    canCompleteTypewriter,
    hasChoices,
    hasNextLocalLine,
    hasNextNode,
    stageWaiting,
  });

  return {
    advanceDisabled: busy || nextAction === NEXT_ACTION.WAIT_NEXT_NODE || nextAction === NEXT_ACTION.CHOICE_PENDING || nextAction === NEXT_ACTION.NONE,
    advanceLabel: isWaitingForNextNode ? "准备中" : isChoicePending ? "请选择" : "下一句",
    canCompleteTypewriter,
    canRequestNextNode: Boolean(hasNextNode && !hasChoices && !stageWaiting && !busy && atNodeBoundary),
    hasChoices: Boolean(hasChoices),
    hasDialogueLine,
    hasNextLocalLine,
    isChoicePending,
    isWaitingForNextNode,
    lineCount: normalizedLineCount,
    lineIndex: normalizedLineIndex,
    nextAction,
    shouldShowAdvance: canCompleteTypewriter || hasNextLocalLine || isWaitingForNextNode || Boolean(hasNextNode && !hasChoices),
  };
}

function resolveNextAction({ busy, canCompleteTypewriter, hasChoices, hasNextLocalLine, hasNextNode, stageWaiting }) {
  if (busy) return NEXT_ACTION.NONE;
  if (canCompleteTypewriter) return NEXT_ACTION.COMPLETE_TYPEWRITER;
  if (hasNextLocalLine) return NEXT_ACTION.NEXT_LINE;
  if (stageWaiting && (hasNextNode || hasChoices)) return NEXT_ACTION.WAIT_NEXT_NODE;
  if (hasChoices) return NEXT_ACTION.CHOICE_PENDING;
  if (hasNextNode) return NEXT_ACTION.ADVANCE_NODE;
  return NEXT_ACTION.NONE;
}

function clampLineIndex(value, lineCount) {
  const index = Math.max(0, Number(value) || 0);
  if (lineCount <= 0) return 0;
  return Math.min(index, lineCount - 1);
}
