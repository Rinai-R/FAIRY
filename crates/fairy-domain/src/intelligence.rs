use serde::{Deserialize, Serialize};

use crate::{
    AssistantSource, ConversationId, ExtractionJobId, FairyError, KnowledgeId, PersonalMemoryId,
    TurnId,
};

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum PersonalMemoryKind {
    Preference,
    Profile,
    Relationship,
    Experience,
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
pub enum ExtractionJobStatus {
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
pub struct ExtractionJobRecord {
    pub id: ExtractionJobId,
    pub conversation_id: ConversationId,
    pub turn_id: TurnId,
    pub status: ExtractionJobStatus,
    pub error: Option<FairyError>,
    pub created_at_unix_ms: i64,
    pub updated_at_unix_ms: i64,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RetrievedPersonalMemory {
    pub id: PersonalMemoryId,
    pub kind: PersonalMemoryKind,
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
    pub active_personal_memories: u64,
    pub candidate_knowledge: u64,
    pub verified_knowledge: u64,
    pub pending_jobs: u64,
    pub running_jobs: u64,
    pub failed_jobs: u64,
}
