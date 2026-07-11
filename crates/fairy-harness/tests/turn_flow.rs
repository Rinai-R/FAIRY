use std::collections::{HashMap, VecDeque};
use std::sync::{Arc, Mutex, MutexGuard};
use std::time::Duration;

use async_trait::async_trait;
use fairy_domain::{
    CachedTokenObservation, CharacterBriefInput, CharacterCompiler, CharacterId,
    CompiledPromptRequest, ConversationBootstrap, ConversationId, ConversationMessageRecord,
    ConversationMessageRole, ConversationRecord, ErrorCode, FairyError, GatewayCapabilities,
    HarnessEvent, HarnessEventPayload, MessageId, ModelCompletion, ModelStreamEvent,
    ModelTurnOutput, ModelUsage, PersonalMemoryId, PersonalMemoryKind, PromptItem, PromptLane,
    PromptWindowRecord, RetrievalContext, RetrievedPersonalMemory, Revision, TurnId, TurnState,
    UserProfileCompiler, UserProfileInput, UserProfileSnapshot, WindowRevision,
};
use fairy_harness::{
    CompanionPersistence, HarnessEventSink, HarnessRuntime, ModelEventSink, ModelGateway,
    PersistenceBinding,
};
use tokio::sync::Notify;
use tokio_util::sync::CancellationToken;

enum FakeBehavior {
    Complete {
        output: String,
        deltas: Vec<String>,
    },
    CompleteWithInputTokens {
        output: String,
        deltas: Vec<String>,
        input_tokens: u64,
    },
    WaitAfterTextDelta {
        delta: String,
    },
    FailAfterTextDelta {
        delta: String,
    },
}

struct FakeGateway {
    behaviors: Mutex<VecDeque<FakeBehavior>>,
    requests: Mutex<Vec<CompiledPromptRequest>>,
    first_text_delta: Notify,
}

impl FakeGateway {
    fn new(behaviors: Vec<FakeBehavior>) -> Self {
        Self {
            behaviors: Mutex::new(behaviors.into()),
            requests: Mutex::new(Vec::new()),
            first_text_delta: Notify::new(),
        }
    }

    fn requests(&self) -> Vec<CompiledPromptRequest> {
        lock(&self.requests).clone()
    }
}

#[async_trait]
impl ModelGateway for FakeGateway {
    fn capabilities(&self) -> GatewayCapabilities {
        GatewayCapabilities::responses_http(true, true)
    }

    async fn execute(
        &self,
        request: CompiledPromptRequest,
        cancellation: CancellationToken,
        sink: &mut (dyn ModelEventSink + Send),
    ) -> Result<ModelCompletion, FairyError> {
        lock(&self.requests).push(request.clone());
        let behavior = lock(&self.behaviors)
            .pop_front()
            .expect("fake behavior for every request");
        match behavior {
            FakeBehavior::Complete { output, deltas } => {
                for delta in deltas {
                    sink.send(ModelStreamEvent::TextDelta { delta })?;
                }
                Ok(completion(output))
            }
            FakeBehavior::CompleteWithInputTokens {
                output,
                deltas,
                input_tokens,
            } => {
                for delta in deltas {
                    sink.send(ModelStreamEvent::TextDelta { delta })?;
                }
                let mut completion = completion(output);
                completion.usage.input_tokens = Some(input_tokens);
                Ok(completion)
            }
            FakeBehavior::WaitAfterTextDelta { delta } => {
                sink.send(ModelStreamEvent::TextDelta { delta })?;
                self.first_text_delta.notify_waiters();
                cancellation.cancelled().await;
                Err(FairyError::new(
                    ErrorCode::TurnInterrupted,
                    "fake cancelled",
                    false,
                ))
            }
            FakeBehavior::FailAfterTextDelta { delta } => {
                sink.send(ModelStreamEvent::TextDelta { delta })?;
                Err(FairyError::new(
                    ErrorCode::ModelStreamFailed,
                    "fake stream failed",
                    true,
                ))
            }
        }
    }
}

fn completion(output: String) -> ModelCompletion {
    ModelCompletion {
        response_id: Some("fake-response".to_owned()),
        output: ModelTurnOutput::Text {
            text: output.clone(),
        },
        response_items: vec![PromptItem::AssistantMessage {
            content: output.clone(),
        }],
        usage: ModelUsage {
            input_tokens: Some(20),
            output_tokens: Some(8),
            cached_input_tokens: CachedTokenObservation::Observed(4),
            cache_write_tokens: CachedTokenObservation::Missing,
        },
    }
}

struct FakeIntelligence {
    results: Mutex<VecDeque<Result<RetrievalContext, FairyError>>>,
    queries: Mutex<Vec<(CharacterId, String)>>,
    pending_extraction_turns: u64,
    batch_claimed: Mutex<bool>,
    committed: Mutex<Vec<Vec<fairy_domain::MemoryMutation>>>,
    failed: Mutex<Vec<FairyError>>,
}

impl FakeIntelligence {
    fn new(results: Vec<Result<RetrievalContext, FairyError>>) -> Self {
        Self {
            results: Mutex::new(results.into()),
            queries: Mutex::new(Vec::new()),
            pending_extraction_turns: 0,
            batch_claimed: Mutex::new(false),
            committed: Mutex::new(Vec::new()),
            failed: Mutex::new(Vec::new()),
        }
    }

    fn with_extraction(results: Vec<Result<RetrievalContext, FairyError>>) -> Self {
        Self {
            pending_extraction_turns: 6,
            ..Self::new(results)
        }
    }

    fn with_idle_extraction(results: Vec<Result<RetrievalContext, FairyError>>) -> Self {
        Self {
            pending_extraction_turns: 1,
            ..Self::new(results)
        }
    }

    fn committed(&self) -> Vec<Vec<fairy_domain::MemoryMutation>> {
        lock(&self.committed).clone()
    }

    fn failed(&self) -> Vec<FairyError> {
        lock(&self.failed).clone()
    }

    fn queries(&self) -> Vec<(CharacterId, String)> {
        lock(&self.queries).clone()
    }
}

#[async_trait]
impl CompanionPersistence for FakeIntelligence {
    async fn open_or_create_character_conversation(
        &self,
        character_id: CharacterId,
    ) -> Result<ConversationBootstrap, FairyError> {
        let conversation_id = ConversationId::new();
        Ok(ConversationBootstrap {
            conversation: ConversationRecord {
                id: conversation_id,
                character_id,
                created_at_unix_ms: 1,
                updated_at_unix_ms: 1,
            },
            messages: Vec::new(),
            prompt_window: PromptWindowRecord {
                conversation_id,
                revision: WindowRevision::INITIAL,
                summary: None,
                cutoff_message_sequence: 0,
                updated_at_unix_ms: 1,
            },
        })
    }

    async fn begin_turn(
        &self,
        _conversation_id: ConversationId,
        _turn_id: TurnId,
        _user_message: String,
    ) -> Result<(), FairyError> {
        Ok(())
    }

