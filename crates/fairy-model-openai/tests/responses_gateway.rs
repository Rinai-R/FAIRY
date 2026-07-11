use std::time::Duration;

use fairy_domain::{
    AuthMode, CachedTokenObservation, CompiledPromptRequest, ErrorCode, FairyError,
    ModelConnectionCompiler, ModelConnectionId, ModelConnectionInput, ModelProtocol,
    ModelRequestShape, ModelStreamEvent, PromptItem, PromptLane, ReasoningMode, ToolDefinition,
    ToolName, ToolPolicy,
};
use fairy_harness::{ModelEventSink, ModelGateway};
use fairy_model_openai::OpenAiResponsesGateway;
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
            instructions: "stable".to_owned(),
            reply_mode: None,
            max_output_tokens: 160,
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

fn tool_request() -> CompiledPromptRequest {
    let mut request = request();
    request.shape.tool_policy = ToolPolicy::Auto {
        tools: vec![ToolDefinition {
            name: ToolName::WebSearch,
            description: "查询最新网页事实".to_owned(),
            parameters: serde_json::json!({
                "type": "object",
                "properties": {"query": {"type": "string"}},
                "required": ["query"],
                "additionalProperties": false
            }),
        }],
    };
    request
}

async fn gateway_for(chunks: Vec<String>, status: u16, delay: Duration) -> OpenAiResponsesGateway {
    let endpoint = spawn_server(chunks, status, delay).await;
    let config = ModelConnectionCompiler
        .compile(
            ModelConnectionId::new(),
            ModelConnectionInput {
                protocol: ModelProtocol::Responses,
                endpoint,
                model: "test-model".to_owned(),
                auth_mode: AuthMode::NoAuth,
            },
        )
        .expect("compile test model config");
    OpenAiResponsesGateway::new(config, None).expect("create test gateway")
}

async fn spawn_server(chunks: Vec<String>, status: u16, delay: Duration) -> String {
    let listener = TcpListener::bind("127.0.0.1:0")
        .await
        .expect("bind test server");
    let address = listener.local_addr().expect("test server address");
    tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.expect("accept request");
        let mut request_bytes = Vec::new();
        let mut buffer = [0_u8; 4096];
        loop {
            let read = socket.read(&mut buffer).await.expect("read request");
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
            .expect("write response headers");
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
        serde_json::to_string(&payload).expect("serialize SSE fixture")
    )
}

#[tokio::test]
async fn streams_ordered_deltas_and_records_real_cached_usage() {
    let gateway = gateway_for(
        vec![
            event(serde_json::json!({
                "type": "response.output_text.delta",
                "delta": "你"
            })),
            event(serde_json::json!({
                "type": "response.output_text.delta",
                "delta": "好"
            })),
            event(serde_json::json!({
                "type": "response.completed",
                "response": {
                    "id": "resp_success",
                    "usage": {
                        "input_tokens": 100,
                        "output_tokens": 2,
                        "input_tokens_details": {"cached_tokens": 64}
                    }
                }
            })),
        ],
        200,
        Duration::from_millis(2),
    )
    .await;
    let mut sink = CollectSink::default();

    let completion = gateway
        .execute(request(), CancellationToken::new(), &mut sink)
        .await
        .expect("complete streaming response");

    assert_eq!(completion.output.text(), Some("你好"));
    assert_eq!(completion.response_id.as_deref(), Some("resp_success"));
    assert_eq!(completion.usage.input_tokens, Some(100));
    assert_eq!(
        completion.usage.cached_input_tokens,
        CachedTokenObservation::Observed(64)
    );
    assert_eq!(
        sink.events,
        vec![
            ModelStreamEvent::TextDelta {
                delta: "你".to_owned()
            },
            ModelStreamEvent::TextDelta {
                delta: "好".to_owned()
            }
        ]
    );
}

