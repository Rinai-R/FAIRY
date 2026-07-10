use std::collections::VecDeque;
use std::sync::{Arc, Mutex, MutexGuard};

use async_trait::async_trait;
use fairy_domain::{
    AmbiguityHandling, CachedTokenObservation, CharacterBriefInput, CharacterCompiler, CharacterId,
    CharacterPerspective, CompiledPromptRequest, ConversationGoal, DIALOGUE_POLICY_VERSION,
    ErrorCode, EvidenceReference, FactCommitment, FairyError, GatewayCapabilities, HarnessEvent,
    HarnessEventPayload, InteractionHypothesis, ModelCompletion, ModelOutputContract,
    ModelStreamEvent, ModelUsage, PromptItem, PromptLane, RelationshipIntent, ResponseAction,
    ResponseLength, Revision, TurnPlan, TurnPolicy, TurnState, UserProfileCompiler,
    UserProfileInput, UserProfileSnapshot,
};
use fairy_harness::{HarnessEventSink, HarnessRuntime, ModelEventSink, ModelGateway};
use tokio::sync::Notify;
use tokio_util::sync::CancellationToken;

enum FakeBehavior {
    Complete { output: String, deltas: Vec<String> },
    WaitAfterTextDelta { delta: String },
    FailAfterTextDelta { delta: String },
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
                    let event = match request.shape.output {
                        ModelOutputContract::Text => ModelStreamEvent::TextDelta { delta },
                        ModelOutputContract::JsonSchema { .. } => {
                            ModelStreamEvent::StructuredTextDelta { delta }
                        }
                    };
                    sink.send(event)?;
                }
                Ok(completion(output))
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
        response_items: vec![PromptItem::AssistantMessage {
            content: output.clone(),
        }],
        output_text: output,
        usage: ModelUsage {
            input_tokens: Some(20),
            output_tokens: Some(8),
            cached_input_tokens: CachedTokenObservation::Observed(4),
            cache_write_tokens: CachedTokenObservation::Missing,
        },
    }
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
    CharacterCompiler
        .compile(
            CharacterId::new(),
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

fn plan(user_input: &str) -> TurnPlan {
    TurnPlan {
        interaction_hypothesis: InteractionHypothesis {
            explicit_request: "回应本轮输入".to_owned(),
            goal: ConversationGoal::CasualConversation,
            evidence: vec![EvidenceReference {
                quote: user_input.to_owned(),
            }],
            confidence: 90,
            ambiguity: None,
        },
        character_perspective: CharacterPerspective {
            attention_focus: vec!["用户明确表达".to_owned()],
            relationship_intent: RelationshipIntent::Companion,
            candidate_actions: vec![ResponseAction::ShareLightReaction],
            character_intensity: 55,
        },
        turn_policy: TurnPolicy {
            policy_version: DIALOGUE_POLICY_VERSION.to_owned(),
            primary_action: ResponseAction::ShareLightReaction,
            secondary_action: None,
            use_preferred_name: false,
            response_length: ResponseLength::Brief,
            fact_commitment: FactCommitment::EvidenceBound,
            ambiguity_handling: AmbiguityHandling::ProceedWithExplicitRequest,
        },
    }
}

fn plan_behavior(user_input: &str) -> FakeBehavior {
    let output = serde_json::to_string(&plan(user_input)).expect("serialize test plan");
    FakeBehavior::Complete {
        deltas: vec![output.clone()],
        output,
    }
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
async fn normal_turn_has_ordered_states_deltas_usage_and_single_terminal() {
    let gateway = Arc::new(FakeGateway::new(vec![
        plan_behavior("你好"),
        response_behavior("你好呀。", &["你好", "呀。"]),
    ]));
    let (runtime, conversation_id, _) = setup(gateway.clone());
    let mut sink = RecordingSink::default();

    let outcome = runtime
        .submit_turn(conversation_id, "你好".to_owned(), true, &mut sink)
        .await
        .expect("complete normal turn");
    let events = sink.events();

    assert_eq!(outcome.response_text.as_str(), "你好呀。");
    assert_eq!(outcome.usage.len(), 2);
    assert_eq!(
        events
            .iter()
            .map(|event| event.sequence)
            .collect::<Vec<_>>(),
        vec![1, 2, 3, 4, 5, 6, 7]
    );
    assert_eq!(
        events.iter().map(|event| event.state).collect::<Vec<_>>(),
        vec![
            TurnState::Interpreting,
            TurnState::Planning,
            TurnState::Responding,
            TurnState::Responding,
            TurnState::Responding,
            TurnState::Completed,
            TurnState::Completed,
        ]
    );
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
    assert_eq!(completed, speech);
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
        vec![PromptLane::Interpret, PromptLane::Respond]
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
        plan_behavior("第一轮"),
        FakeBehavior::WaitAfterTextDelta {
            delta: "部分".to_owned(),
        },
        plan_behavior("第二轮"),
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
async fn active_turn_rejects_second_submit_and_role_switch_but_queues_profile_revision() {
    let gateway = Arc::new(FakeGateway::new(vec![
        plan_behavior("等待中的轮次"),
        FakeBehavior::WaitAfterTextDelta {
            delta: "等待".to_owned(),
        },
        plan_behavior("更新后的轮次"),
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
    let next_interpret = requests
        .iter()
        .rev()
        .find(|request| request.shape.lane == PromptLane::Interpret)
        .expect("next interpret request");
    assert!(next_interpret.input.iter().any(|item| matches!(
        item,
        PromptItem::UserProfileUpdated { snapshot: Some(snapshot) }
            if snapshot.revision().get() == 2 && snapshot.preferred_name() == Some("凛")
    )));
}

#[tokio::test]
async fn stream_failure_after_partial_text_has_failed_terminal_not_completed() {
    let gateway = Arc::new(FakeGateway::new(vec![
        plan_behavior("触发失败"),
        FakeBehavior::FailAfterTextDelta {
            delta: "未完成部分".to_owned(),
        },
    ]));
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
            .any(|event| matches!(event.payload, HarnessEventPayload::TextDelta { .. }))
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
