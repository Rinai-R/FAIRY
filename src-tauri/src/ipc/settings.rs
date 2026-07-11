use std::sync::Arc;

use fairy_domain::{
    ConversationId, KnowledgeCatalog, KnowledgeId, KnowledgeRecord, ModelConnectionInput, Revision,
    SearchConnectionInput, UserProfileInput, UserProfileSnapshot,
};
use fairy_harness::HarnessRuntime;
use fairy_storage::UserProfileUpdate;
use serde::Serialize;
use tauri::{AppHandle, Runtime, State};

use crate::{
    app_error::AppError,
    app_state::{AppState, IntelligenceStatus, ModelConnectionStatus, SearchConnectionStatus},
    ipc::{ConfigurationChange, emit_configuration_change},
};

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct UserProfileDto {
    pub revision: Revision,
    pub preferred_name: Option<String>,
}

impl From<UserProfileSnapshot> for UserProfileDto {
    fn from(snapshot: UserProfileSnapshot) -> Self {
        Self {
            revision: snapshot.revision(),
            preferred_name: snapshot.preferred_name().map(str::to_owned),
        }
    }
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct UserProfileUpdateDto {
    pub profile: Option<UserProfileDto>,
    pub changed: bool,
    pub recovered_corruption: bool,
}

impl From<UserProfileUpdate> for UserProfileUpdateDto {
    fn from(update: UserProfileUpdate) -> Self {
        Self {
            profile: update.snapshot.map(UserProfileDto::from),
            changed: update.changed,
            recovered_corruption: update.recovered_corruption,
        }
    }
}

#[tauri::command]
pub fn get_user_profile(state: State<'_, AppState>) -> Result<Option<UserProfileDto>, AppError> {
    state
        .user_profiles
        .current()
        .map(|profile| profile.map(UserProfileDto::from))
        .map_err(AppError::from)
}

#[tauri::command]
pub fn set_user_profile<R: Runtime>(
    app: AppHandle<R>,
    state: State<'_, AppState>,
    input: UserProfileInput,
    conversation_id: Option<ConversationId>,
) -> Result<UserProfileUpdateDto, AppError> {
    let conversation_id = conversation_id.or(state.active_conversation_id()?);
    let runtime = runtime_for_conversation(&state, conversation_id)?;
    let update = state.user_profiles.update(input).map_err(AppError::from)?;
    synchronize_profile(runtime, conversation_id, &update)?;
    emit_configuration_change(
        &app,
        ConfigurationChange::UserProfile {
            revision: update.snapshot.as_ref().map(UserProfileSnapshot::revision),
        },
    )?;
    Ok(UserProfileUpdateDto::from(update))
}

#[tauri::command]
pub fn clear_user_profile<R: Runtime>(
    app: AppHandle<R>,
    state: State<'_, AppState>,
    conversation_id: Option<ConversationId>,
) -> Result<UserProfileUpdateDto, AppError> {
    let conversation_id = conversation_id.or(state.active_conversation_id()?);
    let runtime = runtime_for_conversation(&state, conversation_id)?;
    let update = state.user_profiles.clear().map_err(AppError::from)?;
    synchronize_profile(runtime, conversation_id, &update)?;
    emit_configuration_change(
        &app,
        ConfigurationChange::UserProfile {
            revision: update.snapshot.as_ref().map(UserProfileSnapshot::revision),
        },
    )?;
    Ok(UserProfileUpdateDto::from(update))
}

fn synchronize_profile(
    runtime: Option<Arc<HarnessRuntime>>,
    conversation_id: Option<ConversationId>,
    update: &UserProfileUpdate,
) -> Result<(), AppError> {
    let (Some(runtime), Some(conversation_id), Some(snapshot)) =
        (runtime, conversation_id, update.snapshot.clone())
    else {
        return Ok(());
    };
    runtime
        .update_user_profile(conversation_id, snapshot)
        .map(|_| ())
        .map_err(AppError::from)
}

fn runtime_for_conversation(
    state: &AppState,
    conversation_id: Option<ConversationId>,
) -> Result<Option<Arc<HarnessRuntime>>, AppError> {
    let conversation_id = conversation_id.or(state.active_conversation_id()?);
    let Some(conversation_id) = conversation_id else {
        return Ok(None);
    };
    let runtime = state.runtime()?;
    runtime
        .session_snapshot(conversation_id)
        .map_err(AppError::from)?;
    Ok(Some(runtime))
}

#[tauri::command]
pub fn get_model_connection_status(state: State<'_, AppState>) -> ModelConnectionStatus {
    state.model_status()
}

#[tauri::command]
pub fn save_model_connection<R: Runtime>(
    app: AppHandle<R>,
    state: State<'_, AppState>,
    input: ModelConnectionInput,
    api_key: Option<String>,
) -> Result<ModelConnectionStatus, AppError> {
    let status = state.save_model_connection(input, api_key)?;
    emit_configuration_change(
        &app,
        ConfigurationChange::Model {
            configured: status.configured,
            ready: status.ready,
        },
    )?;
    Ok(status)
}

#[tauri::command]
pub fn clear_model_connection<R: Runtime>(
    app: AppHandle<R>,
    state: State<'_, AppState>,
) -> Result<ModelConnectionStatus, AppError> {
    let status = state.clear_model_connection()?;
    emit_configuration_change(
        &app,
        ConfigurationChange::Model {
            configured: status.configured,
            ready: status.ready,
        },
    )?;
    Ok(status)
}

#[tauri::command]
pub fn get_search_connection_status(state: State<'_, AppState>) -> SearchConnectionStatus {
    state.search_status()
}

#[tauri::command]
pub fn save_search_connection<R: Runtime>(
    app: AppHandle<R>,
    state: State<'_, AppState>,
    input: SearchConnectionInput,
    api_key: Option<String>,
) -> Result<SearchConnectionStatus, AppError> {
    let status = state.save_search_connection(input, api_key)?;
    emit_configuration_change(
        &app,
        ConfigurationChange::Search {
            configured: status.configured,
            ready: status.ready,
        },
    )?;
    Ok(status)
}

#[tauri::command]
pub fn clear_search_connection<R: Runtime>(
    app: AppHandle<R>,
    state: State<'_, AppState>,
) -> Result<SearchConnectionStatus, AppError> {
    let status = state.clear_search_connection()?;
    emit_configuration_change(
        &app,
        ConfigurationChange::Search {
            configured: status.configured,
            ready: status.ready,
        },
    )?;
    Ok(status)
}

#[tauri::command]
pub fn get_intelligence_status(state: State<'_, AppState>) -> IntelligenceStatus {
    state.intelligence_status()
}

#[tauri::command]
pub fn get_knowledge_catalog(state: State<'_, AppState>) -> Result<KnowledgeCatalog, AppError> {
    state.knowledge_catalog()
}

#[tauri::command]
pub fn confirm_knowledge_candidate(
    state: State<'_, AppState>,
    id: KnowledgeId,
) -> Result<KnowledgeRecord, AppError> {
    state.confirm_knowledge_candidate(id)
}

#[tauri::command]
pub fn tombstone_knowledge(state: State<'_, AppState>, id: KnowledgeId) -> Result<(), AppError> {
    state.tombstone_knowledge(id)
}
