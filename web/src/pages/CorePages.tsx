import { Button, Select, Switch, Text, TextArea, TextField } from "@radix-ui/themes";
import { useEffect, useState } from "react";
import { api } from "../api";
import { Field, PageHeader } from "../components/ui";

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

  async function reload() {
    const [next, visuals] = await Promise.all([
      api<Catalog>("/characters"),
      api<{ visualPacks: VisualPack[] }>("/visual-packs"),
    ]);
    setCatalog(next);
    setPacks(visuals.visualPacks || []);
    const pick = next.active?.characterId || next.characters[0]?.characterId || "";
    if (!selectedId && pick) selectCharacter(next, pick);
  }

  function selectCharacter(source: Catalog, id: string) {
    const c = source.characters.find((x) => x.characterId === id);
    setSelectedId(id);
    if (!c) return;
    setName(c.name);
    setDescription(c.description);
    setDialogueStyle(c.dialogueStyle || "");
    setTextLanguage(c.textLanguage || "zh");
    setSpeakingLanguage(c.speakingLanguage || "ja");
    setVisualPackId(c.appearance.visual?.packId || "");
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
      setSelectedId(record.characterId);
      await reload();
    } catch (e: any) {
      onToast(e.message, true);
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
    <section>
      <PageHeader
        title="角色库"
        description="选择当前角色，导入外部角色包，或把本地角色导出成可迁移的 .pack。"
        status={catalog?.active ? `当前：${catalog.active.name}` : "尚未激活"}
        ready={Boolean(catalog?.active)}
      />
      <div className="grid-2">
        <div className="card stack">
          <div className="form-actions" style={{ justifyContent: "space-between" }}>
            <Text weight="medium">角色列表</Text>
            <Button
              variant="soft"
              onClick={() => {
                setSelectedId("");
                setName("");
                setDescription("");
                setDialogueStyle("");
                setTextLanguage("zh");
                setSpeakingLanguage("ja");
                setVisualPackId(packs[0]?.packId || "");
              }}
            >
              新建
            </Button>
          </div>
          <div className="list">
            {(catalog?.characters || []).map((c) => (
              <button
                key={c.characterId}
                type="button"
                className={`list-item ${c.characterId === selectedId ? "active" : ""}`}
                onClick={() => catalog && selectCharacter(catalog, c.characterId)}
              >
                <strong>{c.name}</strong>
                <small>{c.appearance.status === "assigned" ? "外观已绑定" : "等待外观"}</small>
              </button>
            ))}
          </div>
          <label className="hint">
            导入角色包
            <input
              type="file"
              accept=".pack,.zip"
              style={{ display: "block", marginTop: 8 }}
              onChange={(e) => void importPack(e.target.files?.[0] || null)}
            />
          </label>
          <Button variant="soft" disabled={!selectedId} onClick={() => void exportPack()}>
            导出选中角色
          </Button>
        </div>

        <div className="card">
          <Text weight="medium">{isNew ? "新建角色" : "编辑选中角色"}</Text>
          <Text size="1" color="gray" as="p" mb="3">
            保存后会激活该角色；外观选择只影响桌宠画面。
          </Text>
          <Field label="角色名称">
            <TextField.Root value={name} onChange={(e) => setName(e.target.value)} maxLength={48} required />
          </Field>
          <Field label="角色描述" hint="写她会留意什么、如何表达亲近与边界。">
            <TextArea value={description} onChange={(e) => setDescription(e.target.value)} rows={4} />
          </Field>
          <Field label="日常说话方式">
            <TextArea value={dialogueStyle} onChange={(e) => setDialogueStyle(e.target.value)} rows={3} />
          </Field>
          <div className="grid-2">
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
            <Field label="语音语言">
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
          <div className="form-actions">
            <Button onClick={() => void save()}>{isNew ? "创建并激活" : "更新并激活"}</Button>
          </div>
          {selected ? (
            <Text size="1" color="gray" mt="2">
              ID {selected.characterId} · rev {selected.revision}
            </Text>
          ) : null}
        </div>
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
    <section>
      <PageHeader
        title="怎样称呼你"
        description="这个称呼会进入对话上下文，让文字与语音都能自然提到你。"
        status={name || "可以留空"}
        ready={Boolean(name)}
      />
      <div className="card">
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
      </div>
    </section>
  );
}

export function ModelPage({ onToast }: { onToast: (m: string, e?: boolean) => void }) {
  const [protocol, setProtocol] = useState("responses");
  const [endpoint, setEndpoint] = useState("");
  const [model, setModel] = useState("");
  const [contextWindowTokens, setCtx] = useState("1048576");
  const [authMode, setAuthMode] = useState("bearer_key");
  const [apiKey, setApiKey] = useState("");
  const [configured, setConfigured] = useState(false);

  async function reload() {
    const s = await api<any>("/config/model");
    setConfigured(Boolean(s.configured));
    if (s.configured) {
      setProtocol(s.protocol || "responses");
      setEndpoint(s.endpoint || "");
      setModel(s.model || "");
      setCtx(String(s.contextWindowTokens || 1048576));
      setAuthMode(s.authMode || "bearer_key");
    }
  }

  useEffect(() => {
    reload().catch((e) => onToast(e.message, true));
  }, []);

  return (
    <section>
      <PageHeader
        title="模型连接"
        description="明确选择协议；FAIRY 不会自动试错、切换接口或回退 Provider。"
        status={configured ? "已就绪" : "需要配置"}
        ready={configured}
      />
      <div className="card">
        <Field label="OpenAI 兼容协议">
          <Select.Root value={protocol} onValueChange={setProtocol}>
            <Select.Trigger />
            <Select.Content position="popper" side="bottom" align="start" sideOffset={6}>
              <Select.Item value="responses">Responses</Select.Item>
              <Select.Item value="chat_completions">Chat Completions</Select.Item>
            </Select.Content>
          </Select.Root>
        </Field>
        <div className="grid-2">
          <Field label="Base URL" hint="不要附带具体接口路径。">
            <TextField.Root value={endpoint} onChange={(e) => setEndpoint(e.target.value)} />
          </Field>
          <Field label="模型名称">
            <TextField.Root value={model} onChange={(e) => setModel(e.target.value)} />
          </Field>
        </div>
        <div className="grid-2">
          <Field label="上下文窗口">
            <TextField.Root value={contextWindowTokens} onChange={(e) => setCtx(e.target.value)} type="number" />
          </Field>
          <Field label="认证方式">
            <Select.Root value={authMode} onValueChange={setAuthMode}>
              <Select.Trigger />
              <Select.Content position="popper" side="bottom" align="start" sideOffset={6}>
                <Select.Item value="bearer_key">Bearer Key</Select.Item>
                <Select.Item value="no_auth">No Auth</Select.Item>
              </Select.Content>
            </Select.Root>
          </Field>
        </div>
        <Field label="API Key" hint="留空则保留已保存的密钥。">
          <TextField.Root type="password" value={apiKey} onChange={(e) => setApiKey(e.target.value)} />
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
      </div>
    </section>
  );
}

