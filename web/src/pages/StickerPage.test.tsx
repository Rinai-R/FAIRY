// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { Theme } from "@radix-ui/themes";
import { afterEach, describe, expect, it, vi } from "vitest";
import { StickerPage } from "./StickerPage";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("StickerPage", () => {
  it("makes human metadata the only editable meaning and saves it", async () => {
    vi.stubGlobal("ResizeObserver", class {
      observe() {}
      unobserve() {}
      disconnect() {}
    });
    vi.stubGlobal("localStorage", {
      getItem: () => "token",
      setItem: () => undefined,
      removeItem: () => undefined,
      clear: () => undefined,
      key: () => null,
      length: 0,
    });
    vi.stubGlobal("URL", {
      createObjectURL: () => "blob:sticker",
      revokeObjectURL: () => undefined,
    });
    const updates: any[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      if (path.endsWith("/stickers/s1/content")) {
        return new Response(new Uint8Array([0x89, 0x50]), {
          status: 200,
          headers: { "Content-Type": "image/png" },
        });
      }
      if (path.endsWith("/stickers/s1") && init?.method === "PUT") {
        updates.push(JSON.parse(String(init.body)));
        return new Response(JSON.stringify({
          id: "s1", contentSha256: "abc", mimeType: "image/png", byteCount: 2,
          description: updates[0].description, tags: updates[0].tags, status: updates[0].status,
          createdAtUnixMs: 1, updatedAtUnixMs: 2,
        }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      return new Response(JSON.stringify({
        items: [{
          id: "s1", contentSha256: "abc", mimeType: "image/png", byteCount: 2,
          description: "震惊", tags: ["无语"], status: "draft",
          createdAtUnixMs: 1, updatedAtUnixMs: 1,
        }],
        offset: 0, limit: 100, total: 1,
      }), { status: 200, headers: { "Content-Type": "application/json" } });
    }));

    render(<Theme><StickerPage onToast={() => undefined} /></Theme>);
    expect(await screen.findByDisplayValue("震惊")).toBeTruthy();
    expect(screen.getByText(/DeepSeek 只读取描述和标签/)).toBeTruthy();
    expect(screen.queryByText(/自动识别按钮/)).toBeNull();

    const description = screen.getByRole("textbox", { name: "人工描述" });
    fireEvent.change(description, { target: { value: "震惊但不要用于严肃消息" } });
    fireEvent.click(screen.getByRole("button", { name: "保存设置" }));

    await waitFor(() => expect(updates).toHaveLength(1));
    expect(updates[0]).toMatchObject({
      description: "震惊但不要用于严肃消息",
      tags: ["无语"],
      status: "draft",
    });
  });
});

