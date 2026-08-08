// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { Theme } from "@radix-ui/themes";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CharacterPage, ModelPage } from "./CorePages";
import { IntelligencePage } from "./MorePages";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function installCharacterEnvironment() {
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
}

function character(characterId: string, name: string) {
  return {
    characterId,
    revision: 1,
    name,
    description: `${name} 的角色描述`,
    dialogueStyle: `${name} 的说话方式`,
    textLanguage: "zh",
    speakingLanguage: "ja",
    appearance: { status: "unassigned" },
  };
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("CharacterPage deletion", () => {
  it("cancels without sending a delete request", async () => {
    installCharacterEnvironment();
    const alpha = character("character-alpha", "阿尔法");
    const requests: Array<{ path: string; method: string }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      requests.push({ path, method: init?.method || "GET" });
      if (path.endsWith("/visual-packs")) return json({ visualPacks: [] });
      return json({ active: alpha, characters: [alpha] });
    });
    vi.stubGlobal("fetch", fetchMock);
    vi.spyOn(window, "confirm").mockReturnValue(false);

    render(<Theme><CharacterPage onToast={() => undefined} /></Theme>);
    fireEvent.click(await screen.findByRole("button", { name: "删除角色" }));

    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining("阿尔法"));
    expect(requests.filter(({ method }) => method === "DELETE")).toHaveLength(0);
    expect(screen.getByDisplayValue("阿尔法")).toBeTruthy();
  });

  it("disables mutations and selects the first remaining character after deletion", async () => {
    installCharacterEnvironment();
    const alpha = character("character-alpha", "阿尔法");
    const beta = character("character-beta", "贝塔");
    let catalog: { active: typeof alpha | null; characters: typeof alpha[] } = {
      active: alpha,
      characters: [alpha, beta],
    };
    let resolveDelete!: () => void;
    const requests: Array<{ path: string; method: string }> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      const method = init?.method || "GET";
      requests.push({ path, method });
      if (method === "DELETE") {
        return new Promise<Response>((resolve) => {
          resolveDelete = () => {
            catalog = { active: null, characters: [beta] };
            resolve(new Response(null, { status: 204 }));
          };
        });
      }
      if (path.endsWith("/visual-packs")) return json({ visualPacks: [] });
      return json(catalog);
    }));
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const onToast = vi.fn();

    render(<Theme><CharacterPage onToast={onToast} /></Theme>);
    fireEvent.click(await screen.findByRole("button", { name: "删除角色" }));

    const deletingButton = await screen.findByRole("button", { name: "删除中" });
    expect(deletingButton.hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "更新并激活" }).hasAttribute("disabled")).toBe(true);
    expect(requests.filter(({ method }) => method === "DELETE")).toHaveLength(1);

    resolveDelete();
    await waitFor(() => expect(screen.getByDisplayValue("贝塔")).toBeTruthy());
    expect(screen.queryByDisplayValue("阿尔法")).toBeNull();
    expect(onToast).toHaveBeenCalledWith("角色“阿尔法”已删除");
  });

  it("returns to a blank creation form after deleting the last character", async () => {
    installCharacterEnvironment();
    const alpha = character("character-alpha", "阿尔法");
    let catalog = { active: alpha as typeof alpha | null, characters: [alpha] };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      if (init?.method === "DELETE") {
        catalog = { active: null, characters: [] };
        return new Response(null, { status: 204 });
      }
      if (path.endsWith("/visual-packs")) return json({ visualPacks: [] });
      return json(catalog);
    }));
    vi.spyOn(window, "confirm").mockReturnValue(true);

    render(<Theme><CharacterPage onToast={() => undefined} /></Theme>);
    fireEvent.click(await screen.findByRole("button", { name: "删除角色" }));

    expect(await screen.findByRole("heading", { name: "新建角色" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "删除角色" })).toBeNull();
    expect(screen.getByRole("button", { name: "创建并激活" })).toBeTruthy();
    expect(screen.queryByDisplayValue("阿尔法")).toBeNull();
  });

  it("preserves the current form and reports a delete failure", async () => {
    installCharacterEnvironment();
    const alpha = character("character-alpha", "阿尔法");
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      if (init?.method === "DELETE") return json({ error: "角色仍在使用中" }, 500);
      if (path.endsWith("/visual-packs")) return json({ visualPacks: [] });
      return json({ active: alpha, characters: [alpha] });
    }));
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const onToast = vi.fn();

    render(<Theme><CharacterPage onToast={onToast} /></Theme>);
    fireEvent.click(await screen.findByRole("button", { name: "删除角色" }));

    await waitFor(() => expect(onToast).toHaveBeenCalledWith("角色仍在使用中", true));
    expect(screen.getByDisplayValue("阿尔法")).toBeTruthy();
    expect(screen.getByRole("button", { name: "删除角色" }).hasAttribute("disabled")).toBe(false);
  });
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

  it("never submits provider none as enabled", async () => {
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
    let savedBody: Record<string, unknown> | undefined;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const semantic = {
        provider: "none", enabled: true, endpoint: "", model: "", dimensions: 0,
        credentialConfigured: false, reason: "semantic_embedding_disabled",
      };
      if (String(input).endsWith("/config/semantic-embedding") && init?.method === "PUT") {
        savedBody = JSON.parse(String(init.body));
        return new Response(JSON.stringify({ ...semantic, enabled: false }), { status: 200 });
      }
      const body = String(input).endsWith("/config/semantic-embedding") ? semantic : { configured: false };
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    }));

    render(<Theme><ModelPage onToast={() => undefined} /></Theme>);
    const toggle = await screen.findByRole("switch", { name: "启用语义检索" });
    expect(toggle.hasAttribute("disabled")).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "保存语义嵌入" }));
    await waitFor(() => expect(savedBody).toBeDefined());
    expect(savedBody).toMatchObject({ provider: "none", enabled: false });
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
    let semantic = {
      provider: "siliconflow", enabled: true, endpoint: "https://api.siliconflow.cn/v1",
      model: "BAAI/bge-m3", dimensions: 1024, credentialConfigured: true, reason: "",
    };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      requests.push({ input: path, init });
      if (path.endsWith("/config/semantic-embedding/credential")) {
        semantic = { ...semantic, enabled: false, credentialConfigured: false, reason: "semantic_embedding_credential_required" };
        return new Response(JSON.stringify(semantic), { status: 200 });
      }
      if (path.endsWith("/config/semantic-embedding") && init?.method === "PUT") {
        semantic = { ...semantic, enabled: false, credentialConfigured: true, reason: "semantic_embedding_disabled" };
        return new Response(JSON.stringify(semantic), { status: 200 });
      }
      if (path.endsWith("/config/semantic-embedding")) {
        return new Response(JSON.stringify(semantic), { status: 200 });
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
    await waitFor(() => expect(screen.getByRole("switch", { name: "启用语义检索" }).getAttribute("data-state")).toBe("unchecked"));
    expect(screen.getByDisplayValue("https://api.siliconflow.cn/v1")).toBeTruthy();
    expect(screen.getByText("semantic_embedding_disabled")).toBeTruthy();
    expect(screen.getByRole("button", { name: "删除凭据" })).toBeTruthy();

    fireEvent.click(await screen.findByRole("button", { name: "删除凭据" }));
    await waitFor(() => expect(requests.some(({ input, init }) => input.endsWith("/config/semantic-embedding/credential") && init?.method === "DELETE")).toBe(true));
    await waitFor(() => expect(screen.queryByRole("button", { name: "删除凭据" })).toBeNull());
    expect(screen.getByDisplayValue("https://api.siliconflow.cn/v1")).toBeTruthy();
    expect(screen.getByText("semantic_embedding_credential_required")).toBeTruthy();
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
