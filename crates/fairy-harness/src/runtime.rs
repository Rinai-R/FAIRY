use std::collections::{BTreeSet, HashMap};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex, MutexGuard, RwLock, RwLockReadGuard, RwLockWriteGuard};

use fairy_domain::{
    CharacterSnapshot, CompiledReply, CompiledReplyChain, ConversationBootstrap, ConversationId,
    ErrorCode, ExtractionBatchInput, FairyError, HarnessEvent, LaneModelUsage,
    MemoryMutationOutput, ModelCompletion, ModelStreamEvent, ModelTurnOutput, PromptItem,
    PromptLane, TurnCompletion, TurnId, TurnLifecycle, TurnState, UserProfileSnapshot,
    VisualStatePromptEntry,
};
use tokio_util::sync::CancellationToken;

use crate::{
    CompactionCandidate, CompactionPolicy, CompactionResult, CompactionTrigger,
    ConversationHistory, HarnessEventSink, ModelEventSink, ModelGateway, PersistenceBinding,
    PromptCompiler, ReplyCompiler, SessionSnapshot, TurnOutcome, install_compaction,
};

pub struct HarnessRuntime {
    gateway_binding: RwLock<GatewayBinding>,
    compaction_policy: RwLock<CompactionPolicy>,
    persistence_binding: RwLock<PersistenceBinding>,
    background_jobs: Arc<AtomicUsize>,
    intelligence_background_error: Arc<Mutex<Option<FairyError>>>,
    extraction_idle_tokens: Mutex<HashMap<ConversationId, CancellationToken>>,
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
    persistence_binding: PersistenceBinding,
    ephemeral_context: Option<PromptItem>,
    available_visual_states: Vec<VisualStatePromptEntry>,
}

struct CompactionWork {
    session: Arc<Mutex<Session>>,
    conversation_id: ConversationId,
    request: fairy_domain::CompiledPromptRequest,
    gateway: Arc<dyn ModelGateway + Send + Sync>,
    character: CharacterSnapshot,
    user_profile: Option<UserProfileSnapshot>,
    persistence: PersistenceBinding,
    expected_revision: fairy_domain::WindowRevision,
}

const EXTRACTION_THRESHOLD: u64 = 6;
const EXTRACTION_BATCH_LIMIT: usize = 12;
const EXTRACTION_IDLE_SECONDS: u64 = 30;

impl HarnessRuntime {
    pub fn new(
        model: String,
        gateway: Arc<dyn ModelGateway + Send + Sync>,
    ) -> Result<Self, FairyError> {
        Self::new_with_compaction_policy(model, gateway, CompactionPolicy::default())
    }

