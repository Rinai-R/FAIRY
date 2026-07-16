/** Load generated Wails service bindings from domain packages. */
export async function loadFairyBindings() {
  const [
    desktop,
    character,
    companion,
    config,
    memory,
    model,
    profile,
    visual,
  ] = await Promise.all([
    import("../../fairy/frontend/bindings/fairy/desktop/index.js"),
    import("../../fairy/frontend/bindings/fairy/character/index.js"),
    import("../../fairy/frontend/bindings/fairy/companion/index.js"),
    import("../../fairy/frontend/bindings/fairy/config/index.js"),
    import("../../fairy/frontend/bindings/fairy/memory/index.js"),
    import("../../fairy/frontend/bindings/fairy/model/index.js"),
    import("../../fairy/frontend/bindings/fairy/profile/index.js"),
    import("../../fairy/frontend/bindings/fairy/visual/index.js"),
  ]);

  return {
    BootstrapService: desktop.BootstrapService,
    DesktopService: desktop.DesktopService,
    CharacterService: character.CharacterService,
    CompanionService: companion.CompanionService,
    ConfigService: config.ConfigService,
    MemoryService: memory.MemoryService,
    ModelService: model.ModelService,
    ProfileService: profile.ProfileService,
    VisualService: visual.VisualService,
  };
}
