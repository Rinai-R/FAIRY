import React, { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  advanceWorkflow,
  cloneVoice,
  cloneVoiceStatus,
  deleteSession,
  exportWebGAL,
  getCapabilities,
  getSession,
  getPlugins,
  getProviderHealth,
  getSessions,
  getUserConfig,
  runtimeMode,
  saveCharacterPackage,
  saveUserConfig as saveUserConfigRemote,
  startSceneGeneration,
  streamTurn,
  synthesizeVoice,
  turn,
  uploadDocumentAsset
} from "./api";
import { DirectorView } from "./views/DirectorView";
import { GalgameStageView } from "./views/GalgameStage";
import { createCharacterPackage, mergeCharacterPackage } from "./characterPackage";
import { emotionLabel, expressionLabel, motionLabel } from "./views/displayLabels";
import { stageWorkflowWaiting } from "./views/workflowReadiness";

const CONFIG_STORAGE_KEY = "fairy.user-config.v1";
const MAX_DOCUMENT_ASSET_BYTES = 64 * 1024 * 1024;
const EXPRESSION_PRESETS = ["calm", "soft_smile", "happy", "curious", "surprised", "thinking", "serious", "worried", "embarrassed", "angry"];
const DEFAULT_LANGUAGE_PLAN = Object.freeze({
  display_language: "zh-CN",
  speech_language: "ja-JP",
  translation_provider: "agent",
  mode: "translate_for_voice"
});

const defaultCharacters = [
  {
    id: "tutor",
    display_name: "亚托莉",
    voice_id: "zh_female_vv_uranus_bigtts",
    persona: "轻快、好奇、温柔，像视觉小说里的同伴老师。会把文档知识放进玩家可自由提问和互动的 Galgame 教学场景。",
    style_rules: ["只围绕当前文档教学。", "不要替玩家说话。", "每轮只推进一小段材料线索。", "回复适合语音播放。"],
    assets: {
      portrait_url: "",
      background_url: "",
      backgrounds: {},
      reference_image_url: "",
      style_prompt: "clean anime visual novel tutor, white interface, dark red accent",
      cg_prompt: "teaching in a quiet classroom-like visual novel scene",
      moods: {
        calm: { portrait_url: "", cg_prompt: "calm expression" },
        curious: { portrait_url: "", cg_prompt: "curious smile" },
        serious: { portrait_url: "", cg_prompt: "serious teaching expression" }
      }
    }
  },
  {
    id: "skeptic",
    display_name: "追问者",
    voice_id: "zh_female_vv_uranus_bigtts",
    persona: "像同桌一样追问、质疑和举反例，帮助玩家发现自己没理解的地方。",
    style_rules: ["用问题推动思考。", "不要替玩家说话。", "质疑必须围绕文档内容。"],
    assets: {
      portrait_url: "",
      background_url: "",
      backgrounds: {},
      reference_image_url: "",
      style_prompt: "anime visual novel classmate, analytical expression",
      cg_prompt: "asking a sharp question in a study scene",
      moods: {
        calm: { portrait_url: "", cg_prompt: "neutral thinking face" },
        curious: { portrait_url: "", cg_prompt: "leaning forward with curiosity" },
        serious: { portrait_url: "", cg_prompt: "focused skeptical expression" }
      }
    }
  }
];

const defaultPrompt = {
  system: "你是 FAIRY 的文档 Galgame 教学 Agent，负责在玩家驱动的互动场景中解释材料。",
  developer: "不要一次性生成完整剧本。你只扮演当前角色，根据玩家每轮输入即时回应；必须基于文档内容教学。",
  scene_instruction: "围绕当前互动剧情推进对话，像视觉小说角色一样自然表达，但目标始终是帮助用户理解文档。",
  response_contract: "display_text 用于屏幕显示，speech_text 用于语音合成。speech_text 不是机械直译，必须按当前角色的性格、称呼、语气、停顿和口癖做自然发声本地化；两者语言不同也必须知识含义一致。每轮 2 到 5 句，先回应玩家，再把材料中的概念娓娓道来，必要时提出一个小问题。",
  style_rules: ["只围绕当前文档和学习目标。", "不要替玩家说话。", "用自然对白推进教学，不使用固定讲解句式。", "不要跳出教师角色。", "屏幕台词里不要出现内部流程词。"]
};

const defaultRelation = {
  user_id: "default",
  affinity: 0.36,
  trust: 0.58,
  tension: 0.02,
  closeness: 0.4,
  updated_at: new Date().toISOString()
};

const configTabs = [
  { id: "characters", label: "角色资源" },
  { id: "runtime", label: "运行" }
];

const volcengineResourceOptions = [
  { value: "seed-icl-2.0", label: "SeedICL 2.0 声音复刻" },
  { value: "seed-icl-1.0", label: "SeedICL 1.0 声音复刻" },
  { value: "seed-icl-1.0-concurr", label: "SeedICL 1.0 并发版" }
];

class StageErrorBoundary extends React.Component {
  constructor(props) {
    super(props);
    this.state = { error: null };
  }

  static getDerivedStateFromError(error) {
    return { error };
  }

  componentDidUpdate(previousProps) {
    if (previousProps.resetKey !== this.props.resetKey && this.state.error) {
      this.setState({ error: null });
    }
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <section className="stage-error-panel">
        <span className="eyebrow">舞台渲染失败</span>
        <h2>记录数据没有完整恢复</h2>
        <p>{errorMessage(this.state.error, "未知前端错误")}</p>
        <div className="header-actions">
          <button className="primary-button" type="button" onClick={this.props.onBack}>返回记录</button>
          <button className="ghost-button" type="button" onClick={() => this.setState({ error: null })}>重试</button>
        </div>
      </section>
    );
  }
}

