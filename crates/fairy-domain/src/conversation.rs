use serde::{Deserialize, Serialize};

use crate::{
    AssistantSource, ConversationId, ErrorCode, FairyError, HarnessEvent, HarnessEventPayload,
    LaneModelUsage, ResponseText, Revision, SpeechText, TurnId,
};

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

    pub fn fail(&mut self, error: FairyError) -> Result<HarnessEvent, FairyError> {
        if !self.state.can_transition_to(TurnState::Failed) {
            return Err(FairyError::invalid_state(self.state, TurnState::Failed));
        }

        self.state = TurnState::Failed;
        Ok(self.event(HarnessEventPayload::Failed { error }))
    }

    pub fn complete(
        &mut self,
        text: ResponseText,
        speech_text: SpeechText,
        sources: Vec<AssistantSource>,
        character_revision: Revision,
        user_profile_revision: Option<Revision>,
        usage: Vec<LaneModelUsage>,
    ) -> Result<HarnessEvent, FairyError> {
        if !self.state.can_transition_to(TurnState::Completed) {
            return Err(FairyError::invalid_state(self.state, TurnState::Completed));
        }
        self.state = TurnState::Completed;
        Ok(self.event(HarnessEventPayload::Completed {
            text,
            speech_text,
            sources,
            character_revision,
            user_profile_revision,
            usage,
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
            .complete(
                ResponseText::new("你好。".to_owned()).expect("display text"),
                SpeechText::new("你好。".to_owned()).expect("speech text"),
                Vec::new(),
                Revision::INITIAL,
                None,
                Vec::new(),
            )
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
}
