import assert from "node:assert/strict";
import { promises as fs } from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  archiveChange,
  hashTree,
  migrateHistory,
  validateChange,
  validateDomains,
  verifyMigratedHistory,
} from "./openspec-domain.mjs";

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const openspecBin = path.join(repositoryRoot, "node_modules", ".bin", process.platform === "win32" ? "openspec.cmd" : "openspec");

async function write(root, relative, content) {
  const target = path.join(root, relative);
  await fs.mkdir(path.dirname(target), { recursive: true });
  await fs.writeFile(target, content);
}

async function fixture() {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "fairy-openspec-test-"));
  await write(
    root,
    "openspec/domains/companion-runtime/knowledge.md",
    "# knowledge\n\n## 当前行为合同\n\n- `specs/turn-lifecycle.md`\n",
  );
  await write(
    root,
    "openspec/domains/companion-runtime/specs/turn-lifecycle.md",
    `# turn-lifecycle Specification

## Purpose
定义 FAIRY 单次 turn 从进入规划、执行工具、形成回复直到终态持久化的可观察生命周期行为和失败边界。

## Requirements

### Requirement: Turn completes
系统 SHALL 完成 turn。

#### Scenario: Successful turn
- **WHEN** 执行成功
- **THEN** turn completed

### Requirement: Legacy state
系统 SHALL 暂时保留旧状态。

#### Scenario: Read legacy state
- **WHEN** 读取旧状态
- **THEN** 返回旧状态
`,
  );
  return root;
}

async function readyChange(root, name, delta, tasks = "- [x] 1.1 done\n") {
  await write(root, `openspec/changes/${name}/proposal.md`, "# proposal\n");
  await write(root, `openspec/changes/${name}/tasks.md`, tasks);
  await write(
    root,
    `openspec/changes/${name}/acceptance.md`,
    "# acceptance\n\n- Result: PASS\n\n## 领域知识库同步\n\n- 已同步。\n",
  );
  if (delta) {
    await write(
      root,
      `openspec/changes/${name}/domains/companion-runtime/specs/turn-lifecycle.md`,
      delta,
    );
  }
}

test("validates canonical domain specs through official OpenSpec", async (t) => {
  const root = await fixture();
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const result = await validateDomains(root, { openspecBin });
  assert.equal(result.valid, true);
  assert.deepEqual(result.specs, [{ domain: "companion-runtime", capability: "turn-lifecycle" }]);
});

test("rejects legacy top-level specs as a second source of truth", async (t) => {
  const root = await fixture();
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  await write(root, "openspec/specs/legacy/spec.md", "# legacy\n");
  await assert.rejects(validateDomains(root, { openspecBin }), /顶层 openspec\/specs/);
});

test("archives a completed change and applies domain delta", async (t) => {
  const root = await fixture();
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  await readyChange(
    root,
    "add-cancel",
    `## ADDED Requirements

### Requirement: Turn cancellation
系统 SHALL 支持取消 turn。

#### Scenario: Cancel active turn
- **WHEN** 请求取消
- **THEN** turn interrupted
`,
  );
  await fs.appendFile(
    path.join(root, "openspec/domains/companion-runtime/knowledge.md"),
    "\n- 相关变更：`openspec/changes/add-cancel/`。\n",
  );

  const validation = await validateChange(root, "add-cancel", { openspecBin });
  assert.equal(validation.valid, true);
  const result = await archiveChange(root, "add-cancel", { openspecBin, date: "2026-07-26" });
  assert.equal(result.status, "completed");
  assert.match(await fs.readFile(path.join(root, "openspec/domains/companion-runtime/specs/turn-lifecycle.md"), "utf8"), /Turn cancellation/);
  assert.match(
    await fs.readFile(path.join(root, "openspec/domains/companion-runtime/knowledge.md"), "utf8"),
    /`openspec\/changes\/archive\/2026-07-26-add-cancel\/`/,
  );
  assert.equal(await fs.stat(path.join(root, "openspec/changes/archive/2026-07-26-add-cancel")).then(() => true), true);
  assert.equal((await validateDomains(root, { openspecBin })).valid, true);
});

test("validates spec-stage deltas before archive acceptance exists", async (t) => {
  const root = await fixture();
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  await write(root, "openspec/changes/spec-stage/proposal.md", "# proposal\n");
  await write(root, "openspec/changes/spec-stage/tasks.md", "- [ ] 1.1 pending\n");
  await write(
    root,
    "openspec/changes/spec-stage/domains/companion-runtime/specs/turn-lifecycle.md",
    `## ADDED Requirements

### Requirement: Spec-stage validation
系统 SHALL 在实现前验证领域 delta。

#### Scenario: Validate pending change
- **WHEN** change 仍处于规格阶段
- **THEN** delta strict validate 通过
`,
  );

  assert.equal((await validateChange(root, "spec-stage", { openspecBin })).valid, true);
  await assert.rejects(
    archiveChange(root, "spec-stage", { openspecBin, date: "2026-07-26" }),
    /缺少 acceptance.md|未完成任务/,
  );
});

