use std::collections::HashMap;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex, MutexGuard, RwLock, RwLockReadGuard, RwLockWriteGuard};

use fairy_domain::{
    AssistantSource, CapabilityState, CharacterSnapshot, CompanionCapability, CompiledReply,
    ConversationId, ErrorCode, ExtractionOutput, FairyError, HarnessEvent, LaneModelUsage,
    ModelCompletion, ModelStreamEvent, ModelTurnOutput, NewKnowledge, NewPersonalMemory,
    PromptItem, PromptLane, ReplyMode, ToolCall, ToolName, ToolResult, ToolResultOutcome, TurnId,
    TurnLifecycle, TurnState, UserProfileSnapshot,
};
use serde::Deserialize;
use tokio_util::sync::CancellationToken;

use crate::{
    CompactionCandidate, CompactionResult, ConversationHistory, HarnessEventSink,
    IntelligenceBinding, ModelEventSink, ModelGateway, PromptCompiler, ReplyCompiler,
    SessionSnapshot, TurnOutcome, WebSearchGateway, install_compaction,
};

const MAX_WEB_SEARCH_CALLS_PER_TURN: usize = 2;

pub struct HarnessRuntime {
    gateway_binding: RwLock<GatewayBinding>,
    web_search_gateway: RwLock<Option<Arc<dyn WebSearchGateway + Send + Sync>>>,
    intelligence_binding: RwLock<IntelligenceBinding>,
    background_jobs: Arc<AtomicUsize>,
    intelligence_background_error: Arc<Mutex<Option<FairyError>>>,
    sessions: Mutex<HashMap<ConversationId, Arc<Mutex<Session>>>>,
}

#[derive(Clone)]
struct GatewayBinding {
    model: String,
    gateway: Arc<dyn ModelGateway + Send + Sync>,
}

struct Session {
    history: ConversationHistory,
    character: Option<CharacterSnapshot>,
    user_profile: Option<UserProfileSnapshot>,
    pending_user_profile: Option<UserProfileSnapshot>,
    active_turn: Option<ActiveTurn>,
    compacting: bool,
}

struct ActiveTurn {
    turn_id: TurnId,
    lifecycle: TurnLifecycle,
    cancellation: CancellationToken,
    character: CharacterSnapshot,
    user_profile: Option<UserProfileSnapshot>,
    model: String,
    gateway: Arc<dyn ModelGateway + Send + Sync>,
    web_search_gateway: Option<Arc<dyn WebSearchGateway + Send + Sync>>,
    intelligence_binding: IntelligenceBinding,
    user_input: String,
}

struct ExtractionTurnInput {
    conversation_id: ConversationId,
    turn_id: TurnId,
    user_message: String,
    assistant_message: String,
    sources: Vec<AssistantSource>,
}

impl HarnessRuntime {
    pub fn new(
        model: String,
        gateway: Arc<dyn ModelGateway + Send + Sync>,
    ) -> Result<Self, FairyError> {
        validate_runtime_model(&model)?;
        Ok(Self {
            gateway_binding: RwLock::new(GatewayBinding { model, gateway }),
            web_search_gateway: RwLock::new(None),
            intelligence_binding: RwLock::new(IntelligenceBinding::Disabled),
            background_jobs: Arc::new(AtomicUsize::new(0)),
            intelligence_background_error: Arc::new(Mutex::new(None)),
            sessions: Mutex::new(HashMap::new()),
        })
    }

    pub fn replace_gateway(
        &self,
        model: String,
        gateway: Arc<dyn ModelGateway + Send + Sync>,
    ) -> Result<(), FairyError> {
        validate_runtime_model(&model)?;
        *write_lock(&self.gateway_binding) = GatewayBinding { model, gateway };
        Ok(())
    }

    pub fn replace_web_search_gateway(
        &self,
        gateway: Option<Arc<dyn WebSearchGateway + Send + Sync>>,
    ) {
        *write_lock(&self.web_search_gateway) = gateway;
    }

    pub fn replace_intelligence_binding(&self, binding: IntelligenceBinding) {
        *write_lock(&self.intelligence_binding) = binding;
    }