    async fn complete_turn(
        &self,
        _conversation_id: ConversationId,
        _turn_id: TurnId,
        _assistant_message: String,
    ) -> Result<(), FairyError> {
        Ok(())
    }

    async fn terminate_turn(
        &self,
        _conversation_id: ConversationId,
        _turn_id: TurnId,
        _state: TurnState,
        _error: Option<FairyError>,
    ) -> Result<(), FairyError> {
        Ok(())
    }

    async fn retrieve(
        &self,
        character_id: CharacterId,
        query: String,
    ) -> Result<RetrievalContext, FairyError> {
        lock(&self.queries).push((character_id, query));
        lock(&self.results)
            .pop_front()
            .expect("fake intelligence result for every turn")
    }

    async fn pending_extraction_turn_count(
        &self,
        _conversation_id: ConversationId,
    ) -> Result<u64, FairyError> {
        Ok(self.pending_extraction_turns)
    }

    async fn claim_extraction_batch(
        &self,
        conversation_id: ConversationId,
        _limit: usize,
    ) -> Result<Option<fairy_domain::ExtractionBatchInput>, FairyError> {
        if self.pending_extraction_turns == 0 || *lock(&self.batch_claimed) {
            return Ok(None);
        }
        *lock(&self.batch_claimed) = true;
        let character_id = lock(&self.queries)
            .last()
            .map(|(character_id, _)| *character_id)
            .expect("retrieval records active character before extraction");
        Ok(Some(fairy_domain::ExtractionBatchInput {
            batch_id: fairy_domain::ExtractionBatchId::new(),
            conversation_id,
            character_id,
            turns: vec![fairy_domain::ExtractionTurn {
                turn_id: TurnId::new(),
                user_message: "记住我喜欢 Rust".to_owned(),
                assistant_message: "好，我记住了。".to_owned(),
            }],
            existing_memories: Vec::new(),
        }))
    }

    async fn commit_memory_mutations(
        &self,
        _batch_id: fairy_domain::ExtractionBatchId,
        _character_id: CharacterId,
        _allowed_memory_ids: Vec<PersonalMemoryId>,
        mutations: Vec<fairy_domain::MemoryMutation>,
    ) -> Result<Vec<fairy_domain::MemoryMutationResult>, FairyError> {
        lock(&self.committed).push(mutations);
        Ok(Vec::new())
    }

    async fn fail_extraction_batch(
        &self,
        _batch_id: fairy_domain::ExtractionBatchId,
        error: FairyError,
    ) -> Result<(), FairyError> {
        lock(&self.failed).push(error);
        Ok(())
    }

    async fn retry_failed_extraction_batch(
        &self,
        _batch_id: fairy_domain::ExtractionBatchId,
    ) -> Result<ConversationId, FairyError> {
        Err(FairyError::new(
            ErrorCode::InvalidIntelligenceRecord,
            "fake batch is not failed",
            false,
        ))
    }

    async fn commit_prompt_window(
        &self,
        _conversation_id: ConversationId,
        _expected_revision: WindowRevision,
        _summary: String,
    ) -> Result<PromptWindowRecord, FairyError> {
        Err(FairyError::new(
            ErrorCode::CompactionFailed,
            "fake prompt window unavailable",
            false,
        ))
    }
}

#[derive(Default)]
struct PersistentState {
    conversations: HashMap<CharacterId, ConversationBootstrap>,
    actions: Vec<&'static str>,
    terminations: Vec<TurnState>,
    fail_complete: bool,
    fail_prompt_window: bool,
}

#[derive(Clone, Default)]
struct ReopenablePersistence {
    state: Arc<Mutex<PersistentState>>,
}

impl ReopenablePersistence {
    fn fail_complete(&self) {
        lock(&self.state).fail_complete = true;
    }

    fn fail_prompt_window(&self) {
        lock(&self.state).fail_prompt_window = true;
    }

    fn actions(&self) -> Vec<&'static str> {
        lock(&self.state).actions.clone()
    }

    fn terminations(&self) -> Vec<TurnState> {
        lock(&self.state).terminations.clone()
    }

    fn bootstrap_for(&self, character_id: CharacterId) -> ConversationBootstrap {
        lock(&self.state)
            .conversations
            .get(&character_id)
            .cloned()
            .expect("persisted character conversation")
    }
}

#[async_trait]
impl CompanionPersistence for ReopenablePersistence {
    async fn open_or_create_character_conversation(
        &self,
        character_id: CharacterId,
    ) -> Result<ConversationBootstrap, FairyError> {
        let mut state = lock(&self.state);
        state.actions.push("open");
        Ok(state
            .conversations
            .entry(character_id)
            .or_insert_with(|| {
                let conversation_id = ConversationId::new();
                ConversationBootstrap {
                    conversation: ConversationRecord {
                        id: conversation_id,
                        character_id,
                        created_at_unix_ms: 1,
                        updated_at_unix_ms: 1,
                    },
                    messages: Vec::new(),
                    prompt_window: PromptWindowRecord {
                        conversation_id,
                        revision: WindowRevision::INITIAL,
                        summary: None,
                        cutoff_message_sequence: 0,
                        updated_at_unix_ms: 1,
                    },
                }
            })
            .clone())
    }

    async fn begin_turn(
        &self,
        conversation_id: ConversationId,
        turn_id: TurnId,
        user_message: String,
    ) -> Result<(), FairyError> {
        let mut state = lock(&self.state);
        state.actions.push("begin");
        let bootstrap = state
            .conversations
            .values_mut()
            .find(|bootstrap| bootstrap.conversation.id == conversation_id)
            .ok_or_else(|| {
                FairyError::new(ErrorCode::ConversationNotFound, "fake missing", false)
            })?;
        let sequence = bootstrap.messages.len() as u64 + 1;
        bootstrap.messages.push(ConversationMessageRecord {
            id: MessageId::new(),
            conversation_id,
            turn_id,
            sequence,
            role: ConversationMessageRole::User,
            content: user_message,
            created_at_unix_ms: sequence as i64,
        });
        Ok(())
    }

    async fn complete_turn(
        &self,
        conversation_id: ConversationId,
        turn_id: TurnId,
        assistant_message: String,
    ) -> Result<(), FairyError> {
        let mut state = lock(&self.state);
        state.actions.push("complete");
        if state.fail_complete {
            return Err(FairyError::new(
                ErrorCode::StorageIo,
                "fake assistant commit failed",
                false,
            ));
        }
        let bootstrap = state
            .conversations
            .values_mut()
            .find(|bootstrap| bootstrap.conversation.id == conversation_id)
            .ok_or_else(|| {
                FairyError::new(ErrorCode::ConversationNotFound, "fake missing", false)
            })?;
        let sequence = bootstrap.messages.len() as u64 + 1;
        bootstrap.messages.push(ConversationMessageRecord {
            id: MessageId::new(),
            conversation_id,
            turn_id,
            sequence,
            role: ConversationMessageRole::Assistant,
            content: assistant_message,
            created_at_unix_ms: sequence as i64,
        });
        Ok(())
    }