export function App() {
  const savedConfigRef = useRef(loadSavedConfig());
  const audioRef = useRef(null);
  const typewriterTimersRef = useRef(new Set());
  const configHydratedRef = useRef(false);
  const remoteHydrateStartedRef = useRef(false);
  const remoteSaveTimerRef = useRef(null);
  const autoplayBlockRef = useRef({ url: "", time: 0 });
  const voiceTestCacheRef = useRef({ key: "", url: "" });
  const [activeView, setActiveView] = useState(() => {
    const hash = typeof window !== "undefined" ? window.location.hash.replace("#", "") : "";
    return ["dashboard", "history", "director", "config", "logs", "stage"].includes(hash) ? hash : "dashboard";
  });
  const [configTab, setConfigTab] = useState("characters");
  const [capabilities, setCapabilities] = useState(null);
  const [characters, setCharacters] = useState(savedConfigRef.current?.characters || defaultCharacters);
  const [activeCharacterID, setActiveCharacterID] = useState(savedConfigRef.current?.activeCharacterID || "tutor");
  const [documentTitle, setDocumentTitle] = useState(savedConfigRef.current?.documentTitle || "新的文档");
  const [learningGoal, setLearningGoal] = useState(savedConfigRef.current?.learningGoal || "理解核心概念，并能用自己的话复述。");
  const [generationRequirement, setGenerationRequirement] = useState(savedConfigRef.current?.generationRequirement || "");
  const [documentText, setDocumentText] = useState("");
  const [documentURL, setDocumentURL] = useState(savedConfigRef.current?.documentURL || "");
  const [documentAsset, setDocumentAsset] = useState(savedConfigRef.current?.documentAsset || null);
  const [documentImport, setDocumentImport] = useState({ status: "idle", message: "支持粘贴正文、上传任意教学材料文件，或导入网络文档链接。", pages: 0 });
  const [scene, setScene] = useState(defaultScene());
  const [activeSession, setActiveSession] = useState(null);
  const [lessonWorkflow, setLessonWorkflow] = useState(defaultWorkflow());
  const [openingMessage, setOpeningMessage] = useState("");
  const [webgalExport, setWebgalExport] = useState(null);
  const [relation, setRelation] = useState(defaultRelation);
  const [prompt, setPrompt] = useState(savedConfigRef.current?.prompt || defaultPrompt);
  const [agentProvider, setAgentProvider] = useState(savedConfigRef.current?.providers?.agentProvider || "mock");
  const [voiceProvider, setVoiceProvider] = useState(savedConfigRef.current?.providers?.voiceProvider || "macos");
  const [imageProvider, setImageProvider] = useState(savedConfigRef.current?.providers?.imageProvider || "mock");
  const [sceneProvider, setSceneProvider] = useState(savedConfigRef.current?.providers?.sceneProvider || "mock");
  const [voiceProfile, setVoiceProfile] = useState(savedConfigRef.current?.voiceProfile || {
    endpoint: "http://127.0.0.1:9880",
    text_lang: "ja",
    prompt_lang: "ja",
    media_type: "wav",
    text_split_method: "cut5",
    ref_audio_path: "",
    prompt_text: ""
  });
  const [volcengineProfile, setVolcengineProfile] = useState(savedConfigRef.current?.volcengineProfile || {
    app_id: "",
    access_token: "",
    resource_id: "seed-icl-2.0",
    speaker: "",
    language: "ja"
  });
  const [languagePlan, setLanguagePlan] = useState(() => normalizeLanguagePlan(savedConfigRef.current?.languagePlan || DEFAULT_LANGUAGE_PLAN));
  const [voiceClone, setVoiceClone] = useState({
    speaker_id: "",
    status: "idle",
    message: "",
    sample_logs: []
  });
  const [voiceCloneFiles, setVoiceCloneFiles] = useState([]);
  const [cloneBusy, setCloneBusy] = useState(false);
  const [voiceTestText, setVoiceTestText] = useState(savedConfigRef.current?.voiceTestText || "わかりました。これから一緒に勉強しましょう。");
  const [voiceTestBusy, setVoiceTestBusy] = useState(false);
  const [cgConfig, setCgConfig] = useState(savedConfigRef.current?.cgConfig || {
    enabled: true,
    endpoint: "http://127.0.0.1:8188",
    workflow: "",
    negative_prompt: "low quality, blurry, extra fingers",
    style: "galgame character cg",
    size: "1280x720"
  });
  const [messages, setMessages] = useState([
    { role: "assistant", text: "先把材料放进主页，我会把它整理成一段可以推进的教学剧情。前面会覆盖材料主线，最后再进入自由讨论。", meta: "ready" }
  ]);
  const [logs, setLogs] = useState([{ level: "info", message: "FAIRY 已启动。", time: new Date().toLocaleTimeString() }]);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [generationBusy, setGenerationBusy] = useState(false);
  const [speaking, setSpeaking] = useState(false);
  const [lastAudioURL, setLastAudioURL] = useState("");
  const [lastCG, setLastCG] = useState(null);
  const [sessions, setSessions] = useState([]);
  const [providerHealth, setProviderHealth] = useState([]);
  const [pluginCatalog, setPluginCatalog] = useState(null);
  const [configStatus, setConfigStatus] = useState({ state: "loading", message: "正在加载本机配置..." });
  const [runtimeState, setRuntimeState] = useState({
    emotion: "calm",
    expression: "soft_smile",
    motion: "idle",
    voice: "-",
    audio: "-",
    stageWaiting: false,
    cg: "等待生成",
    memories: []
  });

  const providerOptions = capabilities?.providers || {
    agents: [{ id: "mock", display_name: "Mock Agent" }, { id: "codex", display_name: "Codex CLI Agent" }, { id: "fairy-agent", display_name: "FAIRY Agent" }],
    voices: [{ id: "macos", display_name: "macOS say" }, { id: "mock", display_name: "Mock Voice" }, { id: "volcengine", display_name: "火山声音复刻" }],
    images: [{ id: "mock", display_name: "Mock CG" }, { id: "comfyui", display_name: "ComfyUI" }],
    scenes: [{ id: "mock", display_name: "Teaching Scene" }, { id: "codex", display_name: "Codex Scene" }]
  };

  const activeCharacter = useMemo(
    () => characters.find((item) => item.id === activeCharacterID) || characters[0],
    [activeCharacterID, characters]
  );
  const activeCharacters = useMemo(() => activeCharacter ? [activeCharacter] : [], [activeCharacter]);
  const documentStats = useMemo(() => {
    const text = documentText.trim();
    return {
      chars: text.length,
      paragraphs: text ? text.split(/\n\s*\n/).filter(Boolean).length : 0
    };
  }, [documentText]);
  const mood = normalizeMood(runtimeState.expression || runtimeState.emotion);
  const effectiveProviders = useMemo(() => ({
    agent: activeCharacter?.runtime?.agent_provider || agentProvider,
    voice: activeCharacter?.runtime?.voice_provider || voiceProvider,
    image: activeCharacter?.runtime?.image_provider || imageProvider,
    scene: activeCharacter?.runtime?.scene_provider || sceneProvider
  }), [activeCharacter, agentProvider, imageProvider, sceneProvider, voiceProvider]);
  const canEnterStage = useMemo(
    () => hasPlayableWorkflow(lessonWorkflow, scene, activeSession),
    [activeSession, lessonWorkflow, scene]
  );
  const hasGeneratingSessions = useMemo(
    () => sessions.some((record) => recordGenerationStatus(record) === "generating"),
    [sessions]
  );

  useEffect(() => {
    if (remoteHydrateStartedRef.current) return;
    remoteHydrateStartedRef.current = true;
    hydrateUserConfig();
  }, []);

  useEffect(() => {
    getCapabilities()
      .then((payload) => {
        setCapabilities(payload);
        if (!savedConfigRef.current?.providers) {
          setAgentProvider(payload.defaults?.agent_provider || "mock");
          setVoiceProvider(payload.defaults?.voice_provider || "macos");
          setImageProvider(payload.defaults?.image_provider || "mock");
          setSceneProvider(payload.defaults?.scene_provider || "mock");
        }
        appendLog("info", "Capabilities 加载完成。");
      })
      .catch((error) => appendLog("error", `Capabilities 加载失败：${errorMessage(error, "未知错误")}`));
  }, []);

  useEffect(() => {
    refreshSessions();
    refreshProviderHealth();
    refreshPlugins();
  }, []);

  useEffect(() => {
    if (activeView !== "stage" || !activeSession?.id) return undefined;
    const timer = window.setInterval(() => {
      syncActiveSession(activeSession.id);
    }, 1500);
    syncActiveSession(activeSession.id);
    return () => window.clearInterval(timer);
  }, [activeSession?.id, activeView]);

  useEffect(() => {
    if (!hasGeneratingSessions) return undefined;
    const timer = window.setInterval(() => {
      refreshSessions({ quiet: true });
    }, 2500);
    return () => window.clearInterval(timer);
  }, [hasGeneratingSessions, sessions.length]);

  useEffect(() => {
    if (!configHydratedRef.current) return;
    persistCurrentConfig();
  }, [
    activeCharacterID,
    agentProvider,
    cgConfig,
    characters,
    documentAsset,
    documentTitle,
    documentURL,
    imageProvider,
    languagePlan,
    learningGoal,
    generationRequirement,
    prompt,
    sceneProvider,
    voiceProfile,
    voiceProvider,
    voiceTestText,
    volcengineProfile
  ]);

  useEffect(() => () => {
    if (remoteSaveTimerRef.current) {
      window.clearTimeout(remoteSaveTimerRef.current);
    }
    for (const timer of typewriterTimersRef.current) {
      window.clearInterval(timer);
    }
    typewriterTimersRef.current.clear();
  }, []);

  async function hydrateUserConfig() {
    try {
      const payload = await getUserConfig();
      const remoteConfig = payload?.exists ? payload.config : null;
      if (remoteConfig) {
        const config = sanitizeSavedConfig(remoteConfig);
        applySavedConfig(config);
        saveUserConfig(config);
        savedConfigRef.current = config;
        configHydratedRef.current = true;
        setConfigStatus({ state: "ready", message: "已加载本机保存配置。" });
        appendLog("info", "本机保存配置已加载。");
      } else {
        if (savedConfigRef.current) {
          await saveUserConfigRemote(savedConfigRef.current);
          configHydratedRef.current = true;
          setConfigStatus({ state: "ready", message: "已加载浏览器保存配置，并迁移到本机配置。" });
          appendLog("info", "浏览器保存配置已迁移到本机配置。");
        } else {
          configHydratedRef.current = true;
          setConfigStatus({ state: "ready", message: "暂无保存配置，使用默认值。" });
          appendLog("info", "暂无保存配置，使用默认值。");
        }
      }
    } catch (error) {
      const message = errorMessage(error, "未知错误");
      setConfigStatus({ state: "error", message: `本机配置加载失败：${message}` });
      appendLog("error", `本机配置加载失败：${message}`);
    }
  }

  function applySavedConfig(config) {
    if (!config) return;
    if (Array.isArray(config.characters) && config.characters.length > 0) setCharacters(config.characters);
    if (config.activeCharacterID) setActiveCharacterID(config.activeCharacterID);
    if (config.documentTitle) setDocumentTitle(config.documentTitle);
    if (typeof config.learningGoal === "string") setLearningGoal(config.learningGoal);
    if (typeof config.generationRequirement === "string") setGenerationRequirement(config.generationRequirement);
    if (typeof config.documentURL === "string") setDocumentURL(config.documentURL);
    if (Object.prototype.hasOwnProperty.call(config, "documentAsset")) setDocumentAsset(config.documentAsset || null);
    // interactionMode removed
    if (config.languagePlan) setLanguagePlan(normalizeLanguagePlan(config.languagePlan));
    if (config.prompt) setPrompt(config.prompt);
    if (config.cgConfig) setCgConfig(config.cgConfig);
    if (config.voiceProfile) setVoiceProfile(config.voiceProfile);
    if (config.volcengineProfile) setVolcengineProfile(config.volcengineProfile);
    if (typeof config.voiceTestText === "string") setVoiceTestText(config.voiceTestText);
    if (config.providers) {
      if (config.providers.agentProvider) setAgentProvider(config.providers.agentProvider);
      if (config.providers.voiceProvider) setVoiceProvider(config.providers.voiceProvider);
      if (config.providers.imageProvider) setImageProvider(config.providers.imageProvider);
      if (config.providers.sceneProvider) setSceneProvider(config.providers.sceneProvider);
    }
  }

  function buildCurrentConfig(overrides = {}) {
    const next = {
      activeCharacterID,
      cgConfig,
      characters,
      documentAsset,
      documentTitle,
      documentURL,
      languagePlan: normalizeLanguagePlan(overrides.languagePlan || languagePlan),
      learningGoal,
      generationRequirement,
      prompt,
      providers: { agentProvider, voiceProvider, imageProvider, sceneProvider },
      voiceProfile,
      voiceTestText,
      volcengineProfile,
      ...overrides
    };
    next.languagePlan = normalizeLanguagePlan(next.languagePlan || DEFAULT_LANGUAGE_PLAN);
    if (Array.isArray(next.characters)) {
      next.characters = next.characters.map((character) => normalizeSavedCharacter(character)).filter(Boolean);
    }
    return next;
  }

  function persistCurrentConfig(overrides = {}, options = {}) {
    const next = buildCurrentConfig(overrides);
    saveUserConfig(next);
    queueRemoteConfigSave(next, options.immediate);
    savedConfigRef.current = next;
    return next;
  }

  async function saveConfigNow(overrides = {}) {
    const next = buildCurrentConfig(overrides);
    saveUserConfig(next);
    savedConfigRef.current = next;
    setConfigStatus({ state: "loading", message: "正在保存配置..." });
    await saveUserConfigRemote(next);
    setConfigStatus({ state: "ready", message: "配置已保存到本机。" });
    return next;
  }

  function queueRemoteConfigSave(config, immediate = false) {
    if (!configHydratedRef.current) return;
    if (remoteSaveTimerRef.current) {
      window.clearTimeout(remoteSaveTimerRef.current);
    }
    const run = () => {
      saveUserConfigRemote(config)
        .then(() => setConfigStatus({ state: "ready", message: "配置已保存到本机。" }))
        .catch((error) => {
          const message = errorMessage(error, "未知错误");
          setConfigStatus({ state: "error", message: `配置保存失败：${message}` });
          appendLog("error", `配置保存失败：${message}`);
        });
    };
    if (immediate) {
      run();
      return;
    }
    remoteSaveTimerRef.current = window.setTimeout(run, 300);
  }

  function appendLog(level, message) {
    setLogs((items) => [{ level, message, time: new Date().toLocaleTimeString() }, ...items].slice(0, 180));
  }

  function appendAssistantMessage({ text, speechText = "", segments = [], meta, nodeID = "", audioURL = "", animate = true, replaceLast = false }) {
    const id = messageID("assistant");
    const fullText = text || "...";
    const chars = Array.from(fullText);
    const message = {
      id,
      role: "assistant",
      text: animate ? "" : fullText,
      speechText,
      segments,
      meta,
      nodeID,
      audioURL,
      typing: animate
    };
    setMessages((items) => {
      if (replaceLast && items.at(-1)?.role === "assistant") {
        return [...items.slice(0, -1), message];
      }
      return [...items, message];
    });
    if (!animate) return id;

    let index = 0;
    const stepSize = chars.length > 90 ? 2 : 1;
    const timer = window.setInterval(() => {
      index = Math.min(chars.length, index + stepSize);
      const visible = chars.slice(0, index).join("");
      setMessages((items) => items.map((item) => item.id === id ? { ...item, text: visible, typing: index < chars.length } : item));
      if (index >= chars.length) {
        window.clearInterval(timer);
        typewriterTimersRef.current.delete(timer);
      }
    }, 24);
    typewriterTimersRef.current.add(timer);
    return id;
  }

  function updateCharacter(id, recipe) {
    setCharacters((items) => items.map((item) => item.id === id ? recipe(item) : item));
  }

  function setCharacterField(id, field, value) {
    updateCharacter(id, (item) => ({ ...item, [field]: value }));
  }

  function setCharacterAsset(id, field, value) {
    updateCharacter(id, (item) => ({ ...item, assets: { ...(item.assets || {}), [field]: value } }));
  }

  function setCharacterBackgroundAsset(id, key, value) {
    updateCharacter(id, (item) => ({
      ...item,
      assets: {
        ...(item.assets || {}),
        backgrounds: pruneEmptyObject({
          ...(item.assets?.backgrounds || {}),
          [key]: value
        })
      }
    }));
  }

  function setCharacterRuntimeField(id, field, value) {
    updateCharacter(id, (item) => ({
      ...item,
      runtime: pruneEmptyObject({ ...(item.runtime || {}), [field]: value })
    }));
  }

  function setCharacterRuntimeAgentField(id, field, value) {
    updateCharacter(id, (item) => ({
      ...item,
      runtime: {
        ...(item.runtime || {}),
        agent: pruneEmptyObject({ ...(item.runtime?.agent || {}), [field]: value })
      }
    }));
  }

  function setCharacterRuntimeVoiceField(id, field, value) {
    updateCharacter(id, (item) => ({
      ...item,
      runtime: pruneEmptyObject({
        ...(item.runtime || {}),
        voice: pruneEmptyObject({ ...(item.runtime?.voice || {}), [field]: value })
      })
    }));
  }

  function setCharacterRuntimeVoiceExtraField(id, field, value) {
    updateCharacter(id, (item) => ({
      ...item,
      runtime: pruneEmptyObject({
        ...(item.runtime || {}),
        voice: pruneEmptyObject({
          ...(item.runtime?.voice || {}),
          extra: pruneEmptyObject({ ...(item.runtime?.voice?.extra || {}), [field]: value })
        })
      })
    }));
  }

  function setCharacterRuntimeImageField(id, field, value) {
    updateCharacter(id, (item) => ({
      ...item,
      runtime: pruneEmptyObject({
        ...(item.runtime || {}),
        image: pruneEmptyObject({ ...(item.runtime?.image || {}), [field]: value })
      })
    }));
  }

  function setCharacterRuntimeLanguageField(id, field, value) {
    updateCharacter(id, (item) => ({
      ...item,
      runtime: pruneEmptyObject({
        ...(item.runtime || {}),
        language: normalizeLanguageOverride({ ...(item.runtime?.language || {}), [field]: value })
      })
    }));
  }

  function setCharacterPromptField(id, field, value) {
    updateCharacter(id, (item) => ({
      ...item,
      prompt: pruneEmptyObject({ ...(item.prompt || {}), [field]: value })
    }));
  }

  function setCharacterMoodAsset(id, moodName, field, value) {
    updateCharacter(id, (item) => ({
      ...item,
      assets: {
        ...(item.assets || {}),
        moods: {
          ...(item.assets?.moods || {}),
          [moodName]: {
            ...(item.assets?.moods?.[moodName] || {}),
            [field]: value
          }
        }
      }
    }));
  }

  function addCharacterMoodAsset(id) {
    updateCharacter(id, (item) => {
      const moods = item.assets?.moods || {};
      const key = nextMoodKey(moods);
      return {
        ...item,
        assets: {
          ...(item.assets || {}),
          moods: {
            ...moods,
            [key]: {
              label: "新差分",
              description: "描述这个差分适合什么情绪、语气和剧情时刻。",
              portrait_url: "",
              cg_prompt: "",
              voice_style: ""
            }
          }
        }
      };
    });
  }

  function removeCharacterMoodAsset(id, moodName) {
    updateCharacter(id, (item) => {
      const moods = { ...(item.assets?.moods || {}) };
      delete moods[moodName];
      return { ...item, assets: { ...(item.assets || {}), moods } };
    });
  }

  function renameCharacterMoodAsset(id, oldName, nextName) {
    const key = sanitizeMoodKey(nextName);
    if (!key || key === oldName) return;
    updateCharacter(id, (item) => {
      const moods = { ...(item.assets?.moods || {}) };
      if (moods[key]) return item;
      moods[key] = moods[oldName] || {};
      delete moods[oldName];
      return { ...item, assets: { ...(item.assets || {}), moods } };
    });
  }

  async function saveRoleConfig(id = activeCharacterID) {
    const nextCharacters = characters.map((item) => item.id === id ? normalizeSavedCharacter(item) : item);
    const character = characters.find((item) => item.id === id);
    try {
      await saveConfigNow({ characters: nextCharacters });
      appendLog("info", `角色资源已保存：${character?.display_name || id}`);
      return true;
    } catch (error) {
      const message = errorMessage(error, "未知错误");
      setConfigStatus({ state: "error", message: `配置保存失败：${message}` });
      appendLog("error", `角色资源保存失败：${message}`);
      return false;
    }
  }

  async function exportCharacterPackage(scope = "active", options = {}) {
    try {
      const selected = scope === "all" ? characters : characters.filter((item) => item.id === activeCharacterID);
      const redactSensitive = options.redactSensitive !== false;
      const pack = createCharacterPackage(selected.length ? selected : characters.slice(0, 1), { redactSensitive });
      const filenameBase = scope === "all" ? "fairy-character-pack" : (selected[0]?.display_name || selected[0]?.id || "fairy-character");
      const filename = `${safeFileName(filenameBase)}.fairy-character.json`;
      const content = JSON.stringify(pack, null, 2);
      const credentialNote = redactSensitive ? "敏感凭据已脱敏" : "包含敏感凭据";
      const saved = await saveCharacterPackage(filename, content);
      if (saved?.cancelled) {
        appendLog("info", "已取消导出角色包。");
        return;
      }
      if (saved?.path) {
        appendLog("info", `角色包已保存到：${saved.path}（${credentialNote}）`);
        setConfigStatus({ state: "ready", message: `角色包已保存到：${saved.path}（${credentialNote}）` });
        return;
      }
      downloadJSONContent(content, filename);
      appendLog("info", `角色包已交给浏览器下载：${filename}（${credentialNote}）。保存位置由浏览器下载设置决定。`);
      setConfigStatus({ state: "ready", message: `角色包已交给浏览器下载：${filename}（${credentialNote}）` });
    } catch (error) {
      const message = errorMessage(error, "未知错误");
      setConfigStatus({ state: "error", message: `角色包导出失败：${message}` });
      appendLog("error", `角色包导出失败：${message}`);
    }
  }

  async function importCharacterPackageFile(file) {
    if (!file) return;
    try {
      const text = await file.text();
      const merged = mergeCharacterPackage(characters, text);
      setCharacters(merged.characters);
      if (merged.firstCharacterID) setActiveCharacterID(merged.firstCharacterID);
      await saveConfigNow({ characters: merged.characters, activeCharacterID: merged.firstCharacterID || activeCharacterID });
      appendLog("info", `角色包已导入：${merged.importedCount} 个角色。`);
      setConfigStatus({ state: "ready", message: `角色包已导入：${merged.importedCount} 个角色。` });
    } catch (error) {
      const message = errorMessage(error, "未知错误");
      setConfigStatus({ state: "error", message: `角色包导入失败：${message}` });
      appendLog("error", `角色包导入失败：${message}`);
    }
  }

  async function importDocumentFile(file) {
    if (!file) return;
    if (file.size > MAX_DOCUMENT_ASSET_BYTES) {
      const message = `文件过大：${formatBytes(file.size)}，当前上限 ${formatBytes(MAX_DOCUMENT_ASSET_BYTES)}`;
      setDocumentImport({ status: "error", message, pages: 0 });
      appendLog("error", `文档导入失败：${message}`);
      return;
    }
    setDocumentImport({ status: "loading", message: `正在上传 ${file.name}...`, pages: 0 });
    try {
      const payload = await uploadDocumentAsset(file);
      setDocumentAsset(payload);
      if (documentTitle === "新的文档") setDocumentTitle(file.name.replace(/\.[^.]+$/, ""));
      setDocumentImport({
        status: "ready",
        message: `已导入文件 ${payload.filename || file.name} · ${formatBytes(payload.size_bytes || file.size)}`,
        pages: 0
      });
      appendLog("info", `已上传教学材料：${payload.path || file.name}`);
    } catch (error) {
      const message = errorMessage(error, "未知错误");
      setDocumentImport({ status: "error", message, pages: 0 });
      appendLog("error", `文档导入失败：${message}`);
    }
  }

  async function importDocumentURL() {
    const url = documentURL.trim();
    if (!url) {
      setDocumentImport({ status: "error", message: "请先填写网络文档 URL。", pages: 0 });
      return;
    }
    if (documentTitle === "新的文档") setDocumentTitle("网络文档");
    setDocumentImport({
      status: "ready",
      message: "已导入网络文档链接。",
      pages: 0
    });
    appendLog("info", `已导入网络文档链接：${url}`);
  }

  function buildVoiceProfile(provider = voiceProvider) {
    const speechLanguage = languageCodeForVoiceProvider(effectiveLanguagePlan().speech_language);
    if (activeCharacter?.runtime?.voice && Object.keys(activeCharacter.runtime.voice).length > 0) {
      return normalizeVoiceProfileLanguage(activeCharacter.runtime.voice, provider, speechLanguage);
    }
    if (provider === "volcengine") {
      return {
        voice_id: volcengineProfile.speaker,
        media_type: "mp3",
        extra: {
          app_id: volcengineProfile.app_id,
          access_token: volcengineProfile.access_token,
          resource_id: volcengineProfile.resource_id,
          speaker: volcengineProfile.speaker,
          explicit_language: speechLanguage || volcengineProfile.language
        }
      };
    }
    return normalizeVoiceProfileLanguage(voiceProfile, provider, speechLanguage);
  }

  async function startVoiceClone() {
    if (cloneBusy) return;
    setCloneBusy(true);
    try {
      const profile = buildVoiceProfile("volcengine");
      const extra = profile.extra || {};
      const speaker = extra.speaker || profile.voice_id || activeCharacter?.voice_id || "";
      const speechLanguage = languageCodeForVoiceProvider(effectiveLanguagePlan().speech_language || extra.explicit_language || volcengineProfile.language);
      if (!speaker.trim()) {
        throw new Error("请先填写 S_ 开头的声音 ID。");
      }
      if (voiceCloneFiles.length !== 1) {
        throw new Error("火山声音复刻 V3 单次训练只接受 1 段 audio；请先选择一段参考音频。");
      }
      const samples = await Promise.all(Array.from(voiceCloneFiles).map(readAudioSample));
      const payload = await cloneVoice({
        provider: "volcengine",
        app_id: extra.app_id || volcengineProfile.app_id,
        access_token: extra.access_token || volcengineProfile.access_token,
        resource_id: extra.resource_id || volcengineProfile.resource_id,
        speaker_id: speaker,
        language: speechLanguage,
        samples
      });
      const speakerID = payload.speaker_id || speaker;
      setVoiceClone(payload);
      setCharacterField(activeCharacterID, "voice_id", speakerID);
      setCharacterRuntimeVoiceField(activeCharacterID, "voice_id", speakerID);
      setCharacterRuntimeVoiceExtraField(activeCharacterID, "speaker", speakerID);
      appendLog("info", `声音复刻已提交：${speakerID} / ${samples.length} 段音频`);
    } catch (error) {
      const message = errorMessage(error, "未知错误");
      setVoiceClone((current) => ({ ...current, status: "error", message }));
      appendLog("error", `声音复刻提交失败：${message}`);
    } finally {
      setCloneBusy(false);
    }
  }

  async function refreshVoiceCloneStatus() {
    if (cloneBusy) return;
    setCloneBusy(true);
    try {
      const profile = buildVoiceProfile("volcengine");
      const extra = profile.extra || {};
      const speaker = extra.speaker || profile.voice_id || activeCharacter?.voice_id || "";
      const speechLanguage = languageCodeForVoiceProvider(effectiveLanguagePlan().speech_language || extra.explicit_language || volcengineProfile.language);
      const payload = await cloneVoiceStatus({
        provider: "volcengine",
        app_id: extra.app_id || volcengineProfile.app_id,
        access_token: extra.access_token || volcengineProfile.access_token,
        resource_id: extra.resource_id || volcengineProfile.resource_id,
        speaker_id: speaker,
        language: speechLanguage
      });
      setVoiceClone(payload);
      appendLog("info", `声音复刻状态：${payload.status || "unknown"} ${payload.message || ""}`);
    } catch (error) {
      const message = errorMessage(error, "未知错误");
      setVoiceClone((current) => ({ ...current, status: "error", message }));
      appendLog("error", `声音复刻状态查询失败：${message}`);
    } finally {
      setCloneBusy(false);
    }
  }

  async function testVoiceSynthesis() {
    if (voiceTestBusy) return;
    setVoiceTestBusy(true);
    try {
      const text = voiceTestText.trim();
      if (!text) {
        throw new Error("请先填写测试台词。");
      }
      const effectiveVoiceProvider = activeCharacter?.runtime?.voice_provider || voiceProvider;
      const profile = buildVoiceProfile(effectiveVoiceProvider);
      const speaker = profile.extra?.speaker || profile.voice_id || activeCharacter?.voice_id || "";
      if (effectiveVoiceProvider === "volcengine" && !speaker.trim()) {
        throw new Error("请先填写复刻声音 ID。");
      }
      const speechLanguage = languageCodeForVoiceProvider(effectiveLanguagePlan().speech_language);
      const cacheKey = [activeCharacter?.id || "", effectiveVoiceProvider, speaker, speechLanguage, text].join("\n");
      if (voiceTestCacheRef.current.key === cacheKey && voiceTestCacheRef.current.url) {
        setLastAudioURL(voiceTestCacheRef.current.url);
        playAudio(voiceTestCacheRef.current.url);
        return;
      }
      const payload = await synthesizeVoice({
        provider: effectiveVoiceProvider,
        text,
        plan: {
          voice_id: effectiveVoiceProvider === "volcengine" ? speaker : activeCharacter?.voice_id || "",
          style: "test",
          speed: 1,
          pitch: 1
        },
        emotion: runtimeState.emotion || "calm",
        character: activeCharacter,
        profile
      });
      setRuntimeState((current) => ({
        ...current,
        voice: effectiveVoiceProvider === "volcengine" ? speaker : activeCharacter?.voice_id || "-",
        audio: payload.url ? `${payload.format} · ${Math.round((payload.duration_ms || 0) / 1000)}s` : payload.format || "mock"
      }));
      if (payload.url) {
        voiceTestCacheRef.current = { key: cacheKey, url: payload.url };
        setLastAudioURL(payload.url);
        playAudio(payload.url, { quietAutoplayBlock: true });
      }
      appendLog("info", payload.url ? `测试语音已生成，可再次点击试听：${payload.url}` : `测试语音已生成：${payload.format}`);
    } catch (error) {
      appendLog("error", `测试语音失败：${errorMessage(error, "未知错误")}`);
    } finally {
      setVoiceTestBusy(false);
    }
  }

  function buildImageRequest(baseScene = scene) {
    const moodAsset = activeCharacter?.assets?.moods?.[mood] || {};
    const referenceImageURL = activeCharacter?.assets?.reference_image_url || activeCharacter?.assets?.portrait_url || activeCharacter?.avatar_url || "";
    const backgroundURL = activeCharacter?.assets?.background_url || "";
    const promptParts = [
      "anime visual novel character CG",
      `lesson scene: ${baseScene.title}`,
      activeCharacter?.display_name ? `character: ${activeCharacter.display_name}` : "",
      activeCharacter?.assets?.style_prompt || "",
      activeCharacter?.assets?.cg_prompt || "",
      moodAsset.cg_prompt || "",
      "white management UI atmosphere",
      "dark red accent detail"
    ].filter(Boolean);
    return {
      enabled: cgConfig.enabled,
      endpoint: activeCharacter?.runtime?.image?.endpoint || cgConfig.endpoint,
      workflow: activeCharacter?.runtime?.image?.workflow || parseWorkflow(cgConfig.workflow),
      prompt: activeCharacter?.runtime?.image?.prompt || promptParts.join(", "),
      negative_prompt: activeCharacter?.runtime?.image?.negative_prompt || cgConfig.negative_prompt,
      background_url: backgroundURL,
      reference_image_url: referenceImageURL,
      style: activeCharacter?.runtime?.image?.style || cgConfig.style,
      size: activeCharacter?.runtime?.image?.size || cgConfig.size,
      extra: {
        ...(activeCharacter?.runtime?.image?.extra || {}),
        purpose: "player_driven_teaching_cg",
        character_id: activeCharacter?.id || "",
        character_name: activeCharacter?.display_name || "",
        reference_image_url: referenceImageURL,
        background_url: backgroundURL,
        expression: runtimeState.expression,
        mood
      }
    };
  }

  function buildEffectivePrompt() {
    const expressionRules = buildExpressionRules(activeCharacter);
    const merged = mergePromptConfigs(defaultPrompt, prompt, activeCharacter?.prompt);
    const sceneInstruction = [merged.scene_instruction, generationRequirement.trim() ? `本次生成要求：${generationRequirement.trim()}` : ""]
      .filter(Boolean)
      .join("\n");
    return {
      ...merged,
      scene_instruction: sceneInstruction,
      style_rules: mergeStyleRules(merged.style_rules, expressionRules)
    };
  }

  function buildEffectiveRuntime() {
    const effectiveAgentProvider = activeCharacter?.runtime?.agent_provider || agentProvider;
    const effectiveVoiceProvider = activeCharacter?.runtime?.voice_provider || voiceProvider;
    const effectiveImageProvider = activeCharacter?.runtime?.image_provider || imageProvider;
    const effectiveSceneProvider = activeCharacter?.runtime?.scene_provider || sceneProvider;
    return {
      agent_provider: effectiveAgentProvider,
      voice_provider: effectiveVoiceProvider,
      image_provider: effectiveImageProvider,
      scene_provider: effectiveSceneProvider,
      agent: buildAgentProfile(),
      voice: buildVoiceProfile(effectiveVoiceProvider),
      image: buildImageRequest(scene),
      language: effectiveLanguagePlan()
    };
  }

  function effectiveLanguagePlan() {
    return mergeLanguagePlan(languagePlan, activeCharacter?.runtime?.language);
  }

  function buildAgentProfile() {
    return activeCharacter?.runtime?.agent || {};
  }

  function buildSceneVariables() {
    const variables = {
      source: runtimeMode(),
      active_character_id: activeCharacter?.id || "",
      topic: documentTitle,
      document_title: documentTitle,
      learning_goal: learningGoal,
      generation_requirement: generationRequirement
    };
    if (activeCharacter?.assets?.background_url) {
      variables.background_url = activeCharacter.assets.background_url;
    }
    if (documentURL.trim()) {
      variables.document_url = documentURL.trim();
      variables.source_url = documentURL.trim();
      variables.source_mode = "url";
    }
    if (documentAsset?.path) {
      variables.document_asset_id = documentAsset.id || "";
      variables.document_asset_name = documentAsset.filename || "";
      variables.document_asset_type = documentAsset.content_type || "";
      variables.document_asset_path = documentAsset.path;
      variables.material_file_path = documentAsset.path;
      variables.source_mode = "uploaded_file";
    }
    return variables;
  }

  async function generateLesson() {
    if (generationBusy) return;
    setGenerationBusy(true);
    try {
      const payload = await startSceneGeneration({
        topic: documentTitle,
        document_text: documentText,
        learning_goal: learningGoal,
        generation_requirement: generationRequirement,
        characters: activeCharacters,
        prompt: buildEffectivePrompt(),
        runtime: {
          ...buildEffectiveRuntime(),
          image: buildImageRequest(scene)
        },
        variables: buildSceneVariables()
      });
      const record = payload?.record;
      if (record?.session?.id) {
        upsertSessionRecord(record);
      }
      setRuntimeState((current) => ({
        ...current,
        cg: "生成任务已创建",
        audio: "后台生成中",
        stageWaiting: false
      }));
      appendLog("info", payload?.duplicate
        ? `已有相同生成任务：${record?.scene?.title || record?.session?.id || documentTitle}`
        : `生成任务已创建：${record?.scene?.title || documentTitle}`);
      setActiveView("history");
      refreshSessions({ quiet: true });
    } catch (error) {
      const message = errorMessage(error, "未知错误");
      appendLog("error", `互动剧情生成失败：${message}`);
      setMessages((items) => [...items, { id: messageID("error"), role: "assistant", text: `互动剧情生成失败：${message}`, meta: "error" }]);
    } finally {
      setGenerationBusy(false);
    }
  }

  function buildTurnRequest(text) {
    const now = new Date().toISOString();
    const nextScene = { ...scene, last_active_at: now };
    const nextRelation = { ...relation, updated_at: now };
    setRelation(nextRelation);
    return {
      session: {
        ...(activeSession || {}),
        id: activeSession?.id || `${nextScene.id}:default`,
        user_id: activeSession?.user_id || "default",
        active_character_id: activeSession?.active_character_id || activeCharacter?.id || "",
        participant_ids: activeSession?.participant_ids?.length ? activeSession.participant_ids : activeCharacter?.id ? [activeCharacter.id] : []
      },
      characters: activeCharacters,
      character: activeCharacter,
      scene: nextScene,
      relation: nextRelation,
      prompt: buildEffectivePrompt(),
      runtime: {
        ...buildEffectiveRuntime(),
        image: buildImageRequest(nextScene)
      },
      user: { user_id: "default", text, mode: "text" }
    };
  }

  async function submit(event) {
    event.preventDefault();
    await sendTurn(false);
  }

  async function sendTurn(streaming) {
    const text = input.trim();
    await sendTurnText(text, streaming);
  }

  async function sendChoice(choice) {
    const text = choice?.text || choice?.label || "";
    if (text) {
      setMessages((items) => [...items, { id: messageID("choice"), role: "user", text, meta: "choice" }]);
    }
    const advanced = await advanceLessonWorkflow(choice?.target_node_id || currentWorkflowNode()?.next_node_id || "free-discussion", choice?.id || "");
    if (!advanced) return;
  }

  async function sendTurnText(text, streaming) {
    if (!text || busy) return;
    setInput("");
    setBusy(true);
    setMessages((items) => [...items, { id: messageID("user"), role: "user", text, meta: "you" }]);
    appendLog("info", `发送教学对话：${streaming ? "stream" : "normal"} / ${effectiveProviders.agent} / ${effectiveProviders.voice}`);
    try {
      const request = buildTurnRequest(text);
      if (streaming) {
        await sendStream(request);
        return;
      }
      const payload = await turn(request);
      renderTurn(payload);
      refreshSessions();
    } catch (error) {
      const message = errorMessage(error, "未知错误");
      appendLog("error", `对话失败：${message}`);
      setMessages((items) => [...items, { id: messageID("error"), role: "assistant", text: `后端出错：${message}`, meta: "error" }]);
    } finally {
      setBusy(false);
    }
  }

  async function sendStream(request) {
    const id = messageID("stream");
    setMessages((items) => [...items, { id, role: "assistant", text: "", meta: "streaming", typing: true }]);
    const payload = await streamTurn(request, (token) => {
      setMessages((items) => items.map((item) => item.id === id ? { ...item, text: item.text + token } : item));
    });
    if (payload) renderTurn(payload, { replaceLast: true });
    appendLog("info", "流式教学对话完成。");
    refreshSessions();
  }

  async function exportCurrentWebGAL() {
    if (busy) return;
    setBusy(true);
    try {
      const payload = await exportWebGAL({
        scene,
        characters: activeCharacters,
        
        workflow: lessonWorkflow,
        opening_message: openingMessage || messages.find((message) => message.role === "assistant")?.text || scene.title,
        image: lastCG || buildImageRequest(scene)
      });
      setWebgalExport(payload);
      appendLog("info", `WebGAL 脚本已生成：${payload.entry_file || "start.txt"}`);
    } catch (error) {
      const message = errorMessage(error, "未知错误");
      appendLog("error", `WebGAL 导出失败：${message}`);
      setWebgalExport({ error: message });
    } finally {
      setBusy(false);
    }
  }

  async function copyWebGALScript() {
    if (!webgalExport?.script || !navigator?.clipboard) return;
    await navigator.clipboard.writeText(webgalExport.script);
    appendLog("info", "WebGAL start.txt 已复制。");
  }

  function renderTurn(payload, options = {}) {
    appendAssistantMessage({
      text: payload.display_text || "...",
      speechText: payload.speech_text || "",
      segments: payload.segments || [],
      meta: [payload.emotion, payload.expression, payload.motion].filter(Boolean).join(" · "),
      animate: !options.replaceLast,
      replaceLast: options.replaceLast
    });
    setRuntimeState({
      emotion: payload.emotion || "-",
      expression: payload.expression || "-",
      motion: payload.motion || "-",
      voice: payload.voice?.voice_id || "-",
      audio: payload.audio?.url ? `${payload.audio.format} · ${Math.round((payload.audio.duration_ms || 0) / 1000)}s` : payload.audio?.format || "mock",
      cg: payload.scene_image?.error || payload.scene_image?.url || payload.scene_image?.prompt || runtimeState.cg,
      memories: payload.memory_writes || []
    });
    if (payload.scene_image?.url || payload.scene_image?.prompt || payload.scene_image?.error) setLastCG(payload.scene_image);
    if (payload.audio?.url) {
      setLastAudioURL(payload.audio.url);
      appendLog("info", `语音已生成：${payload.audio.url}`);
      playAudio(payload.audio.url);
    }
  }

  function noteAutoplayBlocked(url) {
    const now = Date.now();
    const last = autoplayBlockRef.current;
    if (last.url !== url || now - last.time > 5000) {
      appendLog("warn", "浏览器拦截了自动播放，音频已保留，请点击重播。");
    }
    autoplayBlockRef.current = { url, time: now };
    setRuntimeState((current) => ({ ...current, audio: "等待点击播放" }));
  }

  function playAudio(url = lastAudioURL, options = {}) {
    if (!url || !audioRef.current) return;
    audioRef.current.src = url;
    audioRef.current.play().catch(() => {
      if (options.quietAutoplayBlock) {
        setRuntimeState((current) => ({ ...current, audio: "等待点击播放" }));
        return;
      }
      noteAutoplayBlocked(url);
    });
  }

  async function playAudioQueue(urls) {
    const queue = urls.filter(Boolean);
    if (!queue.length || !audioRef.current) return;
    for (const url of queue) {
      const audio = audioRef.current;
      audio.src = url;
      try {
        await audio.play();
        await new Promise((resolve) => {
          const finish = () => {
            audio.removeEventListener("ended", finish);
            audio.removeEventListener("error", finish);
            resolve();
          };
          audio.addEventListener("ended", finish);
          audio.addEventListener("error", finish);
        });
      } catch {
        noteAutoplayBlocked(url);
        return;
      }
    }
  }

  function currentWorkflowNode() {
    const nodes = Array.isArray(lessonWorkflow?.nodes) ? lessonWorkflow.nodes : [];
    return nodes.find((node) => node.id === lessonWorkflow.current_node_id) || nodes[0];
  }

  function openStage() {
    if (!hasPlayableWorkflow(lessonWorkflow, scene, activeSession)) {
      appendLog("warn", "当前没有可播放的剧情，请先在主页生成剧情，或从记录中读取一份剧情。");
      setActiveView("director");
      return false;
    }
    setActiveView("stage");
    return true;
  }

  function routeView(view) {
    if (view === "stage") return openStage();
    setActiveView(view);
    return true;
  }

  async function advanceLessonWorkflow(nextNodeID, choiceID = "", replay = false) {
    if (!nextNodeID || busy) return false;
    const currentNode = currentWorkflowNode();
    const sessionID = activeSession?.id || "";
    if (!sessionID || scene.id === "lesson-draft") {
      appendLog("warn", "当前还没有可推进的教学剧情，请先在主页导入材料并生成剧情。");
      setActiveView("dashboard");
      return false;
    }
    setBusy(true);
    try {
      const payload = await advanceWorkflow({
        session_id: sessionID,
        current_node_id: currentNode?.id || "",
        next_node_id: nextNodeID,
        choice_id: choiceID,
        replay
      });
      if (payload.session?.session) setActiveSession(payload.session.session);
      if (payload.session?.scene) setScene(payload.session.scene);
      if (payload.workflow) setLessonWorkflow(payload.workflow);
      if (payload.waiting) {
        setRuntimeState((current) => ({
          ...current,
          stageWaiting: true,
          audio: payload.message || "下一幕准备中"
        }));
        appendLog("info", payload.message || "下一幕仍在准备中。");
        refreshSessions();
        return false;
      }
      if (payload.node?.line || payload.node?.lines?.length) {
        appendWorkflowNodeMessage(payload.node);
        const playedPrepared = await playPreparedWorkflowNodeAudio(payload.node);
        if (!playedPrepared) await playWorkflowNodeVoice(payload.node);
      }
      appendLog("info", `教学阶段已推进：${currentNode?.id || "-"} -> ${payload.node?.id || nextNodeID}`);
      refreshSessions();
      return true;
    } catch (error) {
      appendLog("error", `教学阶段推进失败：${errorMessage(error, "未知错误")}`);
      return false;
    } finally {
      setBusy(false);
    }
  }

  function appendWorkflowNodeMessage(node) {
    const nodeLines = node.lines?.length ? node.lines : null;
    const text = workflowNodeDisplayText(node, true);
    const speechText = workflowNodeSpeechText(node);
    const segments = nodeLines ? nodeLines.map((l) => ({
      text: l.text,
      speech_text: workflowLineSpeechText(l),
      emotion: node.kind || "lesson",
      expression: normalizeMood(l.expression || runtimeState.expression || "soft_smile"),
      motion: "script"
    })) : [{
      text,
      speech_text: speechText,
      emotion: node.kind || "lesson",
      expression: normalizeMood(runtimeState.expression || "soft_smile"),
      motion: node.kind || "script"
    }];
    appendAssistantMessage({
      text,
      speechText,
      segments,
      meta: "script",
      nodeID: node.id,
      audioURL: workflowNodeAudioURLs(node)[0] || "",
      animate: true
    });
  }

  async function playPreparedWorkflowNodeAudio(node) {
    const urls = workflowNodeAudioURLs(node);
    const hasReadyAudio = workflowNodeHasReadyAudio(node);
    if (!hasReadyAudio) return false;
    const expression = normalizeMood(node?.lines?.find((line) => line.expression)?.expression || node?.expression || runtimeState.expression || "soft_smile");
    const label = workflowNodeAudioLabel(node);
    setRuntimeState((current) => ({
      ...current,
      stageWaiting: false,
      expression,
      motion: "script",
      audio: label || current.audio
    }));
    if (!urls.length) {
      appendLog("info", `剧情语音已就绪：${label || "placeholder"}`);
      return true;
    }
    setLastAudioURL(urls[0]);
    appendLog("info", `播放预生成剧情语音：第 1 / ${urls.length} 段`);
    playAudio(urls[0]);
    return true;
  }

  async function playWorkflowNodeVoice(node) {
    const nodeLines = node.lines?.length ? node.lines : null;
    const text = workflowNodeSpeechText(node);
    if (!text) return;
    const displayText = workflowNodeDisplayText(node);
    const canReuseWorkflowAudio = !nodeLines || text === displayText;
    const cachedHistoryAudio = (lessonWorkflow.history || []).find((item) => item.node_id === node.id && item.audio_url)?.audio_url;
    if (cachedHistoryAudio && canReuseWorkflowAudio) {
      setLastAudioURL(cachedHistoryAudio);
      setRuntimeState((current) => ({
        ...current,
        audio: "history cache"
      }));
      playAudio(cachedHistoryAudio);
      appendLog("info", `复用剧情语音：${cachedHistoryAudio}`);
      return;
    }
    if (cachedHistoryAudio && !canReuseWorkflowAudio) {
      appendLog("info", "检测到字幕与发声文本分离，跳过旧剧情语音缓存并重新合成。");
    }
    try {
      const effectiveVoiceProvider = activeCharacter?.runtime?.voice_provider || voiceProvider;
      const profile = buildVoiceProfile(effectiveVoiceProvider);
      const expression = normalizeMood(node.expression || runtimeState.expression || "soft_smile");
      const payload = await synthesizeVoice({
        provider: effectiveVoiceProvider,
        text,
        session_id: activeSession?.id || `${scene.id}:default`,
        workflow_node_id: node.id,
        plan: {
          voice_id: profile.extra?.speaker || profile.voice_id || activeCharacter?.voice_id || "",
          style: expression,
          speed: 1,
          pitch: 1
        },
        emotion: expression,
        character: activeCharacter,
        profile
      });
      setRuntimeState((current) => ({
        ...current,
        expression,
        motion: "script",
        voice: profile.extra?.speaker || profile.voice_id || activeCharacter?.voice_id || current.voice,
        audio: payload.url ? `${payload.format} · ${Math.round((payload.duration_ms || 0) / 1000)}s` : payload.format || current.audio
      }));
      if (payload.url) {
        setLastAudioURL(payload.url);
        setMessages((items) => items.map((message) => message.nodeID === node.id ? { ...message, audioURL: payload.url } : message));
        setLessonWorkflow((current) => ({
          ...current,
          history: (current.history || []).map((item) => item.node_id === node.id ? {
            ...item,
            audio_url: payload.url,
            audio_format: payload.format,
            audio_cached: Boolean(payload.cached)
          } : item)
        }));
        appendLog("info", `剧情语音已生成：${payload.url}`);
        playAudio(payload.url);
      } else {
        appendLog("info", `剧情语音已处理：${payload.format || "placeholder"}`);
      }
    } catch (error) {
      appendLog("warn", `剧情语音生成失败：${errorMessage(error, "未知错误")}`);
    }
  }

  function upsertSessionRecord(record) {
    if (!record?.session?.id) return;
    setSessions((items) => {
      const next = Array.isArray(items) ? [...items] : [];
      const index = next.findIndex((item) => item?.session?.id === record.session.id);
      if (index >= 0) next[index] = record;
      else next.unshift(record);
      return next.sort((a, b) => new Date(b.updated_at || 0) - new Date(a.updated_at || 0));
    });
  }

  function removeSessionRecord(sessionID) {
    if (!sessionID) return;
    setSessions((items) => (Array.isArray(items) ? items.filter((item) => item?.session?.id !== sessionID) : []));
  }

  async function refreshSessions(options = {}) {
    try {
      const payload = await getSessions();
      setSessions(payload.sessions || []);
      if (!options.quiet) {
        appendLog("info", `会话历史已刷新：${payload.sessions?.length || 0} 条。`);
      }
    } catch (error) {
      appendLog("error", `会话历史刷新失败：${errorMessage(error, "未知错误")}`);
    }
  }

  async function syncActiveSession(sessionID) {
    if (!sessionID) return;
    try {
      const record = await getSession(sessionID);
      if (!record?.session || !record?.workflow) return;
      upsertSessionRecord(record);
      setActiveSession(record.session);
      setScene(record.scene || scene);
      setRelation(record.relation || defaultRelation);
      setLessonWorkflow((current) => normalizeTeachingWorkflow(record.workflow, record.scene || scene, current));
      const waiting = stageWorkflowWaiting(record.workflow);
      setRuntimeState((current) => ({
        ...current,
        stageWaiting: waiting,
        audio: waiting ? "下一幕准备中..." : current.audio
      }));
    } catch (error) {
      appendLog("warn", `会话轮询失败：${errorMessage(error, "未知错误")}`);
    }
  }

  function restoreSession(record) {
    if (!record?.session || !record?.scene) return;
    if (recordGenerationStatus(record) !== "ready") {
      appendLog("warn", `记录尚不可演出：${historyStatusLabel(recordGenerationStatus(record))}`);
      return;
    }
    setWebgalExport(null);
    setActiveSession(record.session);
    setScene(record.scene);
    setLessonWorkflow(normalizeTeachingWorkflow(record.workflow, record.scene));
    setRelation(record.relation || defaultRelation);
    if (record.characters?.length) setCharacters((items) => mergeSessionCharacters(items, record.characters));
    if (record.session.active_character_id) setActiveCharacterID(record.session.active_character_id);
    if (record.teaching?.topic || record.scene?.variables?.topic) setDocumentTitle(record.teaching?.topic || record.scene.variables.topic);
    if (record.teaching?.learning_goal || record.scene?.variables?.learning_goal) setLearningGoal(record.teaching?.learning_goal || record.scene.variables.learning_goal);
    if (record.teaching?.document_text) setDocumentText(record.teaching.document_text);
    if (record.teaching?.prompt) setPrompt(record.teaching.prompt);
    const restoredMessages = record.messages?.map((message) => ({
      role: message.role === "user" ? "user" : "assistant",
      text: message.display_text || message.text,
      speechText: message.speech_text || "",
      segments: message.segments || [],
      meta: message.scene_image_error || message.emotion || message.motion || "history",
      audioURL: message.audio_url || "",
      id: message.id || messageID("history")
    }));
    setMessages(restoredMessages?.length ? restoredMessages : [{ id: messageID("restored"), role: "assistant", text: "这个教学场景已经恢复，可以继续对话。", meta: "restored" }]);
    setOpeningMessage(restoredMessages?.find((message) => message.role === "assistant")?.text || "");
    const latest = record.messages?.at(-1);
    const currentNodeAudioURL = record.workflow?.history?.find((item) => item.node_id === record.workflow?.current_node_id && item.audio_url)?.audio_url || "";
    const latestAudioURL = latest?.audio_url || currentNodeAudioURL;
    if (latestAudioURL) setLastAudioURL(latestAudioURL);
    setLastCG(latest?.scene_image_url || latest?.scene_image_prompt || latest?.scene_image_error ? {
      url: latest.scene_image_url,
      prompt: latest.scene_image_prompt,
      error: latest.scene_image_error
    } : null);
    setRuntimeState((current) => ({
      ...current,
      emotion: latest?.emotion || current.emotion,
      expression: latest?.expression || current.expression,
      motion: latest?.motion || current.motion,
      stageWaiting: stageWorkflowWaiting(record.workflow),
      audio: latestAudioURL ? "history cache" : current.audio,
      cg: latest?.scene_image_error || latest?.scene_image_url || "等待新一轮生成"
    }));
    appendLog("info", `已恢复会话：${record.scene.title || record.session.id}`);
    setActiveView(hasPlayableWorkflow(record.workflow, record.scene, record.session) ? "stage" : "director");
  }

  async function refreshProviderHealth() {
    try {
      const payload = await getProviderHealth();
      setProviderHealth(payload.providers || []);
      appendLog("info", `Provider 状态已检测：${payload.providers?.length || 0} 项。`);
    } catch (error) {
      appendLog("error", `Provider 状态检测失败：${errorMessage(error, "未知错误")}`);
    }
  }

  async function refreshPlugins() {
    try {
      const payload = await getPlugins();
      setPluginCatalog(payload);
      appendLog("info", `插件清单已加载：${payload.manifests?.length || 0} 个 manifest。`);
    } catch (error) {
      appendLog("error", `插件清单加载失败：${errorMessage(error, "未知错误")}`);
    }
  }

  const renderedView = activeView === "stage" && !canEnterStage ? "director" : activeView;
  const shellView = shellViewMeta(renderedView, { busy, logs: logs.length, sessions: sessions.length });
  const isDesktopRuntime = runtimeMode() === "desktop";

  return (
    <main className={`app-frame ${renderedView === "stage" ? "is-stage" : ""} ${isDesktopRuntime ? "is-desktop-runtime" : ""}`}>
      {renderedView === "stage" ? (
        <StageErrorBoundary resetKey={`${activeSession?.id || "draft"}:${lessonWorkflow?.current_node_id || ""}`} onBack={() => { setActiveView("history"); refreshSessions(); }}>
          <GalgameStageView
            activeCharacter={activeCharacter}
            activeCharacterID={activeCharacterID}
            busy={busy}
            input={input}
            isTyping={messages.some((m) => m.typing)}
            lastCG={lastCG}
            lessonWorkflow={lessonWorkflow}
            messages={messages}
            mood={mood}
            playAudio={playAudio}
            playWorkflowNodeVoice={playWorkflowNodeVoice}
            providerLine={`${activeCharacter?.display_name || "角色"} · ${effectiveProviders.agent} / ${effectiveProviders.voice} / ${effectiveProviders.image}`}
            runtimeState={runtimeState}
            scene={scene}
            advanceLessonWorkflow={advanceLessonWorkflow}
            sendChoice={sendChoice}
            sendTurn={sendTurn}
            setInput={setInput}
            speaking={speaking}
            submit={submit}
            onOpenHome={() => setActiveView("director")}
            onOpenHistory={() => { setActiveView("history"); refreshSessions(); }}
            onOpenConfig={() => setActiveView("config")}
            onOpenLogs={() => setActiveView("logs")}
          />
        </StageErrorBoundary>
      ) : (
        <section className="fairy-desktop">
          <section className="fairy-window">
            <header className="fairy-titlebar">
              <div className="traffic-lights" aria-hidden="true" />
              <div className="window-title"><b>FAIRY</b><span>{shellView.title}</span></div>
              <div className="top-actions">
                <button className="ghost-button" type="button" onClick={() => { setActiveView("history"); refreshSessions(); }}>历史</button>
                <button className="primary-button" type="button" onClick={() => canEnterStage ? openStage() : setActiveView("dashboard")} disabled={busy}>
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M5 3l14 9-14 9V3z" /></svg>
                  {canEnterStage ? "开始演出" : "生成剧情"}
                </button>
              </div>
            </header>
            <div className="fairy-shell">
              <nav className="side-nav" aria-label="主导航">
                <div className="side-nav__brand">
                  <span className={`brand-mark ${busy ? "is-busy" : ""}`}>{BRAND_STAR}</span>
                  <div>
                    <strong>FAIRY</strong>
                    <small>STORY ENGINE</small>
                  </div>
                </div>
                <div className="side-nav__items">
                  <NavButton active={renderedView === "dashboard"} icon={NAV_ICONS.dashboard} label="新建情节" onClick={() => setActiveView("dashboard")} />
                  <NavButton active={renderedView === "history"} icon={NAV_ICONS.history} label="历史记录" onClick={() => { setActiveView("history"); refreshSessions(); }} />
                  <NavButton active={renderedView === "director" || renderedView === "stage"} icon={NAV_ICONS.stage} label="剧情演出" onClick={() => setActiveView("director")} />
                  <NavButton active={renderedView === "config"} icon={NAV_ICONS.config} label="角色档案" onClick={() => setActiveView("config")} />
                  <NavButton active={renderedView === "logs"} icon={NAV_ICONS.logs} label="制作日志" onClick={() => setActiveView("logs")} />
                </div>
                <div className="rail-note">
                  <strong>{shellView.noteTitle}</strong>
                  <p>{shellView.note}</p>
                </div>
              </nav>
              <section className="workspace">
        {renderedView === "dashboard" && (
          <HomeView
            activeCharacterID={activeCharacterID}
            activeCharacter={activeCharacter}
            busy={busy}
            characters={characters}
            documentAsset={documentAsset}
            documentImport={documentImport}
            documentStats={documentStats}
            documentText={documentText}
            documentTitle={documentTitle}
            documentURL={documentURL}
            effectiveProviders={effectiveProviders}
            generationBusy={generationBusy}
            generateLesson={generateLesson}
            importDocumentFile={importDocumentFile}
            importDocumentURL={importDocumentURL}
            lastAudioURL={lastAudioURL}
            lastCG={lastCG}
            learningGoal={learningGoal}
            generationRequirement={generationRequirement}
            logs={logs}
            pluginCatalog={pluginCatalog}
            providerHealth={providerHealth}
            refreshPlugins={refreshPlugins}
            refreshProviderHealth={refreshProviderHealth}
            refreshSessions={refreshSessions}
            runtimeState={runtimeState}
            scene={scene}
            setActiveCharacterID={setActiveCharacterID}
            setDocumentText={setDocumentText}
            setDocumentTitle={setDocumentTitle}
            setDocumentURL={setDocumentURL}
            setLearningGoal={setLearningGoal}
            setGenerationRequirement={setGenerationRequirement}
            setActiveView={routeView}
            sessions={sessions}
          />
        )}
        {renderedView === "history" && (
          <HistoryView
            appendLog={appendLog}
            busy={busy}
            refreshSessions={refreshSessions}
            removeSessionRecord={removeSessionRecord}
            restoreSession={restoreSession}
            sessions={sessions}
            setActiveView={routeView}
          />
        )}
        {renderedView === "director" && (
          <DirectorView
            activeCharacter={activeCharacter}
            activeCharacterID={activeCharacterID}
            busy={busy}
            documentAsset={documentAsset}
            documentTitle={documentTitle}
            effectiveProviders={effectiveProviders}
            lastAudioURL={lastAudioURL}
            lastCG={lastCG}
            lessonWorkflow={lessonWorkflow}
            logs={logs}
            messages={messages}
            mood={mood}
            playAudio={playAudio}
            providerLine={`${activeCharacter?.display_name || "角色"} · ${effectiveProviders.agent} / ${effectiveProviders.voice} / ${effectiveProviders.image}`}
            runtimeState={runtimeState}
            scene={scene}
            sessions={sessions}
            advanceLessonWorkflow={advanceLessonWorkflow}
            playWorkflowNodeVoice={playWorkflowNodeVoice}
            sendChoice={sendChoice}
            setActiveView={routeView}
            speaking={speaking}
          />
        )}
        {renderedView === "config" && (
          <ConfigView
            activeCharacterID={activeCharacterID}
            cgConfig={cgConfig}
            characters={characters}
            cloneBusy={cloneBusy}
            configStatus={configStatus}
            configTab={configTab}
            languagePlan={languagePlan}
            pluginCatalog={pluginCatalog}
            prompt={prompt}
            providerHealth={providerHealth}
            providerOptions={providerOptions}
            refreshPlugins={refreshPlugins}
            refreshProviderHealth={refreshProviderHealth}
            refreshSessions={refreshSessions}
            refreshVoiceCloneStatus={refreshVoiceCloneStatus}
            runtimeState={runtimeState}
            sessions={sessions}
            exportCharacterPackage={exportCharacterPackage}
            importCharacterPackageFile={importCharacterPackageFile}
            setActiveCharacterID={setActiveCharacterID}
            setCgConfig={setCgConfig}
            setCharacterAsset={setCharacterAsset}
            setCharacterBackgroundAsset={setCharacterBackgroundAsset}
            setCharacterField={setCharacterField}
            addCharacterMoodAsset={addCharacterMoodAsset}
            removeCharacterMoodAsset={removeCharacterMoodAsset}
            renameCharacterMoodAsset={renameCharacterMoodAsset}
            setCharacterMoodAsset={setCharacterMoodAsset}
            setCharacterPromptField={setCharacterPromptField}
            setCharacterRuntimeAgentField={setCharacterRuntimeAgentField}
            setCharacterRuntimeField={setCharacterRuntimeField}
            setCharacterRuntimeImageField={setCharacterRuntimeImageField}
            setCharacterRuntimeLanguageField={setCharacterRuntimeLanguageField}
            setCharacterRuntimeVoiceExtraField={setCharacterRuntimeVoiceExtraField}
            setCharacterRuntimeVoiceField={setCharacterRuntimeVoiceField}
            setConfigTab={setConfigTab}
            setActiveView={routeView}
            setLanguagePlan={setLanguagePlan}
            setPrompt={setPrompt}
            setVoiceCloneFiles={setVoiceCloneFiles}
            startVoiceClone={startVoiceClone}
            testVoiceSynthesis={testVoiceSynthesis}
            voiceClone={voiceClone}
            voiceCloneFiles={voiceCloneFiles}
            voiceTestBusy={voiceTestBusy}
            voiceTestText={voiceTestText}
            saveRoleConfig={saveRoleConfig}
            setVoiceTestText={setVoiceTestText}
          />
        )}
        {renderedView === "logs" && <LogsView logs={logs} setActiveView={routeView} setLogs={setLogs} documentTitle={documentTitle} activeCharacter={activeCharacter} lessonWorkflow={lessonWorkflow} effectiveProviders={effectiveProviders} />}
              </section>
            </div>
          </section>
        </section>
      )}

      <audio ref={audioRef} onPlay={() => setSpeaking(true)} onPause={() => setSpeaking(false)} onEnded={() => setSpeaking(false)} />
    </main>
  );
}

