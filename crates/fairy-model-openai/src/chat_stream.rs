use std::collections::BTreeMap;

use async_trait::async_trait;
use eventsource_stream::Eventsource;
use fairy_domain::{
    CompiledPromptRequest, ErrorCode, FairyError, GatewayCapabilities, ModelCompletion,
    ModelConnectionConfig, ModelProtocol, ModelStreamEvent, ModelTurnOutput, PromptItem, ToolCall,
    ToolName,
};
use fairy_harness::{ModelEventSink, ModelGateway};
use futures_util::StreamExt;
use secrecy::SecretString;
use serde_json::Value;
use tokio_util::sync::CancellationToken;

use crate::chat_request::build_chat_completions_request;
use crate::chat_usage::parse_chat_usage;
use crate::shared::map_http_status;

#[derive(Debug)]
pub struct OpenAiChatCompletionsGateway {
    client: reqwest::Client,
    config: ModelConnectionConfig,
    api_key: Option<SecretString>,
}

impl OpenAiChatCompletionsGateway {
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
impl ModelGateway for OpenAiChatCompletionsGateway {
    fn capabilities(&self) -> GatewayCapabilities {
        self.config.capabilities()
    }

    async fn execute(
        &self,
        request: CompiledPromptRequest,
        cancellation: CancellationToken,
        sink: &mut (dyn ModelEventSink + Send),
    ) -> Result<ModelCompletion, FairyError> {
        let allowed_tools = request
            .shape
            .tool_policy
            .tools()
            .iter()
            .map(|tool| tool.name)
            .collect::<Vec<_>>();
        let http_request = build_chat_completions_request(
            &self.client,
            &self.config,
            self.api_key.as_ref(),
            &request,
        )?;
        let response = tokio::select! {
            () = cancellation.cancelled() => return Err(turn_interrupted()),
            result = self.client.execute(http_request) => {
                result.map_err(|_| stream_failed("无法连接模型服务", true))?
            }
        };
        if !response.status().is_success() {
            return Err(map_http_status(
                response.status(),
                ModelProtocol::ChatCompletions,
                response.url(),
            ));
        }

        let mut events = response.bytes_stream().eventsource();
        let mut output = String::new();
        let mut response_id = None;
        let mut finish_reason = None;
        let mut tool_calls = BTreeMap::new();
        let mut usage = None;
        let mut saw_done = false;

        while let Some(next) = tokio::select! {
            () = cancellation.cancelled() => return Err(turn_interrupted()),
            next = events.next() => next,
        } {
            let event = next.map_err(|_| stream_failed("模型 SSE 流中断", true))?;
            if event.data == "[DONE]" {
                saw_done = true;
                break;
            }
            let payload: Value = serde_json::from_str(&event.data)
                .map_err(|_| invalid_response("模型返回了无法解析的 Chat SSE 事件"))?;
            record_response_id(&payload, &mut response_id)?;
            if let Some(value) = payload.get("usage").filter(|value| !value.is_null()) {
                if !value.is_object() {
                    return Err(invalid_response("Chat SSE usage 必须是对象"));
                }
                usage = Some(value.clone());
            }

            let choices = payload
                .get("choices")
                .and_then(Value::as_array)
                .ok_or_else(|| invalid_response("Chat SSE 事件缺少 choices"))?;
            if choices.is_empty() {
                if payload.get("usage").is_none_or(Value::is_null) {
                    return Err(invalid_response("Chat SSE 空 choices 只能承载 usage"));
                }
                continue;
            }

            let choice = &choices[0];
            if choice.get("index").and_then(Value::as_u64) != Some(0) {
                return Err(invalid_response("Chat SSE 第一个 choice 的 index 不是 0"));
            }
            let delta = choice
                .get("delta")
                .and_then(Value::as_object)
                .ok_or_else(|| invalid_response("Chat SSE choice 缺少 delta 对象"))?;
            if let Some(content) = delta.get("content").filter(|value| !value.is_null()) {
                let content = content
                    .as_str()
                    .ok_or_else(|| invalid_response("Chat SSE content 不是字符串"))?;
                if !content.is_empty() {
                    if !tool_calls.is_empty() {
                        return Err(invalid_response(
                            "Chat SSE 不能在同一 completion 混合文本与工具调用",
                        ));
                    }
                    if finish_reason.is_some() {
                        return Err(invalid_response("Chat SSE 在结束原因后继续返回文本"));
                    }
                    output.push_str(content);
                    sink.send(ModelStreamEvent::TextDelta {
                        delta: content.to_owned(),
                    })?;
                    if cancellation.is_cancelled() {
                        return Err(turn_interrupted());
                    }
                }
            }
            record_tool_call_deltas(delta.get("tool_calls"), &mut tool_calls, &output)?;

            if let Some(reason) = choice.get("finish_reason").filter(|value| !value.is_null()) {
                let reason = reason
                    .as_str()
                    .ok_or_else(|| invalid_response("Chat SSE finish_reason 不是字符串"))?;
                match reason {
                    "stop" => {
                        if !tool_calls.is_empty() {
                            return Err(invalid_response("Chat 工具调用不能使用 stop 结束原因"));
                        }
                        finish_reason = Some(reason.to_owned());
                    }
                    "tool_calls" => {
                        if tool_calls.is_empty() || !output.is_empty() {
                            return Err(invalid_response(
                                "Chat tool_calls 结束原因与实际输出不一致",
                            ));
                        }
                        finish_reason = Some(reason.to_owned());
                    }
                    "length" | "content_filter" | "insufficient_system_resource" => {
                        return Err(stream_failed("模型未能正常完成本次 Chat 回复", false));
                    }
                    _ => return Err(invalid_response("模型返回了未知的 Chat finish_reason")),
                }
            }
        }

        if !saw_done {
            return Err(stream_failed("模型 Chat 流在 [DONE] 前结束", true));
        }
        let (output, response_items) = match finish_reason.as_deref() {
            Some("stop") if !output.is_empty() => (
                ModelTurnOutput::Text {
                    text: output.clone(),
                },
                vec![PromptItem::AssistantMessage { content: output }],
            ),
            Some("tool_calls") => {
                let calls = finalize_tool_calls(tool_calls, &allowed_tools)?;
                let items = calls
                    .iter()
                    .cloned()
                    .map(|call| PromptItem::ToolCall { call })
                    .collect();
                (ModelTurnOutput::ToolCalls { calls }, items)
            }
            Some("stop") => return Err(invalid_response("模型完成但没有返回 Chat 文本")),
            _ => return Err(stream_failed("模型 Chat 流缺少正常结束原因", true)),
        };

        Ok(ModelCompletion {
            response_id,
            output,
            response_items,
            usage: parse_chat_usage(usage.as_ref(), self.config.capabilities()),
        })
    }
}

#[derive(Default)]
struct PartialToolCall {
    id: Option<String>,
    name: Option<String>,
    arguments: String,
}

fn record_tool_call_deltas(
    value: Option<&Value>,
    calls: &mut BTreeMap<u64, PartialToolCall>,
    text_output: &str,
) -> Result<(), FairyError> {
    let Some(value) = value.filter(|value| !value.is_null()) else {
        return Ok(());
    };
    if !text_output.is_empty() {
        return Err(invalid_response(
            "Chat SSE 不能在同一 completion 混合文本与工具调用",
        ));
    }
    let deltas = value
        .as_array()
        .ok_or_else(|| invalid_response("Chat SSE tool_calls 不是数组"))?;
    if deltas.is_empty() {
        return Err(invalid_response("Chat SSE tool_calls 不能为空数组"));
    }
    for delta in deltas {
        let index = delta
            .get("index")
            .and_then(Value::as_u64)
            .ok_or_else(|| invalid_response("Chat 工具调用缺少有效 index"))?;
        let partial = calls.entry(index).or_default();
        if let Some(kind) = delta.get("type").filter(|value| !value.is_null())
            && kind.as_str() != Some("function")
        {
            return Err(invalid_response("Chat 只支持 function 工具调用"));
        }
        if let Some(id) = delta.get("id").filter(|value| !value.is_null()) {
            let id = id
                .as_str()
                .filter(|id| !id.is_empty())
                .ok_or_else(|| invalid_response("Chat 工具调用 id 无效"))?;
            if partial.id.as_deref().is_some_and(|known| known != id) {
                return Err(invalid_response("Chat 工具调用 id 在流中发生变化"));
            }
            partial.id = Some(id.to_owned());
        }
        let Some(function) = delta.get("function").filter(|value| !value.is_null()) else {
            continue;
        };
        let function = function
            .as_object()
            .ok_or_else(|| invalid_response("Chat 工具 function 不是对象"))?;
        if let Some(name) = function.get("name").filter(|value| !value.is_null()) {
            let name = name
                .as_str()
                .filter(|name| !name.is_empty())
                .ok_or_else(|| invalid_response("Chat 工具名称无效"))?;
            if partial.name.as_deref().is_some_and(|known| known != name) {
                return Err(invalid_response("Chat 工具名称在流中发生变化"));
            }
            partial.name = Some(name.to_owned());
        }
        if let Some(arguments) = function.get("arguments").filter(|value| !value.is_null()) {
            partial.arguments.push_str(
                arguments
                    .as_str()
                    .ok_or_else(|| invalid_response("Chat 工具参数增量不是字符串"))?,
            );
        }
    }
    Ok(())
}

fn finalize_tool_calls(
    partials: BTreeMap<u64, PartialToolCall>,
    allowed_tools: &[ToolName],
) -> Result<Vec<ToolCall>, FairyError> {
    let mut calls = Vec::with_capacity(partials.len());
    for (expected_index, (actual_index, partial)) in partials.into_iter().enumerate() {
        if actual_index != expected_index as u64 {
            return Err(invalid_response("Chat 工具调用 index 不连续"));
        }
        let id = partial
            .id
            .ok_or_else(|| invalid_response("Chat 工具调用缺少 id"))?;
        let raw_name = partial
            .name
            .ok_or_else(|| invalid_response("Chat 工具调用缺少 name"))?;
        let name = ToolName::parse(&raw_name)?;
        if !allowed_tools.contains(&name) {
            return Err(invalid_response("Chat 模型调用了当前请求未声明的工具"));
        }
        let arguments: Value = serde_json::from_str(&partial.arguments)
            .map_err(|_| invalid_response("Chat 工具参数不是有效 JSON"))?;
        if !arguments.is_object() {
            return Err(invalid_response("Chat 工具参数必须是 JSON object"));
        }
        calls.push(ToolCall {
            id,
            name,
            arguments_json: partial.arguments,
        });
    }
    Ok(calls)
}

fn record_response_id(payload: &Value, response_id: &mut Option<String>) -> Result<(), FairyError> {
    let Some(next_id) = payload
        .get("id")
        .and_then(Value::as_str)
        .filter(|value| !value.is_empty())
    else {
        return Ok(());
    };
    if response_id
        .as_deref()
        .is_some_and(|current| current != next_id)
    {
        return Err(invalid_response("Chat SSE 响应 id 在流中发生变化"));
    }
    *response_id = Some(next_id.to_owned());
    Ok(())
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