    async fn terminate_turn(
        &self,
        _conversation_id: ConversationId,
        _turn_id: TurnId,
        state: TurnState,
        _error: Option<FairyError>,
    ) -> Result<(), FairyError> {
        let mut persisted = lock(&self.state);
        persisted.actions.push("terminate");
        persisted.terminations.push(state);
        Ok(())
    }

    async fn retrieve(
        &self,
        _character_id: CharacterId,
        _query: String,
    ) -> Result<RetrievalContext, FairyError> {
        Ok(RetrievalContext::default())
    }

    async fn pending_extraction_turn_count(
        &self,
        _conversation_id: ConversationId,
    ) -> Result<u64, FairyError> {
        Ok(0)
    }

    async fn claim_extraction_batch(
        &self,
        _conversation_id: ConversationId,
        _limit: usize,
    ) -> Result<Option<fairy_domain::ExtractionBatchInput>, FairyError> {
        Ok(None)
    }

    async fn commit_memory_mutations(
        &self,
        _batch_id: fairy_domain::ExtractionBatchId,
        _character_id: CharacterId,
        _allowed_memory_ids: Vec<PersonalMemoryId>,
        _mutations: Vec<fairy_domain::MemoryMutation>,
    ) -> Result<Vec<fairy_domain::MemoryMutationResult>, FairyError> {
        Ok(Vec::new())
    }

    async fn fail_extraction_batch(
        &self,
        _batch_id: fairy_domain::ExtractionBatchId,
        _error: FairyError,
    ) -> Result<(), FairyError> {
        Ok(())
    }

    async fn retry_failed_extraction_batch(
        &self,
        _batch_id: fairy_domain::ExtractionBatchId,
    ) -> Result<ConversationId, FairyError> {
        Err(FairyError::new(
            ErrorCode::InvalidIntelligenceRecord,
            "fake batch is not failed",
            false,
        ))
    }

    async fn commit_prompt_window(
        &self,
        conversation_id: ConversationId,
        expected_revision: WindowRevision,
        summary: String,
    ) -> Result<PromptWindowRecord, FairyError> {
        let mut state = lock(&self.state);
        if state.fail_prompt_window {
            return Err(FairyError::new(
                ErrorCode::StorageIo,
                "fake prompt window commit failed",
                false,
            ));
        }
        let bootstrap = state
            .conversations
            .values_mut()
            .find(|bootstrap| bootstrap.conversation.id == conversation_id)
            .ok_or_else(|| {
                FairyError::new(ErrorCode::ConversationNotFound, "fake missing", false)
            })?;
        if bootstrap.prompt_window.revision != expected_revision {
            return Err(FairyError::new(
                ErrorCode::CompactionFailed,
                "fake revision mismatch",
                false,
            ));
        }
        let revision = expected_revision
            .checked_next()
            .expect("next fake revision");
        bootstrap.prompt_window.revision = revision;
        bootstrap.prompt_window.summary = Some(summary);
        bootstrap.prompt_window.cutoff_message_sequence = bootstrap
            .messages
            .last()
            .map_or(0, |message| message.sequence);
        Ok(bootstrap.prompt_window.clone())
    }
}

async fn wait_for_background(runtime: &HarnessRuntime) {
    tokio::time::timeout(Duration::from_secs(2), async {
        while runtime.active_background_jobs() > 0 {
            tokio::task::yield_now().await;
        }
    })
    .await
    .expect("background extraction completes");
}

#[derive(Clone, Default)]
struct RecordingSink {
    events: Arc<Mutex<Vec<HarnessEvent>>>,
}

impl RecordingSink {
    fn events(&self) -> Vec<HarnessEvent> {
        lock(&self.events).clone()
    }
}

impl HarnessEventSink for RecordingSink {
    fn send(&mut self, event: HarnessEvent) -> Result<(), FairyError> {
        lock(&self.events).push(event);
        Ok(())
    }
}

fn character(revision: Revision, description: &str) -> fairy_domain::CharacterSnapshot {
    character_for(CharacterId::new(), revision, description)
}

fn character_for(
    character_id: CharacterId,
    revision: Revision,
    description: &str,
) -> fairy_domain::CharacterSnapshot {
    CharacterCompiler
        .compile(
            character_id,
            revision,
            CharacterBriefInput {
                name: "亚托莉".to_owned(),
                description: description.to_owned(),
            },
        )
        .expect("compile test character")
}

fn profile(revision: Revision, name: &str) -> UserProfileSnapshot {
    UserProfileCompiler
        .compile(
            revision,
            UserProfileInput {
                preferred_name: Some(name.to_owned()),
            },
        )
        .expect("compile test profile")
}

fn profile_without_name(revision: Revision) -> UserProfileSnapshot {
    UserProfileCompiler
        .compile(
            revision,
            UserProfileInput {
                preferred_name: None,
            },
        )
        .expect("compile cleared test profile")
}

fn response_behavior(output: &str, deltas: &[&str]) -> FakeBehavior {
    FakeBehavior::Complete {
        output: output.to_owned(),
        deltas: deltas.iter().map(|value| (*value).to_owned()).collect(),
    }
}

fn setup(
    gateway: Arc<FakeGateway>,
) -> (
    Arc<HarnessRuntime>,
    fairy_domain::ConversationId,
    fairy_domain::CharacterSnapshot,
) {
    let runtime = Arc::new(
        HarnessRuntime::new("test-model".to_owned(), gateway).expect("create test runtime"),
    );
    let session = runtime.create_session();
    let role = character(Revision::INITIAL, "自然回应用户。");
    runtime
        .activate_character(session.conversation_id, role.clone())
        .expect("activate test role");
    runtime
        .update_user_profile(session.conversation_id, profile(Revision::INITIAL, "Rinai"))
        .expect("set test profile");
    (runtime, session.conversation_id, role)
}