    pub fn create_session(&self) -> SessionSnapshot {
        let conversation_id = ConversationId::new();
        let session = Session {
            history: ConversationHistory::new(conversation_id),
            character: None,
            user_profile: None,
            pending_user_profile: None,
            active_turn: None,
            compacting: false,
        };
        lock(&self.sessions).insert(conversation_id, Arc::new(Mutex::new(session)));
        SessionSnapshot {
            conversation_id,
            state: TurnState::Idle,
            active_turn_id: None,
        }
    }

    pub fn session_snapshot(
        &self,
        conversation_id: ConversationId,
    ) -> Result<SessionSnapshot, FairyError> {
        let session = self.session(conversation_id)?;
        let session = lock(&session);
        let (state, active_turn_id) = session
            .active_turn
            .as_ref()
            .map(|turn| (turn.lifecycle.state(), Some(turn.turn_id)))
            .unwrap_or((TurnState::Idle, None));
        Ok(SessionSnapshot {
            conversation_id,
            state,
            active_turn_id,
        })
    }

    #[must_use]
    pub fn has_active_work(&self) -> bool {
        if self.background_jobs.load(Ordering::Acquire) > 0 {
            return true;
        }
        let sessions: Vec<_> = lock(&self.sessions).values().cloned().collect();
        sessions.iter().any(|session| {
            let session = lock(session);
            session.active_turn.is_some() || session.compacting
        })
    }

    #[must_use]
    pub fn active_background_jobs(&self) -> usize {
        self.background_jobs.load(Ordering::Acquire)
    }

    #[must_use]
    pub fn last_intelligence_background_error(&self) -> Option<FairyError> {
        lock(&self.intelligence_background_error).clone()
    }

    pub fn activate_character(
        &self,
        conversation_id: ConversationId,
        snapshot: CharacterSnapshot,
    ) -> Result<bool, FairyError> {
        snapshot.verify_integrity()?;
        let session = self.session(conversation_id)?;
        let mut session = lock(&session);
        if session.active_turn.is_some() || session.compacting {
            return Err(turn_in_progress());
        }
        let changed = session.history.activate_character(&snapshot);
        session.character = Some(snapshot);
        Ok(changed)
    }

    pub fn update_user_profile(
        &self,
        conversation_id: ConversationId,
        snapshot: UserProfileSnapshot,
    ) -> Result<bool, FairyError> {
        snapshot.verify_integrity()?;
        let session = self.session(conversation_id)?;
        let mut session = lock(&session);
        if session.active_turn.is_some() || session.compacting {
            session.history.queue_user_profile(snapshot.clone());
            let changed = session
                .pending_user_profile
                .as_ref()
                .is_none_or(|pending| pending.revision() != snapshot.revision());
            session.pending_user_profile = Some(snapshot);
            return Ok(changed);
        }
        let changed = session.history.synchronize_user_profile(&snapshot);
        session.user_profile = Some(snapshot);
        Ok(changed)
    }

    pub fn cancel_turn(&self, turn_id: TurnId) -> Result<(), FairyError> {
        let sessions: Vec<_> = lock(&self.sessions).values().cloned().collect();
        for session in sessions {
            let session = lock(&session);
            if let Some(active) = &session.active_turn
                && active.turn_id == turn_id
                && !active.lifecycle.state().is_terminal()
            {
                active.cancellation.cancel();
                return Ok(());
            }
        }
        Err(FairyError::new(
            ErrorCode::TurnNotActive,
            "指定 turn 不存在或已经结束",
            false,
        ))
    }

    pub async fn submit_turn(
        &self,
        conversation_id: ConversationId,
        input: String,
        speech_enabled: bool,
        events: &mut (dyn HarnessEventSink + Send),
    ) -> Result<TurnOutcome, FairyError> {
        if input.trim().is_empty() {
            return Err(FairyError::new(
                ErrorCode::InvalidEventPayload,
                "对话输入不能为空",
                false,
            ));
        }
        let session = self.session(conversation_id)?;
        let intelligence = read_lock(&self.intelligence_binding).clone();
        let context_item = retrieve_context_item(&intelligence, &input).await;
        let turn_id = self.begin_turn(
            &session,
            conversation_id,
            &input,
            context_item,
            intelligence,
        )?;
        let result = self
            .run_turn(&session, conversation_id, turn_id, speech_enabled, events)
            .await;
        match result {
            Ok(outcome) => Ok(outcome),
            Err(error) => {
                self.finish_error(&session, turn_id, &error, events);
                Err(error)
            }
        }
    }

