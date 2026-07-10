import test from "node:test";
import assert from "node:assert/strict";

import {
  DEFAULT_CHARACTER,
  describeCharacterFailure,
  isPackagedAssetPath,
  validateCharacter,
} from "./defaultCharacter.mjs";

test("the default character uses an offline packaged asset", () => {
  assert.equal(DEFAULT_CHARACTER.id, "atri.official-prototype");
  assert.equal(DEFAULT_CHARACTER.renderer, "static-png");
  assert.equal(isPackagedAssetPath(DEFAULT_CHARACTER.assetPath), true);
  assert.equal(DEFAULT_CHARACTER.assetPath.startsWith("http"), false);
});

test("remote character assets are rejected at the boundary", () => {
  assert.throws(
    () =>
      validateCharacter({
        id: "remote.character",
        displayName: "Remote",
        renderer: "static-png",
        assetPath: "https://example.com/character.svg",
      }),
    /packaged local path/,
  );
});

test("a missing character asset has an explicit visible error", () => {
  const error = describeCharacterFailure(DEFAULT_CHARACTER);

  assert.equal(error.code, "CHARACTER_ASSET_FAILED");
  assert.match(error.message, /ATRI/);
});
