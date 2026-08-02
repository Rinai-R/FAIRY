import { open, readdir, readFile, rename, stat } from "node:fs/promises";
import path from "node:path";
import process from "node:process";
import { pathToFileURL } from "node:url";

const DEFAULT_DATA_DIR = "/root/llonebot/data";
const DEFAULT_INTERVAL_MS = 2000;
const ACCOUNT_CONFIG_PATTERN = /^config_[1-9][0-9]*\.json$/;

export class ConfigError extends Error {
  constructor(code) {
    super(code);
    this.name = "ConfigError";
    this.code = code;
  }
}

export function configureDocument(document, token) {
  if (!document || typeof document !== "object" || Array.isArray(document)) {
    throw new ConfigError("invalid_root");
  }
  if (typeof token !== "string" || token.length === 0) {
    throw new ConfigError("token_required");
  }
  if (!document.ob11 || typeof document.ob11 !== "object" || Array.isArray(document.ob11) || !Array.isArray(document.ob11.connect)) {
    throw new ConfigError("ob11_shape_unsupported");
  }
  const http = uniqueConnection(document.ob11.connect, "http");
  const httpPost = uniqueConnection(document.ob11.connect, "http-post");

  document.ob11.enable = true;
  Object.assign(http, {
    enable: true,
    host: "0.0.0.0",
    port: 3000,
    token,
  });
  Object.assign(httpPost, {
    enable: true,
    url: "http://qq-onebot:3002",
    token,
  });
  return document;
}

function uniqueConnection(connections, type) {
  const matches = connections.filter((connection) => connection && typeof connection === "object" && connection.type === type);
  if (matches.length !== 1) {
    throw new ConfigError(`${type.replace("-", "_")}_connection_required`);
  }
  return matches[0];
}

export async function configureFile(filePath, token) {
  let original;
  let document;
  try {
    original = await readFile(filePath, "utf8");
    document = JSON.parse(original);
  } catch {
    throw new ConfigError("invalid_json");
  }

  configureDocument(document, token);
  const next = `${JSON.stringify(document, null, 2)}\n`;
  if (next === original) {
    return false;
  }

  const sourceStat = await stat(filePath);
  const temporaryPath = path.join(path.dirname(filePath), `.${path.basename(filePath)}.${process.pid}.tmp`);
  let handle;
  try {
    handle = await open(temporaryPath, "w", sourceStat.mode & 0o777);
    await handle.writeFile(next, "utf8");
    await handle.sync();
    await handle.close();
    handle = undefined;
    await rename(temporaryPath, filePath);
  } catch (error) {
    if (handle) await handle.close().catch(() => undefined);
    throw new ConfigError("atomic_write_failed");
  }
  return true;
}

export async function configureAvailableFiles(dataDir, token, onResult = () => undefined) {
  let entries;
  try {
    entries = await readdir(dataDir, { withFileTypes: true });
  } catch (error) {
    if (error?.code === "ENOENT") return 0;
    throw new ConfigError("data_dir_unavailable");
  }
  const names = entries
    .filter((entry) => entry.isFile() && ACCOUNT_CONFIG_PATTERN.test(entry.name))
    .map((entry) => entry.name)
    .sort();
  for (const name of names) {
    try {
      const changed = await configureFile(path.join(dataDir, name), token);
      onResult({ name, changed, code: "ok" });
    } catch (error) {
      onResult({ name, changed: false, code: error instanceof ConfigError ? error.code : "unknown_error" });
    }
  }
  return names.length;
}

async function run() {
  const token = process.env.FAIRY_ONEBOT_TOKEN;
  if (typeof token !== "string" || token.length === 0) {
    console.error("llonebot-config: token_required");
    process.exitCode = 1;
    return;
  }
  const dataDir = process.env.LLONEBOT_DATA_DIR || DEFAULT_DATA_DIR;
  let stopped = false;
  process.once("SIGINT", () => { stopped = true; });
  process.once("SIGTERM", () => { stopped = true; });

  const reported = new Map();
  while (!stopped) {
    await configureAvailableFiles(dataDir, token, ({ name, changed, code }) => {
      const state = `${code}:${changed}`;
      if (reported.get(name) === state) return;
      reported.set(name, state);
      console.log(`llonebot-config: ${name}: ${code}${changed ? ": updated" : ""}`);
    }).catch((error) => {
      const code = error instanceof ConfigError ? error.code : "unknown_error";
      if (reported.get("data_dir") !== code) {
        reported.set("data_dir", code);
        console.error(`llonebot-config: data_dir: ${code}`);
      }
    });
    if (!stopped) await new Promise((resolve) => setTimeout(resolve, DEFAULT_INTERVAL_MS));
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  await run();
}

