use fairy_domain::{
    CharacterBriefInput, CharacterId, CharacterSnapshot, ConversationId, ErrorCode, Revision,
};
use fairy_storage::CharacterDiagnostic;
use serde::Serialize;
use tauri::{AppHandle, Runtime, State};

use crate::{
    app_error::AppError,
    app_state::AppState,
    ipc::{ConfigurationChange, emit_configuration_change},
};

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CharacterDto {
    pub character_id: CharacterId,
    pub revision: Revision,
    pub name: String,
    pub description: String,
}

impl From<CharacterSnapshot> for CharacterDto {
    fn from(snapshot: CharacterSnapshot) -> Self {
        Self {
            character_id: snapshot.character_id(),
            revision: snapshot.revision(),
            name: snapshot.identity().name.clone(),
            description: snapshot.identity().description.clone(),
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

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CharacterCatalogDto {
    pub characters: Vec<CharacterDto>,
    pub active: Option<CharacterDto>,
    pub diagnostics: Vec<CharacterDiagnosticDto>,
}

#[tauri::command]
pub fn create_character<R: Runtime>(
    app: AppHandle<R>,
    state: State<'_, AppState>,
    brief: CharacterBriefInput,
) -> Result<CharacterDto, AppError> {
    let snapshot = state.characters.create(brief).map_err(AppError::from)?;
    emit_configuration_change(
        &app,
        ConfigurationChange::Character {
            revision: snapshot.revision(),
        },
    )?;
    Ok(CharacterDto::from(snapshot))
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
    emit_configuration_change(
        &app,
        ConfigurationChange::Character {
            revision: snapshot.revision(),
        },
    )?;
    Ok(CharacterDto::from(snapshot))
}

#[tauri::command]
pub fn list_characters(state: State<'_, AppState>) -> Result<CharacterCatalogDto, AppError> {
    let catalog = state.characters.list().map_err(AppError::from)?;
    let active = state
        .characters
        .active()
        .map_err(AppError::from)?
        .map(CharacterDto::from);
    Ok(CharacterCatalogDto {
        characters: catalog
            .characters
            .into_iter()
            .map(CharacterDto::from)
            .collect(),
        active,
        diagnostics: catalog
            .diagnostics
            .into_iter()
            .map(CharacterDiagnosticDto::from)
            .collect(),
    })
}

#[tauri::command]
pub fn activate_character<R: Runtime>(
    app: AppHandle<R>,
    state: State<'_, AppState>,
    character_id: CharacterId,
    revision: Revision,
    conversation_id: Option<ConversationId>,
) -> Result<CharacterDto, AppError> {
    let conversation_id = conversation_id.or(state.active_conversation_id()?);
    let runtime = conversation_id.map(|_| state.runtime()).transpose()?;
    let previous = state.characters.active().map_err(AppError::from)?;
    let snapshot = state
        .characters
        .activate(character_id, revision)
        .map_err(AppError::from)?;

    let (Some(conversation_id), Some(runtime)) = (conversation_id, runtime) else {
        emit_configuration_change(
            &app,
            ConfigurationChange::Character {
                revision: snapshot.revision(),
            },
        )?;
        return Ok(CharacterDto::from(snapshot));
    };
    let activation = runtime.activate_character(conversation_id, snapshot.clone());
    if let Err(error) = activation {
        rollback_active_character(&state, previous)?;
        return Err(AppError::from(error));
    }
    emit_configuration_change(
        &app,
        ConfigurationChange::Character {
            revision: snapshot.revision(),
        },
    )?;
    Ok(CharacterDto::from(snapshot))
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
