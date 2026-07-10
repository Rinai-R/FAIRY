use std::collections::HashMap;
use std::sync::{Arc, Mutex, MutexGuard, RwLock, RwLockReadGuard, RwLockWriteGuard};

use fairy_domain::{
    CharacterSnapshot, ConversationId, ErrorCode, FairyError, HarnessEvent, LaneModelUsage,
    ModelStreamEvent, PromptItem, PromptLane, ResponseText, TurnId, TurnLifecycle, TurnState,
    UserProfileSnapshot,
};
use tokio_util::sync::CancellationToken;

use crate::{
    CompactionCandidate, CompactionResult, ConversationHistory, HarnessEventSink,
    InterpretTurnRequest, ModelEventSink, ModelGateway, PromptCompiler, SessionSnapshot,
    TurnOutcome, install_compaction, interpret_turn,
};

pub struct HarnessRuntime {
    gateway_binding: RwLock<GatewayBinding>,
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
}

impl HarnessRuntime {
    pub fn new(
        model: String,
        gateway: Arc<dyn ModelGateway + Send + Sync>,
    ) -> Result<Self, FairyError> {
        validate_runtime_model(&model)?;
        Ok(Self {
            gateway_binding: RwLock::new(GatewayBinding { model, gateway }),
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
        let sessions: Vec<_> = lock(&self.sessions).values().cloned().collect();
        sessions.iter().any(|session| {
            let session = lock(session);
            session.active_turn.is_some() || session.compacting
        })
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
        let turn_id = self.begin_turn(&session, conversation_id, &input)?;
        let result = self
            .run_turn(
                &session,
                conversation_id,
                turn_id,
                input,
                speech_enabled,
                events,
            )
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
                if !sink.output.is_empty() && sink.output != completion.output_text {
                    Err(FairyError::new(
                        ErrorCode::ModelResponseInvalid,
                        "Compactor 流式文本与完成文本不一致",
                        false,
                    ))
                } else {
                    match serde_json::from_str::<CompactionWireOutput>(&completion.output_text) {
                        Ok(output) => {
                            let mut session_guard = lock(&session);
                            install_compaction(
                                &mut session_guard.history,
                                CompactionCandidate {
                                    summary: output.summary,
                                    replacement_items: Vec::new(),
                                },
                                &character,
                                user_profile.as_ref(),
                            )
                        }
                        Err(_) => Err(FairyError::new(
                            ErrorCode::ModelResponseInvalid,
                            "Compactor 返回的 summary 无法解析",
                            false,
                        )),
                    }
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
        session
            .history
            .lane_mut(PromptLane::Interpret)
            .append(PromptItem::UserMessage {
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
        });
        Ok(turn_id)
    }

    async fn run_turn(
        &self,
        session: &Arc<Mutex<Session>>,
        conversation_id: ConversationId,
        turn_id: TurnId,
        input: String,
        speech_enabled: bool,
        events: &mut (dyn HarnessEventSink + Send),
    ) -> Result<TurnOutcome, FairyError> {
        emit_state(session, turn_id, TurnState::Interpreting, events)?;
        let (interpret_input, interpret_key, cancellation, character, user_profile, model, gateway) = {
            let session = lock(session);
            let active = active_turn(&session, turn_id)?;
            let lane = session.history.lane(PromptLane::Interpret);
            (
                lane.items().to_vec(),
                cache_key(active.gateway.as_ref(), lane.cache_key()),
                active.cancellation.clone(),
                active.character.clone(),
                active.user_profile.clone(),
                active.model.clone(),
                Arc::clone(&active.gateway),
            )
        };
        let interpretation = interpret_turn(
            gateway.as_ref(),
            InterpretTurnRequest {
                model: model.clone(),
                input: interpret_input,
                prompt_cache_key: interpret_key,
                user_input: input.clone(),
                character: character.clone(),
                user_profile: user_profile.clone(),
            },
            cancellation.clone(),
        )
        .await?;
        if cancellation.is_cancelled() {
            return Err(turn_interrupted());
        }

        let turn_plan = interpretation.plan.into_inner();
        {
            let mut session = lock(session);
            active_turn(&session, turn_id)?;
            session
                .history
                .lane_mut(PromptLane::Interpret)
                .append(PromptItem::TurnPlan {
                    plan: turn_plan.clone(),
                });
            session
                .history
                .lane_mut(PromptLane::Interpret)
                .seal_current_prefix()?;
            session
                .history
                .lane_mut(PromptLane::Respond)
                .append(PromptItem::UserMessage {
                    content: input.clone(),
                });
            session
                .history
                .lane_mut(PromptLane::Respond)
                .append(PromptItem::TurnPlan {
                    plan: turn_plan.clone(),
                });
        }
        emit_state(session, turn_id, TurnState::Planning, events)?;
        emit_state(session, turn_id, TurnState::Responding, events)?;

        let (respond_request, respond_window) = {
            let session = lock(session);
            let lane = session.history.lane(PromptLane::Respond);
            (
                PromptCompiler.compile(
                    PromptLane::Respond,
                    model,
                    lane.items().to_vec(),
                    cache_key(gateway.as_ref(), lane.cache_key()),
                ),
                lane.window_revision(),
            )
        };
        let mut responder_sink = ResponderEventSink {
            session: Arc::clone(session),
            turn_id,
            cancellation: cancellation.clone(),
            events,
        };
        let response = gateway
            .execute(respond_request, cancellation.clone(), &mut responder_sink)
            .await?;
        if cancellation.is_cancelled() {
            return Err(turn_interrupted());
        }
        let response_text = ResponseText::new(response.output_text.clone())?;

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
                    content: response_text.as_str().to_owned(),
                });
            session
                .history
                .lane_mut(PromptLane::Respond)
                .seal_current_prefix()?;
            (character_revision, user_profile_revision)
        };
        let usage = vec![
            LaneModelUsage {
                lane: PromptLane::Interpret,
                history_window: session_window(session, PromptLane::Interpret),
                usage: interpretation.completion.usage,
            },
            LaneModelUsage {
                lane: PromptLane::Respond,
                history_window: respond_window,
                usage: response.usage,
            },
        ];
        let terminal_events = complete_turn(
            session,
            turn_id,
            response_text.clone(),
            character_revision,
            user_profile_revision,
            usage.clone(),
            speech_enabled,
        )?;
        for event in terminal_events {
            events.send(event)?;
        }

        Ok(TurnOutcome {
            conversation_id,
            turn_id,
            response_text,
            character_revision,
            user_profile_revision,
            usage,
            speech_requested: speech_enabled,
        })
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

struct ResponderEventSink<'a> {
    session: Arc<Mutex<Session>>,
    turn_id: TurnId,
    cancellation: CancellationToken,
    events: &'a mut (dyn HarnessEventSink + Send),
}

#[derive(Default)]
struct CompactionOutputSink {
    output: String,
}

impl ModelEventSink for CompactionOutputSink {
    fn send(&mut self, event: ModelStreamEvent) -> Result<(), FairyError> {
        match event {
            ModelStreamEvent::StructuredTextDelta { delta } => {
                self.output.push_str(&delta);
                Ok(())
            }
            ModelStreamEvent::TextDelta { .. } => Err(FairyError::new(
                ErrorCode::ModelResponseInvalid,
                "Compactor 收到了非结构化文本增量",
                false,
            )),
        }
    }
}

#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct CompactionWireOutput {
    summary: String,
}

