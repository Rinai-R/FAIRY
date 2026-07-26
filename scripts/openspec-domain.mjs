#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { promises as fs } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const ID_PATTERN = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;
const BLOCKED_RESULT_PATTERN = /Result:\s*(?:\*\*)?(FAIL|PENDING|INCOMPLETE)\b/i;
const PASS_RESULT_PATTERN = /Result:\s*(?:\*\*)?PASS\b/i;

function fail(message) {
  throw new Error(message);
}

async function pathExists(target) {
  try {
    await fs.access(target);
    return true;
  } catch (error) {
    if (error?.code === "ENOENT") return false;
    throw error;
  }
}

async function readText(target) {
  return fs.readFile(target, "utf8");
}

function assertID(kind, value) {
  if (!ID_PATTERN.test(value) || value.includes("--")) {
    fail(`${kind} 必须是 kebab-case，且不得包含连续连字符：${value}`);
  }
}

export function encodeCapability(domain, capability) {
  assertID("domain", domain);
  assertID("capability", capability);
  return `${domain}--${capability}`;
}

export async function findProjectRoot(start = process.cwd()) {
  let current = path.resolve(start);
  while (true) {
    if (await pathExists(path.join(current, "openspec", "domains"))) return current;
    const parent = path.dirname(current);
    if (parent === current) fail(`无法从 ${start} 定位 FAIRY OpenSpec root`);
    current = parent;
  }
}

async function collectDomainSpecs(root, relativeRoot) {
  const base = path.join(root, relativeRoot);
  if (!(await pathExists(base))) return [];

  const result = [];
  const domains = await fs.readdir(base, { withFileTypes: true });
  for (const domainEntry of domains.sort((a, b) => a.name.localeCompare(b.name))) {
    if (!domainEntry.isDirectory()) continue;
    assertID("domain", domainEntry.name);
    const specsDir = path.join(base, domainEntry.name, "specs");
    if (!(await pathExists(specsDir))) continue;
    const entries = await fs.readdir(specsDir, { withFileTypes: true });
    for (const entry of entries.sort((a, b) => a.name.localeCompare(b.name))) {
      if (!entry.isFile() || path.extname(entry.name) !== ".md") continue;
      const capability = path.basename(entry.name, ".md");
      assertID("capability", capability);
      result.push({
        domain: domainEntry.name,
        capability,
        encoded: encodeCapability(domainEntry.name, capability),
        source: path.join(specsDir, entry.name),
      });
    }
  }

  const encoded = new Set();
  for (const spec of result) {
    if (encoded.has(spec.encoded)) fail(`领域 capability 映射冲突：${spec.encoded}`);
    encoded.add(spec.encoded);
  }
  return result;
}

function resolveOpenSpecBin(projectRoot, override) {
  if (override) return override;
  const executable = process.platform === "win32" ? "openspec.cmd" : "openspec";
  return path.join(projectRoot, "node_modules", ".bin", executable);
}

function runOpenSpec(projectRoot, cwd, args, options = {}) {
  const executable = resolveOpenSpecBin(projectRoot, options.openspecBin);
  const result = spawnSync(executable, args, {
    cwd,
    encoding: "utf8",
    env: { ...process.env, OPENSPEC_TELEMETRY: "0" },
    maxBuffer: 16 * 1024 * 1024,
  });
  if (result.error) throw result.error;
  if (result.status !== 0) {
    fail(`OpenSpec ${args.join(" ")} 失败\n${result.stdout}${result.stderr}`.trim());
  }
  return result.stdout;
}

async function copyFile(source, target) {
  await fs.mkdir(path.dirname(target), { recursive: true });
  await fs.copyFile(source, target);
}

