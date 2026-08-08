import { Button, Select, TextArea, TextField } from "@radix-ui/themes";
import { useEffect, useState } from "react";
import { api, apiBlob } from "../api";
import { EmptyState, Field, PageHeader } from "../components/ui";

type StickerStatus = "draft" | "active" | "disabled";

type StickerRecord = {
  id: string;
  contentSha256: string;
  mimeType: string;
  byteCount: number;
  description: string;
  tags: string[];
  status: StickerStatus;
  createdAtUnixMs: number;
  updatedAtUnixMs: number;
};

type StickerPageResponse = {
  items: StickerRecord[];
  offset: number;
  limit: number;
  total: number;
};

function StickerPreview({ record }: { record: StickerRecord }) {
  const [src, setSrc] = useState("");
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let active = true;
    let objectURL = "";
    setFailed(false);
    apiBlob(`/stickers/${record.id}/content`)
      .then((blob) => {
        if (!active) return;
        objectURL = URL.createObjectURL(blob);
        setSrc(objectURL);
      })
      .catch(() => {
        if (active) setFailed(true);
      });
    return () => {
      active = false;
      if (objectURL) URL.revokeObjectURL(objectURL);
    };
  }, [record.id, record.contentSha256]);

  if (failed) return <span className="sticker-preview unavailable">图片不可用</span>;
  if (!src) return <span className="sticker-preview loading">读取中</span>;
  return <img className="sticker-preview" src={src} alt={record.description || "待描述表情包"} />;
}

