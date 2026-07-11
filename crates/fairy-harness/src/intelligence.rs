use std::sync::Arc;

use async_trait::async_trait;
use fairy_domain::{
    CharacterId, ConversationBootstrap, ConversationId, ExtractionBatchId, ExtractionBatchInput,
    FairyError, MemoryMutation, MemoryMutationResult, PersonalMemoryId, PromptWindowRecord,
    RetrievalContext, TurnId, TurnState, WindowRevision,
};

#[async_trait]
pub trait CompanionPersistence: Send + Sync {
    async fn open_or_create_character_conversation(
        &self,
        character_id: CharacterId,
    ) -> Result<ConversationBootstrap, FairyError>;

    async fn begin_turn(
        &self,
        conversation_id: ConversationId,
        turn_id: TurnId,
        user_message: String,
    ) -> Result<(), FairyError>;

    async fn complete_turn(
        &self,
        conversation_id: ConversationId,
        turn_id: TurnId,
        assistant_message: String,
    ) -> Result<(), FairyError>;

    async fn terminate_turn(
        &self,
        conversation_id: ConversationId,
        turn_id: TurnId,
        state: TurnState,
        error: Option<FairyError>,
    ) -> Result<(), FairyError>;

    async fn retrieve(
        &self,
        character_id: CharacterId,
        query: String,
    ) -> Result<RetrievalContext, FairyError>;

    async fn pending_extraction_turn_count(
        &self,
        conversation_id: ConversationId,
    ) -> Result<u64, FairyError>;

    async fn claim_extraction_batch(
        &self,
        conversation_id: ConversationId,
        limit: usize,
    ) -> Result<Option<ExtractionBatchInput>, FairyError>;

    async fn commit_memory_mutations(
        &self,
        batch_id: ExtractionBatchId,
        character_id: CharacterId,
        allowed_memory_ids: Vec<PersonalMemoryId>,
        mutations: Vec<MemoryMutation>,
    ) -> Result<Vec<MemoryMutationResult>, FairyError>;

    async fn fail_extraction_batch(
        &self,
        batch_id: ExtractionBatchId,
        error: FairyError,
    ) -> Result<(), FairyError>;

    async fn retry_failed_extraction_batch(
        &self,
        batch_id: ExtractionBatchId,
    ) -> Result<ConversationId, FairyError>;

    async fn commit_prompt_window(
        &self,
        conversation_id: ConversationId,
        expected_revision: WindowRevision,
        summary: String,
    ) -> Result<PromptWindowRecord, FairyError>;
}

#[derive(Clone, Default)]
pub enum PersistenceBinding {
    #[default]
    Disabled,
    Available(Arc<dyn CompanionPersistence + Send + Sync>),
    Unavailable(FairyError),
}