    pub async fn compact_conversation(
        &self,
        conversation_id: ConversationId,
    ) -> Result<CompactionResult, FairyError> {
        let session = self.session(conversation_id)?;
        let (request, gateway, character, user_profile) = {
            let mut session = lock(&session);
            if session.active_turn.is_some() || session.compacting {
                return Err(turn_in_progress());
            }
            if let Some(pending) = session.pending_user_profile.take() {
                session.history.flush_pending_context();
                session.user_profile = Some(pending);
            }
            let character = session.character.clone().ok_or_else(|| {
                FairyError::new(
                    ErrorCode::CharacterNotAvailable,
                    "压缩会话前必须激活一个有效角色",
                    false,
                )
            })?;
            let user_profile = session.user_profile.clone();
            let binding = read_lock(&self.gateway_binding).clone();
            let lane = session.history.lane(PromptLane::Compact);
            let request = PromptCompiler.compile(
                PromptLane::Compact,
                binding.model,
                lane.items().to_vec(),
                cache_key(binding.gateway.as_ref(), lane.cache_key()),
            );
            session.compacting = true;
            (request, binding.gateway, character, user_profile)
        };

        let mut sink = CompactionOutputSink::default();
        let completion = gateway
            .execute(request, CancellationToken::new(), &mut sink)
            .await;
        let result = match completion {
            Ok(completion) => {
                let output_text = completion.output.into_text()?;
                if !sink.output.is_empty() && sink.output != output_text {
                    Err(FairyError::new(
                        ErrorCode::ModelResponseInvalid,
                        "Compactor 流式文本与完成文本不一致",
                        false,
                    ))
                } else {
                    let mut session_guard = lock(&session);
                    install_compaction(
                        &mut session_guard.history,
                        CompactionCandidate {
                            summary: output_text,
                            replacement_items: Vec::new(),
                        },
                        &character,
                        user_profile.as_ref(),
                    )
                }
            }
            Err(error) => Err(error),
        };
        lock(&session).compacting = false;
        result
    }

    fn begin_turn(
        &self,
        session: &Arc<Mutex<Session>>,
        conversation_id: ConversationId,
        input: &str,
        context_item: Option<PromptItem>,
        intelligence_binding: IntelligenceBinding,
    ) -> Result<TurnId, FairyError> {
        let mut session = lock(session);
        if session.active_turn.is_some() || session.compacting {
            return Err(turn_in_progress());
        }
        if let Some(pending) = session.pending_user_profile.take() {
            session.history.flush_pending_context();
            session.user_profile = Some(pending);
        }
        let character = session.character.clone().ok_or_else(|| {
            FairyError::new(
                ErrorCode::CharacterNotAvailable,
                "开始对话前必须激活一个有效角色",
                false,
            )
        })?;
        let user_profile = session.user_profile.clone();
        let binding = read_lock(&self.gateway_binding).clone();
        let turn_id = TurnId::new();
        let cancellation = CancellationToken::new();
        let respond = session.history.lane_mut(PromptLane::Respond);
        if let Some(context_item) = context_item {
            respond.append(context_item);
        }
        respond.append(PromptItem::UserMessage {
            content: input.to_owned(),
        });
        session.active_turn = Some(ActiveTurn {
            turn_id,
            lifecycle: TurnLifecycle::new(conversation_id, turn_id),
            cancellation,
            character,
            user_profile,
            model: binding.model,
            gateway: binding.gateway,
            web_search_gateway: read_lock(&self.web_search_gateway).clone(),
            intelligence_binding,
            user_input: input.to_owned(),
        });
        Ok(turn_id)
    }

