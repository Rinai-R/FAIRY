use serde::{Deserialize, Serialize};

use crate::{
    AssistantSource, CharacterId, CompiledReplyChain, ConversationId, ErrorCode, FairyError,
    HarnessEvent, HarnessEventPayload, LaneModelUsage, MessageId, ResponseText, Revision,
    SpeechText, TurnId, VisualStateId, WindowRevision,
};

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ConversationMessageRole {
    User,
    Assistant,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ConversationRecord {
    pub id: ConversationId,
    pub character_id: CharacterId,
    pub created_at_unix_ms: i64,
    pub updated_at_unix_ms: i64,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ConversationMessageRecord {
    pub id: MessageId,
    pub conversation_id: ConversationId,
    pub turn_id: TurnId,
    pub sequence: u64,
    pub role: ConversationMessageRole,
    pub content: String,
    pub created_at_unix_ms: i64,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TurnCompletion {
    pub text: ResponseText,
    pub speech_text: SpeechText,
    pub sources: Vec<AssistantSource>,
    pub character_revision: Revision,
    pub user_profile_revision: Option<Revision>,
    pub usage: Vec<LaneModelUsage>,
    pub visual_state: VisualStateId,
    pub chains: Vec<CompiledReplyChain>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PersistedTurnRecord {
    pub id: TurnId,
    pub conversation_id: ConversationId,
    pub state: TurnState,
    pub error: Option<FairyError>,
    pub created_at_unix_ms: i64,
    pub updated_at_unix_ms: i64,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PromptWindowRecord {
    pub conversation_id: ConversationId,
    pub revision: WindowRevision,
    pub summary: Option<String>,
    pub cutoff_message_sequence: u64,
    pub updated_at_unix_ms: i64,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ConversationBootstrap {
    pub conversation: ConversationRecord,
    pub messages: Vec<ConversationMessageRecord>,
    pub prompt_window: PromptWindowRecord,
}

impl ConversationBootstrap {
    pub fn verify_integrity(&self) -> Result<(), FairyError> {
        if self.prompt_window.conversation_id != self.conversation.id {
            return Err(invalid_conversation(
                "Prompt window 不属于当前 conversation",
            ));
        }
        let mut previous_sequence = None;
        for message in &self.messages {
            if message.conversation_id != self.conversation.id {
                return Err(invalid_conversation("消息不属于当前 conversation"));
            }
            if message.content.is_empty() || message.content.chars().any(|value| value == '\0') {
                return Err(invalid_conversation("持久消息正文无效"));
            }
            if previous_sequence.is_some_and(|previous| message.sequence <= previous) {
                return Err(invalid_conversation("持久消息 sequence 不是严格递增"));
            }
            previous_sequence = Some(message.sequence);
        }
        Ok(())
    }
}

fn invalid_conversation(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::InvalidConversationRecord, message, false)
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum TurnState {
    Idle,
    Interpreting,
    Planning,
    Responding,
    Completed,
    Interrupted,
    Failed,
}

impl TurnState {
    #[must_use]
    pub const fn is_terminal(self) -> bool {
        matches!(self, Self::Completed | Self::Interrupted | Self::Failed)
    }

    #[must_use]
    pub const fn can_transition_to(self, next: Self) -> bool {
        matches!(
            (self, next),
            (Self::Idle, Self::Interpreting)
                | (Self::Interpreting, Self::Planning)
                | (Self::Interpreting, Self::Interrupted | Self::Failed)
                | (Self::Planning, Self::Responding)
                | (Self::Planning, Self::Interrupted | Self::Failed)
                | (
                    Self::Responding,
                    Self::Completed | Self::Interrupted | Self::Failed
                )
        )
    }
}

#[derive(Debug)]
pub struct TurnLifecycle {
    conversation_id: ConversationId,
    turn_id: TurnId,
    state: TurnState,
    next_sequence: u64,
}

impl TurnLifecycle {
    #[must_use]
    pub fn new(conversation_id: ConversationId, turn_id: TurnId) -> Self {
        Self {
            conversation_id,
            turn_id,
            state: TurnState::Idle,
            next_sequence: 1,
        }
    }

    #[must_use]
    pub const fn state(&self) -> TurnState {
        self.state
    }

    pub fn transition(&mut self, next: TurnState) -> Result<HarnessEvent, FairyError> {
        if !self.state.can_transition_to(next) {
            return Err(FairyError::invalid_state(self.state, next));
        }

        self.state = next;
        Ok(self.event(HarnessEventPayload::StateChanged))
    }

    pub fn text_delta(&mut self, delta: String) -> Result<HarnessEvent, FairyError> {
        if self.state != TurnState::Responding {
            return Err(FairyError::new(
                ErrorCode::InvalidStateTransition,
                format!(
                    "只有 Responding 状态可以发送文本增量，当前为 {:?}",
                    self.state
                ),
                false,
            ));
        }
        if delta.is_empty() {
            return Err(FairyError::new(
                ErrorCode::InvalidEventPayload,
                "文本增量不能为空",
                false,
            ));
        }

        Ok(self.event(HarnessEventPayload::TextDelta { delta }))
    }

    pub fn reply_chain(
        &mut self,
        index: u8,
        delta: String,
        chain: CompiledReplyChain,
    ) -> Result<HarnessEvent, FairyError> {
        if self.state != TurnState::Responding {
            return Err(FairyError::new(
                ErrorCode::InvalidStateTransition,
                format!(
                    "只有 Responding 状态可以发送回复分段，当前为 {:?}",
                    self.state
                ),
                false,
            ));
        }
        if delta.is_empty() {
            return Err(FairyError::new(
                ErrorCode::InvalidEventPayload,
                "回复分段增量不能为空",
                false,
            ));
        }

        Ok(self.event(HarnessEventPayload::ReplyChain {
            index,
            delta,
            text: chain.text,
            speech_text: chain.speech_text,
            visual_state: chain.visual_state,
        }))
    }

    pub fn fail(&mut self, error: FairyError) -> Result<HarnessEvent, FairyError> {
        if !self.state.can_transition_to(TurnState::Failed) {
            return Err(FairyError::invalid_state(self.state, TurnState::Failed));
        }

        self.state = TurnState::Failed;
        Ok(self.event(HarnessEventPayload::Failed { error }))
    }

    pub fn complete(&mut self, completion: TurnCompletion) -> Result<HarnessEvent, FairyError> {
        if !self.state.can_transition_to(TurnState::Completed) {
            return Err(FairyError::invalid_state(self.state, TurnState::Completed));
        }
        self.state = TurnState::Completed;
        Ok(self.event(HarnessEventPayload::Completed {
            text: completion.text,
            speech_text: completion.speech_text,
            sources: completion.sources,
            character_revision: completion.character_revision,
            user_profile_revision: completion.user_profile_revision,
            usage: completion.usage,
            visual_state: completion.visual_state,
            chains: completion.chains,
        }))
    }

    pub fn speech_requested(
        &mut self,
        text: SpeechText,
        character_revision: Revision,
        user_profile_revision: Option<Revision>,
    ) -> Result<HarnessEvent, FairyError> {
        if self.state != TurnState::Completed {
            return Err(FairyError::invalid_state(self.state, TurnState::Completed));
        }
        Ok(self.event(HarnessEventPayload::SpeechRequested {
            text,
            character_revision,
            user_profile_revision,
        }))
    }

    fn event(&mut self, payload: HarnessEventPayload) -> HarnessEvent {
        let event = HarnessEvent {
            conversation_id: self.conversation_id,
            turn_id: self.turn_id,
            sequence: self.next_sequence,
            state: self.state,
            payload,
        };
        self.next_sequence = self
            .next_sequence
            .checked_add(1)
            .expect("turn event sequence exhausted");
        event
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn bootstrap() -> ConversationBootstrap {
        let conversation_id = ConversationId::new();
        let turn_id = TurnId::new();
        ConversationBootstrap {
            conversation: ConversationRecord {
                id: conversation_id,
                character_id: CharacterId::new(),
                created_at_unix_ms: 1,
                updated_at_unix_ms: 2,
            },
            messages: vec![
                ConversationMessageRecord {
                    id: MessageId::new(),
                    conversation_id,
                    turn_id,
                    sequence: 1,
                    role: ConversationMessageRole::User,
                    content: "你好".to_owned(),
                    created_at_unix_ms: 1,
                },
                ConversationMessageRecord {
                    id: MessageId::new(),
                    conversation_id,
                    turn_id,
                    sequence: 2,
                    role: ConversationMessageRole::Assistant,
                    content: "嗯，怎么啦？".to_owned(),
                    created_at_unix_ms: 2,
                },
            ],
            prompt_window: PromptWindowRecord {
                conversation_id,
                revision: WindowRevision::INITIAL,
                summary: None,
                cutoff_message_sequence: 0,
                updated_at_unix_ms: 2,
            },
        }
    }

    #[test]
    fn conversation_bootstrap_accepts_one_owned_ordered_transcript() {
        bootstrap()
            .verify_integrity()
            .expect("valid conversation bootstrap");
    }

    #[test]
    fn conversation_bootstrap_rejects_cross_conversation_and_unordered_messages() {
        let mut cross_conversation = bootstrap();
        cross_conversation.messages[1].conversation_id = ConversationId::new();
        assert!(cross_conversation.verify_integrity().is_err());

        let mut unordered = bootstrap();
        unordered.messages[1].sequence = unordered.messages[0].sequence;
        assert!(unordered.verify_integrity().is_err());

        let mut wrong_window = bootstrap();
        wrong_window.prompt_window.conversation_id = ConversationId::new();
        assert!(wrong_window.verify_integrity().is_err());
    }

    fn lifecycle() -> TurnLifecycle {
        TurnLifecycle::new(ConversationId::new(), TurnId::new())
    }

    #[test]
    fn normal_turn_has_strictly_increasing_sequence_and_one_terminal_state() {
        let mut turn = lifecycle();

        let interpreting = turn
            .transition(TurnState::Interpreting)
            .expect("enter interpreting");
        let planning = turn
            .transition(TurnState::Planning)
            .expect("enter planning");
        let responding = turn
            .transition(TurnState::Responding)
            .expect("enter responding");
        let delta = turn.text_delta("你好".to_owned()).expect("emit delta");
        let completed = turn
            .complete(TurnCompletion {
                text: ResponseText::new("你好。".to_owned()).expect("display text"),
                speech_text: SpeechText::new("你好。".to_owned()).expect("speech text"),
                sources: Vec::new(),
                character_revision: Revision::INITIAL,
                user_profile_revision: None,
                usage: Vec::new(),
                visual_state: "idle".parse().expect("idle visual state"),
                chains: vec![CompiledReplyChain {
                    text: ResponseText::new("你好。".to_owned()).expect("chain text"),
                    speech_text: SpeechText::new("你好。".to_owned()).expect("chain speech"),
                    visual_state: "idle".parse().expect("idle visual state"),
                }],
            })
            .expect("complete turn");

        assert_eq!(
            [
                interpreting.sequence,
                planning.sequence,
                responding.sequence,
                delta.sequence,
                completed.sequence,
            ],
            [1, 2, 3, 4, 5]
        );
        assert_eq!(turn.state(), TurnState::Completed);
        assert!(turn.state().is_terminal());
        assert!(turn.transition(TurnState::Failed).is_err());
        assert!(turn.text_delta("迟到的增量".to_owned()).is_err());
    }

    #[test]
    fn invalid_transition_is_rejected_without_changing_state_or_sequence() {
        let mut turn = lifecycle();

        let error = turn
            .transition(TurnState::Responding)
            .expect_err("idle cannot skip to responding");
        assert_eq!(error.code, ErrorCode::InvalidStateTransition);
        assert_eq!(turn.state(), TurnState::Idle);

        let first = turn
            .transition(TurnState::Interpreting)
            .expect("first valid transition");
        assert_eq!(first.sequence, 1);
    }

    #[test]
    fn cancellation_is_legal_from_each_active_state() {
        for active in [
            TurnState::Interpreting,
            TurnState::Planning,
            TurnState::Responding,
        ] {
            let mut turn = lifecycle();
            turn.transition(TurnState::Interpreting)
                .expect("enter interpreting");
            if matches!(active, TurnState::Planning | TurnState::Responding) {
                turn.transition(TurnState::Planning)
                    .expect("enter planning");
            }
            if active == TurnState::Responding {
                turn.transition(TurnState::Responding)
                    .expect("enter responding");
            }

            let interrupted = turn
                .transition(TurnState::Interrupted)
                .expect("interrupt active turn");
            assert_eq!(interrupted.state, TurnState::Interrupted);
            assert!(turn.text_delta("不应发送".to_owned()).is_err());
        }
    }

    #[test]
    fn failure_is_terminal_and_carries_only_safe_error() {
        let mut turn = lifecycle();
        turn.transition(TurnState::Interpreting)
            .expect("enter interpreting");
        let event = turn
            .fail(FairyError::new(
                ErrorCode::ModelStreamFailed,
                "模型连接中断",
                true,
            ))
            .expect("fail active turn");

        assert_eq!(event.state, TurnState::Failed);
        assert!(matches!(
            event.payload,
            HarnessEventPayload::Failed {
                error: FairyError {
                    code: ErrorCode::ModelStreamFailed,
                    ..
                }
            }
        ));
        assert!(turn.transition(TurnState::Interrupted).is_err());
    }

    #[test]
    fn text_delta_preserves_whitespace_but_rejects_empty_chunk() {
        let mut turn = lifecycle();
        turn.transition(TurnState::Interpreting)
            .expect("enter interpreting");
        turn.transition(TurnState::Planning)
            .expect("enter planning");
        turn.transition(TurnState::Responding)
            .expect("enter responding");

        let event = turn.text_delta(" ".to_owned()).expect("whitespace is text");
        assert!(matches!(
            event.payload,
            HarnessEventPayload::TextDelta { ref delta } if delta == " "
        ));
        assert!(turn.text_delta(String::new()).is_err());
    }

    #[test]
    fn reply_chain_events_carry_segment_text_and_visual_state() {
        let mut turn = lifecycle();
        turn.transition(TurnState::Interpreting)
            .expect("enter interpreting");
        turn.transition(TurnState::Planning)
            .expect("enter planning");
        turn.transition(TurnState::Responding)
            .expect("enter responding");

        let event = turn
            .reply_chain(
                0,
                "你好。".to_owned(),
                CompiledReplyChain {
                    text: ResponseText::new("你好。".to_owned()).expect("text"),
                    speech_text: SpeechText::new("你好。".to_owned()).expect("speech"),
                    visual_state: "happy".parse().expect("visual state"),
                },
            )
            .expect("emit reply chain");

        assert!(matches!(
            event.payload,
            HarnessEventPayload::ReplyChain {
                index: 0,
                ref delta,
                ref visual_state,
                ..
            } if delta == "你好。" && visual_state.as_str() == "happy"
        ));
    }
}
