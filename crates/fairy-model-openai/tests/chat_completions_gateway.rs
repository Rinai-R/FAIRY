use std::time::Duration;

use fairy_domain::{
    AuthMode, CachedTokenObservation, CompiledPromptRequest, ErrorCode, FairyError,
    ModelConnectionCompiler, ModelConnectionId, ModelConnectionInput, ModelProtocol,
    ModelRequestShape, ModelStreamEvent, PromptItem, PromptLane, ReasoningMode, ToolPolicy,
};
use fairy_harness::{ModelEventSink, ModelGateway};
use fairy_model_openai::OpenAiChatCompletionsGateway;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;
use tokio_util::sync::CancellationToken;

#[derive(Default)]
struct CollectSink {
    events: Vec<ModelStreamEvent>,
}

impl ModelEventSink for CollectSink {
    fn send(&mut self, event: ModelStreamEvent) -> Result<(), FairyError> {
        self.events.push(event);
        Ok(())
    }
}

struct CancelAfterFirstSink {
    events: Vec<ModelStreamEvent>,
    cancellation: CancellationToken,
}

impl ModelEventSink for CancelAfterFirstSink {
    fn send(&mut self, event: ModelStreamEvent) -> Result<(), FairyError> {
        self.events.push(event);
        self.cancellation.cancel();
        Ok(())
    }
}

fn request() -> CompiledPromptRequest {
    CompiledPromptRequest {
        shape: ModelRequestShape {
            lane: PromptLane::Respond,
            model: "test-model".to_owned(),
            instructions: "stable companion instructions".to_owned(),
            tool_policy: ToolPolicy::Disabled,
            parallel_tool_calls: false,
            reasoning: ReasoningMode::ProviderDefault,
            prompt_cache_key: Some("fairy:test:respond".to_owned()),
        },
        input: vec![PromptItem::UserMessage {
            content: "你好".to_owned(),
        }],
    }
}

async fn gateway_for(
    chunks: Vec<String>,
    status: u16,
    delay: Duration,
) -> OpenAiChatCompletionsGateway {
    let endpoint = spawn_server(chunks, status, delay).await;
    let config = ModelConnectionCompiler
        .compile(
            ModelConnectionId::new(),
            ModelConnectionInput {
                protocol: ModelProtocol::ChatCompletions,
                endpoint,
                model: "test-model".to_owned(),
                auth_mode: AuthMode::NoAuth,
            },
        )
        .expect("compile Chat test model config");
    OpenAiChatCompletionsGateway::new(config, None).expect("create Chat test gateway")
}

async fn spawn_server(chunks: Vec<String>, status: u16, delay: Duration) -> String {
    let listener = TcpListener::bind("127.0.0.1:0")
        .await
        .expect("bind Chat test server");
    let address = listener.local_addr().expect("Chat test server address");
    tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.expect("accept Chat request");
        let mut request_bytes = Vec::new();
        let mut buffer = [0_u8; 4096];
        loop {
            let read = socket.read(&mut buffer).await.expect("read Chat request");
            if read == 0 {
                break;
            }
            request_bytes.extend_from_slice(&buffer[..read]);
            if request_bytes.windows(4).any(|window| window == b"\r\n\r\n") {
                break;
            }
        }

        let reason = match status {
            200 => "OK",
            401 => "Unauthorized",
            500 => "Internal Server Error",
            _ => "Test Status",
        };
        let content_type = if status == 200 {
            "text/event-stream"
        } else {
            "application/json"
        };
        socket
            .write_all(
                format!(
                    "HTTP/1.1 {status} {reason}\r\nContent-Type: {content_type}\r\nConnection: close\r\n\r\n"
                )
                .as_bytes(),
            )
            .await
            .expect("write Chat response headers");
        for chunk in chunks {
            if socket.write_all(chunk.as_bytes()).await.is_err() {
                return;
            }
            if socket.flush().await.is_err() {
                return;
            }
            tokio::time::sleep(delay).await;
        }
    });
    format!("http://{address}/v1")
}

