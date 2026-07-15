import { useCallback, useEffect, useState } from "react";

import {
  CheckCircledIcon,
  Cross2Icon,
  DesktopIcon,
  ExclamationTriangleIcon,
  IdCardIcon,
  Link2Icon,
  LockClosedIcon,
  MagicWandIcon,
  MagnifyingGlassIcon,
  PersonIcon,
  PlusIcon,
  ResetIcon,
  TrashIcon,
} from "@radix-ui/react-icons";
import {
  Badge,
  Button,
  Callout,
  Card,
  Flex,
  Heading,
  IconButton,
  ScrollArea,
  SegmentedControl,
  Select,
  Separator,
  Switch,
  Tabs,
  Text,
  TextArea,
  TextField,
  Tooltip,
} from "@radix-ui/themes";
import { AnimatePresence, motion } from "motion/react";

import {
  activateCharacter,
  assignLegacyRelationship,
  clearModelConnection,
  clearUserProfile,
  createCharacter,
  createPersonalMemory,
  confirmKnowledgeCandidate,
  getModelConnectionStatus,
  getIntelligenceStatus,
  getExtractionBatchCatalog,
  getKnowledgeCatalog,
  getPersonalMemoryCatalog,
  getUserProfile,
  listCharacters,
  listVisualPacks,
  saveModelConnection,
  retryExtractionBatch,
  revisePersonalMemory,
  setUserProfile,
  setCharacterAppearance,
  tombstoneKnowledge,
  tombstonePersonalMemory,
  updateCharacter,
} from "../companionClient.mjs";
import { normalizeCompanionError } from "../companionState.mjs";
import {
  CONTROL_PANEL_SECTIONS,
  DEFAULT_MODEL_CONTEXT_WINDOW_TOKENS,
  MAX_MODEL_CONTEXT_WINDOW_TOKENS,
  MIN_MODEL_CONTEXT_WINDOW_TOKENS,
  MODEL_PROTOCOL_OPTIONS,
  assertControlPanelSection,
  buildModelConnectionInput,
  buildCharacterSaveInput,
  selectedAppearancePackId,
} from "../controlPanelState.mjs";
import {
  getDesktopState,
  hideCompanion,
  listenToDesktopState,
  restoreCompanionAfterControlPanel,
  restoreCompanionInteraction,
  setAlwaysOnTop,
  setClickThrough,
  showCompanion,
} from "../desktopClient.js";
import { normalizeInvokeError } from "../desktopState.mjs";
import { listenToConfigurationChanges } from "../windowClient.js";

const EMPTY_CATALOG = Object.freeze({
  characters: Object.freeze([]),
  active: null,
  diagnostics: Object.freeze([]),
});
const EMPTY_VISUAL_CATALOG = Object.freeze({ visualPacks: Object.freeze([]) });

const SECTION_ICONS = Object.freeze({
  character: PersonIcon,
  profile: IdCardIcon,
  model: Link2Icon,
  intelligence: MagnifyingGlassIcon,
  desktop: DesktopIcon,
});

function Field({ id, label, hint, children }) {
  return (
    <Flex className="cp-field" direction="column" gap="1">
      <Text as="label" htmlFor={id} size="2" weight="medium">{label}</Text>
      {children}
      {hint ? <Text as="p" size="1" color="gray">{hint}</Text> : null}
    </Flex>
  );
}

function PageHeader({ title, description, status, ready = false }) {
  return (
    <Flex className="cp-page-header" align="start" justify="between" gap="3">
      <div>
        <Heading as="h2" size="4" weight="medium">{title}</Heading>
        <Text as="p" size="2" color="gray">{description}</Text>
      </div>
      <Badge color={ready ? "teal" : "gray"} variant="soft" radius="full">{status}</Badge>
    </Flex>
  );
}

function KnowledgeEntry({ record, disabled, onConfirm, onDelete }) {
  const candidate = record.status === "candidate";
  const basis = record.verificationBasis === "web_source"
    ? `网页来源 · ${record.sources.length}`
    : record.verificationBasis === "user_confirmed"
      ? "由你确认"
      : "等待确认";
  return (
    <article className={`cp-knowledge-entry ${candidate ? "is-candidate" : "is-verified"}`}>
      <Flex align="center" justify="between" gap="2">
        <Text size="1" weight="medium" className="cp-knowledge-topic">{record.topic}</Text>
        <Badge size="1" color={candidate ? "amber" : "sky"} variant="soft">{basis}</Badge>
      </Flex>
      <Text as="p" size="2" className="cp-knowledge-statement">{record.statement}</Text>
      <Flex className="cp-knowledge-actions" align="center" justify="end" gap="2">
        {candidate ? (
          <Button size="1" variant="soft" type="button" disabled={disabled} onClick={() => onConfirm(record.id)}>
            <CheckCircledIcon />确认可用
          </Button>
        ) : null}
        <Tooltip content="从可用知识中移除并保留审计记录">
          <IconButton size="1" color="tomato" variant="ghost" type="button" disabled={disabled} onClick={() => onDelete(record.id)} aria-label={`移除知识：${record.topic}`}>
            <TrashIcon />
          </IconButton>
        </Tooltip>
      </Flex>
    </article>
  );
}

