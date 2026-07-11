use std::collections::VecDeque;
use std::sync::{Arc, Mutex, MutexGuard};
use std::time::Duration;

use async_trait::async_trait;
use fairy_domain::{
    AssistantSource, CachedTokenObservation, CharacterBriefInput, CharacterCompiler, CharacterId,
    CompiledPromptRequest, ConversationId, ErrorCode, ExtractionJobId, FairyError,
    GatewayCapabilities, HarnessEvent, HarnessEventPayload, ModelCompletion, ModelStreamEvent,
    ModelTurnOutput, ModelUsage, NewKnowledge, NewPersonalMemory, PersonalMemoryId,
    PersonalMemoryKind, PromptItem, PromptLane, RetrievalContext, RetrievedPersonalMemory,
    Revision, ToolCall, ToolName, ToolPolicy, ToolResultOutcome, TurnId, TurnState,
    UserProfileCompiler, UserProfileInput, UserProfileSnapshot, WebSearchResponse,
};
use fairy_harness::{
    CompanionIntelligence, HarnessEventSink, HarnessRuntime, IntelligenceBinding, ModelEventSink,
    ModelGateway, WebSearchGateway,
};
use tokio::sync::Notify;
use tokio_util::sync::CancellationToken;

enum FakeBehavior {
    Complete { output: String, deltas: Vec<String> },
    ToolCalls { calls: Vec<ToolCall> },
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
                    sink.send(ModelStreamEvent::TextDelta { delta })?;
                }
                Ok(completion(output))
            }
            FakeBehavior::ToolCalls { calls } => Ok(tool_completion(calls)),
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

fn tool_completion(calls: Vec<ToolCall>) -> ModelCompletion {
    ModelCompletion {
        response_id: Some("fake-tool-response".to_owned()),
        output: ModelTurnOutput::ToolCalls {
            calls: calls.clone(),
        },
        response_items: calls
            .into_iter()
            .map(|call| PromptItem::ToolCall { call })
            .collect(),
        usage: ModelUsage {
            input_tokens: Some(24),
            output_tokens: Some(4),
            cached_input_tokens: CachedTokenObservation::Observed(8),
            cache_write_tokens: CachedTokenObservation::Missing,
        },
    }
}

struct FakeSearchGateway {
    results: Mutex<VecDeque<Result<WebSearchResponse, FairyError>>>,
    queries: Mutex<Vec<String>>,
}

impl FakeSearchGateway {
    fn new(results: Vec<Result<WebSearchResponse, FairyError>>) -> Self {
        Self {
            results: Mutex::new(results.into()),
            queries: Mutex::new(Vec::new()),
        }
    }

    fn queries(&self) -> Vec<String> {
        lock(&self.queries).clone()
    }
}

#[async_trait]
impl WebSearchGateway for FakeSearchGateway {
    async fn search(
        &self,
        query: String,
        cancellation: CancellationToken,
    ) -> Result<WebSearchResponse, FairyError> {
        if cancellation.is_cancelled() {
            return Err(FairyError::new(
                ErrorCode::TurnInterrupted,
                "fake search cancelled",
                false,
            ));
        }
        lock(&self.queries).push(query);
        lock(&self.results)
            .pop_front()
            .expect("fake search result for every call")
    }
}

struct FakeIntelligence {
    results: Mutex<VecDeque<Result<RetrievalContext, FairyError>>>,
    extraction_enabled: bool,
    committed: Mutex<Vec<(Vec<NewPersonalMemory>, Vec<NewKnowledge>)>>,
    failed: Mutex<Vec<FairyError>>,
}

impl FakeIntelligence {
    fn new(results: Vec<Result<RetrievalContext, FairyError>>) -> Self {
        Self {
            results: Mutex::new(results.into()),
            extraction_enabled: false,
            committed: Mutex::new(Vec::new()),
            failed: Mutex::new(Vec::new()),
        }
    }

    fn with_extraction(results: Vec<Result<RetrievalContext, FairyError>>) -> Self {
        Self {
            extraction_enabled: true,
            ..Self::new(results)
        }
    }

    fn committed(&self) -> Vec<(Vec<NewPersonalMemory>, Vec<NewKnowledge>)> {
        lock(&self.committed).clone()
    }

