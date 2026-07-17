import {
  ClockIcon,
  Cross2Icon,
  ExclamationTriangleIcon,
  GearIcon,
  MagicWandIcon,
  PaperPlaneIcon,
  StopIcon,
} from "@radix-ui/react-icons";
import {
  Callout,
  Card,
  Flex,
  IconButton,
  Text,
  TextArea,
} from "@radix-ui/themes";
import { motion } from "motion/react";
import { useEffect, useLayoutEffect, useReducer, useRef } from "react";

import { PixelCharacter } from "./PixelCharacter.jsx";
import { Transcript } from "./Transcript.jsx";
import {
  resolvePixelCharacterRenderKey,
  resolveChatKeyboardAction,
} from "../companionViewState.mjs";
import {
  createFootComposerUiState,
  isFootComposerVisible,
  reduceFootComposerUiState,
} from "../footComposerState.mjs";

const DESKTOP_CHARACTER_DISPLAY_SCALE = 1.69;
const FOOT_INPUT_MAX_HEIGHT = 96;
const BUSY_SESSION_STATES = new Set([
  "interpreting",
  "gathering",
  "planning",
  "responding",
]);

function FootDock({
  className = "",
  visible = true,
  ready,
  active,
  characterName,
  companion,
  controlsDisabled,
  showClock = true,
  historyOpen = false,
  onPointerEnter,
  onPointerLeave,
  onDraftChange,
  onKeyDown,
  onFocus,
  onBlur,
  onSubmit,
  onCancel,
  onOpenControlPanel,
  onToggleHistory,
}) {
  const formRef = useRef(null);

  useLayoutEffect(() => {
    if (!ready) return;
    const el = formRef.current?.querySelector("textarea");
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, FOOT_INPUT_MAX_HEIGHT)}px`;
  }, [companion.draft, ready]);

  return (
    <div
      className={`fairy-foot-dock${visible ? " is-visible" : ""}${className ? ` ${className}` : ""}`}
      onPointerEnter={onPointerEnter}
      onPointerLeave={onPointerLeave}
    >
      <div className="fairy-foot-dock__shell">
        <div className="fairy-foot-dock__tools">
          <IconButton
            type="button"
            size="1"
            variant="ghost"
            color="gray"
            className="fairy-foot-dock__btn"
            aria-label="打开设置"
            disabled={controlsDisabled}
            onClick={onOpenControlPanel}
          >
            <GearIcon />
          </IconButton>

          {showClock ? (
            <IconButton
              type="button"
              size="1"
              variant={historyOpen ? "soft" : "ghost"}
              color="gray"
              className="fairy-foot-dock__btn"
              aria-label={historyOpen ? "关闭历史聊天" : "打开历史聊天"}
              aria-pressed={historyOpen}
              disabled={controlsDisabled || !ready}
              onClick={onToggleHistory}
            >
              <ClockIcon />
            </IconButton>
          ) : null}
        </div>

        <form className="fairy-foot-dock__form" ref={formRef} onSubmit={onSubmit}>
          {ready ? (
            <TextArea
              className="fairy-foot-dock__input"
              value={companion.draft}
              onChange={(event) => onDraftChange(event.target.value)}
              onKeyDown={onKeyDown}
              onFocus={onFocus}
              onBlur={onBlur}
              rows={1}
              resize="none"
              placeholder={characterName ? `对 ${characterName} 说…` : "选择角色后开始对话"}
              aria-label="快捷消息输入"
              disabled={!ready || active}
            />
          ) : (
            <button
              type="button"
              className="fairy-foot-dock__setup"
              onClick={onOpenControlPanel}
            >
              <MagicWandIcon aria-hidden="true" />
              <span>先配置角色与模型</span>
            </button>
          )}
          {ready && active && companion.activeTurnId ? (
            <IconButton
              type="button"
              size="1"
              color="tomato"
              variant="soft"
              className="fairy-foot-dock__btn"
              aria-label="停止回复"
              onClick={onCancel}
            >
              <StopIcon />
            </IconButton>
          ) : (
            <IconButton
              type="submit"
              size="1"
              variant="solid"
              className="fairy-foot-dock__send"
              aria-label="发送消息"
              disabled={!ready || active || companion.draft.trim().length === 0}
            >
              <PaperPlaneIcon />
            </IconButton>
          )}
        </form>
      </div>
    </div>
  );
}

export function CompanionPanel({
  characterName,
  character,
  visual,
  pixelCharacter,
  assetState,
  pixelSurfaceEpoch = 0,
  onAssetReady,
  onAssetError,
  onPetDragStart,
  onPetDragEnd,
  petDragging = false,
  historyMounted,
  historyVisualOpen,
  petVisualOpen,
  controlsDisabled,
  onOpenHistory,
  onRequestCloseHistory,
  onHistoryExitComplete,
  onPetExitComplete,
  onOpenControlPanel,
  companion,
  onDraftChange,
  onSubmit,
  onCancel,
  externalError = null,
}) {
  const ready = companion.conversationId !== null && character !== null && visual !== null;
  const turnBusy = companion.submitting
    || companion.activeTurnId !== null
    || BUSY_SESSION_STATES.has(companion.sessionState);
  const displayedError = companion.error ?? externalError;
  const accessibleCharacterName = characterName ?? "桌面角色";
  const pixelCharacterRenderKey = resolvePixelCharacterRenderKey(character, visual);
  const pixelMountKey = pixelCharacterRenderKey === null
    ? null
    : `${pixelCharacterRenderKey}:${pixelSurfaceEpoch}`;

  const [footUi, dispatchFootUi] = useReducer(
    reduceFootComposerUiState,
    undefined,
    createFootComposerUiState,
  );
  const footVisible = isFootComposerVisible(footUi);

  useEffect(() => {
    if (petDragging) dispatchFootUi({ type: "drag_start" });
    else dispatchFootUi({ type: "drag_end" });
  }, [petDragging]);

  function handleSubmit(event) {
    event.preventDefault();
    onSubmit();
  }

  function handleKeyDown(event) {
    const action = resolveChatKeyboardAction(event.key, event.shiftKey);
    if (action === "close") {
      event.preventDefault();
      event.stopPropagation();
      if (historyMounted) {
        onRequestCloseHistory();
        return;
      }
      event.currentTarget.blur();
      dispatchFootUi({ type: "blur" });
      return;
    }
    if (action === "submit") {
      event.preventDefault();
      if (!turnBusy && companion.draft.trim().length > 0) onSubmit();
    }
  }

  const dockProps = {
    ready,
    active: turnBusy,
    characterName,
    companion,
    controlsDisabled,
    onDraftChange,
    onKeyDown: handleKeyDown,
    onFocus: () => dispatchFootUi({ type: "focus" }),
    onBlur: () => dispatchFootUi({ type: "blur" }),
    onSubmit: handleSubmit,
    onCancel,
    onOpenControlPanel,
    historyOpen: historyMounted,
    onToggleHistory: historyMounted ? onRequestCloseHistory : onOpenHistory,
  };

  return (
    <>
      <motion.section
        className="fairy-pet"
        aria-label={`${accessibleCharacterName} 桌面角色`}
        initial={false}
        animate={petVisualOpen ? "visible" : "hidden"}
        variants={{
          hidden: { opacity: 0, y: 18, scale: 0.94 },
          visible: { opacity: 1, y: 0, scale: 1 },
        }}
        onAnimationComplete={(definition) => {
          if (definition === "hidden") onPetExitComplete();
        }}
      >
        {visual !== null && assetState.phase !== "error" ? (
          <motion.div
            className="fairy-pet__character"
            data-fairy-pet-drag-region
            aria-label={`拖动${accessibleCharacterName}`}
            initial={false}
            animate={{ y: 0, scale: 1 }}
            style={{ opacity: assetState.phase === "ready" ? 1 : 0 }}
            onPointerDown={onPetDragStart}
            onPointerUp={onPetDragEnd}
            onPointerCancel={onPetDragEnd}
          >
            <motion.div
              className="fairy-pet__pixel-motion"
              animate={{ x: pixelCharacter.displacement }}
              transition={{ duration: 0.9, ease: [0.22, 1, 0.36, 1] }}
              aria-hidden="true"
            >
              {pixelMountKey !== null ? (
                <PixelCharacter
                  key={pixelMountKey}
                  visual={visual}
                  visualState={pixelCharacter.visualState}
                  direction={pixelCharacter.direction}
                  displayScale={DESKTOP_CHARACTER_DISPLAY_SCALE}
                  onReady={onAssetReady}
                  onError={onAssetError}
                />
              ) : null}
            </motion.div>
          </motion.div>
        ) : null}

        {assetState.phase === "error" ? (
          <Callout.Root
            className="fairy-pet__asset-error"
            color="tomato"
            size="1"
            role="alert"
            data-fairy-pet-drag-region
            aria-label={`拖动${accessibleCharacterName}`}
            onPointerDown={onPetDragStart}
            onPointerUp={onPetDragEnd}
            onPointerCancel={onPetDragEnd}
          >
            <Callout.Icon><ExclamationTriangleIcon /></Callout.Icon>
            <Callout.Text>
              {assetState.error.code} · {assetState.error.message}
            </Callout.Text>
          </Callout.Root>
        ) : null}

        <FootDock
          {...dockProps}
          visible={footVisible || historyMounted}
          showClock
          onPointerEnter={() => dispatchFootUi({ type: "pointer_enter" })}
          onPointerLeave={() => dispatchFootUi({ type: "pointer_leave" })}
        />

        {displayedError && !historyMounted ? (
          <Callout.Root className="fairy-foot-error" color="tomato" size="1" role="alert">
            <Callout.Icon><ExclamationTriangleIcon /></Callout.Icon>
            <Callout.Text>{displayedError.message}</Callout.Text>
          </Callout.Root>
        ) : null}
      </motion.section>

      {historyMounted ? (
        <motion.div
          className="fairy-history-layer"
          initial={false}
          animate={historyVisualOpen ? "visible" : "hidden"}
          variants={{
            hidden: { opacity: 0, x: 8, y: 6, scale: 0.98 },
            visible: { opacity: 1, x: 0, y: 0, scale: 1 },
          }}
          onAnimationComplete={(definition) => {
            if (definition === "hidden") onHistoryExitComplete();
          }}
        >
          <Card className="fairy-history-card" size="1">
            <Flex className="fairy-history-card__bar" align="center" justify="between">
              <Text size="2" weight="medium">历史</Text>
              <IconButton
                type="button"
                size="1"
                variant="ghost"
                color="gray"
                aria-label="关闭历史"
                onClick={onRequestCloseHistory}
              >
                <Cross2Icon />
              </IconButton>
            </Flex>
            {displayedError ? (
              <Callout.Root className="fairy-chat-error" color="tomato" size="1" role="alert">
                <Callout.Icon><ExclamationTriangleIcon /></Callout.Icon>
                <Callout.Text>{displayedError.message}</Callout.Text>
              </Callout.Root>
            ) : null}
            <Transcript
              characterName={characterName}
              transcript={companion.transcript}
              responseDraft=""
              sessionState="idle"
              variant="history"
            />
          </Card>
        </motion.div>
      ) : null}
    </>
  );
}