fn event(payload: serde_json::Value) -> String {
    format!(
        "data: {}\n\n",
        serde_json::to_string(&payload).expect("serialize Chat SSE fixture")
    )
}

fn delta(content: serde_json::Value, finish_reason: serde_json::Value) -> String {
    event(serde_json::json!({
        "id": "chatcmpl-test",
        "choices": [{
            "index": 0,
            "delta": content,
            "finish_reason": finish_reason
        }]
    }))
}

fn done() -> String {
    "data: [DONE]\n\n".to_owned()
}

#[tokio::test]
async fn streams_ordered_unicode_and_discards_reasoning_content() {
    let gateway = gateway_for(
        vec![
            delta(
                serde_json::json!({
                    "role": "assistant",
                    "reasoning_content": "绝不能进入输出",
                    "content": "你"
                }),
                serde_json::Value::Null,
            ),
            delta(
                serde_json::json!({"reasoning_content": "仍然丢弃", "content": "好🌙"}),
                serde_json::Value::Null,
            ),
            delta(serde_json::json!({}), serde_json::json!("stop")),
            event(serde_json::json!({
                "id": "chatcmpl-test",
                "choices": [],
                "usage": {"prompt_tokens": 9, "completion_tokens": 2}
            })),
            done(),
        ],
        200,
        Duration::from_millis(2),
    )
    .await;
    let mut sink = CollectSink::default();

    let completion = gateway
        .execute(request(), CancellationToken::new(), &mut sink)
        .await
        .expect("complete Chat stream");

    assert_eq!(completion.response_id.as_deref(), Some("chatcmpl-test"));
    assert_eq!(completion.output_text, "你好🌙");
    assert!(!completion.output_text.contains("丢弃"));
    assert_eq!(completion.usage.input_tokens, Some(9));
    assert_eq!(completion.usage.output_tokens, Some(2));
    assert_eq!(
        completion.usage.cached_input_tokens,
        CachedTokenObservation::Missing
    );
    assert_eq!(
        sink.events,
        vec![
            ModelStreamEvent::TextDelta {
                delta: "你".to_owned()
            },
            ModelStreamEvent::TextDelta {
                delta: "好🌙".to_owned()
            }
        ]
    );
}

#[tokio::test]
async fn maps_openai_and_deepseek_cached_usage_from_usage_only_chunks() {
    for (usage, expected_cached) in [
        (
            serde_json::json!({
                "prompt_tokens": 100,
                "completion_tokens": 2,
                "prompt_tokens_details": {"cached_tokens": 0}
            }),
            0,
        ),
        (
            serde_json::json!({
                "prompt_tokens": 100,
                "completion_tokens": 2,
                "prompt_cache_hit_tokens": 72,
                "prompt_cache_miss_tokens": 28
            }),
            72,
        ),
    ] {
        let gateway = gateway_for(
            vec![
                delta(
                    serde_json::json!({"content": "完成"}),
                    serde_json::Value::Null,
                ),
                delta(serde_json::json!({}), serde_json::json!("stop")),
                event(serde_json::json!({"choices": [], "usage": usage})),
                done(),
            ],
            200,
            Duration::ZERO,
        )
        .await;
        let completion = gateway
            .execute(
                request(),
                CancellationToken::new(),
                &mut CollectSink::default(),
            )
            .await
            .expect("complete cached usage fixture");
        assert_eq!(
            completion.usage.cached_input_tokens,
            CachedTokenObservation::Observed(expected_cached)
        );
        assert_eq!(
            completion.usage.cache_write_tokens,
            CachedTokenObservation::Missing
        );
    }
}

