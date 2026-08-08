import { Button, Popover, Text, TextField, Theme } from "@radix-ui/themes";
import {
  BarChartIcon,
  ChatBubbleIcon,
  DashboardIcon,
  FileTextIcon,
  IdCardIcon,
  ImageIcon,
  LightningBoltIcon,
  LockClosedIcon,
  Link2Icon,
  MixerHorizontalIcon,
  PersonIcon,
  ReaderIcon,
  Share2Icon,
} from "@radix-ui/react-icons";
import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { API_UNAUTHORIZED_EVENT, ApiError, apiWithToken, getToken, setToken } from "./api";
import {
  CharacterPage,
  ModelPage,
  ProfilePage,
} from "./pages/CorePages";
import { IntelligencePage, OverviewPage } from "./pages/MorePages";
import { ObservabilityPage, type ObservabilityView } from "./pages/ObservabilityPage";
import { StickerPage } from "./pages/StickerPage";
import { IntegrationPage } from "./pages/IntegrationPage";
import { ConversationDebugPage } from "./pages/ConversationDebugPage";
import "@radix-ui/themes/styles.css";
import "./styles/console.css";

const NAV = [
  { id: "overview", label: "概览", icon: DashboardIcon },
  { id: "character", label: "角色", icon: PersonIcon },
  { id: "profile", label: "用户", icon: IdCardIcon },
  { id: "model", label: "模型", icon: MixerHorizontalIcon },
  { id: "stickers", label: "表情包", icon: ImageIcon },
  { id: "integrations", label: "接入", icon: Link2Icon },
  { id: "intelligence", label: "记忆与知识", icon: LightningBoltIcon },
  { id: "conversation-debug", label: "对话调试", icon: ChatBubbleIcon },
  { id: "metrics", label: "指标", icon: BarChartIcon },
  { id: "tracing", label: "链路跟踪", icon: Share2Icon },
  { id: "logs", label: "日志", icon: FileTextIcon },
] as const;

type Section = (typeof NAV)[number]["id"];
type ConnectionState = "missing" | "checking" | "ready" | "rejected" | "unavailable";

function isObservabilitySection(section: Section): section is ObservabilityView {
  return section === "metrics" || section === "tracing" || section === "logs";
}

function sectionFromHash(hash: string): Section {
  const route = hash.replace(/^#\/?/, "").trim().split("?", 1)[0] || "";
  const value = decodeURIComponent(route);
  return NAV.some((item) => item.id === value) ? value as Section : "overview";
}

function sectionHash(section: Section): string {
  return `#/${section}`;
}

const CONNECTION_COPY: Record<Exclude<ConnectionState, "ready">, { title: string; detail: string }> = {
  missing: {
    title: "连接 FAIRY Core",
    detail: "输入 Core 的 API Token，验证成功后才会读取管理数据。",
  },
  checking: {
    title: "正在连接 Core",
    detail: "正在验证保存的 Token 和 Core 状态。",
  },
  rejected: {
    title: "Core 拒绝了这个 Token",
    detail: "Token 已失效或与当前 Core 不一致，请输入新的 Token 后重试。",
  },
  unavailable: {
    title: "无法连接 Core",
    detail: "Core 暂时不可达。确认服务已启动后重试当前 Token。",
  },
};

function Brand() {
  return (
    <div className="brand">
      <span className="brand-mark" aria-hidden="true"><ReaderIcon /></span>
      <div className="brand-copy">
        <strong>FAIRY</strong>
        <small>本地陪伴管理台</small>
      </div>
    </div>
  );
}

function ConnectionGate({
  state,
  hasSavedToken,
  onConnect,
  onRetry,
}: {
  state: Exclude<ConnectionState, "ready">;
  hasSavedToken: boolean;
  onConnect: (token: string) => void;
  onRetry: () => void;
}) {
  const [draft, setDraft] = useState("");
  const copy = CONNECTION_COPY[state];
  const checking = state === "checking";

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const token = draft.trim();
    if (!token || checking) return;
    onConnect(token);
    setDraft("");
  }

  return (
    <div className="connection-shell">
      <div className="connection-panel">
        <Brand />
        <div className={`connection-state connection-state-${state}`} aria-live="polite">
          <span className="connection-state-mark" aria-hidden="true"><LockClosedIcon /></span>
          <div>
            <h1>{copy.title}</h1>
            <p>{copy.detail}</p>
          </div>
        </div>
        <form className="connection-form" onSubmit={submit}>
          <label htmlFor="core-api-token">Core API Token</label>
          <TextField.Root
            id="core-api-token"
            type="password"
            size="3"
            autoComplete="current-password"
            placeholder={hasSavedToken ? "输入新的 Token" : "Bearer Token"}
            value={draft}
            disabled={checking}
            onChange={(event) => setDraft(event.target.value)}
          />
          <div className="connection-actions">
            {hasSavedToken && state !== "missing" ? (
              <Button type="button" variant="soft" disabled={checking} onClick={onRetry}>
                重试当前 Token
              </Button>
            ) : null}
            <Button type="submit" disabled={checking || !draft.trim()}>
              {checking ? "正在验证" : "连接 Core"}
            </Button>
          </div>
        </form>
        <Text as="p" size="1" color="gray" className="connection-note">
          Token 只会随同源 `/v1` 请求发送，不会进入 URL。
        </Text>
      </div>
    </div>
  );
}