test("archives a new capability with a strict-valid Purpose", async (t) => {
  const root = await fixture();
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  await readyChange(root, "add-observation", null);
  await write(
    root,
    "openspec/changes/add-observation/domains/companion-runtime/specs/desktop-observation.md",
    `## ADDED Requirements

### Requirement: Desktop observation
系统 SHALL 按需获取桌面观察证据。

#### Scenario: Observe desktop
- **WHEN** 模型请求桌面观察
- **THEN** 返回当前桌面证据
`,
  );
  await write(
    root,
    "openspec/domains/companion-runtime/knowledge.md",
    "# knowledge\n\n## 当前行为合同\n\n- `specs/turn-lifecycle.md`\n- `specs/desktop-observation.md`\n",
  );

  await archiveChange(root, "add-observation", { openspecBin, date: "2026-07-26" });
  const current = await fs.readFile(
    path.join(root, "openspec/domains/companion-runtime/specs/desktop-observation.md"),
    "utf8",
  );
  assert.match(current, /定义 FAIRY companion-runtime 领域中 desktop-observation 能力/);
  assert.doesNotMatch(current, /TBD - created by archiving change/);
  assert.equal((await validateDomains(root, { openspecBin })).valid, true);
});

test("rejects incomplete change without writes", async (t) => {
  const root = await fixture();
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  await readyChange(root, "incomplete-change", null, "- [ ] 1.1 pending\n");
  const before = await hashTree(root);
  await assert.rejects(
    archiveChange(root, "incomplete-change", { openspecBin, date: "2026-07-26" }),
    /未完成任务/,
  );
  assert.deepEqual(await hashTree(root), before);
});

test("applies complete MODIFIED and REMOVED requirements", async (t) => {
  const root = await fixture();
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  await readyChange(
    root,
    "revise-turn",
    `## MODIFIED Requirements

### Requirement: Turn completes
系统 SHALL 完成或明确中断 turn。

#### Scenario: Successful turn
- **WHEN** 执行成功
- **THEN** turn completed

#### Scenario: Interrupted turn
- **WHEN** 执行被取消
- **THEN** turn interrupted

## REMOVED Requirements

### Requirement: Legacy state
**Reason**: 当前生命周期已覆盖该状态。
**Migration**: 使用 turn terminal state。
`,
  );
  await archiveChange(root, "revise-turn", { openspecBin, date: "2026-07-26" });
  const current = await fs.readFile(path.join(root, "openspec/domains/companion-runtime/specs/turn-lifecycle.md"), "utf8");
  assert.match(current, /Interrupted turn/);
  assert.doesNotMatch(current, /Legacy state/);
});

test("invalid delta fails before touching domain specs", async (t) => {
  const root = await fixture();
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  await readyChange(
    root,
    "invalid-delta",
    `## MODIFIED Requirements

### Requirement: Missing requirement
系统 SHALL 不允许修改不存在的合同。

#### Scenario: Missing target
- **WHEN** delta 指向不存在的 Requirement
- **THEN** 归档失败
`,
  );
  const before = await hashTree(root);
  await assert.rejects(
    archiveChange(root, "invalid-delta", { openspecBin, date: "2026-07-26" }),
    /target spec|MODIFIED failed|失败/,
  );
  assert.deepEqual(await hashTree(root), before);
});

test("dry-run validates but changes no files", async (t) => {
  const root = await fixture();
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  await readyChange(root, "dry-run-change", null);
  const before = await hashTree(root);
  const result = await archiveChange(root, "dry-run-change", { openspecBin, date: "2026-07-26", dryRun: true });
  assert.equal(result.dryRun, true);
  assert.deepEqual(await hashTree(root), before);
});

test("superseded archive requires reason and never applies deltas", async (t) => {
  const root = await fixture();
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  await readyChange(
    root,
    "old-design",
    `## ADDED Requirements

### Requirement: Obsolete behavior
系统 SHALL NOT 合入该行为。

#### Scenario: Historical only
- **WHEN** 以 superseded 归档
- **THEN** 当前合同不改变
`,
  );
  await assert.rejects(
    archiveChange(root, "old-design", { openspecBin, superseded: true, date: "2026-07-26" }),
    /必须提供非空 --reason/,
  );
  const before = await fs.readFile(path.join(root, "openspec/domains/companion-runtime/specs/turn-lifecycle.md"), "utf8");
  const result = await archiveChange(root, "old-design", {
    openspecBin,
    superseded: true,
    reason: "已由当前设计取代",
    date: "2026-07-26",
  });
  assert.equal(result.status, "superseded");
  assert.equal(await fs.readFile(path.join(root, "openspec/domains/companion-runtime/specs/turn-lifecycle.md"), "utf8"), before);
  const metadata = JSON.parse(await fs.readFile(path.join(result.archivePath, "archive.json"), "utf8"));
  assert.equal(metadata.appliedDomainDeltas, false);
});

test("history migration is dry-runnable, hash-preserving, and leaves active changes", async (t) => {
  const root = await fixture();
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  await readyChange(root, "completed-history", null);
  await readyChange(root, "superseded-history", null, "- [ ] 1.1 obsolete\n");
  await readyChange(root, "active-work", null, "- [ ] 1.1 active\n");
  const manifestPath = path.join(root, "openspec/migrations/test.json");
  await write(
    root,
    "openspec/migrations/test.json",
    `${JSON.stringify({
      version: 1,
      archiveDate: "2026-07-26",
      policy: "当前合同已从证据重建。",
      active: ["active-work"],
      completed: ["completed-history"],
      superseded: [{ name: "superseded-history", reason: "已被新实现取代。" }],
    }, null, 2)}\n`,
  );

  const before = await hashTree(root);
  const dryRun = await migrateHistory(root, manifestPath, { dryRun: true });
  assert.equal(dryRun.moves, 2);
  assert.deepEqual(await hashTree(root), before);

  const result = await migrateHistory(root, manifestPath);
  assert.equal(result.archived, 2);
  const verification = await verifyMigratedHistory(root, manifestPath);
  assert.deepEqual(verification, { valid: true, active: 1, archived: 2 });
  assert.equal(await fs.stat(path.join(root, "openspec/changes/active-work")).then(() => true), true);
});