    async fn run_turn(
        &self,
        session: &Arc<Mutex<Session>>,
        conversation_id: ConversationId,
        turn_id: TurnId,
        speech_enabled: bool,
        events: &mut (dyn HarnessEventSink + Send),
    ) -> Result<TurnOutcome, FairyError> {
        emit_state(session, turn_id, TurnState::Interpreting, events)?;
        let (cancellation, model, gateway, web_search_gateway, intelligence_binding, user_input) = {
            let session = lock(session);
            let active = active_turn(&session, turn_id)?;
            (
                active.cancellation.clone(),
                active.model.clone(),
                Arc::clone(&active.gateway),
                active.web_search_gateway.clone(),
                active.intelligence_binding.clone(),
                active.user_input.clone(),
            )
        };
        if cancellation.is_cancelled() {
            return Err(turn_interrupted());
        }
        emit_state(session, turn_id, TurnState::Planning, events)?;
        emit_state(session, turn_id, TurnState::Responding, events)?;

        let (compiled, usage) = execute_respond_loop(
            session,
            turn_id,
            model.clone(),
            Arc::clone(&gateway),
            web_search_gateway,
            cancellation.clone(),
        )
        .await?;
        emit_text(
            session,
            turn_id,
            compiled.display_text.as_str().to_owned(),
            events,
        )?;

        let (character_revision, user_profile_revision) = {
            let mut session = lock(session);
            let active = active_turn(&session, turn_id)?;
            let character_revision = active.character.revision();
            let user_profile_revision = active
                .user_profile
                .as_ref()
                .map(UserProfileSnapshot::revision);
            session
                .history
                .lane_mut(PromptLane::Respond)
                .append(PromptItem::AssistantMessage {
                    content: compiled.display_text.as_str().to_owned(),
                });
            session
                .history
                .lane_mut(PromptLane::Respond)
                .seal_current_prefix()?;
            (character_revision, user_profile_revision)
        };
        let terminal_events = complete_turn(
            session,
            turn_id,
            compiled.clone(),
            character_revision,
            user_profile_revision,
            usage.clone(),
            speech_enabled,
        )?;
        for event in terminal_events {
            events.send(event)?;
        }

        self.schedule_background_extraction(
            intelligence_binding,
            gateway,
            model,
            ExtractionTurnInput {
                conversation_id,
                turn_id,
                user_message: user_input,
                assistant_message: compiled.display_text.as_str().to_owned(),
                sources: compiled.sources.clone(),
            },
        )
        .await;

        Ok(TurnOutcome {
            conversation_id,
            turn_id,
            response_text: compiled.display_text,
            speech_text: compiled.speech_text,
            sources: compiled.sources,
            character_revision,
            user_profile_revision,
            usage,
            speech_requested: speech_enabled,
        })
    }

    async fn schedule_background_extraction(
        &self,
        binding: IntelligenceBinding,
        gateway: Arc<dyn ModelGateway + Send + Sync>,
        model: String,
        input: ExtractionTurnInput,
    ) {
        let IntelligenceBinding::Available(intelligence) = binding else {
            return;
        };
        let job_id = match intelligence
            .create_extraction_job(input.conversation_id, input.turn_id)
            .await
        {
            Ok(job_id) => job_id,
            Err(error) => {
                *lock(&self.intelligence_background_error) = Some(error);
                return;
            }
        };
        if let Err(error) = intelligence.mark_extraction_running(job_id).await {
            *lock(&self.intelligence_background_error) = Some(error);
            return;
        }

        let jobs = Arc::clone(&self.background_jobs);
        let background_error = Arc::clone(&self.intelligence_background_error);
        jobs.fetch_add(1, Ordering::AcqRel);
        tokio::spawn(async move {
            let _guard = BackgroundJobGuard::new(Arc::clone(&jobs));
            let result =
                run_background_extraction(intelligence.as_ref(), gateway, model, job_id, input)
                    .await;
            match result {
                Ok(()) => {
                    *lock(&background_error) = None;
                }
                Err(error) => {
                    let recorded = match intelligence
                        .fail_extraction_job(job_id, error.clone())
                        .await
                    {
                        Ok(()) => error,
                        Err(recording_error) => recording_error,
                    };
                    *lock(&background_error) = Some(recorded);
                }
            }
        });
    }

