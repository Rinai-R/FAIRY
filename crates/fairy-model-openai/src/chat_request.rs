use fairy_domain::{
    CompiledPromptRequest, FairyError, ModelConnectionConfig, ModelProtocol, PromptLane,
};
use secrecy::SecretString;
use serde::Serialize;

use crate::shared::{
    AssistantMessageFormat, OpenAiMessage, OpenAiRole, authenticated_post, invalid_model_request,
    map_prompt_items, protocol_url, validate_request,
};

#[derive(Serialize)]
struct ChatCompletionsRequestBody<'a> {
    model: &'a str,
    messages: Vec<OpenAiMessage>,
    stream: bool,
    stream_options: StreamOptions,
    max_tokens: u32,
    #[serde(skip_serializing_if = "Option::is_none")]
    response_format: Option<ResponseFormat>,
}

#[derive(Serialize)]
struct StreamOptions {
    include_usage: bool,
}

#[derive(Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
enum ResponseFormat {
    JsonObject,
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
    let assistant_message_format = if request.shape.lane == PromptLane::Respond {
        AssistantMessageFormat::ReplyChainsJson
    } else {
        AssistantMessageFormat::PlainText
    };
    messages.extend(map_prompt_items(&request.input, assistant_message_format)?);

    let body = serde_json::to_vec(&ChatCompletionsRequestBody {
        model: config.model(),
        messages,
        stream: true,
        stream_options: StreamOptions {
            include_usage: true,
        },
        max_tokens: request.shape.max_output_tokens,
        response_format: (request.shape.lane == PromptLane::Respond)
            .then_some(ResponseFormat::JsonObject),
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
        AuthMode, CharacterBriefInput, CharacterCompiler, CharacterId, CompiledPromptRequest,
        DEFAULT_MODEL_CONTEXT_WINDOW_TOKENS, ErrorCode, ModelConnectionCompiler, ModelConnectionId,
        ModelConnectionInput, ModelProtocol, ModelRequestShape, PromptItem, PromptLane,
        ReasoningMode, Revision,
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
                    context_window_tokens: DEFAULT_MODEL_CONTEXT_WINDOW_TOKENS,
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
                max_output_tokens: 160,
                reasoning: ReasoningMode::ProviderDefault,
                prompt_cache_key: Some("fairy:conversation:respond".to_owned()),
            },
            input: vec![
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

    fn character_context_request() -> CompiledPromptRequest {
        let snapshot = CharacterCompiler
            .compile(
                CharacterId::new(),
                Revision::INITIAL,
                CharacterBriefInput {
                    name: "亚托莉".to_owned(),
                    description: "有点骄傲，说话简短；口癖「我是高性能的嘛！」。".to_owned(),
                    dialogue_style: None,
                },
            )
            .expect("compile character");
        let mut request = compiled();
        request
            .input
            .insert(0, PromptItem::CharacterActivated { snapshot });
        request
    }

    #[test]
    fn respond_request_uses_deepseek_json_output_without_tools() {
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
        assert_eq!(body["response_format"]["type"], "json_object");
        assert_eq!(body["messages"][0]["role"], "system");
        assert_eq!(body["messages"][0]["content"], "stable instructions");
        assert_eq!(body["messages"][1]["role"], "user");
        assert_eq!(body["messages"][1]["content"], "你好");
        assert_eq!(body["messages"][2]["role"], "assistant");
        assert_eq!(
            body["messages"][2]["content"],
            r#"{"chains":[{"visualState":"idle","text":"我在"}]}"#
        );
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
    fn chat_completions_keeps_character_context_as_user_data() {
        let request = build_chat_completions_request(
            &reqwest::Client::new(),
            &config(ModelProtocol::ChatCompletions, AuthMode::BearerKey),
            Some(&SecretString::from("sk-exact".to_owned())),
            &character_context_request(),
        )
        .expect("build Chat request");
        let body = body_json(&request);
        let messages = body["messages"].as_array().expect("messages array");

        assert_eq!(messages[0]["role"], "system");
        assert_eq!(messages[0]["content"], "stable instructions");
        assert_eq!(messages[1]["role"], "user");
        assert!(
            messages[1]["content"]
                .as_str()
                .expect("character content")
                .contains("我是高性能的嘛")
        );
        assert_eq!(messages[2]["role"], "user");
        assert_eq!(messages[2]["content"], "你好");
        assert_eq!(messages[3]["role"], "assistant");
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
