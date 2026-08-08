import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFile } from "node:fs/promises";

const contract = JSON.parse(await readFile(new URL("./image-contract.json", import.meta.url), "utf8"));

function inspectManifest(name, imageContract) {
  const image = `${imageContract.repository}:${imageContract.tag}`;
  const output = execFileSync(
    "docker",
    ["buildx", "imagetools", "inspect", image, "--format", "{{json .Manifest}}"],
    { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] },
  );
  const descriptor = JSON.parse(output);
  assert.equal(descriptor.digest, imageContract.manifestDigest, `unexpected ${name} manifest digest`);
  assert.ok(Array.isArray(descriptor.manifests), `${name} registry response is not a multi-platform manifest`);

  const actual = new Map(
    descriptor.manifests.map((entry) => [`${entry.platform?.os}/${entry.platform?.architecture}`, entry.digest]),
  );
  for (const [platform, expectedDigest] of Object.entries(imageContract.platforms)) {
    assert.equal(actual.get(platform), expectedDigest, `unexpected ${name} ${platform} manifest digest`);
  }
  return image;
}

async function fetchText(url, label) {
  const response = await fetch(url, { signal: AbortSignal.timeout(15_000) });
  assert.equal(response.ok, true, `${label} request failed with HTTP ${response.status}`);
  return response.text();
}

async function inspectUpstream() {
  const [defaultConfigText, installer] = await Promise.all([
    fetchText(contract.upstream.defaultConfigUrl, "default config"),
    fetchText(contract.upstream.installerUrl, "installer"),
  ]);
  const document = JSON.parse(defaultConfigText);
  assert.ok(Array.isArray(document?.ob11?.connect), "upstream default config has no ob11.connect array");
  for (const type of ["http", "http-post"]) {
    const matches = document.ob11.connect.filter((connection) => connection?.type === type);
    assert.equal(matches.length, 1, `upstream default config must contain exactly one ${type} connection`);
  }
  for (const expected of ["linyuchen/llbot", 'PROTOCOL_MODE="direct"', "/app/llbot/data", "auth_token.txt", "webui_token.txt"]) {
    assert.ok(installer.includes(expected), `upstream installer no longer contains ${expected}`);
  }
  assert.ok(installer.includes('if [ "$PROTOCOL_MODE" == "pmhq" ]; then'), "upstream installer lost its explicit direct/PMHQ branch");
}

try {
  const images = Object.entries(contract.images).map(([name, image]) => inspectManifest(name, image));
  await inspectUpstream();
  console.log(
    `verified official installer selections ${images.join(", ")}: linux/amd64, linux/arm64; ` +
      `source ${contract.upstream.repository}@${contract.upstream.revision}`,
  );
} catch (error) {
  console.error(`LLOneBot artifact verification failed: ${error instanceof Error ? error.message : "unknown error"}`);
  process.exitCode = 1;
}
