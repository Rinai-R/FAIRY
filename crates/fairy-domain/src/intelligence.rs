use serde::{Deserialize, Serialize};

use crate::{
    AssistantSource, CharacterId, ConversationId, ErrorCode, ExtractionBatchId, FairyError,
    KnowledgeId, PersonalMemoryId, TurnId,
};

#[derive(Clone, Copy, Debug, Deserialize, Eq, Hash, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum PersonalMemoryKind {
    Preference,
    Profile,
    Relationship,
    Experience,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, Hash, PartialEq, Serialize)]
#[serde(
    tag = "type",
    rename_all = "snake_case",
    rename_all_fields = "camelCase"
)]
pub enum MemoryScope {
    Global,
    Character { character_id: CharacterId },
    UnassignedLegacy,
}

impl MemoryScope {
    pub fn validate_for(self, kind: PersonalMemoryKind) -> Result<(), FairyError> {
        let valid = matches!(
            (kind, self),
            (
                PersonalMemoryKind::Preference
                    | PersonalMemoryKind::Profile
                    | PersonalMemoryKind::Experience,
                Self::Global
            ) | (
                PersonalMemoryKind::Relationship,
                Self::Character { .. } | Self::UnassignedLegacy
            )
        );
        if valid {
            Ok(())
        } else {
            Err(invalid_memory("个人记忆 kind 与 scope 不兼容"))
        }
    }
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum PersonalMemoryReviewStatus {
    Ready,
    NeedsReview,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum PersonalMemoryStatus {
    Active,
    Superseded,
    Tombstone,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum KnowledgeStatus {
    Candidate,
    Verified,
    Superseded,
    Tombstone,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum KnowledgeVerificationBasis {
    Unverified,
    WebSource,
    UserConfirmed,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ExtractionBatchStatus {
    Pending,
    Running,
    Succeeded,
    Failed,
    Cancelled,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct NewPersonalMemory {
    pub kind: PersonalMemoryKind,
    pub scope: MemoryScope,
    pub content: String,
    pub confidence_basis_points: u16,
    pub source_conversation_id: ConversationId,
    pub source_turn_id: TurnId,
    pub supersedes_id: Option<PersonalMemoryId>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PersonalMemoryRecord {
    pub id: PersonalMemoryId,
    pub kind: PersonalMemoryKind,
    pub scope: MemoryScope,
    pub review_status: PersonalMemoryReviewStatus,
    pub content: String,
    pub status: PersonalMemoryStatus,
    pub confidence_basis_points: u16,
    pub source_conversation_id: ConversationId,
    pub source_turn_id: TurnId,
    pub supersedes_id: Option<PersonalMemoryId>,
    pub created_at_unix_ms: i64,
    pub updated_at_unix_ms: i64,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct NewKnowledge {
    pub topic: String,
    pub statement: String,
    pub confidence_basis_points: u16,
    pub source_conversation_id: ConversationId,
    pub source_turn_id: TurnId,
    pub supersedes_id: Option<KnowledgeId>,
    pub sources: Vec<AssistantSource>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct KnowledgeRecord {
    pub id: KnowledgeId,
    pub topic: String,
    pub statement: String,
    pub status: KnowledgeStatus,
    pub verification_basis: KnowledgeVerificationBasis,
    pub confidence_basis_points: u16,
    pub source_conversation_id: ConversationId,
    pub source_turn_id: TurnId,
    pub supersedes_id: Option<KnowledgeId>,
    pub sources: Vec<AssistantSource>,
    pub created_at_unix_ms: i64,
    pub updated_at_unix_ms: i64,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RetrievedPersonalMemory {
    pub id: PersonalMemoryId,
    pub kind: PersonalMemoryKind,
    pub scope: MemoryScope,
    pub content: String,
    pub confidence_basis_points: u16,
    pub updated_at_unix_ms: i64,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RetrievedKnowledge {
    pub id: KnowledgeId,
    pub topic: String,
    pub statement: String,
    pub verification_basis: KnowledgeVerificationBasis,
    pub confidence_basis_points: u16,
    pub sources: Vec<AssistantSource>,
    pub updated_at_unix_ms: i64,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct KnowledgeCatalog {
    pub candidates: Vec<KnowledgeRecord>,
    pub verified: Vec<KnowledgeRecord>,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RetrievalContext {
    pub personal_memories: Vec<RetrievedPersonalMemory>,
    pub knowledge: Vec<RetrievedKnowledge>,
}

impl RetrievalContext {
    #[must_use]
    pub fn is_empty(&self) -> bool {
        self.personal_memories.is_empty() && self.knowledge.is_empty()
    }
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct ExtractedPersonalMemory {
    pub kind: PersonalMemoryKind,
    pub content: String,
    pub confidence_basis_points: u16,
    pub supersedes_id: Option<PersonalMemoryId>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(
    deny_unknown_fields,
    tag = "operation",
    rename_all = "snake_case",
    rename_all_fields = "camelCase"
)]
pub enum MemoryMutation {
    Create {
        kind: PersonalMemoryKind,
        scope: MemoryScope,
        content: String,
        confidence_basis_points: u16,
    },
    Supersede {
        memory_id: PersonalMemoryId,
        kind: PersonalMemoryKind,
        scope: MemoryScope,
        content: String,
        confidence_basis_points: u16,
    },
}

impl MemoryMutation {
    pub fn verify_integrity(&self) -> Result<(), FairyError> {
        let (kind, scope, content, confidence) = match self {
            Self::Create {
                kind,
                scope,
                content,
                confidence_basis_points,
            }
            | Self::Supersede {
                kind,
                scope,
                content,
                confidence_basis_points,
                ..
            } => (*kind, *scope, content, *confidence_basis_points),
        };
        scope.validate_for(kind)?;
        if content.trim().is_empty()
            || content.trim() != content
            || content.chars().any(char::is_control)
        {
            return Err(invalid_memory("记忆 mutation 正文无效"));
        }
        if confidence > 10_000 {
            return Err(invalid_memory("记忆 mutation 置信度超出范围"));
        }
        Ok(())
    }
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct MemoryMutationOutput {
    pub mutations: Vec<MemoryMutation>,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(tag = "status", rename_all = "snake_case")]
pub enum MemoryMutationResult {
    Applied {
        memory_id: PersonalMemoryId,
    },
    NoChange {
        existing_memory_id: PersonalMemoryId,
    },
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ExtractionTurn {
    pub turn_id: TurnId,
    pub user_message: String,
    pub assistant_message: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ExtractionBatchInput {
    pub batch_id: ExtractionBatchId,
    pub conversation_id: ConversationId,
    pub character_id: CharacterId,
    pub turns: Vec<ExtractionTurn>,
    pub existing_memories: Vec<RetrievedPersonalMemory>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ExtractionBatchRecord {
    pub id: ExtractionBatchId,
    pub conversation_id: ConversationId,
    pub character_id: CharacterId,
    pub status: ExtractionBatchStatus,
    pub first_turn_sequence: u64,
    pub last_turn_sequence: u64,
    pub error: Option<FairyError>,
    pub created_at_unix_ms: i64,
    pub updated_at_unix_ms: i64,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ExtractionBatchCatalog {
    pub running: Vec<ExtractionBatchRecord>,
    pub failed: Vec<ExtractionBatchRecord>,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PersonalMemoryCatalog {
    pub global: Vec<PersonalMemoryRecord>,
    pub character: Vec<PersonalMemoryRecord>,
    pub needs_review: Vec<PersonalMemoryRecord>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct ExtractedKnowledge {
    pub topic: String,
    pub statement: String,
    pub confidence_basis_points: u16,
    pub supersedes_id: Option<KnowledgeId>,
    pub source_ranks: Vec<u8>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct ExtractionOutput {
    pub personal_memories: Vec<ExtractedPersonalMemory>,
    pub knowledge: Vec<ExtractedKnowledge>,
}

#[derive(Clone, Copy, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct IntelligenceStoreSummary {
    pub conversations: u64,
    pub active_global_memories: u64,
    pub active_character_memories: u64,
    pub needs_review_memories: u64,
    pub pending_extraction_turns: u64,
    pub running_batches: u64,
    pub failed_batches: u64,
    pub candidate_knowledge: u64,
    pub verified_knowledge: u64,
}

fn invalid_memory(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::InvalidIntelligenceRecord, message, false)
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    #[test]
    fn memory_scope_is_explicitly_bound_to_memory_kind() {
        let character_id = CharacterId::new();

        assert!(
            MemoryScope::Global
                .validate_for(PersonalMemoryKind::Preference)
                .is_ok()
        );
        assert!(
            MemoryScope::Character { character_id }
                .validate_for(PersonalMemoryKind::Relationship)
                .is_ok()
        );
        assert!(
            MemoryScope::Global
                .validate_for(PersonalMemoryKind::Relationship)
                .is_err()
        );
        assert!(
            MemoryScope::Character { character_id }
                .validate_for(PersonalMemoryKind::Profile)
                .is_err()
        );
    }

    #[test]
    fn memory_mutation_wire_shape_is_strict_and_does_not_support_delete() {
        let character_id = CharacterId::new();
        let create = json!({
            "operation": "create",
            "kind": "relationship",
            "scope": { "type": "character", "characterId": character_id },
            "content": "用户和当前角色约定周末继续聊这件事",
            "confidenceBasisPoints": 9200
        });
        let mutation: MemoryMutation =
            serde_json::from_value(create).expect("deserialize strict create mutation");
        mutation.verify_integrity().expect("valid mutation");

        let unknown_field = json!({
            "operation": "create",
            "kind": "preference",
            "scope": { "type": "global" },
            "content": "用户喜欢红茶",
            "confidenceBasisPoints": 9000,
            "reasoning": "must not enter the wire contract"
        });
        assert!(serde_json::from_value::<MemoryMutation>(unknown_field).is_err());

        let delete = json!({
            "operation": "delete",
            "memoryId": PersonalMemoryId::new()
        });
        assert!(serde_json::from_value::<MemoryMutation>(delete).is_err());
    }

    #[test]
    fn mutation_integrity_rejects_invalid_scope_content_and_confidence() {
        let invalid_scope = MemoryMutation::Create {
            kind: PersonalMemoryKind::Relationship,
            scope: MemoryScope::Global,
            content: "用户信任当前角色".to_owned(),
            confidence_basis_points: 8000,
        };
        assert!(invalid_scope.verify_integrity().is_err());

        let invalid_content = MemoryMutation::Create {
            kind: PersonalMemoryKind::Preference,
            scope: MemoryScope::Global,
            content: " 用户喜欢红茶".to_owned(),
            confidence_basis_points: 8000,
        };
        assert!(invalid_content.verify_integrity().is_err());

        let invalid_confidence = MemoryMutation::Create {
            kind: PersonalMemoryKind::Preference,
            scope: MemoryScope::Global,
            content: "用户喜欢红茶".to_owned(),
            confidence_basis_points: 10_001,
        };
        assert!(invalid_confidence.verify_integrity().is_err());
    }
}
