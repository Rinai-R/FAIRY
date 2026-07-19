import { Theme, TextField, Text } from "@radix-ui/themes";
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
  { id: "overview", label: "概览" },
  { id: "character", label: "角色" },
  { id: "profile", label: "称呼" },
  { id: "model", label: "模型" },
  { id: "speech", label: "语音" },
  { id: "intelligence", label: "智能" },
  { id: "usage", label: "用量" },
  { id: "logs", label: "日志" },
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
    <Theme appearance="light" accentColor="cyan" grayColor="slate" radius="medium" scaling="100%">
      <div className="shell">
        <aside className="sidebar">
          <div className="brand">
            <span className="brand-mark" aria-hidden="true">✦</span>
            <div>
              <strong>FAIRY</strong>
              <small>控制台</small>
            </div>
          </div>
          <nav className="nav">
            {NAV.map((item) => (
              <button
                key={item.id}
                type="button"
                className={`nav-item ${section === item.id ? "active" : ""}`}
                onClick={() => setSection(item.id)}
              >
                {item.label}
              </button>
            ))}
          </nav>
          <div className="sidebar-foot">
            <Text as="label" size="1" color="gray">
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