#[tokio::test]
async fn automatic_cache_observation_keeps_zero_and_missing_distinct() {
    for (details, expected) in [
        (
            serde_json::json!({"cached_tokens": 0}),
            CachedTokenObservation::Observed(0),
        ),
        (serde_json::json!({}), CachedTokenObservation::Missing),
    ] {
        let gateway = gateway_for(
            vec![
                event(serde_json::json!({
                    "type": "response.output_text.delta",
                    "delta": "完成"
                })),
                event(serde_json::json!({
                    "type": "response.completed",
                    "response": {
                        "usage": {"input_tokens_details": details}
                    }
                })),
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
            .expect("complete cache observation fixture");
        assert_eq!(completion.usage.cached_input_tokens, expected);
    }
}

#[tokio::test]
async fn completion_payload_without_deltas_still_emits_the_single_real_text() {
    let gateway = gateway_for(
        vec![event(serde_json::json!({
            "type": "response.completed",
            "response": {
                "id": "resp_full",
                "output": [{
                    "type": "message",
                    "content": [{"type": "output_text", "text": "完整文本"}]
                }],
                "usage": {}
            }
        }))],
        200,
        Duration::ZERO,
    )
    .await;
    let mut sink = CollectSink::default();

    let completion = gateway
        .execute(request(), CancellationToken::new(), &mut sink)
        .await
        .expect("complete from canonical response output");

    assert_eq!(completion.output.text(), Some("完整文本"));
    assert_eq!(
        sink.events,
        vec![ModelStreamEvent::TextDelta {
            delta: "完整文本".to_owned()
        }]
    );
}

#[tokio::test]
async fn assembles_function_call_and_keeps_it_out_of_visible_text() {
    let arguments = r#"{"query":"Rust 1.95"}"#;
    let gateway = gateway_for(
        vec![
            event(serde_json::json!({
                "type": "response.function_call_arguments.delta",
                "item_id": "fc_1",
                "output_index": 0,
                "delta": "{\"query\":\"Rust"
            })),
            event(serde_json::json!({
                "type": "response.function_call_arguments.delta",
                "item_id": "fc_1",
                "output_index": 0,
                "delta": " 1.95\"}"
            })),
            event(serde_json::json!({
                "type": "response.function_call_arguments.done",
                "item_id": "fc_1",
                "output_index": 0,
                "arguments": arguments
            })),
            event(serde_json::json!({
                "type": "response.completed",
                "response": {
                    "id": "resp_tool",
                    "output": [{
                        "type": "function_call",
                        "id": "fc_1",
                        "call_id": "call_1",
                        "name": "web_search",
                        "arguments": arguments
                    }],
                    "usage": {"input_tokens": 10, "output_tokens": 3}
                }
            })),
        ],
        200,
        Duration::ZERO,
    )
    .await;
    let mut sink = CollectSink::default();

    let completion = gateway
        .execute(tool_request(), CancellationToken::new(), &mut sink)
        .await
        .expect("complete Responses tool call");

    let fairy_domain::ModelTurnOutput::ToolCalls { calls } = completion.output else {
        panic!("expected tool calls")
    };
    assert_eq!(calls.len(), 1);
    assert_eq!(calls[0].id, "call_1");
    assert_eq!(calls[0].name, ToolName::WebSearch);
    assert_eq!(calls[0].arguments_json, arguments);
    assert!(sink.events.is_empty());
    assert!(matches!(
        completion.response_items.as_slice(),
        [PromptItem::ToolCall { call }] if call == &calls[0]
    ));
}

#[tokio::test]
async fn function_call_delta_mismatch_and_undeclared_tool_are_rejected() {
    let fixtures = [
        (
            tool_request(),
            vec![
                event(serde_json::json!({
                    "type": "response.function_call_arguments.delta",
                    "item_id": "fc_1",
                    "delta": "{\"query\":\"a\"}"
                })),
                event(serde_json::json!({
                    "type": "response.completed",
                    "response": {"output": [{
                        "type": "function_call",
                        "id": "fc_1",
                        "call_id": "call_1",
                        "name": "web_search",
                        "arguments": "{\"query\":\"b\"}"
                    }]}
                })),
            ],
        ),
        (
            request(),
            vec![event(serde_json::json!({
                "type": "response.completed",
                "response": {"output": [{
                    "type": "function_call",
                    "id": "fc_1",
                    "call_id": "call_1",
                    "name": "web_search",
                    "arguments": "{\"query\":\"x\"}"
                }]}
            }))],
        ),
    ];

    for (request, chunks) in fixtures {
        let gateway = gateway_for(chunks, 200, Duration::ZERO).await;
        let error = gateway
            .execute(
                request,
                CancellationToken::new(),
                &mut CollectSink::default(),
            )
            .await
            .expect_err("invalid function call must fail");
        assert_eq!(error.code, ErrorCode::ModelResponseInvalid);
    }
}

#[tokio::test]
async fn auth_and_server_statuses_have_safe_distinct_errors() {
    let auth = gateway_for(
        vec!["{\"secret\":\"must-not-surface\"}".to_owned()],
        401,
        Duration::ZERO,
    )
    .await;
    let auth_error = auth
        .execute(
            request(),
            CancellationToken::new(),
            &mut CollectSink::default(),
        )
        .await
        .expect_err("401 must fail");
    assert_eq!(auth_error.code, ErrorCode::ModelAuthFailed);
    assert!(!auth_error.message.contains("secret"));

    let server = gateway_for(vec!["internal details".to_owned()], 500, Duration::ZERO).await;
    let server_error = server
        .execute(
            request(),
            CancellationToken::new(),
            &mut CollectSink::default(),
        )
        .await
        .expect_err("500 must fail");
    assert_eq!(server_error.code, ErrorCode::ModelStreamFailed);
    assert!(server_error.retryable);
    assert!(!server_error.message.contains("internal details"));
}

#[tokio::test]
async fn malformed_event_and_half_stream_never_become_partial_success() {
    let malformed = gateway_for(vec!["data: {broken\n\n".to_owned()], 200, Duration::ZERO).await;
    let malformed_error = malformed
        .execute(
            request(),
            CancellationToken::new(),
            &mut CollectSink::default(),
        )
        .await
        .expect_err("malformed SSE JSON must fail");
    assert_eq!(malformed_error.code, ErrorCode::ModelResponseInvalid);

    let half = gateway_for(
        vec![event(serde_json::json!({
            "type": "response.output_text.delta",
            "delta": "部分"
        }))],
        200,
        Duration::ZERO,
    )
    .await;
    let mut sink = CollectSink::default();
    let half_error = half
        .execute(request(), CancellationToken::new(), &mut sink)
        .await
        .expect_err("stream without completion must fail");
    assert_eq!(half_error.code, ErrorCode::ModelStreamFailed);
    assert_eq!(sink.events.len(), 1);
}

#[tokio::test]
async fn cancellation_after_first_delta_stops_stream_with_interrupted_error() {
    let gateway = gateway_for(
        vec![
            event(serde_json::json!({
                "type": "response.output_text.delta",
                "delta": "先"
            })),
            event(serde_json::json!({
                "type": "response.completed",
                "response": {"usage": {}}
            })),
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
        .expect_err("cancelled stream must not complete");

    assert_eq!(error.code, ErrorCode::TurnInterrupted);
    assert_eq!(sink.events.len(), 1);
}
