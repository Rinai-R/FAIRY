use fairy_domain::{
    AuthMode, CompiledPromptRequest, ErrorCode, FairyError, ModelConnectionConfig,
    ModelOutputContract, PromptItem,
};
use reqwest::header::{AUTHORIZATION, CONTENT_TYPE, HeaderValue};
use secrecy::{ExposeSecret, SecretString};
use serde::Serialize;
use serde_json::Value;
use url::Url;

#[derive(Serialize)]
struct ResponsesRequestBody<'a> {
    model: &'a str,
    instructions: &'a str,
    input: Vec<ResponseInputMessage>,
    tool_choice: &'static str,
    parallel_tool_calls: bool,
    store: bool,
    stream: bool,
    text: TextConfiguration,
    #[serde(skip_serializing_if = "Option::is_none")]
    prompt_cache_key: Option<&'a str>,
}

#[derive(Serialize)]
struct ResponseInputMessage {
    role: ResponseRole,
    content: String,
}

#[derive(Clone, Copy, Serialize)]
#[serde(rename_all = "lowercase")]
enum ResponseRole {
    Developer,
    User,
    Assistant,
}

#[derive(Serialize)]
struct TextConfiguration {
    format: TextFormat,
}

#[derive(Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
enum TextFormat {
    Text,
    JsonSchema {
        name: String,
        strict: bool,
        schema: Value,
    },
}

#[derive(Serialize)]
struct ContextData<'a> {
    fairy_context_data: &'a PromptItem,
}

pub fn build_responses_request(
    client: &reqwest::Client,
    config: &ModelConnectionConfig,
    api_key: Option<&SecretString>,
    request: &CompiledPromptRequest,
) -> Result<reqwest::Request, FairyError> {
    config.verify_integrity()?;
    if request.shape.model != config.model() {
        return Err(invalid_model_request(
            "请求模型与已配置模型不一致，不能复用该请求 shape",
        ));
    }
    if request.shape.parallel_tool_calls {
        return Err(invalid_model_request(
            "当前 FAIRY transport 不支持并行工具调用",
        ));
    }

    let url = responses_url(config.endpoint())?;
    let input = request
        .input
        .iter()
        .map(map_prompt_item)
        .collect::<Result<Vec<_>, _>>()?;
    let text = TextConfiguration {
        format: match &request.shape.output {
            ModelOutputContract::Text => TextFormat::Text,
            ModelOutputContract::JsonSchema {
                name,
                strict,
                schema_json,
            } => TextFormat::JsonSchema {
                name: name.clone(),
                strict: *strict,
                schema: serde_json::from_str(schema_json)
                    .map_err(|_| invalid_model_request("结构化输出 Schema 不是有效 JSON"))?,
            },
        },
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

    let mut builder = client
        .post(url)
        .header(CONTENT_TYPE, "application/json")
        .body(body);
    match (config.auth_mode(), api_key) {
        (AuthMode::BearerKey, Some(secret)) => {
            let value = secret.expose_secret();
            if value.is_empty() || value.trim() != value {
                return Err(FairyError::new(
                    ErrorCode::ModelSecretUnavailable,
                    "模型密钥为空或包含首尾空白字符",
                    false,
                ));
            }
            let mut header = HeaderValue::from_str(&format!("Bearer {value}")).map_err(|_| {
                FairyError::new(
                    ErrorCode::ModelSecretUnavailable,
                    "模型密钥不能编码为认证 Header",
                    false,
                )
            })?;
            header.set_sensitive(true);
            builder = builder.header(AUTHORIZATION, header);
        }
        (AuthMode::BearerKey, None) => {
            return Err(FairyError::new(
                ErrorCode::ModelSecretUnavailable,
                "BearerKey 连接缺少模型密钥",
                false,
            ));
        }
        (AuthMode::NoAuth, None) => {}
        (AuthMode::NoAuth, Some(_)) => {
            return Err(invalid_model_request("NoAuth 连接不得携带模型认证密钥"));
        }
    }

    builder
        .build()
        .map_err(|_| invalid_model_request("无法构造 Responses HTTP 请求"))
}

fn responses_url(endpoint: &str) -> Result<Url, FairyError> {
    let mut url =
        Url::parse(endpoint).map_err(|_| invalid_model_request("模型 endpoint 不是有效 URL"))?;
    url.path_segments_mut()
        .map_err(|_| invalid_model_request("模型 endpoint 不能作为层级 URL"))?
        .pop_if_empty()
        .push("responses");
    Ok(url)
}

fn map_prompt_item(item: &PromptItem) -> Result<ResponseInputMessage, FairyError> {
    match item {
        PromptItem::HarnessContext { .. } => Ok(ResponseInputMessage {
            role: ResponseRole::Developer,
            content: context_data(item)?,
        }),
        PromptItem::UserMessage { content } => Ok(ResponseInputMessage {
            role: ResponseRole::User,
            content: content.clone(),
        }),
        PromptItem::AssistantMessage { content } => Ok(ResponseInputMessage {
            role: ResponseRole::Assistant,
            content: content.clone(),
        }),
        PromptItem::CharacterActivated { .. }
        | PromptItem::UserProfileUpdated { .. }
        | PromptItem::TurnPlan { .. }
        | PromptItem::CompactionSummary { .. } => Ok(ResponseInputMessage {
            role: ResponseRole::User,
            content: context_data(item)?,
        }),
    }
}

fn context_data(item: &PromptItem) -> Result<String, FairyError> {
    serde_json::to_string(&ContextData {
        fairy_context_data: item,
    })
    .map_err(|_| invalid_model_request("无法序列化 FAIRY 上下文数据"))
}

fn invalid_model_request(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::ModelResponseInvalid, message, false)
}

