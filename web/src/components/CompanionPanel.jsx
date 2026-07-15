import {
  ChatBubbleIcon,
  Cross2Icon,
  ExclamationTriangleIcon,
  GearIcon,
  MagicWandIcon,
  PaperPlaneIcon,
  StopIcon,
} from "@radix-ui/react-icons";
import {
  Button,
  Callout,
  Card,
  Flex,
  IconButton,
  Popover,
  Text,
  TextArea,
  Tooltip,
} from "@radix-ui/themes";
import { motion } from "motion/react";

import { PixelCharacter } from "./PixelCharacter.jsx";
import { Transcript } from "./Transcript.jsx";
import { resolveChatKeyboardAction } from "../companionViewState.mjs";
import { selectRecentTranscript } from "../windowState.mjs";

const DESKTOP_CHARACTER_DISPLAY_SCALE = 1.69;

export function CompanionPanel({
  characterName,
  character,
  visual,
  pixelCharacter,
  assetState,
  onAssetReady,
  onAssetError,
  onPetDragStart,
  onPetDragEnd,
  popoverMounted,
  chatVisualOpen,
  petVisualOpen,
  controlsDisabled,
  onOpenChat,
  onRequestCloseChat,
  onChatExitComplete,
  onPetExitComplete,
  onOpenControlPanel,
  companion,
  onDraftChange,
  onSubmit,
  onCancel,
  externalError = null,
}) {
  const ready = companion.conversationId !== null && character !== null && visual !== null;
  const active = companion.activeTurnId !== null || companion.submitting;
  const displayedError = companion.error ?? externalError;
  const accessibleCharacterName = characterName ?? "桌面角色";

  function handleOpenChange(open) {
    if (open) onOpenChat();
    else onRequestCloseChat();
  }

  function handleSubmit(event) {
    event.preventDefault();
    onSubmit();
  }

  function handleKeyDown(event) {
    const action = resolveChatKeyboardAction(event.key, event.shiftKey);
    if (action === "close") {
      event.preventDefault();
      event.stopPropagation();
      onRequestCloseChat();
      return;
    }
    if (action === "submit") {
      event.preventDefault();
      if (!active && companion.draft.trim().length > 0) onSubmit();
    }
  }

  return (
    <Popover.Root open={popoverMounted} onOpenChange={handleOpenChange}>
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
            data-tauri-drag-region
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
              <PixelCharacter
                visual={visual}
                visualState={pixelCharacter.visualState}
                direction={pixelCharacter.direction}
                displayScale={DESKTOP_CHARACTER_DISPLAY_SCALE}
                onReady={onAssetReady}
                onError={onAssetError}
              />
            </motion.div>
          </motion.div>
        ) : assetState.phase === "error" ? (
          <Callout.Root
            className="fairy-pet__asset-error"
            color="tomato"
            role="alert"
            data-tauri-drag-region
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

        <Popover.Trigger>
          <Button
            className="fairy-chat-trigger"
            type="button"
            size="2"
            variant="surface"
            disabled={controlsDisabled}
            aria-label={characterName ? `和${characterName}聊一会儿` : "打开角色设置"}
          >
            <ChatBubbleIcon />
            聊一会儿
          </Button>
        </Popover.Trigger>
      </motion.section>

      {popoverMounted ? (
        <Popover.Content
          className="fairy-popover-content"
          forceMount
          side="left"
          align="end"
          sideOffset={10}
          collisionPadding={14}
          onEscapeKeyDown={(event) => {
            event.preventDefault();
            onRequestCloseChat();
          }}
        >
              <motion.div
                key="chat-card"
                className="fairy-chat-motion"
                initial="hidden"
                animate={chatVisualOpen ? "visible" : "hidden"}
                variants={{
                  hidden: { opacity: 0, x: 10, y: 7, scale: 0.97 },
                  visible: { opacity: 1, x: 0, y: 0, scale: 1 },
                }}
                onAnimationComplete={(definition) => {
                  if (definition === "hidden") onChatExitComplete();
                }}
              >
                <Card className="fairy-chat-card" size="1">
                  <header className="fairy-chat-card__header">
                    <div>
                      <Flex align="center" gap="2" mb="1">
                        <span className={`fairy-presence-dot ${ready ? "is-ready" : ""}`} aria-hidden="true" />
                        <Text size="1" color="gray">{ready ? `${characterName}可以听见你` : "等待角色和模型就绪"}</Text>
                      </Flex>
                      <Text as="div" size="3" weight="medium">{characterName ? `和${characterName}说说话` : "角色对话"}</Text>
                    </div>
                    <Tooltip content="收起聊天">
                      <IconButton
                        type="button"
                        size="2"
                        variant="soft"
                        color="gray"
                        aria-label="收起聊天"
                        onClick={onRequestCloseChat}
                      >
                        <Cross2Icon />
                      </IconButton>
                    </Tooltip>
                  </header>

                  {ready ? (
                    <Transcript
                      characterName={characterName}
                      transcript={selectRecentTranscript(companion.transcript, 4)}
                      responseDraft={companion.responseDraft}
                      sessionState={companion.sessionState}
                    />
                  ) : (
                    <div className="fairy-chat-onboarding">
                      <MagicWandIcon aria-hidden="true" />
                      <Text as="p" size="2" weight="medium">还需要选择角色并配置模型连接。</Text>
                      <Button type="button" size="2" variant="soft" onClick={onOpenControlPanel}>
                        <GearIcon />
                        打开设置
                      </Button>
                    </div>
                  )}

                  {displayedError ? (
                    <Callout.Root className="fairy-chat-error" color="tomato" size="1" role="alert">
                      <Callout.Icon><ExclamationTriangleIcon /></Callout.Icon>
                      <Callout.Text>{displayedError.message}</Callout.Text>
                    </Callout.Root>
                  ) : null}

                  <form className="fairy-composer" onSubmit={handleSubmit}>
                    <TextArea
                      className="fairy-composer__input"
                      value={companion.draft}
                      onChange={(event) => onDraftChange(event.target.value)}
                      onKeyDown={handleKeyDown}
                      rows={2}
                      resize="none"
                      placeholder={characterName ? `想对${characterName}说什么？` : "选择角色后开始对话"}
                      aria-label="消息输入框"
                      autoFocus={ready}
                      disabled={!ready || active}
                    />
                    <Flex className="fairy-composer__actions" align="center" gap="2">
                      <Tooltip content="打开设置">
                        <IconButton
                          type="button"
                          size="2"
                          variant="ghost"
                          color="gray"
                          aria-label="打开设置"
                          onClick={onOpenControlPanel}
                        >
                          <GearIcon />
                        </IconButton>
                      </Tooltip>
                      <Text className="fairy-composer__hint" size="1" color="gray">Enter 发送 · Shift+Enter 换行</Text>
                      {active && companion.activeTurnId ? (
                        <Button type="button" size="2" color="tomato" variant="soft" onClick={onCancel}>
                          <StopIcon />
                          停止
                        </Button>
                      ) : (
                        <IconButton
                          type="submit"
                          size="2"
                          variant="solid"
                          aria-label="发送消息"
                          disabled={!ready || active || companion.draft.trim().length === 0}
                        >
                          <PaperPlaneIcon />
                        </IconButton>
                      )}
                    </Flex>
                  </form>
                </Card>
              </motion.div>
        </Popover.Content>
      ) : null}
    </Popover.Root>
  );
}
