use async_trait::async_trait;
use eventsource_stream::Eventsource;
use fairy_domain::{
    CompiledPromptRequest, ErrorCode, FairyError, GatewayCapabilities, ModelCompletion,
    ModelConnectionConfig, ModelProtocol, ModelStreamEvent, PromptItem,
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

            if let Some(reason) = choice.get("finish_reason").filter(|value| !value.is_null()) {
                let reason = reason
                    .as_str()
                    .ok_or_else(|| invalid_response("Chat SSE finish_reason 不是字符串"))?;
                match reason {
                    "stop" => finish_reason = Some(reason.to_owned()),
                    "length" | "content_filter" | "tool_calls" | "insufficient_system_resource" => {
                        return Err(stream_failed("模型未能正常完成本次 Chat 回复", false));
                    }
                    _ => return Err(invalid_response("模型返回了未知的 Chat finish_reason")),
                }
            }
        }

        if !saw_done {
            return Err(stream_failed("模型 Chat 流在 [DONE] 前结束", true));
        }
        if finish_reason.as_deref() != Some("stop") {
            return Err(stream_failed("模型 Chat 流缺少正常结束原因", true));
        }
        if output.is_empty() {
            return Err(invalid_response("模型完成但没有返回 Chat 文本"));
        }

        Ok(ModelCompletion {
            response_id,
            output_text: output.clone(),
            response_items: vec![PromptItem::AssistantMessage { content: output }],
            usage: parse_chat_usage(usage.as_ref(), self.config.capabilities()),
        })
    }
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
