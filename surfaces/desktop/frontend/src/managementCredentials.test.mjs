import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { extname, join } from "node:path";
import { fileURLToPath } from "node:url";

const frontendRoot = fileURLToPath(new URL("./", import.meta.url));
const bindingsRoot = fileURLToPath(new URL("../bindings", import.meta.url));

const forbiddenSourceTokens = [
  "FAIRY_API_TOKEN",
  "127.0.0.1:8787",
  "fairy.apiToken",
  "Authorization",
  "postgres://",
  "mysql://",
  "sk-live-",
];

function walkFiles(root, files = []) {
  for (const name of readdirSync(root)) {
    const path = join(root, name);
    const info = statSync(path);
    if (info.isDirectory()) {
      if (name === "node_modules") continue;
      walkFiles(path, files);
      continue;
    }
    if (name.includes(".test.")) continue;
    if ([".js", ".jsx", ".mjs", ".css"].includes(extname(name))) files.push(path);
  }
  return files;
}

function renderManagementDOM(fixtures) {
  return [
    `应用 ${fixtures.overview.bootstrap.appName} ${fixtures.overview.bootstrap.coreVersion}`,
    `存储 ${fixtures.overview.storage.storage} ${fixtures.overview.storage.ready ? "就绪" : "未就绪"}`,
    `密钥 ${fixtures.overview.secretKey.ready ? "已配置" : "未就绪"}`,
    `模型 ${fixtures.overview.model.configured ? `${fixtures.overview.model.model}（已配置）` : "未配置"}`,
    `语义检索 ${fixtures.overview.semanticEmbedding.configured ? "已配置" : "未配置"}`,
    `诊断 ${fixtures.overview.storage.error}`,
    `模型凭据 ${fixtures.modelStatus.configured ? "已配置" : "未配置"}`,
    `语义凭据 ${fixtures.semanticStatus.credentialConfigured ? "已配置" : "未配置"}`,
    fixtures.saveError.message,
    fixtures.pluginError.message,
    fixtures.qqSettings.groupAllowlist.join("\n"),
    `搜索 ${fixtures.webSearch.ready ? "就绪" : "未就绪"}`,
    fixtures.logEntry.message,
    `${fixtures.trace.spans[0].operation} · ${fixtures.trace.spans[0].status} · ${fixtures.trace.spans[0].durationMs}ms`,
    `路径 ${fixtures.backup.path}`,
    `文件数 ${fixtures.backup.fileCount}`,
  ].join("\n");
}

test("management production sources do not embed Core bearer, remote endpoint, or secret material", () => {
  const files = walkFiles(frontendRoot).concat(walkFiles(bindingsRoot));
  assert.ok(files.length > 0);
  for (const file of files) {
    const text = readFileSync(file, "utf8");
    for (const token of forbiddenSourceTokens) {
      assert.doesNotMatch(text, new RegExp(token.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")), `${file} contains ${token}`);
    }
    assert.doesNotMatch(text, /pmhq/i, `${file} contains PMHQ`);
    assert.doesNotMatch(text, /Bearer\s+\S+/);
  }
});

test("management DOM, log, toast, and error fixtures never echo credentials", () => {
  const secrets = {
    model: "sk-live-fixture-secret-value",
    semantic: "sf-semantic-fixture-secret",
    search: "search-provider-fixture-secret",
    plugin: "plugin-credential-fixture-secret",
    database: "seekdb-private-password",
    qq: "qq-access-token-fixture",
  };
  const fixtures = {
    overview: {
      bootstrap: { appName: "FAIRY", coreVersion: "dev" },
      secretKey: { ready: true, mode: "production" },
      model: { configured: true, model: "test", authMode: "bearer_key" },
      semanticEmbedding: { configured: true, credentialConfigured: true, provider: "siliconflow" },
      storage: { ready: false, storage: "seekdb", mode: "production", error: "dial failed with [seekdb-credential]" },
    },
    modelStatus: { configured: true, protocol: "openai_compatible_api", model: "test", authMode: "bearer_key", secretStorageMigrated: true },
    semanticStatus: { provider: "siliconflow", enabled: true, configured: true, credentialConfigured: true },
    webSearch: { enabled: true, ready: true },
    qqSettings: { schemaVersion: 1, groupAllowlist: ["123"] },
    pluginError: { message: "plugin host is not configured" },
    logEntry: { sequence: 1, level: "info", message: "runtime ready", fields: [{ key: "authorization", value: "[REDACTED]" }] },
    saveError: { message: "model credential is required" },
    backup: { path: "/tmp/fairy-backup", fileCount: 1, createdAtUnixMs: 1 },
    workspace: { section: "tracing", traceId: "trace-1", messageId: "message-1", logLevel: "warn", pluginInstanceId: "echo-1" },
    trace: { traceId: "trace-1", spans: [{ spanId: "span-1", operation: "model", status: "ok", durationMs: 12 }] },
    clearedForm: { protocol: "openai_compatible_api", endpoint: "", model: "test", apiKey: "" },
  };
  const rendered = renderManagementDOM(fixtures);
  const payload = JSON.stringify(fixtures) + rendered;
  for (const secret of Object.values(secrets)) {
    assert.doesNotMatch(payload, new RegExp(secret.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
  assert.doesNotMatch(payload, /Bearer\s+\S+/);
  assert.doesNotMatch(payload, /FAIRY_API_TOKEN|127\.0\.0\.1:8787|fairy\.apiToken|postgres:\/\/|mysql:\/\//);
  assert.doesNotMatch(payload, /"apiKey"\s*:\s*"[^"]+"/);
  assert.match(JSON.stringify(fixtures.clearedForm), /"apiKey":""/);
  assert.match(rendered, /模型凭据 已配置/);
  assert.match(rendered, /\[seekdb-credential\]/);
});

test("model save handler clears the password field and never logs or toasts the credential", () => {
  const source = readFileSync(new URL("./management.jsx", import.meta.url), "utf8");
  assert.match(source, /SaveManagementModel\(/);
  assert.match(source, /setForm\(\(current\) => \(\{ \.\.\.current, apiKey: "" \}\)\)/);
  assert.match(source, /type="password"/);
  assert.match(source, /data\?\.configured \? "已配置" : "未配置"/);
  assert.match(source, /semantic\.data\?\.credentialConfigured \? "已配置" : "未配置"/);
  assert.match(source, /function hostError\(err\) \{\n  if \(err && typeof err\.message === "string"/);
  assert.match(source, /<span>\{entry\.message\}<\/span>/);
  assert.doesNotMatch(source, /entry\.fields/);
  assert.doesNotMatch(source, /JSON\.stringify\((?:err|error|form|entry|data)\)/);
  assert.doesNotMatch(source, /console\.(?:log|debug|info|warn|error)\([^)]*apiKey/);
  assert.doesNotMatch(source, /setActionError\(form\.apiKey\)/);
  assert.doesNotMatch(source, /data\.storage\.(?:password|user|dsn)/);
  assert.doesNotMatch(source, /webSearch\.baseUrl/);
});