    fn finish_error(
        &self,
        session: &Arc<Mutex<Session>>,
        turn_id: TurnId,
        error: &FairyError,
        events: &mut (dyn HarnessEventSink + Send),
    ) {
        let event = {
            let mut session = lock(session);
            let Some(active) = session.active_turn.as_mut() else {
                return;
            };
            if active.turn_id != turn_id || active.lifecycle.state().is_terminal() {
                session.active_turn = None;
                return;
            }
            let event =
                if error.code == ErrorCode::TurnInterrupted || active.cancellation.is_cancelled() {
                    active.lifecycle.transition(TurnState::Interrupted)
                } else {
                    active.lifecycle.fail(error.clone())
                };
            session.active_turn = None;
            event.ok()
        };
        if let Some(event) = event {
            let _ = events.send(event);
        }
    }

    fn session(&self, conversation_id: ConversationId) -> Result<Arc<Mutex<Session>>, FairyError> {
        lock(&self.sessions)
            .get(&conversation_id)
            .cloned()
            .ok_or_else(|| {
                FairyError::new(
                    ErrorCode::ConversationNotFound,
                    "指定 conversation 不存在",
                    false,
                )
            })
    }
}

async fn retrieve_context_item(binding: &IntelligenceBinding, input: &str) -> Option<PromptItem> {
    match binding {
        IntelligenceBinding::Disabled => None,
        IntelligenceBinding::Unavailable(error) => Some(PromptItem::CapabilityStatus {
            capability: CompanionCapability::Intelligence,
            state: CapabilityState::Unavailable,
            error: Some(error.clone()),
        }),
        IntelligenceBinding::Available(intelligence) => {
            match intelligence.retrieve(input.to_owned()).await {
                Ok(context) if context.is_empty() => None,
                Ok(context) => Some(PromptItem::RetrievedContext { context }),
                Err(error) => Some(PromptItem::CapabilityStatus {
                    capability: CompanionCapability::Intelligence,
                    state: CapabilityState::Unavailable,
                    error: Some(error),
                }),
            }
        }
    }
}

struct BackgroundJobGuard {
    jobs: Arc<AtomicUsize>,
}

impl BackgroundJobGuard {
    fn new(jobs: Arc<AtomicUsize>) -> Self {
        Self { jobs }
    }
}

impl Drop for BackgroundJobGuard {
    fn drop(&mut self) {
        self.jobs.fetch_sub(1, Ordering::AcqRel);
    }
}

