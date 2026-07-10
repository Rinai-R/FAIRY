use std::sync::Arc;

use fairy_domain::{
    ConversationId, ModelConnectionInput, Revision, UserProfileInput, UserProfileSnapshot,
};
use fairy_harness::HarnessRuntime;
use fairy_storage::UserProfileUpdate;
use serde::Serialize;
use tauri::State;

use crate::{
    app_error::AppError,
    app_state::{AppState, ModelConnectionStatus},
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
pub fn set_user_profile(
    state: State<'_, AppState>,
    input: UserProfileInput,
    conversation_id: Option<ConversationId>,
) -> Result<UserProfileUpdateDto, AppError> {
    let runtime = runtime_for_conversation(&state, conversation_id)?;
    let update = state.user_profiles.update(input).map_err(AppError::from)?;
    synchronize_profile(runtime, conversation_id, &update)?;
    Ok(UserProfileUpdateDto::from(update))
}

#[tauri::command]
pub fn clear_user_profile(
    state: State<'_, AppState>,
    conversation_id: Option<ConversationId>,
) -> Result<UserProfileUpdateDto, AppError> {
    let runtime = runtime_for_conversation(&state, conversation_id)?;
    let update = state.user_profiles.clear().map_err(AppError::from)?;
    synchronize_profile(runtime, conversation_id, &update)?;
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
pub fn save_model_connection(
    state: State<'_, AppState>,
    input: ModelConnectionInput,
    api_key: Option<String>,
) -> Result<ModelConnectionStatus, AppError> {
    state.save_model_connection(input, api_key)
}

#[tauri::command]
pub fn clear_model_connection(
    state: State<'_, AppState>,
) -> Result<ModelConnectionStatus, AppError> {
    state.clear_model_connection()
}
