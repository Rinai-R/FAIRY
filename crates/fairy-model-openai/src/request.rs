use fairy_domain::{CompiledPromptRequest, FairyError, ModelConnectionConfig, ModelProtocol};
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
    input: Vec<OpenAiMessage>,
    tool_choice: &'static str,
    parallel_tool_calls: bool,
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

pub fn build_responses_request(
    client: &reqwest::Client,
    config: &ModelConnectionConfig,
    api_key: Option<&SecretString>,
    request: &CompiledPromptRequest,
) -> Result<reqwest::Request, FairyError> {
    validate_request(config, ModelProtocol::Responses, request)?;

    let url = protocol_url(config, ModelProtocol::Responses)?;
    let input = map_prompt_items(&request.input, OpenAiRole::Developer)?;
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
        tool_choice: "none",
        parallel_tool_calls: false,
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
        assert_eq!(body["prompt_cache_key"], "fairy:conversation:respond");
        assert_eq!(body["text"]["format"]["type"], "text");
        assert_eq!(body["input"].as_array().expect("input array").len(), 2);
        assert_eq!(body["input"][0]["role"], "developer");
        assert_eq!(body["input"][1]["role"], "user");
        assert_eq!(body["input"][1]["content"], "你好");
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
