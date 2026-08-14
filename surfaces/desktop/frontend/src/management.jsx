import { useEffect, useState } from "react";
import {
  BarChartIcon,
  ChatBubbleIcon,
  DashboardIcon,
  FileTextIcon,
  IdCardIcon,
  ImageIcon,
  LightningBoltIcon,
  Link2Icon,
  MixerHorizontalIcon,
  PersonIcon,
  ReaderIcon,
  Share2Icon,
} from "@radix-ui/react-icons";

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
];

function sectionFromHash(hash) {
  const route = hash.replace(/^#\/?/, "").trim().split("?", 1)[0] || "";
  const value = decodeURIComponent(route);
  return NAV.some((item) => item.id === value) ? value : "overview";
}

function sectionHash(section) {
  return `#/${section}`;
}

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

function ManagementTask({ section }) {
  const item = NAV.find((entry) => entry.id === section);
  const observability = section === "metrics" || section === "tracing" || section === "logs";
  return (
    <section
      className={observability ? "observability-page" : "management-task"}
      id={observability ? `observability-panel-${section}` : undefined}
      aria-labelledby="management-task-title"
    >
      <header className="management-task__header">
        <h1 id="management-task-title">{item?.label}</h1>
        <p>此任务由本地宿主提供数据，不要求 Core endpoint 或 bearer。</p>
      </header>
    </section>
  );
}

export function ManagementSurface() {
  const [section, setSection] = useState(() => sectionFromHash(window.location.hash));

  useEffect(() => {
    const syncSection = () => setSection(sectionFromHash(window.location.hash));
    window.addEventListener("hashchange", syncSection);
    const parsedSection = sectionFromHash(window.location.hash);
    const hasKnownRoute = window.location.hash.replace(/^#\/?/, "").split("?", 1)[0] === parsedSection;
    if (!hasKnownRoute) window.history.replaceState(null, "", sectionHash(parsedSection));
    return () => window.removeEventListener("hashchange", syncSection);
  }, []);

  function selectSection(next) {
    setSection(next);
    if (window.location.hash !== sectionHash(next)) window.location.hash = sectionHash(next);
  }

  return (
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
                  className={`nav-item${active ? " active" : ""}`}
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
      </aside>
      <main className="main">
        <header className="shell-topline" aria-label="控制台状态">
          <div className="topline-path">
            <span>管理工作区</span>
            <i>/</i>
            <strong>{NAV.find((item) => item.id === section)?.label}</strong>
          </div>
          <div className="topline-health">
            <span className="core-status-dot" aria-hidden="true" />
            <strong>本地运行时</strong>
          </div>
        </header>
        <div className="main-canvas">
          <ManagementTask section={section} />
        </div>
      </main>
    </div>
  );
}