export function SpeechPage({ onToast }: { onToast: (m: string, e?: boolean) => void }) {
  const [enabled, setEnabled] = useState(false);
  const [configured, setConfigured] = useState(false);
  const [form, setForm] = useState({
    baseUrl: "",
    appId: "",
    synthesisResourceId: "",
    synthesisModel: "",
    defaultSpeaker: "",
    defaultFormat: "mp3",
    apiKey: "",
    accessToken: "",
  });

  async function reload() {
    const s = await api<any>("/config/speech");
    setConfigured(Boolean(s.configured));
    setEnabled(Boolean(s.enabled));
    setForm((f) => ({
      ...f,
      baseUrl: s.baseUrl || "",
      appId: s.appId || "",
      synthesisResourceId: s.synthesisResourceId || "",
      synthesisModel: s.synthesisModel || "",
      defaultSpeaker: s.defaultSpeaker || "",
      defaultFormat: s.defaultFormat || "mp3",
      apiKey: "",
      accessToken: "",
    }));
  }

  useEffect(() => {
    reload().catch((e) => onToast(e.message, true));
  }, []);

  function set<K extends keyof typeof form>(key: K, value: string) {
    setForm((f) => ({ ...f, [key]: value }));
  }

  return (
    <section>
      <PageHeader
        title="语音 TTS"
        description="火山语音克隆 HTTP；ASR / 打断后置。"
        status={enabled ? "已启用" : configured ? "已配置" : "未启用"}
        ready={enabled && configured}
      />
      <div className="card">
        <label style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 12 }}>
          <Switch checked={enabled} onCheckedChange={setEnabled} />
          <Text size="2">启用语音合成</Text>
        </label>
        <Field label="Base URL">
          <TextField.Root value={form.baseUrl} onChange={(e) => set("baseUrl", e.target.value)} />
        </Field>
        <div className="grid-2">
          <Field label="App ID">
            <TextField.Root value={form.appId} onChange={(e) => set("appId", e.target.value)} />
          </Field>
          <Field label="Synthesis resource ID">
            <TextField.Root value={form.synthesisResourceId} onChange={(e) => set("synthesisResourceId", e.target.value)} />
          </Field>
        </div>
        <div className="grid-2">
          <Field label="Synthesis model">
            <TextField.Root value={form.synthesisModel} onChange={(e) => set("synthesisModel", e.target.value)} />
          </Field>
          <Field label="Default speaker">
            <TextField.Root value={form.defaultSpeaker} onChange={(e) => set("defaultSpeaker", e.target.value)} />
          </Field>
        </div>
        <Field label="Default format">
          <TextField.Root value={form.defaultFormat} onChange={(e) => set("defaultFormat", e.target.value)} />
        </Field>
        <div className="grid-2">
          <Field label="API Key" hint="留空保留">
            <TextField.Root type="password" value={form.apiKey} onChange={(e) => set("apiKey", e.target.value)} />
          </Field>
          <Field label="Access Token" hint="留空保留">
            <TextField.Root type="password" value={form.accessToken} onChange={(e) => set("accessToken", e.target.value)} />
          </Field>
        </div>
        <div className="form-actions">
          <Button
            color="tomato"
            variant="soft"
            onClick={() =>
              void api("/config/speech", { method: "DELETE" })
                .then(() => {
                  onToast("语音已清除");
                  return reload();
                })
                .catch((e) => onToast(e.message, true))
            }
          >
            清除全部
          </Button>
          <Button
            onClick={() =>
              void api("/config/speech", {
                method: "PUT",
                body: JSON.stringify({
                  enabled,
                  baseUrl: form.baseUrl,
                  appId: form.appId,
                  synthesisResourceId: form.synthesisResourceId,
                  synthesisModel: form.synthesisModel,
                  defaultSpeaker: form.defaultSpeaker,
                  defaultFormat: form.defaultFormat,
                  defaultLanguage: 0,
                  apiKey: form.apiKey,
                  accessToken: form.accessToken,
                  clearApiKey: false,
                  clearAccessToken: false,
                  trainPath: "",
                  queryPath: "",
                  upgradePath: "",
                }),
              })
                .then(() => {
                  onToast("语音已保存");
                  return reload();
                })
                .catch((e) => onToast(e.message, true))
            }
          >
            保存
          </Button>
        </div>
      </div>
    </section>
  );
}