function shellViewMeta(activeView, counters = {}) {
  if (activeView === "history") {
    return {
      title: "历史记录",
      noteTitle: "最近恢复",
      note: counters.sessions ? `当前有 ${counters.sessions} 份生成档案，可以从历史记录恢复演出。` : "生成情节后，这里会出现可回溯的脚本记录。"
    };
  }
  if (activeView === "config") {
    return {
      title: "角色档案",
      noteTitle: "当前绑定",
      note: "角色保存后，新建情节会使用该角色 Prompt、Provider、语音和素材资源。"
    };
  }
  if (activeView === "logs") {
    return {
      title: "制作日志",
      noteTitle: "当前任务",
      note: counters.busy ? "剧情或语音任务正在运行，日志会持续追加。" : `当前累计 ${counters.logs || 0} 条制作记录。`
    };
  }
  if (activeView === "director") {
    return {
      title: "剧情预览",
      noteTitle: "演出前确认",
      note: "确认当前章节、角色和素材状态后，可以进入沉浸式剧情演出。"
    };
  }
  return {
    title: "文档 → Galgame 创作",
    noteTitle: "当前模式",
    note: "首页只处理文档、生成目标与主讲角色；演出会在剧情生成后开启。"
  };
}

function HomeView({
  activeCharacterID,
  activeCharacter,
  busy,
  characters,
  documentAsset,
  documentImport,
  documentStats,
  documentText,
  documentTitle,
  documentURL,
  effectiveProviders,
  generationBusy,
  generationRequirement,
  generateLesson,
  importDocumentFile,
  importDocumentURL,
  lastAudioURL,
  lastCG,
  learningGoal,
  logs,
  pluginCatalog,
  providerHealth,
  refreshPlugins,
  refreshProviderHealth,
  refreshSessions,
  runtimeState,
  scene,
  setActiveCharacterID,
  setDocumentText,
  setDocumentTitle,
  setDocumentURL,
  setGenerationRequirement,
  setLearningGoal,
  setActiveView,
  sessions
}) {
  const [homeStep, setHomeStep] = useState("source");
  const [sourceMethod, setSourceMethod] = useState(documentURL ? "url" : documentText ? "text" : "upload");
  const homePageRef = useRef(null);
  const recentSessions = [...sessions].sort((a, b) => new Date(b.updated_at || 0) - new Date(a.updated_at || 0)).slice(0, 5);
  const audioCacheCount = sessions.reduce((total, record) => total + countHistoryAudio(record), 0);
  const healthyProviders = providerHealth.filter((item) => item.status === "ok" || item.status === "ready").length;
  const portraitURL = activeCharacter?.assets?.portrait_url || activeCharacter?.avatar_url || "";
  const sourceReady = Boolean(documentAsset?.path || documentText.trim() || documentURL.trim());
  const docLabel = documentAsset?.filename || (documentURL.trim() ? "网络文档" : documentText.trim() ? "粘贴正文" : "尚未选择文档");
  useEffect(() => {
    homePageRef.current?.scrollTo({ top: 0 });
  }, [homeStep]);
  return (
    <section className="page home-page" ref={homePageRef}>
      <section className="home-layout">
        <div className="home-main">
          <header className="hero-block">
            <span className="eyebrow">{homeStep === "source" ? "文档 · 单角色视觉小说教学" : "第 2 步 · 生成目标"}</span>
            <h1>放进一份文档，<br />生成一段可玩的 <span>Galgame</span>。</h1>
          </header>

          <article className="intake-panel">
            <div className="intake-head">
              <div className="segmented-tabs">
                <button className={homeStep === "source" ? "is-active" : ""} type="button" onClick={() => setHomeStep("source")}>文档源</button>
                <button className={homeStep === "target" ? "is-active" : ""} type="button" onClick={() => setHomeStep("target")}>生成目标</button>
              </div>
              <span className="doc-support">{homeStep === "source" ? "支持 PDF · DOCX · PPT · Markdown · URL · 目录" : `当前文档：${docLabel} · ${documentStats.chars} 字符`}</span>
            </div>

            <div className="intake-body">
              {homeStep === "source" ? (
                <>
                  <div className="source-methods">
                    <button className={`source-method ${sourceMethod === "upload" ? "is-active" : ""}`} type="button" onClick={() => setSourceMethod("upload")}><b>上传文件</b><span>PDF、PPT、DOCX、图片笔记</span></button>
                    <button className={`source-method ${sourceMethod === "text" ? "is-active" : ""}`} type="button" onClick={() => setSourceMethod("text")}><b>粘贴正文</b><span>文章、课件、脚本</span></button>
                    <button className={`source-method ${sourceMethod === "url" ? "is-active" : ""}`} type="button" onClick={() => setSourceMethod("url")}><b>导入链接</b><span>网页正文与结构</span></button>
                    <button className={`source-method ${sourceMethod === "directory" ? "is-active" : ""}`} type="button" onClick={() => setSourceMethod("directory")}><b>本地目录</b><span>代码仓库、知识库</span></button>
                  </div>

                  {sourceMethod === "upload" && (
                    <label
                      className={`document-drop ${documentAsset?.path ? "has-file" : ""}`}
                      onDragOver={(event) => event.preventDefault()}
                      onDrop={(event) => {
                        event.preventDefault();
                        importDocumentFile(event.dataTransfer.files?.[0]);
                      }}
                    >
                      <input type="file" onChange={(event) => importDocumentFile(event.target.files?.[0])} />
                      <strong>{documentAsset?.filename || "把文档拖到这里"}</strong>
                      <span>{documentAsset?.path ? `${documentAsset.content_type || "file"} · ${formatBytes(documentAsset.size_bytes || 0)}` : "松手后 FAIRY 会先整理结构与重点，再带你设定这段剧情的学习目标。"}</span>
                      <em>选择文件</em>
                    </label>
                  )}

                  {sourceMethod === "text" && (
                    <div className="source-editor">
                      <TextAreaField label="材料正文" value={documentText} onChange={setDocumentText} rows={9} placeholder="粘贴文章、课件、脚本或笔记正文。" />
                    </div>
                  )}

                  {sourceMethod === "url" && (
                    <div className="source-editor">
                      <div className="document-url-row">
                        <TextField label="网络文档 URL" value={documentURL} onChange={setDocumentURL} placeholder="https://example.com/doc 或飞书文档链接" />
                        <button className="ghost-button" type="button" onClick={importDocumentURL} disabled={busy || !documentURL.trim()}>导入</button>
                      </div>
                      <p className="source-hint">链接内容会作为材料入口交给 Agent；复杂网页、飞书文档或图片型资料后续由 Agent 工具链理解。</p>
                    </div>
                  )}

                  {sourceMethod === "directory" && (
                    <div className="source-editor">
                      <TextAreaField label="本地目录 / Agent 指令" value={documentText} onChange={setDocumentText} rows={8} placeholder="例如：请读取 /Users/rinai/project/demo 下的 README、docs 和核心代码，整理成教学剧情。" />
                      <p className="source-hint">目录模式先用文本指令表达，后续接入本地目录选择和 Agent 工具调用。</p>
                    </div>
                  )}
                </>
              ) : (
                <div className="target-form">
                  <TextField label="文档标题" value={documentTitle} onChange={setDocumentTitle} />
                  <TextAreaField label="学习目标" value={learningGoal} onChange={setLearningGoal} rows={4} placeholder="这段 Galgame 要讲清什么？例如：让玩家理解文档核心结构、关键概念和使用边界。" />
                  <TextAreaField label="生成要求" value={generationRequirement} onChange={setGenerationRequirement} rows={6} placeholder="写给剧情 Agent 的补充要求，例如：面向刚接触项目的玩家；先讲清主流程，再解释关键术语；控制在 5-7 个剧情节点；只让主讲角色发言。" />
                </div>
              )}
            </div>

            <div className={`generate-status generate-status--${documentImport.status}`}>
              <span>{homeStep === "source" ? documentImport.message : sourceReady ? `文档源已就绪：${docLabel}` : "还没有文档源，仍可以先填写目标，但建议先导入材料。"}</span>
              <strong>{documentStats.chars} 字符 · {documentStats.paragraphs} 段</strong>
            </div>

            <footer className="intake-foot">
              <div className="provider-dots">
                <span><i />剧情 Agent</span>
                <span><i />模型服务</span>
                <span><i className="is-idle" />语音服务</span>
                <span><i className="is-idle" />素材服务</span>
              </div>
              <button className="primary-button" type="button" onClick={homeStep === "source" ? () => setHomeStep("target") : generateLesson} disabled={generationBusy || (homeStep === "target" && !sourceReady)}>
                {generationBusy ? "创建任务..." : homeStep === "source" ? "继续到生成目标" : "生成章节"}
              </button>
            </footer>
          </article>
        </div>

        <aside className="cast-panel">
          <div className="cast-head">
            <div className="cast-title">
              <small>NOW STARRING</small>
              <b>{activeCharacter?.display_name || "主讲角色"}</b>
            </div>
            <span className="cast-ready">
              <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M5 12l5 5 9-11" /></svg>
              就绪
            </span>
          </div>
          <div className="cast-halo" aria-hidden="true" />
          {portraitURL ? (
            <img className="cast-heroine" src={portraitURL} alt={activeCharacter?.display_name || "主讲角色"} />
          ) : (
            <div className="cast-empty"><span>未绑定立绘</span></div>
          )}
          <div className="cast-selector" aria-label="选择生成角色">
            {characters.map((character) => {
              const portrait = character?.assets?.portrait_url || character?.avatar_url || "";
              const selected = character.id === activeCharacterID;
              return (
                <button
                  key={character.id}
                  type="button"
                  className={`cast-option ${selected ? "is-active" : ""}`}
                  onClick={() => setActiveCharacterID(character.id)}
                  disabled={generationBusy}
                  title={character.display_name || character.id}
                >
                  <span style={portrait ? { backgroundImage: `url("${portrait}")` } : undefined}>{portrait ? "" : (character.display_name || character.id || "?").slice(0, 1)}</span>
                  <b>{character.display_name || character.id}</b>
                </button>
              );
            })}
          </div>
          <div className="cast-bubble">
            <div className="cast-bubble__who">
              <span className="cast-bubble__pin" aria-hidden="true" />
              <b>{activeCharacter?.display_name || "主讲角色"}</b>
              <em>单角色讲解 · 追问 · 反馈</em>
            </div>
            <p>{activeCharacter?.persona || "把文档交给我吧。我会把它讲成一段你可以走进去、能随时提问的故事。"}</p>
          </div>
        </aside>
      </section>
    </section>
  );
}

