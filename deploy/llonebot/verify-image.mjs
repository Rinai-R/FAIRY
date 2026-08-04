import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFile } from "node:fs/promises";

const contract = JSON.parse(await readFile(new URL("./image-contract.json", import.meta.url), "utf8"));
const image = `${contract.repository}@${contract.manifestDigest}`;

function inspectManifest() {
  const output = execFileSync("docker", ["buildx", "imagetools", "inspect", image, "--raw"], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  });
  const index = JSON.parse(output);
  assert.ok(Array.isArray(index.manifests), "registry response is not a multi-platform manifest");

  const actual = new Map(
    index.manifests.map((entry) => [`${entry.platform?.os}/${entry.platform?.architecture}`, entry.digest]),
  );
  for (const [platform, expectedDigest] of Object.entries(contract.platforms)) {
    assert.equal(actual.get(platform), expectedDigest, `unexpected ${platform} manifest digest`);
  }
}

async function inspectDefaultConfig() {
  const response = await fetch(contract.upstream.llonebot.defaultConfigUrl, {
    signal: AbortSignal.timeout(15_000),
  });
  assert.equal(response.ok, true, `default config request failed with HTTP ${response.status}`);
  const document = await response.json();
  assert.ok(Array.isArray(document?.ob11?.connect), "upstream default config has no ob11.connect array");

  for (const type of ["http", "http-post"]) {
    const matches = document.ob11.connect.filter((connection) => connection?.type === type);
    assert.equal(matches.length, 1, `upstream default config must contain exactly one ${type} connection`);
  }
}

try {
  inspectManifest();
  await inspectDefaultConfig();
  console.log(
    `verified ${image}: ${Object.keys(contract.platforms).join(", ")}; ` +
      `LLOneBot ${contract.upstream.llonebot.version}, PMHQ ${contract.upstream.pmhqVersion}, QQ ${contract.upstream.qqVersion}`,
  );
} catch (error) {
  console.error(`llonebot artifact verification failed: ${error instanceof Error ? error.message : "unknown error"}`);
  process.exitCode = 1;
}
