export type Beat = { beatId: string; kind?: string; chainIndex: number; displayText: string; visualState: string };
export type ChatMessage = { id: string; role: "user" | "assistant"; text: string; sequence?: number; pending?: boolean };
export type ChatState = { messages: ChatMessage[]; activeTurn: string | null; status: "idle" | "connecting" | "sending" | "error"; error: string | null; visualState: string };
export const initialChatState: ChatState = { messages: [], activeTurn: null, status: "idle", error: null, visualState: "idle" };

export function chatReducer(state: ChatState, action: any): ChatState {
  switch (action.type) {
    case "restore": return { ...state, messages: action.messages, status: "idle", error: null };
    case "connect": return { ...state, status: "connecting", error: null };
    case "connected": return { ...state, status: "idle", error: null };
    case "send": return { ...state, messages: [...state.messages, { id: action.id, role: "user", text: action.text }, { id: `${action.id}:pending`, role: "assistant", text: "", pending: true }], activeTurn: action.id, status: "sending", error: null };
    case "beat": {
      const beat = action.beat as Beat;
      if (beat.kind && beat.kind !== "final") return state;
      const pending = state.messages.findIndex((message) => message.pending && message.id.startsWith(`${state.activeTurn}:`));
      if (pending < 0 || state.messages[pending].text.includes(beat.displayText)) return { ...state, visualState: beat.visualState };
      const next = [...state.messages];
      next[pending] = { ...next[pending], text: next[pending].text ? `${next[pending].text}\n${beat.displayText}` : beat.displayText, pending: true };
      return { ...state, messages: next, visualState: beat.visualState };
    }
    case "completed": return { ...state, messages: state.messages.map((message) => message.pending ? { ...message, pending: false } : message), activeTurn: null, status: "idle" };
    case "interrupted": return { ...state, messages: state.messages.filter((message) => !message.pending), activeTurn: null, status: "idle" };
    case "failed": return { ...state, messages: state.messages.filter((message) => !message.pending), activeTurn: null, status: "error", error: action.message || "本轮请求失败" };
    case "error": return { ...state, status: "error", error: action.message };
    default: return state;
  }
}