function HistoryView(props) {
  const {
    appendLog, busy, refreshSessions, removeSessionRecord, restoreSession, sessions, setActiveView
  } = props;
  const [selectedSessionID, setSelectedSessionID] = useState("");
  const [deleteError, setDeleteError] = useState("");
  const [confirmDeleteID, setConfirmDeleteID] = useState("");
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState("all");
  const [deletingIDs, setDeletingIDs] = useState(new Set());
  const sortedSessions = [...sessions].sort((a, b) => new Date(b.updated_at || 0) - new Date(a.updated_at || 0));
  const visibleSessions = sortedSessions.filter((record) => {
    const status = recordGenerationStatus(record);
    if (statusFilter === "generated" && status !== "ready") return false;
    if (statusFilter === "draft" && status !== "draft") return false;
    if (statusFilter === "generating" && status !== "generating") return false;
    if (statusFilter === "failed" && status !== "failed") return false;
    if (!query.trim()) return true;
    const q = query.trim().toLowerCase();
    return [sessionTitle(record), sessionGoal(record), sessionSource(record)].some((text) => (text || "").toLowerCase().includes(q));
  });
  const selectedRecord = visibleSessions.find((record) => record.session.id === selectedSessionID) || visibleSessions[0];

  useEffect(() => {
    if (!confirmDeleteID) return;
    if (!sessions.some((record) => record?.session?.id === confirmDeleteID)) setConfirmDeleteID("");
  }, [confirmDeleteID, sessions]);

  function requestDelete(record) {
    const sessionID = record?.session?.id || "";
    if (!sessionID || deletingIDs.has(sessionID)) return;
    setDeleteError("");
    setConfirmDeleteID(sessionID);
  }

  async function handleDelete(record) {
    const sessionID = record?.session?.id || "";
    if (!sessionID || deletingIDs.has(sessionID)) return;
    setDeleteError("");
    setConfirmDeleteID("");
    setDeletingIDs((prev) => new Set(prev).add(sessionID));
    try {
      await deleteSession(sessionID);
      if (selectedSessionID === sessionID) setSelectedSessionID("");
      removeSessionRecord?.(sessionID);
      appendLog?.("info", `已删除记录：${sessionTitle(record)}`);
      await refreshSessions({ quiet: true });
    } catch (e) {
      const message = `删除记录失败：${e.message}`;
      setDeleteError(message);
      appendLog?.("error", message);
    } finally {
      setDeletingIDs((prev) => { const next = new Set(prev); next.delete(sessionID); return next; });
    }
  }
  const pills = [{ id: "all", label: "全部" }, { id: "generated", label: "已生成" }, { id: "generating", label: "生成中" }, { id: "failed", label: "失败" }, { id: "draft", label: "草稿" }];
  const sel = selectedRecord;
  const selStatus = sel ? recordGenerationStatus(sel) : "draft";
  const selGenerated = selStatus === "ready";
  const selCharacter = sel ? sessionCharacter(sel) : null;
  const selPortrait = selCharacter?.assets?.portrait_url || selCharacter?.avatar_url || "";
  const selNodes = sel?.workflow?.nodes || [];

  if (!sortedSessions.length) {
    return (
      <section className="page hist2-page">
        <section className="empty-history save-empty">
          <span className="eyebrow">暂无记录</span>
          <h2>还没有学习历史</h2>
          <p>从“新建”导入一份材料并生成教学剧情后，这里会保留可回溯的会话记录。</p>
          <button className="primary-button" type="button" onClick={() => setActiveView("dashboard")}>去主页新建</button>
        </section>
      </section>
    );
  }

  return (
    <section className="page hist2-page">
      {deleteError ? <div className="inline-error">{deleteError}</div> : null}
      <div className="hist2">
        <section className="hist2-stage">
          <div className="ph">
            <div>
              <h2>生成档案</h2>
              <p>按文档、角色、状态和生成日期检索历史脚本。</p>
            </div>
            <button className="h-btn ghost" type="button" onClick={refreshSessions} disabled={busy}>刷新</button>
          </div>

          <div className="filters">
            <label className="search">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><circle cx="11" cy="11" r="7" /><path d="M21 21l-4-4" /></svg>
              <input type="search" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索文档名、学习目标或章节标题" />
            </label>
            <div className="pills">
              {pills.map((pill) => (
                <button key={pill.id} type="button" className={statusFilter === pill.id ? "on" : ""} onClick={() => setStatusFilter(pill.id)}>{pill.label}</button>
              ))}
            </div>
          </div>

          <article className="h-table">
            <div className="cap"><b>历史记录</b><span>共 {sortedSessions.length} 条记录 · 点击记录在右侧查看详情</span></div>
            <div className="row head"><span>编号</span><span>标题</span><span>文档源</span><span>保存时间</span><span>状态</span></div>
            <div className="rows">
              {visibleSessions.length ? visibleSessions.map((record, index) => {
                const status = recordGenerationStatus(record);
                const on = sel?.session.id === record.session.id;
                const deleting = deletingIDs.has(record.session.id);
                return (
                  <div className={`row item ${on ? "on" : ""} ${deleting ? "is-deleting" : ""}`} key={record.session.id} onClick={() => setSelectedSessionID(record.session.id)}>
                    <span className="no">{String(index + 1).padStart(2, "0")}</span>
                    <div className="ttl"><b>{sessionTitle(record)}</b><span>{sessionGoal(record)}</span></div>
                    <span className="src">{sessionSource(record)}</span>
                    <span className="time">{formatHistoryTime(record.updated_at)}</span>
                    <span className={`h-chip ${historyStatusClass(status)} ${deleting ? "is-deleting" : ""}`}>{deleting ? "删除中" : historyStatusLabel(status)}</span>
                  </div>
                );
              }) : (
                <div className="h-empty">没有匹配的记录。试试调整搜索词或筛选条件。</div>
              )}
            </div>
            <div className="pager"><span>共 {sortedSessions.length} 条 · 当前显示 {visibleSessions.length} 条</span></div>
          </article>
        </section>

        <aside className="hist2-insp">
          {sel ? (
            <>
              <div className="h-panel">
                <h4>选中记录</h4>
                <div className="h-name">{sessionTitle(sel)}</div>
                <p className="h-desc">{historyStatusDescription(selStatus)}</p>
                <div className="h-kv-list">
                  <div className="kv"><span>文档源</span><b>{sessionSource(sel)}</b></div>
                  <div className="kv"><span>章节</span><b>{selNodes.length} 个章节</b></div>
                  <div className="kv"><span>状态</span><b style={{ color: historyStatusColor(selStatus) }}>{historyStatusLabel(selStatus)}</b></div>
                  <div className="kv"><span>保存时间</span><b>{formatHistoryTime(sel.updated_at)}</b></div>
                </div>
                <div className="insp-actions">
                  <button className="h-btn primary sm" type="button" onClick={() => restoreSession(sel)} disabled={!selGenerated || busy}>进入演出</button>
                  <button className="h-btn danger sm" type="button" onClick={() => requestDelete(sel)} disabled={deletingIDs.has(sel.session.id)}>
                    {deletingIDs.has(sel.session.id) ? "删除中" : "删除记录"}
                  </button>
                </div>
                {confirmDeleteID === sel.session.id ? (
                  <div className="h-delete-confirm" role="alertdialog" aria-live="polite" aria-label="确认删除历史记录">
                    <div>
                      <b>删除这条历史记录？</b>
                      <p>这会从本机历史中移除该记录。</p>
                    </div>
                    <div className="h-delete-confirm__actions">
                      <button className="h-btn ghost sm" type="button" onClick={() => setConfirmDeleteID("")}>取消</button>
                      <button className="h-btn danger sm" type="button" onClick={() => handleDelete(sel)} disabled={deletingIDs.has(sel.session.id)}>确认删除</button>
                    </div>
                  </div>
                ) : null}
              </div>

              <div className="h-panel">
                <h4>主讲角色</h4>
                <div className="speaker">
                  <span className="face" style={selPortrait ? { backgroundImage: `url("${selPortrait}")` } : undefined} />
                  <div><b>{selCharacter?.display_name || "主讲角色"}</b><span>单角色讲解 / 追问 / 反馈</span></div>
                </div>
              </div>

              {selNodes.length ? (
                <div className="h-panel">
                  <h4>章节快照</h4>
                  {selNodes.slice(0, 6).map((node, index) => (
                    <div className="snap" key={node.id || index}>
                      <span className="i">{index + 1}</span>
                      <div>
                        <b>{cleanHistoryText(node.title || formatProgressLabel(node.kind))}</b>
                        <p>{cleanHistoryText(node.summary || formatProgressLabel(node.kind))}</p>
                      </div>
                    </div>
                  ))}
                </div>
              ) : null}
            </>
          ) : (
            <div className="h-panel"><h4>选中记录</h4><p className="h-desc">从左侧选择一条记录查看详情。</p></div>
          )}
        </aside>
      </div>
    </section>
  );
}