#[tokio::test]
async fn empty_choices_without_usage_and_invalid_choice_are_rejected() {
    for chunk in [
        event(serde_json::json!({"choices": []})),
        event(serde_json::json!({
            "choices": [{"index": 1, "delta": {}, "finish_reason": null}]
        })),
    ] {
        let gateway = gateway_for(vec![chunk], 200, Duration::ZERO).await;
        let error = gateway
            .execute(
                request(),
                CancellationToken::new(),
                &mut CollectSink::default(),
            )
            .await
            .expect_err("invalid Chat choice must fail");
        assert_eq!(error.code, ErrorCode::ModelResponseInvalid);
    }
}

#[tokio::test]
async fn malformed_json_half_stream_and_done_without_stop_never_succeed() {
    let fixtures = [
        vec!["data: {broken\n\n".to_owned()],
        vec![delta(
            serde_json::json!({"content": "部分"}),
            serde_json::Value::Null,
        )],
        vec![
            delta(
                serde_json::json!({"content": "部分"}),
                serde_json::Value::Null,
            ),
            done(),
        ],
    ];
    for chunks in fixtures {
        let gateway = gateway_for(chunks, 200, Duration::ZERO).await;
        let error = gateway
            .execute(
                request(),
                CancellationToken::new(),
                &mut CollectSink::default(),
            )
            .await
            .expect_err("incomplete Chat stream must fail");
        assert!(matches!(
            error.code,
            ErrorCode::ModelResponseInvalid | ErrorCode::ModelStreamFailed
        ));
    }
}

#[tokio::test]
async fn abnormal_finish_reasons_are_explicit_failures() {
    for reason in [
        "length",
        "content_filter",
        "tool_calls",
        "insufficient_system_resource",
    ] {
        let gateway = gateway_for(
            vec![delta(serde_json::json!({}), serde_json::json!(reason))],
            200,
            Duration::ZERO,
        )
        .await;
        let error = gateway
            .execute(
                request(),
                CancellationToken::new(),
                &mut CollectSink::default(),
            )
            .await
            .expect_err("abnormal Chat finish must fail");
        assert_eq!(error.code, ErrorCode::ModelStreamFailed);
    }
}

#[tokio::test]
async fn cancellation_after_first_delta_stops_without_partial_success() {
    let gateway = gateway_for(
        vec![
            delta(
                serde_json::json!({"content": "先"}),
                serde_json::Value::Null,
            ),
            delta(serde_json::json!({}), serde_json::json!("stop")),
            done(),
        ],
        200,
        Duration::from_millis(200),
    )
    .await;
    let cancellation = CancellationToken::new();
    let mut sink = CancelAfterFirstSink {
        events: Vec::new(),
        cancellation: cancellation.clone(),
    };

    let error = gateway
        .execute(request(), cancellation, &mut sink)
        .await
        .expect_err("cancelled Chat stream must not complete");

    assert_eq!(error.code, ErrorCode::TurnInterrupted);
    assert_eq!(sink.events.len(), 1);
}

#[tokio::test]
async fn http_statuses_are_distinct_and_never_surface_response_bodies() {
    for (status, expected) in [
        (401, ErrorCode::ModelAuthFailed),
        (403, ErrorCode::ModelAuthFailed),
        (404, ErrorCode::ModelProtocolMismatch),
        (405, ErrorCode::ModelProtocolMismatch),
        (500, ErrorCode::ModelStreamFailed),
    ] {
        let gateway = gateway_for(
            vec![r#"{"server_secret":"must-not-surface"}"#.to_owned()],
            status,
            Duration::ZERO,
        )
        .await;
        let error = gateway
            .execute(
                request(),
                CancellationToken::new(),
                &mut CollectSink::default(),
            )
            .await
            .expect_err("non-success Chat status must fail");
        assert_eq!(error.code, expected);
        assert!(error.message.contains(&status.to_string()));
        assert!(error.message.contains("chat_completions"));
        assert!(error.message.contains("/v1/chat/completions"));
        assert!(!error.message.contains("server_secret"));
        assert!(!format!("{error:?}").contains("must-not-surface"));
        assert!(
            !serde_json::to_string(&error)
                .expect("serialize safe Chat HTTP error")
                .contains("must-not-surface")
        );
    }
}
