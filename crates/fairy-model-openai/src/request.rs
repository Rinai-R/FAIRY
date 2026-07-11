use fairy_domain::{
    CompiledPromptRequest, FairyError, ModelConnectionConfig, ModelProtocol, PromptItem,
    ToolDefinition, ToolPolicy,
};
use secrecy::SecretString;
use serde::Serialize;

use crate::shared::{
    OpenAiMessage, OpenAiRole, authenticated_post, invalid_model_request, map_prompt_items,
    protocol_url, validate_request,
};

#[derive(Serialize)]
struct ResponsesRequestBody<'a> {
    model: &'a str,
    instructions: &'a str,
    input: Vec<ResponsesInputItem>,
    tool_choice: &'static str,
    parallel_tool_calls: bool,
    max_output_tokens: u32,
    #[serde(skip_serializing_if = "Option::is_none")]
    tools: Option<Vec<ResponsesTool<'a>>>,
    store: bool,
    stream: bool,
    text: TextConfiguration,
    #[serde(skip_serializing_if = "Option::is_none")]
    prompt_cache_key: Option<&'a str>,
}

#[derive(Serialize)]
#[serde(untagged)]
enum ResponsesInputItem {
    Message(OpenAiMessage),
    FunctionCall {
        #[serde(rename = "type")]
        kind: &'static str,
        call_id: String,
        name: &'static str,
        arguments: String,
    },
    FunctionCallOutput {
        #[serde(rename = "type")]
        kind: &'static str,
        call_id: String,
        output: String,
    },
}

#[derive(Serialize)]
struct ResponsesTool<'a> {
    #[serde(rename = "type")]
    kind: &'static str,
    name: &'static str,
    description: &'a str,
    parameters: &'a serde_json::Value,
    strict: bool,
}

#[derive(Serialize)]
struct TextConfiguration {
    format: TextFormat,
}

#[derive(Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
enum TextFormat {
    Text,
}

fn map_tools(tools: &[ToolDefinition]) -> Vec<ResponsesTool<'_>> {
    tools
        .iter()
        .map(|tool| ResponsesTool {
            kind: "function",
            name: tool.name.as_str(),
            description: &tool.description,
            parameters: &tool.parameters,
            strict: true,
        })
        .collect()
}

fn map_responses_input(items: &[PromptItem]) -> Result<Vec<ResponsesInputItem>, FairyError> {
    items
        .iter()
        .map(|item| match item {
            PromptItem::ToolCall { call } => Ok(ResponsesInputItem::FunctionCall {
                kind: "function_call",
                call_id: call.id.clone(),
                name: call.name.as_str(),
                arguments: call.arguments_json.clone(),
            }),
            PromptItem::ToolResult { result } => Ok(ResponsesInputItem::FunctionCallOutput {
                kind: "function_call_output",
                call_id: result.call_id.clone(),
                output: serde_json::to_string(&result.outcome)
                    .map_err(|_| invalid_model_request("无法序列化 Responses 工具结果"))?,
            }),
            _ => {
                let mut messages =
                    map_prompt_items(std::slice::from_ref(item), OpenAiRole::Developer)?;
                let message = messages
                    .pop()
                    .ok_or_else(|| invalid_model_request("Responses message mapper 返回空结果"))?;
                Ok(ResponsesInputItem::Message(message))
            }
        })
        .collect()
}

pub fn build_responses_request(
    client: &reqwest::Client,
    config: &ModelConnectionConfig,
    api_key: Option<&SecretString>,
    request: &CompiledPromptRequest,
) -> Result<reqwest::Request, FairyError> {
    validate_request(config, ModelProtocol::Responses, request)?;

    let url = protocol_url(config, ModelProtocol::Responses)?;
    let input = map_responses_input(&request.input)?;
    let text = TextConfiguration {
        format: TextFormat::Text,
    };
    let prompt_cache_key = if config.capabilities().prompt_cache_key {
        Some(
            request
                .shape
                .prompt_cache_key
                .as_deref()
                .ok_or_else(|| invalid_model_request("当前 capability 需要稳定 cache key"))?,
        )
    } else {
        None
    };
    let (tools, tool_choice) = match &request.shape.tool_policy {
        ToolPolicy::Disabled => (None, "none"),
        ToolPolicy::Auto { tools } => (Some(map_tools(tools)), "auto"),
    };
    let body = serde_json::to_vec(&ResponsesRequestBody {
        model: config.model(),
        instructions: &request.shape.instructions,
        input,
        tool_choice,
        parallel_tool_calls: false,
        max_output_tokens: request.shape.max_output_tokens,
        tools,
        store: false,
        stream: true,
        text,
        prompt_cache_key,
    })
    .map_err(|_| invalid_model_request("无法序列化 Responses 请求"))?;

    authenticated_post(client, url, config, api_key)?
        .body(body)
        .build()
        .map_err(|_| invalid_model_request("无法构造 Responses HTTP 请求"))
}

#[cfg(test)]
mod tests {
    use fairy_domain::{
        AuthMode, ErrorCode, GatewayCapabilities, ModelConnectionCompiler, ModelConnectionId,
        ModelConnectionInput, ModelProtocol, ModelRequestShape, PromptItem, PromptLane,
        ReasoningMode, ToolPolicy,
    };
    use reqwest::header::AUTHORIZATION;
    use serde_json::Value;

    use super::*;

