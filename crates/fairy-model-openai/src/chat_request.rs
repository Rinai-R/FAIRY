use fairy_domain::{
    CompiledPromptRequest, FairyError, ModelConnectionConfig, ModelProtocol, ToolDefinition,
    ToolPolicy,
};
use secrecy::SecretString;
use serde::Serialize;

use crate::shared::{
    OpenAiMessage, OpenAiRole, authenticated_post, invalid_model_request, map_chat_prompt_items,
    protocol_url, validate_request,
};

#[derive(Serialize)]
struct ChatCompletionsRequestBody<'a> {
    model: &'a str,
    messages: Vec<OpenAiMessage>,
    stream: bool,
    stream_options: StreamOptions,
    max_tokens: u32,
    #[serde(skip_serializing_if = "Option::is_none")]
    tools: Option<Vec<ChatTool<'a>>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    tool_choice: Option<&'static str>,
    #[serde(skip_serializing_if = "Option::is_none")]
    parallel_tool_calls: Option<bool>,
}

#[derive(Serialize)]
struct StreamOptions {
    include_usage: bool,
}

#[derive(Serialize)]
struct ChatTool<'a> {
    #[serde(rename = "type")]
    kind: &'static str,
    function: ChatFunction<'a>,
}

#[derive(Serialize)]
struct ChatFunction<'a> {
    name: &'static str,
    description: &'a str,
    parameters: &'a serde_json::Value,
}

fn map_tools(tools: &[ToolDefinition]) -> Vec<ChatTool<'_>> {
    tools
        .iter()
        .map(|tool| ChatTool {
            kind: "function",
            function: ChatFunction {
                name: tool.name.as_str(),
                description: &tool.description,
                parameters: &tool.parameters,
            },
        })
        .collect()
}

pub fn build_chat_completions_request(
    client: &reqwest::Client,
    config: &ModelConnectionConfig,
    api_key: Option<&SecretString>,
    request: &CompiledPromptRequest,
) -> Result<reqwest::Request, FairyError> {
    validate_request(config, ModelProtocol::ChatCompletions, request)?;
    let mut messages = Vec::with_capacity(request.input.len() + 1);
    messages.push(OpenAiMessage::new(
        OpenAiRole::System,
        request.shape.instructions.clone(),
    ));
    messages.extend(map_chat_prompt_items(&request.input, OpenAiRole::System)?);
    let (tools, tool_choice, parallel_tool_calls) = match &request.shape.tool_policy {
        ToolPolicy::Disabled => (None, None, None),
        ToolPolicy::Auto { tools } => (Some(map_tools(tools)), Some("auto"), Some(false)),
    };

    let body = serde_json::to_vec(&ChatCompletionsRequestBody {
        model: config.model(),
        messages,
        stream: true,
        stream_options: StreamOptions {
            include_usage: true,
        },
        max_tokens: request.shape.max_output_tokens,
        tools,
        tool_choice,
        parallel_tool_calls,
    })
    .map_err(|_| invalid_model_request("无法序列化 Chat Completions 请求"))?;

    authenticated_post(
        client,
        protocol_url(config, ModelProtocol::ChatCompletions)?,
        config,
        api_key,
    )?
    .body(body)
    .build()
    .map_err(|_| invalid_model_request("无法构造 Chat Completions HTTP 请求"))
}

#[cfg(test)]
mod tests {
    use fairy_domain::{
        AuthMode, CompiledPromptRequest, ErrorCode, ModelConnectionCompiler, ModelConnectionId,
        ModelConnectionInput, ModelProtocol, ModelRequestShape, PromptItem, PromptLane,
        ReasoningMode, ToolPolicy,
    };
    use secrecy::SecretString;
    use serde_json::Value;

    use super::*;

    fn config(protocol: ModelProtocol, auth_mode: AuthMode) -> ModelConnectionConfig {
        ModelConnectionCompiler
            .compile(
                ModelConnectionId::new(),
                ModelConnectionInput {
                    protocol,
                    endpoint: match auth_mode {
                        AuthMode::BearerKey => "https://api.deepseek.com".to_owned(),
                        AuthMode::NoAuth => "http://127.0.0.1:11434/v1/".to_owned(),
                    },
                    model: "deepseek-v4-flash".to_owned(),
                    auth_mode,
                },
            )
            .expect("compile model config")
    }

