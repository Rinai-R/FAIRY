import React from "react";
import { createRoot } from "react-dom/client";
import "./styles.css";
import { App } from "./App.jsx";

class RootErrorBoundary extends React.Component {
  constructor(props) {
    super(props);
    this.state = { error: null };
  }

  static getDerivedStateFromError(error) {
    return { error };
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <main className="stage-error-panel">
        <span className="eyebrow">FAIRY 前端错误</span>
        <h2>界面渲染失败</h2>
        <p>{this.state.error.message || "未知错误"}</p>
        <div className="header-actions">
          <button className="primary-button" type="button" onClick={() => window.location.reload()}>刷新页面</button>
        </div>
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
