import assert from "node:assert/strict";
import {
  DOCUMENT_SOURCE_MODES,
  coreMaterialSourceFromState,
  documentSourceReady,
  documentSourceStateFromRecord,
  inferDocumentSourceMode,
  normalizeDocumentSourceMode,
} from "./documentSource.js";

const uploaded = {
  id: "asset-1",
  filename: "lesson.docx",
  content_type: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  path: "materials/lesson.docx",
};

assert.deepEqual(DOCUMENT_SOURCE_MODES, ["upload", "text"], "文档源入口只允许上传文件和粘贴正文");
assert.deepEqual(
  coreMaterialSourceFromState("text", { documentText: "  GMP 调度材料  " }),
  { mode: "text", text: "  GMP 调度材料  " },
  "粘贴正文应生成 text material_source",
);

assert.deepEqual(
  coreMaterialSourceFromState("upload", { documentAsset: uploaded }),
  {
    mode: "uploaded_file",
    asset_id: "asset-1",
    asset_name: "lesson.docx",
    asset_type: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
    asset_path: "materials/lesson.docx",
  },
  "上传文件应生成 uploaded_file material_source",
);

assert.deepEqual(coreMaterialSourceFromState("unsupported", { documentText: "旧入口内容" }), {});
assert.equal(documentSourceReady("unsupported", { documentText: "旧入口内容" }), false);
assert.equal(normalizeDocumentSourceMode("unsupported"), "upload");
assert.equal(inferDocumentSourceMode({ documentSourceMode: "unsupported" }), "upload");
assert.equal(documentSourceStateFromRecord({ teaching: { material_source: { mode: "unsupported" } } }), null);
assert.equal(
  documentSourceStateFromRecord({
    teaching: { variables: { imported_material: "materials/legacy.md" } },
  }),
  null,
  "历史恢复不应从 loose variables 复活文档源",
);
assert.equal(
  documentSourceStateFromRecord({
    teaching: {
      material_context: {
        report: {
          items: [{ source_type: "legacy", path: "", filename: "legacy-material" }],
        },
      },
    },
  }),
  null,
  "历史恢复不应从旧 material report 复活文档源",
);

console.log("documentSource tests passed");