    fn compiled() -> CompiledPromptRequest {
        CompiledPromptRequest {
            shape: ModelRequestShape {
                lane: PromptLane::Respond,
                model: "deepseek-v4-flash".to_owned(),
                instructions: "stable instructions".to_owned(),
                reply_mode: None,
                max_output_tokens: 160,
                tool_policy: ToolPolicy::Disabled,
                parallel_tool_calls: false,
                reasoning: ReasoningMode::ProviderDefault,
                prompt_cache_key: Some("fairy:conversation:respond".to_owned()),
            },
            input: vec![
                PromptItem::HarnessContext {
                    protocol_version: "v1".to_owned(),
                    policy_version: "policy-v1".to_owned(),
                    priorities: vec![],
                },
                PromptItem::UserMessage {
                    content: "你好".to_owned(),
                },
                PromptItem::AssistantMessage {
                    content: "我在".to_owned(),
                },
            ],
        }
    }

    fn body_json(request: &reqwest::Request) -> Value {
        serde_json::from_slice(
            request
                .body()
                .and_then(reqwest::Body::as_bytes)
                .expect("request body bytes"),
        )
        .expect("parse request body")
    }

    #[test]
    fn text_request_has_minimal_deepseek_compatible_shape() {
        let request = build_chat_completions_request(
            &reqwest::Client::new(),
            &config(ModelProtocol::ChatCompletions, AuthMode::BearerKey),
            Some(&SecretString::from("sk-exact".to_owned())),
            &compiled(),
        )
        .expect("build Chat request");
        let body = body_json(&request);

        assert_eq!(
            request.url().as_str(),
            "https://api.deepseek.com/chat/completions"
        );
        assert_eq!(body["model"], "deepseek-v4-flash");
        assert_eq!(body["stream"], true);
        assert_eq!(body["stream_options"]["include_usage"], true);
        assert_eq!(body["max_tokens"], 160);
        assert!(body.get("response_format").is_none());
        assert_eq!(body["messages"][0]["role"], "system");
        assert_eq!(body["messages"][0]["content"], "stable instructions");
        assert_eq!(body["messages"][1]["role"], "system");
        assert_eq!(body["messages"][2]["role"], "user");
        assert_eq!(body["messages"][2]["content"], "你好");
        assert_eq!(body["messages"][3]["role"], "assistant");
        assert_eq!(body["messages"][3]["content"], "我在");
        for absent in [
            "prompt_cache_key",
            "previous_response_id",
            "tools",
            "tool_choice",
            "parallel_tool_calls",
            "store",
        ] {
            assert!(body.get(absent).is_none(), "unexpected field: {absent}");
        }
    }

    #[test]
    fn tool_request_maps_schema_and_history_without_leaking_into_text() {
        use fairy_domain::{ToolCall, ToolDefinition, ToolName, ToolResult, ToolResultOutcome};

        let mut compiled = compiled();
        compiled.shape.tool_policy = ToolPolicy::Auto {
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
        compiled.input.extend([
            PromptItem::ToolCall {
                call: ToolCall {
                    id: "call_1".to_owned(),
                    name: ToolName::WebSearch,
                    arguments_json: r#"{"query":"Rust 1.95"}"#.to_owned(),
                },
            },
            PromptItem::ToolResult {
                result: ToolResult {
                    call_id: "call_1".to_owned(),
                    name: ToolName::WebSearch,
                    outcome: ToolResultOutcome::Success {
                        output: "搜索完成".to_owned(),
                        sources: Vec::new(),
                    },
                },
            },
        ]);

        let request = build_chat_completions_request(
            &reqwest::Client::new(),
            &config(ModelProtocol::ChatCompletions, AuthMode::BearerKey),
            Some(&SecretString::from("sk-exact".to_owned())),
            &compiled,
        )
        .expect("build tool request");
        let body = body_json(&request);

        assert_eq!(body["tool_choice"], "auto");
        assert_eq!(body["parallel_tool_calls"], false);
        assert_eq!(body["tools"][0]["type"], "function");
        assert_eq!(body["tools"][0]["function"]["name"], "web_search");
        let assistant = body["messages"]
            .as_array()
            .expect("messages")
            .iter()
            .find(|message| message.get("tool_calls").is_some())
            .expect("assistant tool call history");
        assert_eq!(assistant["role"], "assistant");
        assert!(assistant.get("content").is_none());
        assert_eq!(assistant["tool_calls"][0]["id"], "call_1");
        let result = body["messages"]
            .as_array()
            .expect("messages")
            .iter()
            .find(|message| message["role"] == "tool")
            .expect("tool result history");
        assert_eq!(result["tool_call_id"], "call_1");
        assert!(
            result["content"]
                .as_str()
                .expect("result json")
                .contains("搜索完成")
        );
    }

    #[test]
    fn protocol_mismatch_fails_before_network() {
        let client = reqwest::Client::new();
        let error = build_chat_completions_request(
            &client,
            &config(ModelProtocol::Responses, AuthMode::NoAuth),
            None,
            &compiled(),
        )
        .expect_err("Responses config cannot build Chat request");
        assert_eq!(error.code, ErrorCode::ModelResponseInvalid);
    }
}
