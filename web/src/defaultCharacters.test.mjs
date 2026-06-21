import assert from "node:assert/strict";
import { ATRI_DEFAULT_PORTRAIT_URL, defaultCharacters } from "./defaultCharacters.js";
import { characterVisualStyle, resolveCharacterPortrait } from "./views/characterVisualLayout.js";

const tutor = defaultCharacters.find((character) => character.id === "tutor");
assert.ok(tutor, "默认角色应包含主讲角色");
assert.equal(tutor.display_name, "亚托莉");
assert.equal(tutor.assets.portrait_url, ATRI_DEFAULT_PORTRAIT_URL);
assert.equal(tutor.assets.reference_image_url, ATRI_DEFAULT_PORTRAIT_URL);

for (const mood of ["soft_smile", "calm", "curious", "thinking", "serious"]) {
  assert.ok(tutor.assets.moods[mood]?.portrait_url, `默认主讲角色应包含 ${mood} 差分立绘`);
}

const softSmile = resolveCharacterPortrait(tutor, "soft_smile", "calm");
assert.equal(softSmile.url, ATRI_DEFAULT_PORTRAIT_URL, "默认主讲角色首屏应有稳定立绘");

const curious = resolveCharacterPortrait(tutor, "curious", "soft_smile");
assert.match(curious.url, /surprised_/, "好奇差分应使用正常服装的好奇或意外立绘");

const homeStyle = characterVisualStyle(softSmile.layout, "home");
assert.equal(homeStyle["--character-home-height"], "66%");
assert.equal(homeStyle["--character-home-bottom"], "92px");

const stageStyle = characterVisualStyle(softSmile.layout, "stage");
assert.equal(stageStyle["--character-stage-height"], "78vh");
assert.equal(stageStyle["--character-stage-top"], "8vh");

console.log("defaultCharacters tests passed");
