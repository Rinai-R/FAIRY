//! FAIRY 的纯领域契约。

#![forbid(unsafe_code)]

pub mod character;
pub mod conversation;
pub mod error;
pub mod event;
pub mod ids;
pub mod intelligence;
pub mod model;
pub mod prompt;
pub mod search;
pub mod user_profile;

pub use character::{
    AttentionBias, CharacterBriefInput, CharacterCompiler, CharacterIdentity, CharacterSnapshot,
    EmotionalTendency, HardBoundary, RelationshipStance, ResponseDrive, SpeechStyle,
    SpeechStyleFallback, Worldview,
};
pub use conversation::{TurnLifecycle, TurnState};
pub use error::{ErrorCode, FairyError};
pub use event::{HarnessEvent, HarnessEventPayload};
pub use ids::{
    CharacterId, ConversationId, ExtractionJobId, KnowledgeId, KnowledgeSourceId,
    ModelConnectionId, PersonalMemoryId, Revision, SearchConnectionId, TurnId, WindowRevision,
};
pub use intelligence::{
    ExtractedKnowledge, ExtractedPersonalMemory, ExtractionJobRecord, ExtractionJobStatus,
    ExtractionOutput, IntelligenceStoreSummary, KnowledgeCatalog, KnowledgeRecord, KnowledgeStatus,
    KnowledgeVerificationBasis, NewKnowledge, NewPersonalMemory, PersonalMemoryKind,
    PersonalMemoryRecord, PersonalMemoryStatus, RetrievalContext, RetrievedKnowledge,
    RetrievedPersonalMemory,
};
pub use model::{
    AuthMode, CachedTokenObservation, GatewayCapabilities, LaneModelUsage, ModelCompletion,
    ModelConnectionCompiler, ModelConnectionConfig, ModelConnectionInput, ModelProtocol,
    ModelStreamEvent, ModelTurnOutput, ModelUsage,
};
pub use prompt::{
    AssistantSource, CapabilityState, CompanionCapability, CompiledPromptRequest, CompiledReply,
    DIALOGUE_POLICY_VERSION, DIALOGUE_PRIORITIES, DialoguePriority, ModelRequestShape, PromptItem,
    PromptLane, ReasoningMode, ReplyMode, ResponseText, SpeechText, ToolCall, ToolDefinition,
    ToolName, ToolPolicy, ToolResult, ToolResultOutcome,
};
pub use search::{
    DEFAULT_BRAVE_SEARCH_ENDPOINT, SearchConnectionCompiler, SearchConnectionConfig,
    SearchConnectionInput, SearchProvider, WebSearchResponse,
};
pub use user_profile::{UserProfileCompiler, UserProfileInput, UserProfileSnapshot};