#[tokio::test]
async fn persistent_character_session_restores_after_runtime_restart_and_isolates_roles() {
    let persistence = Arc::new(ReopenablePersistence::default());
    let character_a_id = CharacterId::new();
    let character_b_id = CharacterId::new();
    let character_a = character_for(character_a_id, Revision::INITIAL, "会自然接住用户的话。");
    let character_b = character_for(character_b_id, Revision::INITIAL, "说话更简短。");
    let first_gateway = Arc::new(FakeGateway::new(vec![response_behavior(
        "嗯，我记得了。",
        &["嗯，我记得了。"],
    )]));
    let first_runtime =
        HarnessRuntime::new("first-model".to_owned(), first_gateway).expect("create first runtime");
    first_runtime.replace_persistence_binding(PersistenceBinding::Available(persistence.clone()));
    let first_bootstrap = first_runtime
        .open_or_create_character_session(
            character_a.clone(),
            Some(profile(Revision::INITIAL, "Rinai")),
        )
        .await
        .expect("open first character session");
    first_runtime
        .submit_turn(
            first_bootstrap.conversation.id,
            "我今天买了红茶".to_owned(),
            false,
            &mut RecordingSink::default(),
        )
        .await
        .expect("persist first turn");

    let second_gateway = Arc::new(FakeGateway::new(vec![response_behavior(
        "当然，刚才说的是红茶。",
        &["当然，刚才说的是红茶。"],
    )]));
    let second_runtime = HarnessRuntime::new("second-model".to_owned(), second_gateway.clone())
        .expect("create restarted runtime");
    second_runtime.replace_persistence_binding(PersistenceBinding::Available(persistence.clone()));
    let restored_a = second_runtime
        .open_or_create_character_session(
            character_a.clone(),
            Some(profile(Revision::new(2).expect("revision two"), "小凛")),
        )
        .await
        .expect("restore character a");
    assert_eq!(restored_a.conversation.id, first_bootstrap.conversation.id);
    assert_eq!(restored_a.messages.len(), 2);
    second_runtime
        .submit_turn(
            restored_a.conversation.id,
            "我刚才买了什么？".to_owned(),
            false,
            &mut RecordingSink::default(),
        )
        .await
        .expect("respond with restored history");
    let request = &second_gateway.requests()[0];
    let restored_user = request.input.iter().position(
        |item| matches!(item, PromptItem::UserMessage { content } if content == "我今天买了红茶"),
    );
    let restored_assistant = request.input.iter().position(|item| {
        matches!(item, PromptItem::AssistantMessage { content } if content == "嗯，我记得了。")
    });
    let current_user = request.input.iter().position(
        |item| matches!(item, PromptItem::UserMessage { content } if content == "我刚才买了什么？"),
    );
    assert!(restored_user < restored_assistant && restored_assistant < current_user);

    let restored_b = second_runtime
        .open_or_create_character_session(character_b, None)
        .await
        .expect("open isolated character b");
    assert_ne!(restored_b.conversation.id, restored_a.conversation.id);
    assert!(restored_b.messages.is_empty());

    let restored_a_again = second_runtime
        .open_or_create_character_session(character_a, None)
        .await
        .expect("switch back to character a");
    assert_eq!(restored_a_again.conversation.id, restored_a.conversation.id);
    assert_eq!(restored_a_again.messages.len(), 4);
}

#[tokio::test]
async fn completed_turn_at_token_threshold_persists_compaction_before_memory_window_changes() {
    let persistence = Arc::new(ReopenablePersistence::default());
    let role = character(Revision::INITIAL, "自然回应。");
    let character_id = role.character_id();
    let gateway = Arc::new(FakeGateway::new(vec![
        FakeBehavior::CompleteWithInputTokens {
            output: "这一轮很长。".to_owned(),
            deltas: vec!["这一轮很长。".to_owned()],
            input_tokens: 32_000,
        },
        response_behavior("用户完成了一轮长对话。", &["用户完成了一轮长对话。"]),
    ]));
    let runtime = HarnessRuntime::new("test-model".to_owned(), gateway).expect("create runtime");
    runtime.replace_persistence_binding(PersistenceBinding::Available(persistence.clone()));
    let bootstrap = runtime
        .open_or_create_character_session(role.clone(), None)
        .await
        .expect("open session");

    runtime
        .submit_turn(
            bootstrap.conversation.id,
            "制造压缩阈值".to_owned(),
            false,
            &mut RecordingSink::default(),
        )
        .await
        .expect("complete threshold turn");
    wait_for_background(&runtime).await;
    let compacted = persistence.bootstrap_for(character_id);
    assert_eq!(compacted.prompt_window.revision.get(), 2);
    assert_eq!(
        compacted.prompt_window.summary.as_deref(),
        Some("用户完成了一轮长对话。")
    );
    assert_eq!(compacted.prompt_window.cutoff_message_sequence, 2);

    let restarted_gateway = Arc::new(FakeGateway::new(vec![response_behavior(
        "已经从压缩窗口恢复。",
        &["已经从压缩窗口恢复。"],
    )]));
    let restarted = HarnessRuntime::new("next-model".to_owned(), restarted_gateway.clone())
        .expect("create restarted runtime");
    restarted.replace_persistence_binding(PersistenceBinding::Available(persistence));
    let restored = restarted
        .open_or_create_character_session(role, None)
        .await
        .expect("restore compacted session");
    restarted
        .submit_turn(
            restored.conversation.id,
            "继续".to_owned(),
            false,
            &mut RecordingSink::default(),
        )
        .await
        .expect("respond after compacted restart");
    assert!(restarted_gateway.requests()[0].input.iter().any(|item| {
        matches!(item, PromptItem::CompactionSummary { summary } if summary == "用户完成了一轮长对话。")
    }));
}

#[tokio::test]
async fn prompt_window_commit_failure_keeps_persisted_and_in_memory_windows_unchanged() {
    let persistence = Arc::new(ReopenablePersistence::default());
    persistence.fail_prompt_window();
    let role = character(Revision::INITIAL, "自然回应。");
    let character_id = role.character_id();
    let gateway = Arc::new(FakeGateway::new(vec![
        FakeBehavior::CompleteWithInputTokens {
            output: "第一轮回复。".to_owned(),
            deltas: vec!["第一轮回复。".to_owned()],
            input_tokens: 32_000,
        },
        response_behavior("不会被持久化的摘要", &["不会被持久化的摘要"]),
        response_behavior("旧窗口仍可继续。", &["旧窗口仍可继续。"]),
    ]));
    let runtime =
        HarnessRuntime::new("test-model".to_owned(), gateway.clone()).expect("create runtime");
    runtime.replace_persistence_binding(PersistenceBinding::Available(persistence.clone()));
    let bootstrap = runtime
        .open_or_create_character_session(role, None)
        .await
        .expect("open session");
    runtime
        .submit_turn(
            bootstrap.conversation.id,
            "第一轮用户消息".to_owned(),
            false,
            &mut RecordingSink::default(),
        )
        .await
        .expect("complete visible turn");
    wait_for_background(&runtime).await;

    assert_eq!(
        persistence
            .bootstrap_for(character_id)
            .prompt_window
            .revision,
        WindowRevision::INITIAL
    );
    assert_eq!(
        runtime
            .last_intelligence_background_error()
            .expect("compaction persistence diagnostic")
            .code,
        ErrorCode::StorageIo
    );
    runtime
        .submit_turn(
            bootstrap.conversation.id,
            "第二轮".to_owned(),
            false,
            &mut RecordingSink::default(),
        )
        .await
        .expect("continue with old window");
    let second_respond = &gateway.requests()[2];
    assert!(second_respond.input.iter().any(|item| {
        matches!(item, PromptItem::UserMessage { content } if content == "第一轮用户消息")
    }));
    assert!(
        second_respond
            .input
            .iter()
            .all(|item| { !matches!(item, PromptItem::CompactionSummary { .. }) })
    );
}

