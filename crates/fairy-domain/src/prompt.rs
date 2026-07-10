use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::{CharacterSnapshot, ErrorCode, FairyError, UserProfileSnapshot};

pub const DIALOGUE_POLICY_VERSION: &str = "fairy-dialogue-policy-v1";

pub const DIALOGUE_PRIORITIES: [DialoguePriority; 7] = [
    DialoguePriority::FactsSafetyPrivacyRelationshipBoundaries,
    DialoguePriority::ExplicitUserRequest,
    DialoguePriority::ExternalFacts,
    DialoguePriority::InteractionHypothesis,
    DialoguePriority::CharacterPerspective,
    DialoguePriority::LanguageStyle,
    DialoguePriority::ImplicitExpectation,
];

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum DialoguePriority {
    FactsSafetyPrivacyRelationshipBoundaries,
    ExplicitUserRequest,
    ExternalFacts,
    InteractionHypothesis,
    CharacterPerspective,
    LanguageStyle,
    ImplicitExpectation,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ConversationGoal {
    NeedToBeHeard,
    NeedReassurance,
    NeedPracticalHelp,
    NeedClarification,
    CasualConversation,
    ShareJoy,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct EvidenceReference {
    pub quote: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct InteractionHypothesis {
    pub explicit_request: String,
    pub goal: ConversationGoal,
    pub evidence: Vec<EvidenceReference>,
    pub confidence: u8,
    pub ambiguity: Option<String>,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum RelationshipIntent {
    Listen,
    Reassure,
    Help,
    Clarify,
    Celebrate,
    Companion,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, Hash, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ResponseAction {
    AcknowledgeFeeling,
    ReflectContent,
    AskGentleQuestion,
    OfferPracticalHelp,
    GiveDirectAnswer,
    ReassureWithoutClaimingFacts,
    ShareLightReaction,
    StayPresent,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct CharacterPerspective {
    pub attention_focus: Vec<String>,
    pub relationship_intent: RelationshipIntent,
    pub candidate_actions: Vec<ResponseAction>,
    pub character_intensity: u8,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ResponseLength {
    Brief,
    Moderate,
    Detailed,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum FactCommitment {
    EvidenceBound,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum AmbiguityHandling {
    LowCommitmentResponse,
    ClarifyNaturally,
    ProceedWithExplicitRequest,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct TurnPolicy {
    pub policy_version: String,
    pub primary_action: ResponseAction,
    pub secondary_action: Option<ResponseAction>,
    pub use_preferred_name: bool,
    pub response_length: ResponseLength,
    pub fact_commitment: FactCommitment,
    pub ambiguity_handling: AmbiguityHandling,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub struct TurnPlan {
    pub interaction_hypothesis: InteractionHypothesis,
    pub character_perspective: CharacterPerspective,
    pub turn_policy: TurnPolicy,
}

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

#[derive(Clone, Copy, Debug, Deserialize, Eq, Hash, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum PromptLane {
    Interpret,
    Respond,
    Compact,
}

impl PromptLane {
    #[must_use]
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::Interpret => "interpret",
            Self::Respond => "respond",
            Self::Compact => "compact",
        }
    }
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum PromptItem {
    HarnessContext {
        protocol_version: String,
        policy_version: String,
        priorities: Vec<DialoguePriority>,
    },
    CharacterActivated {
        snapshot: CharacterSnapshot,
    },
    UserProfileUpdated {
        snapshot: Option<UserProfileSnapshot>,
    },
    UserMessage {
        content: String,
    },
    TurnPlan {
        plan: TurnPlan,
    },
    AssistantMessage {
        content: String,
    },
    CompactionSummary {
        summary: String,
    },
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ToolPolicy {
    Disabled,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ReasoningMode {
    ProviderDefault,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum ModelOutputContract {
    Text,
    JsonSchema {
        name: String,
        strict: bool,
        schema_json: String,
    },
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct ModelRequestShape {
    pub lane: PromptLane,
    pub model: String,
    pub instructions: String,
    pub tool_policy: ToolPolicy,
    pub parallel_tool_calls: bool,
    pub reasoning: ReasoningMode,
    pub output: ModelOutputContract,
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
    fn policy_priority_is_fixed_and_serializes_in_declared_order() {
        assert_eq!(
            DIALOGUE_PRIORITIES[0],
            DialoguePriority::FactsSafetyPrivacyRelationshipBoundaries
        );
        assert_eq!(
            DIALOGUE_PRIORITIES[DIALOGUE_PRIORITIES.len() - 1],
            DialoguePriority::ImplicitExpectation
        );
        let serialized =
            serde_json::to_string(&DIALOGUE_PRIORITIES).expect("serialize dialogue priority order");
        assert!(
            serialized.find("facts_safety").expect("first priority")
                < serialized
                    .find("implicit_expectation")
                    .expect("last priority")
        );
    }
}
