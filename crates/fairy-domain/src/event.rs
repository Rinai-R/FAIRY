use serde::{Deserialize, Serialize};

use crate::{
    AssistantSource, CompiledReplyChain, ConversationId, FairyError, LaneModelUsage, ResponseText,
    Revision, SpeechText, TurnId, TurnState, VisualStateId,
};

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(
    tag = "type",
    rename_all = "snake_case",
    rename_all_fields = "camelCase"
)]
pub enum HarnessEventPayload {
    StateChanged,
    TextDelta {
        delta: String,
    },
    ReplyChain {
        index: u8,
        delta: String,
        text: ResponseText,
        speech_text: SpeechText,
        visual_state: VisualStateId,
    },
    Completed {
        text: ResponseText,
        speech_text: SpeechText,
        sources: Vec<AssistantSource>,
        character_revision: Revision,
        user_profile_revision: Option<Revision>,
        usage: Vec<LaneModelUsage>,
        visual_state: VisualStateId,
        chains: Vec<CompiledReplyChain>,
    },
    #[serde(rename = "speech.requested")]
    SpeechRequested {
        text: SpeechText,
        character_revision: Revision,
        user_profile_revision: Option<Revision>,
    },
    Failed {
        error: FairyError,
    },
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HarnessEvent {
    pub conversation_id: ConversationId,
    pub turn_id: TurnId,
    pub sequence: u64,
    pub state: TurnState,
    pub payload: HarnessEventPayload,
}
