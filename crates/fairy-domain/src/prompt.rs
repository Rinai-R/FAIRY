use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::{CharacterSnapshot, ErrorCode, FairyError, RetrievalContext, UserProfileSnapshot};

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
pub struct CompiledReply {
    pub display_text: ResponseText,
    pub speech_text: SpeechText,
    pub sources: Vec<AssistantSource>,
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
    CapabilityStatus {
        capability: CompanionCapability,
        state: CapabilityState,
        error: Option<FairyError>,
    },
    ToolCall {
        call: ToolCall,
    },
    ToolResult {
        result: ToolResult,
    },
    CompactionSummary {
        summary: String,
    },
    ExtractionInput {
        user_message: String,
        assistant_message: String,
        sources: Vec<AssistantSource>,
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
pub enum ToolName {
    WebSearch,
}

impl ToolName {
    #[must_use]
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::WebSearch => "web_search",
        }
    }

    pub fn parse(value: &str) -> Result<Self, FairyError> {
        match value {
            "web_search" => Ok(Self::WebSearch),
            _ => Err(FairyError::new(
                ErrorCode::ModelResponseInvalid,
                "模型请求了未声明的工具名称",
                false,
            )),
        }
    }
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ToolDefinition {
    pub name: ToolName,
    pub description: String,
    pub parameters: serde_json::Value,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ToolCall {
    pub id: String,
    pub name: ToolName,
    pub arguments_json: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ToolResult {
    pub call_id: String,
    pub name: ToolName,
    pub outcome: ToolResultOutcome,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(tag = "status", rename_all = "snake_case")]
pub enum ToolResultOutcome {
    Success {
        output: String,
        sources: Vec<AssistantSource>,
    },
    Failed {
        error: FairyError,
    },
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ToolPolicy {
    Disabled,
    Auto { tools: Vec<ToolDefinition> },
}

impl ToolPolicy {
    #[must_use]
    pub fn tools(&self) -> &[ToolDefinition] {
        match self {
            Self::Disabled => &[],
            Self::Auto { tools } => tools,
        }
    }
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
    pub tool_policy: ToolPolicy,
    pub parallel_tool_calls: bool,
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
pub struct CompiledPromptRequest {
    pub shape: ModelRequestShape,
    pub input: Vec<PromptItem>,
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
        };
        let value = serde_json::to_value(reply).expect("serialize compiled reply");

        assert_eq!(value["displayText"], "第一句。后续说明。");
        assert_eq!(value["speechText"], "第一句。");
        assert_eq!(value["sources"][0]["rank"], 1);
    }
}
