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
    ModelConnectionCompiler, ModelConnectionConfig, ModelConnectionInput, ModelStreamEvent,
    ModelUsage,
};
pub use prompt::{
    AmbiguityHandling, CharacterPerspective, CompiledPromptRequest, ConversationGoal,
    DIALOGUE_POLICY_VERSION, DIALOGUE_PRIORITIES, DialoguePriority, EvidenceReference,
    FactCommitment, InteractionHypothesis, ModelOutputContract, ModelRequestShape, PromptItem,
    PromptLane, ReasoningMode, RelationshipIntent, ResponseAction, ResponseLength, ResponseText,
    ToolPolicy, TurnPlan, TurnPolicy,
};
pub use user_profile::{UserProfileCompiler, UserProfileInput, UserProfileSnapshot};
