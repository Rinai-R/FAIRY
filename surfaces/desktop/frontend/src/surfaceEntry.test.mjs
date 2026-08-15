import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

const mainSource = readFileSync(new URL("./main.jsx", import.meta.url), "utf8");
const surfaceSource = readFileSync(new URL("./surface.jsx", import.meta.url), "utf8");
const controlPanelStyles = readFileSync(new URL("./styles/control-panel.css", import.meta.url), "utf8");
const desktopMainSource = readFileSync(new URL("../../main.go", import.meta.url), "utf8");

test("production entry uses the standalone CoreService surface router", () => {
  assert.match(mainSource, /import \{ SurfaceApp \} from "\.\/surface\.jsx"/);
  assert.match(surfaceSource, /\.\.\/bindings\/fairy-desktop\/coreservice\.js/);
  assert.match(surfaceSource, /if \(surface === "control-panel"\)/);
  assert.match(surfaceSource, /if \(surface === "history"\)/);
  assert.match(surfaceSource, /if \(surface === "speech"\)/);
  assert.match(surfaceSource, /if \(surface === "management"\)/);
  assert.doesNotMatch(mainSource + surfaceSource, /fairy\/frontend\/bindings\/fairy\/desktop/);
  assert.doesNotMatch(mainSource + surfaceSource, /GetDesktopState|OpenCompanionChat/);
});