    fn failed(&self) -> Vec<FairyError> {
        lock(&self.failed).clone()
    }
}

#[async_trait]
impl CompanionIntelligence for FakeIntelligence {
    async fn retrieve(&self, _query: String) -> Result<RetrievalContext, FairyError> {
        lock(&self.results)
            .pop_front()
            .expect("fake intelligence result for every turn")
    }

    async fn create_extraction_job(
        &self,
        _conversation_id: ConversationId,
        _turn_id: TurnId,
    ) -> Result<ExtractionJobId, FairyError> {
        if self.extraction_enabled {
            Ok(ExtractionJobId::new())
        } else {
            Err(FairyError::new(
                ErrorCode::IntelligenceUnavailable,
                "fake extraction disabled",
                false,
            ))
        }
    }

    async fn mark_extraction_running(&self, _job_id: ExtractionJobId) -> Result<(), FairyError> {
        Ok(())
    }

    async fn commit_extraction(
        &self,
        _job_id: ExtractionJobId,
        personal_memories: Vec<NewPersonalMemory>,
        knowledge: Vec<NewKnowledge>,
    ) -> Result<(), FairyError> {
        lock(&self.committed).push((personal_memories, knowledge));
        Ok(())
    }

    async fn fail_extraction_job(
        &self,
        _job_id: ExtractionJobId,
        error: FairyError,
    ) -> Result<(), FairyError> {
        lock(&self.failed).push(error);
        Ok(())
    }
}

fn search_call(id: &str, query: &str) -> ToolCall {
    ToolCall {
        id: id.to_owned(),
        name: ToolName::WebSearch,
        arguments_json: serde_json::json!({"query": query}).to_string(),
    }
}

