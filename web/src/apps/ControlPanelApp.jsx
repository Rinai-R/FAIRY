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
  clearModelConnection,
  clearUserProfile,
  createCharacter,
  getModelConnectionStatus,
  getUserProfile,
  listCharacters,
  saveModelConnection,
  setUserProfile,
  updateCharacter,
} from "../companionClient.mjs";
import { normalizeCompanionError } from "../companionState.mjs";
import {
  CONTROL_PANEL_SECTIONS,
  MODEL_PROTOCOL_OPTIONS,
  assertControlPanelSection,
  buildModelConnectionInput,
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

const SECTION_ICONS = Object.freeze({
  character: PersonIcon,
  profile: IdCardIcon,
  model: Link2Icon,
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

export function ControlPanelApp() {
  const [section, setSection] = useState("model");
  const [catalog, setCatalog] = useState(EMPTY_CATALOG);
  const [profile, setProfile] = useState(null);
  const [modelStatus, setModelStatus] = useState(null);
  const [desktop, setDesktop] = useState(null);
  const [pending, setPending] = useState(null);
  const [error, setError] = useState(null);
  const [panelVisualOpen, setPanelVisualOpen] = useState(false);
  const [closeRequested, setCloseRequested] = useState(false);

  const [characterId, setCharacterId] = useState(null);
  const [characterName, setCharacterName] = useState("");
  const [characterDescription, setCharacterDescription] = useState("");
  const [preferredName, setPreferredName] = useState("");
  const [protocol, setProtocol] = useState("chat_completions");
  const [endpoint, setEndpoint] = useState("https://api.deepseek.com");
  const [model, setModel] = useState("deepseek-v4-flash");
  const [authMode, setAuthMode] = useState("bearer_key");
  const [apiKey, setApiKey] = useState("");

  const refresh = useCallback(async (hydrateForms = false) => {
    const [nextCatalog, nextProfile, nextModelStatus, nextDesktop] = await Promise.all([
      listCharacters(),
      getUserProfile(),
      getModelConnectionStatus(),
      getDesktopState(),
    ]);
    setCatalog(nextCatalog);
    setProfile(nextProfile);
    setModelStatus(nextModelStatus);
    setDesktop(nextDesktop);
    if (hydrateForms) {
      if (nextCatalog.active) {
        setCharacterId(nextCatalog.active.characterId);
        setCharacterName(nextCatalog.active.name);
        setCharacterDescription(nextCatalog.active.description);
      }
      setPreferredName(nextProfile?.preferredName ?? "");
      if (nextModelStatus.config) {
        setProtocol(nextModelStatus.config.protocol);
        setEndpoint(nextModelStatus.config.endpoint);
        setModel(nextModelStatus.config.model);
        setAuthMode(nextModelStatus.config.authMode);
      }
    }
  }, []);

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
    if (!characterName.trim() || !characterDescription.trim()) {
      setError({ code: "INVALID_CHARACTER_BRIEF", message: "角色名称和描述都需要填写。", retryable: false });
      return;
    }
    await run("character", async () => {
      const saved = characterId
        ? await updateCharacter(characterId, { name: characterName, description: characterDescription })
        : await createCharacter({ name: characterName, description: characterDescription });
      await activateCharacter(saved.characterId, saved.revision);
      await refresh(true);
    });
  }

  async function chooseCharacter(character) {
    await run("character", async () => {
      await activateCharacter(character.characterId, character.revision);
      setCharacterId(character.characterId);
      setCharacterName(character.name);
      setCharacterDescription(character.description);
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
      input = buildModelConnectionInput({ protocol, endpoint, model, authMode });
    } catch (reason) {
      setError({ code: "INVALID_MODEL_CONFIG", message: reason.message, retryable: false });
      return;
    }
    const status = await run("model", () => saveModelConnection(input, apiKey || null));
    setApiKey("");
    if (status) setModelStatus(status);
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
                                <Button type="button" size="2" variant="soft" onClick={() => { setCharacterId(null); setCharacterName(""); setCharacterDescription(""); }} disabled={disabled}>
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