    pub fn new_with_compaction_policy(
        model: String,
        gateway: Arc<dyn ModelGateway + Send + Sync>,
        compaction_policy: CompactionPolicy,
    ) -> Result<Self, FairyError> {
        validate_runtime_model(&model)?;
        Ok(Self {
            gateway_binding: RwLock::new(GatewayBinding { model, gateway }),
            compaction_policy: RwLock::new(compaction_policy),
            persistence_binding: RwLock::new(PersistenceBinding::Disabled),
            background_jobs: Arc::new(AtomicUsize::new(0)),
            intelligence_background_error: Arc::new(Mutex::new(None)),
            extraction_idle_tokens: Mutex::new(HashMap::new()),
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

    pub fn replace_gateway_with_compaction_policy(
        &self,
        model: String,
        gateway: Arc<dyn ModelGateway + Send + Sync>,
        compaction_policy: CompactionPolicy,
    ) -> Result<(), FairyError> {
        validate_runtime_model(&model)?;
        *write_lock(&self.gateway_binding) = GatewayBinding { model, gateway };
        *write_lock(&self.compaction_policy) = compaction_policy;
        Ok(())
    }

    pub fn replace_persistence_binding(&self, binding: PersistenceBinding) {
        *write_lock(&self.persistence_binding) = binding;
    }

    pub async fn open_or_create_character_session(
        &self,
        character: CharacterSnapshot,
        user_profile: Option<UserProfileSnapshot>,
    ) -> Result<ConversationBootstrap, FairyError> {
        character.verify_integrity()?;
        if let Some(profile) = user_profile.as_ref() {
            profile.verify_integrity()?;
        }
        if self.has_active_session_turn() {
            return Err(turn_in_progress());
        }
        let binding = read_lock(&self.persistence_binding).clone();
        let persistence = available_persistence(&binding)?;
        let bootstrap = persistence
            .open_or_create_character_conversation(character.character_id())
            .await?;
        let history = ConversationHistory::restore(&bootstrap, &character, user_profile.as_ref())?;
        let conversation_id = bootstrap.conversation.id;
        let session = Session {
            history,
            character: Some(character),
            user_profile,
            pending_user_profile: None,
            active_turn: None,
            compacting: false,
        };
        lock(&self.sessions).insert(conversation_id, Arc::new(Mutex::new(session)));
        Ok(bootstrap)
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

    fn has_active_session_turn(&self) -> bool {
        lock(&self.sessions)
            .values()
            .any(|session| lock(session).active_turn.is_some())
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

    pub fn update_user_profile_for_character(
        &self,
        character_id: fairy_domain::CharacterId,
        snapshot: UserProfileSnapshot,
    ) -> Result<bool, FairyError> {
        snapshot.verify_integrity()?;
        let session = lock(&self.sessions)
            .values()
            .find(|session| {
                lock(session)
                    .character
                    .as_ref()
                    .is_some_and(|character| character.character_id() == character_id)
            })
            .cloned();
        let Some(session) = session else {
            return Ok(false);
        };
        let conversation_id = lock(&session)
            .history
            .lane(PromptLane::Respond)
            .conversation_id();
        self.update_user_profile(conversation_id, snapshot)
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
        self.submit_turn_with_visual_states(
            conversation_id,
            input,
            speech_enabled,
            idle_visual_states(),
            events,
        )
        .await
    }

    pub async fn submit_turn_with_visual_states(
        &self,
        conversation_id: ConversationId,
        input: String,
        speech_enabled: bool,
        available_visual_states: Vec<VisualStatePromptEntry>,
        events: &mut (dyn HarnessEventSink + Send),
    ) -> Result<TurnOutcome, FairyError> {
        if input.trim().is_empty() {
            return Err(FairyError::new(
                ErrorCode::InvalidEventPayload,
                "对话输入不能为空",
                false,
            ));
        }
        validate_available_visual_states(&available_visual_states)?;
        let session = self.session(conversation_id)?;
        let persistence = read_lock(&self.persistence_binding).clone();
        let (character_id, query) = retrieval_query(&session, &input)?;
        let context_item = retrieve_context_item(&persistence, character_id, &query).await;
        let turn_id = self.prepare_turn(
            &session,
            conversation_id,
            persistence.clone(),
            available_visual_states,
        )?;
        if let Err(error) =
            persist_user_turn(&persistence, conversation_id, turn_id, input.clone()).await
        {
            self.discard_prepared_turn(&session, turn_id);
            return Err(error);
        }
        self.install_user_turn(&session, turn_id, &input, context_item)?;
        let result = self
            .run_turn(&session, conversation_id, turn_id, speech_enabled, events)
            .await;
        match result {
            Ok(outcome) => Ok(outcome),
            Err(error) => Err(self
                .finish_error(&session, conversation_id, turn_id, error, events)
                .await),
        }
    }

    pub async fn compact_conversation(
        &self,
        conversation_id: ConversationId,
    ) -> Result<CompactionResult, FairyError> {
        let session = self.session(conversation_id)?;
        let (request, gateway, character, user_profile, persistence, expected_revision) = {
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
            let expected_revision = lane.window_revision();
            let request = PromptCompiler.compile(
                PromptLane::Compact,
                binding.model,
                lane.items().to_vec(),
                cache_key(binding.gateway.as_ref(), lane.cache_key()),
            );
            session.compacting = true;
            (
                request,
                binding.gateway,
                character,
                user_profile,
                read_lock(&self.persistence_binding).clone(),
                expected_revision,
            )
        };

        let mut sink = CompactionOutputSink::default();
        let completion = gateway
            .execute(request, CancellationToken::new(), &mut sink)
            .await;
        let result = async {
            match completion {
                Ok(completion) => {
                    let output_text = completion.output.into_text()?;
                    if !sink.output.is_empty() && sink.output != output_text {
                        Err(FairyError::new(
                            ErrorCode::ModelResponseInvalid,
                            "Compactor 流式文本与完成文本不一致",
                            false,
                        ))
                    } else {
                        let (candidate_history, result, summary) = {
                            let session_guard = lock(&session);
                            let mut candidate_history = session_guard.history.clone();
                            let result = install_compaction(
                                &mut candidate_history,
                                CompactionCandidate {
                                    summary: output_text,
                                    replacement_items: Vec::new(),
                                },
                                &character,
                                user_profile.as_ref(),
                            )?;
                            let summary = candidate_history
                                .lane(PromptLane::Respond)
                                .items()
                                .iter()
                                .rev()
                                .find_map(|item| match item {
                                    PromptItem::CompactionSummary { summary } => {
                                        Some(summary.clone())
                                    }
                                    _ => None,
                                })
                                .ok_or_else(|| {
                                    FairyError::new(
                                        ErrorCode::CompactionFailed,
                                        "候选窗口缺少 compaction summary",
                                        false,
                                    )
                                })?;
                            (candidate_history, result, summary)
                        };
                        let persisted = persist_prompt_window(
                            &persistence,
                            conversation_id,
                            expected_revision,
                            summary,
                        )
                        .await?;
                        if persisted.revision != result.window_revision {
                            return Err(FairyError::new(
                                ErrorCode::CompactionFailed,
                                "持久 window revision 与候选 revision 不一致",
                                false,
                            ));
                        }
                        let mut session_guard = lock(&session);
                        if session_guard
                            .history
                            .lane(PromptLane::Respond)
                            .window_revision()
                            != expected_revision
                        {
                            return Err(FairyError::new(
                                ErrorCode::CompactionFailed,
                                "内存 window revision 在提交期间发生变化",
                                false,
                            ));
                        }
                        session_guard.history = candidate_history;
                        Ok(result)
                    }
                }
                Err(error) => Err(error),
            }
        }
        .await;
        lock(&session).compacting = false;
        result
    }

    fn prepare_turn(
        &self,
        session: &Arc<Mutex<Session>>,
        conversation_id: ConversationId,
        persistence_binding: PersistenceBinding,
        available_visual_states: Vec<VisualStatePromptEntry>,
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
        session.active_turn = Some(ActiveTurn {
            turn_id,
            lifecycle: TurnLifecycle::new(conversation_id, turn_id),
            cancellation,
            character,
            user_profile,
            model: binding.model,
            gateway: binding.gateway,
            persistence_binding,
            ephemeral_context: None,
            available_visual_states,
        });
        Ok(turn_id)
    }

    fn install_user_turn(
        &self,
        session: &Arc<Mutex<Session>>,
        turn_id: TurnId,
        input: &str,
        context_item: Option<PromptItem>,
    ) -> Result<(), FairyError> {
        let mut session = lock(session);
        active_turn_mut(&mut session, turn_id)?.ephemeral_context = context_item;
        let respond = session.history.lane_mut(PromptLane::Respond);
        respond.append(PromptItem::UserMessage {
            content: input.to_owned(),
        });
        Ok(())
    }

    fn discard_prepared_turn(&self, session: &Arc<Mutex<Session>>, turn_id: TurnId) {
        let mut session = lock(session);
        if session
            .active_turn
            .as_ref()
            .is_some_and(|active| active.turn_id == turn_id)
        {
            session.active_turn = None;
        }
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
        let (cancellation, model, gateway, persistence_binding) = {
            let session = lock(session);
            let active = active_turn(&session, turn_id)?;
            (
                active.cancellation.clone(),
                active.model.clone(),
                Arc::clone(&active.gateway),
                active.persistence_binding.clone(),
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
            cancellation.clone(),
        )
        .await?;
        persist_assistant_turn(
            &persistence_binding,
            conversation_id,
            turn_id,
            compiled.display_text.as_str().to_owned(),
        )
        .await?;
        emit_reply_chains(session, turn_id, &compiled.chains, events)?;

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

        self.schedule_background_extraction(persistence_binding, gateway, model, conversation_id)
            .await;
        let respond_usage = usage
            .iter()
            .find(|entry| entry.lane == PromptLane::Respond)
            .map(|entry| &entry.usage);
        let compaction_policy = *read_lock(&self.compaction_policy);
        if compaction_policy.should_compact(CompactionTrigger::AfterCompletedTurn, respond_usage) {
            self.schedule_auto_compaction(conversation_id);
        }

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
            visual_state: compiled.visual_state,
            chains: compiled.chains,
        })
    }

    async fn schedule_background_extraction(
        &self,
        binding: PersistenceBinding,
        gateway: Arc<dyn ModelGateway + Send + Sync>,
        model: String,
        conversation_id: ConversationId,
    ) {
        let PersistenceBinding::Available(persistence) = binding else {
            return;
        };
        if let Some(previous) = lock(&self.extraction_idle_tokens).remove(&conversation_id) {
            previous.cancel();
        }
        let pending = match persistence
            .pending_extraction_turn_count(conversation_id)
            .await
        {
            Ok(pending) => pending,
            Err(error) => {
                *lock(&self.intelligence_background_error) = Some(error);
                return;
            }
        };
        let jobs = Arc::clone(&self.background_jobs);
        let background_error = Arc::clone(&self.intelligence_background_error);
        if pending >= EXTRACTION_THRESHOLD {
            claim_and_spawn_extraction(
                persistence,
                gateway,
                model,
                conversation_id,
                jobs,
                background_error,
            )
            .await;
            return;
        }
        if pending == 0 {
            return;
        }
        let idle = CancellationToken::new();
        lock(&self.extraction_idle_tokens).insert(conversation_id, idle.clone());
        tokio::spawn(async move {
            tokio::select! {
                () = idle.cancelled() => {}
                () = tokio::time::sleep(std::time::Duration::from_secs(EXTRACTION_IDLE_SECONDS)) => {
                    claim_and_spawn_extraction(
                        persistence,
                        gateway,
                        model,
                        conversation_id,
                        jobs,
                        background_error,
                    ).await;
                }
            }
        });
    }

    fn schedule_auto_compaction(&self, conversation_id: ConversationId) {
        let work = match self.prepare_compaction_work(conversation_id) {
            Ok(work) => work,
            Err(error) => {
                *lock(&self.intelligence_background_error) = Some(error);
                return;
            }
        };
        let jobs = Arc::clone(&self.background_jobs);
        let background_error = Arc::clone(&self.intelligence_background_error);
        jobs.fetch_add(1, Ordering::AcqRel);
        tokio::spawn(async move {
            let _guard = BackgroundJobGuard::new(Arc::clone(&jobs));
            match execute_compaction_work(work).await {
                Ok(_) => *lock(&background_error) = None,
                Err(error) => *lock(&background_error) = Some(error),
            }
        });
    }

    fn prepare_compaction_work(
        &self,
        conversation_id: ConversationId,
    ) -> Result<CompactionWork, FairyError> {
        let session = self.session(conversation_id)?;
        let (request, gateway, character, user_profile, expected_revision) = {
            let mut session_guard = lock(&session);
            if session_guard.active_turn.is_some() || session_guard.compacting {
                return Err(turn_in_progress());
            }
            let character = session_guard.character.clone().ok_or_else(|| {
                FairyError::new(
                    ErrorCode::CharacterNotAvailable,
                    "压缩会话前必须激活一个有效角色",
                    false,
                )
            })?;
            let user_profile = session_guard.user_profile.clone();
            let binding = read_lock(&self.gateway_binding).clone();
            let lane = session_guard.history.lane(PromptLane::Compact);
            let expected_revision = lane.window_revision();
            let request = PromptCompiler.compile(
                PromptLane::Compact,
                binding.model,
                lane.items().to_vec(),
                cache_key(binding.gateway.as_ref(), lane.cache_key()),
            );
            session_guard.compacting = true;
            (
                request,
                binding.gateway,
                character,
                user_profile,
                expected_revision,
            )
        };
        Ok(CompactionWork {
            session,
            conversation_id,
            request,
            gateway,
            character,
            user_profile,
            persistence: read_lock(&self.persistence_binding).clone(),
            expected_revision,
        })
    }

    async fn finish_error(
        &self,
        session: &Arc<Mutex<Session>>,
        conversation_id: ConversationId,
        turn_id: TurnId,
        error: FairyError,
        events: &mut (dyn HarnessEventSink + Send),
    ) -> FairyError {
        let (binding, state, persisted_error) = {
            let session = lock(session);
            let Some(active) = session.active_turn.as_ref() else {
                return error;
            };
            if active.turn_id != turn_id || active.lifecycle.state().is_terminal() {
                return error;
            }
            let interrupted =
                error.code == ErrorCode::TurnInterrupted || active.cancellation.is_cancelled();
            (
                active.persistence_binding.clone(),
                if interrupted {
                    TurnState::Interrupted
                } else {
                    TurnState::Failed
                },
                (!interrupted).then(|| error.clone()),
            )
        };
        let effective_error = match persist_terminated_turn(
            &binding,
            conversation_id,
            turn_id,
            state,
            persisted_error,
        )
        .await
        {
            Ok(()) => error,
            Err(storage_error) => storage_error,
        };
        let event = {
            let mut session = lock(session);
            let Some(active) = session.active_turn.as_mut() else {
                return effective_error;
            };
            if active.turn_id != turn_id || active.lifecycle.state().is_terminal() {
                session.active_turn = None;
                return effective_error;
            }
            let event = if state == TurnState::Interrupted {
                active.lifecycle.transition(TurnState::Interrupted)
            } else {
                active.lifecycle.fail(effective_error.clone())
            };
            session.active_turn = None;
            event.ok()
        };
        if let Some(event) = event {
            let _ = events.send(event);
        }
        effective_error
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

async fn execute_compaction_work(work: CompactionWork) -> Result<CompactionResult, FairyError> {
    let CompactionWork {
        session,
        conversation_id,
        request,
        gateway,
        character,
        user_profile,
        persistence,
        expected_revision,
    } = work;
    let result = async {
        let mut sink = CompactionOutputSink::default();
        let completion = gateway
            .execute(request, CancellationToken::new(), &mut sink)
            .await?;
        let output_text = completion.output.into_text()?;
        if !sink.output.is_empty() && sink.output != output_text {
            return Err(FairyError::new(
                ErrorCode::ModelResponseInvalid,
                "Compactor 流式文本与完成文本不一致",
                false,
            ));
        }
        let (candidate_history, result, summary) = {
            let session_guard = lock(&session);
            let mut candidate_history = session_guard.history.clone();
            let result = install_compaction(
                &mut candidate_history,
                CompactionCandidate {
                    summary: output_text,
                    replacement_items: Vec::new(),
                },
                &character,
                user_profile.as_ref(),
            )?;
            let summary = candidate_history
                .lane(PromptLane::Respond)
                .items()
                .iter()
                .rev()
                .find_map(|item| match item {
                    PromptItem::CompactionSummary { summary } => Some(summary.clone()),
                    _ => None,
                })
                .ok_or_else(|| {
                    FairyError::new(
                        ErrorCode::CompactionFailed,
                        "候选窗口缺少 compaction summary",
                        false,
                    )
                })?;
            (candidate_history, result, summary)
        };
        let persisted =
            persist_prompt_window(&persistence, conversation_id, expected_revision, summary)
                .await?;
        if persisted.revision != result.window_revision {
            return Err(FairyError::new(
                ErrorCode::CompactionFailed,
                "持久 window revision 与候选 revision 不一致",
                false,
            ));
        }
        let mut session_guard = lock(&session);
        if session_guard
            .history
            .lane(PromptLane::Respond)
            .window_revision()
            != expected_revision
        {
            return Err(FairyError::new(
                ErrorCode::CompactionFailed,
                "内存 window revision 在提交期间发生变化",
                false,
            ));
        }
        session_guard.history = candidate_history;
        Ok(result)
    }
    .await;
    lock(&session).compacting = false;
    result
}

async fn retrieve_context_item(
    binding: &PersistenceBinding,
    character_id: fairy_domain::CharacterId,
    input: &str,
) -> Option<PromptItem> {
    match binding {
        PersistenceBinding::Disabled => None,
        PersistenceBinding::Unavailable(_) => None,
        PersistenceBinding::Available(intelligence) => {
            match intelligence.retrieve(character_id, input.to_owned()).await {
                Ok(context) if context.is_empty() => None,
                Ok(context) => Some(PromptItem::RetrievedContext { context }),
                Err(_) => None,
            }
        }
    }
}

fn available_persistence(
    binding: &PersistenceBinding,
) -> Result<Arc<dyn crate::CompanionPersistence + Send + Sync>, FairyError> {
    match binding {
        PersistenceBinding::Available(persistence) => Ok(Arc::clone(persistence)),
        PersistenceBinding::Unavailable(error) => Err(error.clone()),
        PersistenceBinding::Disabled => Err(FairyError::new(
            ErrorCode::IntelligenceUnavailable,
            "持久会话存储未绑定",
            false,
        )),
    }
}

async fn persist_user_turn(
    binding: &PersistenceBinding,
    conversation_id: ConversationId,
    turn_id: TurnId,
    user_message: String,
) -> Result<(), FairyError> {
    match binding {
        PersistenceBinding::Available(persistence) => {
            persistence
                .begin_turn(conversation_id, turn_id, user_message)
                .await
        }
        PersistenceBinding::Disabled => Ok(()),
        PersistenceBinding::Unavailable(error) => Err(error.clone()),
    }
}

async fn persist_assistant_turn(
    binding: &PersistenceBinding,
    conversation_id: ConversationId,
    turn_id: TurnId,
    assistant_message: String,
) -> Result<(), FairyError> {
    match binding {
        PersistenceBinding::Available(persistence) => {
            persistence
                .complete_turn(conversation_id, turn_id, assistant_message)
                .await
        }
        PersistenceBinding::Disabled => Ok(()),
        PersistenceBinding::Unavailable(error) => Err(error.clone()),
    }
}

async fn persist_terminated_turn(
    binding: &PersistenceBinding,
    conversation_id: ConversationId,
    turn_id: TurnId,
    state: TurnState,
    error: Option<FairyError>,
) -> Result<(), FairyError> {
    match binding {
        PersistenceBinding::Available(persistence) => {
            persistence
                .terminate_turn(conversation_id, turn_id, state, error)
                .await
        }
        PersistenceBinding::Disabled => Ok(()),
        PersistenceBinding::Unavailable(error) => Err(error.clone()),
    }
}

async fn persist_prompt_window(
    binding: &PersistenceBinding,
    conversation_id: ConversationId,
    expected_revision: fairy_domain::WindowRevision,
    summary: String,
) -> Result<fairy_domain::PromptWindowRecord, FairyError> {
    match binding {
        PersistenceBinding::Available(persistence) => {
            persistence
                .commit_prompt_window(conversation_id, expected_revision, summary)
                .await
        }
        PersistenceBinding::Disabled => Err(FairyError::new(
            ErrorCode::IntelligenceUnavailable,
            "持久 prompt window 存储未绑定",
            false,
        )),
        PersistenceBinding::Unavailable(error) => Err(error.clone()),
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

async fn claim_and_spawn_extraction(
    persistence: Arc<dyn crate::CompanionPersistence + Send + Sync>,
    gateway: Arc<dyn ModelGateway + Send + Sync>,
    model: String,
    conversation_id: ConversationId,
    jobs: Arc<AtomicUsize>,
    background_error: Arc<Mutex<Option<FairyError>>>,
) {
    let batch = match persistence
        .claim_extraction_batch(conversation_id, EXTRACTION_BATCH_LIMIT)
        .await
    {
        Ok(Some(batch)) => batch,
        Ok(None) => return,
        Err(error) => {
            *lock(&background_error) = Some(error);
            return;
        }
    };
    jobs.fetch_add(1, Ordering::AcqRel);
    tokio::spawn(async move {
        let _guard = BackgroundJobGuard::new(Arc::clone(&jobs));
        let batch_id = batch.batch_id;
        let result = run_background_extraction(persistence.as_ref(), gateway, model, batch).await;
        match result {
            Ok(()) => *lock(&background_error) = None,
            Err(error) => {
                let recorded = match persistence
                    .fail_extraction_batch(batch_id, error.clone())
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

async fn run_background_extraction(
    persistence: &(dyn crate::CompanionPersistence + Send + Sync),
    gateway: Arc<dyn ModelGateway + Send + Sync>,
    model: String,
    input: ExtractionBatchInput,
) -> Result<(), FairyError> {
    let raw_cache_key = format!("fairy:{}:extract", input.conversation_id);
    let request = PromptCompiler.compile(
        PromptLane::Extract,
        model,
        vec![PromptItem::ExtractionBatch {
            input: input.clone(),
        }],
        cache_key(gateway.as_ref(), &raw_cache_key),
    );
    let mut sink = BufferedOutputSink::default();
    let completion = gateway
        .execute(request, CancellationToken::new(), &mut sink)
        .await?;
    let text = completion.output.into_text()?;
    if !sink.output.is_empty() && sink.output != text {
        return Err(FairyError::new(
            ErrorCode::ExtractionBatchFailed,
            "抽取模型的流式文本与完成文本不一致",
            false,
        ));
    }
    let output: MemoryMutationOutput = serde_json::from_str(&text).map_err(|_| {
        FairyError::new(
            ErrorCode::ExtractionBatchFailed,
            "抽取模型没有返回严格 MemoryMutationOutput JSON",
            false,
        )
    })?;
    if output.mutations.len() > 16 {
        return Err(FairyError::new(
            ErrorCode::ExtractionBatchFailed,
            "单批次 memory mutation 超过 16 条上限",
            false,
        ));
    }
    for mutation in &output.mutations {
        mutation.verify_integrity()?;
    }
    let allowed_memory_ids = input
        .existing_memories
        .iter()
        .map(|memory| memory.id)
        .collect();
    persistence
        .commit_memory_mutations(
            input.batch_id,
            input.character_id,
            allowed_memory_ids,
            output.mutations,
        )
        .await
        .map(|_| ())
}

async fn execute_respond_loop(
    session: &Arc<Mutex<Session>>,
    turn_id: TurnId,
    model: String,
    gateway: Arc<dyn ModelGateway + Send + Sync>,
    cancellation: CancellationToken,
) -> Result<(CompiledReply, Vec<LaneModelUsage>), FairyError> {
    if cancellation.is_cancelled() {
        return Err(turn_interrupted());
    }
    let (request, window_revision, available_visual_states) = {
        let session = lock(session);
        let active = active_turn(&session, turn_id)?;
        let lane = session.history.lane(PromptLane::Respond);
        let mut input = lane.items().to_vec();
        let mut insertion_index = input.len().checked_sub(1).ok_or_else(|| {
            FairyError::new(
                ErrorCode::PromptHistoryInvalid,
                "当前 turn 缺少用户消息",
                false,
            )
        })?;
        if let Some(context) = active.ephemeral_context.clone() {
            input.insert(insertion_index, context);
            insertion_index += 1;
        }
        input.insert(
            insertion_index,
            PromptItem::AvailableVisualStates {
                states: active.available_visual_states.clone(),
            },
        );
        (
            PromptCompiler.compile(
                PromptLane::Respond,
                model,
                input,
                cache_key(gateway.as_ref(), lane.cache_key()),
            ),
            lane.window_revision(),
            active.available_visual_states.clone(),
        )
    };
    let mut sink = BufferedOutputSink::default();
    let ModelCompletion {
        output,
        usage: model_usage,
        ..
    } = gateway
        .execute(request, cancellation.clone(), &mut sink)
        .await?;
    if cancellation.is_cancelled() {
        return Err(turn_interrupted());
    }
    let ModelTurnOutput::Text { text } = output;
    if !sink.output.is_empty() && sink.output != text {
        return Err(FairyError::new(
            ErrorCode::ModelResponseInvalid,
            "Responder 流式文本与完成文本不一致",
            false,
        ));
    }
    let reply = ReplyCompiler.compile(&text, Vec::new(), &available_visual_states)?;
    Ok((
        reply,
        vec![LaneModelUsage {
            lane: PromptLane::Respond,
            history_window: window_revision,
            usage: model_usage,
        }],
    ))
}

fn retrieval_query(
    session: &Arc<Mutex<Session>>,
    current_input: &str,
) -> Result<(fairy_domain::CharacterId, String), FairyError> {
    let session = lock(session);
    if session.active_turn.is_some() || session.compacting {
        return Err(turn_in_progress());
    }
    let character_id = session
        .character
        .as_ref()
        .ok_or_else(|| {
            FairyError::new(
                ErrorCode::CharacterNotAvailable,
                "检索长期记忆前必须激活有效角色",
                false,
            )
        })?
        .character_id();
    let mut complete_turns = Vec::new();
    let mut pending_user = None;
    for item in session.history.lane(PromptLane::Respond).items() {
        match item {
            PromptItem::UserMessage { content } => pending_user = Some(content.as_str()),
            PromptItem::AssistantMessage { content } => {
                if let Some(user) = pending_user.take() {
                    complete_turns.push((user, content.as_str()));
                }
            }
            _ => {}
        }
    }
    let mut query_parts = Vec::new();
    for (user, assistant) in complete_turns.iter().rev().take(2).rev() {
        query_parts.push(*user);
        query_parts.push(*assistant);
    }
    query_parts.push(current_input);
    Ok((character_id, query_parts.join(" ")))
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

fn emit_reply_chains(
    session: &Arc<Mutex<Session>>,
    turn_id: TurnId,
    chains: &[CompiledReplyChain],
    events: &mut (dyn HarnessEventSink + Send),
) -> Result<(), FairyError> {
    for (index, chain) in chains.iter().enumerate() {
        let delta = if index == 0 {
            chain.text.as_str().to_owned()
        } else {
            format!("\n{}", chain.text.as_str())
        };
        let event = {
            let mut session = lock(session);
            active_turn_mut(&mut session, turn_id)?
                .lifecycle
                .reply_chain(index as u8, delta, chain.clone())?
        };
        events.send(event)?;
    }
    Ok(())
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
    let completed = active.lifecycle.complete(TurnCompletion {
        text: reply.display_text,
        speech_text: reply.speech_text.clone(),
        sources: reply.sources,
        character_revision,
        user_profile_revision,
        usage,
        visual_state: reply.visual_state.clone(),
        chains: reply.chains,
    })?;
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

fn idle_visual_states() -> Vec<VisualStatePromptEntry> {
    vec![VisualStatePromptEntry {
        id: "idle".parse().expect("idle visual state"),
        description: "安静待机，适合普通回复。".to_owned(),
    }]
}

fn validate_available_visual_states(states: &[VisualStatePromptEntry]) -> Result<(), FairyError> {
    if states.is_empty() || states.len() > 16 {
        return Err(FairyError::new(
            ErrorCode::InvalidEventPayload,
            "可用视觉状态清单必须包含 1-16 个状态",
            false,
        ));
    }
    let mut ids = BTreeSet::new();
    let mut has_idle = false;
    for state in states {
        if !ids.insert(state.id.as_str()) {
            return Err(FairyError::new(
                ErrorCode::InvalidEventPayload,
                "可用视觉状态清单包含重复状态",
                false,
            ));
        }
        has_idle |= state.id.as_str() == "idle";
        let description_length = state.description.chars().count();
        if description_length == 0
            || description_length > 96
            || state.description.trim() != state.description
            || state.description.chars().any(char::is_control)
        {
            return Err(FairyError::new(
                ErrorCode::InvalidEventPayload,
                "可用视觉状态描述无效",
                false,
            ));
        }
    }
    if !has_idle {
        return Err(FairyError::new(
            ErrorCode::InvalidEventPayload,
            "可用视觉状态清单必须包含 idle",
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
