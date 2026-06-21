export const DOCUMENT_SOURCE_MODES = Object.freeze(["upload", "text"]);

export function normalizeDocumentSourceMode(value) {
  return DOCUMENT_SOURCE_MODES.includes(value) ? value : "upload";
}

export function inferDocumentSourceMode(config) {
  if (!config) return "upload";
  if (DOCUMENT_SOURCE_MODES.includes(config.documentSourceMode)) return config.documentSourceMode;
  if (config.documentAsset?.path) return "upload";
  return "upload";
}

export function coreMaterialSourceFromState(mode, source = {}) {
  switch (normalizeDocumentSourceMode(mode)) {
    case "text":
      return source.documentText?.trim() ? { mode: "text", text: source.documentText } : {};
    case "upload":
      return source.documentAsset?.path ? {
        mode: "uploaded_file",
        asset_id: source.documentAsset.id || "",
        asset_name: source.documentAsset.filename || "",
        asset_type: source.documentAsset.content_type || "",
        asset_path: source.documentAsset.path
      } : {};
    default:
      return {};
  }
}

export function documentSourceReady(mode, source = {}) {
  switch (normalizeDocumentSourceMode(mode)) {
    case "upload":
      return Boolean(source.documentAsset?.path);
    case "text":
      return Boolean(source.documentText?.trim());
    default:
      return false;
  }
}

export function documentSourceLabel(mode, source = {}) {
  switch (normalizeDocumentSourceMode(mode)) {
    case "upload":
      return source.documentAsset?.filename || "尚未选择上传文件";
    case "text":
      return source.documentText?.trim() ? "粘贴正文" : "尚未粘贴正文";
    default:
      return "尚未选择文档源";
  }
}

export function documentSourceStatusMessage(mode, source = {}, documentImport = {}) {
  switch (normalizeDocumentSourceMode(mode)) {
    case "upload":
      return documentImport?.message || "选择一个本地材料文件。";
    case "text":
      return source.documentText?.trim() ? "粘贴正文已就绪。" : "请粘贴文章、课件、脚本或笔记正文。";
    default:
      return "请选择文档源。";
  }
}

export function documentSourceStateFromRecord(record) {
  const source = record?.teaching?.material_source || record?.generation?.request?.material_source || {};
  const mode = String(source.mode || "").trim();
  if (mode === "text") {
    return {
      mode: "text",
      documentText: source.text || "",
      documentAsset: null,
      message: "粘贴正文已就绪。"
    };
  }
  if (mode === "uploaded_file" || mode === "upload") {
    const assetPath = source.asset_path || "";
    return {
      mode: "upload",
      documentText: "",
      documentAsset: assetPath ? {
        id: source.asset_id || "",
        filename: source.asset_name || assetPath.split("/").at(-1) || "上传文件",
        content_type: source.asset_type || "",
        path: assetPath
      } : null,
      message: assetPath ? "上传文件已就绪。" : "选择一个本地材料文件。"
    };
  }
  return null;
}
