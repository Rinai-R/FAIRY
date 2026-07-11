use fairy_domain::{
    AssistantSource, ConversationId, FairyError, HarnessEvent, LaneModelUsage, ResponseText,
    Revision, SpeechText, TurnId, TurnState,
};
use serde::Serialize;

pub trait HarnessEventSink: Send {
    fn send(&mut self, event: HarnessEvent) -> Result<(), FairyError>;
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionSnapshot {
    pub conversation_id: ConversationId,
    pub state: TurnState,
    pub active_turn_id: Option<TurnId>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct TurnOutcome {
    pub conversation_id: ConversationId,
    pub turn_id: TurnId,
    pub response_text: ResponseText,
    pub speech_text: SpeechText,
    pub sources: Vec<AssistantSource>,
    pub character_revision: Revision,
    pub user_profile_revision: Option<Revision>,
    pub usage: Vec<LaneModelUsage>,
    pub speech_requested: bool,
}
