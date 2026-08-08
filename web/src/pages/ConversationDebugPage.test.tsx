// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { Theme } from "@radix-ui/themes";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConversationDebugPage } from "./ConversationDebugPage";

let persistedMessagesByConversation: Map<string, Array<Record<string, unknown>>>;
let endpointConversations: Map<string, string>;
let openedEndpointKeys: string[];
let nextConversationNumber: number;
let nextTurnNumber: number;
let nextIDNumber: number;
let durableStorageValues: Map<string, string>;
let sessionStorageValues: Map<string, string>;
let assistantContent: string;
let assistantParts: unknown[] | undefined;
let runtimeEvents: Array<Record<string, unknown>>;
let holdTurnOpen: boolean;
let includeCompletedUsage: boolean;
let clipboardWriteText: ReturnType<typeof vi.fn>;
let rejectSubmitAfterPersist: boolean;

function metricValues(container: HTMLElement) {
  return Object.fromEntries(
    [...container.querySelectorAll(".debug-metric-grid > div")].map((item) => [
      item.querySelector("span")?.textContent || "",
      item.querySelector("strong")?.textContent || "",
    ]),
  );
}

class DebugPageSocket extends EventTarget {
  static readonly OPEN = 1;
  readonly protocol = "fairy.session.v1";
  readyState = DebugPageSocket.OPEN;
  private conversationId = "";

  constructor() {
    super();
    setTimeout(() => this.frame({ type: "ready" }), 0);
  }

  send(raw: string) {
    const frame = JSON.parse(raw);
    if (frame.type === "session.open") {
      const endpointKey = String(frame.endpointKey || "");
      openedEndpointKeys.push(endpointKey);
      let conversationId = endpointConversations.get(endpointKey);
      if (!conversationId) {
        nextConversationNumber += 1;
        conversationId = `conversation-${nextConversationNumber}`;
        endpointConversations.set(endpointKey, conversationId);
        persistedMessagesByConversation.set(conversationId, []);
      }
      this.conversationId = conversationId;
      this.frame({
        type: "session.opened",
        requestId: frame.requestId,
        conversationId,
        characterId: frame.characterId,
        messageCount: persistedMessagesByConversation.get(conversationId)?.length || 0,
        endpoint: "desktop",
      });
      return;
    }
    if (frame.type === "session.watch") {
      this.frame({ type: "ack", requestId: frame.requestId, conversationId: this.conversationId });
      return;
    }
    if (frame.type === "turn.submit") {
      const now = Date.now();
      nextTurnNumber += 1;
      const turnId = `turn-${nextTurnNumber}`;
      const messages = persistedMessagesByConversation.get(this.conversationId) || [];
      const sequence = messages.length + 1;
      if (holdTurnOpen) {
        messages.push({ id: `message-user-${turnId}`, messageId: frame.messageId, turnId, sequence, role: "user", content: frame.input, createdAtUnixMs: now });
        persistedMessagesByConversation.set(this.conversationId, messages);
        this.frame({
          type: "turn.event",
          conversationId: this.conversationId,
          event: {
            conversationId: this.conversationId,
            turnId,
            sequence: 1,
            state: "planning",
            payload: { type: "state" },
          },
        });
        this.frame({ type: "result", requestId: frame.requestId, payload: { outcome: { turnId } } });
        return;
      }
      messages.push(
        { id: `message-user-${turnId}`, messageId: frame.messageId, turnId, sequence, role: "user", content: frame.input, createdAtUnixMs: now },
        {
          id: `message-assistant-${turnId}`,
          turnId,
          sequence: sequence + 1,
          role: "assistant",
          content: assistantContent,
          parts: assistantParts,
          createdAtUnixMs: now + 100,
        },
      );
      persistedMessagesByConversation.set(this.conversationId, messages);
      this.frame({
        type: "turn.event",
        conversationId: this.conversationId,
        event: {
          conversationId: this.conversationId,
          turnId,
          sequence: 1,
          state: "completed",
          payload: {
            type: "completed",
            text: assistantContent,
            ...(includeCompletedUsage ? { usage: [{
              lane: "respond",
              historyWindow: 1,
              usage: {
                inputTokens: 120,
                outputTokens: 24,
                cachedInputTokens: { status: "observed", tokens: 80 },
                cacheWriteTokens: { status: "missing" },
              },
            }] } : {}),
          },
        },
      });
      if (rejectSubmitAfterPersist) {
        this.dispatchEvent(new Event("close"));
        return;
      }
      this.frame({ type: "result", requestId: frame.requestId, payload: { outcome: { turnId } } });
    }
  }

