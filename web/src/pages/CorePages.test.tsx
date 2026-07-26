// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { Theme } from "@radix-ui/themes";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ModelPage } from "./CorePages";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("ModelPage vision capability", () => {
  it("loads and saves visionInput explicitly", async () => {
    vi.stubGlobal("ResizeObserver", class {
      observe() {}
      unobserve() {}
      disconnect() {}
    });
    vi.stubGlobal("localStorage", {
      getItem: () => "",
      setItem: () => undefined,
      removeItem: () => undefined,
      clear: () => undefined,
      key: () => null,
      length: 0,
    });
    const requests: RequestInit[] = [];
    vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "PUT") {
        requests.push(init);
      }
      return new Response(JSON.stringify({
        configured: true,
        protocol: "chat_completions",
        endpoint: "https://api.deepseek.com",
        model: "deepseek-v4-flash",
        contextWindowTokens: 1048576,
        authMode: "bearer_key",
        capabilities: { visionInput: false },
      }), { status: 200, headers: { "Content-Type": "application/json" } });
    }));

    render(<Theme><ModelPage onToast={() => undefined} /></Theme>);
    const toggle = await screen.findByRole("switch", { name: "视觉输入" });
    expect(toggle.getAttribute("data-state")).toBe("unchecked");
    fireEvent.click(toggle);
    fireEvent.click(screen.getByRole("button", { name: "保存连接" }));

    await waitFor(() => expect(requests).toHaveLength(1));
    expect(JSON.parse(String(requests[0].body))).toMatchObject({ visionInput: true });
  });
});
