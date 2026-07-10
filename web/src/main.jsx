import React, { lazy, Suspense } from "react";
import { createRoot } from "react-dom/client";
import { CrossCircledIcon } from "@radix-ui/react-icons";
import { Theme } from "@radix-ui/themes";
import { MotionConfig } from "motion/react";

import { FAIRY_MOTION_TRANSITION, FAIRY_THEME } from "./uiTheme.mjs";
import { currentProductWindowLabel } from "./windowClient.js";
import "@radix-ui/themes/styles.css";
import "./styles/shared.css";
import "./styles/companion.css";
import "./styles/control-panel.css";

class RootErrorBoundary extends React.Component {
  constructor(props) {
    super(props);
    this.state = { failed: false };
  }

  static getDerivedStateFromError() {
    return { failed: true };
  }

  componentDidCatch(error) {
    console.error("FAIRY_RENDER_FAILURE", error);
  }

  render() {
    if (!this.state.failed) return this.props.children;
    return (
      <main className="fatal-error" role="alert">
        <CrossCircledIcon aria-hidden="true" />
        <h1>FAIRY 暂时无法显示</h1>
        <p>界面发生了未预期错误，请从菜单栏退出后重新启动。</p>
      </main>
    );
  }
}

function UnsupportedWindow() {
  return (
    <main className="fatal-error" role="alert">
      <CrossCircledIcon aria-hidden="true" />
      <h1>无法识别这个 FAIRY 窗口</h1>
      <p>当前窗口不属于 Companion 或控制面板，请从菜单栏重新打开。</p>
    </main>
  );
}

function ProductApp() {
  try {
    const ProductWindow = currentProductWindowLabel() === "companion"
      ? LazyCompanionApp
      : LazyControlPanelApp;
    return (
      <Suspense fallback={<div className="window-loading" aria-label="正在加载 FAIRY" />}>
        <ProductWindow />
      </Suspense>
    );
  } catch {
    return <UnsupportedWindow />;
  }
}

const LazyCompanionApp = lazy(() => import("./App.jsx").then(({ App }) => ({ default: App })));
const LazyControlPanelApp = lazy(() => import("./apps/ControlPanelApp.jsx").then(({ ControlPanelApp }) => ({ default: ControlPanelApp })));

createRoot(document.querySelector("#root")).render(
  <React.StrictMode>
    <RootErrorBoundary>
      <MotionConfig reducedMotion="user" transition={FAIRY_MOTION_TRANSITION}>
        <Theme {...FAIRY_THEME}>
          <ProductApp />
        </Theme>
      </MotionConfig>
    </RootErrorBoundary>
  </React.StrictMode>,
);
