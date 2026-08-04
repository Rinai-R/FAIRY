import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import test from "node:test";

const repositoryRoot = new URL("../../", import.meta.url);
const contract = JSON.parse(await readFile(new URL("./image-contract.json", import.meta.url), "utf8"));
const expectedDigest = "sha256:da9e3a2b3e23daa2d9f5b48c5963a328dcc8471e8143c2a22f8e759dc26cb590";
const placeholder = "contract-test-placeholder";

function composeConfig() {
  const output = execFileSync(
    "docker",
    [
      "compose",
      "--env-file",
      "/dev/null",
      "-f",
      "docker-compose.yml",
      "-f",
      "docker-compose.qq.yml",
      "config",
      "--format",
      "json",
    ],
    {
      cwd: repositoryRoot,
      encoding: "utf8",
      env: {
        ...process.env,
        FAIRY_API_TOKEN: placeholder,
        FAIRY_ONEBOT_TOKEN: placeholder,
        FAIRY_POSTGRES_PASSWORD: placeholder,
        FAIRY_SECRET_MASTER_KEY: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
        LLONEBOT_AUTH_TOKEN: placeholder,
        LLONEBOT_QUICK_LOGIN_QQ: "",
      },
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  return JSON.parse(output);
}

function publishedPorts(service) {
  return (service.ports ?? []).map(({ host_ip, published, target }) => ({
    hostIP: host_ip,
    published: Number(published),
    target,
  }));
}

function volumeTargets(service) {
  return (service.volumes ?? []).map(({ source, target, type }) => ({ source, target, type }));
}

test("locks the reviewed LLOneBot artifact metadata", () => {
  assert.equal(contract.schemaVersion, 1);
  assert.equal(contract.repository, "initialencounter/llonebot");
  assert.equal(contract.manifestDigest, expectedDigest);
  assert.match(contract.manifestDigest, /^sha256:[a-f0-9]{64}$/);
  assert.deepEqual(Object.keys(contract.platforms).sort(), ["linux/amd64", "linux/arm64"]);
  for (const digest of Object.values(contract.platforms)) {
    assert.match(digest, /^sha256:[a-f0-9]{64}$/);
  }
  assert.deepEqual(
    {
      llonebot: contract.upstream.llonebot.version,
      pmhq: contract.upstream.pmhqVersion,
      qq: contract.upstream.qqVersion,
    },
    { llonebot: "8.1.5", pmhq: "8.1.1", qq: "3.2.31-260710" },
  );
  assert.match(contract.upstream.llonebot.revision, /^[a-f0-9]{40}$/);
  assert.match(contract.upstream.llonebotNix.revision, /^[a-f0-9]{40}$/);
});

test("keeps the QQ Compose image, topology, and secret boundaries explicit", () => {
  const config = composeConfig();
  const llonebot = config.services.llonebot;
  const sidecar = config.services["llonebot-config"];
  const surface = config.services["qq-onebot"];

  assert.equal(llonebot.image, `${contract.repository}@${contract.manifestDigest}`);
  assert.ok(!llonebot.image.includes(":latest"));
  assert.deepEqual(publishedPorts(llonebot), [{ hostIP: "127.0.0.1", published: 3080, target: 3080 }]);
  assert.deepEqual(llonebot.expose, ["3000"]);
  assert.deepEqual(volumeTargets(llonebot), [
    { source: "llonebot-qq-login", target: "/root/.config/QQ", type: "volume" },
    { source: "llonebot-data", target: "/root/llonebot", type: "volume" },
  ]);
  assert.deepEqual(Object.keys(llonebot.environment).sort(), ["AUTH_TOKEN", "QUICK_LOGIN_QQ"]);
  assert.equal(llonebot.environment.AUTH_TOKEN, placeholder);

  assert.deepEqual(publishedPorts(surface), []);
  assert.deepEqual(surface.expose, ["3002"]);
  assert.equal(surface.environment.FAIRY_CORE_TOKEN, placeholder);
  assert.equal(surface.environment.FAIRY_ONEBOT_TOKEN, placeholder);
  assert.equal(surface.environment.FAIRY_ONEBOT_GROUP_ALLOWLIST, undefined);
  assert.equal(sidecar.environment.FAIRY_ONEBOT_TOKEN, placeholder);
  assert.equal(sidecar.environment.AUTH_TOKEN, undefined);
});
