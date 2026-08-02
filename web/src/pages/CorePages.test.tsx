// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { Theme } from "@radix-ui/themes";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ModelPage } from "./CorePages";
import { IntelligencePage } from "./MorePages";

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
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "PUT") {
        requests.push(init);
      }
      if (String(input).endsWith("/config/semantic-embedding")) {
        return new Response(JSON.stringify({
          provider: "none", enabled: false, endpoint: "", model: "", dimensions: 0,
          credentialConfigured: false, reason: "semantic_embedding_disabled",
        }), { status: 200, headers: { "Content-Type": "application/json" } });
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

  it("projects legacy semantic status without replacing its dimensions", async () => {
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
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const body = String(input).endsWith("/config/semantic-embedding")
        ? {
            provider: "openai_compatible_api", enabled: true,
            endpoint: "http://127.0.0.1:11434/v1", model: "bge-small-zh-v1.5", dimensions: 512,
            credentialConfigured: false, reason: "semantic_embedding_legacy_dimensions",
          }
        : { configured: false };
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    }));

    render(<Theme><ModelPage onToast={() => undefined} /></Theme>);

    expect(await screen.findByDisplayValue("bge-small-zh-v1.5")).toBeTruthy();
    expect(screen.getByDisplayValue("512")).toBeTruthy();
    expect(screen.getByText("semantic_embedding_legacy_dimensions")).toBeTruthy();
    expect(screen.queryByDisplayValue("1024")).toBeNull();
  });

  it("applies and deletes semantic credentials from the model page", async () => {
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
    const requests: Array<{ input: string; init?: RequestInit }> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      requests.push({ input: path, init });
      if (path.endsWith("/config/semantic-embedding/credential")) {
        return new Response(JSON.stringify({ credentialConfigured: false, reason: "semantic_embedding_credential_required" }), { status: 200 });
      }
      if (path.endsWith("/config/semantic-embedding") && init?.method === "PUT") {
        return new Response(JSON.stringify({ credentialConfigured: true, reason: "" }), { status: 200 });
      }
      if (path.endsWith("/config/semantic-embedding")) {
        return new Response(JSON.stringify({
          provider: "siliconflow", enabled: true, endpoint: "https://api.siliconflow.cn/v1",
          model: "BAAI/bge-m3", dimensions: 1024, credentialConfigured: true, reason: "",
        }), { status: 200 });
      }
      return new Response(JSON.stringify({ configured: false }), { status: 200 });
    }));

    render(<Theme><ModelPage onToast={() => undefined} /></Theme>);
    const apiKey = await screen.findByPlaceholderText("已配置，留空表示保持不变");
    fireEvent.change(apiKey, { target: { value: "semantic-test-key" } });
    fireEvent.click(screen.getByRole("button", { name: "保存语义嵌入" }));
    await waitFor(() => expect(requests.some(({ input, init }) => input.endsWith("/config/semantic-embedding") && init?.method === "PUT")).toBe(true));
    const save = requests.find(({ input, init }) => input.endsWith("/config/semantic-embedding") && init?.method === "PUT");
    expect(JSON.parse(String(save?.init?.body))).toMatchObject({ provider: "siliconflow", model: "BAAI/bge-m3", dimensions: 1024, apiKey: "semantic-test-key" });

    fireEvent.click(await screen.findByRole("button", { name: "删除凭据" }));
    await waitFor(() => expect(requests.some(({ input, init }) => input.endsWith("/config/semantic-embedding/credential") && init?.method === "DELETE")).toBe(true));
  });

  it("keeps semantic mutation controls out of the intelligence page", async () => {
    vi.stubGlobal("ResizeObserver", class {
      observe() {}
      unobserve() {}
      disconnect() {}
    });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const body = String(input).endsWith("/intelligence")
        ? { ready: true, summary: {}, webSearch: { enabled: false, baseUrl: "" } }
        : { global: [], character: [], needsReview: [] };
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    }));

    render(<Theme><IntelligencePage onToast={() => undefined} /></Theme>);
    await screen.findByText("允许检索公开资料");
    expect(screen.queryByText("语义嵌入模型")).toBeNull();
    expect(screen.queryByRole("button", { name: "保存语义嵌入" })).toBeNull();
  });
});