function SavePreview({ record, restoreSession }) {
  if (!record) return null;
  const title = sessionTitle(record);
  const goal = sessionGoal(record);
  const nodeCount = record.workflow?.nodes?.length || 0;
  const visitedCount = record.workflow?.history?.length || 0;
  const audioCount = countHistoryAudio(record);
  const lastMessage = [...(record.messages || [])].reverse().find((message) => message?.text);
  const character = sessionCharacter(record);
  const backgroundURL = character?.assets?.background_url || record.scene?.variables?.background_url || "";
  const portraitURL = character?.assets?.portrait_url || character?.avatar_url || "";
  const speaker = lastMessage?.role === "user" ? "你" : character?.display_name || lastMessage?.character_id || "角色";
  return (
    <aside className="save-preview-panel">
      <span className="eyebrow">记录预览</span>
      <h2>{title}</h2>
      <p>{goal}</p>
      <div className="save-preview-stage" style={stagePreviewBackground(backgroundURL)}>
        <div>
          <span>{character?.display_name || "讲解角色"}</span>
          <strong>{formatProgressLabel(record.workflow?.current_node_id || record.scene?.phase)}</strong>
        </div>
        {portraitURL ? <img src={portraitURL} alt={character?.display_name || "角色立绘"} /> : <b>{(character?.display_name || "角色").slice(0, 2)}</b>}
      </div>
      <div className="history-card__progress" aria-label="教学进度">
        <span style={{ width: `${progressPercent(visitedCount, nodeCount)}%` }} />
      </div>
      <div className="save-preview-stats">
        <StateRow label="对话" value={`${record.messages?.length || 0} 条`} />
        <StateRow label="进度" value={nodeCount ? `${visitedCount}/${nodeCount}` : String(visitedCount)} />
        <StateRow label="语音" value={`${audioCount} 段`} />
        <StateRow label="更新" value={formatHistoryTime(record.updated_at)} />
      </div>
      <div className="save-preview-quote">
        <strong>{speaker}</strong>
        <span>{cleanHistoryText(lastMessage?.text || "还没有可预览的对白。")}</span>
      </div>
      {(record.workflow?.nodes || []).length ? (
        <div className="snapshot-list">
          <span className="eyebrow">章节快照</span>
          {(record.workflow.nodes).slice(0, 6).map((node, index) => {
            const visited = (record.workflow?.history || []).some((item) => (item.node_id || item.id) === node.id);
            return (
              <div className={`snapshot-item ${visited ? "is-visited" : ""}`} key={node.id || index}>
                <span className="snapshot-num">{index + 1}</span>
                <div>
                  <b>{cleanHistoryText(node.title || formatProgressLabel(node.kind))}</b>
                  <small>{cleanHistoryText(node.summary || formatProgressLabel(node.kind))}</small>
                </div>
              </div>
            );
          })}
        </div>
      ) : null}
      <button className="primary-button" type="button" onClick={() => restoreSession(record)}>读取记录</button>
    </aside>
  );
}