async function prepareStage(projectRoot, changeName, options = {}) {
  const tempRoot = await fs.mkdtemp(path.join(os.tmpdir(), "fairy-openspec-"));
  const mainSpecs = await collectDomainSpecs(projectRoot, path.join("openspec", "domains"));
  const changeSpecs = changeName
    ? await collectDomainSpecs(projectRoot, path.join("openspec", "changes", changeName, "domains"))
    : [];

  for (const spec of mainSpecs) {
    await copyFile(spec.source, path.join(tempRoot, "openspec", "specs", spec.encoded, "spec.md"));
  }

  if (changeName) {
    const sourceChange = path.join(projectRoot, "openspec", "changes", changeName);
    const stagedChange = path.join(tempRoot, "openspec", "changes", changeName);
    await fs.mkdir(stagedChange, { recursive: true });
    for (const file of ["proposal.md", "design.md", "tasks.md", "acceptance.md"]) {
      const source = path.join(sourceChange, file);
      if (await pathExists(source)) await copyFile(source, path.join(stagedChange, file));
    }
    for (const spec of changeSpecs) {
      await copyFile(spec.source, path.join(stagedChange, "specs", spec.encoded, "spec.md"));
    }
  }

  return { tempRoot, mainSpecs, changeSpecs };
}

async function cleanupStage(stage) {
  if (stage?.tempRoot) await fs.rm(stage.tempRoot, { recursive: true, force: true });
}