fn search_response(query: &str, url: &str) -> WebSearchResponse {
    WebSearchResponse {
        query: query.to_owned(),
        sources: vec![AssistantSource {
            title: "测试来源".to_owned(),
            url: url.to_owned(),
            snippet: "可验证摘要".to_owned(),
            rank: 1,
            fetched_at_unix_ms: 42,
        }],
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
async fn one_search_round_produces_one_visible_reply_with_sources() {
    let gateway = Arc::new(FakeGateway::new(vec![
        FakeBehavior::ToolCalls {
            calls: vec![search_call("call_1", "Rust 1.95 release")],
        },
        response_behavior("Rust 1.95 已经发布。", &["Rust 1.95 已经发布。"]),
    ]));
    let search = Arc::new(FakeSearchGateway::new(vec![Ok(search_response(
        "Rust 1.95 release",
        "https://example.test/rust",
    ))]));
    let (runtime, conversation_id, _) = setup(gateway.clone());
    runtime.replace_web_search_gateway(Some(search.clone()));
    let mut sink = RecordingSink::default();

    let outcome = runtime
        .submit_turn(
            conversation_id,
            "Rust 1.95 发布了吗？".to_owned(),
            true,
            &mut sink,
        )
        .await
        .expect("complete search-assisted turn");

    assert_eq!(outcome.response_text.as_str(), "Rust 1.95 已经发布。");
    assert_eq!(outcome.speech_text.as_str(), "Rust 1.95 已经发布。");
    assert_eq!(outcome.sources.len(), 1);
    assert_eq!(outcome.sources[0].url, "https://example.test/rust");
    assert_eq!(outcome.usage.len(), 2);
    assert_eq!(search.queries(), vec!["Rust 1.95 release"]);
    let requests = gateway.requests();
    assert_eq!(requests.len(), 2);
    assert_eq!(requests[0].shape, requests[1].shape);
    assert!(matches!(
        requests[0].shape.tool_policy,
        ToolPolicy::Auto { .. }
    ));
    assert!(
        requests[1]
            .input
            .iter()
            .any(|item| matches!(item, PromptItem::ToolCall { .. }))
    );
    assert!(
        requests[1]
            .input
            .iter()
            .any(|item| matches!(item, PromptItem::ToolResult { .. }))
    );
    assert_eq!(
        sink.events()
            .iter()
            .filter(|event| matches!(event.payload, HarnessEventPayload::TextDelta { .. }))
            .count(),
        1
    );
}

#[tokio::test]
async fn retrieval_context_is_appended_before_user_message_and_empty_is_omitted() {
    let context = RetrievalContext {
        personal_memories: vec![RetrievedPersonalMemory {
            id: PersonalMemoryId::new(),
            kind: PersonalMemoryKind::Preference,
            content: "用户不喜欢太甜的饮料".to_owned(),
            confidence_basis_points: 9000,
            updated_at_unix_ms: 42,
        }],
        knowledge: Vec::new(),
    };
    let gateway = Arc::new(FakeGateway::new(vec![response_behavior(
        "那就选清爽一点的。",
        &["那就选清爽一点的。"],
    )]));
    let intelligence = Arc::new(FakeIntelligence::new(vec![Ok(context.clone())]));
    let (runtime, conversation_id, _) = setup(gateway.clone());
    runtime.replace_intelligence_binding(IntelligenceBinding::Available(intelligence));

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

    let empty_gateway = Arc::new(FakeGateway::new(vec![response_behavior(
        "我在。",
        &["我在。"],
    )]));
    let empty_intelligence = Arc::new(FakeIntelligence::new(vec![Ok(RetrievalContext::default())]));
    let (empty_runtime, empty_conversation, _) = setup(empty_gateway.clone());
    empty_runtime.replace_intelligence_binding(IntelligenceBinding::Available(empty_intelligence));
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
async fn unavailable_intelligence_is_explicit_context_not_silent_empty_memory() {
    let gateway = Arc::new(FakeGateway::new(vec![response_behavior(
        "这次记忆功能暂时不可用。",
        &["这次记忆功能暂时不可用。"],
    )]));
    let (runtime, conversation_id, _) = setup(gateway.clone());
    runtime.replace_intelligence_binding(IntelligenceBinding::Unavailable(FairyError::new(
        ErrorCode::IntelligenceUnavailable,
        "数据库无法打开",
        false,
    )));

    runtime
        .submit_turn(
            conversation_id,
            "记住这件事".to_owned(),
            false,
            &mut RecordingSink::default(),
        )
        .await
        .expect("model can report unavailable capability");

    let request = &gateway.requests()[0];
    assert!(request.input.iter().any(|item| matches!(
        item,
        PromptItem::CapabilityStatus {
            state: fairy_domain::CapabilityState::Unavailable,
            error: Some(error),
            ..
        } if error.code == ErrorCode::IntelligenceUnavailable
    )));
    assert!(
        request
            .input
            .iter()
            .all(|item| !matches!(item, PromptItem::RetrievedContext { .. }))
    );
}

#[tokio::test]
async fn successful_background_extraction_commits_memory_and_sourced_knowledge() {
    let extraction_json = serde_json::json!({
        "personalMemories": [{
            "kind": "preference",
            "content": "用户喜欢 Rust",
            "confidenceBasisPoints": 9000,
            "supersedesId": null
        }],
        "knowledge": [{
            "topic": "Rust",
            "statement": "Rust 1.95 已发布",
            "confidenceBasisPoints": 9500,
            "supersedesId": null,
            "sourceRanks": [1]
        }]
    })
    .to_string();
    let gateway = Arc::new(FakeGateway::new(vec![
        FakeBehavior::ToolCalls {
            calls: vec![search_call("call_1", "Rust 1.95 release")],
        },
        response_behavior("已经查到相关资料。", &["已经查到相关资料。"]),
        response_behavior(&extraction_json, &[&extraction_json]),
    ]));
    let search = Arc::new(FakeSearchGateway::new(vec![Ok(search_response(
        "Rust 1.95 release",
        "https://example.test/rust-1-95",
    ))]));
    let intelligence = Arc::new(FakeIntelligence::with_extraction(vec![Ok(
        RetrievalContext::default(),
    )]));
    let (runtime, conversation_id, _) = setup(gateway.clone());
    runtime.replace_web_search_gateway(Some(search));
    runtime.replace_intelligence_binding(IntelligenceBinding::Available(intelligence.clone()));

    let outcome = runtime
        .submit_turn(
            conversation_id,
            "查一下 Rust 1.95，并记住我喜欢 Rust".to_owned(),
            false,
            &mut RecordingSink::default(),
        )
        .await
        .expect("visible reply completes before extraction");
    assert_eq!(outcome.response_text.as_str(), "已经查到相关资料。");
    wait_for_background(&runtime).await;

    let committed = intelligence.committed();
    assert_eq!(committed.len(), 1);
    assert_eq!(committed[0].0.len(), 1);
    assert_eq!(committed[0].0[0].content, "用户喜欢 Rust");
    assert_eq!(committed[0].1.len(), 1);
    assert_eq!(committed[0].1[0].sources.len(), 1);
    assert_eq!(
        committed[0].1[0].sources[0].url,
        "https://example.test/rust-1-95"
    );
    assert!(intelligence.failed().is_empty());
    assert!(runtime.last_intelligence_background_error().is_none());
    let requests = gateway.requests();
    assert_eq!(requests.len(), 3);
    assert_eq!(requests[2].shape.lane, PromptLane::Extract);
    assert_eq!(requests[2].shape.max_output_tokens, 800);
    assert_eq!(requests[2].shape.tool_policy, ToolPolicy::Disabled);
    assert!(matches!(
        requests[2].input.as_slice(),
        [PromptItem::ExtractionInput { .. }]
    ));
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
    runtime.replace_intelligence_binding(IntelligenceBinding::Available(intelligence.clone()));
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
    assert_eq!(failed[0].code, ErrorCode::IntelligenceExtractionFailed);
    assert_eq!(
        runtime
            .last_intelligence_background_error()
            .expect("background diagnostic")
            .code,
        ErrorCode::IntelligenceExtractionFailed
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
async fn search_failure_is_a_truthful_tool_result_not_provider_fallback() {
    let gateway = Arc::new(FakeGateway::new(vec![
        FakeBehavior::ToolCalls {
            calls: vec![search_call("call_1", "current news")],
        },
        response_behavior("现在暂时查不到结果。", &["现在暂时查不到结果。"]),
    ]));
    let search = Arc::new(FakeSearchGateway::new(vec![Err(FairyError::new(
        ErrorCode::SearchRateLimited,
        "fake rate limit",
        true,
    ))]));
    let (runtime, conversation_id, _) = setup(gateway.clone());
    runtime.replace_web_search_gateway(Some(search));

    let outcome = runtime
        .submit_turn(
            conversation_id,
            "查一下现在的新闻".to_owned(),
            false,
            &mut RecordingSink::default(),
        )
        .await
        .expect("tool failure can produce an honest reply");

    assert_eq!(outcome.response_text.as_str(), "现在暂时查不到结果。");
    assert!(outcome.sources.is_empty());
    let requests = gateway.requests();
    let failed = requests[1]
        .input
        .iter()
        .find_map(|item| match item {
            PromptItem::ToolResult { result } => Some(&result.outcome),
            _ => None,
        })
        .expect("failed tool result in second request");
    assert!(matches!(
        failed,
        ToolResultOutcome::Failed { error }
            if error.code == ErrorCode::SearchRateLimited
    ));
}

#[tokio::test]
async fn third_search_call_fails_with_explicit_limit_and_no_visible_text() {
    let gateway = Arc::new(FakeGateway::new(vec![
        FakeBehavior::ToolCalls {
            calls: vec![search_call("call_1", "one"), search_call("call_2", "two")],
        },
        FakeBehavior::ToolCalls {
            calls: vec![search_call("call_3", "three")],
        },
    ]));
    let search = Arc::new(FakeSearchGateway::new(vec![
        Ok(search_response("one", "https://example.test/one")),
        Ok(search_response("two", "https://example.test/two")),
    ]));
    let (runtime, conversation_id, _) = setup(gateway);
    runtime.replace_web_search_gateway(Some(search));
    let mut sink = RecordingSink::default();

    let error = runtime
        .submit_turn(conversation_id, "连续搜索".to_owned(), false, &mut sink)
        .await
        .expect_err("third search must fail");

    assert_eq!(error.code, ErrorCode::ToolLimitExceeded);
    assert!(
        sink.events()
            .iter()
            .all(|event| !matches!(event.payload, HarnessEventPayload::TextDelta { .. }))
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
