import { Button, Select, Switch, Text, TextArea, TextField } from "@radix-ui/themes";
import { useEffect, useState } from "react";
import { api } from "../api";
import { ConfigSection, Field, PageHeader } from "../components/ui";

type Character = {
  characterId: string;
  revision: number;
  name: string;
  description: string;
  dialogueStyle?: string | null;
  textLanguage: string;
  speakingLanguage: string;
  appearance: { status: string; visual?: { packId?: string; displayName?: string } };
};

type Catalog = { characters: Character[]; active: Character | null };
type VisualPack = { packId: string; displayName: string };

export function CharacterPage({ onToast }: { onToast: (m: string, e?: boolean) => void }) {
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [packs, setPacks] = useState<VisualPack[]>([]);
  const [selectedId, setSelectedId] = useState<string>("");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [dialogueStyle, setDialogueStyle] = useState("");
  const [textLanguage, setTextLanguage] = useState("zh");
  const [speakingLanguage, setSpeakingLanguage] = useState("ja");
  const [visualPackId, setVisualPackId] = useState("");
  const [deleting, setDeleting] = useState(false);

  async function reload(preferredId = selectedId) {
    const [next, visuals] = await Promise.all([
      api<Catalog>("/characters"),
      api<{ visualPacks: VisualPack[] }>("/visual-packs"),
    ]);
    const nextPacks = visuals.visualPacks || [];
    setCatalog(next);
    setPacks(nextPacks);
    const retainedId = preferredId && next.characters.some((c) => c.characterId === preferredId)
      ? preferredId
      : "";
    const pick = retainedId || next.active?.characterId || next.characters[0]?.characterId || "";
    if (pick) {
      selectCharacter(next, pick);
      return;
    }
    resetEditor(nextPacks[0]?.packId || "");
  }

  function selectCharacter(source: Catalog, id: string) {
    const c = source.characters.find((x) => x.characterId === id);
    if (!c) return;
    setSelectedId(id);
    setName(c.name);
    setDescription(c.description);
    setDialogueStyle(c.dialogueStyle || "");
    setTextLanguage(c.textLanguage || "zh");
    setSpeakingLanguage(c.speakingLanguage || "ja");
    setVisualPackId(c.appearance.visual?.packId || "");
  }

  function resetEditor(defaultVisualPackId = "") {
    setSelectedId("");
    setName("");
    setDescription("");
    setDialogueStyle("");
    setTextLanguage("zh");
    setSpeakingLanguage("ja");
    setVisualPackId(defaultVisualPackId);
  }

  useEffect(() => {
    reload().catch((e) => onToast(e.message, true));
  }, []);

  const selected = catalog?.characters.find((c) => c.characterId === selectedId);
  const isNew = !selectedId;

  async function save() {
    try {
      const brief = {
        name,
        description,
        dialogueStyle: dialogueStyle.trim() || null,
        textLanguage,
        speakingLanguage,
      };
      let record: Character;
      if (isNew) {
        record = await api<Character>("/characters", {
          method: "POST",
          body: JSON.stringify({ ...brief, visualPackId }),
        });
        record = await api<Character>(`/characters/${record.characterId}/activate`, {
          method: "POST",
          body: JSON.stringify({ revision: record.revision }),
        });
      } else {
        record = await api<Character>(`/characters/${selectedId}`, {
          method: "PUT",
          body: JSON.stringify(brief),
        });
        if (visualPackId) {
          record = await api<Character>(`/characters/${selectedId}/appearance`, {
            method: "POST",
            body: JSON.stringify({ visualPackId }),
          });
        }
        record = await api<Character>(`/characters/${selectedId}/activate`, {
          method: "POST",
          body: JSON.stringify({ revision: record.revision }),
        });
      }
      onToast(isNew ? "角色已创建并激活" : "角色已更新并激活");
      await reload(record.characterId);
    } catch (e: any) {
      onToast(e.message, true);
    }
  }

  async function deleteSelected() {
    if (!selected) return;
    const target = selected;
    const confirmed = window.confirm(
      `删除角色“${target.name}”？角色配置和外观绑定会被删除，历史会话、知识和记忆仍会保留。`,
    );
    if (!confirmed) return;

    setDeleting(true);
    try {
      await api(`/characters/${target.characterId}`, { method: "DELETE" });
      onToast(`角色“${target.name}”已删除`);
      await reload("");
    } catch (e: any) {
      onToast(e.message, true);
    } finally {
      setDeleting(false);
    }
  }

  async function importPack(file: File | null) {
    if (!file) return;
    try {
      const fd = new FormData();
      fd.append("file", file);
      await api("/characters/import", { method: "POST", body: fd });
      onToast("角色包已导入");
      await reload();
    } catch (e: any) {
      onToast(e.message, true);
    }
  }

  async function exportPack() {
    if (!selectedId) return;
    try {
      const token = localStorage.getItem("fairy.apiToken") || "";
      const headers: HeadersInit = {};
      if (token) headers.Authorization = `Bearer ${token}`;
      const res = await fetch(`/v1/characters/${selectedId}/export`, { headers });
      if (!res.ok) throw new Error((await res.json()).error || res.statusText);
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `${selectedId}.pack`;
      a.click();
      URL.revokeObjectURL(url);
      onToast("已开始下载角色包");
    } catch (e: any) {
      onToast(e.message, true);
    }
  }

  return (
    <section className="character-page">
      <PageHeader
        title="角色库"
        description="选择当前角色，导入外部角色包，或把本地角色导出成可迁移的 .pack。"
        status={catalog?.active ? `当前：${catalog.active.name}` : "尚未激活"}
        ready={Boolean(catalog?.active)}
      />
      <div className="master-detail character-workspace">
        <aside className="collection-pane character-index">
          <div className="pane-heading">
            <div>
              <span>角色</span>
              <strong>{catalog?.characters.length || 0} 个本地角色</strong>
            </div>
            <Button
              variant="soft"
              disabled={deleting}
              onClick={() => resetEditor(packs[0]?.packId || "")}
            >
              新建
            </Button>
          </div>
          <div className="collection-list">
            {(catalog?.characters || []).map((c) => (
              <button
                key={c.characterId}
                type="button"
                className={`collection-item ${c.characterId === selectedId ? "active" : ""}`}
                disabled={deleting}
                onClick={() => catalog && selectCharacter(catalog, c.characterId)}
              >
                <span className="collection-avatar" aria-hidden="true">{c.name.trim().slice(0, 1) || "F"}</span>
                <span>
                  <strong>{c.name}</strong>
                  <small>{c.appearance.status === "assigned" ? "外观已绑定" : "等待外观"}</small>
                </span>
              </button>
            ))}
          </div>
          <label className="collection-import">
            <span>导入角色包</span>
            <input
              type="file"
              accept=".pack,.zip"
              className="file-input"
              disabled={deleting}
              onChange={(e) => void importPack(e.target.files?.[0] || null)}
            />
          </label>
          <Button variant="soft" disabled={!selectedId || deleting} onClick={() => void exportPack()}>
            导出选中角色
          </Button>
        </aside>

        <section className="editor-pane character-editor">
          <header className="editor-heading">
            <div>
              <h2>{isNew ? "新建角色" : "编辑角色"}</h2>
              <p>保存后会激活该角色；外观选择只影响桌宠画面。</p>
            </div>
            {selected ? <span>版本 {selected.revision}</span> : null}
          </header>
          <Field label="角色名称">
            <TextField.Root value={name} onChange={(e) => setName(e.target.value)} maxLength={48} required />
          </Field>
          <Field label="角色描述" hint="写她会留意什么、如何表达亲近与边界。">
            <TextArea value={description} onChange={(e) => setDescription(e.target.value)} rows={4} />
          </Field>
          <Field label="日常说话方式">
            <TextArea value={dialogueStyle} onChange={(e) => setDialogueStyle(e.target.value)} rows={3} />
          </Field>
          <div className="form-grid form-grid-2">
            <Field label="文本语言">
              <Select.Root value={textLanguage} onValueChange={setTextLanguage}>
                <Select.Trigger />
                <Select.Content position="popper" side="bottom" align="start" sideOffset={6}>
                  <Select.Item value="zh">中文</Select.Item>
                  <Select.Item value="ja">日语</Select.Item>
                  <Select.Item value="en">英文</Select.Item>
                </Select.Content>
              </Select.Root>
            </Field>
            <Field label="角色语言偏好">
              <Select.Root value={speakingLanguage} onValueChange={setSpeakingLanguage}>
                <Select.Trigger />
                <Select.Content position="popper" side="bottom" align="start" sideOffset={6}>
                  <Select.Item value="ja">日语</Select.Item>
                  <Select.Item value="zh">中文</Select.Item>
                  <Select.Item value="en">英文</Select.Item>
                </Select.Content>
              </Select.Root>
            </Field>
          </div>
          <Field label="角色外观">
            <Select.Root value={visualPackId || undefined} onValueChange={setVisualPackId}>
              <Select.Trigger placeholder="选择角色外观" />
              <Select.Content position="popper" side="bottom" align="start" sideOffset={6}>
                {packs.map((p) => (
                  <Select.Item key={p.packId} value={p.packId}>
                    {p.displayName || p.packId}
                  </Select.Item>
                ))}
              </Select.Content>
            </Select.Root>
          </Field>
          <div className={`form-actions ${selected ? "spread" : ""}`}>
            {selected ? (
              <Button color="red" variant="soft" disabled={deleting} onClick={() => void deleteSelected()}>
                {deleting ? "删除中" : "删除角色"}
              </Button>
            ) : null}
            <Button disabled={deleting} onClick={() => void save()}>
              {isNew ? "创建并激活" : "更新并激活"}
            </Button>
          </div>
          {selected ? (
            <Text size="1" color="gray" mt="2">ID {selected.characterId}</Text>
          ) : null}
        </section>
      </div>
    </section>
  );
}

