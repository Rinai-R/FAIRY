// @vitest-environment node
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const styles = readFileSync(new URL("./console.css", import.meta.url), "utf8");
const appSource = readFileSync(new URL("../App.tsx", import.meta.url), "utf8");
const uiSource = readFileSync(new URL("../components/ui.tsx", import.meta.url), "utf8");
const observabilitySource = readFileSync(new URL("../pages/ObservabilityPage.tsx", import.meta.url), "utf8");

function token(name: string): string {
  const match = styles.match(new RegExp(`--${name}:\\s*(#[0-9a-fA-F]{6})`));
  if (!match) throw new Error(`missing color token ${name}`);
  return match[1];
}

function contrast(foreground: string, background: string): number {
  const luminance = (hex: string) => {
    const channels = [1, 3, 5].map((index) => Number.parseInt(hex.slice(index, index + 2), 16) / 255);
    const linear = channels.map((value) => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4);
    return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2];
  };
  const light = Math.max(luminance(foreground), luminance(background));
  const dark = Math.min(luminance(foreground), luminance(background));
  return (light + 0.05) / (dark + 0.05);
}

describe("restrained blue-white product language", () => {
  it("keeps one light Radix system and a low-saturation palette", () => {
    expect(appSource).toContain('import "./styles/console.css"');
    expect(appSource).toContain('accentColor="blue" grayColor="slate" radius="small"');
    expect(styles).toContain("color-scheme: light");
    expect(styles).toContain("--fairy-bg: #f4f8fc");
    expect(styles).toContain("--fairy-surface: #ffffff");
    expect(styles).toContain("--fairy-blue-soft: #edf6ff");
    expect(styles).toContain("--fairy-blue: #2878d0");
    expect(styles).toContain("--fairy-ink: #17283b");
    expect(styles).not.toMatch(/#0b0d14|#0e1117|#12151d|--fairy-(?:paper|binding)/i);
  });

  it("keeps principal, secondary, action and danger text above WCAG AA contrast", () => {
    expect(contrast(token("fairy-ink"), token("fairy-surface"))).toBeGreaterThanOrEqual(4.5);
    expect(contrast(token("fairy-muted"), token("fairy-surface"))).toBeGreaterThanOrEqual(4.5);
    expect(contrast(token("fairy-surface"), token("fairy-blue-deep"))).toBeGreaterThanOrEqual(4.5);
    expect(contrast(token("fairy-danger"), token("fairy-surface"))).toBeGreaterThanOrEqual(4.5);
  });

  it("removes the previous showcase-style decoration system", () => {
    expect(styles).not.toMatch(/clip-path|writing-mode|text-orientation|radial-gradient|repeating-linear-gradient/i);
    expect(styles).not.toMatch(/skew[XY]?\(|rotate\(/i);
    expect(styles).not.toMatch(/\.page-header::(?:before|after)|\.main-canvas::(?:before|after)/);
    expect(styles).not.toMatch(/font-size:\s*clamp\([^;]*(?:54px|7\.2vw|112px)/);
  });
});

describe("stable application shell and page hierarchy", () => {
  it("uses a fixed label sidebar, compact header and controlled content width", () => {
    expect(appSource).toContain('className="nav-section-label"');
    expect(appSource).toContain('className="topline-path"');
    expect(appSource).toContain('className="topline-health"');
    expect(styles).toContain("--fairy-sidebar: 240px");
    expect(styles).toContain("--fairy-topbar: 60px");
    expect(styles).toContain("--fairy-content: 1440px");
    expect(styles).toMatch(/html\s*\{[\s\S]*?scrollbar-gutter:\s*stable/);
    expect(styles).toMatch(/\.shell\s*\{[\s\S]*?grid-template-columns:\s*var\(--fairy-sidebar\) minmax\(0, 1fr\)/);
    expect(styles).toMatch(/\.tool-rail\s*\{[\s\S]*?position:\s*sticky;[\s\S]*?background:\s*var\(--fairy-surface\)/);
    expect(styles).toMatch(/\.shell-topline\s*\{[\s\S]*?background:\s*var\(--fairy-surface\)/);
    expect(styles).toMatch(/\.nav-item\s*\{[\s\S]*?min-height:\s*44px;[\s\S]*?text-align:\s*left/);
    expect(styles).toMatch(/\.main-canvas\s*\{[\s\S]*?width:\s*min\(100%, var\(--fairy-content\)\);[\s\S]*?margin:\s*0 auto/);
  });

  it("uses task-scale headings and consistent control geometry", () => {
    expect(styles).toMatch(/\.page-heading-copy h1\s*\{[\s\S]*?font-size:\s*clamp\(27px, 2vw, 32px\)/);
    expect(styles).toContain("--fairy-card-radius: 12px");
    expect(styles).toContain("--fairy-control-radius: 8px");
    expect(styles).toContain("--fairy-focus: 0 0 0 3px rgba(40, 120, 208, 0.2)");
    expect(styles).toMatch(/\.rt-TextFieldRoot,[\s\S]*?min-height:\s*42px;[\s\S]*?background:\s*var\(--fairy-surface\)/);
    expect(styles).toMatch(/\.form-grid \.field > \.hint\s*\{[\s\S]*?min-height:\s*17px/);
    expect(styles).toMatch(/\.form-grid \.field > \.hint\.empty\s*\{\s*visibility:\s*hidden/);
  });

  it("places configuration headings above auto-height form bodies", () => {
    expect(uiSource.indexOf("config-section-heading")).toBeLessThan(uiSource.indexOf("config-section-body"));
    expect(styles).toMatch(/\.config-section-heading\s*\{[\s\S]*?border-bottom:\s*1px solid var\(--fairy-line\)/);
    expect(styles).toMatch(/\.config-section-body\s*\{[\s\S]*?padding:\s*22px 24px 24px;[\s\S]*?gap:\s*18px/);
    const configSectionRule = styles.slice(styles.indexOf(".config-section {"), styles.indexOf(".config-section-heading"));
    expect(configSectionRule).not.toContain("grid-template-columns");
  });
});

describe("task structures, responsive layout and data surfaces", () => {
  it("keeps task-specific structures without geometric decoration", () => {
    expect(styles).toMatch(/\.overview-dashboard\s*\{[\s\S]*?grid-template-columns:\s*minmax\(0, 1\.55fr\) minmax\(380px, 1fr\)/);
    expect(styles).toMatch(/\.companion-summary\s*\{[\s\S]*?grid-template-columns:\s*112px minmax\(0, 1fr\)/);
    expect(styles).toMatch(/\.runtime-summary\s*\{[\s\S]*?grid-template-rows:\s*auto 1fr/);
    expect(styles).not.toMatch(/\.overview-identity|\.overview-systems|\.core-summary|\.system-row/);
    expect(styles).toMatch(/\.master-detail\s*\{[\s\S]*?grid-template-columns:\s*minmax\(270px, 0\.72fr\) minmax\(0, 1\.8fr\)/);
    expect(styles).toMatch(/\.ledger-summary\s*\{[\s\S]*?grid-template-columns:\s*repeat\(4, minmax\(0, 1fr\)\)/);
    expect(styles).toMatch(/\.metrics-trend-grid\s*\{[\s\S]*?grid-template-columns:\s*repeat\(12, minmax\(0, 1fr\)\)/);
    expect(styles).toMatch(/\.metric-trend-chart\.wide\s*\{\s*grid-column:\s*span 7/);
    expect(styles).toMatch(/\.usage-summary\s*\{[\s\S]*?grid-template-columns:\s*repeat\(4, minmax\(0, 1fr\)\)/);
  });

  it("places observability tasks in the global navigation and keeps one mounted panel per concern", () => {
    expect(observabilitySource).toContain('className="observability-workbench"');
    expect(observabilitySource).toContain('export type ObservabilityView = "metrics" | "tracing" | "logs"');
    expect(appSource).toContain('{ id: "metrics", label: "指标"');
    expect(appSource).toContain('{ id: "tracing", label: "链路跟踪"');
    expect(appSource).toContain('{ id: "logs", label: "日志"');
    expect(appSource).not.toContain('{ id: "observability"');
    expect(appSource).not.toContain('className="nav-subtasks"');
    expect(appSource).not.toContain('role="tablist"');
    expect(observabilitySource).toContain('role="region"');
    expect(observabilitySource).not.toContain('role="tabpanel"');
    expect(observabilitySource).not.toContain('className="observability-tabs"');
    expect(observabilitySource).toContain('<MetricsTrendDashboard history={metricsTrend} messagesAvailable={metrics.messagesAvailable} />');
    expect(observabilitySource).toContain('<UsageDashboard report={metrics.usage} />');
    expect(observabilitySource).toContain('<LiveLogPanel token={token} />');
    expect(observabilitySource).not.toContain('title="消息时延"');
    expect(observabilitySource).not.toContain('接收 → Turn 开始');
    expect(observabilitySource).not.toContain('{ id: "overview", label: "运行总览" }');
    expect(observabilitySource).not.toContain('{ id: "usage", label: "模型用量" }');
    expect(observabilitySource).not.toContain('{ id: "logs", label: "实时日志" }');
    expect(styles).not.toMatch(/\.workspace-tabs|\.workspace-panel/);
    expect(styles).not.toMatch(/\.observability-tabs/);
    expect(styles).not.toMatch(/\.nav-subtasks|\.nav-subtask|\.tool-rail\.observability-open/);
    expect(styles).toMatch(/\.observability-workbench\s*\{[\s\S]*?display:\s*grid;[\s\S]*?gap:\s*18px/);
    expect(styles).toMatch(/\.observability-panel\[hidden\]\s*\{\s*display:\s*none/);
  });

  it("defines compact desktop, mobile and narrow-mobile reflow", () => {
    expect(styles).toContain("@media (max-width: 1080px)");
    expect(styles).toMatch(/@media \(max-width: 1080px\)[\s\S]*?\.shell\s*\{\s*display:\s*block;/);
    expect(styles).toMatch(/@media \(max-width: 1080px\)[\s\S]*?\.nav-primary\s*\{[\s\S]*?flex-direction:\s*row;[\s\S]*?overflow-x:\s*auto/);
    expect(styles).toMatch(/@media \(max-width: 760px\)[\s\S]*?\.overview-dashboard\s*\{\s*grid-template-columns:\s*1fr/);
    expect(styles).toMatch(/@media \(max-width: 760px\)[\s\S]*?\.form-grid-2,[\s\S]*?grid-template-columns:\s*1fr/);
    expect(styles).toMatch(/@media \(max-width: 760px\)[\s\S]*?\.observability-section > \.section-heading\s*\{[\s\S]*?flex-direction:\s*column/);
    expect(styles).toMatch(/@media \(max-width: 760px\)[\s\S]*?\.observability-section > \.section-heading \.section-heading-aside\s*\{[\s\S]*?white-space:\s*normal/);
    expect(styles).toMatch(/@media \(max-width: 760px\)[\s\S]*?\.metrics-trend-grid\s*\{[\s\S]*?grid-template-columns:\s*1fr/);
    expect(styles).toMatch(/\.debug-runtime-heading\s*\{[\s\S]*?flex-wrap:\s*wrap/);
    expect(styles).toMatch(/\.debug-runtime-toggle\.rt-Button\s*\{[\s\S]*?max-width:\s*100%/);
    expect(styles).toMatch(/@media \(max-width: 760px\)[\s\S]*?\.debug-runtime-section\s*\{[\s\S]*?padding-left:\s*0;[\s\S]*?border-left:\s*0/);
    expect(styles).toContain("@media (max-width: 439px)");
    expect(styles).toContain("@media (prefers-reduced-motion: reduce)");
  });

  it("keeps tables and logs locally scrollable on one light data surface", () => {
    expect(styles).toMatch(/\.table-scroll\s*\{[\s\S]*?max-width:\s*100%;[\s\S]*?overflow:\s*auto/);
    expect(styles).toMatch(/\.usage-recent > \.table-scroll\s*\{[\s\S]*?max-height:\s*360px;[\s\S]*?overflow:\s*auto/);
    expect(styles).toMatch(/\.usage-recent \.data-table thead th\s*\{[\s\S]*?position:\s*sticky;[\s\S]*?top:\s*0/);
    expect(styles).toMatch(/\.log-list\s*\{[\s\S]*?overflow:\s*auto;[\s\S]*?background:\s*var\(--fairy-surface\)/);
    const logStyles = styles.slice(styles.indexOf("/* Light log surface */"), styles.indexOf("/* Core connection gate */"));
    expect(logStyles).not.toMatch(/background:\s*#(?:000|0b0f10|0d1315|111719|12191b)\b/i);
    expect(styles).toMatch(/\.log-row\s*\{[\s\S]*?min-width:\s*760px/);
    expect(styles).toMatch(/@media \(max-width: 760px\)[\s\S]*?\.log-row\s*\{[\s\S]*?min-width:\s*0;[\s\S]*?grid-template-columns:\s*72px 54px minmax\(0, 1fr\)/);
    expect(styles).toMatch(/@media \(max-width: 760px\)[\s\S]*?\.log-content\s*\{\s*grid-column:\s*1 \/ -1/);
    expect(styles).toMatch(/\.memory-panel\s*\{\s*min-height:\s*0;/);
  });
});