async fn run_background_extraction(
    intelligence: &(dyn crate::CompanionIntelligence + Send + Sync),
    gateway: Arc<dyn ModelGateway + Send + Sync>,
    model: String,
    job_id: fairy_domain::ExtractionJobId,
    input: ExtractionTurnInput,
) -> Result<(), FairyError> {
    let extraction_item = PromptItem::ExtractionInput {
        user_message: input.user_message,
        assistant_message: input.assistant_message,
        sources: input.sources.clone(),
    };
    let raw_cache_key = format!("fairy:{}:extract", input.conversation_id);
    let request = PromptCompiler.compile(
        PromptLane::Extract,
        model,
        vec![PromptCompiler::canonical_harness_context(), extraction_item],
        cache_key(gateway.as_ref(), &raw_cache_key),
    );
    let mut sink = BufferedOutputSink::default();
    let completion = gateway
        .execute(request, CancellationToken::new(), &mut sink)
        .await?;
    let text = completion.output.into_text()?;
    if !sink.output.is_empty() && sink.output != text {
        return Err(FairyError::new(
            ErrorCode::IntelligenceExtractionFailed,
            "提取模型的流式文本与完成文本不一致",
            false,
        ));
    }
    let extracted: ExtractionOutput = serde_json::from_str(&text).map_err(|_| {
        FairyError::new(
            ErrorCode::IntelligenceExtractionFailed,
            "提取模型没有返回严格 ExtractionOutput JSON",
            false,
        )
    })?;
    if extracted.personal_memories.len() > 16 || extracted.knowledge.len() > 16 {
        return Err(FairyError::new(
            ErrorCode::IntelligenceExtractionFailed,
            "单轮提取候选超过 16 条上限",
            false,
        ));
    }

    let personal_memories = extracted
        .personal_memories
        .into_iter()
        .map(|candidate| NewPersonalMemory {
            kind: candidate.kind,
            content: candidate.content,
            confidence_basis_points: candidate.confidence_basis_points,
            source_conversation_id: input.conversation_id,
            source_turn_id: input.turn_id,
            supersedes_id: candidate.supersedes_id,
        })
        .collect();
    let mut knowledge = Vec::with_capacity(extracted.knowledge.len());
    for candidate in extracted.knowledge {
        let mut seen_ranks = std::collections::BTreeSet::new();
        let mut candidate_sources = Vec::with_capacity(candidate.source_ranks.len());
        for rank in candidate.source_ranks {
            if rank == 0 || !seen_ranks.insert(rank) {
                return Err(FairyError::new(
                    ErrorCode::IntelligenceExtractionFailed,
                    "知识候选包含无效或重复 sourceRanks",
                    false,
                ));
            }
            let source = input
                .sources
                .iter()
                .find(|source| source.rank == rank)
                .ok_or_else(|| {
                    FairyError::new(
                        ErrorCode::IntelligenceExtractionFailed,
                        "知识候选引用了本轮不存在的搜索来源",
                        false,
                    )
                })?;
            candidate_sources.push(source.clone());
        }
        knowledge.push(NewKnowledge {
            topic: candidate.topic,
            statement: candidate.statement,
            confidence_basis_points: candidate.confidence_basis_points,
            source_conversation_id: input.conversation_id,
            source_turn_id: input.turn_id,
            supersedes_id: candidate.supersedes_id,
            sources: candidate_sources,
        });
    }
    intelligence
        .commit_extraction(job_id, personal_memories, knowledge)
        .await
}

async fn execute_respond_loop(
    session: &Arc<Mutex<Session>>,
    turn_id: TurnId,
    model: String,
    gateway: Arc<dyn ModelGateway + Send + Sync>,
    web_search_gateway: Option<Arc<dyn WebSearchGateway + Send + Sync>>,
    cancellation: CancellationToken,
) -> Result<(CompiledReply, Vec<LaneModelUsage>), FairyError> {
    let mut search_call_count = 0_usize;
    let mut sources = Vec::new();
    let mut usage = Vec::new();

    loop {
        if cancellation.is_cancelled() {
            return Err(turn_interrupted());
        }
        let (request, window_revision) = {
            let session = lock(session);
            active_turn(&session, turn_id)?;
            let lane = session.history.lane(PromptLane::Respond);
            (
                PromptCompiler.compile_with_search(
                    PromptLane::Respond,
                    model.clone(),
                    lane.items().to_vec(),
                    cache_key(gateway.as_ref(), lane.cache_key()),
                    web_search_gateway.is_some(),
                ),
                lane.window_revision(),
            )
        };
        let reply_mode = request.shape.reply_mode.unwrap_or(ReplyMode::Brief);
        let mut sink = BufferedOutputSink::default();
        let ModelCompletion {
            output,
            usage: model_usage,
            ..
        } = gateway
            .execute(request, cancellation.clone(), &mut sink)
            .await?;
        usage.push(LaneModelUsage {
            lane: PromptLane::Respond,
            history_window: window_revision,
            usage: model_usage,
        });
        if cancellation.is_cancelled() {
            return Err(turn_interrupted());
        }

        match output {
            ModelTurnOutput::Text { text } => {
                if !sink.output.is_empty() && sink.output != text {
                    return Err(FairyError::new(
                        ErrorCode::ModelResponseInvalid,
                        "Responder 流式文本与完成文本不一致",
                        false,
                    ));
                }
                let reply = ReplyCompiler.compile(reply_mode, &text, sources)?;
                return Ok((reply, usage));
            }
            ModelTurnOutput::ToolCalls { calls } => {
                if !sink.output.is_empty() {
                    return Err(FairyError::new(
                        ErrorCode::ModelResponseInvalid,
                        "工具调用 completion 不得包含可见文本增量",
                        false,
                    ));
                }
                if calls.is_empty() {
                    return Err(FairyError::new(
                        ErrorCode::ModelResponseInvalid,
                        "模型返回了空工具调用集合",
                        false,
                    ));
                }
                if search_call_count.saturating_add(calls.len()) > MAX_WEB_SEARCH_CALLS_PER_TURN {
                    return Err(FairyError::new(
                        ErrorCode::ToolLimitExceeded,
                        "单轮 web_search 调用超过上限",
                        false,
                    ));
                }
                let search = web_search_gateway.as_ref().ok_or_else(|| {
                    FairyError::new(
                        ErrorCode::SearchConfigRequired,
                        "模型请求搜索，但当前 turn 没有可用搜索 Gateway",
                        false,
                    )
                })?;
                append_tool_calls(session, turn_id, &calls)?;
                for call in calls {
                    search_call_count += 1;
                    let result =
                        execute_web_search_call(search.as_ref(), call, cancellation.clone())
                            .await?;
                    if let ToolResultOutcome::Success { sources: found, .. } = &result.outcome {
                        for source in found {
                            if !sources.iter().any(|known: &fairy_domain::AssistantSource| {
                                known.url == source.url
                            }) {
                                sources.push(source.clone());
                            }
                        }
                    }
                    append_tool_result(session, turn_id, result)?;
                }
            }
        }
    }
}