#[tokio::test]
async fn assistant_persistence_failure_emits_no_text_or_completed_success() {
    let persistence = Arc::new(ReopenablePersistence::default());
    persistence.fail_complete();
    let gateway = Arc::new(FakeGateway::new(vec![response_behavior(
        "这条回复不能伪装成成功。",
        &["这条回复不能伪装成成功。"],
    )]));
    let runtime = HarnessRuntime::new("test-model".to_owned(), gateway).expect("create runtime");
    runtime.replace_persistence_binding(PersistenceBinding::Available(persistence.clone()));
    let role = character(Revision::INITIAL, "自然回应。");
    let bootstrap = runtime
        .open_or_create_character_session(role, None)
        .await
        .expect("open session");
    let mut sink = RecordingSink::default();

    let error = runtime
        .submit_turn(
            bootstrap.conversation.id,
            "测试写入失败".to_owned(),
            true,
            &mut sink,
        )
        .await
        .expect_err("assistant persistence failure must fail turn");

    assert_eq!(error.code, ErrorCode::StorageIo);
    assert_eq!(
        persistence.actions(),
        vec!["open", "begin", "complete", "terminate"]
    );
    assert_eq!(
        persistence
            .bootstrap_for(bootstrap.conversation.character_id)
            .messages
            .len(),
        1
    );
    assert!(sink.events().iter().all(|event| !matches!(
        event.payload,
        HarnessEventPayload::TextDelta { .. }
            | HarnessEventPayload::Completed { .. }
            | HarnessEventPayload::SpeechRequested { .. }
    )));
    assert_eq!(
        sink.events().last().map(|event| event.state),
        Some(TurnState::Failed)
    );
}

#[tokio::test]
async fn model_failure_persists_failed_turn_and_keeps_only_the_user_message() {
    let persistence = Arc::new(ReopenablePersistence::default());
    let gateway = Arc::new(FakeGateway::new(vec![FakeBehavior::FailAfterTextDelta {
        delta: "未完成".to_owned(),
    }]));
    let runtime = HarnessRuntime::new("test-model".to_owned(), gateway).expect("create runtime");
    runtime.replace_persistence_binding(PersistenceBinding::Available(persistence.clone()));
    let role = character(Revision::INITIAL, "自然回应。");
    let character_id = role.character_id();
    let bootstrap = runtime
        .open_or_create_character_session(role, None)
        .await
        .expect("open session");

    let error = runtime
        .submit_turn(
            bootstrap.conversation.id,
            "模型会失败".to_owned(),
            false,
            &mut RecordingSink::default(),
        )
        .await
        .expect_err("model failure remains explicit");

    assert_eq!(error.code, ErrorCode::ModelStreamFailed);
    assert_eq!(persistence.terminations(), vec![TurnState::Failed]);
    let restored = persistence.bootstrap_for(character_id);
    assert_eq!(restored.messages.len(), 1);
    assert_eq!(restored.messages[0].role, ConversationMessageRole::User);
    assert_eq!(restored.messages[0].content, "模型会失败");
}

#[tokio::test]
async fn cancellation_persists_interrupted_turn_without_assistant_message() {
    let persistence = Arc::new(ReopenablePersistence::default());
    let gateway = Arc::new(FakeGateway::new(vec![FakeBehavior::WaitAfterTextDelta {
        delta: "未完成".to_owned(),
    }]));
    let runtime = Arc::new(
        HarnessRuntime::new("test-model".to_owned(), gateway.clone()).expect("create runtime"),
    );
    runtime.replace_persistence_binding(PersistenceBinding::Available(persistence.clone()));
    let role = character(Revision::INITIAL, "自然回应。");
    let character_id = role.character_id();
    let bootstrap = runtime
        .open_or_create_character_session(role, None)
        .await
        .expect("open session");
    let conversation_id = bootstrap.conversation.id;
    let sink = RecordingSink::default();
    let first_delta = gateway.first_text_delta.notified();
    let task_runtime = runtime.clone();
    let task_sink = sink.clone();
    let turn = tokio::spawn(async move {
        let mut task_sink = task_sink;
        task_runtime
            .submit_turn(
                conversation_id,
                "取消这一轮".to_owned(),
                false,
                &mut task_sink,
            )
            .await
    });
    first_delta.await;
    let turn_id = sink.events()[0].turn_id;
    runtime.cancel_turn(turn_id).expect("cancel persisted turn");
    let error = turn
        .await
        .expect("join turn")
        .expect_err("cancelled turn fails");

    assert_eq!(error.code, ErrorCode::TurnInterrupted);
    assert_eq!(persistence.terminations(), vec![TurnState::Interrupted]);
    assert_eq!(persistence.bootstrap_for(character_id).messages.len(), 1);
}

#[tokio::test]
async fn normal_turn_has_ordered_states_deltas_usage_and_single_terminal() {
    let gateway = Arc::new(FakeGateway::new(vec![response_behavior(
        "你好呀。",
        &["你好", "呀。"],
    )]));
    let (runtime, conversation_id, _) = setup(gateway.clone());
    let mut sink = RecordingSink::default();

    let outcome = runtime
        .submit_turn(conversation_id, "你好".to_owned(), true, &mut sink)
        .await
        .expect("complete normal turn");
    let events = sink.events();

    assert_eq!(outcome.response_text.as_str(), "你好呀。");
    assert_eq!(outcome.usage.len(), 1);
    assert_eq!(
        events
            .iter()
            .map(|event| event.sequence)
            .collect::<Vec<_>>(),
        vec![1, 2, 3, 4, 5, 6]
    );
    assert_eq!(
        events.iter().map(|event| event.state).collect::<Vec<_>>(),
        vec![
            TurnState::Interpreting,
            TurnState::Planning,
            TurnState::Responding,
            TurnState::Responding,
            TurnState::Completed,
            TurnState::Completed,
        ]
    );
    let deltas = events
        .iter()
        .filter_map(|event| match &event.payload {
            HarnessEventPayload::TextDelta { delta } => Some(delta.as_str()),
            _ => None,
        })
        .collect::<Vec<_>>();
    assert_eq!(deltas, vec!["你好呀。"]);
    assert_eq!(
        events
            .iter()
            .filter(|event| matches!(event.payload, HarnessEventPayload::Completed { .. }))
            .count(),
        1
    );
    let completed = events
        .iter()
        .find_map(|event| match &event.payload {
            HarnessEventPayload::Completed {
                text,
                character_revision,
                user_profile_revision,
                ..
            } => Some((text, character_revision, user_profile_revision)),
            _ => None,
        })
        .expect("completed payload");
    let speech = events
        .iter()
        .find_map(|event| match &event.payload {
            HarnessEventPayload::SpeechRequested {
                text,
                character_revision,
                user_profile_revision,
            } => Some((text, character_revision, user_profile_revision)),
            _ => None,
        })
        .expect("speech requested payload");
    assert_eq!(completed.0.as_str(), speech.0.as_str());
    assert_eq!(completed.1, speech.1);
    assert_eq!(completed.2, speech.2);
    assert_eq!(completed.0.as_str(), outcome.response_text.as_str());
    assert!(outcome.speech_requested);
    assert_eq!(
        runtime
            .session_snapshot(conversation_id)
            .expect("read idle session")
            .state,
        TurnState::Idle
    );
    assert_eq!(
        gateway
            .requests()
            .iter()
            .map(|request| request.shape.lane)
            .collect::<Vec<_>>(),
        vec![PromptLane::Respond]
    );
    assert!(
        gateway.requests()[0]
            .shape
            .instructions
            .contains("期待怎样继续这段对话")
    );
    assert_eq!(
        runtime
            .cancel_turn(outcome.turn_id)
            .expect_err("completed turn is not active")
            .code,
        ErrorCode::TurnNotActive
    );
}

