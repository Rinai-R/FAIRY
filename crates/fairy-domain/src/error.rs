use std::fmt;

use serde::{Deserialize, Serialize};

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum ErrorCode {
    ModelConfigRequired,
    ModelSecretUnavailable,
    ModelAuthFailed,
    ModelProtocolMismatch,
    ModelStreamFailed,
    ModelResponseInvalid,
    InvalidModelConfig,
    InvalidCharacterBrief,
    CharacterCompileFailed,
    CharacterNotAvailable,
    InvalidUserProfile,
    UserProfileUnavailable,
    TurnInProgress,
    TurnNotActive,
    TurnInterrupted,
    InvalidStateTransition,
    InvalidEventPayload,
    ConversationNotFound,
    StorageCorrupted,
    StorageIo,
    PromptHistoryInvalid,
    CompactionFailed,
    IpcChannelClosed,
}

impl ErrorCode {
    #[must_use]
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::ModelConfigRequired => "MODEL_CONFIG_REQUIRED",
            Self::ModelSecretUnavailable => "MODEL_SECRET_UNAVAILABLE",
            Self::ModelAuthFailed => "MODEL_AUTH_FAILED",
            Self::ModelProtocolMismatch => "MODEL_PROTOCOL_MISMATCH",
            Self::ModelStreamFailed => "MODEL_STREAM_FAILED",
            Self::ModelResponseInvalid => "MODEL_RESPONSE_INVALID",
            Self::InvalidModelConfig => "INVALID_MODEL_CONFIG",
            Self::InvalidCharacterBrief => "INVALID_CHARACTER_BRIEF",
            Self::CharacterCompileFailed => "CHARACTER_COMPILE_FAILED",
            Self::CharacterNotAvailable => "CHARACTER_NOT_AVAILABLE",
            Self::InvalidUserProfile => "INVALID_USER_PROFILE",
            Self::UserProfileUnavailable => "USER_PROFILE_UNAVAILABLE",
            Self::TurnInProgress => "TURN_IN_PROGRESS",
            Self::TurnNotActive => "TURN_NOT_ACTIVE",
            Self::TurnInterrupted => "TURN_INTERRUPTED",
            Self::InvalidStateTransition => "INVALID_STATE_TRANSITION",
            Self::InvalidEventPayload => "INVALID_EVENT_PAYLOAD",
            Self::ConversationNotFound => "CONVERSATION_NOT_FOUND",
            Self::StorageCorrupted => "STORAGE_CORRUPTED",
            Self::StorageIo => "STORAGE_IO",
            Self::PromptHistoryInvalid => "PROMPT_HISTORY_INVALID",
            Self::CompactionFailed => "COMPACTION_FAILED",
            Self::IpcChannelClosed => "IPC_CHANNEL_CLOSED",
        }
    }
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct FairyError {
    pub code: ErrorCode,
    pub message: String,
    pub retryable: bool,
}

impl FairyError {
    #[must_use]
    pub fn new(code: ErrorCode, message: impl Into<String>, retryable: bool) -> Self {
        Self {
            code,
            message: message.into(),
            retryable,
        }
    }

    #[must_use]
    pub fn invalid_state(current: crate::TurnState, attempted: crate::TurnState) -> Self {
        Self::new(
            ErrorCode::InvalidStateTransition,
            format!("轮次不能从 {current:?} 转换到 {attempted:?}"),
            false,
        )
    }
}

impl fmt::Display for FairyError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(formatter, "{}: {}", self.code.as_str(), self.message)
    }
}

impl std::error::Error for FairyError {}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn error_code_has_stable_wire_name() {
        let error = FairyError::new(ErrorCode::ModelConfigRequired, "请先配置模型连接", false);
        let value = serde_json::to_value(error).expect("serialize error");

        assert_eq!(value["code"], "MODEL_CONFIG_REQUIRED");
        assert_eq!(value["retryable"], false);
    }

    #[test]
    fn display_contains_only_the_safe_domain_message() {
        let error = FairyError::new(
            ErrorCode::ModelAuthFailed,
            "模型认证失败，请检查连接设置",
            false,
        );

        assert_eq!(
            error.to_string(),
            "MODEL_AUTH_FAILED: 模型认证失败，请检查连接设置"
        );
        assert!(!error.to_string().contains("Bearer"));
    }
}
