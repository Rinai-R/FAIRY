//! FAIRY 陪伴会话 Harness Runtime。

#![forbid(unsafe_code)]

mod compaction;
mod gateway;
mod history;
mod policy;
mod prompt_compiler;
mod runtime;
mod state;

pub use compaction::{
    CompactionCandidate, CompactionPolicy, CompactionResult, CompactionTrigger, install_compaction,
};
pub use gateway::{
    ContinuationDecision, ContinuationFullRequestReason, ContinuationState, ModelEventSink,
    ModelGateway, decide_continuation,
};
pub use history::{ConversationHistory, LaneHistory};
pub use policy::{
    InterpretTurnRequest, InterpretationResult, ValidatedTurnPlan, interpret_turn,
    validate_turn_plan,
};
pub use prompt_compiler::PromptCompiler;
pub use runtime::HarnessRuntime;
pub use state::{HarnessEventSink, SessionSnapshot, TurnOutcome};