#[tokio::test]
async fn cancellation_after_partial_delta_is_interrupted_and_next_turn_can_start() {
    let gateway = Arc::new(FakeGateway::new(vec![
        FakeBehavior::WaitAfterTextDelta {
            delta: "部分".to_owned(),
        },
        response_behavior("第二轮完成", &["第二轮完成"]),
    ]));
    let (runtime, conversation_id, _) = setup(gateway.clone());
    let sink = RecordingSink::default();
    let first_delta = gateway.first_text_delta.notified();
    let runtime_task = runtime.clone();
    let sink_task = sink.clone();
    let first = tokio::spawn(async move {
        let mut task_sink = sink_task;
        runtime_task
            .submit_turn(conversation_id, "第一轮".to_owned(), false, &mut task_sink)
            .await
    });
    first_delta.await;
    let turn_id = sink.events()[0].turn_id;

    runtime.cancel_turn(turn_id).expect("cancel active turn");
    let error = first
        .await
        .expect("join first turn")
        .expect_err("cancelled turn must fail with interrupted code");
    assert_eq!(error.code, ErrorCode::TurnInterrupted);
    let first_events = sink.events();
    assert_eq!(
        first_events.last().expect("terminal event").state,
        TurnState::Interrupted
    );
    let interrupted_index = first_events
        .iter()
        .position(|event| event.state == TurnState::Interrupted)
        .expect("interrupted event");
    assert!(first_events[interrupted_index + 1..].is_empty());

    let mut next_sink = RecordingSink::default();
    let next = runtime
        .submit_turn(conversation_id, "第二轮".to_owned(), false, &mut next_sink)
        .await
        .expect("next turn starts after cancellation");
    assert_eq!(next.response_text.as_str(), "第二轮完成");
}

#[tokio::test]
async fn replacing_gateway_keeps_active_turn_snapshot_and_affects_only_next_turn() {
    let original = Arc::new(FakeGateway::new(vec![FakeBehavior::WaitAfterTextDelta {
        delta: "旧连接仍在输出".to_owned(),
    }]));
    let replacement = Arc::new(FakeGateway::new(vec![response_behavior(
        "新连接回复",
        &["新连接回复"],
    )]));
    let (runtime, conversation_id, _) = setup(original.clone());
    let sink = RecordingSink::default();
    let first_delta = original.first_text_delta.notified();
    let runtime_task = Arc::clone(&runtime);
    let sink_task = sink.clone();
    let first = tokio::spawn(async move {
        let mut task_sink = sink_task;
        runtime_task
            .submit_turn(conversation_id, "第一轮".to_owned(), false, &mut task_sink)
            .await
    });
    first_delta.await;
    let active_turn_id = sink.events()[0].turn_id;

    runtime
        .replace_gateway("test-model".to_owned(), replacement.clone())
        .expect("replace gateway while old turn is active");
    runtime
        .cancel_turn(active_turn_id)
        .expect("cancel old active turn");
    first
        .await
        .expect("join old turn")
        .expect_err("old active turn remains bound to original gateway");

    let mut next_sink = RecordingSink::default();
    let next = runtime
        .submit_turn(conversation_id, "第二轮".to_owned(), false, &mut next_sink)
        .await
        .expect("next turn uses replacement gateway");

    assert_eq!(next.response_text.as_str(), "新连接回复");
    assert_eq!(
        original
            .requests()
            .iter()
            .map(|request| request.shape.lane)
            .collect::<Vec<_>>(),
        vec![PromptLane::Respond]
    );
    assert_eq!(
        replacement
            .requests()
            .iter()
            .map(|request| request.shape.lane)
            .collect::<Vec<_>>(),
        vec![PromptLane::Respond]
    );
}

#[tokio::test]
async fn active_turn_rejects_second_submit_and_role_switch_but_queues_profile_revision() {
    let gateway = Arc::new(FakeGateway::new(vec![
        FakeBehavior::WaitAfterTextDelta {
            delta: "等待".to_owned(),
        },
        response_behavior("已使用新资料", &["已使用新资料"]),
    ]));
    let (runtime, conversation_id, _) = setup(gateway.clone());
    let sink = RecordingSink::default();
    let first_delta = gateway.first_text_delta.notified();
    let runtime_task = runtime.clone();
    let sink_task = sink.clone();
    let first = tokio::spawn(async move {
        let mut task_sink = sink_task;
        runtime_task
            .submit_turn(
                conversation_id,
                "等待中的轮次".to_owned(),
                false,
                &mut task_sink,
            )
            .await
    });
    first_delta.await;
    let active_turn_id = sink.events()[0].turn_id;

    let mut second_sink = RecordingSink::default();
    let second_error = runtime
        .submit_turn(
            conversation_id,
            "并发输入".to_owned(),
            false,
            &mut second_sink,
        )
        .await
        .expect_err("second submit must fail");
    assert_eq!(second_error.code, ErrorCode::TurnInProgress);
    assert_eq!(
        runtime
            .activate_character(
                conversation_id,
                character(Revision::new(2).expect("revision two"), "新角色"),
            )
            .expect_err("role switch during turn must fail")
            .code,
        ErrorCode::TurnInProgress
    );
    assert!(
        runtime
            .update_user_profile(
                conversation_id,
                profile(Revision::new(2).expect("revision two"), "凛"),
            )
            .expect("queue profile update")
    );

    runtime
        .cancel_turn(active_turn_id)
        .expect("cancel waiting turn");
    first
        .await
        .expect("join waiting turn")
        .expect_err("waiting turn is interrupted");

    let mut next_sink = RecordingSink::default();
    runtime
        .submit_turn(
            conversation_id,
            "更新后的轮次".to_owned(),
            false,
            &mut next_sink,
        )
        .await
        .expect("run turn with queued profile");
    let requests = gateway.requests();
    let next_respond = requests
        .iter()
        .rev()
        .find(|request| request.shape.lane == PromptLane::Respond)
        .expect("next respond request");
    assert!(next_respond.input.iter().any(|item| matches!(
        item,
        PromptItem::UserProfileUpdated { snapshot: Some(snapshot) }
            if snapshot.revision().get() == 2 && snapshot.preferred_name() == Some("凛")
    )));
}