  close() {
    this.readyState = 3;
  }

  private frame(value: unknown) {
    this.dispatchEvent(new MessageEvent("message", { data: JSON.stringify(value) }));
  }
}

beforeEach(() => {
  persistedMessagesByConversation = new Map();
  endpointConversations = new Map();
  openedEndpointKeys = [];
  nextConversationNumber = 0;
  nextTurnNumber = 0;
  nextIDNumber = 0;
  assistantContent = "真实角色回复";
  assistantParts = undefined;
  holdTurnOpen = false;
  includeCompletedUsage = true;
  rejectSubmitAfterPersist = false;
  runtimeEvents = [
    { sequence: 1, eventType: "transition", state: "interpreting", metadata: {}, createdAtUnixMs: 1_001 },
    { sequence: 2, eventType: "transition", state: "gathering", metadata: {}, createdAtUnixMs: 1_002 },
    { sequence: 3, eventType: "gather", state: "gathering", metadata: {}, createdAtUnixMs: 1_003 },
    { sequence: 4, eventType: "transition", state: "planning", metadata: {}, createdAtUnixMs: 1_004 },
    { sequence: 5, eventType: "prompt", state: "planning", metadata: { retrievedPersonalCount: 0, retrievedKnowledgeCount: 0 }, createdAtUnixMs: 1_005 },
    { sequence: 6, eventType: "continuation", state: "planning", metadata: { incremental: false }, createdAtUnixMs: 1_006 },
    { sequence: 7, eventType: "model", state: "planning", metadata: { completedMs: 2097 }, createdAtUnixMs: 1_007 },
    { sequence: 8, eventType: "model", state: "planning", metadata: { usage: [{ lane: "respond", usage: { inputTokens: 120, outputTokens: 24, cachedInputTokens: { status: "observed", tokens: 80 } } }] }, createdAtUnixMs: 1_008 },
    { sequence: 9, eventType: "compile", state: "planning", metadata: { status: "completed", chainCount: 1 }, createdAtUnixMs: 1_009 },
    { sequence: 10, eventType: "transition", state: "responding", metadata: {}, createdAtUnixMs: 1_010 },
    { sequence: 11, eventType: "beat_delivery", state: "responding", metadata: { status: "planned", kind: "utterance", chainIndex: 0, playIndex: 0 }, createdAtUnixMs: 1_011 },
    { sequence: 12, eventType: "beat_delivery", state: "responding", metadata: { status: "published", kind: "utterance", chainIndex: 0, playIndex: 0 }, createdAtUnixMs: 1_012 },
    { sequence: 13, eventType: "terminal", state: "completed", metadata: { status: "completed" }, createdAtUnixMs: 1_013 },
    { sequence: 14, eventType: "context_window", state: "completed", metadata: { windowNumber: 1, observedPrefillTokens: 120 }, createdAtUnixMs: 1_014 },
  ];
  vi.stubGlobal("WebSocket", DebugPageSocket);
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  vi.stubGlobal("crypto", { randomUUID: () => `id-${++nextIDNumber}` });
  clipboardWriteText = vi.fn(async () => undefined);
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText: clipboardWriteText },
  });
  sessionStorageValues = new Map<string, string>();
  vi.stubGlobal("sessionStorage", {
    getItem: (key: string) => sessionStorageValues.get(key) ?? null,
    setItem: (key: string, value: string) => sessionStorageValues.set(key, value),
    removeItem: (key: string) => sessionStorageValues.delete(key),
    clear: () => sessionStorageValues.clear(),
  });
  durableStorageValues = new Map([["fairy.apiToken", "token"]]);
  vi.stubGlobal("localStorage", {
    getItem: (key: string) => durableStorageValues.get(key) ?? null,
    setItem: (key: string, value: string) => durableStorageValues.set(key, value),
    removeItem: (key: string) => durableStorageValues.delete(key),
    clear: () => durableStorageValues.clear(),
  });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input);
    if (path.endsWith("/characters")) {
      const character = { characterId: "character-1", name: "亚托莉" };
      return Response.json({ characters: [character], active: character });
    }
    if (path.endsWith("/session/browser-ticket")) {
      return Response.json({ ticket: "ticket", protocol: "fairy.session.v1", expiresAtUnixMs: Date.now() + 30_000 }, { status: 201 });
    }
    if (path.includes("/messages")) {
      const conversationId = path.match(/\/sessions\/([^/]+)\/messages/)?.[1] || "";
      return Response.json({ messages: persistedMessagesByConversation.get(conversationId) || [] });
    }
    if (path.includes("/runtime")) {
      const [, conversationId = "", turnId = ""] = path.match(/\/sessions\/([^/]+)\/turns\/([^/]+)\/runtime/) || [];
      return Response.json({
        conversationId,
        turnId,
        events: runtimeEvents,
      });
    }
    return Response.json({ error: "not found" }, { status: 404 });
  }));
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("ConversationDebugPage", () => {
  it("streams elapsed time, observed usage, and runtime events while a turn is active", async () => {
    holdTurnOpen = true;
    runtimeEvents = [
      { sequence: 1, eventType: "transition", state: "planning", metadata: {}, createdAtUnixMs: 1_001 },
      {
        sequence: 2,
        eventType: "model",
        state: "planning",
        metadata: {
          usage: [{
            lane: "gather",
            usage: {
              inputTokens: 222,
              cachedInputTokens: { status: "observed", tokens: 111 },
            },
          }],
        },
        createdAtUnixMs: 1_002,
      },
      { sequence: 3, eventType: "tool", state: "planning", metadata: { tool: "web_search", phase: "model_driven", status: "ok" }, createdAtUnixMs: 1_003 },
    ];

    const { container } = render(<Theme><ConversationDebugPage onOpenCharacters={() => undefined} /></Theme>);
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });
    const composer = screen.getByLabelText("调试消息");
    fireEvent.change(composer, { target: { value: "执行期间展示进度" } });
    fireEvent.keyDown(composer, { key: "Enter", shiftKey: false });

    expect(await screen.findByText("回复生成中")).toBeTruthy();
    await waitFor(() => expect(metricValues(container)["输入 Token"]).toBe("222"));
    const metrics = metricValues(container);
    expect(metrics["耗时"]).toMatch(/^(\d+ ms|\d+\.\d{2} s)$/);
    expect(metrics["耗时"]).not.toBe("不可用");
    expect(metrics["输出 Token"]).toBe("统计中");
    expect(metrics["缓存命中"]).toBe("111");
    expect(await screen.findByText("关键 3 / 原始 3")).toBeTruthy();
    expect(screen.getByText("模型调用")).toBeTruthy();
    expect(screen.getByRole("button", { name: /工具执行.*web_search.*model_driven.*ok/ })).toBeTruthy();
    expect(screen.queryByText("Turn 终态后展示运行记录")).toBeNull();
  });

  it("runs a real Session turn and presents usage plus runtime evidence", async () => {
    const { container } = render(<Theme><ConversationDebugPage onOpenCharacters={() => undefined} /></Theme>);
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });
    const composer = screen.getByLabelText("调试消息");
    fireEvent.change(composer, { target: { value: "今天感觉怎么样？" } });
    fireEvent.keyDown(composer, { key: "Enter", shiftKey: false });

    expect(await screen.findByText("真实角色回复")).toBeTruthy();
    await waitFor(() => expect(screen.getByText("120")).toBeTruthy());
    expect(screen.getByText("24")).toBeTruthy();
    expect(screen.getByText("80")).toBeTruthy();
    expect(await screen.findByText("关键 11 / 原始 14")).toBeTruthy();
    expect(screen.getAllByText("模型调用")).toHaveLength(1);
    expect(screen.getByText("完成 2,097 ms，输入 120 Token，输出 24 Token，缓存命中 80 Token")).toBeTruthy();
    expect(screen.getAllByText("回复交付")).toHaveLength(1);
    expect(screen.getByText("第 1 段已发布")).toBeTruthy();
    expect(screen.queryByText("上下文窗口")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "查看原始事件 (14)" }));
    expect(await screen.findByText("原始事件")).toBeTruthy();
    expect(screen.getAllByText("模型调用")).toHaveLength(2);
    expect(screen.getAllByText("回复交付")).toHaveLength(2);
    expect(screen.getByText("上下文窗口")).toBeTruthy();
    expect([...container.querySelectorAll(".debug-runtime-list > li > span")].map((item) => item.textContent)).toEqual(
      Array.from({ length: 14 }, (_, index) => String(index + 1)),
    );

    fireEvent.click(screen.getByRole("button", { name: "返回关键时间线" }));
    expect(screen.queryByText("上下文窗口")).toBeNull();
    expect(screen.getAllByText("回复交付")).toHaveLength(1);
  });

  it("appends consecutive turns without replacing the earlier transcript", async () => {
    assistantContent = "第一轮回复";
    const { container } = render(<Theme><ConversationDebugPage onOpenCharacters={() => undefined} /></Theme>);
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });
    const composer = screen.getByLabelText("调试消息");

    fireEvent.change(composer, { target: { value: "第一轮问题" } });
    fireEvent.keyDown(composer, { key: "Enter", shiftKey: false });
    expect(await screen.findByText("第一轮回复")).toBeTruthy();

    assistantContent = "第二轮回复";
    fireEvent.change(composer, { target: { value: "第二轮问题" } });
    fireEvent.keyDown(composer, { key: "Enter", shiftKey: false });
    expect(await screen.findByText("第二轮回复")).toBeTruthy();

    expect(screen.getByText("第一轮问题")).toBeTruthy();
    expect(screen.getByText("第一轮回复")).toBeTruthy();
    expect(screen.getByText("第二轮问题")).toBeTruthy();
    expect(screen.getByText("2 个 Turn")).toBeTruthy();
    expect(container.querySelectorAll(".debug-message")).toHaveLength(4);
    expect(container.querySelectorAll(".debug-turn-picker button")).toHaveLength(2);
  });

  it("restores the same evaluation conversation after the page remounts", async () => {
    const firstRender = render(<Theme><ConversationDebugPage onOpenCharacters={() => undefined} /></Theme>);
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });
    const composer = screen.getByLabelText("调试消息");
    fireEvent.change(composer, { target: { value: "导航后仍需保留" } });
    fireEvent.keyDown(composer, { key: "Enter", shiftKey: false });
    expect(await screen.findByText("真实角色回复")).toBeTruthy();
    const originalEndpointKey = openedEndpointKeys.at(-1);

    firstRender.unmount();
    sessionStorageValues.clear();
    render(<Theme><ConversationDebugPage onOpenCharacters={() => undefined} /></Theme>);
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });

    expect(await screen.findByText("导航后仍需保留")).toBeTruthy();
    expect(screen.getByText("真实角色回复")).toBeTruthy();
    expect(screen.getByText("1 个 Turn")).toBeTruthy();
    expect(openedEndpointKeys.at(-1)).toBe(originalEndpointKey);
    expect(endpointConversations.size).toBe(1);
    await waitFor(() => expect(vi.mocked(fetch).mock.calls.some(([input]) => String(input).includes("/turns/turn-1/runtime"))).toBe(true));
  });

  it("keeps one copyable messageId across optimistic delivery and history recovery", async () => {
    const firstRender = render(<Theme><ConversationDebugPage onOpenCharacters={() => undefined} /></Theme>);
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });
    const composer = screen.getByLabelText("调试消息");
    fireEvent.change(composer, { target: { value: "按消息关联链路" } });
    fireEvent.keyDown(composer, { key: "Enter", shiftKey: false });
    expect(await screen.findByText("真实角色回复")).toBeTruthy();

    const copyButton = screen.getByRole("button", { name: /复制 messageId debug-msg-/ });
    const messageId = copyButton.getAttribute("title");
    expect(messageId).toMatch(/^debug-msg-id-/);
    fireEvent.click(copyButton);
    await waitFor(() => expect(clipboardWriteText).toHaveBeenCalledWith(messageId));

    firstRender.unmount();
    render(<Theme><ConversationDebugPage onOpenCharacters={() => undefined} /></Theme>);
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });
    expect(await screen.findByRole("button", { name: `复制 messageId ${messageId}` })).toBeTruthy();
  });

  it("keeps the accepted messageId when the request connection fails after persistence", async () => {
    rejectSubmitAfterPersist = true;
    render(<Theme><ConversationDebugPage onOpenCharacters={() => undefined} /></Theme>);
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });
    const composer = screen.getByLabelText("调试消息");
    fireEvent.change(composer, { target: { value: "连接失败也不能换关联 ID" } });
    fireEvent.keyDown(composer, { key: "Enter", shiftKey: false });
    expect(await screen.findByText("真实角色回复")).toBeTruthy();
    const original = screen.getByRole("button", { name: /复制 messageId debug-msg-/ }).getAttribute("title");
    expect(await screen.findByRole("button", { name: "重连" })).toBeTruthy();

    rejectSubmitAfterPersist = false;
    fireEvent.click(screen.getByRole("button", { name: "重连" }));
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });
    expect(screen.getAllByRole("button", { name: `复制 messageId ${original}` })).toHaveLength(1);
  });

  it("migrates the existing session endpoint key into durable storage", async () => {
    const storageKey = "fairy.console.debug.endpoint.v1.character-1";
    const legacyEndpointKey = "web-evaluation-existing-session";
    sessionStorageValues.set(storageKey, legacyEndpointKey);

    const firstRender = render(<Theme><ConversationDebugPage onOpenCharacters={() => undefined} /></Theme>);
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });
    expect(openedEndpointKeys.at(-1)).toBe(legacyEndpointKey);
    expect(durableStorageValues.get(storageKey)).toBe(legacyEndpointKey);

    firstRender.unmount();
    sessionStorageValues.clear();
    render(<Theme><ConversationDebugPage onOpenCharacters={() => undefined} /></Theme>);
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });
    expect(openedEndpointKeys.at(-1)).toBe(legacyEndpointKey);
    expect(endpointConversations.size).toBe(1);
  });

  it("rotates the evaluation conversation only when the user explicitly starts a new one", async () => {
    const firstRender = render(<Theme><ConversationDebugPage onOpenCharacters={() => undefined} /></Theme>);
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });
    const composer = screen.getByLabelText("调试消息");
    fireEvent.change(composer, { target: { value: "旧会话消息" } });
    fireEvent.keyDown(composer, { key: "Enter", shiftKey: false });
    expect(await screen.findByText("真实角色回复")).toBeTruthy();
    const originalEndpointKey = openedEndpointKeys.at(-1);

    fireEvent.click(screen.getByRole("button", { name: "新建会话" }));
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });
    await waitFor(() => expect(screen.getByText("0 个 Turn")).toBeTruthy());
    const freshEndpointKey = openedEndpointKeys.at(-1);
    expect(freshEndpointKey).not.toBe(originalEndpointKey);
    expect(screen.queryByText("旧会话消息")).toBeNull();

    firstRender.unmount();
    sessionStorageValues.clear();
    render(<Theme><ConversationDebugPage onOpenCharacters={() => undefined} /></Theme>);
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });
    expect(openedEndpointKeys.at(-1)).toBe(freshEndpointKey);
    expect(screen.getByText("0 个 Turn")).toBeTruthy();
    expect(endpointConversations.size).toBe(2);
  });

  it("fails visibly instead of opening an in-memory conversation when durable storage is unavailable", async () => {
    vi.stubGlobal("localStorage", {
      getItem: (key: string) => {
        if (key === "fairy.apiToken") return "token";
        throw new DOMException("blocked", "SecurityError");
      },
      setItem: () => {
        throw new DOMException("blocked", "SecurityError");
      },
      removeItem: () => undefined,
      clear: () => undefined,
    });

    render(<Theme><ConversationDebugPage onOpenCharacters={() => undefined} /></Theme>);
    expect(await screen.findByText("浏览器持久存储不可用，无法恢复调试会话")).toBeTruthy();
    expect(screen.getAllByText("连接失败").length).toBeGreaterThan(0);
    expect(openedEndpointKeys).toHaveLength(0);
    expect(vi.mocked(fetch).mock.calls.some(([input]) => String(input).endsWith("/session/browser-ticket"))).toBe(false);
  });

  it("ignores an older message response that arrives after a newer turn was restored", async () => {
    const fallbackFetch = vi.mocked(fetch).getMockImplementation();
    let messageRequestCount = 0;
    let resolveOlderMessages: (() => void) | undefined;
    vi.mocked(fetch).mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      if (path.includes("/messages")) {
        messageRequestCount += 1;
        const conversationId = path.match(/\/sessions\/([^/]+)\/messages/)?.[1] || "";
        if (messageRequestCount === 2) {
          const olderSnapshot = [...(persistedMessagesByConversation.get(conversationId) || [])];
          return new Promise<Response>((resolve) => {
            resolveOlderMessages = () => resolve(Response.json({ messages: olderSnapshot }));
          });
        }
      }
      if (!fallbackFetch) throw new Error("missing default fetch mock");
      return fallbackFetch(input, init);
    });

    assistantContent = "较早回复";
    render(<Theme><ConversationDebugPage onOpenCharacters={() => undefined} /></Theme>);
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });
    const composer = screen.getByLabelText("调试消息") as HTMLTextAreaElement;
    fireEvent.change(composer, { target: { value: "较早问题" } });
    fireEvent.keyDown(composer, { key: "Enter", shiftKey: false });
    await waitFor(() => expect(composer.disabled).toBe(false));

    assistantContent = "较新回复";
    fireEvent.change(composer, { target: { value: "较新问题" } });
    fireEvent.keyDown(composer, { key: "Enter", shiftKey: false });
    expect(await screen.findByText("较新回复")).toBeTruthy();
    expect(screen.getByText("较早回复")).toBeTruthy();

    resolveOlderMessages?.();
    await Promise.resolve();
    expect(screen.getByText("较早回复")).toBeTruthy();
    expect(screen.getByText("较新回复")).toBeTruthy();
    expect(screen.getByText("2 个 Turn")).toBeTruthy();
  });

  it("keeps terminal usage unavailable when the completed event did not observe it", async () => {
    includeCompletedUsage = false;
    const { container } = render(<Theme><ConversationDebugPage onOpenCharacters={() => undefined} /></Theme>);
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });
    const composer = screen.getByLabelText("调试消息");
    fireEvent.change(composer, { target: { value: "缺失最终用量" } });
    fireEvent.keyDown(composer, { key: "Enter", shiftKey: false });

    expect(await screen.findByText("真实角色回复")).toBeTruthy();
    await screen.findByText("关键 11 / 原始 14");
    const metrics = metricValues(container);
    expect(metrics["输入 Token"]).toBe("不可用");
    expect(metrics["输出 Token"]).toBe("不可用");
    expect(metrics["缓存命中"]).toBe("不可用");
    expect(Object.values(metrics)).not.toContain("统计中");
  });

  it("renders ordered expression parts as separate bubbles within one assistant message", async () => {
    assistantContent = "第一句话。\n第二句话？";
    assistantParts = [
      { kind: "utterance", text: "第一句话。", visualState: "happy" },
      { kind: "sticker", visualState: "happy", sticker: { id: "wave", description: "开心挥手", mimeType: "image/webp" } },
      { kind: "utterance", text: "第二句话？", visualState: "curious" },
    ];
    const { container } = render(<Theme><ConversationDebugPage onOpenCharacters={() => undefined} /></Theme>);
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });
    const composer = screen.getByLabelText("调试消息");
    fireEvent.change(composer, { target: { value: "分段回复" } });
    fireEvent.keyDown(composer, { key: "Enter", shiftKey: false });

    expect(await screen.findByText("开心挥手")).toBeTruthy();
    const assistantMessage = container.querySelector(".debug-message.assistant:not(.pending)");
    const bubbles = [...(assistantMessage?.querySelectorAll(".debug-message-bubble") || [])];
    expect(bubbles).toHaveLength(3);
    expect(bubbles.map((bubble) => bubble.getAttribute("data-expression-kind"))).toEqual(["utterance", "sticker", "utterance"]);
    expect(bubbles.map((bubble) => bubble.textContent)).toEqual(["第一句话。", "表情包开心挥手", "第二句话？"]);
    expect(assistantMessage?.querySelectorAll("time")).toHaveLength(1);
    expect(bubbles.some((bubble) => bubble.textContent === assistantContent)).toBe(false);
  });

  it("keeps legacy aggregate content in one bubble instead of guessing sentence boundaries", async () => {
    assistantContent = "旧消息第一句。\n旧消息第二句。";
    assistantParts = undefined;
    const { container } = render(<Theme><ConversationDebugPage onOpenCharacters={() => undefined} /></Theme>);
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });
    const composer = screen.getByLabelText("调试消息");
    fireEvent.change(composer, { target: { value: "读取旧消息" } });
    fireEvent.keyDown(composer, { key: "Enter", shiftKey: false });

    await waitFor(() => {
      const assistantMessage = container.querySelector(".debug-message.assistant:not(.pending)");
      expect(assistantMessage?.textContent).toContain(assistantContent);
    });
    const bubbles = container.querySelectorAll(".debug-message.assistant:not(.pending) .debug-message-bubble");
    expect(bubbles).toHaveLength(1);
    expect(bubbles[0]?.textContent).toBe(assistantContent);
  });

  it("opens tool evidence in a dialog and separates returned data from merged model context", async () => {
    runtimeEvents = [{
      sequence: 1,
      eventType: "tool",
      state: "planning",
      metadata: {
        tool: "web_search",
        phase: "model_driven",
        status: "ok",
        detail: {
          version: "v1",
          arguments: { query: "苍之彼方的四重奏" },
          receipt: { status: "ok", knowledgeCount: 1 },
          result: {
            personalMemories: [],
            knowledge: [{
              id: "web-1",
              topic: "web_search",
              statement: "公开标题 — 公开摘要",
              sources: [{ title: "公开来源", url: "https://example.com/source", snippet: "公开摘要", rank: 1 }],
            }],
            socialMemories: { entries: [] },
            semanticStatus: "unavailable",
          },
          mergedContext: {
            personalMemories: [{ id: "memory-1", kind: "preference", content: "喜欢蓝色" }],
            knowledge: [{ id: "web-1", topic: "web_search", statement: "公开标题 — 公开摘要", sources: [] }],
            socialMemories: { entries: [] },
            semanticStatus: "unavailable",
          },
        },
      },
      createdAtUnixMs: 1_001,
    }];
    render(<Theme><ConversationDebugPage onOpenCharacters={() => undefined} /></Theme>);
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });
    const composer = screen.getByLabelText("调试消息");
    fireEvent.change(composer, { target: { value: "请搜索作品资料" } });
    fireEvent.keyDown(composer, { key: "Enter", shiftKey: false });

    const toggle = await screen.findByRole("button", { name: /工具执行.*web_search.*model_driven.*ok/ });
    expect(toggle.getAttribute("aria-haspopup")).toBe("dialog");
    expect(screen.queryByText("苍之彼方的四重奏")).toBeNull();
    fireEvent.click(toggle);
    expect(await screen.findByRole("dialog", { name: "工具执行详情" })).toBeTruthy();
    expect(screen.getByText("苍之彼方的四重奏")).toBeTruthy();
    expect(screen.getAllByText("公开标题 — 公开摘要")).toHaveLength(1);
    expect(screen.getByRole("link", { name: "公开来源" }).getAttribute("href")).toBe("https://example.com/source");
    expect(screen.queryByText("喜欢蓝色")).toBeNull();

    const mergedTab = screen.getByRole("tab", { name: /最终注入上下文 2/ });
    fireEvent.mouseDown(mergedTab, { button: 0, ctrlKey: false });
    expect(await screen.findByText("喜欢蓝色")).toBeTruthy();
    expect(screen.getAllByText("公开标题 — 公开摘要")).toHaveLength(1);

    fireEvent.click(screen.getByRole("button", { name: "关闭工具详情" }));
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "工具执行详情" })).toBeNull();
      expect(document.activeElement).toBe(toggle);
    });
  });

  it("keeps legacy tool events inspectable with an explicit compatibility message", async () => {
    runtimeEvents = [{
      sequence: 1,
      eventType: "tool",
      state: "planning",
      metadata: { tool: "web_search", phase: "model_driven", status: "ok" },
      createdAtUnixMs: 1_001,
    }];
    render(<Theme><ConversationDebugPage onOpenCharacters={() => undefined} /></Theme>);
    await screen.findAllByText("会话就绪", {}, { timeout: 3000 });
    const composer = screen.getByLabelText("调试消息");
    fireEvent.change(composer, { target: { value: "读取旧工具记录" } });
    fireEvent.keyDown(composer, { key: "Enter", shiftKey: false });

    fireEvent.click(await screen.findByRole("button", { name: /工具执行.*web_search/ }));
    expect(await screen.findByRole("dialog", { name: "工具执行详情" })).toBeTruthy();
    expect(screen.getByText("该 Turn 创建时未记录工具明细，无法还原调用参数和注入上下文。")).toBeTruthy();
  });
});