function sessionTitle(record) {
  return cleanHistoryText(record?.teaching?.topic || record?.scene?.variables?.topic || record?.scene?.title || record?.session?.id || "未命名剧情");
}

function sessionGoal(record) {
  if (recordGenerationStatus(record) === "failed" && record?.generation?.error) {
    return cleanHistoryText(record.generation.error);
  }
  return cleanHistoryText(record?.teaching?.learning_goal || record?.scene?.variables?.learning_goal || "继续此前的教学场景。");
}

function sessionSource(record) {
  const vars = record?.scene?.variables || {};
  return cleanHistoryText(vars.document_asset_name || vars.source_url || vars.document_url || vars.document_title || "—");
}

function recordGenerationStatus(record) {
  const status = String(record?.generation?.status || "").trim();
  if (status === "generating") return "generating";
  if (status === "failed") return "failed";
  if (status === "ready") return "ready";
  return hasPlayableWorkflow(record?.workflow, record?.scene, record?.session) ? "ready" : "draft";
}

function historyStatusLabel(status) {
  switch (status) {
    case "generating": return "生成中";
    case "failed": return "失败";
    case "ready": return "已生成";
    default: return "草稿";
  }
}

function historyStatusClass(status) {
  switch (status) {
    case "generating": return "generating";
    case "failed": return "failed";
    case "ready": return "ok";
    default: return "draft";
  }
}

function historyStatusColor(status) {
  switch (status) {
    case "generating": return "var(--accent)";
    case "failed": return "var(--bad)";
    case "ready": return "var(--ok)";
    default: return "var(--warn)";
  }
}

function historyStatusDescription(status) {
  switch (status) {
    case "generating":
      return "剧情正在后台生成，可以继续新建下一条任务；完成后这里会自动变成可演出记录。";
    case "failed":
      return "这条生成任务失败了，记录中保留了错误信息，修正配置后可以重新发起。";
    case "ready":
      return "已生成完整章节脚本，可直接进入剧情演出；不再需要的记录可以删除。";
    default:
      return "草稿尚未生成完整章节，可继续补全或删除。";
  }
}

function sessionCharacter(record) {
  const characters = Array.isArray(record?.characters) ? record.characters : [];
  const activeID = record?.session?.active_character_id;
  return characters.find((character) => character.id === activeID) || characters[0] || null;
}

function cleanHistoryText(value) {
  return String(value || "")
    .replace(/教学工作流/g, "教学剧情")
    .replace(/工作流/g, "剧情")
    .replace(/脚本节点/g, "剧情段落")
    .replace(/自由讨论节点/g, "自由讨论")
    .replace(/节点/g, "段落")
    .replace(/\bworkflow\b/gi, "story")
    .trim();
}

function workflowLineSpeechText(line) {
  return String(line?.speech_text || line?.speechText || line?.text || "").trim();
}

function workflowLineDisplayText(line) {
  return String(line?.text || line?.speech_text || line?.speechText || "").trim();
}

function workflowNodeDisplayText(node, withSpeaker = false) {
  const lines = Array.isArray(node?.lines) ? node.lines : [];
  if (lines.length) {
    return lines
      .map((line) => {
        const text = workflowLineDisplayText(line);
        if (!text) return "";
        if (withSpeaker && line?.speaker) return `【${line.speaker}】${text}`;
        return text;
      })
      .filter(Boolean)
      .join(withSpeaker ? "\n" : " ");
  }
  return String(node?.line || "").trim();
}

function workflowNodeSpeechText(node) {
  const lines = Array.isArray(node?.lines) ? node.lines : [];
  if (lines.length) return lines.map(workflowLineSpeechText).filter(Boolean).join(" ");
  return String(node?.speech_text || node?.line || "").trim();
}

function workflowNodeAudioURLs(node) {
  const lines = Array.isArray(node?.lines) ? node.lines : [];
  return lines
    .map((line) => line?.audio?.url || line?.audio_url || "")
    .filter(Boolean);
}

function workflowNodeHasReadyAudio(node) {
  const lines = Array.isArray(node?.lines) ? node.lines : [];
  if (!lines.length) return false;
  return lines.every((line) => {
    const status = line?.audio_status || line?.audioStatus || "";
    const audio = line?.audio || {};
    return status === "ready" || Boolean(audio.url || audio.format || line?.audio_url);
  });
}

function workflowNodeAudioLabel(node) {
  const lines = Array.isArray(node?.lines) ? node.lines : [];
  const ready = lines.filter((line) => line?.audio_status === "ready" || line?.audioStatus === "ready" || line?.audio?.format || line?.audio_url);
  if (!ready.length) return "";
  const formats = [...new Set(ready.map((line) => line?.audio?.format || line?.audio_format || "audio").filter(Boolean))];
  return `${formats.join("+")} · ${ready.length} 段`;
}

function stagePreviewBackground(url) {
  const fallback = "linear-gradient(180deg, #dfecfb 0%, #e9f2fd 46%, #f4f9ff 100%)";
  if (!url) return { backgroundImage: fallback };
  return { backgroundImage: `linear-gradient(180deg, rgba(255,255,255,0.10), rgba(40,80,130,0.18)), url("${url}"), ${fallback}` };
}

function formatProgressLabel(value) {
  const text = String(value || "").trim();
  if (!text) return "未开始";
  const normalized = text.toLowerCase().replace(/[_-]+/g, " ");
  if (normalized.includes("opening")) return "开场";
  if (normalized.includes("summary")) return "总结";
  if (normalized.includes("free discussion") || normalized.includes("free")) return "自由讨论";
  if (normalized.includes("choice") || normalized.includes("check")) return "确认";
  if (normalized.includes("challenge")) return "问答";
  if (normalized.includes("lesson")) return "讲解";
  return cleanHistoryText(text);
}

function ConfigView(props) {
  const {
    activeCharacterID, cgConfig, characters, cloneBusy, configStatus, configTab, languagePlan, pluginCatalog, prompt, providerHealth, providerOptions,
    refreshPlugins, refreshProviderHealth, refreshSessions, refreshVoiceCloneStatus, runtimeState, sessions, setActiveCharacterID,
    exportCharacterPackage, importCharacterPackageFile,
    setActiveView, setCgConfig, setCharacterAsset, setCharacterBackgroundAsset, setCharacterField, addCharacterMoodAsset, removeCharacterMoodAsset,
    renameCharacterMoodAsset, setCharacterMoodAsset, setCharacterPromptField,
    setCharacterRuntimeAgentField, setCharacterRuntimeField, setCharacterRuntimeImageField, setCharacterRuntimeLanguageField, setCharacterRuntimeVoiceExtraField,
    setCharacterRuntimeVoiceField, setConfigTab, setPrompt,
    setLanguagePlan, setVoiceCloneFiles, startVoiceClone, voiceClone,
    voiceCloneFiles, saveRoleConfig, setVoiceTestText, testVoiceSynthesis,
    voiceTestBusy, voiceTestText
  } = props;
  const [editingCharacterID, setEditingCharacterID] = useState(null);
  const [editorOpen, setEditorOpen] = useState(false);
  const [redactCharacterPackage, setRedactCharacterPackage] = useState(true);
  const packageInputRef = useRef(null);
  const editId = editingCharacterID || activeCharacterID;
  const editingCharacter = characters.find((character) => character.id === editId) || characters[0];
  const castPortrait = editingCharacter?.assets?.portrait_url || editingCharacter?.avatar_url || "";
  const castMoods = Object.keys(editingCharacter?.assets?.moods || {}).length;
  const castVoiceProvider = editingCharacter?.runtime?.voice_provider || "";
  const castAgentProvider = editingCharacter?.runtime?.agent_provider || "";
  return (
    <section className="page char2-page char2-page--single">
      <div className="char2">
          <section className="char2-stage">
            <div className="ch-head">
              <div>
                <h2>主讲角色设定</h2>
                <p>先选角色槽位，再在弹窗里编辑 Prompt、剧情 Agent、模型与语音服务。{configStatus?.message ? <em className="ch-status">· {configStatus.message}</em> : null}</p>
              </div>
              <div className="character-pack-actions">
                <input
                  ref={packageInputRef}
                  type="file"
                  accept="application/json,.json,.fairy-character.json"
                  onChange={(event) => {
                    importCharacterPackageFile?.(event.target.files?.[0]);
                    event.target.value = "";
                  }}
                />
                <button className="h-btn ghost sm" type="button" onClick={() => packageInputRef.current?.click()}>导入角色包</button>
                <label className="character-pack-redact">
                  <input type="checkbox" checked={redactCharacterPackage} onChange={(event) => setRedactCharacterPackage(event.target.checked)} />
                  <span>导出时脱敏凭据</span>
                </label>
                <button className="h-btn ghost sm" type="button" onClick={() => exportCharacterPackage?.("active", { redactSensitive: redactCharacterPackage })}>另存当前角色</button>
                <button className="h-btn ghost sm" type="button" onClick={() => exportCharacterPackage?.("all", { redactSensitive: redactCharacterPackage })}>另存全部角色</button>
              </div>
            </div>
            <div className="ch-body">
              <div className="ch-slots">
                {characters.map((character, index) => {
                  const portrait = character.assets?.portrait_url || character.avatar_url || "";
                  const on = character.id === editId && editorOpen;
                  return (
                    <button
                      key={character.id}
                      type="button"
                      className={`ch-slot ${on ? "on" : ""}`}
                      onClick={() => { setEditingCharacterID(character.id); setEditorOpen(true); }}
                    >
                      <span className="ch-no">{String(index + 1).padStart(2, "0")}</span>
                      <span className="ch-face" style={portrait ? { backgroundImage: `url("${portrait}")` } : undefined} />
                      <span className="ch-meta">
                        <b>{character.display_name || character.id}</b>
                        <span>{character.id === activeCharacterID ? "单角色讲解 / 追问 / 反馈 · 默认绑定" : "单角色讲解 / 追问 / 反馈"}</span>
                      </span>
                      <span className={`h-btn ${on ? "primary" : ""} sm`}>{on ? "编辑中" : "编辑"}</span>
                    </button>
                  );
                })}
              </div>
              {editingCharacter && editorOpen ? (
                <>
                  <div className="ch-scrim" role="presentation" onClick={() => setEditorOpen(false)} />
                  <CharacterConfigModal
                    inline
                    character={editingCharacter}
                    languagePlan={languagePlan}
                    isDefault={editingCharacter.id === activeCharacterID}
                    onSetDefault={() => setActiveCharacterID(editingCharacter.id)}
                    onClose={() => setEditorOpen(false)}
                    providerOptions={providerOptions}
                setCharacterAsset={setCharacterAsset}
                setCharacterBackgroundAsset={setCharacterBackgroundAsset}
                setCharacterField={setCharacterField}
                addCharacterMoodAsset={addCharacterMoodAsset}
                removeCharacterMoodAsset={removeCharacterMoodAsset}
                renameCharacterMoodAsset={renameCharacterMoodAsset}
                setCharacterMoodAsset={setCharacterMoodAsset}
                setCharacterPromptField={setCharacterPromptField}
                setCharacterRuntimeAgentField={setCharacterRuntimeAgentField}
                setCharacterRuntimeField={setCharacterRuntimeField}
                setCharacterRuntimeImageField={setCharacterRuntimeImageField}
                setCharacterRuntimeLanguageField={setCharacterRuntimeLanguageField}
                setCharacterRuntimeVoiceExtraField={setCharacterRuntimeVoiceExtraField}
                setCharacterRuntimeVoiceField={setCharacterRuntimeVoiceField}
                saveRoleConfig={saveRoleConfig}
                cloneBusy={cloneBusy}
                refreshVoiceCloneStatus={refreshVoiceCloneStatus}
                setVoiceCloneFiles={setVoiceCloneFiles}
                startVoiceClone={startVoiceClone}
                testVoiceSynthesis={testVoiceSynthesis}
                voiceClone={voiceClone}
                voiceCloneFiles={voiceCloneFiles}
                voiceTestBusy={voiceTestBusy}
                voiceTestText={voiceTestText}
                setVoiceTestText={setVoiceTestText}
                  />
                </>
              ) : null}
            </div>
          </section>
          <aside className="char2-cast">
            <h4>角色卡</h4>
            <div className="ch-charcard">
              <div className="ch-pic" style={castPortrait ? { backgroundImage: `url("${castPortrait}")` } : undefined}>
                {castPortrait ? null : <span className="ch-pic-empty">未绑定立绘</span>}
              </div>
              <div className="ch-cap"><b>{editingCharacter?.display_name || "主讲角色"}</b><p>“{editingCharacter?.persona || "还没有角色设定。"}”</p></div>
            </div>
            <div className="ch-panel">
              <h4>观测状态</h4>
              <div className="ch-obs"><div className="t"><b>对话服务</b><span>{castAgentProvider || "继承运行默认"}</span></div><span className={`ch-chip ${castAgentProvider ? "lock" : ""}`}>{castAgentProvider ? "已绑定" : "继承"}</span></div>
              <div className="ch-obs"><div className="t"><b>语音服务</b><span>{castVoiceProvider || "继承运行默认"}</span></div><span className={`ch-chip ${castVoiceProvider ? "ok" : ""}`}>{castVoiceProvider ? "已绑定" : "继承"}</span></div>
              <div className="ch-obs"><div className="t"><b>差分立绘</b><span>按描述自动匹配表情</span></div><span className="ch-chip">{castMoods} 个</span></div>
            </div>
          </aside>
      </div>
    </section>
  );
}