impl ModelEventSink for ResponderEventSink<'_> {
    fn send(&mut self, event: ModelStreamEvent) -> Result<(), FairyError> {
        if self.cancellation.is_cancelled() {
            return Err(turn_interrupted());
        }
        let delta = match event {
            ModelStreamEvent::TextDelta { delta } => delta,
            ModelStreamEvent::StructuredTextDelta { .. } => {
                return Err(FairyError::new(
                    ErrorCode::ModelResponseInvalid,
                    "Responder 收到了结构化文本增量",
                    false,
                ));
            }
        };
        let harness_event = {
            let mut session = lock(&self.session);
            active_turn_mut(&mut session, self.turn_id)?
                .lifecycle
                .text_delta(delta)?
        };
        self.events.send(harness_event)
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

fn complete_turn(
    session: &Arc<Mutex<Session>>,
    turn_id: TurnId,
    response_text: ResponseText,
    character_revision: fairy_domain::Revision,
    user_profile_revision: Option<fairy_domain::Revision>,
    usage: Vec<LaneModelUsage>,
    speech_enabled: bool,
) -> Result<Vec<HarnessEvent>, FairyError> {
    let mut session = lock(session);
    let active = active_turn_mut(&mut session, turn_id)?;
    let completed = active.lifecycle.complete(
        response_text.clone(),
        character_revision,
        user_profile_revision,
        usage,
    )?;
    let speech = if speech_enabled {
        Some(active.lifecycle.speech_requested(
            response_text,
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

fn session_window(session: &Arc<Mutex<Session>>, lane: PromptLane) -> fairy_domain::WindowRevision {
    lock(session).history.lane(lane).window_revision()
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