#[tokio::test]
async fn profile_changes_append_context_without_replacing_conversation_history() {
    let gateway = Arc::new(FakeGateway::new(vec![
        response_behavior("第一轮回复", &["第一轮回复"]),
        response_behavior("第二轮回复", &["第二轮回复"]),
        response_behavior("第三轮回复", &["第三轮回复"]),
    ]));
    let (runtime, conversation_id, _) = setup(gateway.clone());

    runtime
        .submit_turn(
            conversation_id,
            "第一轮消息".to_owned(),
            false,
            &mut RecordingSink::default(),
        )
        .await
        .expect("complete first turn");
    assert!(
        runtime
            .update_user_profile(
                conversation_id,
                profile(Revision::new(2).expect("revision two"), "凛"),
            )
            .expect("update preferred name")
    );
    assert_eq!(
        runtime
            .session_snapshot(conversation_id)
            .expect("session after name update")
            .conversation_id,
        conversation_id
    );
    runtime
        .submit_turn(
            conversation_id,
            "第二轮消息".to_owned(),
            false,
            &mut RecordingSink::default(),
        )
        .await
        .expect("complete second turn");

    assert!(
        runtime
            .update_user_profile(
                conversation_id,
                profile_without_name(Revision::new(3).expect("revision three")),
            )
            .expect("clear preferred name")
    );
    runtime
        .submit_turn(
            conversation_id,
            "第三轮消息".to_owned(),
            false,
            &mut RecordingSink::default(),
        )
        .await
        .expect("complete third turn");

    let requests = gateway.requests();
    let second = &requests[1];
    assert!(second.input.iter().any(|item| matches!(
        item,
        PromptItem::UserMessage { content } if content == "第一轮消息"
    )));
    assert!(second.input.iter().any(|item| matches!(
        item,
        PromptItem::AssistantMessage { content } if content == "第一轮回复"
    )));
    assert!(second.input.iter().any(|item| matches!(
        item,
        PromptItem::UserProfileUpdated { snapshot: Some(snapshot) }
            if snapshot.revision().get() == 2 && snapshot.preferred_name() == Some("凛")
    )));
    let third = &requests[2];
    assert!(third.input.iter().any(|item| matches!(
        item,
        PromptItem::UserProfileUpdated { snapshot: Some(snapshot) }
            if snapshot.revision().get() == 3 && snapshot.preferred_name().is_none()
    )));
    assert!(third.input.iter().any(|item| matches!(
        item,
        PromptItem::AssistantMessage { content } if content == "第二轮回复"
    )));
}

#[tokio::test]
async fn stream_failure_after_partial_text_has_failed_terminal_not_completed() {
    let gateway = Arc::new(FakeGateway::new(vec![FakeBehavior::FailAfterTextDelta {
        delta: "未完成部分".to_owned(),
    }]));
    let (runtime, conversation_id, _) = setup(gateway);
    let mut sink = RecordingSink::default();

    let error = runtime
        .submit_turn(conversation_id, "触发失败".to_owned(), true, &mut sink)
        .await
        .expect_err("stream failure must fail turn");
    let events = sink.events();

    assert_eq!(error.code, ErrorCode::ModelStreamFailed);
    assert!(
        events
            .iter()
            .all(|event| !matches!(event.payload, HarnessEventPayload::TextDelta { .. }))
    );
    assert_eq!(
        events.last().expect("failed terminal").state,
        TurnState::Failed
    );
    assert!(
        !events
            .iter()
            .any(|event| event.state == TurnState::Completed)
    );
    assert!(
        !events
            .iter()
            .any(|event| matches!(event.payload, HarnessEventPayload::SpeechRequested { .. }))
    );
    assert_eq!(
        runtime
            .session_snapshot(conversation_id)
            .expect("session returns idle")
            .state,
        TurnState::Idle
    );
}

#[tokio::test]
async fn retrieval_context_is_ephemeral_before_current_user_and_recent_turns_feed_next_query() {
    let context = RetrievalContext {
        personal_memories: vec![RetrievedPersonalMemory {
            id: PersonalMemoryId::new(),
            kind: PersonalMemoryKind::Preference,
            scope: fairy_domain::MemoryScope::Global,
            content: "用户不喜欢太甜的饮料".to_owned(),
            confidence_basis_points: 9000,
            updated_at_unix_ms: 42,
        }],
        knowledge: Vec::new(),
    };
    let gateway = Arc::new(FakeGateway::new(vec![
        response_behavior("那就选清爽一点的。", &["那就选清爽一点的。"]),
        response_behavior("上一轮说的是饮料。", &["上一轮说的是饮料。"]),
    ]));
    let intelligence = Arc::new(FakeIntelligence::new(vec![
        Ok(context.clone()),
        Ok(RetrievalContext::default()),
    ]));
    let (runtime, conversation_id, role) = setup(gateway.clone());
    runtime.replace_persistence_binding(PersistenceBinding::Available(intelligence.clone()));

    runtime
        .submit_turn(
            conversation_id,
            "推荐一杯太甜的饮料".to_owned(),
            false,
            &mut RecordingSink::default(),
        )
        .await
        .expect("complete memory-assisted turn");
    let request = &gateway.requests()[0];
    let retrieved_index = request
        .input
        .iter()
        .position(|item| matches!(item, PromptItem::RetrievedContext { .. }))
        .expect("retrieved context item");
    let user_index = request
        .input
        .iter()
        .position(|item| matches!(item, PromptItem::UserMessage { .. }))
        .expect("user message item");
    assert!(retrieved_index < user_index);
    assert!(matches!(
        &request.input[retrieved_index],
        PromptItem::RetrievedContext { context: injected } if injected == &context
    ));

    runtime
        .submit_turn(
            conversation_id,
            "刚才说的是什么？".to_owned(),
            false,
            &mut RecordingSink::default(),
        )
        .await
        .expect("complete second turn");
    assert!(
        gateway.requests()[1]
            .input
            .iter()
            .all(|item| !matches!(item, PromptItem::RetrievedContext { .. }))
    );
    let queries = intelligence.queries();
    assert_eq!(queries[1].0, role.character_id());
    assert!(queries[1].1.contains("推荐一杯太甜的饮料"));
    assert!(queries[1].1.contains("那就选清爽一点的。"));
    assert!(queries[1].1.ends_with("刚才说的是什么？"));

    let empty_gateway = Arc::new(FakeGateway::new(vec![response_behavior(
        "我在。",
        &["我在。"],
    )]));
    let empty_intelligence = Arc::new(FakeIntelligence::new(vec![Ok(RetrievalContext::default())]));
    let (empty_runtime, empty_conversation, _) = setup(empty_gateway.clone());
    empty_runtime.replace_persistence_binding(PersistenceBinding::Available(empty_intelligence));
    empty_runtime
        .submit_turn(
            empty_conversation,
            "你好".to_owned(),
            false,
            &mut RecordingSink::default(),
        )
        .await
        .expect("complete empty retrieval turn");
    assert!(
        empty_gateway.requests()[0]
            .input
            .iter()
            .all(|item| !matches!(item, PromptItem::RetrievedContext { .. }))
    );
}

