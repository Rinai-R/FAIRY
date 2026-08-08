// @vitest-environment jsdom
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { Theme } from "@radix-ui/themes";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { OverviewPage, summarizeRoleDescription } from "./MorePages";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

beforeEach(() => {
  vi.stubGlobal("localStorage", {
    getItem: () => "",
    setItem: () => undefined,
    removeItem: () => undefined,
    clear: () => undefined,
    key: () => null,
    length: 0,
  });
});

function json(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

describe("OverviewPage information hierarchy", () => {
  it("projects a bounded role summary and groups runtime capabilities once", async () => {
    const description = [
      "亚托莉是从海底被打捞出来的机器人少女，拥有鲜活自然的表达方式。",
      "她希望找回缺失的记忆，并完成前任主人留下的最后命令。",
      "这段完整设定只应在角色编辑页出现，不应继续撑高概览首屏。",
    ].join("\n\n");

    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).endsWith("/characters")) {
        return json({ active: { name: "亚托莉", description }, characters: [] });
      }
      return json({
        model: { configured: true, model: "deepseek-v4-flash", endpoint: "https://api.deepseek.com" },
        webSearch: { enabled: true, baseUrl: "http://openserp:7000" },
        semanticEmbedding: { configured: true, enabled: true, provider: "siliconflow", model: "BAAI/bge-m3" },
      });
    }));

    render(<Theme><OverviewPage onToast={() => undefined} /></Theme>);

    expect(await screen.findByRole("heading", { name: "亚托莉" })).toBeTruthy();
    expect(screen.getByText(/亚托莉是从海底被打捞出来的机器人少女/).textContent?.endsWith("…")).toBe(true);
    expect(screen.queryByText(/这段完整设定只应/)).toBeNull();
    expect(screen.getByText("完整设定在角色页维护")).toBeTruthy();

    const runtime = screen.getByRole("complementary", { name: "运行状态" });
    expect(within(runtime).getByText("Core 运行正常")).toBeTruthy();
    expect(within(runtime).getAllByText("模型")).toHaveLength(1);
    expect(within(runtime).getAllByText("公开检索")).toHaveLength(1);
    expect(within(runtime).getAllByText("语义嵌入")).toHaveLength(1);
  });

  it("keeps summary generation deterministic and bounded", () => {
    const summary = summarizeRoleDescription(`  第一段有多余空白。\n\n第二段继续描述。${"很长".repeat(100)}。第三段不应出现。`);
    expect(summary).not.toMatch(/\s{2,}/u);
    expect(Array.from(summary).length).toBeLessThanOrEqual(161);
    expect(summary.endsWith("…")).toBe(true);
  });

  it("shows explicit empty and pending states without blank columns", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).endsWith("/characters")) return json({ active: null, characters: [] });
      return json({
        model: { configured: false },
        webSearch: { enabled: false },
        semanticEmbedding: { configured: false, enabled: false, provider: "none" },
      });
    }));

    render(<Theme><OverviewPage onToast={() => undefined} /></Theme>);

    await waitFor(() => expect(screen.getByText("还没有激活角色")).toBeTruthy());
    expect(screen.getByText("等待角色")).toBeTruthy();
    expect(screen.getByText("Core 运行正常")).toBeTruthy();
    expect(screen.getAllByText("待配置")).toHaveLength(3);
  });
});
