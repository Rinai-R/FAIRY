use std::path::Path;

use fairy_domain::{
    CharacterBriefInput, CharacterId, CharacterSnapshot, ConversationBootstrap, ErrorCode,
    FairyError, Revision, VerifiedVisualPack, VisualPackId,
};
use fairy_storage::{CharacterAppearanceBinding, CharacterAppearanceRead, CharacterDiagnostic};
use serde::Serialize;
use tauri::{AppHandle, Runtime, State};

use crate::{
    app_error::AppError,
    app_state::AppState,
    character_package::{
        export_character_package as export_package, import_character_package as import_package,
    },
    ipc::{ConfigurationChange, emit_configuration_change},
};

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(
    tag = "status",
    rename_all = "snake_case",
    rename_all_fields = "camelCase"
)]
pub enum CharacterAppearanceDto {
    Assigned {
        binding_revision: Revision,
        visual: Box<VerifiedVisualPack>,
    },
    Unassigned,
    Unavailable,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CharacterDto {
    pub character_id: CharacterId,
    pub revision: Revision,
    pub name: String,
    pub description: String,
    pub dialogue_style: Option<String>,
    pub appearance: CharacterAppearanceDto,
}

impl CharacterDto {
    fn new(snapshot: CharacterSnapshot, appearance: CharacterAppearanceDto) -> Self {
        Self {
            character_id: snapshot.character_id(),
            revision: snapshot.revision(),
            name: snapshot.identity().name.clone(),
            description: snapshot.identity().description.clone(),
            dialogue_style: snapshot.identity().dialogue_style.clone(),
            appearance,
        }
    }
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CharacterDiagnosticDto {
    pub character_id: Option<CharacterId>,
    pub revision: Option<u64>,
    pub code: ErrorCode,
    pub message: String,
}

impl From<CharacterDiagnostic> for CharacterDiagnosticDto {
    fn from(diagnostic: CharacterDiagnostic) -> Self {
        Self {
            character_id: diagnostic.character_id,
            revision: diagnostic.revision,
            code: diagnostic.code,
            message: diagnostic.message,
        }
    }
}

impl CharacterDiagnosticDto {
    fn appearance(snapshot: &CharacterSnapshot, error: FairyError) -> Self {
        Self {
            character_id: Some(snapshot.character_id()),
            revision: Some(snapshot.revision().get()),
            code: error.code,
            message: error.message,
        }
    }
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CharacterCatalogDto {
    pub characters: Vec<CharacterDto>,
    pub active: Option<CharacterDto>,
    pub diagnostics: Vec<CharacterDiagnosticDto>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct VisualPackCatalogDto {
    pub visual_packs: Vec<VerifiedVisualPack>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CharacterActivationDto {
    pub character: CharacterDto,
    pub session: ConversationBootstrap,
}

#[tauri::command]
pub fn import_character_package<R: Runtime>(
    app: AppHandle<R>,
    state: State<'_, AppState>,
    package_path: String,
) -> Result<CharacterDto, AppError> {
    let imported = import_package(state.visual_packs.root(), Path::new(&package_path))
        .map_err(AppError::from)?;
    let imported_visual_pack_id = imported.visual_pack_id.clone();

    if let Some(snapshot) = find_character_bound_to_visual_pack(&state, &imported_visual_pack_id)? {
        let binding = require_assigned_appearance(&state, snapshot.character_id())?;
        emit_configuration_change(
            &app,
            ConfigurationChange::Character {
                revision: snapshot.revision(),
            },
        )?;
        return Ok(CharacterDto::new(
            snapshot,
            assigned_appearance(binding, imported.visual),
        ));
    }

    let character_id = CharacterId::new();
    let binding = match state
        .character_appearances
        .assign(character_id, imported.visual_pack_id)
    {
        Ok(binding) => binding,
        Err(error) => {
            let _ = state.visual_packs.remove(&imported_visual_pack_id);
            return Err(AppError::from(error));
        }
    };
    let snapshot = match state
        .characters
        .create_with_id(character_id, imported.brief)
    {
        Ok(snapshot) => snapshot,
        Err(error) => {
            let _ = state.character_appearances.clear(character_id);
            let _ = state.visual_packs.remove(&imported_visual_pack_id);
            return Err(AppError::from(error));
        }
    };
    emit_configuration_change(
        &app,
        ConfigurationChange::Character {
            revision: snapshot.revision(),
        },
    )?;
    Ok(CharacterDto::new(
        snapshot,
        assigned_appearance(binding, imported.visual),
    ))
}

#[tauri::command]
pub fn export_character_package(
    state: State<'_, AppState>,
    character_id: CharacterId,
    output_path: String,
) -> Result<(), AppError> {
    let snapshot = state
        .characters
        .list()
        .map_err(AppError::from)?
        .characters
        .into_iter()
        .find(|snapshot| snapshot.character_id() == character_id)
        .ok_or_else(|| AppError::from(character_not_available()))?;
    let binding = require_assigned_appearance(&state, character_id)?;
    let visual = state
        .visual_packs
        .get(&binding.visual_pack_id)
        .map_err(AppError::from)?;
    export_package(
        state.visual_packs.root(),
        &snapshot,
        &visual,
        Path::new(&output_path),
    )
    .map_err(AppError::from)
}

#[tauri::command]
pub fn create_character<R: Runtime>(
    app: AppHandle<R>,
    state: State<'_, AppState>,
    brief: CharacterBriefInput,
    visual_pack_id: VisualPackId,
) -> Result<CharacterDto, AppError> {
    let visual = state
        .visual_packs
        .get(&visual_pack_id)
        .map_err(AppError::from)?
        .clone();
    let character_id = CharacterId::new();
    let binding = state
        .character_appearances
        .assign(character_id, visual_pack_id)
        .map_err(AppError::from)?;
    let snapshot = match state.characters.create_with_id(character_id, brief) {
        Ok(snapshot) => snapshot,
        Err(error) => {
            state
                .character_appearances
                .clear(character_id)
                .map_err(AppError::from)?;
            return Err(AppError::from(error));
        }
    };
    emit_configuration_change(
        &app,
        ConfigurationChange::Character {
            revision: snapshot.revision(),
        },
    )?;
    Ok(CharacterDto::new(
        snapshot,
        assigned_appearance(binding, visual),
    ))
}

#[tauri::command]
pub fn update_character<R: Runtime>(
    app: AppHandle<R>,
    state: State<'_, AppState>,
    character_id: CharacterId,
    brief: CharacterBriefInput,
) -> Result<CharacterDto, AppError> {
    let snapshot = state
        .characters
        .update(character_id, brief)
        .map_err(AppError::from)?;
    let (appearance, _) = resolve_appearance(&state, &snapshot);
    emit_configuration_change(
        &app,
        ConfigurationChange::Character {
            revision: snapshot.revision(),
        },
    )?;
    Ok(CharacterDto::new(snapshot, appearance))
}

#[tauri::command]
pub fn list_characters(state: State<'_, AppState>) -> Result<CharacterCatalogDto, AppError> {
    build_character_catalog(&state)
}

#[tauri::command]
pub fn list_visual_packs(state: State<'_, AppState>) -> Result<VisualPackCatalogDto, AppError> {
    Ok(VisualPackCatalogDto {
        visual_packs: state.visual_packs.list().map_err(AppError::from)?,
    })
}

#[tauri::command]
pub fn set_character_appearance<R: Runtime>(
    app: AppHandle<R>,
    state: State<'_, AppState>,
    character_id: CharacterId,
    visual_pack_id: VisualPackId,
) -> Result<CharacterDto, AppError> {
    let visual = state
        .visual_packs
        .get(&visual_pack_id)
        .map_err(AppError::from)?
        .clone();
    let snapshot = state
        .characters
        .list()
        .map_err(AppError::from)?
        .characters
        .into_iter()
        .find(|snapshot| snapshot.character_id() == character_id)
        .ok_or_else(|| AppError::from(character_not_available()))?;
    let binding = state
        .character_appearances
        .assign(character_id, visual_pack_id)
        .map_err(AppError::from)?;
    emit_configuration_change(
        &app,
        ConfigurationChange::Character {
            revision: snapshot.revision(),
        },
    )?;
    Ok(CharacterDto::new(
        snapshot,
        assigned_appearance(binding, visual),
    ))
}

#[tauri::command]
pub async fn activate_character<R: Runtime>(
    app: AppHandle<R>,
    state: State<'_, AppState>,
    character_id: CharacterId,
    revision: Revision,
) -> Result<CharacterActivationDto, AppError> {
    let binding = require_assigned_appearance(&state, character_id)?;
    let visual = state
        .visual_packs
        .get(&binding.visual_pack_id)
        .map_err(AppError::from)?
        .clone();
    let runtime = state.runtime()?;
    let profile = state.user_profiles.current().map_err(AppError::from)?;
    let previous = state.characters.active().map_err(AppError::from)?;
    let snapshot = state
        .characters
        .activate(character_id, revision)
        .map_err(AppError::from)?;

    let session = match runtime
        .open_or_create_character_session(snapshot.clone(), profile)
        .await
    {
        Ok(session) => session,
        Err(error) => {
            rollback_active_character(&state, previous)?;
            return Err(AppError::from(error));
        }
    };
    emit_configuration_change(
        &app,
        ConfigurationChange::Character {
            revision: snapshot.revision(),
        },
    )?;
    Ok(CharacterActivationDto {
        character: CharacterDto::new(snapshot, assigned_appearance(binding, visual)),
        session,
    })
}

fn build_character_catalog(state: &AppState) -> Result<CharacterCatalogDto, AppError> {
    let catalog = state.characters.list().map_err(AppError::from)?;
    let active_snapshot = state.characters.active().map_err(AppError::from)?;
    let mut diagnostics: Vec<CharacterDiagnosticDto> = catalog
        .diagnostics
        .into_iter()
        .map(CharacterDiagnosticDto::from)
        .collect();
    let characters = catalog
        .characters
        .into_iter()
        .map(|snapshot| {
            let (appearance, diagnostic) = resolve_appearance(state, &snapshot);
            if let Some(diagnostic) = diagnostic {
                diagnostics.push(diagnostic);
            }
            CharacterDto::new(snapshot, appearance)
        })
        .collect();
    let active = active_snapshot.map(|snapshot| {
        let (appearance, _) = resolve_appearance(state, &snapshot);
        CharacterDto::new(snapshot, appearance)
    });
    Ok(CharacterCatalogDto {
        characters,
        active,
        diagnostics,
    })
}

fn find_character_bound_to_visual_pack(
    state: &AppState,
    visual_pack_id: &VisualPackId,
) -> Result<Option<CharacterSnapshot>, AppError> {
    let active_character_id = state
        .characters
        .active()
        .map_err(AppError::from)?
        .map(|snapshot| snapshot.character_id());
    let catalog = state.characters.list().map_err(AppError::from)?;
    let mut first_match = None;

    for snapshot in catalog.characters {
        let Ok(CharacterAppearanceRead::Assigned(binding)) =
            state.character_appearances.read(snapshot.character_id())
        else {
            continue;
        };
        if &binding.visual_pack_id != visual_pack_id {
            continue;
        }
        if Some(snapshot.character_id()) == active_character_id {
            return Ok(Some(snapshot));
        }
        if first_match.is_none() {
            first_match = Some(snapshot);
        }
    }

    Ok(first_match)
}

fn resolve_appearance(
    state: &AppState,
    snapshot: &CharacterSnapshot,
) -> (CharacterAppearanceDto, Option<CharacterDiagnosticDto>) {
    match state.character_appearances.read(snapshot.character_id()) {
        Ok(CharacterAppearanceRead::Unassigned) => {
            let error = appearance_unassigned();
            (
                CharacterAppearanceDto::Unassigned,
                Some(CharacterDiagnosticDto::appearance(snapshot, error)),
            )
        }
        Ok(CharacterAppearanceRead::Assigned(binding)) => {
            match state.visual_packs.get(&binding.visual_pack_id) {
                Ok(visual) => (assigned_appearance(binding, visual), None),
                Err(error) => (
                    CharacterAppearanceDto::Unavailable,
                    Some(CharacterDiagnosticDto::appearance(snapshot, error)),
                ),
            }
        }
        Err(error) => (
            CharacterAppearanceDto::Unavailable,
            Some(CharacterDiagnosticDto::appearance(snapshot, error)),
        ),
    }
}

fn assigned_appearance(
    binding: CharacterAppearanceBinding,
    visual: VerifiedVisualPack,
) -> CharacterAppearanceDto {
    CharacterAppearanceDto::Assigned {
        binding_revision: binding.revision,
        visual: Box::new(visual),
    }
}

fn require_assigned_appearance(
    state: &AppState,
    character_id: CharacterId,
) -> Result<CharacterAppearanceBinding, AppError> {
    match state
        .character_appearances
        .read(character_id)
        .map_err(AppError::from)?
    {
        CharacterAppearanceRead::Assigned(binding) => Ok(binding),
        CharacterAppearanceRead::Unassigned => Err(AppError::from(appearance_unassigned())),
    }
}

fn appearance_unassigned() -> FairyError {
    FairyError::new(
        ErrorCode::CharacterAppearanceUnassigned,
        "该角色尚未选择外观",
        false,
    )
}

fn character_not_available() -> FairyError {
    FairyError::new(ErrorCode::CharacterNotAvailable, "找不到指定角色", false)
}

fn rollback_active_character(
    state: &AppState,
    previous: Option<CharacterSnapshot>,
) -> Result<(), AppError> {
    if let Some(previous) = previous {
        state
            .characters
            .activate(previous.character_id(), previous.revision())
            .map(|_| ())
            .map_err(AppError::from)
    } else {
        state
            .characters
            .clear_active()
            .map(|_| ())
            .map_err(AppError::from)
    }
}

#[cfg(test)]
mod tests {
    use std::str::FromStr;

    use fairy_domain::CharacterBriefInput;

    use crate::visual_registry::write_test_visual_pack;

    use super::*;

    fn brief(name: &str) -> CharacterBriefInput {
        CharacterBriefInput {
            name: name.to_owned(),
            description: format!("{name} 会自然回应用户。"),
            dialogue_style: None,
        }
    }

    #[test]
    fn legacy_character_remains_listed_with_unassigned_diagnostic() {
        let directory = tempfile::tempdir().expect("temp directory");
        let state = AppState::initialize(directory.path()).expect("app state");
        let snapshot = state.characters.create(brief("旧角色")).expect("character");

        let catalog = build_character_catalog(&state).expect("catalog");

        assert_eq!(catalog.characters.len(), 1);
        assert_eq!(catalog.characters[0].character_id, snapshot.character_id());
        assert_eq!(
            catalog.characters[0].appearance,
            CharacterAppearanceDto::Unassigned
        );
        assert_eq!(catalog.diagnostics.len(), 1);
        assert_eq!(
            catalog.diagnostics[0].code,
            ErrorCode::CharacterAppearanceUnassigned
        );
    }

    #[test]
    fn assigned_visual_is_public_and_does_not_change_character_revision() {
        let directory = tempfile::tempdir().expect("temp directory");
        let state = AppState::initialize(directory.path()).expect("app state");
        write_test_visual_pack(
            state
                .visual_packs
                .root()
                .parent()
                .expect("visual root parent"),
            "fairy.atri",
        );
        let snapshot = state.characters.create(brief("亚托莉")).expect("character");
        let binding = state
            .character_appearances
            .assign(
                snapshot.character_id(),
                VisualPackId::from_str("fairy.atri").expect("pack id"),
            )
            .expect("assign appearance");
        let (appearance, diagnostic) = resolve_appearance(&state, &snapshot);

        assert!(diagnostic.is_none());
        assert_eq!(snapshot.revision(), Revision::INITIAL);
        assert!(matches!(
            appearance,
            CharacterAppearanceDto::Assigned {
                binding_revision,
                ref visual,
            } if binding_revision == binding.revision && visual.pack_id().as_str() == "fairy.atri"
        ));
        let json = serde_json::to_string(&appearance).expect("serialize appearance");
        assert!(!json.contains("\"script\""));
        assert!(json.contains("fairy-character://localhost/fairy.atri/idle.png"));
    }

    #[test]
    fn corrupt_visual_pack_manifest_is_not_reported_as_an_empty_catalog() {
        let directory = tempfile::tempdir().expect("temp directory");
        let state = AppState::initialize(directory.path()).expect("app state");
        let root = state.visual_packs.root().join("fairy.bad");
        std::fs::create_dir_all(&root).expect("create corrupt visual pack");
        std::fs::write(root.join("manifest.json"), "{\"schemaVersion\":2}")
            .expect("write corrupt visual manifest");

        let error = state
            .visual_packs
            .list()
            .expect_err("corrupt visual pack should surface");

        assert_eq!(AppError::from(error).code, "INVALID_VISUAL_MANIFEST");
    }

    #[test]
    fn unassigned_character_cannot_enter_activation_preflight() {
        let directory = tempfile::tempdir().expect("temp directory");
        let state = AppState::initialize(directory.path()).expect("app state");
        let snapshot = state.characters.create(brief("未绑定")).expect("character");

        let error = require_assigned_appearance(&state, snapshot.character_id())
            .expect_err("unassigned must fail");
        assert_eq!(error.code, "CHARACTER_APPEARANCE_UNASSIGNED");
        assert!(state.characters.active().expect("active state").is_none());
    }

    #[test]
    fn import_reuses_the_active_character_already_bound_to_the_pack_id() {
        let directory = tempfile::tempdir().expect("temp directory");
        let state = AppState::initialize(directory.path()).expect("app state");
        let visual_pack_id = VisualPackId::from_str("fairy.atri").expect("pack id");
        let first = state.characters.create(brief("亚托莉")).expect("first");
        let second = state.characters.create(brief("亚托莉")).expect("second");
        state
            .character_appearances
            .assign(first.character_id(), visual_pack_id.clone())
            .expect("assign first");
        state
            .character_appearances
            .assign(second.character_id(), visual_pack_id.clone())
            .expect("assign second");
        state
            .characters
            .activate(first.character_id(), first.revision())
            .expect("activate first");

        let reused = find_character_bound_to_visual_pack(&state, &visual_pack_id)
            .expect("find bound character")
            .expect("bound character");

        assert_eq!(reused.character_id(), first.character_id());
    }
}