fn append_tool_calls(
    session: &Arc<Mutex<Session>>,
    turn_id: TurnId,
    calls: &[ToolCall],
) -> Result<(), FairyError> {
    let mut session = lock(session);
    active_turn(&session, turn_id)?;
    let lane = session.history.lane_mut(PromptLane::Respond);
    for call in calls {
        lane.append(PromptItem::ToolCall { call: call.clone() });
    }
    Ok(())
}

fn append_tool_result(
    session: &Arc<Mutex<Session>>,
    turn_id: TurnId,
    result: ToolResult,
) -> Result<(), FairyError> {
    let mut session = lock(session);
    active_turn(&session, turn_id)?;
    session
        .history
        .lane_mut(PromptLane::Respond)
        .append(PromptItem::ToolResult { result });
    Ok(())
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct WebSearchArguments {
    query: String,
}

async fn execute_web_search_call(
    gateway: &(dyn WebSearchGateway + Send + Sync),
    call: ToolCall,
    cancellation: CancellationToken,
) -> Result<ToolResult, FairyError> {
    if call.name != ToolName::WebSearch {
        return Err(FairyError::new(
            ErrorCode::ToolArgumentsInvalid,
            "Harness 不支持模型请求的工具",
            false,
        ));
    }
    let arguments: WebSearchArguments =
        serde_json::from_str(&call.arguments_json).map_err(|_| {
            FairyError::new(
                ErrorCode::ToolArgumentsInvalid,
                "web_search arguments 不符合严格 JSON schema",
                false,
            )
        })?;
    let outcome = match gateway.search(arguments.query.clone(), cancellation).await {
        Ok(response) => ToolResultOutcome::Success {
            output: format!(
                "web_search returned {} source(s) for the quoted query",
                response.sources.len()
            ),
            sources: response.sources,
        },
        Err(error) if error.code == ErrorCode::TurnInterrupted => return Err(error),
        Err(error) => ToolResultOutcome::Failed { error },
    };
    Ok(ToolResult {
        call_id: call.id,
        name: call.name,
        outcome,
    })
}

#[derive(Default)]
struct CompactionOutputSink {
    output: String,
}

#[derive(Default)]
struct BufferedOutputSink {
    output: String,
}

impl ModelEventSink for CompactionOutputSink {
    fn send(&mut self, event: ModelStreamEvent) -> Result<(), FairyError> {
        match event {
            ModelStreamEvent::TextDelta { delta } => {
                self.output.push_str(&delta);
                Ok(())
            }
        }
    }
}

impl ModelEventSink for BufferedOutputSink {
    fn send(&mut self, event: ModelStreamEvent) -> Result<(), FairyError> {
        let ModelStreamEvent::TextDelta { delta } = event;
        self.output.push_str(&delta);
        Ok(())
    }
}

fn emit_state(
    session: &Arc<Mutex<Session>>,
    turn_id: TurnId,
    state: TurnState,
    events: &mut (dyn HarnessEventSink + Send),
) -> Result<(), FairyError> {
    let event = {
        let mut session = lock(session);
        active_turn_mut(&mut session, turn_id)?
            .lifecycle
            .transition(state)?
    };
    events.send(event)
}

fn emit_text(
    session: &Arc<Mutex<Session>>,
    turn_id: TurnId,
    text: String,
    events: &mut (dyn HarnessEventSink + Send),
) -> Result<(), FairyError> {
    let event = {
        let mut session = lock(session);
        active_turn_mut(&mut session, turn_id)?
            .lifecycle
            .text_delta(text)?
    };
    events.send(event)
}

fn complete_turn(
    session: &Arc<Mutex<Session>>,
    turn_id: TurnId,
    reply: CompiledReply,
    character_revision: fairy_domain::Revision,
    user_profile_revision: Option<fairy_domain::Revision>,
    usage: Vec<LaneModelUsage>,
    speech_enabled: bool,
) -> Result<Vec<HarnessEvent>, FairyError> {
    let mut session = lock(session);
    let active = active_turn_mut(&mut session, turn_id)?;
    let completed = active.lifecycle.complete(
        reply.display_text,
        reply.speech_text.clone(),
        reply.sources,
        character_revision,
        user_profile_revision,
        usage,
    )?;
    let speech = if speech_enabled {
        Some(active.lifecycle.speech_requested(
            reply.speech_text,
            character_revision,
            user_profile_revision,
        )?)
    } else {
        None
    };
    session.active_turn = None;
    Ok(match speech {
        Some(speech) => vec![completed, speech],
        None => vec![completed],
    })
}

fn active_turn(session: &Session, turn_id: TurnId) -> Result<&ActiveTurn, FairyError> {
    session
        .active_turn
        .as_ref()
        .filter(|active| active.turn_id == turn_id)
        .ok_or_else(turn_not_active)
}

fn active_turn_mut(session: &mut Session, turn_id: TurnId) -> Result<&mut ActiveTurn, FairyError> {
    session
        .active_turn
        .as_mut()
        .filter(|active| active.turn_id == turn_id)
        .ok_or_else(turn_not_active)
}

fn lock<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex.lock().expect("Harness Runtime mutex poisoned")
}