function CharacterConfigModal({
  character,
  cloneBusy,
  inline = false,
  isDefault = false,
  languagePlan = DEFAULT_LANGUAGE_PLAN,
  onSetDefault,
  onClose,
  providerOptions,
  refreshVoiceCloneStatus,
  saveRoleConfig,
  setCharacterAsset,
  setCharacterBackgroundAsset,
  setCharacterField,
  addCharacterMoodAsset,
  removeCharacterMoodAsset,
  renameCharacterMoodAsset,
  setCharacterMoodAsset,
  setCharacterPromptField,
  setCharacterRuntimeAgentField,
  setCharacterRuntimeField,
  setCharacterRuntimeImageField,
  setCharacterRuntimeLanguageField,
  setCharacterRuntimeVoiceExtraField,
  setCharacterRuntimeVoiceField,
  setVoiceCloneFiles,
  setVoiceTestText,
  startVoiceClone,
  testVoiceSynthesis,
  voiceClone,
  voiceCloneFiles,
  voiceTestBusy,
  voiceTestText
}) {
  const agentProvider = character.runtime?.agent_provider || "";
  const agentProfile = character.runtime?.agent || {};
  const voiceProvider = character.runtime?.voice_provider || "";
  const voice = character.runtime?.voice || {};
  const voiceExtra = voice.extra || {};
  const language = character.runtime?.language || {};
  const effectiveLanguage = mergeLanguagePlan(languagePlan, language);
  const image = character.runtime?.image || {};
  const speaker = voiceExtra.speaker || voice.voice_id || character.voice_id || "";
  const [provTab, setProvTab] = useState("agent");
  const handleSave = async () => {
    const saved = await saveRoleConfig(character.id);
    if (saved) onClose?.();
  };
  const provTabs = [
    { id: "agent", label: "剧情 Agent" },
    { id: "image", label: "图片服务" },
    { id: "voice", label: "语音服务" },
    { id: "asset", label: "素材服务" }
  ];

  const editor = (
      <section className="ch-modal" role="dialog" aria-modal="true" aria-label="角色配置">
        <div className="m-head">
          <div>
            <h3>编辑角色档案：{character.display_name || character.id}</h3>
            <p>角色信息在上，Prompt 居中，Provider 按标签页展开配置。</p>
          </div>
          <button className="h-btn ghost" type="button" onClick={onClose}>关闭</button>
        </div>

        <div className="m-scroll">
        <div className="m-top">
          <div>
            <div className="lbl">角色名</div>
            <input className="ch-inp" value={character.display_name || ""} onChange={(event) => setCharacterField(character.id, "display_name", event.target.value)} />
          </div>
          <div>
            <div className="lbl">默认绑定</div>
            <button className={`ch-inp sel ${isDefault ? "is-on" : ""}`} type="button" onClick={() => { if (!isDefault) onSetDefault?.(); }}>
              {isDefault ? "新建情节默认使用" : "设为新建情节默认"}
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M6 9l6 6 6-6" /></svg>
            </button>
          </div>
        </div>

        <div className="m-prompt">
          <div className="lbl">角色 Prompt</div>
          <textarea className="ch-ta" rows={4} value={character.persona || ""} onChange={(event) => setCharacterField(character.id, "persona", event.target.value)} placeholder="描述这个主讲角色的口吻、讲解方式与边界。" />
        </div>

        <div className="m-language">
          <div className="m-language__head">
            <div>
              <div className="lbl">语言配置</div>
              <p>屏幕字幕和语音台词可以分开设置；保存后影响重新生成、后续分幕和自由讨论，当前已生成历史不会自动翻译。</p>
            </div>
            <span>当前：{languageLabel(effectiveLanguage.display_language)} / {languageLabel(effectiveLanguage.speech_language)}</span>
          </div>
          <div className="language-fields">
            <SelectField label="屏幕显示语言" value={normalizeLanguageCode(language.display_language) || ""} onChange={(value) => setCharacterRuntimeLanguageField(character.id, "display_language", value)} options={languageSelectOptions} />
            <SelectField label="发声语言" value={normalizeLanguageCode(language.speech_language) || ""} onChange={(value) => setCharacterRuntimeLanguageField(character.id, "speech_language", value)} options={languageSelectOptions} />
            <SelectField label="语言模式" value={language.mode || ""} onChange={(value) => setCharacterRuntimeLanguageField(character.id, "mode", value)} options={languageModeOptions} />
            <SelectField label="翻译策略" value={language.translation_provider || ""} onChange={(value) => setCharacterRuntimeLanguageField(character.id, "translation_provider", value)} options={translationProviderOptions} />
          </div>
        </div>

        <div className="m-prov">
          <div className="ch-tabs">
            {provTabs.map((tab) => (
              <button key={tab.id} type="button" className={`ch-tab ${provTab === tab.id ? "on" : ""}`} onClick={() => setProvTab(tab.id)}>{tab.label}</button>
            ))}
          </div>
          <div className="ch-prov-body">
            {provTab === "agent" && (
              <div className="ch-prov-split">
                <div className="ch-agent-pick">
                  {[{ id: "", display_name: "继承运行默认" }, ...(providerOptions.agents || [])].map((opt) => (
                    <button
                      key={opt.id || "inherit"}
                      type="button"
                      className={`ch-agent ${(character.runtime?.agent_provider || "") === opt.id ? "on" : ""}`}
                      onClick={() => setCharacterRuntimeField(character.id, "agent_provider", opt.id)}
                    >
                      <b>{opt.display_name}</b>
                      <span>{agentHint(opt.id)}</span>
                    </button>
                  ))}
                </div>
                <div className="ch-fields">
                  <p className="ch-hint">选择 Agent 后，再设置剧情服务、模型与运行参数。</p>
                  <SelectField label="剧情服务" value={character.runtime?.scene_provider || ""} onChange={(value) => setCharacterRuntimeField(character.id, "scene_provider", value)} options={providerOptionsWithInherit(providerOptions.scenes)} />
                  {agentProvider === "fairy-agent" ? (
                    <>
                      <TextField label="模型名称" value={agentProfile.model || ""} onChange={(value) => setCharacterRuntimeAgentField(character.id, "model", value)} />
                      <TextField label="接口地址" value={agentProfile.endpoint || ""} onChange={(value) => setCharacterRuntimeAgentField(character.id, "endpoint", value)} className="ch-field-wide" />
                      <TextField label="密钥" value={agentProfile.api_key || ""} onChange={(value) => setCharacterRuntimeAgentField(character.id, "api_key", value)} type="password" />
                      <TextField label="额外请求 JSON" value={agentProfile.extra_body || ""} onChange={(value) => setCharacterRuntimeAgentField(character.id, "extra_body", value)} />
                    </>
                  ) : null}
                  <TextAreaField label="角色提示词覆盖" value={character.prompt?.developer || ""} onChange={(value) => setCharacterPromptField(character.id, "developer", value)} rows={3} className="ch-field-wide" />
                </div>
              </div>
            )}
            {provTab === "image" && (
              <div className="ch-fields">
                <p className="ch-hint">图片配置只影响该角色的 Galgame CG。</p>
                <SelectField label="图片服务" value={character.runtime?.image_provider || ""} onChange={(value) => setCharacterRuntimeField(character.id, "image_provider", value)} options={providerOptionsWithInherit(providerOptions.images)} />
                <TextField label="图片 Endpoint" value={image.endpoint || ""} onChange={(value) => setCharacterRuntimeImageField(character.id, "endpoint", value)} />
                <TextField label="图片尺寸" value={image.size || ""} onChange={(value) => setCharacterRuntimeImageField(character.id, "size", value)} />
                <TextField label="图片风格" value={image.style || ""} onChange={(value) => setCharacterRuntimeImageField(character.id, "style", value)} />
                <TextField label="负向提示词" value={image.negative_prompt || ""} onChange={(value) => setCharacterRuntimeImageField(character.id, "negative_prompt", value)} />
                <TextAreaField label="角色图片提示词覆盖" value={image.prompt || ""} onChange={(value) => setCharacterRuntimeImageField(character.id, "prompt", value)} rows={2} className="ch-field-wide" />
              </div>
            )}
            {provTab === "voice" && (
              <div className="ch-fields">
                <p className="ch-hint">{voiceProvider === "volcengine" ? "火山声音复刻使用 APP ID、Access Token、资源 ID 和 S_ 声音 ID。" : "本地语音或 HTTP voice 插件使用 Endpoint、参考音频和语言参数。"}</p>
                <SelectField label="语音服务" value={character.runtime?.voice_provider || ""} onChange={(value) => setCharacterRuntimeField(character.id, "voice_provider", value)} options={providerOptionsWithInherit(providerOptions.voices)} />
                {voiceProvider === "volcengine" ? (
                  <>
                    <TextField label="APP ID" value={voiceExtra.app_id || ""} onChange={(value) => setCharacterRuntimeVoiceExtraField(character.id, "app_id", value)} />
                    <TextField label="Access Token" value={voiceExtra.access_token || ""} onChange={(value) => setCharacterRuntimeVoiceExtraField(character.id, "access_token", value)} type="password" />
                    <SelectField label="模型资源" value={voiceExtra.resource_id || "seed-icl-2.0"} onChange={(value) => setCharacterRuntimeVoiceExtraField(character.id, "resource_id", value)} options={volcengineResourceOptions} />
                    <TextField label="复刻声音 ID" value={speaker} onChange={(value) => {
                      setCharacterField(character.id, "voice_id", value);
                      setCharacterRuntimeVoiceField(character.id, "voice_id", value);
                      setCharacterRuntimeVoiceExtraField(character.id, "speaker", value);
                    }} />
                    <SelectField label="音频格式" value={voice.media_type || "mp3"} onChange={(value) => setCharacterRuntimeVoiceField(character.id, "media_type", value)} options={[{ value: "mp3", label: "mp3" }, { value: "wav", label: "wav" }]} />
                    <div className="ch-field-wide clone-workbench">
                      <div className="clone-actions">
                        <label className="file-button">上传训练音频<input type="file" accept="audio/*" multiple onChange={(event) => setVoiceCloneFiles(Array.from(event.target.files || []))} /></label>
                        <button className="primary-button" type="button" onClick={startVoiceClone} disabled={cloneBusy || voiceCloneFiles.length !== 1}>{cloneBusy ? "处理中" : "提交复刻训练"}</button>
                        <button className="ghost-button" type="button" onClick={refreshVoiceCloneStatus} disabled={cloneBusy || !speaker}>查询状态</button>
                      </div>
                      <div className="clone-status">
                        <StateRow label="files" value={`${voiceCloneFiles.length} 段`} />
                        <StateRow label="submitted" value={voiceClone.sample_count ? `${voiceClone.sample_count} 段` : "-"} />
                        <StateRow label="speaker" value={voiceClone.speaker_id || speaker || "-"} />
                        <StateRow label="status" value={voiceClone.status || "idle"} />
                      </div>
                      <div className="clone-file-list">
                        {voiceCloneFiles.length ? voiceCloneFiles.map((file) => (
                          <div key={`${file.name}-${file.size}`} className="clone-file-item">
                            <strong>{file.name}</strong>
                            <span>{formatBytes(file.size)} · {file.type || "audio"}</span>
                          </div>
                        )) : <div className="clone-file-empty">还没有选择训练音频。当前只支持单文件训练。</div>}
                      </div>
                    </div>
                  </>
                ) : (
                  <>
                    <TextField label="Endpoint" value={voice.endpoint || ""} onChange={(value) => setCharacterRuntimeVoiceField(character.id, "endpoint", value)} />
                    <TextField label="参考音频语言" value={voice.prompt_lang || ""} onChange={(value) => setCharacterRuntimeVoiceField(character.id, "prompt_lang", value)} />
                    <TextField label="参考音频路径" value={voice.ref_audio_path || ""} onChange={(value) => setCharacterRuntimeVoiceField(character.id, "ref_audio_path", value)} />
                    <TextField label="参考音频文本" value={voice.prompt_text || ""} onChange={(value) => setCharacterRuntimeVoiceField(character.id, "prompt_text", value)} />
                    <SelectField label="音频格式" value={voice.media_type || ""} onChange={(value) => setCharacterRuntimeVoiceField(character.id, "media_type", value)} options={[{ value: "", label: "运行默认值" }, { value: "wav", label: "wav" }, { value: "mp3", label: "mp3" }]} />
                  </>
                )}
                <div className="ch-field-wide voice-test-card">
                  <TextAreaField label="测试台词" value={voiceTestText} onChange={setVoiceTestText} rows={2} />
                  <button className="primary-button" type="button" onClick={testVoiceSynthesis} disabled={voiceTestBusy || (voiceProvider === "volcengine" && !speaker)}>{voiceTestBusy ? "合成中" : "试听角色语音"}</button>
                </div>
              </div>
            )}
            {provTab === "asset" && (
              <div className="ch-fields">
                <p className="ch-hint">普通对话使用立绘；阶段背景按段落自动切换；差分立绘按描述匹配表情。</p>
                <TextField label="声音 ID" value={character.voice_id} onChange={(value) => setCharacterField(character.id, "voice_id", value)} />
                <TextField label="默认立绘 URL" value={character.assets?.portrait_url || ""} onChange={(value) => setCharacterAsset(character.id, "portrait_url", value)} />
                <TextField label="默认背景 URL" value={character.assets?.background_url || ""} onChange={(value) => setCharacterAsset(character.id, "background_url", value)} />
                <TextField label="参考图 URL" value={character.assets?.reference_image_url || ""} onChange={(value) => setCharacterAsset(character.id, "reference_image_url", value)} />
                <TextAreaField label="CG 提示词" value={character.assets?.cg_prompt || ""} onChange={(value) => setCharacterAsset(character.id, "cg_prompt", value)} rows={2} className="ch-field-wide" />
                <div className="ch-field-wide ch-subhead">阶段背景</div>
                {["opening", "lesson", "choice", "discussion", "summary"].map((name) => (
                  <TextField key={name} label={`${name} 背景`} value={character.assets?.backgrounds?.[name] || ""} onChange={(value) => setCharacterBackgroundAsset(character.id, name, value)} />
                ))}
                <div className="ch-field-wide ch-subhead">差分立绘</div>
                <div className="ch-field-wide asset-list">
                  {Object.entries(character.assets?.moods || {}).map(([name, asset]) => (
                    <article className="expression-asset" key={name}>
                      <div className="expression-asset__head">
                        <TextField label="表情 Key" value={name} onChange={(value) => renameCharacterMoodAsset(character.id, name, value)} />
                        <TextField label="显示名" value={asset.label || ""} onChange={(value) => setCharacterMoodAsset(character.id, name, "label", value)} />
                        <button className="ghost-button danger" type="button" onClick={() => removeCharacterMoodAsset(character.id, name)}>删除</button>
                      </div>
                      <TextAreaField label="识别描述" value={asset.description || ""} onChange={(value) => setCharacterMoodAsset(character.id, name, "description", value)} rows={2} placeholder="例如：轻松微笑，适合鼓励玩家、解释直觉概念和缓和气氛。" />
                      <div className="field-grid">
                        <TextField label="立绘 URL" value={asset.portrait_url || ""} onChange={(value) => setCharacterMoodAsset(character.id, name, "portrait_url", value)} />
                        <TextField label="CG 提示词" value={asset.cg_prompt || ""} onChange={(value) => setCharacterMoodAsset(character.id, name, "cg_prompt", value)} />
                        <TextField label="语音风格" value={asset.voice_style || ""} onChange={(value) => setCharacterMoodAsset(character.id, name, "voice_style", value)} />
                      </div>
                    </article>
                  ))}
                  {!Object.keys(character.assets?.moods || {}).length ? <div className="clone-file-empty">还没有差分资产。添加一个后，Agent 就可以按描述选择它。</div> : null}
                  <button className="ghost-button" type="button" onClick={() => addCharacterMoodAsset(character.id)}>添加差分</button>
                </div>
              </div>
            )}
          </div>
        </div>

        <div className="m-foot">
          <span className="note">保存后，新建情节会使用该角色 Prompt 和 Provider 配置；已有脚本不会被静默覆盖。</span>
          <button className="h-btn primary" type="button" onClick={handleSave}>保存档案</button>
        </div>
        </div>
      </section>
  );
  if (inline) return editor;
  return createPortal(<div className="modal-backdrop" role="presentation" onMouseDown={onClose}>{editor}</div>, document.body);
}

function LogsView({ logs, setActiveView, setLogs, documentTitle, activeCharacter, lessonWorkflow, effectiveProviders }) {
  const [levelFilter, setLevelFilter] = useState("all");
  const [query, setQuery] = useState("");
  const errorCount = logs.filter((log) => log.level === "error").length;
  const warnCount = logs.filter((log) => log.level === "warn" || log.level === "warning").length;
  const recentLogs = [...logs].reverse();
  const visibleLogs = recentLogs.filter((log) => {
    if (levelFilter !== "all") {
      const level = log.level === "warning" ? "warn" : log.level;
      if (level !== levelFilter) return false;
    }
    if (!query.trim()) return true;
    return (log.message || "").toLowerCase().includes(query.trim().toLowerCase());
  });
  const issues = recentLogs.filter((log) => log.level === "error" || log.level === "warn" || log.level === "warning").slice(0, 5);
  const nodeCount = lessonWorkflow?.nodes?.length || 0;
  const visitedCount = lessonWorkflow?.history?.length || 0;
  const levelPills = [{ id: "all", label: "全部" }, { id: "info", label: "INFO" }, { id: "warn", label: "WARN" }, { id: "error", label: "ERROR" }];
  return (
    <section className="page logs-page">
      <PageHeader eyebrow="制作日志" title="制作日志" description="按时间查看文档解析、Agent 调用、角色生成和素材处理记录。" action={<div className="header-actions"><button className="ghost-button" type="button" onClick={() => setActiveView("stage")}>进入剧情</button><button className="ghost-button danger-button" type="button" onClick={() => setLogs([])}>清空记录</button></div>} />
      <div className="log-console-shell">
        <div className="log-console-main">
          <div className="history-toolbar">
            <div className="history-pills">
              {levelPills.map((pill) => (
                <button key={pill.id} type="button" className={levelFilter === pill.id ? "is-active" : ""} onClick={() => setLevelFilter(pill.id)}>{pill.label}</button>
              ))}
            </div>
            <label className="history-search">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" aria-hidden="true"><circle cx="11" cy="11" r="7" /><path d="M21 21l-4-4" /></svg>
              <input type="search" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索消息、级别或 run id" />
            </label>
          </div>
          <div className="log-table" aria-label="制作日志时间线">
            <div className="log-table__row log-table__row--head">
              <span>时间</span><span>级别</span><span>消息</span>
            </div>
            {visibleLogs.length > 0 ? visibleLogs.map((log, index) => (
              <div className={`log-table__row log-line--${log.level}`} key={`${log.time}-${index}`}>
                <span className="log-table__time">{log.time}</span>
                <span className={`log-table__level log-level--${log.level}`}>{(log.level || "info").toUpperCase()}</span>
                <span className="log-table__msg">{log.message}</span>
              </div>
            )) : (
              <div className="log-empty">
                <strong>没有匹配的记录</strong>
                <span>保存配置、生成剧情或播放语音后，会在这里出现活动时间线。</span>
              </div>
            )}
          </div>
        </div>
        <aside className="log-side">
          <div className="log-side__panel">
            <span className="eyebrow">当前运行</span>
            <div className="observation-row"><b>文档</b><span>{documentTitle || "—"}</span></div>
            <div className="observation-row"><b>角色</b><span>{activeCharacter?.display_name || "主讲角色"}</span></div>
            <div className="observation-row"><b>章节</b><span>{nodeCount ? `${visitedCount}/${nodeCount}` : "—"}</span></div>
            <div className="observation-row"><b>Agent</b><span>{activeCharacter?.runtime?.agent_provider || effectiveProviders?.agent || "—"}</span></div>
          </div>
          <div className="log-stats">
            <div className="log-stat"><strong>{logs.length}</strong><span>条日志</span></div>
            <div className={`log-stat ${errorCount > 0 ? "is-alert" : ""}`}><strong>{errorCount}</strong><span>个错误</span></div>
          </div>
          <div className="log-side__panel">
            <span className="eyebrow">最近问题</span>
            {issues.length ? issues.map((log, index) => (
              <div className="log-issue" key={`${log.time}-${index}`}>
                <span className={`log-table__level log-level--${log.level}`}>{(log.level || "info").toUpperCase()}</span>
                <p>{log.message}</p>
              </div>
            )) : <p className="log-issue-empty">暂无警告或错误。{warnCount === 0 ? "运行平稳。" : ""}</p>}
          </div>
        </aside>
      </div>
    </section>
  );
}

function PageHeader({ action, description, eyebrow, title }) {
  return (
    <header className="page-header">
      <div>
        <span className="eyebrow">{eyebrow}</span>
        <h1>{title}</h1>
        <p>{description}</p>
      </div>
      {action}
    </header>
  );
}

function NavButton({ active, icon, label, onClick }) {
  return (
    <button className={`nav-button ${active ? "is-active" : ""}`} type="button" onClick={onClick}>
      {icon}
      <span>{label}</span>
    </button>
  );
}

const NAV_ICONS = {
  dashboard: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round"><path d="M4 5h11l5 5v9H4z" /><path d="M15 5v5h5" /></svg>,
  history: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round"><path d="M12 7v5l3 2" /><circle cx="12" cy="12" r="8" /></svg>,
  stage: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round"><path d="M5 4l14 8-14 8V4z" /></svg>,
  config: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round"><circle cx="12" cy="8" r="4" /><path d="M4 20c0-4 4-6 8-6s8 2 8 6" /></svg>,
  logs: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round"><path d="M4 6h16M4 12h16M4 18h10" /></svg>
};

const BRAND_STAR = <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M12 2l2.2 5.6L20 9l-4.4 3.2L17 18l-5-3-5 3 1.4-5.8L4 9l5.8-1.4L12 2z" /></svg>;

function CGPreview({ cg, compact = false }) {
  return (
    <div className={`cg-preview ${compact ? "is-compact" : ""}`}>
      {cg?.url ? <img src={cg.url} alt="生成的场景 CG" /> : <div className="cg-placeholder"><span>{cg?.error ? "CG 生成失败" : "CG Preview"}</span></div>}
      <p>{cg?.error || cg?.prompt || "生成互动剧情后会显示场景 CG 或生成计划。"}</p>
    </div>
  );
}

function TextField({ className = "", label, onChange, placeholder, type = "text", value }) {
  return (
    <label className={`field ${className}`.trim()}>
      <span>{label}</span>
      <input type={type} value={value || ""} onChange={(event) => onChange(event.target.value)} placeholder={placeholder} />
    </label>
  );
}

function TextAreaField({ className = "", label, onChange, placeholder, rows = 4, value }) {
  return (
    <label className={`field ${className}`.trim()}>
      <span>{label}</span>
      <textarea value={value || ""} onChange={(event) => onChange(event.target.value)} placeholder={placeholder} rows={rows} />
    </label>
  );
}