export function ProfilePage({ onToast }: { onToast: (m: string, e?: boolean) => void }) {
  const [name, setName] = useState("");
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    api<{ preferredName?: string | null }>("/profile")
      .then((p) => {
        setName(p.preferredName || "");
        setLoaded(true);
      })
      .catch((e) => onToast(e.message, true));
  }, []);

  return (
    <section className="profile-page config-flow narrow-flow">
      <PageHeader
        title="怎样称呼你"
        description="这个称呼会进入对话上下文，让角色在交流中自然提到你。"
        status={name || "可以留空"}
        ready={Boolean(name)}
      />
      <ConfigSection title="偏好称呼" description="这个称呼会进入每次对话的角色上下文。">
        <Field label="偏好称呼" hint="例如 Rinai、凛，或任何让你觉得自然的名字。">
          <TextField.Root
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="你希望她怎样叫你？"
            maxLength={64}
          />
        </Field>
        <div className="form-actions">
          <Button
            color="tomato"
            variant="soft"
            disabled={!loaded || !name}
            onClick={() =>
              void api("/profile", { method: "DELETE" })
                .then(() => {
                  setName("");
                  onToast("称呼已清除");
                })
                .catch((e) => onToast(e.message, true))
            }
          >
            清除称呼
          </Button>
          <Button
            onClick={() =>
              void api("/profile", {
                method: "PUT",
                body: JSON.stringify({ preferredName: name.trim() || null }),
              })
                .then(() => onToast("称呼已保存"))
                .catch((e) => onToast(e.message, true))
            }
          >
            保存称呼
          </Button>
        </div>
      </ConfigSection>
    </section>
  );
}

