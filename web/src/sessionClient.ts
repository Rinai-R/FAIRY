import { api } from "./api";

const SESSION_PROTOCOL = "fairy.session.v1";
const SESSION_TICKET_PREFIX = "fairy.session.ticket.";

export type CachedTokenObservation = {
  status: string;
  tokens?: number;
};

export type LaneModelUsage = {
  lane: string;
  historyWindow: number;
  usage: {
    inputTokens?: number;
    outputTokens?: number;
    cachedInputTokens: CachedTokenObservation;
    cacheWriteTokens: CachedTokenObservation;
  };
};

export type TurnEvent = {
  conversationId: string;
  turnId: string;
  sequence: number;
  state: string;
  payload: Record<string, unknown>;
};

export type SessionOpened = {
  conversationId: string;
  characterId: string;
  messageCount: number;
  endpoint: string;
};

type TicketResponse = {
  ticket: string;
  protocol: string;
  expiresAtUnixMs: number;
};

type ServerFrame = {
  type: string;
  requestId?: string;
  conversationId?: string;
  characterId?: string;
  messageCount?: number;
  endpoint?: string;
  error?: string;
  payload?: unknown;
  event?: TurnEvent;
};

type PendingRequest = {
  resolve: (frame: ServerFrame) => void;
  reject: (error: Error) => void;
  timer: ReturnType<typeof setTimeout>;
};

export type DebugSessionHandlers = {
  onTurnEvent: (event: TurnEvent) => void;
  onDisconnect: (reason: string) => void;
};

function requestID() {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `request-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function sessionURL() {
  const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${window.location.host}/v1/session/ws`;
}

export class DebugSessionClient {
  private readonly socket: WebSocket;
  private readonly handlers: DebugSessionHandlers;
  private readonly pending = new Map<string, PendingRequest>();
  private closed = false;

  private constructor(socket: WebSocket, handlers: DebugSessionHandlers) {
    this.socket = socket;
    this.handlers = handlers;
    socket.addEventListener("message", (event) => this.onMessage(event));
    socket.addEventListener("close", () => this.finish("Session 连接已断开"));
    socket.addEventListener("error", () => this.finish("Session 连接发生错误"));
  }

  static async connect(characterId: string, endpointKey: string, handlers: DebugSessionHandlers) {
    const ticket = await api<TicketResponse>("/session/browser-ticket", { method: "POST" });
    if (!ticket.ticket || ticket.protocol !== SESSION_PROTOCOL || ticket.expiresAtUnixMs <= Date.now()) {
      throw new Error("Core 返回了无效的 Session 票据");
    }
    const socket = new WebSocket(sessionURL(), [SESSION_PROTOCOL, `${SESSION_TICKET_PREFIX}${ticket.ticket}`]);
    const client = new DebugSessionClient(socket, handlers);
    await client.waitForReady();
    const opened = await client.request({
      type: "session.open",
      endpoint: "desktop",
      endpointKey,
      characterId,
      interaction: {
        audience: "single",
        initiation: "direct",
        presentation: "chat",
        evaluation: true,
      },
      outputCapabilities: { sticker: false },
    }, 15_000);
    if (!opened.conversationId || !opened.characterId) {
      client.close();
      throw new Error("Session open 响应缺少会话标识");
    }
    await client.request({ type: "session.watch", conversationId: opened.conversationId }, 15_000);
    return {
      client,
      opened: {
        conversationId: opened.conversationId,
        characterId: opened.characterId,
        messageCount: opened.messageCount || 0,
        endpoint: opened.endpoint || "desktop",
      } satisfies SessionOpened,
    };
  }

  submitTurn(conversationId: string, input: string) {
    return this.request({ type: "turn.submit", conversationId, input }, 10 * 60_000);
  }

  cancelTurn(conversationId: string, turnId: string) {
    return this.request({ type: "turn.cancel", conversationId, turnId }, 15_000);
  }

  close() {
    if (this.closed) return;
    this.closed = true;
    this.rejectPending(new Error("Session 已关闭"));
    this.socket.close(1000, "debug session closed");
  }

  private waitForReady() {
    return new Promise<void>((resolve, reject) => {
      const cleanup = () => {
        clearTimeout(timer);
        this.socket.removeEventListener("message", onMessage);
        this.socket.removeEventListener("close", onClose);
      };
      const timer = setTimeout(() => {
        cleanup();
        reject(new Error("等待 Session ready 超时"));
      }, 15_000);
      const onMessage = (event: MessageEvent) => {
        let frame: ServerFrame;
        try {
          frame = JSON.parse(String(event.data));
        } catch {
          cleanup();
          reject(new Error("Session 返回了无效 JSON"));
          return;
        }
        if (frame.type !== "ready") return;
        cleanup();
        if (this.socket.protocol !== SESSION_PROTOCOL) {
          reject(new Error("Session 协议协商失败"));
          return;
        }
        resolve();
      };
      const onClose = () => {
        cleanup();
        reject(new Error("Session 在 ready 前断开"));
      };
      this.socket.addEventListener("message", onMessage);
      this.socket.addEventListener("close", onClose, { once: true });
    });
  }

  private request(frame: Record<string, unknown>, timeoutMs: number) {
    if (this.closed || this.socket.readyState !== WebSocket.OPEN) {
      return Promise.reject(new Error("Session 尚未连接"));
    }
    const id = requestID();
    return new Promise<ServerFrame>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error("Session 请求超时"));
      }, timeoutMs);
      this.pending.set(id, { resolve, reject, timer });
      this.socket.send(JSON.stringify({ ...frame, requestId: id }));
    });
  }

  private onMessage(event: MessageEvent) {
    let frame: ServerFrame;
    try {
      frame = JSON.parse(String(event.data));
    } catch {
      this.finish("Session 返回了无效 JSON");
      return;
    }
    if (frame.type === "turn.event" && frame.event) {
      this.handlers.onTurnEvent(frame.event);
      return;
    }
    if (!frame.requestId) return;
    const pending = this.pending.get(frame.requestId);
    if (!pending) return;
    clearTimeout(pending.timer);
    this.pending.delete(frame.requestId);
    if (frame.type === "error") {
      pending.reject(new Error(frame.error || "Session 请求失败"));
      return;
    }
    pending.resolve(frame);
  }

  private rejectPending(error: Error) {
    for (const pending of this.pending.values()) {
      clearTimeout(pending.timer);
      pending.reject(error);
    }
    this.pending.clear();
  }

  private finish(reason: string) {
    if (this.closed) return;
    this.closed = true;
    this.rejectPending(new Error(reason));
    this.handlers.onDisconnect(reason);
  }
}