test("Desktop sends text turns and renders ordered display beats", () => {
  assert.match(surfaceSource, /Send\(input\)/);
  assert.doesNotMatch(surfaceSource, /createSpeechPlayback|new Audio|dataUrl|audioUnavailable/);
  assert.match(surfaceSource, /const newIdentifiedTurn = nextTurnId && bubbleRef\.current\.turnId !== nextTurnId/);
  assert.match(surfaceSource, /if \(newIdentifiedTurn \|\| localPendingTurn\)/);
  assert.match(surfaceSource, /if \(newIdentifiedTurn \|\| localPendingTurn\) \{\s+return waitingPhase\s+\? \{ visible: true, waiting: true, settled: false, turnId: nextTurnId, parts: \[\] \}/);
  assert.match(surfaceSource, /turn\?\.state === "interrupted"/);
  assert.match(surfaceSource, /if \(isDesktopTurnAborted\(turn\)\) \{/);
  const abortBranch = surfaceSource.indexOf("if (isDesktopTurnAborted(turn)) {");
  const genericStateBranch = surfaceSource.indexOf('if (turn.type === "state_changed") {', abortBranch);
  assert.ok(abortBranch >= 0 && genericStateBranch > abortBranch);
});

test("speech surface bounds sticker delivery receipt deduplication", () => {
  assert.match(surfaceSource, /MAX_REPORTED_STICKER_RECEIPTS = 16/);
  assert.match(surfaceSource, /reportedStickersRef\.current\.delete\(oldest\)/);
});

test("companion surface projects direct and proactive Turn events into one active state", () => {
  assert.match(surfaceSource, /import \{ projectDesktopTurnActive \} from "\.\/turnViewState\.mjs"/);
  assert.match(surfaceSource, /setActive\(\(current\) => projectDesktopTurnActive\(current, turn\)\)/);
  assert.match(surfaceSource, /disabled=\{!session \|\| active\}/);
  assert.match(surfaceSource, /active \? <IconButton[^>]+aria-label="停止回复"/);
});

test("settings surface shows local runtime status instead of Core connection fields", () => {
  assert.match(surfaceSource, /RuntimeInfo\(\)/);
  assert.match(surfaceSource, /Connect\(\)/);
  assert.match(surfaceSource, /id="quick-panel-status-title"/);
  assert.match(surfaceSource, /打开管理工作区/);
  assert.match(surfaceSource, /OpenManagement\(\)/);
  assert.doesNotMatch(surfaceSource, /ConnectionSettings|SaveConnection|保存连接配置|defaultEndpoint/);
  assert.doesNotMatch(surfaceSource, /127\.0\.0\.1:8787|FAIRY_API_TOKEN|Core 地址|采样间隔|离开阈值/);
  assert.doesNotMatch(surfaceSource, /Keychain|keychain/);
  assert.doesNotMatch(surfaceSource, /fairy\.endpoint(?:Key)?/);
  assert.doesNotMatch(surfaceSource, /localStorage\.(?:getItem|setItem)\([^)]*(?:endpoint|token)/);
});

test("Core settings keep every field accessible in a resizable scrolling WebUI panel", () => {
  assert.match(surfaceSource, /className="cp-settings-scroll" data-testid="settings-scroll-region"/);
  assert.match(surfaceSource, /className="cp-settings-section" aria-labelledby="quick-panel-status-title"/);
  assert.match(surfaceSource, /className="cp-settings-section" aria-labelledby="quick-panel-switches-title"/);
  assert.match(surfaceSource, /className="cp-settings-status" role="status"/);
  assert.match(controlPanelStyles, /--cp-bg:\s*#f4f8fc/);
  assert.match(controlPanelStyles, /--cp-blue:\s*#2878d0/);
  assert.match(controlPanelStyles, /\.cp-settings-scroll\s*\{[^}]*overflow-y:\s*scroll[^}]*scrollbar-gutter:\s*stable/s);
  assert.match(controlPanelStyles, /\.cp-settings-scroll::-webkit-scrollbar/);
  assert.match(controlPanelStyles, /\.cp-settings-status\s*\{[^}]*overflow-wrap:\s*anywhere[^}]*white-space:\s*normal/s);

  const settingsWindowStart = desktopMainSource.indexOf("settings := app.Window.NewWithOptions");
  const historyWindowStart = desktopMainSource.indexOf("history := app.Window.NewWithOptions", settingsWindowStart);
  assert.ok(settingsWindowStart >= 0 && historyWindowStart > settingsWindowStart);
  const settingsWindow = desktopMainSource.slice(settingsWindowStart, historyWindowStart);
  assert.match(desktopMainSource, /controlPanelWidth\s*=\s*420/);
  assert.match(desktopMainSource, /controlPanelHeight\s*=\s*560/);
  assert.match(settingsWindow, /MinWidth:\s*controlPanelMinWidth/);
  assert.match(settingsWindow, /MinHeight:\s*controlPanelMinHeight/);
  assert.doesNotMatch(settingsWindow, /DisableResize:\s*true/);
  assert.match(desktopMainSource, /WindowDidMove[\s\S]*core\.repositionControlPanel\(\)/);
});

test("management workspace is a resizable local shell with sibling observability tasks", () => {
  const managementSource = readFileSync(new URL("./management.jsx", import.meta.url), "utf8");
  assert.match(desktopMainSource, /Name: "management"/);
  assert.match(desktopMainSource, /MinWidth: managementMinWidth/);
  assert.match(desktopMainSource, /MinHeight: managementMinHeight/);
  const managementWindowStart = desktopMainSource.indexOf("management := app.Window.NewWithOptions");
  const managementWindow = desktopMainSource.slice(managementWindowStart, desktopMainSource.indexOf("bubble.SetIgnoreMouseEvents"));
  assert.doesNotMatch(managementWindow, /DisableResize:\s*true/);
  assert.doesNotMatch(managementWindow, /AlwaysOnTop:\s*true/);
  assert.match(desktopMainSource, /installApplicationMenu\(app, core\)/);
  assert.match(managementSource, /aria-label="控制台导航"/);
  assert.match(managementSource, /label: "指标"/);
  assert.match(managementSource, /label: "链路跟踪"/);
  assert.match(managementSource, /label: "日志"/);
  assert.doesNotMatch(managementSource, /observability-tabs|nav-subtasks|ConnectionGate|Bearer|127\.0\.0\.1:8787|FAIRY_API_TOKEN/);
  assert.match(managementSource, /不要求 Core endpoint 或 bearer/);
  assert.match(managementSource, /label: "插件"/);
  assert.match(managementSource, /label: "备份"/);
  assert.match(managementSource, /ManagementOverview\(\)/);
  assert.match(managementSource, /SaveManagementModel\(/);
  assert.match(managementSource, /SubscribeManagementLogs\(\)/);
  assert.match(managementSource, /CreateManagementBackup\(\)/);
  assert.match(managementSource, /ManagementWorkspaceState\(\)/);
  assert.match(managementSource, /SaveManagementWorkspaceState\(/);
  assert.doesNotMatch(managementSource, /fetch\s*\(|fairy\.apiToken|Authorization|\/v1\//);
});

test("management workspace keeps canvas scrolling and hover diagnostics inside the window", () => {
  const managementSource = readFileSync(new URL("./management.jsx", import.meta.url), "utf8");
  const managementStyles = readFileSync(new URL("./styles/management.css", import.meta.url), "utf8");
  assert.match(desktopMainSource, /MinWidth: managementMinWidth/);
  assert.match(desktopMainSource, /MinHeight: managementMinHeight/);
  assert.match(managementStyles, /min-width:\s*960px/);
  assert.match(managementStyles, /height:\s*100vh/);
  assert.match(managementStyles, /\.shell\s*\{[^}]*overflow:\s*hidden/s);
  assert.match(managementStyles, /\.main-canvas\s*\{[^}]*overflow-y:\s*scroll[^}]*scrollbar-gutter:\s*stable/s);
  assert.match(managementStyles, /@media \(min-width: 1600px\)/);
  assert.match(managementStyles, /\.trace-span:hover \.trace-span__tip/);
  assert.match(managementStyles, /\.metric-bar:hover \.metric-bar__tip/);
  assert.match(managementStyles, /max-width:\s*min\(360px, 70vw\)/);
  assert.match(managementSource, /data-testid="trace-timeline"/);
  assert.match(managementSource, /data-testid="metric-chart"/);
});