function ConnectionControl({ onConnect, onRetry }: { onConnect: (token: string) => void; onRetry: () => void }) {
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState("");

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const token = draft.trim();
    if (!token) return;
    setOpen(false);
    setDraft("");
    onConnect(token);
  }

  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger>
        <Button className="connection-trigger" variant="ghost" size="2" aria-label="Core 已连接">
          <LockClosedIcon aria-hidden="true" />
          <span>Core 已连接</span>
        </Button>
      </Popover.Trigger>
      <Popover.Content className="connection-popover" side="right" align="end" size="2">
        <form onSubmit={submit}>
          <Text as="label" size="2" weight="medium" htmlFor="replacement-core-token">
            更换 Core Token
          </Text>
          <TextField.Root
            id="replacement-core-token"
            type="password"
            size="2"
            mt="2"
            autoComplete="new-password"
            placeholder="输入新的 Token"
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
          />
          <div className="connection-popover-actions">
            <Button type="button" size="1" variant="ghost" onClick={() => { setOpen(false); onRetry(); }}>
              重新验证
            </Button>
            <Button type="submit" size="1" disabled={!draft.trim()}>
              更换 Token
            </Button>
          </div>
        </form>
      </Popover.Content>
    </Popover.Root>
  );
}

export default function App() {
  const [section, setSection] = useState<Section>(() => sectionFromHash(window.location.hash));
  const [initialToken] = useState(() => getToken());
  const [activeToken, setActiveToken] = useState(initialToken);
  const [connection, setConnection] = useState<ConnectionState>(initialToken ? "checking" : "missing");
  const [readyRevision, setReadyRevision] = useState(0);
  const [toast, setToast] = useState<{ message: string; error?: boolean } | null>(null);
  const attemptRef = useRef(0);

  const verifyToken = useCallback(async (candidate: string, persist: boolean) => {
    const token = candidate.trim();
    if (!token) {
      attemptRef.current += 1;
      setActiveToken("");
      setConnection("missing");
      return;
    }

    if (persist) setToken(token);
    setActiveToken(token);
    const attempt = ++attemptRef.current;
    setToast(null);
    setConnection("checking");

    try {
      await apiWithToken("/status", token);
      if (attempt !== attemptRef.current) return;
      setConnection("ready");
      setReadyRevision(attempt);
    } catch (error) {
      if (attempt !== attemptRef.current) return;
      setConnection(error instanceof ApiError && error.status === 401 ? "rejected" : "unavailable");
    }
  }, []);

  useEffect(() => {
    function onUnauthorized() {
      attemptRef.current += 1;
      setToast(null);
      setConnection("rejected");
    }
    window.addEventListener(API_UNAUTHORIZED_EVENT, onUnauthorized);
    return () => window.removeEventListener(API_UNAUTHORIZED_EVENT, onUnauthorized);
  }, []);

  useEffect(() => {
    if (initialToken) void verifyToken(initialToken, false);
  }, [initialToken, verifyToken]);

  useEffect(() => {
    const syncSection = () => setSection(sectionFromHash(window.location.hash));
    window.addEventListener("hashchange", syncSection);
    const parsedSection = sectionFromHash(window.location.hash);
    const hasKnownRoute = window.location.hash.replace(/^#\/?/, "").split("?", 1)[0] === parsedSection;
    if (!hasKnownRoute) window.history.replaceState(null, "", sectionHash(parsedSection));
    return () => window.removeEventListener("hashchange", syncSection);
  }, []);

  function onToast(message: string, error = false) {
    setToast({ message, error });
    if (!error) setTimeout(() => setToast(null), 2800);
  }

  function selectSection(next: Section) {
    setSection(next);
    if (window.location.hash !== sectionHash(next)) window.location.hash = sectionHash(next);
    window.scrollTo({ top: 0, left: 0, behavior: "auto" });
  }

  return (
    <Theme appearance="light" accentColor="blue" grayColor="slate" radius="small" scaling="100%">
      {connection !== "ready" ? (
        <ConnectionGate
          state={connection}
          hasSavedToken={Boolean(activeToken)}
          onConnect={(token) => void verifyToken(token, true)}
          onRetry={() => void verifyToken(activeToken, false)}
        />
      ) : (
        <div className="shell">
          <aside className="tool-rail">
            <Brand />
            <nav className="nav" aria-label="控制台导航">
              <div className="nav-primary">
                <span className="nav-section-label">工作台</span>
                {NAV.map((item) => {
                  const Icon = item.icon;
                  const active = section === item.id;
                  return (
                    <button
                      key={item.id}
                      type="button"
                      className={`nav-item ${active ? "active" : ""}`}
                      aria-current={active ? "page" : undefined}
                      onClick={() => selectSection(item.id)}
                    >
                      <span className="nav-icon" aria-hidden="true"><Icon /></span>
                      <span className="nav-label">{item.label}</span>
                    </button>
                  );
                })}
              </div>
            </nav>
            <div className="tool-rail-foot">
              <ConnectionControl
                onConnect={(token) => void verifyToken(token, true)}
                onRetry={() => void verifyToken(activeToken, false)}
              />
            </div>
          </aside>
          <main className="main" key={readyRevision}>
            <header className="shell-topline" aria-label="控制台状态">
              <div className="topline-path">
                <span>控制台</span>
                <i>/</i>
                <strong>{NAV.find((item) => item.id === section)?.label}</strong>
              </div>
              <div className="topline-health">
                <span className="core-status-dot" aria-hidden="true" />
                <strong>Core 正常</strong>
                <span className="topline-avatar" aria-hidden="true">F</span>
              </div>
            </header>
            <div className="main-canvas">
              {toast ? <div className={`toast ${toast.error ? "error" : ""}`} role="status">{toast.message}</div> : null}
              {section === "overview" ? <OverviewPage onToast={onToast} /> : null}
              {section === "character" ? <CharacterPage onToast={onToast} /> : null}
              {section === "profile" ? <ProfilePage onToast={onToast} /> : null}
              {section === "model" ? <ModelPage onToast={onToast} /> : null}
              {section === "stickers" ? <StickerPage onToast={onToast} /> : null}
              {section === "integrations" ? <IntegrationPage onToast={onToast} /> : null}
              {section === "intelligence" ? <IntelligencePage onToast={onToast} /> : null}
              {section === "conversation-debug" ? <ConversationDebugPage onOpenCharacters={() => selectSection("character")} /> : null}
              {isObservabilitySection(section) ? <ObservabilityPage token={activeToken} view={section} /> : null}
            </div>
          </main>
        </div>
      )}
    </Theme>
  );
}
