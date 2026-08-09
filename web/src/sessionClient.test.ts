// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DebugSessionClient, type TurnEvent } from "./sessionClient";

class FakeWebSocket extends EventTarget {
  static readonly OPEN = 1;
  static readonly CLOSED = 3;
  static instances: FakeWebSocket[] = [];
  static respondToCancel = true;
  static rejectDelivery = false;

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
          state: "responding",
          payload: {
            type: "beat.ready",
            kind: "final",
            beatId: "final-0",
            displayText: "你好",
            part: { kind: "utterance", text: "你好", visualState: "happy" },
          },
        },
      });
      this.frame({
        type: "turn.event",
        conversationId: frame.conversationId,
        event: {
          conversationId: frame.conversationId,
          turnId: "turn-1",
          sequence: 2,
          state: "completed",
          payload: { type: "completed", text: "你好", usage: [] },
        },
      });
      this.frame({ type: "result", requestId: frame.requestId, payload: { outcome: { turnId: "turn-1" } } });
    } else if (frame.type === "expression.delivery") {
      this.frame({
        type: FakeWebSocket.rejectDelivery ? "error" : "ack",
        requestId: frame.requestId,
        conversationId: frame.conversationId,
        ...(FakeWebSocket.rejectDelivery ? { error: "expression delivery is not pending" } : {}),
      });
    } else if (frame.type === "turn.cancel" && FakeWebSocket.respondToCancel) {
      this.frame({ type: "ack", requestId: frame.requestId, conversationId: frame.conversationId });
    }
  }

  close() {
    this.readyState = FakeWebSocket.CLOSED;
    this.dispatchEvent(new Event("close"));
  }

  emit(value: unknown) {
    this.frame(value);
  }

  private frame(value: unknown) {
    this.dispatchEvent(new MessageEvent("message", { data: JSON.stringify(value) }));
  }
}

beforeEach(() => {
  FakeWebSocket.instances = [];
  FakeWebSocket.respondToCancel = true;
  FakeWebSocket.rejectDelivery = false;
  let requestNumber = 0;
  vi.stubGlobal("WebSocket", FakeWebSocket);
  vi.stubGlobal("crypto", { randomUUID: () => `request-${++requestNumber}` });
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

    await client.submitTurn(opened.conversationId, "测试", "debug-message-1");
    expect(socket.sent.find((frame) => frame.type === "turn.submit")).toMatchObject({
      conversationId: "conversation-1",
      input: "测试",
      messageId: "debug-message-1",
    });
    expect(events).toHaveLength(2);
    expect(events[0].payload.type).toBe("beat.ready");
    expect(events[1].state).toBe("completed");
    expect(socket.sent.find((frame) => frame.type === "expression.delivery")).toEqual(expect.objectContaining({
      type: "expression.delivery",
      conversationId: "conversation-1",
      deliveryResult: {
        conversationId: "conversation-1",
        turnId: "turn-1",
        beatId: "final-0",
        status: "succeeded",
      },
    }));
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

  it("acknowledges multiple accepted final utterances once and in event order", async () => {
    const accepted: string[] = [];
    const { client } = await DebugSessionClient.connect("character-2", "evaluation-key", {
      onTurnEvent: (event) => accepted.push(String(event.payload.beatId || event.payload.type)),
      onDisconnect: () => undefined,
    });
    const socket = FakeWebSocket.instances[0];
    const beat = (beatId: string, sequence: number, text: string) => ({
      type: "turn.event",
      conversationId: "conversation-1",
      event: {
        conversationId: "conversation-1",
        turnId: "turn-many",
        sequence,
        state: "responding",
        payload: {
          type: "beat.ready",
          kind: "final",
          beatId,
          displayText: text,
          part: { kind: "utterance", text, visualState: "idle" },
        },
      },
    });

    socket.emit(beat("final-0", 1, "第一段"));
    socket.emit(beat("final-1", 2, "第二段"));

    expect(accepted).toEqual(["final-0", "final-1"]);
    expect(socket.sent
      .filter((frame) => frame.type === "expression.delivery")
      .map((frame: any) => frame.deliveryResult.beatId)).toEqual(["final-0", "final-1"]);
    client.close();
  });

  it("does not acknowledge incomplete, progressive, or sticker beats", async () => {
    const { client } = await DebugSessionClient.connect("character-2", "evaluation-key", {
      onTurnEvent: () => undefined,
      onDisconnect: () => undefined,
    });
    const socket = FakeWebSocket.instances[0];
    const emitPayload = (payload: Record<string, unknown>, sequence: number) => socket.emit({
      type: "turn.event",
      conversationId: "conversation-1",
      event: { conversationId: "conversation-1", turnId: "turn-invalid", sequence, state: "responding", payload },
    });
    emitPayload({ type: "beat.ready", kind: "utterance", beatId: "utt-0", displayText: "处理中" }, 1);
    emitPayload({ type: "beat.ready", kind: "final", beatId: " final-0 ", displayText: "空白身份" }, 2);
    emitPayload({
      type: "beat.ready",
      kind: "final",
      beatId: "final-1",
      displayText: "",
      part: { kind: "sticker", sticker: { id: "wave" }, visualState: "happy" },
    }, 3);
    emitPayload({ type: "beat.ready", kind: "final", beatId: "final-2", displayText: "   " }, 4);

    expect(socket.sent.filter((frame) => frame.type === "expression.delivery")).toHaveLength(0);
    client.close();
  });

  it("closes explicitly when the page rejects an event", async () => {
    const disconnected = vi.fn();
    await DebugSessionClient.connect("character-2", "evaluation-key", {
      onTurnEvent: () => { throw new Error("render rejected"); },
      onDisconnect: disconnected,
    });
    const socket = FakeWebSocket.instances[0];
    socket.emit({
      type: "turn.event",
      conversationId: "conversation-1",
      event: {
        conversationId: "conversation-1",
        turnId: "turn-1",
        sequence: 1,
        state: "responding",
        payload: { type: "beat.ready", kind: "final", beatId: "final-0", displayText: "你好" },
      },
    });

    expect(disconnected).toHaveBeenCalledWith("页面无法接纳回复事件");
    expect(socket.sent.filter((frame) => frame.type === "expression.delivery")).toHaveLength(0);
    expect(socket.readyState).toBe(FakeWebSocket.CLOSED);
  });

  it("closes explicitly when Core rejects a delivery receipt", async () => {
    const disconnected = vi.fn();
    FakeWebSocket.rejectDelivery = true;
    await DebugSessionClient.connect("character-2", "evaluation-key", {
      onTurnEvent: () => undefined,
      onDisconnect: disconnected,
    });
    const socket = FakeWebSocket.instances[0];
    socket.emit({
      type: "turn.event",
      conversationId: "conversation-1",
      event: {
        conversationId: "conversation-1",
        turnId: "turn-1",
        sequence: 1,
        state: "responding",
        payload: { type: "beat.ready", kind: "final", beatId: "final-0", displayText: "你好" },
      },
    });

    await vi.waitFor(() => expect(disconnected).toHaveBeenCalledWith("回复投递回执失败"));
    expect(socket.readyState).toBe(FakeWebSocket.CLOSED);
  });

  it("does not create delivery receipts while opening historical state", async () => {
    const { client } = await DebugSessionClient.connect("character-2", "evaluation-key", {
      onTurnEvent: () => undefined,
      onDisconnect: () => undefined,
    });
    const socket = FakeWebSocket.instances[0];
    expect(socket.sent.filter((frame) => frame.type === "expression.delivery")).toHaveLength(0);
    client.close();
  });
});
