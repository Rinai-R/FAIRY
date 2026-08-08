import assert from "node:assert/strict";
import { mkdtemp, readFile, stat, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { ConfigError, configureAuthToken, configureAvailableFiles, configureFile, configureWebUIToken } from "./configure.mjs";

const secret = "test-onebot-token-not-for-production";
const webuiSecret = "test-webui-token-not-for-production";
const authSecret = "test-auth-token-not-for-production";

function fixture() {
  return {
    webui: { enable: true, host: "127.0.0.1", port: 3080 },
    ob11: {
      enable: false,
      connect: [
        { type: "ws", enable: false, token: "", custom: "keep-ws" },
        { type: "http", enable: false, host: "127.0.0.1", port: 3000, token: "", custom: "keep-http" },
        { type: "http-post", enable: false, url: "", token: "", custom: "keep-post" },
      ],
    },
    customTopLevel: { keep: true },
  };
}

test("configures account JSON atomically and remains idempotent", async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "fairy-llonebot-config-"));
  const filePath = path.join(directory, "config_123456.json");
  await writeFile(filePath, `${JSON.stringify(fixture(), null, 2)}\n`, { mode: 0o640 });

  assert.equal(await configureFile(filePath, secret), true);
  const first = await readFile(filePath, "utf8");
  const document = JSON.parse(first);
  const http = document.ob11.connect.find((connection) => connection.type === "http");
  const httpPost = document.ob11.connect.find((connection) => connection.type === "http-post");
  assert.deepEqual(
    { enabled: document.ob11.enable, host: http.host, port: http.port, token: http.token, custom: http.custom },
    { enabled: true, host: "0.0.0.0", port: 3000, token: secret, custom: "keep-http" },
  );
  assert.deepEqual(
    { enable: httpPost.enable, url: httpPost.url, token: httpPost.token, custom: httpPost.custom },
    { enable: true, url: "http://qq-onebot:3002", token: secret, custom: "keep-post" },
  );
  assert.deepEqual(document.customTopLevel, { keep: true });
  assert.equal((await stat(filePath)).mode & 0o777, 0o640);

  assert.equal(await configureFile(filePath, secret), false);
  assert.equal(await readFile(filePath, "utf8"), first);
});

test("initializes the WebUI token atomically with private permissions", async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "fairy-llbot-webui-"));
  const filePath = path.join(directory, "webui_token.txt");

  assert.equal(await configureWebUIToken(directory, webuiSecret), true);
  assert.equal(await readFile(filePath, "utf8"), `${webuiSecret}\n`);
  assert.equal((await stat(filePath)).mode & 0o777, 0o600);
  assert.equal(await configureWebUIToken(directory, webuiSecret), false);

  await writeFile(filePath, "old-token\n", { mode: 0o644 });
  assert.equal(await configureWebUIToken(directory, webuiSecret), true);
  assert.equal(await readFile(filePath, "utf8"), `${webuiSecret}\n`);
  assert.equal((await stat(filePath)).mode & 0o777, 0o600);
});

test("initializes the direct auth token atomically with private permissions", async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "fairy-llbot-auth-"));
  const filePath = path.join(directory, "auth_token.txt");

  assert.equal(await configureAuthToken(directory, authSecret), true);
  assert.equal(await readFile(filePath, "utf8"), `${authSecret}\n`);
  assert.equal((await stat(filePath)).mode & 0o777, 0o600);
  assert.equal(await configureAuthToken(directory, authSecret), false);

  await writeFile(filePath, "old-token\n", { mode: 0o644 });
  assert.equal(await configureAuthToken(directory, authSecret), true);
  assert.equal(await readFile(filePath, "utf8"), `${authSecret}\n`);
  assert.equal((await stat(filePath)).mode & 0o777, 0o600);
});

test("rejects missing or padded WebUI tokens without exposing them", async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "fairy-llbot-webui-invalid-"));
  for (const token of ["", ` ${webuiSecret}`]) {
    await assert.rejects(
      configureWebUIToken(directory, token),
      (error) => error instanceof ConfigError && !error.message.includes(webuiSecret),
    );
  }
});

test("rejects missing or padded auth tokens without exposing them", async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "fairy-llbot-auth-invalid-"));
  for (const token of ["", ` ${authSecret}`]) {
    await assert.rejects(
      configureAuthToken(directory, token),
      (error) => error instanceof ConfigError && !error.message.includes(authSecret),
    );
  }
});

test("leaves invalid or unsupported account files unchanged with redacted errors", async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "fairy-llonebot-invalid-"));
  const cases = [
    ["config_10001.json", `{\"token\":\"${secret}\"`],
    ["config_10002.json", `${JSON.stringify({ ob11: { connect: [] }, marker: secret })}\n`],
  ];
  for (const [name, content] of cases) {
    const filePath = path.join(directory, name);
    await writeFile(filePath, content);
    await assert.rejects(
      configureFile(filePath, secret),
      (error) => error instanceof ConfigError && !error.message.includes(secret),
    );
    assert.equal(await readFile(filePath, "utf8"), content);
  }
});

test("scans only account config names and reports bounded results", async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "fairy-llonebot-scan-"));
  await writeFile(path.join(directory, "config_20001.json"), `${JSON.stringify(fixture(), null, 2)}\n`);
  await writeFile(path.join(directory, "default_config.json"), `${JSON.stringify(fixture(), null, 2)}\n`);
  await writeFile(path.join(directory, ".config_20001.json.tmp"), "ignored");
  const results = [];

  assert.equal(await configureAvailableFiles(directory, secret, (result) => results.push(result)), 1);
  assert.deepEqual(results, [{ name: "config_20001.json", changed: true, code: "ok" }]);
  assert.equal(JSON.parse(await readFile(path.join(directory, "default_config.json"), "utf8")).ob11.enable, false);
});
