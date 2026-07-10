//! FAIRY 的纯领域契约。

#![forbid(unsafe_code)]

pub mod character;
pub mod conversation;
pub mod error;
pub mod event;
pub mod ids;
pub mod model;
pub mod prompt;
pub mod user_profile;

pub use character::{
    AttentionBias, CharacterBriefInput, CharacterCompiler, CharacterIdentity, CharacterSnapshot,
    EmotionalTendency, HardBoundary, RelationshipStance, ResponseDrive, SpeechStyle,
    SpeechStyleFallback, Worldview,
};
pub use conversation::{TurnLifecycle, TurnState};
pub use error::{ErrorCode, FairyError};
pub use event::{HarnessEvent, HarnessEventPayload};
pub use ids::{CharacterId, ConversationId, ModelConnectionId, Revision, TurnId, WindowRevision};
pub use model::{
    AuthMode, CachedTokenObservation, GatewayCapabilities, LaneModelUsage, ModelCompletion,
    ModelConnectionCompiler, ModelConnectionConfig, ModelConnectionInput, ModelProtocol,
    ModelStreamEvent, ModelUsage,
};
pub use prompt::{
    CompiledPromptRequest, DIALOGUE_POLICY_VERSION, DIALOGUE_PRIORITIES, DialoguePriority,
    ModelRequestShape, PromptItem, PromptLane, ReasoningMode, ResponseText, ToolPolicy,
};
pub use user_profile::{UserProfileCompiler, UserProfileInput, UserProfileSnapshot};
