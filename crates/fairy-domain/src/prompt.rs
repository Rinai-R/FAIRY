use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::{
    CharacterSnapshot, ErrorCode, ExtractionBatchInput, FairyError, RetrievalContext,
    UserProfileSnapshot, VisualStateId,
};

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(transparent)]
pub struct ResponseText(String);

impl ResponseText {
    pub fn new(text: String) -> Result<Self, FairyError> {
        if text.is_empty() {
            return Err(FairyError::new(
                ErrorCode::ModelResponseInvalid,
                "模型没有返回可用回复文本",
                false,
            ));
        }
        Ok(Self(text))
    }

    #[must_use]
    pub fn as_str(&self) -> &str {
        &self.0
    }

    #[must_use]
    pub fn into_inner(self) -> String {
        self.0
    }
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(transparent)]
pub struct SpeechText(String);

impl SpeechText {
    pub fn new(text: String) -> Result<Self, FairyError> {
        if text.is_empty() {
            return Err(FairyError::new(
                ErrorCode::ModelResponseInvalid,
                "语音文本不能为空",
                false,
            ));
        }
        if text.contains(['\r', '\n']) || text.chars().any(|character| character == '\0') {
            return Err(FairyError::new(
                ErrorCode::ModelResponseInvalid,
                "语音文本必须是单行有效文本",
                false,
            ));
        }
        Ok(Self(text))
    }

    #[must_use]
    pub fn as_str(&self) -> &str {
        &self.0
    }

