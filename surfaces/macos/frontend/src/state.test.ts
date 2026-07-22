import { describe, expect, it } from "vitest";
import { chatReducer, initialChatState } from "./state";

describe("chatReducer", () => {
  it("renders only final beats and never duplicates completed text", () => {
    let state = chatReducer(initialChatState, { type: "send", id: "t1", text: "你好" });
    state = chatReducer(state, { type: "beat", beat: { beatId: "u1", kind: "utterance", chainIndex: -1, displayText: "稍等", visualState: "idle" } });
    expect(state.messages[1].text).toBe("");
    state = chatReducer(state, { type: "beat", beat: { beatId: "b1", kind: "final", chainIndex: 0, displayText: "第一拍", visualState: "happy" } });
    state = chatReducer(state, { type: "beat", beat: { beatId: "b1", kind: "final", chainIndex: 0, displayText: "第一拍", visualState: "happy" } });
    state = chatReducer(state, { type: "completed", text: "第一拍" });
    expect(state.messages[1]).toMatchObject({ text: "第一拍", pending: false });
    expect(state.messages).toHaveLength(2);
  });

  it("removes uncertain assistant draft after interruption", () => {
    let state = chatReducer(initialChatState, { type: "send", id: "t1", text: "停止" });
    state = chatReducer(state, { type: "beat", beat: { beatId: "b1", kind: "final", chainIndex: 0, displayText: "未确认", visualState: "idle" } });
    state = chatReducer(state, { type: "interrupted" });
    expect(state.messages).toEqual([{ id: "t1", role: "user", text: "停止" }]);
    expect(state.activeTurn).toBeNull();
  });

  it("clears pending state when the session stream closes", () => {
    let state = chatReducer(initialChatState, { type: "send", id: "t1", text: "还在吗" });
    state = chatReducer(state, { type: "stream_closed", message: "连接已断开" });
    expect(state.messages).toEqual([{ id: "t1", role: "user", text: "还在吗" }]);
    expect(state).toMatchObject({ activeTurn: null, status: "error", error: "连接已断开" });
  });
});