function parseTags(value: string): string[] {
  return value
    .split(/[,，\n]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

export function StickerPage({ onToast }: { onToast: (message: string, error?: boolean) => void }) {
  const [items, setItems] = useState<StickerRecord[]>([]);
  const [selectedID, setSelectedID] = useState("");
  const [uploadFile, setUploadFile] = useState<File | null>(null);
  const [description, setDescription] = useState("");
  const [tags, setTags] = useState("");
  const [status, setStatus] = useState<StickerStatus>("draft");
  const [busy, setBusy] = useState(false);

  async function reload(preferredID = selectedID) {
    const page = await api<StickerPageResponse>("/stickers?limit=100");
    setItems(page.items || []);
    const nextID = (page.items || []).some((item) => item.id === preferredID)
      ? preferredID
      : page.items?.[0]?.id || "";
    setSelectedID(nextID);
    const selected = page.items?.find((item) => item.id === nextID);
    if (selected) loadRecord(selected);
  }

  function loadRecord(record: StickerRecord) {
    setSelectedID(record.id);
    setDescription(record.description);
    setTags(record.tags.join("，"));
    setStatus(record.status);
  }

  useEffect(() => {
    reload().catch((error) => onToast(error.message, true));
  }, []);

  async function upload() {
    if (!uploadFile) {
      onToast("请先选择图片", true);
      return;
    }
    setBusy(true);
    try {
      const form = new FormData();
      form.append("file", uploadFile);
      form.append("description", description.trim());
      form.append("tags", JSON.stringify(parseTags(tags)));
      form.append("status", status);
      const created = await api<StickerRecord>("/stickers", { method: "POST", body: form });
      setUploadFile(null);
      onToast(status === "active" ? "表情包已上传并启用" : "表情包已保存");
      await reload(created.id);
    } catch (error: any) {
      onToast(error.message, true);
    } finally {
      setBusy(false);
    }
  }

  async function save() {
    if (!selectedID) return;
    setBusy(true);
    try {
      const updated = await api<StickerRecord>(`/stickers/${selectedID}`, {
        method: "PUT",
        body: JSON.stringify({
          description: description.trim(),
          tags: parseTags(tags),
          status,
        }),
      });
      onToast(updated.status === "active" ? "人工语义已保存并启用" : "表情包设置已保存");
      await reload(updated.id);
    } catch (error: any) {
      onToast(error.message, true);
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    if (!selectedID || !window.confirm("删除这个表情包？历史消息仍只保留发送时的文字快照。")) return;
    setBusy(true);
    try {
      await api(`/stickers/${selectedID}`, { method: "DELETE" });
      onToast("表情包已删除");
      setSelectedID("");
      setDescription("");
      setTags("");
      setStatus("draft");
      await reload("");
    } catch (error: any) {
      onToast(error.message, true);
    } finally {
      setBusy(false);
    }
  }

  const activeCount = items.filter((item) => item.status === "active").length;
  const selected = items.find((item) => item.id === selectedID);

  return (
    <section className="sticker-page">
      <PageHeader
        title="表情包"
        description="图片含义由人工描述。模型只读取描述与标签，不会自动看图、OCR 或猜测语义。"
        status={`${activeCount} 个可用 / ${items.length} 个`}
        ready={activeCount > 0}
      />
      <div className="master-detail sticker-workspace">
        <aside className="collection-pane sticker-library">
          <div className="pane-heading">
            <div>
              <span>资产库</span>
              <strong>{items.length} 个表情包</strong>
            </div>
          </div>
          <div className="sticker-grid" aria-label="表情包资产">
            {items.length === 0 ? (
              <EmptyState title="还没有表情包" description="先选择图片并补充人工语义，再上传到本机资产库。" />
            ) : items.map((record) => (
              <button
                type="button"
                key={record.id}
                className={`sticker-tile ${record.id === selectedID ? "active" : ""}`}
                onClick={() => loadRecord(record)}
              >
                <StickerPreview record={record} />
                <span className="sticker-tile-copy">
                  <small>{record.description || "等待人工描述"}</small>
                  <span className={`sticker-status ${record.status}`}>{stickerStatusLabel(record.status)}</span>
                </span>
              </button>
            ))}
          </div>
          <div className="collection-upload">
          <Field label="上传图片" hint="JPEG、PNG、GIF 或 WebP，最大 5 MiB。上传内容按 SHA-256 去重。">
            <input
              aria-label="上传表情包图片"
              type="file"
              accept="image/jpeg,image/png,image/gif,image/webp"
              onChange={(event) => setUploadFile(event.target.files?.[0] || null)}
            />
          </Field>
          <Button disabled={!uploadFile || busy} onClick={() => void upload()}>
            上传当前图片
          </Button>
          </div>
        </aside>

        <section className="editor-pane sticker-editor">
          <header className="editor-heading">
            <div>
              <h2>{selected ? "编辑人工语义" : "新资产默认信息"}</h2>
              <p>人工描述是模型理解图片含义的唯一依据；没有描述的资产不能启用。</p>
            </div>
          </header>
          <Field label="人工描述" hint="描述它表达的情绪、语境和使用边界，不要只写文件名。">
            <TextArea
              aria-label="人工描述"
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              rows={5}
              maxLength={512}
              placeholder="例如：震惊又无语，适合对方说出离谱内容时回应；不要用于严肃坏消息。"
            />
          </Field>
          <Field label="标签" hint="使用逗号分隔，最多 16 个。">
            <TextField.Root
              aria-label="表情包标签"
              value={tags}
              onChange={(event) => setTags(event.target.value)}
              placeholder="震惊，无语，吐槽"
            />
          </Field>
          <Field label="状态">
            <Select.Root value={status} onValueChange={(value) => setStatus(value as StickerStatus)}>
              <Select.Trigger aria-label="表情包状态" />
              <Select.Content position="popper" side="bottom" align="start" sideOffset={6}>
                <Select.Item value="draft">待完善</Select.Item>
                <Select.Item value="active">已启用</Select.Item>
                <Select.Item value="disabled">已停用</Select.Item>
              </Select.Content>
            </Select.Root>
          </Field>
          <div className="form-actions">
            <Button
              color="tomato"
              variant="soft"
              disabled={!selectedID || busy}
              onClick={() => void remove()}
            >
              删除
            </Button>
            <Button disabled={!selectedID || busy} onClick={() => void save()}>
              保存设置
            </Button>
          </div>
          {selected ? (
            <div className="asset-meta">
              <span>{selected.mimeType}</span>
              <span>{(selected.byteCount / 1024).toFixed(1)} KiB</span>
              <code>{selected.id}</code>
            </div>
          ) : null}
        </section>
      </div>
    </section>
  );
}

function stickerStatusLabel(status: StickerStatus) {
  return { draft: "待完善", active: "已启用", disabled: "已停用" }[status];
}
