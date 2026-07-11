use std::collections::HashMap;

use async_trait::async_trait;
use eventsource_stream::Eventsource;
use fairy_domain::{
    CompiledPromptRequest, ErrorCode, FairyError, GatewayCapabilities, ModelCompletion,
    ModelConnectionConfig, ModelProtocol, ModelStreamEvent, ModelTurnOutput, PromptItem,
    PromptLane, ToolCall, ToolName,
};
use fairy_harness::{ModelEventSink, ModelGateway};
use futures_util::StreamExt;
use secrecy::SecretString;
use serde_json::Value;
use tokio_util::sync::CancellationToken;

use crate::request::build_responses_request;
use crate::shared::map_http_status;
use crate::usage::parse_usage;

#[derive(Debug)]
pub struct OpenAiResponsesGateway {
    client: reqwest::Client,
    config: ModelConnectionConfig,
    api_key: Option<SecretString>,
}

impl OpenAiResponsesGateway {
    pub fn new(
        config: ModelConnectionConfig,
        api_key: Option<SecretString>,
    ) -> Result<Self, FairyError> {
        config.verify_integrity()?;
        let client = reqwest::Client::builder()
            .build()
            .map_err(|_| stream_failed("无法创建模型 HTTP client", true))?;
        Ok(Self {
            client,
            config,
            api_key,
        })
    }

    pub fn with_client(
        client: reqwest::Client,
        config: ModelConnectionConfig,
        api_key: Option<SecretString>,
    ) -> Result<Self, FairyError> {
        config.verify_integrity()?;
        Ok(Self {
            client,
            config,
            api_key,
        })
    }
}

#[async_trait]
impl ModelGateway for OpenAiResponsesGateway {
    fn capabilities(&self) -> GatewayCapabilities {
        self.config.capabilities()
    }

    async fn execute(
        &self,
        request: CompiledPromptRequest,
        cancellation: CancellationToken,
        sink: &mut (dyn ModelEventSink + Send),
    ) -> Result<ModelCompletion, FairyError> {
        let lane = request.shape.lane;
        let allowed_tools = request
            .shape
            .tool_policy
            .tools()
            .iter()
            .map(|tool| tool.name)
            .collect::<Vec<_>>();
        let http_request =
            build_responses_request(&self.client, &self.config, self.api_key.as_ref(), &request)?;
        let response = tokio::select! {
            () = cancellation.cancelled() => return Err(turn_interrupted()),
            result = self.client.execute(http_request) => {
                result.map_err(|_| stream_failed("无法连接模型服务", true))?
            }
        };
        if !response.status().is_success() {
            return Err(map_http_status(
                response.status(),
                ModelProtocol::Responses,
                response.url(),
            ));
        }

        let mut events = response.bytes_stream().eventsource();
        let mut output = String::new();
        let mut function_argument_deltas: HashMap<String, String> = HashMap::new();
        while let Some(next) = tokio::select! {
            () = cancellation.cancelled() => return Err(turn_interrupted()),
            next = events.next() => next,
        } {
            let event = next.map_err(|_| stream_failed("模型 SSE 流中断", true))?;
            if event.data == "[DONE]" {
                continue;
            }
            let payload: Value = serde_json::from_str(&event.data)
                .map_err(|_| invalid_response("模型返回了无法解析的 SSE 事件"))?;
            let event_type = payload
                .get("type")
                .and_then(Value::as_str)
                .ok_or_else(|| invalid_response("模型 SSE 事件缺少 type"))?;
            match event_type {
                "response.output_text.delta" | "response.refusal.delta" => {
                    if !function_argument_deltas.is_empty() {
                        return Err(invalid_response(
                            "Responses completion 不能混合文本与工具调用",
                        ));
                    }
                    let delta = payload
                        .get("delta")
                        .and_then(Value::as_str)
                        .ok_or_else(|| invalid_response("模型文本增量缺少 delta"))?;
                    if delta.is_empty() {
                        continue;
                    }
                    output.push_str(delta);
                    sink.send(ModelStreamEvent::TextDelta {
                        delta: delta.to_owned(),
                    })?;
                }
                "response.function_call_arguments.delta" => {
                    if !output.is_empty() {
                        return Err(invalid_response(
                            "Responses completion 不能混合文本与工具调用",
                        ));
                    }
                    let item_id =
                        required_non_empty_string(&payload, "item_id", "工具参数增量缺少 item_id")?;
                    let delta = payload
                        .get("delta")
                        .and_then(Value::as_str)
                        .ok_or_else(|| invalid_response("工具参数增量缺少 delta"))?;
                    function_argument_deltas
                        .entry(item_id.to_owned())
                        .or_default()
                        .push_str(delta);
                }
                "response.function_call_arguments.done" => {
                    let item_id = required_non_empty_string(
                        &payload,
                        "item_id",
                        "工具参数完成事件缺少 item_id",
                    )?;
                    let arguments = payload
                        .get("arguments")
                        .and_then(Value::as_str)
                        .ok_or_else(|| invalid_response("工具参数完成事件缺少 arguments"))?;
                    if function_argument_deltas
                        .get(item_id)
                        .is_some_and(|known| known != arguments)
                    {
                        return Err(invalid_response("Responses 工具参数完成文本与增量不一致"));
                    }
                    function_argument_deltas.insert(item_id.to_owned(), arguments.to_owned());
                }
                "response.completed" => {
                    let response = payload
                        .get("response")
                        .ok_or_else(|| invalid_response("完成事件缺少 response"))?;
                    let completed_text = extract_output_text(response);
                    let calls =
                        extract_tool_calls(response, &function_argument_deltas, &allowed_tools)?;
                    if !completed_text.is_empty() && !calls.is_empty() {
                        return Err(invalid_response(
                            "Responses completion 不能混合文本与工具调用",
                        ));
                    }
                    let (model_output, response_items) = if calls.is_empty() {
                        if output.is_empty() && !completed_text.is_empty() {
                            output.push_str(&completed_text);
                            sink.send(ModelStreamEvent::TextDelta {
                                delta: completed_text,
                            })?;
                        } else if !completed_text.is_empty() && completed_text != output {
                            return Err(invalid_response("模型完成文本与流式增量聚合结果不一致"));
                        }
                        if output.is_empty() {
                            return Err(invalid_response("模型完成但没有返回文本或工具调用"));
                        }
                        (
                            ModelTurnOutput::Text {
                                text: output.clone(),
                            },
                            response_items(lane, &output),
                        )
                    } else {
                        if !output.is_empty() {
                            return Err(invalid_response(
                                "Responses completion 不能混合文本与工具调用",
                            ));
                        }
                        let items = calls
                            .iter()
                            .cloned()
                            .map(|call| PromptItem::ToolCall { call })
                            .collect();
                        (ModelTurnOutput::ToolCalls { calls }, items)
                    };

                    let response_id = response
                        .get("id")
                        .and_then(Value::as_str)
                        .filter(|value| !value.is_empty())
                        .map(str::to_owned);
                    return Ok(ModelCompletion {
                        response_id,
                        output: model_output,
                        response_items,
                        usage: parse_usage(response, self.config.capabilities()),
                    });
                }
                "response.failed" | "response.incomplete" | "error" => {
                    return Err(stream_failed("模型未能完成本次回复", true));
                }
                ignored if is_ignorable_event(ignored) => {}
                _ => return Err(invalid_response("模型返回了不受支持的 SSE 事件")),
            }
        }

        Err(stream_failed("模型流在完成事件前结束", true))
    }
}

