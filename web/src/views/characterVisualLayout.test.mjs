import assert from "node:assert/strict";
import { characterVisualStyle, resolveCharacterPortrait } from "./characterVisualLayout.js";

{
  const portrait = resolveCharacterPortrait({
    avatar_url: "/fallback.png",
    assets: {
      portrait_url: "/base.png",
      visual_layout: {
        preset: "full_body",
        stage: { left: "70%", height: "82vh" },
        home: { height: "70%" }
      },
      moods: {
        angry: {
          portrait_url: "/angry.png",
          visual_layout: {
            stage: { left: "66%" }
          }
        }
      }
    }
  }, "angry", "calm");

  assert.equal(portrait.url, "/angry.png");

  const stageStyle = characterVisualStyle(portrait.layout, "stage");
  assert.equal(stageStyle["--character-stage-left"], "66%");
  assert.equal(stageStyle["--character-stage-height"], "82vh");
  assert.equal(stageStyle["--character-stage-top"], "7vh");

  const homeStyle = characterVisualStyle(portrait.layout, "home");
  assert.equal(homeStyle["--character-home-height"], "70%");
  assert.equal(homeStyle["--character-home-bottom"], "128px");
}

{
  const portrait = resolveCharacterPortrait({
    avatar_url: "/fallback.png",
    assets: {
      visual_layout: "bust",
      moods: {}
    }
  }, "missing", "calm");

  assert.equal(portrait.url, "/fallback.png");

  const directorStyle = characterVisualStyle(portrait.layout, "director");
  assert.equal(directorStyle["--character-director-height"], "58%");
  assert.equal(directorStyle["--character-director-top"], "108px");
}

console.log("characterVisualLayout tests passed");
