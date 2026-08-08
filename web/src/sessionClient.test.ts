// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DebugSessionClient, type TurnEvent } from "./sessionClient";

class FakeWebSocket extends EventTarget {
  static readonly OPEN = 1;
  static readonly CLOSED = 3;
  static instances: FakeWebSocket[] = [];
  static respondToCancel = true;

  readonly url: string;
  readonly protocols: string[];
  readonly protocol = "fairy.session.v1";
  readyState = FakeWebSocket.OPEN;
  sent: Record<string, unknown>[] = [];

  constructor(url: string, protocols: string[]) {
    super();
    this.url = url;
    this.protocols = protocols;
    FakeWebSocket.instances.push(this);
    setTimeout(() => this.frame({ type: "ready" }), 0);
  }

  send(raw: string) {
    const frame = JSON.parse(raw);
    this.sent.push(frame);
    if (frame.type === "session.open") {
      this.frame({
        type: "session.opened",
        requestId: frame.requestId,
        conversationId: "conversation-1",
        characterId: frame.characterId,
        messageCount: 0,
        endpoint: "desktop",
      });
    } else if (frame.type === "session.watch") {
      this.frame({ type: "ack", requestId: frame.requestId, conversationId: frame.conversationId });
    } else if (frame.type === "turn.submit") {
      this.frame({
        type: "turn.event",
        conversationId: frame.conversationId,
        event: {
          conversationId: frame.conversationId,
          turnId: "turn-1",
          sequence: 1,
          state: "completed",
          payload: { type: "completed", text: "你好", usage: [] },
        },
      });
      this.frame({ type: "result", requestId: frame.requestId, payload: { outcome: { turnId: "turn-1" } } });
    } else if (frame.type === "turn.cancel" && FakeWebSocket.respondToCancel) {
      this.frame({ type: "ack", requestId: frame.requestId, conversationId: frame.conversationId });
    }
  }

  close() {
    this.readyState = FakeWebSocket.CLOSED;
    this.dispatchEvent(new Event("close"));
  }

  private frame(value: unknown) {
    this.dispatchEvent(new MessageEvent("message", { data: JSON.stringify(value) }));
  }
}

beforeEach(() => {
  FakeWebSocket.instances = [];
  FakeWebSocket.respondToCancel = true;
  vi.stubGlobal("WebSocket", FakeWebSocket);
  vi.stubGlobal("crypto", { randomUUID: () => "request-id" });
  vi.stubGlobal("localStorage", {
    getItem: () => "long-lived-api-token",
    setItem: () => undefined,
    removeItem: () => undefined,
  });
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
    ticket: "short-lived-ticket",
    protocol: "fairy.session.v1",
    expiresAtUnixMs: Date.now() + 30_000,
  }), { status: 201, headers: { "Content-Type": "application/json" } })));
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("DebugSessionClient", () => {
  it("uses a one-time protocol ticket and opens a role-targeted evaluation session", async () => {
    const events: TurnEvent[] = [];
    const { client, opened } = await DebugSessionClient.connect("character-2", "evaluation-key", {
      onTurnEvent: (event) => events.push(event),
      onDisconnect: () => undefined,
    });
    const socket = FakeWebSocket.instances[0];
    expect(socket.url).toBe("ws://localhost:3000/v1/session/ws");
    expect(socket.url).not.toContain("long-lived-api-token");
    expect(socket.protocols).toEqual(["fairy.session.v1", "fairy.session.ticket.short-lived-ticket"]);
    expect(socket.protocols.join(" ")).not.toContain("long-lived-api-token");
    const open = socket.sent.find((frame) => frame.type === "session.open") as any;
    expect(open.characterId).toBe("character-2");
    expect(open.interaction).toEqual({ audience: "single", initiation: "direct", presentation: "chat", evaluation: true });
    expect(opened.conversationId).toBe("conversation-1");

    await client.submitTurn(opened.conversationId, "测试");
    expect(events).toHaveLength(1);
    expect(events[0].state).toBe("completed");
    await client.cancelTurn(opened.conversationId, "turn-1");
    expect(socket.sent.some((frame) => frame.type === "turn.cancel" && frame.turnId === "turn-1")).toBe(true);
    client.close();
  });

  it("rejects pending work and reports a connection that drops after ready", async () => {
    const disconnected = vi.fn();
    const { client, opened } = await DebugSessionClient.connect("character-2", "evaluation-key", {
      onTurnEvent: () => undefined,
      onDisconnect: disconnected,
    });
    const socket = FakeWebSocket.instances[0];
    FakeWebSocket.respondToCancel = false;
    const pending = client.cancelTurn(opened.conversationId, "turn-2");
    socket.dispatchEvent(new Event("close"));

    await expect(pending).rejects.toThrow("Session 连接已断开");
    expect(disconnected).toHaveBeenCalledOnce();
  });
});
