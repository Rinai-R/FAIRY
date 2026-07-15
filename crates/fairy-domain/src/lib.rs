//! FAIRY 的纯领域契约。

#![forbid(unsafe_code)]

pub mod character;
pub mod character_visual;
pub mod conversation;
pub mod error;
pub mod event;
pub mod ids;
pub mod intelligence;
pub mod model;
pub mod prompt;
pub mod user_profile;

pub use character::{
    AttentionBias, CharacterBriefInput, CharacterCompiler, CharacterIdentity, CharacterSnapshot,
    EmotionalTendency, HardBoundary, RelationshipStance, ResponseDrive, SpeechStyle,
    SpeechStyleFallback, Worldview,
};
pub use character_visual::{
    CharacterVisualCompiler, FrameAnchor, FrameSize, VerifiedVisualPack, VisualPackId,
    VisualRenderer, VisualStateId, VisualStateImage,
};
pub use conversation::{
    ConversationBootstrap, ConversationMessageRecord, ConversationMessageRole, ConversationRecord,
    PersistedTurnRecord, PromptWindowRecord, TurnCompletion, TurnLifecycle, TurnState,
};
pub use error::{ErrorCode, FairyError};
pub use event::{HarnessEvent, HarnessEventPayload};
pub use ids::{
    CharacterId, ConversationId, ExtractionBatchId, KnowledgeId, KnowledgeSourceId, MessageId,
    ModelConnectionId, PersonalMemoryId, Revision, TurnId, WindowRevision,
};
pub use intelligence::{
    ExtractedKnowledge, ExtractedPersonalMemory, ExtractionBatchCatalog, ExtractionBatchInput,
    ExtractionBatchRecord, ExtractionBatchStatus, ExtractionOutput, ExtractionTurn,
    IntelligenceStoreSummary, KnowledgeCatalog, KnowledgeRecord, KnowledgeStatus,
    KnowledgeVerificationBasis, MemoryMutation, MemoryMutationOutput, MemoryMutationResult,
    MemoryScope, NewKnowledge, NewPersonalMemory, PersonalMemoryCatalog, PersonalMemoryKind,
    PersonalMemoryRecord, PersonalMemoryReviewStatus, PersonalMemoryStatus, RetrievalContext,
    RetrievedKnowledge, RetrievedPersonalMemory,
};
pub use model::{
    AuthMode, CachedTokenObservation, DEFAULT_MODEL_CONTEXT_WINDOW_TOKENS, GatewayCapabilities,
    LaneModelUsage, ModelCompletion, ModelConnectionCompiler, ModelConnectionConfig,
    ModelConnectionInput, ModelProtocol, ModelStreamEvent, ModelTurnOutput, ModelUsage,
};
pub use prompt::{
    AssistantSource, CapabilityState, CompanionCapability, CompiledPromptRequest, CompiledReply,
    CompiledReplyChain, ModelRequestShape, PromptItem, PromptLane, ReasoningMode, ResponseText,
    SpeechText, VisualStatePromptEntry,
};
pub use user_profile::{UserProfileCompiler, UserProfileInput, UserProfileSnapshot};
