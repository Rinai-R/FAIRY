//! FAIRY 陪伴会话 Harness Runtime。

#![forbid(unsafe_code)]

mod compaction;
mod gateway;
mod history;
mod intelligence;
mod prompt_compiler;
mod reply;
mod runtime;
mod search;
mod state;

pub use compaction::{
    CompactionCandidate, CompactionPolicy, CompactionResult, CompactionTrigger, install_compaction,
};
pub use gateway::{
    ContinuationDecision, ContinuationFullRequestReason, ContinuationState, ModelEventSink,
    ModelGateway, decide_continuation,
};
pub use history::{ConversationHistory, LaneHistory};
pub use intelligence::{CompanionIntelligence, IntelligenceBinding};
pub use prompt_compiler::PromptCompiler;
pub use reply::{BRIEF_OUTPUT_TOKENS, EXPANDED_OUTPUT_TOKENS, ReplyBudgetSelector, ReplyCompiler};
pub use runtime::HarnessRuntime;
pub use search::{WebSearchGateway, web_search_tool_definition};
pub use state::{HarnessEventSink, SessionSnapshot, TurnOutcome};