export function ModelPage({ onToast }: { onToast: (m: string, e?: boolean) => void }) {
  const [protocol, setProtocol] = useState("responses");
  const [endpoint, setEndpoint] = useState("");
  const [model, setModel] = useState("");
  const [contextWindowTokens, setCtx] = useState("1048576");
  const [authMode, setAuthMode] = useState("bearer_key");
  const [visionInput, setVisionInput] = useState(false);
  const [apiKey, setApiKey] = useState("");
  const [configured, setConfigured] = useState(false);
  const [semanticProvider, setSemanticProvider] = useState("none");
  const [semanticEnabled, setSemanticEnabled] = useState(false);
  const [semanticEndpoint, setSemanticEndpoint] = useState("");
  const [semanticModel, setSemanticModel] = useState("");
  const [semanticDimensions, setSemanticDimensions] = useState(0);
  const [semanticApiKey, setSemanticApiKey] = useState("");
  const [semanticCredentialConfigured, setSemanticCredentialConfigured] = useState(false);
  const [semanticReason, setSemanticReason] = useState("");

  async function reload() {
    const [chat, semantic] = await Promise.all([
      api<any>("/config/model"),
      api<any>("/config/semantic-embedding"),
    ]);
    setConfigured(Boolean(chat.configured));
    if (chat.configured) {
      setProtocol(chat.protocol || "responses");
      setEndpoint(chat.endpoint || "");
      setModel(chat.model || "");
      setCtx(String(chat.contextWindowTokens || 1048576));
      setAuthMode(chat.authMode || "bearer_key");
      setVisionInput(Boolean(chat.capabilities?.visionInput));
    }
    setSemanticProvider(semantic.provider || "none");
    setSemanticEnabled(Boolean(semantic.enabled));
    setSemanticEndpoint(semantic.endpoint || "");
    setSemanticModel(semantic.model || "");
    setSemanticDimensions(Number(semantic.dimensions || 0));
    setSemanticCredentialConfigured(Boolean(semantic.credentialConfigured));
    setSemanticReason(semantic.reason || "");
  }

  useEffect(() => {
    reload().catch((e) => onToast(e.message, true));
  }, []);

  return (
    <section className="model-page config-flow">
      <PageHeader
        title="模型连接"
        description="明确选择协议；FAIRY 不会自动试错、切换接口或回退提供商。"
        status={configured ? "已就绪" : "需要配置"}
        ready={configured}
      />
      <ConfigSection title="对话模型" description="负责对话、规划、记忆提取与知识整理。">
        <Field label="OpenAI 兼容协议">
          <Select.Root value={protocol} onValueChange={setProtocol}>
            <Select.Trigger />
            <Select.Content position="popper" side="bottom" align="start" sideOffset={6}>
              <Select.Item value="responses">Responses</Select.Item>
              <Select.Item value="chat_completions">Chat Completions</Select.Item>
            </Select.Content>
          </Select.Root>
        </Field>
        <div className="form-grid form-grid-2">
          <Field label="服务地址" hint="不要附带具体接口路径。">
            <TextField.Root value={endpoint} onChange={(e) => setEndpoint(e.target.value)} />
          </Field>
          <Field label="模型名称">
            <TextField.Root value={model} onChange={(e) => setModel(e.target.value)} />
          </Field>
        </div>
        <div className="form-grid form-grid-2">
          <Field label="上下文窗口">
            <TextField.Root value={contextWindowTokens} onChange={(e) => setCtx(e.target.value)} type="number" />
          </Field>
          <Field label="认证方式">
            <Select.Root value={authMode} onValueChange={setAuthMode}>
              <Select.Trigger />
              <Select.Content position="popper" side="bottom" align="start" sideOffset={6}>
                <Select.Item value="bearer_key">Bearer 密钥</Select.Item>
                <Select.Item value="no_auth">无需认证</Select.Item>
              </Select.Content>
            </Select.Root>
          </Field>
        </div>
        <Field label="API 密钥" hint="留空则保留已保存的密钥。">
          <TextField.Root type="password" value={apiKey} onChange={(e) => setApiKey(e.target.value)} />
        </Field>
        <Field label="视觉输入" hint="仅在确认当前模型支持图片输入时启用。">
          <Switch checked={visionInput} onCheckedChange={setVisionInput} aria-label="视觉输入" />
        </Field>
        <div className="form-actions">
          <Button
            color="tomato"
            variant="soft"
            onClick={() =>
              void api("/config/model", { method: "DELETE" })
                .then(() => {
                  onToast("模型已清除");
                  return reload();
                })
                .catch((e) => onToast(e.message, true))
            }
          >
            清除
          </Button>
          <Button
            onClick={() => {
              const body: any = {
                protocol,
                endpoint,
                model,
                contextWindowTokens: Number(contextWindowTokens),
                authMode,
                visionInput,
              };
              if (apiKey.trim()) body.apiKey = apiKey.trim();
              void api("/config/model", { method: "PUT", body: JSON.stringify(body) })
                .then(() => {
                  setApiKey("");
                  onToast("模型已保存");
                  return reload();
                })
                .catch((e) => onToast(e.message, true));
            }}
          >
            保存连接
          </Button>
        </div>
      </ConfigSection>
      <ConfigSection title="语义嵌入模型" description="启用后，允许生成向量的记忆与知识正文会发送到外部模型提供商。">
        <Field label="嵌入提供商">
          <Select.Root
            value={semanticProvider}
            onValueChange={(value) => {
              setSemanticProvider(value);
              if (value === "none") {
                setSemanticEnabled(false);
              } else if (value === "siliconflow") {
                setSemanticEndpoint("https://api.siliconflow.cn/v1");
                setSemanticModel("BAAI/bge-m3");
                setSemanticDimensions(1024);
                setSemanticEnabled(true);
              }
            }}
          >
            <Select.Trigger />
            <Select.Content position="popper" side="bottom" align="start" sideOffset={6}>
              <Select.Item value="none">不启用（仅全文检索）</Select.Item>
              <Select.Item value="siliconflow">SiliconFlow（BGE-M3）</Select.Item>
              <Select.Item value="openai_compatible_api">OpenAI 兼容接口</Select.Item>
            </Select.Content>
          </Select.Root>
        </Field>
        <label className="toggle-row">
          <Switch checked={semanticEnabled} onCheckedChange={setSemanticEnabled} disabled={semanticProvider === "none"} aria-label="启用语义检索" />
          <Text size="2">启用语义检索</Text>
        </label>
        <div className="form-grid form-grid-2">
          <Field label="嵌入服务地址">
            <TextField.Root value={semanticEndpoint} onChange={(e) => setSemanticEndpoint(e.target.value)} placeholder="https://api.siliconflow.cn/v1" />
          </Field>
          <Field label="嵌入模型">
            <TextField.Root value={semanticModel} onChange={(e) => setSemanticModel(e.target.value)} disabled={semanticProvider === "siliconflow"} />
          </Field>
        </div>
        <div className="form-grid form-grid-2">
          <Field label="向量维度">
            <TextField.Root type="number" value={String(semanticDimensions)} readOnly />
          </Field>
          <Field label="API 密钥（仅写入，不会回显）">
            <TextField.Root
              type="password"
              value={semanticApiKey}
              onChange={(e) => setSemanticApiKey(e.target.value)}
              autoComplete="new-password"
              placeholder={semanticCredentialConfigured ? "已配置，留空表示保持不变" : "输入提供商 API 密钥"}
            />
          </Field>
        </div>
        {semanticReason ? <Text size="1" color="orange">{semanticReason}</Text> : null}
        <div className="form-actions">
          {semanticCredentialConfigured ? (
            <Button
              variant="soft"
              color="red"
              onClick={() =>
                void api<any>("/config/semantic-embedding/credential", { method: "DELETE" })
                  .then(async () => {
                    setSemanticApiKey("");
                    await reload();
                    onToast("语义嵌入凭据已删除");
                  })
                  .catch((e) => onToast(e.message, true))
              }
            >
              删除凭据
            </Button>
          ) : null}
          <Button
            onClick={() =>
              void (async () => {
                const body: Record<string, unknown> = {
                  schema_version: 2,
                  provider: semanticProvider,
                  enabled: semanticProvider !== "none" && semanticEnabled,
                  endpoint: semanticEndpoint,
                  model: semanticModel,
                  dimensions: semanticDimensions,
                };
                if (semanticApiKey.trim() !== "") body.apiKey = semanticApiKey;
                await api<any>("/config/semantic-embedding", {
                  method: "PUT",
                  body: JSON.stringify(body),
                });
                setSemanticApiKey("");
                await reload();
                onToast("语义嵌入已保存");
              })().catch((e) => onToast(e.message, true))
            }
          >
            保存语义嵌入
          </Button>
        </div>
      </ConfigSection>
    </section>
  );
}