function SelectField({ label, onChange, options, value }) {
  return (
    <label className="field">
      <span>{label}</span>
      <select value={value || ""} onChange={(event) => onChange(event.target.value)}>
        {options.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
      </select>
    </label>
  );
}

function Metric({ label, value }) {
  return (
    <div className="metric">
      <span>{label}</span>
      <strong>{value || "-"}</strong>
    </div>
  );
}

function StateRow({ label, value }) {
  return (
    <div className="state-row">
      <span>{label}</span>
      <strong>{value || "-"}</strong>
    </div>
  );
}

function MiniList({ action, empty, items, title }) {
  return (
    <section className="mini-panel">
      <div className="panel-title tight">
        <h3>{title}</h3>
        {action}
      </div>
      <ul className="plain-list">
        {items.length ? items.slice(0, 6).map((item, index) => <li key={`${item}-${index}`}>{item}</li>) : <li>{empty}</li>}
      </ul>
    </section>
  );
}

function defaultScene() {
  return {
    id: "lesson-draft",
    title: "等待生成教学场景",
    location: "interactive classroom",
    phase: "draft",
    variables: {},
    last_active_at: new Date().toISOString()
  };
}

function defaultWorkflow() {
  return {
    id: "lesson-draft-workflow",
    title: "教学剧情草稿",
    goal: "等待生成教学目标。",
    preparing: false,
    pending_node_id: "",
    current_node_id: "opening",
    nodes: [
      {
        id: "opening",
        kind: "draft",
        title: "待生成",
        summary: "等待材料生成剧情。",
        speaker: "亚托莉",
        line: "把材料放进来后，我会生成第一段正式剧情。",
        speech_text: "把材料放进来后，我会生成第一段正式剧情。",
        background_key: "opening",
        choices: []
      }
    ]
  };
}

function toOptions(items = []) {
  return items.map((item) => ({ value: item.id, label: item.display_name || item.id }));
}

function providerOptionsWithInherit(items = []) {
  return [{ value: "", label: "运行默认值" }, ...toOptions(items)];
}

const languageSelectOptions = [
  { value: "", label: "继承运行默认" },
  { value: "zh-CN", label: "中文（简体）" },
  { value: "ja-JP", label: "日语" },
  { value: "en-US", label: "英语" }
];

const languageModeOptions = [
  { value: "", label: "运行默认值" },
  { value: "translate_for_voice", label: "字幕与发声可不同" },
  { value: "same", label: "字幕与发声相同" }
];

const translationProviderOptions = [
  { value: "", label: "运行默认值" },
  { value: "agent", label: "Agent 生成语音稿" },
  { value: "external", label: "外部翻译 Provider" }
];

function languageLabel(value) {
  switch (normalizeLanguageCode(value)) {
    case "zh-CN":
      return "中文";
    case "ja-JP":
      return "日语";
    case "en-US":
      return "英语";
    case "":
      return "继承默认";
    default:
      return value;
  }
}

function agentHint(id) {
  switch (id) {
    case "fairy-agent":
      return "原生剧情生成工作流";
    case "codex":
      return "调用本地 Codex 命令";
    case "mock":
      return "本地占位 / 测试用";
    case "":
      return "使用运行默认 Agent";
    default:
      return "自定义 Agent";
  }
}

function pruneEmptyObject(value) {
  const entries = Object.entries(value || {}).filter(([, item]) => {
    if (item == null) return false;
    if (typeof item === "string") return item.trim() !== "";
    if (Array.isArray(item)) return item.length > 0;
    if (typeof item === "object") return Object.keys(item).length > 0;
    return true;
  });
  return entries.length ? Object.fromEntries(entries) : undefined;
}

function mergePromptConfigs(...configs) {
  return configs.filter(Boolean).reduce((merged, config) => ({
    system: config.system || merged.system,
    developer: config.developer || merged.developer,
    scene_instruction: config.scene_instruction || merged.scene_instruction,
    response_contract: config.response_contract || merged.response_contract,
    style_rules: mergeStyleRules(merged.style_rules, config.style_rules)
  }), {});
}

function mergeStyleRules(base = [], next = []) {
  const rules = [...base, ...next].filter(Boolean);
  return Array.from(new Set(rules));
}

function buildExpressionRules(character) {
  const entries = Object.entries(character?.assets?.moods || {});
  if (!entries.length) return [];
  const descriptions = entries.map(([key, asset]) => {
    const label = asset.label ? ` / ${asset.label}` : "";
    const description = asset.description || asset.cg_prompt || asset.voice_style || "用户导入的角色差分";
    return `${key}${label}: ${description}`;
  });
  return [
    `可用角色差分 expression key 只能优先从这些用户导入资产中选择：${descriptions.join("；")}。`,
    "segments[].expression 必须尽量使用上述 expression key；只有没有合适资产时，才输出简短英文情绪词作为 fallback。"
  ];
}

function sanitizeMoodKey(value) {
  return String(value || "")
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, "_")
    .replace(/^_+|_+$/g, "")
    .slice(0, 48);
}

function nextMoodKey(moods) {
  let index = Object.keys(moods || {}).length + 1;
  let key = `expression_${index}`;
  while (moods?.[key]) {
    index += 1;
    key = `expression_${index}`;
  }
  return key;
}

function formatBytes(value) {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let size = value;
  let index = 0;
  while (size >= 1024 && index < units.length - 1) {
    size /= 1024;
    index += 1;
  }
  return `${size.toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
}

function downloadJSONContent(content, filename) {
  const blob = new Blob([content], { type: "application/json;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}

function safeFileName(value) {
  return String(value || "fairy-character")
    .trim()
    .replace(/[\\/:*?"<>|]+/g, "-")
    .replace(/\s+/g, "-")
    .slice(0, 64) || "fairy-character";
}

function formatHistoryTime(value) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return date.toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit"
  });
}

function countHistoryAudio(record) {
  const workflowAudio = (record?.workflow?.history || []).filter((item) => item?.audio_url).length;
  const messageAudio = (record?.messages || []).filter((item) => item?.audio_url).length;
  return workflowAudio + messageAudio;
}

function progressPercent(visited, total) {
  if (!Number.isFinite(total) || total <= 0) return visited > 0 ? 100 : 0;
  return Math.max(4, Math.min(100, Math.round((visited / total) * 100)));
}

function parseWorkflow(value) {
  const text = value.trim();
  if (!text) return undefined;
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

function normalizeTeachingWorkflow(workflow, scene) {
  const nodes = Array.isArray(workflow?.nodes)
    ? workflow.nodes.filter((node) => node?.id && node?.kind && node?.title)
    : [];
  if (nodes.length) {
    const currentNodeID = nodes.some((node) => node.id === workflow.current_node_id) ? workflow.current_node_id : nodes[0].id;
    return {
      id: workflow.id || `${scene?.id || "lesson"}-workflow`,
      title: workflow.title || `教学剧情：${scene?.variables?.topic || scene?.title || "新的文档"}`,
      goal: workflow.goal || scene?.variables?.learning_goal || "理解材料核心概念。",
      preparing: Boolean(workflow.preparing),
      pending_node_id: workflow.pending_node_id || "",
      current_node_id: currentNodeID,
      nodes,
      history: Array.isArray(workflow.history) ? workflow.history : []
    };
  }
  const fallback = defaultWorkflow(); return { ...fallback, id: `${scene?.id || "lesson"}-workflow`, title: fallback.title || scene?.title || "教学剧情", goal: fallback.goal || scene?.variables?.learning_goal || "理解材料核心概念。", preparing: Boolean(workflow?.preparing), pending_node_id: workflow?.pending_node_id || "", current_node_id: fallback.current_node_id, nodes: fallback.nodes, history: Array.isArray(workflow?.history) ? workflow.history : [] };
}

function hasPlayableWorkflow(workflow, scene, session) {
  if (!session?.id || scene?.id === "lesson-draft") return false;
  const nodes = Array.isArray(workflow?.nodes) ? workflow.nodes : [];
  return nodes.some((node) => {
    if (!node?.id || node.kind === "draft") return false;
    if (Array.isArray(node.lines) && node.lines.some((line) => String(line?.text || "").trim())) return true;
    if (String(node.line || "").trim()) return true;
    if (Array.isArray(node.choices) && node.choices.length > 0) return true;
    return Boolean(node.free_discussion || node.kind === "free_discussion");
  });
}

function loadSavedConfig() {
  if (typeof window === "undefined") return null;
  const raw = window.localStorage.getItem(CONFIG_STORAGE_KEY);
  if (!raw) return null;
  try {
    const payload = JSON.parse(raw);
    return payload && payload.version === 1 ? sanitizeSavedConfig(payload) : null;
  } catch {
    return null;
  }
}

function sanitizeSavedConfig(payload) {
  return {
    ...payload,
    prompt: sanitizePromptConfig(payload.prompt),
    languagePlan: normalizeLanguagePlan(payload.languagePlan || DEFAULT_LANGUAGE_PLAN),
    characters: mergeSavedCharacters(payload.characters)
  };
}

function mergeSavedCharacters(savedCharacters) {
  if (!Array.isArray(savedCharacters)) return savedCharacters;
  return savedCharacters.map((character) => normalizeSavedCharacter(character)).filter(Boolean);
}

function firstNonEmpty(...values) {
  for (const value of values) {
    if (typeof value === "string" && value.trim()) return value.trim();
  }
  return "";
}

function normalizeLanguageCode(value) {
  const raw = String(value || "").trim();
  const normalized = raw.toLowerCase().replaceAll("_", "-");
  switch (normalized) {
    case "":
      return "";
    case "cn":
    case "zh":
    case "zh-cn":
    case "zh-hans":
    case "zh-hans-cn":
      return "zh-CN";
    case "jp":
    case "ja":
    case "ja-jp":
      return "ja-JP";
    case "en":
    case "en-us":
      return "en-US";
    default:
      return raw;
  }
}

function normalizeLanguagePlan(plan = {}, defaults = DEFAULT_LANGUAGE_PLAN) {
  const merged = { ...(defaults || {}), ...(plan || {}) };
  const mode = String(merged.mode || DEFAULT_LANGUAGE_PLAN.mode).trim() || DEFAULT_LANGUAGE_PLAN.mode;
  const displayLanguage = normalizeLanguageCode(merged.display_language) || DEFAULT_LANGUAGE_PLAN.display_language;
  const speechLanguage = mode === "same"
    ? displayLanguage
    : normalizeLanguageCode(merged.speech_language) || displayLanguage;
  const translationProvider = String(merged.translation_provider || DEFAULT_LANGUAGE_PLAN.translation_provider).trim() || DEFAULT_LANGUAGE_PLAN.translation_provider;
  return {
    display_language: displayLanguage,
    speech_language: speechLanguage,
    translation_provider: translationProvider,
    mode
  };
}

function normalizeLanguageOverride(plan = {}) {
  const out = {};
  const displayLanguage = normalizeLanguageCode(plan.display_language);
  const speechLanguage = normalizeLanguageCode(plan.speech_language);
  const mode = String(plan.mode || "").trim();
  const translationProvider = String(plan.translation_provider || "").trim();
  if (displayLanguage) out.display_language = displayLanguage;
  if (speechLanguage) out.speech_language = mode === "same" && displayLanguage ? displayLanguage : speechLanguage;
  if (translationProvider) out.translation_provider = translationProvider;
  if (mode) out.mode = mode;
  return pruneEmptyObject(out);
}

function mergeLanguagePlan(base = DEFAULT_LANGUAGE_PLAN, override = {}) {
  const merged = { ...(base || {}) };
  for (const [key, value] of Object.entries(override || {})) {
    if (typeof value === "string" && value.trim() === "") continue;
    if (value == null) continue;
    merged[key] = value;
  }
  return normalizeLanguagePlan(merged);
}

function languageCodeForVoiceProvider(value) {
  const language = normalizeLanguageCode(value);
  if (language === "zh-CN") return "zh";
  if (language === "ja-JP") return "ja";
  if (language === "en-US") return "en";
  return language || String(value || "").trim();
}

function normalizeVoiceProfileLanguage(profile = {}, provider = "", speechLanguage = "") {
  const voiceLanguage = languageCodeForVoiceProvider(speechLanguage);
  const next = { ...(profile || {}) };
  if (provider === "volcengine") {
    next.extra = {
      ...(profile?.extra || {}),
      explicit_language: voiceLanguage || profile?.extra?.explicit_language || ""
    };
    return next;
  }
  if (voiceLanguage) {
    next.text_lang = voiceLanguage;
  }
  return next;
}

function mergeSessionCharacters(currentCharacters, sessionCharacters) {
  const current = Array.isArray(currentCharacters) ? currentCharacters.map((character) => normalizeSavedCharacter(character)).filter(Boolean) : [];
  const incoming = Array.isArray(sessionCharacters) ? sessionCharacters.map((character) => normalizeSavedCharacter(character)).filter(Boolean) : [];
  if (!incoming.length) return current;
  const currentByID = new Map(current.map((character) => [character.id, character]));
  const merged = incoming.map((character) => {
    const existing = currentByID.get(character.id);
    if (!existing) return character;
    return normalizeSavedCharacter({
      ...character,
      display_name: firstNonEmpty(existing.display_name, character.display_name),
      voice_id: firstNonEmpty(existing.voice_id, character.voice_id),
      persona: firstNonEmpty(existing.persona, character.persona),
      style_rules: existing.style_rules?.length ? existing.style_rules : character.style_rules,
      prompt: mergePromptConfigs(character.prompt, existing.prompt),
      assets: {
        ...(character.assets || {}),
        ...(existing.assets || {}),
        backgrounds: {
          ...(character.assets?.backgrounds || {}),
          ...(existing.assets?.backgrounds || {})
        },
        moods: {
          ...(character.assets?.moods || {}),
          ...(existing.assets?.moods || {})
        }
      },
      runtime: pruneEmptyObject({
        ...(character.runtime || {}),
        ...(existing.runtime || {}),
        agent: { ...(character.runtime?.agent || {}), ...(existing.runtime?.agent || {}) },
        voice: {
          ...(character.runtime?.voice || {}),
          ...(existing.runtime?.voice || {}),
          extra: {
            ...(character.runtime?.voice?.extra || {}),
            ...(existing.runtime?.voice?.extra || {})
          }
        },
        image: { ...(character.runtime?.image || {}), ...(existing.runtime?.image || {}) },
        language: { ...(character.runtime?.language || {}), ...(existing.runtime?.language || {}) }
      })
    });
  });
  const mergedIDs = new Set(merged.map((character) => character.id));
  return [...merged, ...current.filter((character) => !mergedIDs.has(character.id))];
}

function normalizeSavedCharacter(character) {
  if (!character) return character;
  const runtime = character.runtime ? { ...character.runtime } : {};
  const voice = runtime.voice ? { ...runtime.voice } : undefined;
  const voiceExtra = voice?.extra || {};
  const language = normalizeLanguageOverride({
    ...(runtime.language || {}),
    speech_language: runtime.language?.speech_language || voiceExtra.explicit_language || ""
  });
  const hasVolcengineCredential = Boolean(
    voiceExtra.app_id ||
    voiceExtra.access_token ||
    voiceExtra.resource_id ||
    voiceExtra.speaker ||
    String(voice?.voice_id || character.voice_id || "").startsWith("S_")
  );
  if (!runtime.voice_provider && hasVolcengineCredential) {
    runtime.voice_provider = "volcengine";
  }
  if (voice && !voice.voice_id && voiceExtra.speaker) {
    voice.voice_id = voiceExtra.speaker;
  }
  if (voice) {
    const speechLanguage = languageCodeForVoiceProvider(language?.speech_language || voiceExtra.explicit_language || "");
    voice.extra = pruneEmptyObject({
      ...(voice.extra || {}),
      explicit_language: voice.extra?.explicit_language ? speechLanguage : voice.extra?.explicit_language
    });
  }
  return {
    ...character,
    voice_id: character.voice_id || voiceExtra.speaker || voice?.voice_id || "",
    assets: {
      ...(character.assets || {}),
      backgrounds: character.assets?.backgrounds || {},
      moods: character.assets?.moods || {}
    },
    runtime: pruneEmptyObject({
      ...runtime,
      voice,
      language
    }),
    prompt: sanitizePromptConfig(character.prompt),
    style_rules: sanitizeStyleRules(character.style_rules)
  };
}

function sanitizePromptConfig(config) {
  if (!config) return config;
  return {
    ...config,
    style_rules: sanitizeStyleRules(config.style_rules)
  };
}

function sanitizeStyleRules(rules) {
  if (!Array.isArray(rules)) return rules;
  const cleaned = rules.filter((rule) => !String(rule).includes("先讲直觉"));
  if (cleaned.length === rules.length) return cleaned;
  return [...cleaned, "用自然短对白，不使用固定讲解句式。"];
}

function saveUserConfig(config) {
  if (typeof window === "undefined") return;
  const previous = loadSavedConfig() || {};
  window.localStorage.setItem(CONFIG_STORAGE_KEY, JSON.stringify({ ...previous, version: 1, ...config }));
}

function clearSavedConfig() {
  if (typeof window === "undefined") return;
  window.localStorage.removeItem(CONFIG_STORAGE_KEY);
}

async function readAudioSample(file) {
  const dataURL = await new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result || ""));
    reader.onerror = () => reject(reader.error || new Error("读取音频失败"));
    reader.readAsDataURL(file);
  });
  const [, dataBase64 = ""] = dataURL.split(",");
  return {
    filename: file.name,
    mime_type: file.type,
    format: file.name.split(".").pop() || "",
    data_base64: dataBase64
  };
}

function errorMessage(error, fallback = "未知错误") {
  if (!error) return fallback;
  if (typeof error === "string") return error.trim() || fallback;
  if (typeof error === "number" || typeof error === "boolean") return String(error);
  if (error instanceof Error && error.message) return error.message;
  if (typeof error === "object") {
    const direct = error.message || error.msg || error.error || error.detail || error.reason;
    if (direct) return String(direct);
    try {
      const serialized = JSON.stringify(error);
      if (serialized && serialized !== "{}") return serialized;
    } catch {
      // Ignore serialization failures and fall back to String().
    }
  }
  const text = String(error);
  return text && text !== "[object Object]" ? text : fallback;
}

function normalizeMood(value) {
  const text = String(value || "").toLowerCase();
  if (EXPRESSION_PRESETS.includes(text)) return text;
  if (text.includes("soft") || text.includes("smile")) return "soft_smile";
  if (text.includes("happy") || text.includes("joy")) return "happy";
  if (text.includes("worried") || text.includes("anxious")) return "worried";
  if (text.includes("embarrass")) return "embarrassed";
  if (text.includes("angry") || text.includes("annoy")) return "angry";
  if (text.includes("serious") || text.includes("thinking") || text.includes("focus")) return "serious";
  if (text.includes("curious") || text.includes("surprise") || text.includes("question")) return "curious";
  if (/^[a-z][a-z0-9_-]{1,47}$/.test(text)) return text;
  return "calm";
}

function messageID(prefix) {
  return `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}
