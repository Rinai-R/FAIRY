import assert from "node:assert/strict";
import {
  CHARACTER_PACKAGE_TYPE,
  createCharacterPackage,
  mergeCharacterPackage,
  parseCharacterPackage,
} from "./characterPackage.js";

const character = {
  id: "atri",
  display_name: "亚托莉",
  voice_id: "voice",
  runtime: {
    agent: {
      api_key: "sk-secret",
      endpoint: "https://example.com",
    },
    voice: {
      extra: {
        access_token: "token-value",
        speaker: "S_atri",
      },
    },
  },
  assets: {
    portrait_url: "/atri.png",
  },
};

{
  const pack = createCharacterPackage([character], { exportedAt: "2026-06-18T00:00:00Z" });
  assert.equal(pack.type, CHARACTER_PACKAGE_TYPE);
  assert.equal(pack.characters[0].runtime.agent.api_key, "");
  assert.equal(pack.characters[0].runtime.voice.extra.access_token, "");
  assert.equal(pack.characters[0].runtime.agent.endpoint, "https://example.com");
  assert.equal(pack.characters[0].assets.portrait_url, "/atri.png");
}

{
  const pack = createCharacterPackage([character], { redactSensitive: false });
  assert.equal(pack.characters[0].runtime.agent.api_key, "sk-secret");
  assert.equal(pack.characters[0].runtime.voice.extra.access_token, "token-value");
}

{
  const merged = mergeCharacterPackage(
    [{ id: "tutor", display_name: "旧角色", assets: {}, runtime: {} }],
    { characters: [character] },
  );
  assert.equal(merged.importedCount, 1);
  assert.equal(merged.firstCharacterID, "atri");
  assert.equal(merged.characters.length, 2);
  assert.equal(merged.characters[1].display_name, "亚托莉");
}

{
  const merged = mergeCharacterPackage(
    [{ id: "atri", display_name: "旧名", assets: {}, runtime: {} }],
    { characters: [character] },
  );
  assert.equal(merged.characters.length, 1);
  assert.equal(merged.characters[0].display_name, "亚托莉");
}

assert.throws(
  () => parseCharacterPackage("{ broken"),
  /JSON 无法解析/,
);

assert.throws(
  () => parseCharacterPackage({ type: "other.pack", characters: [character] }),
  /不是 FAIRY 角色包/,
);

assert.throws(
  () => parseCharacterPackage({ characters: [] }),
  /缺少 characters 数组/,
);

console.log("characterPackage tests passed");