#[cfg(test)]
mod tests {
    use fairy_domain::{
        GatewayCapabilities, ModelConnectionCompiler, ModelConnectionId, ModelConnectionInput,
        ModelRequestShape, PromptLane, ReasoningMode, ToolPolicy,
    };

    use super::*;

    fn config(auth_mode: AuthMode, prompt_cache_key: bool) -> ModelConnectionConfig {
        ModelConnectionCompiler
            .compile(
                ModelConnectionId::new(),
                ModelConnectionInput {
                    endpoint: match auth_mode {
                        AuthMode::BearerKey => "https://api.example.com/v1".to_owned(),
                        AuthMode::NoAuth => "http://127.0.0.1:11434/v1/".to_owned(),
                    },
                    model: "model-a".to_owned(),
                    auth_mode,
                    prompt_cache_key,
                    cached_tokens_usage: true,
                },
            )
            .expect("compile model config")
    }

    fn compiled(output: ModelOutputContract) -> CompiledPromptRequest {
        CompiledPromptRequest {
            shape: ModelRequestShape {
                lane: PromptLane::Respond,
                model: "model-a".to_owned(),
                instructions: "stable instructions".to_owned(),
                tool_policy: ToolPolicy::Disabled,
                parallel_tool_calls: false,
                reasoning: ReasoningMode::ProviderDefault,
                output,
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
            &config(AuthMode::BearerKey, true),
            Some(&key),
            &compiled(ModelOutputContract::Text),
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
    fn structured_request_maps_exact_json_schema() {
        let schema = serde_json::json!({
            "type": "object",
            "additionalProperties": false,
            "required": ["value"],
            "properties": {"value": {"type": "string"}}
        });
        let request = build_responses_request(
            &reqwest::Client::new(),
            &config(AuthMode::BearerKey, true),
            Some(&SecretString::from("sk-exact".to_owned())),
            &compiled(ModelOutputContract::JsonSchema {
                name: "result".to_owned(),
                strict: true,
                schema_json: serde_json::to_string(&schema).expect("serialize test schema"),
            }),
        )
        .expect("build structured request");
        let body = body_json(&request);

        assert_eq!(body["text"]["format"]["type"], "json_schema");
        assert_eq!(body["text"]["format"]["name"], "result");
        assert_eq!(body["text"]["format"]["strict"], true);
        assert_eq!(body["text"]["format"]["schema"], schema);
    }

    #[test]
    fn unsupported_cache_capability_omits_cache_field_without_padding() {
        let compiled = compiled(ModelOutputContract::Text);
        let request = build_responses_request(
            &reqwest::Client::new(),
            &config(AuthMode::NoAuth, false),
            None,
            &compiled,
        )
        .expect("build no-cache request");
        let body = body_json(&request);

        assert!(body.get("prompt_cache_key").is_none());
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
            &config(AuthMode::BearerKey, true),
            Some(&SecretString::from(raw.to_owned())),
            &compiled(ModelOutputContract::Text),
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
        let bearer = config(AuthMode::BearerKey, true);
        let request = compiled(ModelOutputContract::Text);
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
        let capabilities: GatewayCapabilities = config(AuthMode::BearerKey, true).capabilities();
        assert!(!capabilities.websocket_continuation);
        assert!(!capabilities.explicit_breakpoints);
        assert!(!capabilities.cache_retention);
    }
}
