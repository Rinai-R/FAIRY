use std::sync::Arc;

use async_trait::async_trait;
use fairy_domain::{
    ConversationId, ExtractionJobId, FairyError, NewKnowledge, NewPersonalMemory, RetrievalContext,
    TurnId,
};

#[async_trait]
pub trait CompanionIntelligence: Send + Sync {
    async fn retrieve(&self, query: String) -> Result<RetrievalContext, FairyError>;

    async fn create_extraction_job(
        &self,
        conversation_id: ConversationId,
        turn_id: TurnId,
    ) -> Result<ExtractionJobId, FairyError>;

    async fn mark_extraction_running(&self, job_id: ExtractionJobId) -> Result<(), FairyError>;

    async fn commit_extraction(
        &self,
        job_id: ExtractionJobId,
        personal_memories: Vec<NewPersonalMemory>,
        knowledge: Vec<NewKnowledge>,
    ) -> Result<(), FairyError>;

    async fn fail_extraction_job(
        &self,
        job_id: ExtractionJobId,
        error: FairyError,
    ) -> Result<(), FairyError>;
}

#[derive(Clone, Default)]
pub enum IntelligenceBinding {
    #[default]
    Disabled,
    Available(Arc<dyn CompanionIntelligence + Send + Sync>),
    Unavailable(FairyError),
}
