// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import App from "./App";
import { apiBlob } from "./api";

const TOKEN_KEY = "fairy.apiToken";

let storage: Map<string, string>;

type RecordedRequest = {
  path: string;
  authorization: string | null;
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function requestRecord(input: RequestInfo | URL, init?: RequestInit): RecordedRequest {
  const headers = new Headers(init?.headers);
  return {
    path: String(input),
    authorization: headers.get("Authorization"),
  };
}

beforeEach(() => {
  window.history.replaceState(null, "", "#/overview");
  storage = new Map();
  vi.stubGlobal("localStorage", {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => storage.set(key, String(value)),
    removeItem: (key: string) => storage.delete(key),
    clear: () => storage.clear(),
    key: (index: number) => [...storage.keys()][index] ?? null,
    get length() { return storage.size; },
  });
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    unobserve() {}
    disconnect() {}
  });
  vi.stubGlobal("scrollTo", vi.fn());
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("App Core connection gate", () => {
	it("restores the selected global task from the URL hash", async () => {
		window.history.replaceState(null, "", "#/logs");
		localStorage.setItem(TOKEN_KEY, "saved-token");
		vi.stubGlobal("fetch", vi.fn(async () => json({ model: {}, webSearch: {}, semanticEmbedding: {} })));

		render(<App />);

		const navigation = await screen.findByRole("navigation", { name: "控制台导航" });
		expect(within(navigation).getByRole("button", { name: "日志" }).getAttribute("aria-current")).toBe("page");
		expect(document.getElementById("observability-panel-logs")?.hidden).toBe(false);
		fireEvent.click(within(navigation).getByRole("button", { name: "角色" }));
		expect(window.location.hash).toBe("#/character");
		expect(window.location.href).not.toContain("saved-token");

		window.history.back();
		window.dispatchEvent(new HashChangeEvent("hashchange"));
	});

  it("shows only the connection entry and makes no request without a saved token", () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    render(<App />);

    expect(screen.getByRole("heading", { name: "连接 FAIRY Core" })).toBeTruthy();
    expect(screen.queryByRole("navigation")).toBeNull();
    expect(screen.queryByText("运行概览")).toBeNull();
    expect(screen.queryByText("尚未激活")).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText("Core API Token"), { target: { value: "draft-only" } });
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
  });

  it("keeps rejected saved credentials behind the gate and makes only the status handshake", async () => {
    localStorage.setItem(TOKEN_KEY, "saved-token");
    const requests: RecordedRequest[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      requests.push(requestRecord(input, init));
      return json({ error: "unauthorized" }, 401);
    }));

    render(<App />);

    expect(await screen.findByRole("heading", { name: "Core 拒绝了这个 Token" })).toBeTruthy();
    expect(requests).toEqual([{ path: "/v1/status", authorization: "Bearer saved-token" }]);
    expect(screen.queryByRole("navigation")).toBeNull();
    expect(screen.queryByText("尚未激活")).toBeNull();
  });

  it("distinguishes an unreachable Core without mounting a business page", async () => {
    localStorage.setItem(TOKEN_KEY, "saved-token");
    const requests: RecordedRequest[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      requests.push(requestRecord(input, init));
      throw new TypeError("network unavailable");
    }));

    render(<App />);

    expect(await screen.findByRole("heading", { name: "无法连接 Core" })).toBeTruthy();
    expect(requests).toEqual([{ path: "/v1/status", authorization: "Bearer saved-token" }]);
    expect(screen.queryByRole("navigation")).toBeNull();
    expect(screen.queryByText("尚未激活")).toBeNull();
  });

  it("persists a trimmed token only on explicit connect and mounts business data after the handshake", async () => {
    const requests: RecordedRequest[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = requestRecord(input, init);
      requests.push(request);
      if (request.path.endsWith("/characters")) {
        return json({ active: { characterId: "char-1", name: "Alice" }, characters: [] });
      }
      return json({
        model: { configured: true, model: "test-model" },
        webSearch: { enabled: false },
        semanticEmbedding: { provider: "none", enabled: false },
      });
    }));

    render(<App />);

    const input = screen.getByLabelText("Core API Token");
    fireEvent.change(input, { target: { value: "  candidate-token  " } });
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "连接 Core" }));

    expect(await screen.findByText("Alice")).toBeTruthy();
    expect(localStorage.getItem(TOKEN_KEY)).toBe("candidate-token");
    expect(requests[0]).toEqual({ path: "/v1/status", authorization: "Bearer candidate-token" });
    expect(requests.some(({ path }) => path.endsWith("/characters"))).toBe(true);
    expect(requests.every(({ authorization }) => authorization === "Bearer candidate-token")).toBe(true);
    const navigation = screen.getByRole("navigation", { name: "控制台导航" });
    expect(within(navigation).getAllByRole("button")).toHaveLength(11);
    expect(within(navigation).getByRole("button", { name: "记忆与知识" })).toBeTruthy();
    expect(within(navigation).getByRole("button", { name: "对话调试" })).toBeTruthy();
    expect(within(navigation).getByRole("button", { name: "指标" })).toBeTruthy();
    expect(within(navigation).getByRole("button", { name: "链路跟踪" })).toBeTruthy();
    expect(within(navigation).getByRole("button", { name: "日志" })).toBeTruthy();
    expect(within(navigation).queryByRole("button", { name: "可观测" })).toBeNull();
    expect(within(navigation).queryByRole("button", { name: "用量" })).toBeNull();
    expect(navigation.closest(".tool-rail")).toBeTruthy();
    expect(screen.getByText("本地陪伴管理台")).toBeTruthy();
    expect(document.querySelector(".page-eyebrow")).toBeNull();

    fireEvent.click(within(navigation).getByRole("button", { name: "指标" }));
    expect(within(navigation).getByRole("button", { name: "指标" }).getAttribute("aria-current")).toBe("page");
    expect(document.querySelector(".observability-page .observability-tabs")).toBeNull();
    expect(document.querySelector(".nav-subtasks")).toBeNull();
    expect(document.getElementById("observability-panel-metrics")?.hidden).toBe(false);

    fireEvent.click(within(navigation).getByRole("button", { name: "链路跟踪" }));
    expect(within(navigation).getByRole("button", { name: "链路跟踪" }).getAttribute("aria-current")).toBe("page");
    expect(document.getElementById("observability-panel-tracing")?.hidden).toBe(false);

    fireEvent.click(within(navigation).getByRole("button", { name: "日志" }));
    expect(within(navigation).getByRole("button", { name: "日志" }).getAttribute("aria-current")).toBe("page");
    expect(document.getElementById("observability-panel-logs")?.hidden).toBe(false);

    fireEvent.click(within(navigation).getByRole("button", { name: "角色" }));
    expect(window.scrollTo).toHaveBeenCalledWith({ top: 0, left: 0, behavior: "auto" });
  });

  it("unmounts the business shell when a later API request returns 401", async () => {
    localStorage.setItem(TOKEN_KEY, "saved-token");
    let statusCalls = 0;
    const requests: RecordedRequest[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = requestRecord(input, init);
      requests.push(request);
      if (request.path.endsWith("/status")) {
        statusCalls += 1;
        return json({ model: {}, webSearch: {}, semanticEmbedding: {} });
      }
      if (request.path.endsWith("/characters")) {
        return json({ error: "unauthorized" }, 401);
      }
      return json({});
    }));

    render(<App />);

    expect(await screen.findByRole("heading", { name: "Core 拒绝了这个 Token" })).toBeTruthy();
    expect(statusCalls).toBeGreaterThanOrEqual(2);
    expect(requests.some(({ path }) => path.endsWith("/characters"))).toBe(true);
    await waitFor(() => expect(screen.queryByRole("navigation")).toBeNull());
    expect(screen.queryByText("运行概览")).toBeNull();
  });

  it("gates the shell again before replacing a connected token", async () => {
    localStorage.setItem(TOKEN_KEY, "old-token");
    const requests: RecordedRequest[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = requestRecord(input, init);
      requests.push(request);
      if (request.path.endsWith("/characters")) {
        return json({ active: { characterId: "char-1", name: "Alice" }, characters: [] });
      }
      return json({ model: {}, webSearch: {}, semanticEmbedding: {} });
    }));

    render(<App />);
    expect(await screen.findByText("Alice")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Core 已连接" }));
    const replacement = await screen.findByLabelText("更换 Core Token");
    fireEvent.change(replacement, { target: { value: "  new-token  " } });
    expect(localStorage.getItem(TOKEN_KEY)).toBe("old-token");
    fireEvent.click(screen.getByRole("button", { name: "更换 Token" }));

    await waitFor(() => expect(localStorage.getItem(TOKEN_KEY)).toBe("new-token"));
    expect(await screen.findByText("Alice")).toBeTruthy();
    const newTokenHandshake = requests.find(({ path, authorization }) => (
      path.endsWith("/status") && authorization === "Bearer new-token"
    ));
    expect(newTokenHandshake).toBeTruthy();
  });

  it("unmounts the business shell when a Blob API request returns 401", async () => {
    localStorage.setItem(TOKEN_KEY, "saved-token");
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      if (path.endsWith("/stickers/sticker-1/content")) {
        return json({ error: "unauthorized" }, 401);
      }
      if (path.endsWith("/characters")) {
        return json({ active: { characterId: "char-1", name: "Alice" }, characters: [] });
      }
      return json({ model: {}, webSearch: {}, semanticEmbedding: {} });
    }));

    render(<App />);
    expect(await screen.findByText("Alice")).toBeTruthy();

    await expect(apiBlob("/stickers/sticker-1/content")).rejects.toMatchObject({ status: 401 });
    expect(await screen.findByRole("heading", { name: "Core 拒绝了这个 Token" })).toBeTruthy();
    expect(screen.queryByRole("navigation")).toBeNull();
  });

  it("ignores a stale 401 from the previous token while a replacement handshake is running", async () => {
    localStorage.setItem(TOKEN_KEY, "old-token");
    let resolveOldCharacters: (response: Response) => void = () => undefined;
    let resolveNewHandshake: (response: Response) => void = () => undefined;
    const oldCharacters = new Promise<Response>((resolve) => { resolveOldCharacters = resolve; });
    const newHandshake = new Promise<Response>((resolve) => { resolveNewHandshake = resolve; });
    const requests: RecordedRequest[] = [];
    let newStatusCalls = 0;

    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = requestRecord(input, init);
      requests.push(request);
      if (request.authorization === "Bearer old-token" && request.path.endsWith("/characters")) {
        return oldCharacters;
      }
      if (request.authorization === "Bearer new-token" && request.path.endsWith("/status")) {
        newStatusCalls += 1;
        if (newStatusCalls === 1) return newHandshake;
      }
      if (request.authorization === "Bearer new-token" && request.path.endsWith("/characters")) {
        return json({ active: { characterId: "char-2", name: "Bob" }, characters: [] });
      }
      return json({ model: {}, webSearch: {}, semanticEmbedding: {} });
    }));

    render(<App />);
    const connected = await screen.findByRole("button", { name: "Core 已连接" });
    await waitFor(() => expect(requests.some(({ path, authorization }) => (
      path.endsWith("/characters") && authorization === "Bearer old-token"
    ))).toBe(true));

    fireEvent.click(connected);
    fireEvent.change(await screen.findByLabelText("更换 Core Token"), { target: { value: "new-token" } });
    fireEvent.click(screen.getByRole("button", { name: "更换 Token" }));
    await waitFor(() => expect(newStatusCalls).toBe(1));

    resolveOldCharacters(json({ error: "unauthorized" }, 401));
    resolveNewHandshake(json({ model: {}, webSearch: {}, semanticEmbedding: {} }));

    expect(await screen.findByText("Bob")).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Core 拒绝了这个 Token" })).toBeNull();
  });
});
