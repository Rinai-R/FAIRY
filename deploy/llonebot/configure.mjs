import { chmod, mkdir, open, readdir, readFile, rename, rm, stat } from "node:fs/promises";
import path from "node:path";
import process from "node:process";
import { pathToFileURL } from "node:url";

const DEFAULT_DATA_DIR = "/app/llbot/data";
const DEFAULT_INTERVAL_MS = 2000;
const ACCOUNT_CONFIG_PATTERN = /^config_[1-9][0-9]*\.json$/;
const AUTH_TOKEN_FILE = "auth_token.txt";
const WEBUI_TOKEN_FILE = "webui_token.txt";

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

async function configureTokenFile(dataDir, token, fileName, codePrefix) {
  if (typeof token !== "string" || token.length === 0 || token !== token.trim()) {
    throw new ConfigError(`${codePrefix}_required`);
  }
  try {
    await mkdir(dataDir, { recursive: true, mode: 0o700 });
  } catch {
    throw new ConfigError("data_dir_unavailable");
  }

  const filePath = path.join(dataDir, fileName);
  const content = `${token}\n`;
  try {
    const [current, currentStat] = await Promise.all([readFile(filePath, "utf8"), stat(filePath)]);
    if (current === content) {
      if ((currentStat.mode & 0o777) !== 0o600) {
        await chmod(filePath, 0o600);
        return true;
      }
      return false;
    }
  } catch (error) {
    if (error?.code !== "ENOENT") throw new ConfigError(`${codePrefix}_read_failed`);
  }

  const temporaryPath = path.join(dataDir, `.${fileName}.${process.pid}.tmp`);
  let handle;
  try {
    await rm(temporaryPath, { force: true });
    handle = await open(temporaryPath, "wx", 0o600);
    await handle.writeFile(content, "utf8");
    await handle.sync();
    await handle.close();
    handle = undefined;
    await rename(temporaryPath, filePath);
    await chmod(filePath, 0o600);
  } catch {
    if (handle) await handle.close().catch(() => undefined);
    await rm(temporaryPath, { force: true }).catch(() => undefined);
    throw new ConfigError(`${codePrefix}_write_failed`);
  }
  return true;
}

export function configureAuthToken(dataDir, token) {
  return configureTokenFile(dataDir, token, AUTH_TOKEN_FILE, "auth_token");
}

export function configureWebUIToken(dataDir, token) {
  return configureTokenFile(dataDir, token, WEBUI_TOKEN_FILE, "webui_token");
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
  const webuiToken = process.env.LLBOT_WEBUI_TOKEN;
  if (typeof webuiToken !== "string" || webuiToken.length === 0 || webuiToken !== webuiToken.trim()) {
    console.error("llonebot-config: webui_token_required");
    process.exitCode = 1;
    return;
  }
  const authToken = process.env.LLBOT_AUTH_TOKEN;
  if (typeof authToken !== "string" || authToken.length === 0 || authToken !== authToken.trim()) {
    console.error("llonebot-config: auth_token_required");
    process.exitCode = 1;
    return;
  }
  const dataDir = process.env.LLBOT_DATA_DIR || DEFAULT_DATA_DIR;
  try {
    const [authChanged, webuiChanged] = await Promise.all([
      configureAuthToken(dataDir, authToken),
      configureWebUIToken(dataDir, webuiToken),
    ]);
    console.log(`llonebot-config: auth_token: ok${authChanged ? ": updated" : ""}`);
    console.log(`llonebot-config: webui_token: ok${webuiChanged ? ": updated" : ""}`);
  } catch (error) {
    const code = error instanceof ConfigError ? error.code : "unknown_error";
    console.error(`llonebot-config: initialization: ${code}`);
    process.exitCode = 1;
    return;
  }
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
