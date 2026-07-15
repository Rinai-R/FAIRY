use fairy_domain::{
    CompiledPromptRequest, FairyError, ModelConnectionConfig, ModelProtocol, PromptItem,
};
use secrecy::SecretString;
use serde::Serialize;

use crate::shared::{
    AssistantMessageFormat, OpenAiMessage, authenticated_post, invalid_model_request,
    map_prompt_items, protocol_url, validate_request,
};

#[derive(Serialize)]
struct ResponsesRequestBody<'a> {
    model: &'a str,
    instructions: &'a str,
    input: Vec<OpenAiMessage>,
    max_output_tokens: u32,
    store: bool,
    stream: bool,
    text: TextConfiguration,
    #[serde(skip_serializing_if = "Option::is_none")]
    prompt_cache_key: Option<&'a str>,
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

fn map_responses_input(
    items: &[PromptItem],
    assistant_message_format: AssistantMessageFormat,
) -> Result<Vec<OpenAiMessage>, FairyError> {
    map_prompt_items(items, assistant_message_format)
}

pub fn build_responses_request(
    client: &reqwest::Client,
    config: &ModelConnectionConfig,
    api_key: Option<&SecretString>,
    request: &CompiledPromptRequest,
) -> Result<reqwest::Request, FairyError> {
    validate_request(config, ModelProtocol::Responses, request)?;

    let url = protocol_url(config, ModelProtocol::Responses)?;
    let assistant_message_format = if request.shape.lane == fairy_domain::PromptLane::Respond {
        AssistantMessageFormat::ReplyChainsJson
    } else {
        AssistantMessageFormat::PlainText
    };
    let input = map_responses_input(&request.input, assistant_message_format)?;
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
    let body = serde_json::to_vec(&ResponsesRequestBody {
        model: config.model(),
        instructions: &request.shape.instructions,
        input,
        max_output_tokens: request.shape.max_output_tokens,
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
        AuthMode, CharacterBriefInput, CharacterCompiler, CharacterId,
        DEFAULT_MODEL_CONTEXT_WINDOW_TOKENS, ErrorCode, GatewayCapabilities,
        ModelConnectionCompiler, ModelConnectionId, ModelConnectionInput, ModelProtocol,
        ModelRequestShape, PromptItem, PromptLane, ReasoningMode, Revision,
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
                model: "model-a".to_owned(),
                instructions: "stable instructions".to_owned(),
                max_output_tokens: 160,
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
        assert!(body.get("tool_choice").is_none());
        assert!(body.get("parallel_tool_calls").is_none());
        assert!(body.get("tools").is_none());
        assert_eq!(body["max_output_tokens"], 160);
        assert_eq!(body["prompt_cache_key"], "fairy:conversation:respond");
        assert_eq!(body["text"]["format"]["type"], "text");
        assert_eq!(body["input"].as_array().expect("input array").len(), 1);
        assert_eq!(body["input"][0]["role"], "user");
        assert_eq!(body["input"][0]["content"], "你好");
    }

    #[test]
    fn responses_keeps_character_context_as_user_data() {
        let request = build_responses_request(
            &reqwest::Client::new(),
            &config(AuthMode::BearerKey),
            Some(&SecretString::from("sk-exact".to_owned())),
            &character_context_request(),
        )
        .expect("build Responses request");
        let body = body_json(&request);
        let input = body["input"].as_array().expect("input array");

        assert_eq!(body["instructions"], "stable instructions");
        assert_eq!(input[0]["role"], "user");
        assert!(
            input[0]["content"]
                .as_str()
                .expect("character content")
                .contains("我是高性能的嘛")
        );
        assert_eq!(input[1]["role"], "user");
        assert_eq!(input[1]["content"], "你好");
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
