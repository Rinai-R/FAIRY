use fairy_domain::{CompiledPromptRequest, FairyError, ModelConnectionConfig, ModelProtocol};
use secrecy::SecretString;
use serde::Serialize;

use crate::shared::{
    OpenAiMessage, OpenAiRole, authenticated_post, invalid_model_request, map_prompt_items,
    protocol_url, validate_request,
};

#[derive(Serialize)]
struct ChatCompletionsRequestBody<'a> {
    model: &'a str,
    messages: Vec<OpenAiMessage>,
    stream: bool,
    stream_options: StreamOptions,
}

#[derive(Serialize)]
struct StreamOptions {
    include_usage: bool,
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
    messages.extend(map_prompt_items(&request.input, OpenAiRole::System)?);

    let body = serde_json::to_vec(&ChatCompletionsRequestBody {
        model: config.model(),
        messages,
        stream: true,
        stream_options: StreamOptions {
            include_usage: true,
        },
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
