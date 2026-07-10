use fairy_domain::{ConversationId, ErrorCode, FairyError, HarnessEvent, TurnId};
use fairy_harness::{CompactionResult, HarnessEventSink, SessionSnapshot, TurnOutcome};
use tauri::{State, ipc::Channel};

use crate::{app_error::AppError, app_state::AppState};

struct ChannelEventSink {
    channel: Channel<HarnessEvent>,
}

impl HarnessEventSink for ChannelEventSink {
    fn send(&mut self, event: HarnessEvent) -> Result<(), FairyError> {
        self.channel.send(event).map_err(|_| {
            FairyError::new(ErrorCode::IpcChannelClosed, "前端事件通道已经关闭", false)
        })
    }
}

#[tauri::command]
pub fn create_companion_session(state: State<'_, AppState>) -> Result<SessionSnapshot, AppError> {
    let runtime = state.runtime()?;
    let session = runtime.create_session();
    if let Some(character) = state.characters.active().map_err(AppError::from)? {
        runtime
            .activate_character(session.conversation_id, character)
            .map_err(AppError::from)?;
    }
    if let Some(profile) = state.user_profiles.current().map_err(AppError::from)? {
        runtime
            .update_user_profile(session.conversation_id, profile)
            .map_err(AppError::from)?;
    }
    state.register_active_conversation(session.conversation_id)?;
    Ok(session)
}

#[tauri::command]
pub fn get_companion_session(
    state: State<'_, AppState>,
    conversation_id: ConversationId,
) -> Result<SessionSnapshot, AppError> {
    state
        .runtime()?
        .session_snapshot(conversation_id)
        .map_err(AppError::from)
}

#[tauri::command]
pub async fn submit_companion_turn(
    state: State<'_, AppState>,
    conversation_id: ConversationId,
    input: String,
    speech_enabled: bool,
    on_event: Channel<HarnessEvent>,
) -> Result<TurnOutcome, AppError> {
    let runtime = state.runtime()?;
    let mut events = ChannelEventSink { channel: on_event };
    runtime
        .submit_turn(conversation_id, input, speech_enabled, &mut events)
        .await
        .map_err(AppError::from)
}

#[tauri::command]
pub fn cancel_companion_turn(state: State<'_, AppState>, turn_id: TurnId) -> Result<(), AppError> {
    state
        .runtime()?
        .cancel_turn(turn_id)
        .map_err(AppError::from)
}

#[tauri::command]
pub async fn compact_companion_session(
    state: State<'_, AppState>,
    conversation_id: ConversationId,
) -> Result<CompactionResult, AppError> {
    state
        .runtime()?
        .compact_conversation(conversation_id)
        .await
        .map_err(AppError::from)
}

#[cfg(test)]
mod tests {
    use std::sync::{Arc, Mutex};

    use fairy_domain::{ConversationId, HarnessEventPayload, TurnLifecycle, TurnState};
    use tauri::ipc::InvokeResponseBody;

    use super::*;

    #[test]
    fn channel_sink_serializes_the_exact_harness_event() {
        let bodies = Arc::new(Mutex::new(Vec::new()));
        let captured = Arc::clone(&bodies);
        let channel = Channel::new(move |body| {
            captured.lock().expect("lock channel bodies").push(body);
            Ok(())
        });
        let conversation_id = ConversationId::new();
        let turn_id = TurnId::new();
        let event = TurnLifecycle::new(conversation_id, turn_id)
            .transition(TurnState::Interpreting)
            .expect("create state event");
        let mut sink = ChannelEventSink { channel };

        sink.send(event.clone()).expect("send event");
        let bodies = bodies.lock().expect("lock captured bodies");
        let InvokeResponseBody::Json(json) = &bodies[0] else {
            panic!("harness event must use JSON IPC")
        };
        let decoded: HarnessEvent = serde_json::from_str(json).expect("decode event");

        assert_eq!(decoded, event);
        assert!(json.contains("\"conversationId\""));
        assert!(json.contains("\"turnId\""));
        assert!(!json.contains("conversation_id"));
        assert_eq!(decoded.sequence, 1);
        assert_eq!(decoded.payload, HarnessEventPayload::StateChanged);
    }
}