export async function validateDomains(projectRoot, options = {}) {
  const legacySpecsDir = path.join(projectRoot, "openspec", "specs");
  if (await pathExists(legacySpecsDir)) fail("检测到已退出模型的顶层 openspec/specs/，请迁移到 domains/<domain>/specs/");
  const stage = await prepareStage(projectRoot, undefined, options);
  try {
    if (stage.mainSpecs.length === 0) fail("未找到任何领域行为合同");
    for (const spec of stage.mainSpecs) {
      runOpenSpec(projectRoot, stage.tempRoot, ["validate", spec.encoded, "--strict"], options);
    }

    const specsByDomain = new Map();
    for (const spec of stage.mainSpecs) {
      const specs = specsByDomain.get(spec.domain) ?? [];
      specs.push(spec);
      specsByDomain.set(spec.domain, specs);
    }
    const domainRoot = path.join(projectRoot, "openspec", "domains");
    const domainEntries = await fs.readdir(domainRoot, { withFileTypes: true });
    for (const entry of domainEntries) {
      if (!entry.isDirectory()) continue;
      const knowledgePath = path.join(domainRoot, entry.name, "knowledge.md");
      if (!(await pathExists(knowledgePath))) fail(`domain 缺少 knowledge.md：${entry.name}`);
      const specs = specsByDomain.get(entry.name) ?? [];
      if (specs.length === 0) fail(`domain 缺少行为合同：${entry.name}`);
      const knowledge = await readText(knowledgePath);
      for (const spec of specs) {
        const navigation = `\`specs/${spec.capability}.md\``;
        if (!knowledge.includes(navigation)) fail(`knowledge 缺少 capability 导航：${entry.name}/${spec.capability}`);
      }
      for (const match of knowledge.matchAll(/`(openspec\/changes\/[^`]+)`/g)) {
        const reference = match[1];
        if (reference.includes("*") || reference.includes("<")) continue;
        const target = path.join(projectRoot, reference.endsWith("/") ? reference.slice(0, -1) : reference);
        if (!(await pathExists(target))) fail(`knowledge 历史链接不存在：${entry.name} -> ${reference}`);
      }
    }
    return {
      valid: true,
      specs: stage.mainSpecs.map(({ domain, capability }) => ({ domain, capability })),
    };
  } finally {
    await cleanupStage(stage);
  }
}

async function assertArchiveReady(projectRoot, changeName) {
  assertID("change", changeName);
  const changeDir = path.join(projectRoot, "openspec", "changes", changeName);
  if (!(await pathExists(changeDir))) fail(`change 不存在：${changeName}`);

  const tasksPath = path.join(changeDir, "tasks.md");
  const acceptancePath = path.join(changeDir, "acceptance.md");
  if (!(await pathExists(tasksPath))) fail(`缺少 tasks.md：${changeName}`);
  if (!(await pathExists(acceptancePath))) fail(`缺少 acceptance.md：${changeName}`);

  const tasks = await readText(tasksPath);
  if (/^\s*-\s+\[\s\]/m.test(tasks)) fail(`change 仍有未完成任务：${changeName}`);

  const acceptance = await readText(acceptancePath);
  const blocked = acceptance.match(BLOCKED_RESULT_PATTERN);
  if (blocked) fail(`acceptance 仍包含 ${blocked[1].toUpperCase()}：${changeName}`);
  if (!PASS_RESULT_PATTERN.test(acceptance)) fail(`acceptance 未记录 Result: PASS：${changeName}`);
  if (!/^##\s+(Domain Knowledge Sync|领域知识库同步)\s*$/m.test(acceptance)) {
    fail(`acceptance 缺少领域知识库同步章节：${changeName}`);
  }
  return { changeDir, tasksPath, acceptancePath };
}

export async function validateChange(projectRoot, changeName, options = {}) {
  assertID("change", changeName);
  const changeDir = path.join(projectRoot, "openspec", "changes", changeName);
  if (!(await pathExists(changeDir))) fail(`change 不存在：${changeName}`);
  const stage = await prepareStage(projectRoot, changeName, options);
  try {
    if (stage.changeSpecs.length > 0) {
      runOpenSpec(projectRoot, stage.tempRoot, ["validate", changeName, "--strict"], options);
    }
    return {
      valid: true,
      deltas: stage.changeSpecs.map(({ domain, capability }) => ({ domain, capability })),
    };
  } finally {
    await cleanupStage(stage);
  }
}

async function buildMergedUpdates(projectRoot, changeName, options = {}) {
  await assertArchiveReady(projectRoot, changeName);
  const stage = await prepareStage(projectRoot, changeName, options);
  try {
    if (stage.changeSpecs.length === 0) return { stage, updates: [] };
    runOpenSpec(projectRoot, stage.tempRoot, ["validate", changeName, "--strict"], options);
    runOpenSpec(projectRoot, stage.tempRoot, ["archive", changeName, "--yes", "--json"], options);

    const existingSpecs = new Set(stage.mainSpecs.map((spec) => spec.encoded));
    const updates = [];
    for (const spec of stage.changeSpecs) {
      const merged = path.join(stage.tempRoot, "openspec", "specs", spec.encoded, "spec.md");
      if (!existingSpecs.has(spec.encoded)) {
        const generatedPurpose = `TBD - created by archiving change ${changeName}. Update Purpose after archive.`;
        const domainPurpose = `定义 FAIRY ${spec.domain} 领域中 ${spec.capability} 能力的当前有效行为、输入输出、边界条件、失败语义与可验收场景。`;
        const content = await readText(merged);
        if (!content.includes(generatedPurpose)) {
          fail(`新 capability 缺少 OpenSpec 生成的 Purpose 占位符：${spec.domain}/${spec.capability}`);
        }
        await fs.writeFile(merged, content.replace(generatedPurpose, domainPurpose));
      }
      runOpenSpec(projectRoot, stage.tempRoot, ["validate", spec.encoded, "--strict"], options);
      const target = path.join(
        projectRoot,
        "openspec",
        "domains",
        spec.domain,
        "specs",
        `${spec.capability}.md`,
      );
      updates.push({ target, content: await fs.readFile(merged) });
    }
    return { stage, updates };
  } catch (error) {
    await cleanupStage(stage);
    throw error;
  }
}

async function applyUpdates(updates) {
  const transaction = [];
  try {
    for (const update of updates) {
      const previous = (await pathExists(update.target)) ? await fs.readFile(update.target) : null;
      await fs.mkdir(path.dirname(update.target), { recursive: true });
      const temporary = `${update.target}.tmp-${process.pid}-${Date.now()}`;
      await fs.writeFile(temporary, update.content);
      transaction.push({ ...update, previous, temporary, applied: false });
    }
    for (const entry of transaction) {
      await fs.rename(entry.temporary, entry.target);
      entry.applied = true;
    }
  } catch (error) {
    await rollbackUpdates(transaction);
    throw error;
  }
  return async () => rollbackUpdates(transaction);
}

async function buildKnowledgeLinkUpdates(projectRoot, changeName, date) {
  const domainRoot = path.join(projectRoot, "openspec", "domains");
  const activeReference = `openspec/changes/${changeName}/`;
  const archiveReference = `openspec/changes/archive/${date}-${changeName}/`;
  const updates = [];
  const entries = await fs.readdir(domainRoot, { withFileTypes: true });
  for (const entry of entries) {
    if (!entry.isDirectory()) continue;
    const target = path.join(domainRoot, entry.name, "knowledge.md");
    if (!(await pathExists(target))) continue;
    const content = await readText(target);
    if (!content.includes(activeReference)) continue;
    updates.push({ target, content: Buffer.from(content.replaceAll(activeReference, archiveReference)) });
  }
  return updates;
}

async function rollbackUpdates(transaction) {
  for (const entry of [...transaction].reverse()) {
    if (entry.applied) {
      if (entry.previous === null) await fs.rm(entry.target, { force: true });
      else await fs.writeFile(entry.target, entry.previous);
    }
    await fs.rm(entry.temporary, { force: true });
  }
}

function archiveMetadata(status, date, reason, appliedDomainDeltas) {
  return `${JSON.stringify({ status, archivedAt: date, reason, appliedDomainDeltas }, null, 2)}\n`;
}

export async function archiveChange(projectRoot, changeName, options = {}) {
  assertID("change", changeName);
  const date = options.date ?? new Date().toISOString().slice(0, 10);
  if (!/^\d{4}-\d{2}-\d{2}$/.test(date)) fail(`archive date 无效：${date}`);
  const changeDir = path.join(projectRoot, "openspec", "changes", changeName);
  if (!(await pathExists(changeDir))) fail(`change 不存在：${changeName}`);
  const archivePath = path.join(projectRoot, "openspec", "changes", "archive", `${date}-${changeName}`);
  if (await pathExists(archivePath)) fail(`archive 目标已存在：${archivePath}`);

  if (options.superseded) {
    const reason = options.reason?.trim();
    if (!reason) fail("superseded 归档必须提供非空 --reason");
    if (options.dryRun) return { change: changeName, status: "superseded", archivePath, dryRun: true };
    await fs.mkdir(path.dirname(archivePath), { recursive: true });
    const metadataPath = path.join(changeDir, "archive.json");
    await fs.writeFile(metadataPath, archiveMetadata("superseded", date, reason, false));
    try {
      await fs.rename(changeDir, archivePath);
    } catch (error) {
      await fs.rm(metadataPath, { force: true });
      throw error;
    }
    return { change: changeName, status: "superseded", archivePath, applied: [] };
  }

  const { stage, updates } = await buildMergedUpdates(projectRoot, changeName, options);
  try {
    const knowledgeUpdates = await buildKnowledgeLinkUpdates(projectRoot, changeName, date);
    if (options.dryRun) {
      return {
        change: changeName,
        status: "completed",
        archivePath,
        dryRun: true,
        applied: updates.map((entry) => path.relative(projectRoot, entry.target)),
      };
    }

    const rollback = await applyUpdates([...updates, ...knowledgeUpdates]);
    const metadataPath = path.join(changeDir, "archive.json");
    await fs.writeFile(metadataPath, archiveMetadata("completed", date, null, updates.length > 0));
    try {
      await fs.mkdir(path.dirname(archivePath), { recursive: true });
      await fs.rename(changeDir, archivePath);
    } catch (error) {
      await fs.rm(metadataPath, { force: true });
      await rollback();
      throw error;
    }
    return {
      change: changeName,
      status: "completed",
      archivePath,
      applied: updates.map((entry) => path.relative(projectRoot, entry.target)),
    };
  } finally {
    await cleanupStage(stage);
  }
}

export async function listChanges(projectRoot) {
  const changesDir = path.join(projectRoot, "openspec", "changes");
  const entries = await fs.readdir(changesDir, { withFileTypes: true });
  const changes = [];
  for (const entry of entries.sort((a, b) => a.name.localeCompare(b.name))) {
    if (!entry.isDirectory() || entry.name === "archive") continue;
    const tasksPath = path.join(changesDir, entry.name, "tasks.md");
    const tasks = (await pathExists(tasksPath)) ? await readText(tasksPath) : "";
    const total = [...tasks.matchAll(/^\s*-\s+\[[ xX]\]/gm)].length;
    const completed = [...tasks.matchAll(/^\s*-\s+\[[xX]\]/gm)].length;
    changes.push({ name: entry.name, completed, total, status: total > 0 && completed === total ? "tasks-complete" : "active" });
  }
  return changes;
}

export async function validateMigrationManifest(projectRoot, manifestPath) {
  const manifest = JSON.parse(await readText(manifestPath));
  if (manifest.version !== 1) fail(`不支持的 migration manifest version：${manifest.version}`);
  if (!/^\d{4}-\d{2}-\d{2}$/.test(manifest.archiveDate ?? "")) fail("migration manifest archiveDate 无效");
  if (!Array.isArray(manifest.active) || !Array.isArray(manifest.completed) || !Array.isArray(manifest.superseded)) {
    fail("migration manifest 必须包含 active、completed、superseded 数组");
  }

  const classified = [];
  for (const name of manifest.active) classified.push({ name, status: "active" });
  for (const name of manifest.completed) classified.push({ name, status: "completed" });
  for (const entry of manifest.superseded) {
    if (!entry?.reason?.trim()) fail(`superseded change 缺少 reason：${entry?.name ?? "<unknown>"}`);
    classified.push({ name: entry.name, status: "superseded" });
  }

  const seen = new Set();
  for (const entry of classified) {
    assertID("change", entry.name);
    if (seen.has(entry.name)) fail(`migration manifest 重复分类：${entry.name}`);
    seen.add(entry.name);
  }

  const actual = new Set((await listChanges(projectRoot)).map((entry) => entry.name));
  const missing = [...actual].filter((name) => !seen.has(name)).sort();
  const unknown = [...seen].filter((name) => !actual.has(name)).sort();
  if (missing.length || unknown.length) {
    fail(`migration manifest 覆盖不完整；missing=${missing.join(",") || "none"}；unknown=${unknown.join(",") || "none"}`);
  }

  for (const name of manifest.completed) {
    const changeDir = path.join(projectRoot, "openspec", "changes", name);
    const tasks = await readText(path.join(changeDir, "tasks.md"));
    if (/^\s*-\s+\[\s\]/m.test(tasks)) fail(`completed change 仍有未完成任务：${name}`);
    const acceptancePath = path.join(changeDir, "acceptance.md");
    if (!(await pathExists(acceptancePath))) fail(`completed change 缺少 acceptance：${name}`);
    const acceptance = await readText(acceptancePath);
    if (BLOCKED_RESULT_PATTERN.test(acceptance)) fail(`completed change acceptance 含阻塞结果：${name}`);
  }

  return {
    valid: true,
    archiveDate: manifest.archiveDate,
    counts: {
      active: manifest.active.length,
      completed: manifest.completed.length,
      superseded: manifest.superseded.length,
      total: classified.length,
    },
  };
}

function migrationEntries(manifest) {
  return [
    ...manifest.completed.map((name) => ({
      name,
      status: "historical-completed",
      reason: manifest.policy,
    })),
    ...manifest.superseded.map((entry) => ({
      name: entry.name,
      status: "superseded",
      reason: entry.reason,
    })),
  ].sort((a, b) => a.name.localeCompare(b.name));
}

export async function migrateHistory(projectRoot, manifestPath, options = {}) {
  const validation = await validateMigrationManifest(projectRoot, manifestPath);
  const manifest = JSON.parse(await readText(manifestPath));
  const entries = migrationEntries(manifest);
  const changesDir = path.join(projectRoot, "openspec", "changes");
  const prepared = [];

  for (const entry of entries) {
    const source = path.join(changesDir, entry.name);
    const target = path.join(changesDir, "archive", `${manifest.archiveDate}-${entry.name}`);
    if (!(await pathExists(source))) fail(`迁移源不存在：${source}`);
    if (await pathExists(target)) fail(`迁移目标已存在：${target}`);
    prepared.push({ ...entry, source, target, before: await hashTree(source) });
  }

  if (options.dryRun) {
    return { ...validation, dryRun: true, moves: prepared.length };
  }

  await fs.mkdir(path.join(changesDir, "archive"), { recursive: true });
  const moved = [];
  try {
    for (const entry of prepared) {
      const metadataPath = path.join(entry.source, "archive.json");
      await fs.writeFile(
        metadataPath,
        archiveMetadata(entry.status, manifest.archiveDate, entry.reason, false),
      );
      await fs.rename(entry.source, entry.target);
      moved.push(entry);

      const after = await hashTree(entry.target);
      for (const [relative, digest] of entry.before) {
        if (after.get(relative) !== digest) fail(`归档内容 hash 不一致：${entry.name}/${relative}`);
      }
      if (after.size !== entry.before.size + 1 || !after.has("archive.json")) {
        fail(`归档文件数量不一致：${entry.name}`);
      }
    }
  } catch (error) {
    for (const entry of [...moved].reverse()) {
      if (await pathExists(entry.target)) {
        await fs.rename(entry.target, entry.source);
        await fs.rm(path.join(entry.source, "archive.json"), { force: true });
      }
    }
    throw error;
  }

  return {
    valid: true,
    dryRun: false,
    active: manifest.active.length,
    archived: moved.length,
    completed: manifest.completed.length,
    superseded: manifest.superseded.length,
  };
}

export async function verifyMigratedHistory(projectRoot, manifestPath) {
  const manifest = JSON.parse(await readText(manifestPath));
  const active = (await listChanges(projectRoot)).map((entry) => entry.name).sort();
  const expectedActive = [...manifest.active].sort();
  if (JSON.stringify(active) !== JSON.stringify(expectedActive)) {
    fail(`迁移后 active change 不一致；actual=${active.join(",")}；expected=${expectedActive.join(",")}`);
  }

  const changesDir = path.join(projectRoot, "openspec", "changes");
  for (const entry of migrationEntries(manifest)) {
    const target = path.join(changesDir, "archive", `${manifest.archiveDate}-${entry.name}`);
    if (!(await pathExists(target))) fail(`缺少归档目录：${target}`);
    const metadata = JSON.parse(await readText(path.join(target, "archive.json")));
    if (metadata.status !== entry.status || metadata.appliedDomainDeltas !== false) {
      fail(`归档 metadata 不一致：${entry.name}`);
    }
  }
  return { valid: true, active: expectedActive.length, archived: migrationEntries(manifest).length };
}

export async function hashTree(root) {
  const result = new Map();
  async function visit(current) {
    const entries = await fs.readdir(current, { withFileTypes: true });
    for (const entry of entries.sort((a, b) => a.name.localeCompare(b.name))) {
      const target = path.join(current, entry.name);
      if (entry.isDirectory()) await visit(target);
      else if (entry.isFile()) {
        const digest = createHash("sha256").update(await fs.readFile(target)).digest("hex");
        result.set(path.relative(root, target), digest);
      }
    }
  }
  await visit(root);
  return result;
}

function parseArchiveOptions(args) {
  const options = {};
  for (let index = 0; index < args.length; index++) {
    const arg = args[index];
    if (arg === "--dry-run") options.dryRun = true;
    else if (arg === "--superseded") options.superseded = true;
    else if (arg === "--reason") options.reason = args[++index];
    else if (arg === "--date") options.date = args[++index];
    else fail(`未知参数：${arg}`);
  }
  return options;
}

async function main() {
  const [command, subject, ...args] = process.argv.slice(2);
  const projectRoot = await findProjectRoot();
  let result;
  switch (command) {
    case "list":
      result = await listChanges(projectRoot);
      break;
    case "validate-domains":
      result = await validateDomains(projectRoot);
      break;
    case "validate":
      if (!subject) fail("用法：openspec-domain validate <change>");
      result = await validateChange(projectRoot, subject);
      break;
    case "validate-migration": {
      const manifestPath = subject
        ? path.resolve(projectRoot, subject)
        : path.join(projectRoot, "openspec", "migrations", "2026-07-26-domain-workflow.json");
      result = await validateMigrationManifest(projectRoot, manifestPath);
      break;
    }
    case "migrate-history": {
      const manifestPath = subject
        ? path.resolve(projectRoot, subject)
        : path.join(projectRoot, "openspec", "migrations", "2026-07-26-domain-workflow.json");
      result = await migrateHistory(projectRoot, manifestPath, { dryRun: args.includes("--dry-run") });
      break;
    }
    case "verify-migration": {
      const manifestPath = subject
        ? path.resolve(projectRoot, subject)
        : path.join(projectRoot, "openspec", "migrations", "2026-07-26-domain-workflow.json");
      result = await verifyMigratedHistory(projectRoot, manifestPath);
      break;
    }
    case "archive":
      if (!subject) fail("用法：openspec-domain archive <change> [--dry-run|--superseded --reason ...]");
      result = await archiveChange(projectRoot, subject, parseArchiveOptions(args));
      break;
    default:
      fail("用法：openspec-domain <list|validate-domains|validate|validate-migration|migrate-history|verify-migration|archive> [...]");
  }
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
}

const invokedPath = process.argv[1] ? pathToFileURL(path.resolve(process.argv[1])).href : "";
if (invokedPath === import.meta.url) {
  main().catch((error) => {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  });
}

export const scriptDirectory = path.dirname(fileURLToPath(import.meta.url));
