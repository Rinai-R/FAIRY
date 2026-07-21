import { Theme, TextField, Text } from "@radix-ui/themes";
import {
  ActivityLogIcon,
  BarChartIcon,
  DashboardIcon,
  IdCardIcon,
  LightningBoltIcon,
  LockClosedIcon,
  MixerHorizontalIcon,
  PersonIcon,
  SpeakerLoudIcon,
  StarFilledIcon,
} from "@radix-ui/react-icons";
import { useState } from "react";
import { getToken, setToken } from "./api";
import {
  CharacterPage,
  ModelPage,
  ProfilePage,
  SpeechPage,
} from "./pages/CorePages";
import { IntelligencePage, OverviewPage, UsagePage } from "./pages/MorePages";
import { ObservabilityPage } from "./pages/ObservabilityPage";
import "@radix-ui/themes/styles.css";
import "./styles/pure.css";

const NAV = [
  { id: "overview", label: "概览", icon: DashboardIcon },
  { id: "character", label: "角色", icon: PersonIcon },
  { id: "profile", label: "称呼", icon: IdCardIcon },
  { id: "model", label: "模型", icon: MixerHorizontalIcon },
  { id: "speech", label: "语音", icon: SpeakerLoudIcon },
  { id: "intelligence", label: "智能", icon: LightningBoltIcon },
  { id: "usage", label: "用量", icon: BarChartIcon },
  { id: "logs", label: "日志", icon: ActivityLogIcon },
] as const;

type Section = (typeof NAV)[number]["id"];

export default function App() {
  const [section, setSection] = useState<Section>("overview");
  const [token, setTokenState] = useState(getToken());
  const [toast, setToast] = useState<{ message: string; error?: boolean } | null>(null);

  function onToast(message: string, error = false) {
    setToast({ message, error });
    if (!error) setTimeout(() => setToast(null), 2800);
  }

  return (
    <Theme appearance="light" accentColor="teal" grayColor="sand" radius="large" scaling="100%">
      <div className="shell">
        <aside className="sidebar">
          <div className="brand">
            <span className="brand-mark" aria-hidden="true"><StarFilledIcon /></span>
            <div>
              <strong>FAIRY</strong>
              <small>控制台</small>
            </div>
          </div>
          <nav className="nav">
            {NAV.map((item) => {
              const Icon = item.icon;
              const active = section === item.id;
              return (
                <button
                  key={item.id}
                  type="button"
                  className={`nav-item ${active ? "active" : ""}`}
                  aria-current={active ? "page" : undefined}
                  onClick={() => setSection(item.id)}
                >
                  <span className="nav-icon" aria-hidden="true"><Icon /></span>
                  <span className="nav-label">{item.label}</span>
                </button>
              );
            })}
          </nav>
          <div className="sidebar-foot">
            <Text as="label" size="1" color="gray" className="token-label">
              <LockClosedIcon aria-hidden="true" />
              API Token
            </Text>
            <TextField.Root
              type="password"
              size="2"
              mt="1"
              placeholder="可选 Bearer"
              value={token}
              onChange={(e) => {
                setTokenState(e.target.value);
                setToken(e.target.value);
              }}
            />
          </div>
        </aside>
        <main className="main">
          {toast ? <div className={`toast ${toast.error ? "error" : ""}`}>{toast.message}</div> : null}
          {section === "overview" ? <OverviewPage onToast={onToast} /> : null}
          {section === "character" ? <CharacterPage onToast={onToast} /> : null}
          {section === "profile" ? <ProfilePage onToast={onToast} /> : null}
          {section === "model" ? <ModelPage onToast={onToast} /> : null}
          {section === "speech" ? <SpeechPage onToast={onToast} /> : null}
          {section === "intelligence" ? <IntelligencePage onToast={onToast} /> : null}
          {section === "usage" ? <UsagePage onToast={onToast} /> : null}
          {section === "logs" ? <ObservabilityPage token={token} /> : null}
        </main>
      </div>
    </Theme>
  );
}