    #[must_use]
    pub fn into_inner(self) -> String {
        self.0
    }
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AssistantSource {
    pub title: String,
    pub url: String,
    pub snippet: String,
    pub rank: u8,
    pub fetched_at_unix_ms: i64,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CompiledReplyChain {
    pub text: ResponseText,
    pub speech_text: SpeechText,
    pub visual_state: VisualStateId,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CompiledReply {
    pub display_text: ResponseText,
    pub speech_text: SpeechText,
    pub sources: Vec<AssistantSource>,
    pub visual_state: VisualStateId,
    pub chains: Vec<CompiledReplyChain>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct VisualStatePromptEntry {
    pub id: VisualStateId,
    pub description: String,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, Hash, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum PromptLane {
    Respond,
    Compact,
    Extract,
}

impl PromptLane {
    #[must_use]
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::Respond => "respond",
            Self::Compact => "compact",
            Self::Extract => "extract",
        }
    }
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum PromptItem {
    CharacterActivated {
        snapshot: CharacterSnapshot,
    },
    UserProfileUpdated {
        snapshot: Option<UserProfileSnapshot>,
    },
    UserMessage {
        content: String,
    },
    AssistantMessage {
        content: String,
    },
    RetrievedContext {
        context: RetrievalContext,
    },
    AvailableVisualStates {
        states: Vec<VisualStatePromptEntry>,
    },
    CapabilityStatus {
        capability: CompanionCapability,
        state: CapabilityState,
        error: Option<FairyError>,
    },
    CompactionSummary {
        summary: String,
    },
    ExtractionBatch {
        input: ExtractionBatchInput,
    },
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum CompanionCapability {
    Intelligence,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum CapabilityState {
    Unavailable,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ReasoningMode {
    ProviderDefault,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct ModelRequestShape {
    pub lane: PromptLane,
    pub model: String,
    pub instructions: String,
    pub max_output_tokens: u32,
    pub reasoning: ReasoningMode,
    pub prompt_cache_key: Option<String>,
}

impl ModelRequestShape {
    pub fn canonical_bytes(&self) -> Result<Vec<u8>, FairyError> {
        serde_json::to_vec(self).map_err(|_| {
            FairyError::new(
                ErrorCode::ModelResponseInvalid,
                "无法序列化稳定模型请求形状",
                false,
            )
        })
    }

    pub fn fingerprint(&self) -> Result<String, FairyError> {
        let digest = Sha256::digest(self.canonical_bytes()?);
        Ok(digest.iter().map(|byte| format!("{byte:02x}")).collect())
    }
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PromptContinuation {
    previous_response_id: String,
}

impl PromptContinuation {
    pub fn new(previous_response_id: String) -> Result<Self, FairyError> {
        if previous_response_id.is_empty()
            || previous_response_id.trim() != previous_response_id
            || previous_response_id.chars().any(char::is_control)
        {
            return Err(FairyError::new(
                ErrorCode::ModelResponseInvalid,
                "previous_response_id 必须是非空有效文本",
                false,
            ));
        }
        Ok(Self {
            previous_response_id,
        })
    }

    #[must_use]
    pub fn previous_response_id(&self) -> &str {
        &self.previous_response_id
    }
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct CompiledPromptRequest {
    pub shape: ModelRequestShape,
    pub input: Vec<PromptItem>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub continuation: Option<PromptContinuation>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn response_text_rejects_empty_but_preserves_exact_whitespace() {
        assert!(ResponseText::new(String::new()).is_err());
        assert_eq!(
            ResponseText::new("  文本  ".to_owned())
                .expect("non-empty response")
                .as_str(),
            "  文本  "
        );
    }

    #[test]
    fn speech_text_is_non_empty_and_single_line() {
        assert!(SpeechText::new(String::new()).is_err());
        assert!(SpeechText::new("第一行\n第二行".to_owned()).is_err());
        assert!(SpeechText::new("包含\0空字符".to_owned()).is_err());
        assert_eq!(
            SpeechText::new("那就先休息一会儿吧。".to_owned())
                .expect("valid speech text")
                .as_str(),
            "那就先休息一会儿吧。"
        );
    }

    #[test]
    fn compiled_reply_serializes_speech_and_sources_separately() {
        let reply = CompiledReply {
            display_text: ResponseText::new("第一句。后续说明。".to_owned()).expect("display text"),
            speech_text: SpeechText::new("第一句。".to_owned()).expect("speech text"),
            sources: vec![AssistantSource {
                title: "来源".to_owned(),
                url: "https://example.test/source".to_owned(),
                snippet: "摘要".to_owned(),
                rank: 1,
                fetched_at_unix_ms: 42,
            }],
            visual_state: "idle".parse().expect("idle visual state"),
            chains: vec![CompiledReplyChain {
                text: ResponseText::new("第一句。后续说明。".to_owned()).expect("chain text"),
                speech_text: SpeechText::new("第一句。".to_owned()).expect("chain speech"),
                visual_state: "idle".parse().expect("idle visual state"),
            }],
        };
        let value = serde_json::to_value(reply).expect("serialize compiled reply");

        assert_eq!(value["displayText"], "第一句。后续说明。");
        assert_eq!(value["speechText"], "第一句。");
        assert_eq!(value["sources"][0]["rank"], 1);
        assert_eq!(value["chains"][0]["visualState"], "idle");
    }

    #[test]
    fn visual_state_context_serializes_without_image_paths() {
        let item = PromptItem::AvailableVisualStates {
            states: vec![VisualStatePromptEntry {
                id: "happy".parse().expect("visual state"),
                description: "开心回应，适合轻松确认。".to_owned(),
            }],
        };
        let value = serde_json::to_value(item).expect("serialize visual states");

        assert_eq!(value["type"], "available_visual_states");
        assert_eq!(value["states"][0]["id"], "happy");
        assert_eq!(
            value["states"][0]["description"],
            "开心回应，适合轻松确认。"
        );
        assert!(value["states"][0].get("imagePath").is_none());
    }

    #[test]
    fn prompt_continuation_validates_previous_response_id() {
        let continuation =
            PromptContinuation::new("resp_123".to_owned()).expect("valid response id");

        assert_eq!(continuation.previous_response_id(), "resp_123");
        assert!(PromptContinuation::new(String::new()).is_err());
        assert!(PromptContinuation::new(" resp_123".to_owned()).is_err());
        assert!(PromptContinuation::new("resp\n123".to_owned()).is_err());
    }
}