    fn config(auth_mode: AuthMode) -> ModelConnectionConfig {
        ModelConnectionCompiler
            .compile(
                ModelConnectionId::new(),
                ModelConnectionInput {
                    protocol: ModelProtocol::Responses,
                    endpoint: match auth_mode {
                        AuthMode::BearerKey => "https://api.example.com/v1".to_owned(),
                        AuthMode::NoAuth => "http://127.0.0.1:11434/v1/".to_owned(),
                    },
                    model: "model-a".to_owned(),
                    auth_mode,
                },
            )
            .expect("compile model config")
    }

    fn compiled() -> CompiledPromptRequest {
        CompiledPromptRequest {
            shape: ModelRequestShape {
                lane: PromptLane::Respond,
                model: "model-a".to_owned(),
                instructions: "stable instructions".to_owned(),
                max_output_tokens: 160,
                tool_policy: ToolPolicy::Disabled,
                parallel_tool_calls: false,
                reasoning: ReasoningMode::ProviderDefault,
                prompt_cache_key: Some("fairy:conversation:respond".to_owned()),
            },
            input: vec![PromptItem::UserMessage {
                content: "你好".to_owned(),
            }],
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
    fn text_request_has_stable_full_http_shape_and_ordered_input() {
        let client = reqwest::Client::new();
        let key = SecretString::from("sk-exact".to_owned());
        let request = build_responses_request(
            &client,
            &config(AuthMode::BearerKey),
            Some(&key),
            &compiled(),
        )
        .expect("build text request");
        let body = body_json(&request);

        assert_eq!(
            request.url().as_str(),
            "https://api.example.com/v1/responses"
        );
        assert_eq!(body["model"], "model-a");
        assert_eq!(body["instructions"], "stable instructions");
        assert_eq!(body["stream"], true);
        assert_eq!(body["store"], false);
        assert_eq!(body["tool_choice"], "none");
        assert_eq!(body["parallel_tool_calls"], false);
        assert_eq!(body["max_output_tokens"], 160);
        assert_eq!(body["prompt_cache_key"], "fairy:conversation:respond");
        assert_eq!(body["text"]["format"]["type"], "text");
        assert_eq!(body["input"].as_array().expect("input array").len(), 1);
        assert_eq!(body["input"][0]["role"], "user");
        assert_eq!(body["input"][0]["content"], "你好");
    }

    #[test]
    fn tool_request_maps_responses_function_items_and_schema() {
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
                    arguments_json: r#"{"query":"Rust"}"#.to_owned(),
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
        let request = build_responses_request(
            &reqwest::Client::new(),
            &config(AuthMode::BearerKey),
            Some(&SecretString::from("sk-exact".to_owned())),
            &compiled,
        )
        .expect("build Responses tool request");
        let body = body_json(&request);

        assert_eq!(body["tool_choice"], "auto");
        assert_eq!(body["tools"][0]["name"], "web_search");
        assert_eq!(body["tools"][0]["strict"], true);
        assert!(
            body["input"]
                .as_array()
                .expect("input")
                .iter()
                .any(|item| item["type"] == "function_call" && item["call_id"] == "call_1")
        );
        assert!(
            body["input"]
                .as_array()
                .expect("input")
                .iter()
                .any(|item| item["type"] == "function_call_output" && item["call_id"] == "call_1")
        );
    }

    #[test]
    fn responses_cache_key_is_automatic_without_prompt_padding() {
        let compiled = compiled();
        let request = build_responses_request(
            &reqwest::Client::new(),
            &config(AuthMode::NoAuth),
            None,
            &compiled,
        )
        .expect("build automatic cache request");
        let body = body_json(&request);

        assert_eq!(body["prompt_cache_key"], "fairy:conversation:respond");
        assert_eq!(
            body["input"].as_array().expect("input array").len(),
            compiled.input.len()
        );
        assert_eq!(
            request.url().as_str(),
            "http://127.0.0.1:11434/v1/responses"
        );
    }

    #[test]
    fn bearer_header_is_sensitive_and_secret_never_enters_body_or_debug() {
        let raw = "sk-never-log";
        let request = build_responses_request(
            &reqwest::Client::new(),
            &config(AuthMode::BearerKey),
            Some(&SecretString::from(raw.to_owned())),
            &compiled(),
        )
        .expect("build bearer request");

        assert!(request.headers()[AUTHORIZATION].is_sensitive());
        assert!(
            !String::from_utf8_lossy(
                request
                    .body()
                    .and_then(reqwest::Body::as_bytes)
                    .expect("body bytes")
            )
            .contains(raw)
        );
        assert!(!format!("{request:?}").contains(raw));
    }

    #[test]
    fn auth_and_shape_mismatch_fail_before_network() {
        let client = reqwest::Client::new();
        let bearer = config(AuthMode::BearerKey);
        let request = compiled();
        assert_eq!(
            build_responses_request(&client, &bearer, None, &request)
                .expect_err("missing bearer key")
                .code,
            ErrorCode::ModelSecretUnavailable
        );

        let mut changed = request;
        changed.shape.model = "other-model".to_owned();
        assert_eq!(
            build_responses_request(
                &client,
                &bearer,
                Some(&SecretString::from("sk-exact".to_owned())),
                &changed,
            )
            .expect_err("shape model mismatch")
            .code,
            ErrorCode::ModelResponseInvalid
        );
    }

    #[test]
    fn http_gateway_capabilities_remain_explicitly_non_websocket() {
        let capabilities: GatewayCapabilities = config(AuthMode::BearerKey).capabilities();
        assert!(!capabilities.websocket_continuation);
        assert!(!capabilities.explicit_breakpoints);
        assert!(!capabilities.cache_retention);
    }
}