function KnowledgeColumn({ title, description, records, empty, disabled, onConfirm, onDelete }) {
  return (
    <section className="cp-knowledge-column">
      <Flex className="cp-knowledge-column-header" align="start" justify="between" gap="3">
        <div>
          <Text as="h3" size="2" weight="medium">{title}</Text>
          <Text as="p" size="1" color="gray">{description}</Text>
        </div>
        <Badge size="1" color="gray" variant="outline">{records.length}</Badge>
      </Flex>
      {records.length === 0 ? (
        <Text as="p" className="cp-knowledge-empty" size="1" color="gray">{empty}</Text>
      ) : records.map((record) => (
        <KnowledgeEntry
          key={record.id}
          record={record}
          disabled={disabled}
          onConfirm={onConfirm}
          onDelete={onDelete}
        />
      ))}
    </section>
  );
}

function MemoryEntry({ record, disabled, onRevise, onDelete, onAssign }) {
  const [editing, setEditing] = useState(false);
  const [content, setContent] = useState(record.content);
  return (
    <article className="cp-memory-entry">
      {editing ? (
        <TextArea value={content} onChange={(event) => setContent(event.target.value)} resize="vertical" />
      ) : (
        <Text as="p" size="2">{record.content}</Text>
      )}
      <Flex align="center" justify="between" gap="2" wrap="wrap">
        <Badge size="1" color="sky" variant="soft">{record.kind}</Badge>
        <Flex gap="2">
          {onAssign ? (
            <Button size="1" variant="soft" disabled={disabled} onClick={() => onAssign(record.id)}>分配给当前角色</Button>
          ) : editing ? (
            <>
              <Button size="1" variant="ghost" color="gray" onClick={() => { setEditing(false); setContent(record.content); }}>取消</Button>
              <Button size="1" disabled={disabled || !content.trim()} onClick={() => { onRevise(record.id, content.trim()); setEditing(false); }}>保存</Button>
            </>
          ) : (
            <Button size="1" variant="ghost" disabled={disabled} onClick={() => setEditing(true)}>修正</Button>
          )}
          <IconButton size="1" color="tomato" variant="ghost" disabled={disabled} onClick={() => onDelete(record.id)} aria-label="删除记忆">
            <TrashIcon />
          </IconButton>
        </Flex>
      </Flex>
    </article>
  );
}

function MemoryColumn({ title, description, records, empty, disabled, onRevise, onDelete, onAssign }) {
  return (
    <section className="cp-memory-column">
      <Flex align="start" justify="between" gap="3">
        <div><Text as="h3" size="2" weight="medium">{title}</Text><Text as="p" size="1" color="gray">{description}</Text></div>
        <Badge size="1" color="gray" variant="outline">{records.length}</Badge>
      </Flex>
      {records.length === 0 ? <Text as="p" size="1" color="gray" className="cp-memory-empty">{empty}</Text> : records.map((record) => (
        <MemoryEntry key={record.id} record={record} disabled={disabled} onRevise={onRevise} onDelete={onDelete} onAssign={onAssign} />
      ))}
    </section>
  );
}