fn response_items(lane: PromptLane, output: &str) -> Vec<PromptItem> {
    match lane {
        PromptLane::Respond | PromptLane::Compact | PromptLane::Extract => {
            vec![PromptItem::AssistantMessage {
                content: output.to_owned(),
            }]
        }
    }
}

fn extract_output_text(response: &Value) -> String {
    response
        .get("output")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(|item| item.get("content").and_then(Value::as_array))
        .flatten()
        .filter_map(|part| {
            part.get("text")
                .and_then(Value::as_str)
                .or_else(|| part.get("refusal").and_then(Value::as_str))
        })
        .collect()
}

fn extract_tool_calls(
    response: &Value,
    streamed_arguments: &HashMap<String, String>,
    allowed_tools: &[ToolName],
) -> Result<Vec<ToolCall>, FairyError> {
    let mut calls = Vec::new();
    let Some(output) = response.get("output").and_then(Value::as_array) else {
        return Ok(calls);
    };
    for item in output {
        if item.get("type").and_then(Value::as_str) != Some("function_call") {
            continue;
        }
        let item_id = required_non_empty_string(item, "id", "Responses function_call 缺少 id")?;
        let call_id =
            required_non_empty_string(item, "call_id", "Responses function_call 缺少 call_id")?;
        let raw_name =
            required_non_empty_string(item, "name", "Responses function_call 缺少 name")?;
        let name = ToolName::parse(raw_name)?;
        if !allowed_tools.contains(&name) {
            return Err(invalid_response("Responses 模型调用了当前请求未声明的工具"));
        }
        let arguments = item
            .get("arguments")
            .and_then(Value::as_str)
            .ok_or_else(|| invalid_response("Responses function_call 缺少 arguments"))?;
        if streamed_arguments
            .get(item_id)
            .is_some_and(|known| known != arguments)
        {
            return Err(invalid_response(
                "Responses function_call 参数与流式增量不一致",
            ));
        }
        let parsed: Value = serde_json::from_str(arguments)
            .map_err(|_| invalid_response("Responses 工具参数不是有效 JSON"))?;
        if !parsed.is_object() {
            return Err(invalid_response("Responses 工具参数必须是 JSON object"));
        }
        calls.push(ToolCall {
            id: call_id.to_owned(),
            name,
            arguments_json: arguments.to_owned(),
        });
    }
    Ok(calls)
}

fn required_non_empty_string<'a>(
    value: &'a Value,
    field: &str,
    message: &'static str,
) -> Result<&'a str, FairyError> {
    value
        .get(field)
        .and_then(Value::as_str)
        .filter(|value| !value.is_empty())
        .ok_or_else(|| invalid_response(message))
}

fn is_ignorable_event(event_type: &str) -> bool {
    matches!(
        event_type,
        "response.created"
            | "response.queued"
            | "response.in_progress"
            | "response.output_item.added"
            | "response.output_item.done"
            | "response.content_part.added"
            | "response.content_part.done"
            | "response.output_text.done"
            | "response.refusal.done"
            | "response.reasoning_summary_part.added"
            | "response.reasoning_summary_part.done"
            | "response.reasoning_summary_text.delta"
            | "response.reasoning_summary_text.done"
    )
}

fn invalid_response(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::ModelResponseInvalid, message, false)
}

fn stream_failed(message: &'static str, retryable: bool) -> FairyError {
    FairyError::new(ErrorCode::ModelStreamFailed, message, retryable)
}

fn turn_interrupted() -> FairyError {
    FairyError::new(ErrorCode::TurnInterrupted, "模型请求已取消", false)
}