#[tokio::test]
async fn unavailable_persistence_fails_before_model_without_empty_memory_fallback() {
    let gateway = Arc::new(FakeGateway::new(Vec::new()));
    let (runtime, conversation_id, _) = setup(gateway.clone());
    runtime.replace_persistence_binding(PersistenceBinding::Unavailable(FairyError::new(
        ErrorCode::IntelligenceUnavailable,
        "数据库无法打开",
        false,
    )));

    let error = runtime
        .submit_turn(
            conversation_id,
            "记住这件事".to_owned(),
            false,
            &mut RecordingSink::default(),
        )
        .await
        .expect_err("persistence failure must stop before model");

    assert_eq!(error.code, ErrorCode::IntelligenceUnavailable);
    assert!(gateway.requests().is_empty());
    assert_eq!(
        runtime
            .session_snapshot(conversation_id)
            .expect("prepared turn is discarded")
            .state,
        TurnState::Idle
    );
}

#[tokio::test]
async fn successful_background_extraction_commits_personal_memory_without_network_tools() {
    let extraction_json = serde_json::json!({
        "mutations": [{
            "operation": "create",
            "kind": "preference",
            "scope": { "type": "global" },
            "content": "用户喜欢 Rust",
            "confidenceBasisPoints": 9000
        }]
    })
    .to_string();
    let gateway = Arc::new(FakeGateway::new(vec![
        response_behavior("好，我记住了。", &["好，我记住了。"]),
        response_behavior(&extraction_json, &[&extraction_json]),
    ]));
    let intelligence = Arc::new(FakeIntelligence::with_extraction(vec![Ok(
        RetrievalContext::default(),
    )]));
    let (runtime, conversation_id, _) = setup(gateway.clone());
    runtime.replace_persistence_binding(PersistenceBinding::Available(intelligence.clone()));

    let outcome = runtime
        .submit_turn(
            conversation_id,
            "记住我喜欢 Rust".to_owned(),
            false,
            &mut RecordingSink::default(),
        )
        .await
        .expect("visible reply completes before extraction");
    assert_eq!(outcome.response_text.as_str(), "好，我记住了。");
    assert!(outcome.sources.is_empty());
    wait_for_background(&runtime).await;

    let committed = intelligence.committed();
    assert_eq!(committed.len(), 1);
    assert_eq!(committed[0].len(), 1);
    assert!(matches!(
        &committed[0][0],
        fairy_domain::MemoryMutation::Create { content, .. } if content == "用户喜欢 Rust"
    ));
    assert!(intelligence.failed().is_empty());
    assert!(runtime.last_intelligence_background_error().is_none());
    let requests = gateway.requests();
    assert_eq!(requests.len(), 2);
    assert_eq!(requests[1].shape.lane, PromptLane::Extract);
    assert_eq!(requests[1].shape.max_output_tokens, 800);
    assert!(matches!(
        requests[1].input.as_slice(),
        [PromptItem::ExtractionBatch { .. }]
    ));
}

#[tokio::test(start_paused = true)]
async fn one_pending_turn_waits_for_thirty_seconds_of_conversation_idle() {
    let extraction_json = serde_json::json!({ "mutations": [] }).to_string();
    let gateway = Arc::new(FakeGateway::new(vec![
        response_behavior("先正常回复。", &["先正常回复。"]),
        response_behavior(&extraction_json, &[&extraction_json]),
    ]));
    let persistence = Arc::new(FakeIntelligence::with_idle_extraction(vec![Ok(
        RetrievalContext::default(),
    )]));
    let (runtime, conversation_id, _) = setup(gateway.clone());
    runtime.replace_persistence_binding(PersistenceBinding::Available(persistence));

    runtime
        .submit_turn(
            conversation_id,
            "只有一轮".to_owned(),
            false,
            &mut RecordingSink::default(),
        )
        .await
        .expect("complete visible turn");
    assert_eq!(gateway.requests().len(), 1);
    tokio::task::yield_now().await;

    tokio::time::advance(Duration::from_secs(29)).await;
    tokio::task::yield_now().await;
    assert_eq!(gateway.requests().len(), 1);

    tokio::time::advance(Duration::from_secs(1)).await;
    for _ in 0..8 {
        tokio::task::yield_now().await;
    }
    wait_for_background(&runtime).await;
    assert_eq!(gateway.requests().len(), 2);
    assert_eq!(gateway.requests()[1].shape.lane, PromptLane::Extract);
}

#[tokio::test]
async fn invalid_extraction_json_records_failed_job_without_reopening_completed_turn() {
    let invalid = "```json\n{\"personalMemories\":[],\"knowledge\":[]}\n```";
    let gateway = Arc::new(FakeGateway::new(vec![
        response_behavior("我会在后台整理。", &["我会在后台整理。"]),
        response_behavior(invalid, &[invalid]),
    ]));
    let intelligence = Arc::new(FakeIntelligence::with_extraction(vec![Ok(
        RetrievalContext::default(),
    )]));
    let (runtime, conversation_id, _) = setup(gateway);
    runtime.replace_persistence_binding(PersistenceBinding::Available(intelligence.clone()));
    let mut sink = RecordingSink::default();

    let outcome = runtime
        .submit_turn(conversation_id, "记住这件事".to_owned(), false, &mut sink)
        .await
        .expect("visible turn remains successful");
    assert_eq!(outcome.response_text.as_str(), "我会在后台整理。");
    wait_for_background(&runtime).await;

    assert!(intelligence.committed().is_empty());
    let failed = intelligence.failed();
    assert_eq!(failed.len(), 1);
    assert_eq!(failed[0].code, ErrorCode::ExtractionBatchFailed);
    assert_eq!(
        runtime
            .last_intelligence_background_error()
            .expect("background diagnostic")
            .code,
        ErrorCode::ExtractionBatchFailed
    );
    assert_eq!(
        sink.events()
            .iter()
            .filter(|event| matches!(event.payload, HarnessEventPayload::Completed { .. }))
            .count(),
        1
    );
    assert_eq!(
        runtime
            .session_snapshot(conversation_id)
            .expect("completed session")
            .state,
        TurnState::Idle
    );
}

#[tokio::test]
async fn missing_session_turn_and_empty_input_have_explicit_errors() {
    let gateway = Arc::new(FakeGateway::new(vec![]));
    let runtime = HarnessRuntime::new("test-model".to_owned(), gateway).expect("create runtime");
    let session = runtime.create_session();
    let mut sink = RecordingSink::default();
    assert_eq!(
        runtime
            .submit_turn(
                session.conversation_id,
                "  \n ".to_owned(),
                false,
                &mut sink,
            )
            .await
            .expect_err("blank input must fail before a turn starts")
            .code,
        ErrorCode::InvalidEventPayload
    );
    assert!(sink.events().is_empty());
    assert_eq!(
        runtime
            .session_snapshot(fairy_domain::ConversationId::new())
            .expect_err("missing session")
            .code,
        ErrorCode::ConversationNotFound
    );
    assert_eq!(
        runtime
            .cancel_turn(fairy_domain::TurnId::new())
            .expect_err("missing turn")
            .code,
        ErrorCode::TurnNotActive
    );
}

fn lock<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex.lock().expect("test mutex poisoned")
}