export function ControlPanelApp() {
  const [section, setSection] = useState("model");
  const [catalog, setCatalog] = useState(EMPTY_CATALOG);
  const [visualCatalog, setVisualCatalog] = useState(EMPTY_VISUAL_CATALOG);
  const [profile, setProfile] = useState(null);
  const [modelStatus, setModelStatus] = useState(null);
  const [intelligenceStatus, setIntelligenceStatus] = useState(null);
  const [knowledgeCatalog, setKnowledgeCatalog] = useState(null);
  const [memoryCatalog, setMemoryCatalog] = useState(null);
  const [batchCatalog, setBatchCatalog] = useState(null);
  const [knowledgeCatalogLoading, setKnowledgeCatalogLoading] = useState(false);
  const [desktop, setDesktop] = useState(null);
  const [pending, setPending] = useState(null);
  const [error, setError] = useState(null);
  const [panelVisualOpen, setPanelVisualOpen] = useState(false);
  const [closeRequested, setCloseRequested] = useState(false);

  const [characterId, setCharacterId] = useState(null);
  const [characterName, setCharacterName] = useState("");
  const [characterDescription, setCharacterDescription] = useState("");
  const [characterDialogueStyle, setCharacterDialogueStyle] = useState("");
  const [visualPackId, setVisualPackId] = useState("");
  const [preferredName, setPreferredName] = useState("");
  const [protocol, setProtocol] = useState("chat_completions");
  const [endpoint, setEndpoint] = useState("https://api.deepseek.com");
  const [model, setModel] = useState("deepseek-v4-flash");
  const [contextWindowTokens, setContextWindowTokens] = useState(String(DEFAULT_MODEL_CONTEXT_WINDOW_TOKENS));
  const [authMode, setAuthMode] = useState("bearer_key");
  const [apiKey, setApiKey] = useState("");
  const [memoryKind, setMemoryKind] = useState("preference");
  const [memoryContent, setMemoryContent] = useState("");

  const refresh = useCallback(async (hydrateForms = false) => {
    const [
      nextCatalog,
      nextVisualCatalog,
      nextProfile,
      nextModelStatus,
      nextIntelligenceStatus,
      nextDesktop,
    ] = await Promise.all([
      listCharacters(),
      listVisualPacks(),
      getUserProfile(),
      getModelConnectionStatus(),
      getIntelligenceStatus(),
      getDesktopState(),
    ]);
    setCatalog(nextCatalog);
    setVisualCatalog(nextVisualCatalog);
    setProfile(nextProfile);
    setModelStatus(nextModelStatus);
    setIntelligenceStatus(nextIntelligenceStatus);
    setDesktop(nextDesktop);
    if (hydrateForms) {
      if (nextCatalog.active) {
        setCharacterId(nextCatalog.active.characterId);
        setCharacterName(nextCatalog.active.name);
        setCharacterDescription(nextCatalog.active.description);
        setCharacterDialogueStyle(nextCatalog.active.dialogueStyle ?? "");
        setVisualPackId(selectedAppearancePackId(nextCatalog.active));
      }
      setPreferredName(nextProfile?.preferredName ?? "");
      if (nextModelStatus.config) {
        setProtocol(nextModelStatus.config.protocol);
        setEndpoint(nextModelStatus.config.endpoint);
        setModel(nextModelStatus.config.model);
        setContextWindowTokens(String(nextModelStatus.config.contextWindowTokens));
        setAuthMode(nextModelStatus.config.authMode);
      }
    }
  }, []);

  const selectedVisual = visualCatalog.visualPacks.find(
    (visual) => visual.packId === visualPackId,
  ) ?? null;

  const refreshIntelligence = useCallback(async () => {
    setKnowledgeCatalogLoading(true);
    try {
      const nextIntelligenceStatus = await getIntelligenceStatus();
      setIntelligenceStatus(nextIntelligenceStatus);
      if (!nextIntelligenceStatus.ready) {
        setKnowledgeCatalog(null);
        setMemoryCatalog(null);
        setBatchCatalog(null);
        return;
      }
      try {
        const activeCharacterId = catalog.active?.characterId ?? null;
        const [nextKnowledge, nextMemories, nextBatches] = await Promise.all([
          getKnowledgeCatalog(),
          activeCharacterId ? getPersonalMemoryCatalog(activeCharacterId) : Promise.resolve(null),
          activeCharacterId ? getExtractionBatchCatalog(activeCharacterId) : Promise.resolve(null),
        ]);
        setKnowledgeCatalog(nextKnowledge);
        setMemoryCatalog(nextMemories);
        setBatchCatalog(nextBatches);
      } catch (reason) {
        setKnowledgeCatalog(null);
        setMemoryCatalog(null);
        setBatchCatalog(null);
        throw reason;
      }
    } finally {
      setKnowledgeCatalogLoading(false);
    }
  }, [catalog.active?.characterId]);

  useEffect(() => {
    let cancelled = false;
    let stopConfiguration = null;
    let stopDesktop = null;
    refresh(true).catch((reason) => {
      if (!cancelled) setError(normalizeCompanionError(reason));
    });
    listenToConfigurationChanges(
      () => void refresh(false).catch((reason) => setError(normalizeCompanionError(reason))),
      () => setError({ code: "INVALID_CONFIGURATION_EVENT", message: "收到无法识别的配置通知。", retryable: false }),
    ).then((unlisten) => {
      if (cancelled) unlisten(); else stopConfiguration = unlisten;
    }).catch((reason) => {
      if (!cancelled) setError(normalizeCompanionError(reason));
    });
    listenToDesktopState(
      (state) => { if (!cancelled) setDesktop(state); },
      (reason) => { if (!cancelled) setError(normalizeInvokeError(reason)); },
    ).then((unlisten) => {
      if (cancelled) unlisten(); else stopDesktop = unlisten;
    }).catch((reason) => {
      if (!cancelled) setError(normalizeInvokeError(reason));
    });
    return () => {
      cancelled = true;
      stopConfiguration?.();
      stopDesktop?.();
    };
  }, [refresh]);

  useEffect(() => {
    if (section !== "intelligence") return undefined;
    void refreshIntelligence().catch((reason) => {
      setError(normalizeCompanionError(reason));
    });
    if ((intelligenceStatus?.activeBackgroundJobs ?? 0) === 0) return undefined;
    const timer = window.setInterval(() => {
      void refreshIntelligence().catch((reason) => {
        setError(normalizeCompanionError(reason));
      });
    }, 1500);
    return () => window.clearInterval(timer);
  }, [
    intelligenceStatus?.activeBackgroundJobs,
    refreshIntelligence,
    section,
  ]);

  useEffect(() => {
    if (desktop?.phase === "control_panel_visible" && desktop.controlPanelVisible) {
      setCloseRequested(false);
      setPanelVisualOpen(true);
    }
    if (
      desktop?.phase === "transitioning_to_companion" ||
      desktop?.phase === "companion_idle"
    ) {
      setPanelVisualOpen(false);
    }
  }, [desktop?.controlPanelVisible, desktop?.phase]);

  async function run(action, operation, normalize = normalizeCompanionError) {
    setPending(action);
    setError(null);
    try {
      return await operation();
    } catch (reason) {
      setError(normalize(reason));
      return null;
    } finally {
      setPending(null);
    }
  }

  async function submitCharacter(event) {
    event.preventDefault();
    let input;
    try {
      input = buildCharacterSaveInput({
        name: characterName,
        description: characterDescription,
        dialogueStyle: characterDialogueStyle,
        visualPackId,
      });
    } catch (reason) {
      setError({ code: "INVALID_CHARACTER_BRIEF", message: reason.message, retryable: false });
      return;
    }
    await run("character", async () => {
      const existing = characterId
        ? catalog.characters.find((character) => character.characterId === characterId) ?? null
        : null;
      let saved;
      if (existing === null) {
        saved = await createCharacter(input.brief, input.visualPackId);
      } else {
        const existingDialogueStyle = existing.dialogueStyle ?? "";
        const nextDialogueStyle = input.brief.dialogueStyle ?? "";
        const briefChanged = existing.name !== input.brief.name ||
          existing.description !== input.brief.description ||
          existingDialogueStyle !== nextDialogueStyle;
        saved = briefChanged
          ? await updateCharacter(characterId, input.brief)
          : existing;
        if (selectedAppearancePackId(saved) !== input.visualPackId) {
          saved = await setCharacterAppearance(characterId, input.visualPackId);
        }
      }
      await activateCharacter(saved.characterId, saved.revision);
      await refresh(true);
    });
  }

  async function chooseCharacter(character) {
    setCharacterId(character.characterId);
    setCharacterName(character.name);
    setCharacterDescription(character.description);
    setCharacterDialogueStyle(character.dialogueStyle ?? "");
    setVisualPackId(selectedAppearancePackId(character));
    if (character.appearance.status !== "assigned") {
      setError({
        code: character.appearance.status === "unassigned"
          ? "CHARACTER_APPEARANCE_UNASSIGNED"
          : "CHARACTER_APPEARANCE_UNAVAILABLE",
        message: character.appearance.status === "unassigned"
          ? `${character.name} 尚未选择外观，请选择后保存。`
          : `${character.name} 的外观不可用，请重新选择。`,
        retryable: false,
      });
      return;
    }
    await run("character", async () => {
      await activateCharacter(character.characterId, character.revision);
      await refresh(false);
    });
  }

  async function submitProfile(event) {
    event.preventDefault();
    const update = await run("profile", () => setUserProfile(preferredName));
    if (update) setProfile(update.profile);
  }

  async function submitModel(event) {
    event.preventDefault();
    let input;
    try {
      input = buildModelConnectionInput({ protocol, endpoint, model, contextWindowTokens, authMode });
    } catch (reason) {
      setError({ code: "INVALID_MODEL_CONFIG", message: reason.message, retryable: false });
      return;
    }
    const status = await run("model", () => saveModelConnection(input, apiKey || null));
    setApiKey("");
    if (status) setModelStatus(status);
  }

  async function confirmKnowledge(id) {
    await run(`knowledge-confirm-${id}`, async () => {
      await confirmKnowledgeCandidate(id);
      await refreshIntelligence();
    });
  }

  async function deleteKnowledge(id) {
    await run(`knowledge-delete-${id}`, async () => {
      await tombstoneKnowledge(id);
      await refreshIntelligence();
    });
  }

  async function addMemory(event) {
    event.preventDefault();
    if (!memoryContent.trim() || !catalog.active) return;
    await run("memory-create", async () => {
      const scope = memoryKind === "relationship"
        ? { type: "character", characterId: catalog.active.characterId }
        : { type: "global" };
      await createPersonalMemory({ kind: memoryKind, scope, content: memoryContent.trim() });
      setMemoryContent("");
      await refreshIntelligence();
    });
  }

  async function reviseMemory(id, content) {
    await run(`memory-revise-${id}`, async () => {
      await revisePersonalMemory(id, content);
      await refreshIntelligence();
    });
  }

  async function deleteMemory(id) {
    await run(`memory-delete-${id}`, async () => {
      await tombstonePersonalMemory(id);
      await refreshIntelligence();
    });
  }

  async function assignLegacy(id) {
    if (!catalog.active) return;
    await run(`memory-assign-${id}`, async () => {
      await assignLegacyRelationship(id, catalog.active.characterId);
      await refreshIntelligence();
    });
  }

  async function retryBatch(id) {
    await run(`batch-retry-${id}`, async () => {
      await retryExtractionBatch(id);
      await refreshIntelligence();
    });
  }

  async function runDesktop(action, operation) {
    const state = await run(action, operation, normalizeInvokeError);
    if (state) setDesktop(state);
  }

  function requestClose() {
    if (pending !== null || closeRequested) return;
    setCloseRequested(true);
    setPanelVisualOpen(false);
  }

  async function finishClose() {
    if (!closeRequested) return;
    const state = await run("close-panel", restoreCompanionAfterControlPanel, normalizeInvokeError);
    if (state?.phase === "companion_idle") {
      setDesktop(state);
      setCloseRequested(false);
      return;
    }
    setCloseRequested(false);
    setPanelVisualOpen(true);
  }

  const disabled = pending !== null || closeRequested;

  return (
    <main className="cp-stage">
      <AnimatePresence onExitComplete={() => void finishClose()}>
        {panelVisualOpen ? (
          <motion.div
            key="control-panel"
            className="cp-motion"
            initial={{ opacity: 0, y: 18, scale: 0.96 }}
            animate={{ opacity: 1, y: 0, scale: 1 }}
            exit={{ opacity: 0, y: 16, scale: 0.95 }}
          >
            <Card className="cp-shell" size="2">
              <div className="cp-drag-region" data-tauri-drag-region aria-hidden="true" />
              <header className="cp-header">
                <Flex align="center" gap="3">
                  <span className="cp-brand-mark" aria-hidden="true"><MagicWandIcon /></span>
                  <div>
                    <Heading as="h1" size="4" weight="medium">FAIRY 设置</Heading>
                    <Flex align="center" gap="2">
                      <span className={`cp-live-dot ${modelStatus?.ready ? "is-ready" : ""}`} aria-hidden="true" />
                      <Text size="1" color="gray">{modelStatus?.ready ? "Runtime 已就绪" : "等待模型连接"}</Text>
                    </Flex>
                  </div>
                </Flex>
                <Tooltip content="关闭设置并返回角色">
                  <IconButton type="button" size="2" variant="soft" color="gray" aria-label="关闭设置并返回角色" onClick={requestClose} disabled={disabled}>
                    <Cross2Icon />
                  </IconButton>
                </Tooltip>
              </header>

              {error ? (
                <Callout.Root className="cp-error" color="tomato" size="1" role="alert">
                  <Callout.Icon><ExclamationTriangleIcon /></Callout.Icon>
                  <Callout.Text>{error.message}</Callout.Text>
                </Callout.Root>
              ) : null}

              <Tabs.Root className="cp-tabs" value={section} onValueChange={(value) => setSection(assertControlPanelSection(value))}>
                <Tabs.List className="cp-tab-list" size="1" justify="center">
                  {CONTROL_PANEL_SECTIONS.map((item) => {
                    const Icon = SECTION_ICONS[item.id];
                    return (
                      <Tabs.Trigger key={item.id} value={item.id} disabled={disabled}>
                        <Icon aria-hidden="true" />
                        {item.label}
                      </Tabs.Trigger>
                    );
                  })}
                </Tabs.List>
                <Separator size="4" />

                <ScrollArea className="cp-scroll" type="auto" scrollbars="vertical">
                  <AnimatePresence mode="wait" initial={false}>
                    <motion.section
                      className="cp-page"
                      key={section}
                      initial={{ opacity: 0, x: 10 }}
                      animate={{ opacity: 1, x: 0 }}
                      exit={{ opacity: 0, x: -8 }}
                    >
                      {section === "character" ? (
                        <>
                          <PageHeader title="她是谁" description="角色描述会交给 Harness，用来推断此刻更合适的回应。" status={catalog.active ? `当前：${catalog.active.name}` : "尚未激活"} ready={Boolean(catalog.active)} />
                          <Flex className="cp-character-list" gap="2" aria-label="角色列表">
                            <ScrollArea type="auto" scrollbars="horizontal">
                              <Flex gap="2" pb="2">
                                {catalog.characters.map((character) => {
                                  const active = catalog.active?.characterId === character.characterId && catalog.active?.revision === character.revision;
                                  return (
                                    <Button key={`${character.characterId}-${character.revision}`} type="button" size="2" variant={active ? "solid" : "surface"} onClick={() => void chooseCharacter(character)} disabled={disabled}>
                                      {active ? <CheckCircledIcon /> : <PersonIcon />}
                                      {character.name}
                                    </Button>
                                  );
                                })}
                                <Button type="button" size="2" variant="soft" onClick={() => { setCharacterId(null); setCharacterName(""); setCharacterDescription(""); setCharacterDialogueStyle(""); setVisualPackId(""); setError(null); }} disabled={disabled}>
                                  <PlusIcon />新建角色
                                </Button>
                              </Flex>
                            </ScrollArea>
                          </Flex>
                          <form className="cp-form" onSubmit={submitCharacter}>
                            <Field id="character-name" label="角色名称">
                              <TextField.Root id="character-name" value={characterName} onChange={(event) => setCharacterName(event.target.value)} maxLength="48" required />
                            </Field>
                            <Field id="character-description" label="角色描述" hint="写她会留意什么、如何表达亲近与边界；不必写“你是某某”。">
                              <TextArea id="character-description" className="cp-character-description" value={characterDescription} onChange={(event) => setCharacterDescription(event.target.value)} maxLength="2000" resize="none" required />
                            </Field>
                            <Field id="character-dialogue-style" label="日常说话方式" hint="写日常语气、节奏、称呼习惯或几句例句；系统仍保留上面的角色描述。">
                              <TextArea id="character-dialogue-style" className="cp-character-description" value={characterDialogueStyle} onChange={(event) => setCharacterDialogueStyle(event.target.value)} maxLength="1200" resize="none" />
                            </Field>
                            <Field id="character-appearance" label="角色外观" hint="外观只影响桌宠画面，不会修改角色描述、关系记忆或聊天记录。">
                              <div className="cp-appearance-picker">
                                <Select.Root value={visualPackId} onValueChange={setVisualPackId}>
                                  <Select.Trigger id="character-appearance" placeholder="选择角色外观" aria-label="角色外观" />
                                  <Select.Content>
                                    {visualCatalog.visualPacks.map((visual) => (
                                      <Select.Item key={visual.packId} value={visual.packId}>
                                        {visual.displayName}
                                      </Select.Item>
                                    ))}
                                  </Select.Content>
                                </Select.Root>
                                {selectedVisual ? (
                                  <div className="cp-appearance-preview" aria-label={`${selectedVisual.displayName} 外观预览`}>
                                    <span
                                      className="cp-appearance-sprite"
                                      role="img"
                                      aria-label={selectedVisual.displayName}
                                      style={{
                                        width: selectedVisual.frame.width * selectedVisual.scale,
                                        height: selectedVisual.frame.height * selectedVisual.scale,
                                        backgroundImage: `url(${selectedVisual.states[0].imagePath})`,
                                        backgroundSize: `${selectedVisual.frame.width * selectedVisual.scale}px ${selectedVisual.frame.height * selectedVisual.scale}px`,
                                      }}
                                    />
                                  </div>
                                ) : (
                                  <Text as="p" size="1" color="gray">尚未选择外观</Text>
                                )}
                              </div>
                            </Field>
                            <Flex justify="end">
                              <Button type="submit" disabled={disabled}>{characterId ? "更新并激活" : "创建并激活"}</Button>
                            </Flex>
                          </form>
                        </>
                      ) : null}

                      {section === "profile" ? (
                        <>
                          <PageHeader title="怎样称呼你" description="这个称呼会进入对话上下文，让文字与语音都能自然提到你。" status={profile?.preferredName ?? "可以留空"} ready={Boolean(profile?.preferredName)} />
                          <Card className="cp-section-card" size="2">
                            <form className="cp-form" onSubmit={submitProfile}>
                              <Field id="preferred-name" label="偏好称呼" hint="例如 Rinai、凛，或任何让你觉得自然的名字。">
                                <TextField.Root id="preferred-name" value={preferredName} onChange={(event) => setPreferredName(event.target.value)} maxLength="64" placeholder="你希望她怎样叫你？" />
                              </Field>
                              <Flex justify="between" gap="3">
                                <Button color="tomato" variant="soft" type="button" disabled={disabled || !profile?.preferredName} onClick={() => void run("profile", async () => { const update = await clearUserProfile(); setProfile(update.profile); setPreferredName(""); })}>
                                  <TrashIcon />清除称呼
                                </Button>
                                <Button type="submit" disabled={disabled}>保存称呼</Button>
                              </Flex>
                            </form>
                          </Card>
                        </>
                      ) : null}

                      {section === "model" ? (
                        <>
                          <PageHeader title="模型连接" description="明确选择协议；FAIRY 不会自动试错、切换接口或回退 Provider。" status={modelStatus?.ready ? "已就绪" : "需要配置"} ready={Boolean(modelStatus?.ready)} />
                          <form className="cp-form cp-model-form" onSubmit={submitModel}>
                            <Field id="model-protocol" label="OpenAI 兼容协议">
                              <SegmentedControl.Root id="model-protocol" value={protocol} onValueChange={setProtocol} size="2" radius="large">
                                {MODEL_PROTOCOL_OPTIONS.map((option) => <SegmentedControl.Item key={option.value} value={option.value}>{option.label}</SegmentedControl.Item>)}
                              </SegmentedControl.Root>
                            </Field>
                            <div className="cp-two-column">
                              <Field id="model-endpoint" label="Base URL" hint="不要附带具体接口路径。">
                                <TextField.Root id="model-endpoint" type="url" value={endpoint} onChange={(event) => setEndpoint(event.target.value)} required />
                              </Field>
                              <Field id="model-name" label="模型名称">
                                <TextField.Root id="model-name" value={model} onChange={(event) => setModel(event.target.value)} required />
                              </Field>
                            </div>
                            <div className="cp-two-column">
                              <Field id="model-context-window" label="上下文窗口" hint="按模型实际 context window 填写，自动压缩阈值由它推导。">
                                <TextField.Root
                                  id="model-context-window"
                                  type="number"
                                  min={MIN_MODEL_CONTEXT_WINDOW_TOKENS}
                                  max={MAX_MODEL_CONTEXT_WINDOW_TOKENS}
                                  step="1"
                                  value={contextWindowTokens}
                                  onChange={(event) => setContextWindowTokens(event.target.value)}
                                  required
                                />
                              </Field>
                              <Field id="model-auth" label="认证方式">
                                <Select.Root value={authMode} onValueChange={setAuthMode}>
                                  <Select.Trigger id="model-auth" aria-label="认证方式" />
                                  <Select.Content>
                                    <Select.Item value="bearer_key">Bearer Key</Select.Item>
                                    <Select.Item value="no_auth">No Auth（仅 loopback）</Select.Item>
                                  </Select.Content>
                                </Select.Root>
                              </Field>
                              {authMode === "bearer_key" ? (
                                <Field id="model-api-key" label="API Key" hint="写入 Keychain，保存后清空且永不回显。">
                                  <TextField.Root id="model-api-key" type="password" autoComplete="off" value={apiKey} onChange={(event) => setApiKey(event.target.value)} placeholder={modelStatus?.configured ? "留空保留现有密钥" : "仅写入系统 Keychain"}>
                                    <TextField.Slot><LockClosedIcon /></TextField.Slot>
                                  </TextField.Root>
                                </Field>
                              ) : <div />}
                            </div>
                            <Callout.Root className="cp-cache-policy" color="sky" size="1">
                              <Callout.Icon><ResetIcon /></Callout.Icon>
                              <Callout.Text><strong>缓存始终自动</strong>：Runtime 保持稳定前缀，只记录服务端返回的真实缓存用量。</Callout.Text>
                            </Callout.Root>
                            <Flex justify="between" gap="3">
                              <Button color="tomato" variant="soft" type="button" disabled={disabled || !modelStatus?.configured} onClick={() => void run("model", async () => { const status = await clearModelConnection(); setModelStatus(status); })}>
                                <TrashIcon />清除连接
                              </Button>
                              <Button type="submit" disabled={disabled}>保存连接</Button>
                            </Flex>
                          </form>
                        </>
                      ) : null}

                      {section === "intelligence" ? (
                        <>
                          <PageHeader
                            title="智能层"
                            description="会话、个人记忆与知识都保存在本机；当前版本不接入网络搜索。"
                            status={intelligenceStatus?.ready ? "本地层已就绪" : "本地层不可用"}
                            ready={Boolean(intelligenceStatus?.ready)}
                          />

                          <div className="cp-intelligence-track" aria-label="智能层状态">
                            <div className={`cp-intelligence-step ${intelligenceStatus?.ready ? "is-ready" : ""}`}>
                              <span className="cp-intelligence-status-dot" aria-hidden="true" />
                              <div className="cp-intelligence-step-copy">
                                <Text size="1" weight="medium">本地存储</Text>
                                <Text size="1" color="gray">
                                  {intelligenceStatus?.schemaVersion
                                    ? `SQLite v${intelligenceStatus.schemaVersion}`
                                    : "状态不可用"}
                                </Text>
                              </div>
                            </div>
                            <div className={`cp-intelligence-step ${(intelligenceStatus?.activeBackgroundJobs ?? 0) > 0 ? "is-active" : "is-ready"}`}>
                              <span className="cp-intelligence-status-dot" aria-hidden="true" />
                              <div className="cp-intelligence-step-copy">
                                <Text size="1" weight="medium">后台提取</Text>
                                <Text size="1" color="gray">
                                  {(intelligenceStatus?.activeBackgroundJobs ?? 0) > 0
                                    ? `${intelligenceStatus.activeBackgroundJobs} 个任务运行中`
                                    : "当前空闲"}
                                </Text>
                              </div>
                            </div>
                          </div>

                          <div className="cp-intelligence-metrics" aria-label="智能层数据统计">
                            <Card size="1">
                              <Text size="1" color="gray">全局记忆</Text>
                              <Text size="5" weight="medium">
                                {intelligenceStatus?.summary?.activeGlobalMemories ?? "—"}
                              </Text>
                            </Card>
                            <Card size="1">
                              <Text size="1" color="gray">角色关系</Text>
                              <Text size="5" weight="medium">
                                {intelligenceStatus?.summary?.activeCharacterMemories ?? "—"}
                              </Text>
                            </Card>
                            <Card size="1">
                              <Text size="1" color="gray">待抽取轮次</Text>
                              <Text size="5" weight="medium">
                                {intelligenceStatus?.summary?.pendingExtractionTurns ?? "—"}
                              </Text>
                            </Card>
                          </div>

                          {intelligenceStatus?.error ? (
                            <Callout.Root color="tomato" size="1" role="status">
                              <Callout.Icon><ExclamationTriangleIcon /></Callout.Icon>
                              <Callout.Text>{intelligenceStatus.error.message}</Callout.Text>
                            </Callout.Root>
                          ) : null}

                          <section className="cp-memory-ledger" aria-labelledby="memory-ledger-title">
                            <Flex align="start" justify="between" gap="3">
                              <div>
                                <Heading id="memory-ledger-title" as="h3" size="3" weight="medium">个人记忆</Heading>
                                <Text as="p" size="1" color="gray">普通信息全局共享；关系记忆只属于当前角色。</Text>
                              </div>
                              <IconButton variant="ghost" color="gray" type="button" disabled={disabled} onClick={() => void run("intelligence-refresh", refreshIntelligence)} aria-label="刷新记忆">
                                <ResetIcon />
                              </IconButton>
                            </Flex>
                            <form className="cp-memory-create" onSubmit={addMemory}>
                              <Select.Root value={memoryKind} onValueChange={setMemoryKind}>
                                <Select.Trigger aria-label="记忆类型" />
                                <Select.Content>
                                  <Select.Item value="preference">偏好</Select.Item>
                                  <Select.Item value="profile">用户资料</Select.Item>
                                  <Select.Item value="experience">经历</Select.Item>
                                  <Select.Item value="relationship">当前角色关系</Select.Item>
                                </Select.Content>
                              </Select.Root>
                              <TextField.Root value={memoryContent} onChange={(event) => setMemoryContent(event.target.value)} placeholder="简单描述需要记住的信息" />
                              <Button type="submit" disabled={disabled || !catalog.active || !memoryContent.trim()}><PlusIcon />新增</Button>
                            </form>
                            {memoryCatalog ? (
                              <div className="cp-memory-columns">
                                <MemoryColumn title="全局" description="所有角色都可使用" records={memoryCatalog.global} empty="还没有全局记忆。" disabled={disabled} onRevise={(id, content) => void reviseMemory(id, content)} onDelete={(id) => void deleteMemory(id)} />
                                <MemoryColumn title={catalog.active ? `与 ${catalog.active.name} 的关系` : "当前角色关系"} description="不会出现在其他角色的对话中" records={memoryCatalog.character} empty="还没有当前角色的关系记忆。" disabled={disabled} onRevise={(id, content) => void reviseMemory(id, content)} onDelete={(id) => void deleteMemory(id)} />
                                {memoryCatalog.needsReview.length > 0 ? (
                                  <MemoryColumn title="待处理旧记录" description="迁移时无法确认属于哪个角色" records={memoryCatalog.needsReview} empty="没有待处理记录。" disabled={disabled} onRevise={() => {}} onDelete={(id) => void deleteMemory(id)} onAssign={(id) => void assignLegacy(id)} />
                                ) : null}
                              </div>
                            ) : <Text as="p" size="1" color="gray" className="cp-memory-empty">选择并激活角色后读取记忆。</Text>}
                            {batchCatalog?.failed.length > 0 ? (
                              <div className="cp-failed-batches">
                                <Text as="h3" size="2" weight="medium">失败的抽取批次</Text>
                                {batchCatalog.failed.map((batch) => (
                                  <Flex key={batch.id} align="center" justify="between" gap="3">
                                    <Text size="1" color="gray">轮次 {batch.firstTurnSequence}–{batch.lastTurnSequence} · {batch.error?.message ?? "抽取失败"}</Text>
                                    <Button size="1" variant="soft" disabled={disabled} onClick={() => void retryBatch(batch.id)}><ResetIcon />重试</Button>
                                  </Flex>
                                ))}
                              </div>
                            ) : null}
                          </section>

                          <section className="cp-knowledge-ledger" aria-labelledby="knowledge-ledger-title">
                            <Flex className="cp-knowledge-ledger-header" align="start" justify="between" gap="3">
                              <div>
                                <Heading id="knowledge-ledger-title" as="h3" size="3" weight="medium">知识目录</Heading>
                                <Text as="p" size="1" color="gray">既有候选只有你确认后才会参与对话；当前版本不再自动联网验证。</Text>
                              </div>
                              <Tooltip content="重新读取本地知识目录">
                                <IconButton variant="ghost" color="gray" type="button" disabled={disabled} onClick={() => void run("intelligence-refresh", refreshIntelligence)} aria-label="刷新知识目录">
                                  <ResetIcon />
                                </IconButton>
                              </Tooltip>
                            </Flex>
                            {knowledgeCatalog ? (
                              <div className="cp-knowledge-columns">
                                <KnowledgeColumn
                                  title="等待你确认"
                                  description="不会自动进入模型上下文"
                                  records={knowledgeCatalog.candidates}
                                  empty="没有待确认的知识。"
                                  disabled={disabled}
                                  onConfirm={(id) => void confirmKnowledge(id)}
                                  onDelete={(id) => void deleteKnowledge(id)}
                                />
                                <KnowledgeColumn
                                  title="已验证"
                                  description="历史有据或由你明确确认"
                                  records={knowledgeCatalog.verified}
                                  empty="还没有可用知识。"
                                  disabled={disabled}
                                  onConfirm={(id) => void confirmKnowledge(id)}
                                  onDelete={(id) => void deleteKnowledge(id)}
                                />
                              </div>
                            ) : (
                              <Text as="p" className="cp-knowledge-unavailable" size="1" color="gray">
                                {knowledgeCatalogLoading
                                  ? "正在读取本地知识目录…"
                                  : intelligenceStatus?.ready
                                    ? "知识目录未能载入，请查看上方错误后重试。"
                                    : "本地智能层不可用，知识目录未载入。"}
                              </Text>
                            )}
                          </section>

                        </>
                      ) : null}

                      {section === "desktop" ? (
                        <>
                          <PageHeader title="桌面行为" description="控制角色窗口的置顶与点击穿透；关闭设置不会结束当前会话。" status={desktop?.visible ? "角色可见" : "设置替换中"} ready={Boolean(desktop?.controlPanelVisible)} />
                          <Card className="cp-section-card cp-desktop-card" size="2">
                            <Flex className="cp-setting-row" align="center" justify="between" gap="4">
                              <div><Text as="div" size="2" weight="medium">窗口置顶</Text><Text as="p" size="1" color="gray">让角色保持在其他窗口之上。</Text></div>
                              <Switch checked={desktop?.alwaysOnTop ?? false} onCheckedChange={(checked) => void runDesktop("always-on-top", () => setAlwaysOnTop(checked))} disabled={disabled} aria-label="窗口置顶" />
                            </Flex>
                            <Separator size="4" />
                            <Flex className="cp-setting-row" align="center" justify="between" gap="4">
                              <div><Text as="div" size="2" weight="medium">点击穿透</Text><Text as="p" size="1" color="gray">开启后可从菜单栏恢复交互。</Text></div>
                              <Switch checked={desktop?.clickThrough ?? false} onCheckedChange={(checked) => void runDesktop("click-through", () => setClickThrough(checked))} disabled={disabled} aria-label="点击穿透" />
                            </Flex>
                            <Flex className="cp-window-actions" wrap="wrap" gap="2">
                              <Button type="button" variant="surface" onClick={() => void runDesktop("show", showCompanion)} disabled={disabled}>显示角色</Button>
                              <Button type="button" variant="surface" color="gray" onClick={() => void runDesktop("hide", hideCompanion)} disabled={disabled}>隐藏角色</Button>
                              <Button type="button" onClick={() => void runDesktop("restore", restoreCompanionInteraction)} disabled={disabled}><ResetIcon />恢复交互</Button>
                            </Flex>
                          </Card>
                        </>
                      ) : null}
                    </motion.section>
                  </AnimatePresence>
                </ScrollArea>
              </Tabs.Root>
            </Card>
          </motion.div>
        ) : null}
      </AnimatePresence>
    </main>
  );
}
