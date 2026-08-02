import { Button, Text, TextArea, Tooltip } from "@radix-ui/themes";
import { ExternalLinkIcon, PlusIcon, ReloadIcon, TrashIcon } from "@radix-ui/react-icons";
import { useEffect, useState } from "react";
import { api } from "../api";
import { Field, PageHeader } from "../components/ui";

type QQOneBotSettings = {
  schemaVersion: number;
  groupAllowlist: string[];
};

const MAX_GROUPS = 256;

function parseSettings(value: unknown): QQOneBotSettings {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new Error("QQ 接入配置响应必须是 object");
  }
  const record = value as Record<string, unknown>;
  if (record.schemaVersion !== 1 || !Array.isArray(record.groupAllowlist)) {
    throw new Error("QQ 接入配置响应格式无效");
  }
  const groups = record.groupAllowlist.map((item) => {
    if (typeof item !== "string" || !/^[1-9]\d*$/.test(item)) {
      throw new Error("QQ 接入配置包含无效群号");
    }
    return item;
  });
  return { schemaVersion: 1, groupAllowlist: groups };
}

function normalizeDraft(raw: string): string[] {
  const values = raw.split(/[\s,，;；]+/).filter(Boolean);
  const normalized: string[] = [];
  const seen = new Set<string>();
  for (const value of values) {
    if (!/^\d+$/.test(value)) {
      throw new Error(`群号 ${value} 不是正整数`);
    }
    const canonical = BigInt(value).toString(10);
    if (canonical === "0") {
      throw new Error("群号必须大于 0");
    }
    if (!seen.has(canonical)) {
      seen.add(canonical);
      normalized.push(canonical);
    }
  }
  return normalized;
}

export function IntegrationPage({ onToast }: { onToast: (message: string, error?: boolean) => void }) {
  const [groups, setGroups] = useState<string[]>([]);
  const [draft, setDraft] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [loadError, setLoadError] = useState("");

  async function reload() {
    setLoading(true);
    setLoadError("");
    try {
      const settings = parseSettings(await api<unknown>("/config/qq-onebot"));
      setGroups(settings.groupAllowlist);
    } catch (error) {
      setGroups([]);
      setLoadError(error instanceof Error ? error.message : String(error));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void reload();
  }, []);

  function addDraft() {
    try {
      const additions = normalizeDraft(draft);
      const next = [...groups];
      const seen = new Set(groups);
      for (const group of additions) {
        if (!seen.has(group)) {
          seen.add(group);
          next.push(group);
        }
      }
      if (next.length > MAX_GROUPS) {
        throw new Error(`最多允许 ${MAX_GROUPS} 个群`);
      }
      setGroups(next);
      setDraft("");
    } catch (error) {
      onToast(error instanceof Error ? error.message : String(error), true);
    }
  }

  async function save() {
    setSaving(true);
    try {
      const saved = parseSettings(await api<unknown>("/config/qq-onebot", {
        method: "PUT",
        body: JSON.stringify({ groupAllowlist: groups }),
      }));
      setGroups(saved.groupAllowlist);
      onToast("QQ 群接入配置已保存");
    } catch (error) {
      onToast(error instanceof Error ? error.message : String(error), true);
    } finally {
      setSaving(false);
    }
  }

  return (
    <section className="integration-page">
      <PageHeader
        title="接入"
        description="QQ 群参与范围与本机 LLOneBot 管理。"
        status={loading ? "读取中" : loadError ? "配置不可用" : `${groups.length} 个允许群`}
        ready={!loading && !loadError && groups.length > 0}
        action={
          <Button asChild variant="soft">
            <a href="http://127.0.0.1:3080" target="_blank" rel="noreferrer">
              <ExternalLinkIcon />
              LLOneBot
            </a>
          </Button>
        }
      />

      <div className="integration-tool">
        <div className="integration-tool-heading">
          <div>
            <Text weight="medium">允许参与的 QQ 群</Text>
            <Text as="p" size="1" color="gray">{groups.length} / {MAX_GROUPS}</Text>
          </div>
          <Tooltip content="重新读取群配置">
            <Button aria-label="重新读取群配置" variant="ghost" disabled={loading || saving} onClick={() => void reload()}>
              <ReloadIcon />
            </Button>
          </Tooltip>
        </div>

        {loadError ? (
          <div className="integration-error" role="alert">
            <span>{loadError}</span>
            <Button size="1" variant="soft" onClick={() => void reload()}>重试</Button>
          </div>
        ) : null}

        <div className="integration-compose">
          <Field label="群号">
            <TextArea
              aria-label="QQ 群号"
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              onKeyDown={(event) => {
                if ((event.metaKey || event.ctrlKey) && event.key === "Enter") addDraft();
              }}
              rows={3}
              placeholder={"123456789\n987654321"}
              disabled={loading || Boolean(loadError)}
            />
          </Field>
          <Button disabled={loading || Boolean(loadError) || !draft.trim()} onClick={addDraft}>
            <PlusIcon />
            添加
          </Button>
        </div>

        <div className="integration-group-list" aria-label="允许参与的 QQ 群列表">
          {!loading && !loadError && groups.length === 0 ? (
            <div className="integration-empty">当前拒绝全部 QQ 群</div>
          ) : groups.map((group) => (
            <div className="integration-group-row" key={group}>
              <code>{group}</code>
              <Tooltip content={`删除群 ${group}`}>
                <Button
                  aria-label={`删除群 ${group}`}
                  color="tomato"
                  variant="ghost"
                  disabled={saving}
                  onClick={() => setGroups((current) => current.filter((item) => item !== group))}
                >
                  <TrashIcon />
                </Button>
              </Tooltip>
            </div>
          ))}
        </div>

        <div className="form-actions">
          <Button disabled={loading || Boolean(loadError) || saving} onClick={() => void save()}>
            {saving ? "保存中" : "保存群配置"}
          </Button>
        </div>
      </div>
    </section>
  );
}

