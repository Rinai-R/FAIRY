import React from "react";
import { createRoot } from "react-dom/client";

import { App } from "./App.jsx";
import "./styles.css";

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
        <span aria-hidden="true">✦</span>
        <h1>FAIRY 暂时无法显示</h1>
        <p>界面发生了未预期错误，请从菜单栏退出后重新启动。</p>
      </main>
    );
  }
}

createRoot(document.querySelector("#root")).render(
  <React.StrictMode>
    <RootErrorBoundary>
      <App />
    </RootErrorBoundary>
  </React.StrictMode>,
);
