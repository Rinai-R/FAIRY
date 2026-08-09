import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import test from "node:test";

const repositoryRoot = new URL("../../", import.meta.url);
const contract = JSON.parse(await readFile(new URL("./image-contract.json", import.meta.url), "utf8"));
const placeholder = "contract-test-placeholder";
const requiredEnvironment = {
  FAIRY_API_TOKEN: placeholder,
  FAIRY_ONEBOT_TOKEN: placeholder,
  FAIRY_POSTGRES_PASSWORD: placeholder,
  FAIRY_SECRET_MASTER_KEY: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
  LLBOT_AUTH_TOKEN: placeholder,
  LLBOT_WEBUI_TOKEN: placeholder,
};

function composeArguments() {
  return [
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
  ];
}

function composeConfig() {
  const output = execFileSync("docker", composeArguments(), {
    cwd: repositoryRoot,
    encoding: "utf8",
    env: { ...process.env, ...requiredEnvironment },
    stdio: ["ignore", "pipe", "pipe"],
  });
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

function hasDockerSocket(service) {
  return (service.volumes ?? []).some(({ source, target }) =>
    [source, target].some((value) => typeof value === "string" && value.includes("docker.sock")),
  );
}

test("locks the direct LLBot image and reviewed multi-platform manifest", () => {
  assert.equal(contract.schemaVersion, 3);
  assert.deepEqual(Object.keys(contract.images), ["llbot"]);
  assert.deepEqual(
    Object.fromEntries(Object.entries(contract.images).map(([name, image]) => [name, `${image.repository}:${image.tag}`])),
    { llbot: "linyuchen/llbot:8.1.5" },
  );
  for (const image of Object.values(contract.images)) {
    assert.match(image.manifestDigest, /^sha256:[a-f0-9]{64}$/);
    assert.deepEqual(Object.keys(image.platforms).sort(), ["linux/amd64", "linux/arm64"]);
    for (const digest of Object.values(image.platforms)) assert.match(digest, /^sha256:[a-f0-9]{64}$/);
  }
  assert.equal(contract.upstream.repository, "LLOneBot/LuckyLilliaBot");
  assert.match(contract.upstream.revision, /^[a-f0-9]{40}$/);
  assert.ok(contract.upstream.installerUrl.includes(contract.upstream.revision));
  assert.ok(contract.upstream.defaultConfigUrl.includes(contract.upstream.revision));
});

test("keeps the direct topology, readiness, ports, credentials, and volume isolated", () => {
  const config = composeConfig();
  const llbot = config.services.llbot;
  const sidecar = config.services["llbot-config"];
  const surface = config.services["qq-onebot"];

  assert.equal(config.services.pmhq, undefined);
  assert.equal(
    llbot.image,
    `${contract.images.llbot.repository}:${contract.images.llbot.tag}@${contract.images.llbot.manifestDigest}`,
  );
  assert.ok(!llbot.image.includes(":latest"));

  assert.equal(llbot.privileged, undefined);
  assert.equal(sidecar.privileged, undefined);
  assert.equal(surface.privileged, undefined);
  assert.deepEqual(publishedPorts(llbot), [{ hostIP: "127.0.0.1", published: 3080, target: 3080 }]);
  assert.deepEqual(publishedPorts(surface), []);
  assert.deepEqual(llbot.expose, ["3000"]);
  assert.deepEqual(surface.expose, ["3002"]);

  assert.deepEqual(volumeTargets(llbot), [
    { source: "llbot-data", target: "/app/llbot/data", type: "volume" },
  ]);
  assert.deepEqual(volumeTargets(sidecar).find(({ type }) => type === "volume"), {
    source: "llbot-data",
    target: "/app/llbot/data",
    type: "volume",
  });
  assert.deepEqual(volumeTargets(surface), []);

  assert.deepEqual(Object.keys(llbot.environment), ["WEBUI_PORT"]);
  assert.deepEqual(Object.keys(sidecar.environment).sort(), ["FAIRY_ONEBOT_TOKEN", "LLBOT_AUTH_TOKEN", "LLBOT_DATA_DIR", "LLBOT_WEBUI_TOKEN"]);
  assert.equal(sidecar.environment.LLBOT_AUTH_TOKEN, placeholder);
  assert.equal(sidecar.environment.FAIRY_ONEBOT_TOKEN, placeholder);
  assert.equal(sidecar.environment.LLBOT_WEBUI_TOKEN, placeholder);
  assert.equal(surface.environment.FAIRY_CORE_TOKEN, placeholder);
  assert.equal(surface.environment.FAIRY_ONEBOT_TOKEN, placeholder);
  assert.equal(surface.environment.FAIRY_ONEBOT_API_ENDPOINT, "http://llbot:3000");
  assert.equal(surface.environment.FAIRY_ONEBOT_GROUP_ALLOWLIST, undefined);
  assert.deepEqual(surface.healthcheck.test, ["CMD", "/usr/local/bin/fairy-qq-onebot", "healthcheck"]);
  assert.equal(surface.healthcheck.interval, "15s");
  assert.equal(surface.healthcheck.timeout, "15s");
  assert.equal(surface.healthcheck.retries, 4);
  assert.equal(surface.healthcheck.start_period, "20s");

  assert.equal(llbot.depends_on["llbot-config"].condition, "service_healthy");
  assert.equal(surface.depends_on.llbot.condition, "service_healthy");
  assert.ok(Object.hasOwn(config.volumes, "llbot-data"));
  assert.equal(Object.hasOwn(config.volumes, "pmhq-qq-login"), false);
  for (const service of Object.values(config.services)) {
    assert.equal(service.privileged, undefined);
    assert.equal(hasDockerSocket(service), false);
  }
});

test("fails Compose interpolation when any required QQ credential is absent", () => {
  for (const variable of ["LLBOT_AUTH_TOKEN", "LLBOT_WEBUI_TOKEN", "FAIRY_ONEBOT_TOKEN", "FAIRY_API_TOKEN"]) {
    const env = { ...process.env, ...requiredEnvironment };
    delete env[variable];
    const result = spawnSync("docker", composeArguments(), {
      cwd: repositoryRoot,
      encoding: "utf8",
      env,
      stdio: ["ignore", "pipe", "pipe"],
    });
    assert.notEqual(result.status, 0, `${variable} unexpectedly accepted as missing`);
    assert.match(`${result.stdout}${result.stderr}`, new RegExp(variable));
  }
});

test("current deployment files contain no retired PMHQ or legacy image references", async () => {
  const files = [
    "docker-compose.qq.yml",
    ".env.example",
    "README.md",
    "surfaces/qq-onebot/README.md",
    "deploy/llonebot/configure.mjs",
    "deploy/llonebot/image-contract.json",
  ];
  const contents = await Promise.all(files.map((file) => readFile(new URL(`../../${file}`, import.meta.url), "utf8")));
  for (const content of contents) {
    assert.doesNotMatch(content, /initialencounter\/llonebot|LLONEBOT_AUTH_TOKEN|LLONEBOT_QUICK_LOGIN_QQ|llonebot-data|llonebot-qq-login|\/root\/llonebot|PMHQ_AUTH_TOKEN|PMHQ_AUTO_LOGIN_QQ|linyuchen\/pmhq|pmhq-qq-login|PROTOCOL_MODE:\s*pmhq|PMHQ_HOST|privileged:\s*true/);
  }
});
