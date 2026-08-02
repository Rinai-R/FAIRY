// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { Theme } from "@radix-ui/themes";
import { afterEach, describe, expect, it, vi } from "vitest";
import { IntegrationPage } from "./IntegrationPage";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function installBrowserGlobals() {
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    unobserve() {}
    disconnect() {}
  });
  vi.stubGlobal("localStorage", {
    getItem: () => "core-token",
    setItem: () => undefined,
    removeItem: () => undefined,
    clear: () => undefined,
    key: () => null,
    length: 0,
  });
}

describe("IntegrationPage", () => {
  it("loads, bulk adds, removes, and saves the server-normalized allowlist", async () => {
    installBrowserGlobals();
    const requests: Array<{ url: string; init?: RequestInit }> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      requests.push({ url: String(input), init });
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body));
        expect(body).toEqual({ groupAllowlist: ["123", "789"] });
        return new Response(JSON.stringify({ schemaVersion: 1, groupAllowlist: ["123", "789"] }), { status: 200 });
      }
      return new Response(JSON.stringify({ schemaVersion: 1, groupAllowlist: ["123", "456"] }), { status: 200 });
    }));

    render(<Theme><IntegrationPage onToast={() => undefined} /></Theme>);
    expect(await screen.findByText("123")).toBeTruthy();
    fireEvent.change(screen.getByRole("textbox", { name: "QQ 群号" }), { target: { value: "00456, 789\n789" } });
    fireEvent.click(screen.getByRole("button", { name: "添加" }));
    fireEvent.click(screen.getByRole("button", { name: "删除群 456" }));
    fireEvent.click(screen.getByRole("button", { name: "保存群配置" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/config/qq-onebot") && init?.method === "PUT")).toBe(true));
    expect(await screen.findByText("789")).toBeTruthy();
    expect(screen.queryByText("456")).toBeNull();
  });

  it("shows fail-closed empty state and never renders deployment credential fields", async () => {
    installBrowserGlobals();
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ schemaVersion: 1, groupAllowlist: [] }), { status: 200 })));
    render(<Theme><IntegrationPage onToast={() => undefined} /></Theme>);

    expect(await screen.findByText("当前拒绝全部 QQ 群")).toBeTruthy();
    expect(screen.queryByText(/PMHQ/i)).toBeNull();
    expect(screen.queryByText(/OneBot token/i)).toBeNull();
    expect(screen.queryByLabelText(/token/i)).toBeNull();
    const llonebot = screen.getByRole("link", { name: /LLOneBot/ });
    expect(llonebot.getAttribute("href")).toBe("http://127.0.0.1:3080");
  });

  it("keeps a visible error state when loading fails", async () => {
    installBrowserGlobals();
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: "配置文件不可读" }), { status: 500 })));
    render(<Theme><IntegrationPage onToast={() => undefined} /></Theme>);

    expect((await screen.findByRole("alert")).textContent).toContain("配置文件不可读");
    expect((screen.getByRole("button", { name: "保存群配置" }) as HTMLButtonElement).disabled).toBe(true);
  });
});