fn read_lock<T>(lock: &RwLock<T>) -> RwLockReadGuard<'_, T> {
    lock.read().expect("Harness Runtime RwLock poisoned")
}

fn write_lock<T>(lock: &RwLock<T>) -> RwLockWriteGuard<'_, T> {
    lock.write().expect("Harness Runtime RwLock poisoned")
}

fn cache_key(gateway: &(dyn ModelGateway + Send + Sync), cache_key: &str) -> Option<String> {
    gateway
        .capabilities()
        .prompt_cache_key
        .then(|| cache_key.to_owned())
}

fn validate_runtime_model(model: &str) -> Result<(), FairyError> {
    if model.trim().is_empty() || model.chars().any(char::is_control) {
        return Err(FairyError::new(
            ErrorCode::InvalidModelConfig,
            "Harness Runtime 需要有效模型名称",
            false,
        ));
    }
    Ok(())
}

fn turn_in_progress() -> FairyError {
    FairyError::new(
        ErrorCode::TurnInProgress,
        "当前 conversation 已有活动 turn",
        false,
    )
}

fn turn_not_active() -> FairyError {
    FairyError::new(
        ErrorCode::TurnNotActive,
        "指定 turn 不存在或已经结束",
        false,
    )
}

fn turn_interrupted() -> FairyError {
    FairyError::new(ErrorCode::TurnInterrupted, "本轮回复已取消", false)
}
