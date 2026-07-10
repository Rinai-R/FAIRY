use fairy_domain::{
    AuthMode, CompiledPromptRequest, ErrorCode, FairyError, ModelConnectionConfig, ModelProtocol,
    PromptItem,
};
use reqwest::RequestBuilder;
use reqwest::header::{AUTHORIZATION, CONTENT_TYPE, HeaderValue};
use secrecy::{ExposeSecret, SecretString};
use serde::Serialize;
use url::Url;

#[derive(Clone, Copy, Serialize)]
#[serde(rename_all = "lowercase")]
pub(crate) enum OpenAiRole {
    System,
    Developer,
    User,
    Assistant,
}

#[derive(Serialize)]
pub(crate) struct OpenAiMessage {
    role: OpenAiRole,
    content: String,
}

impl OpenAiMessage {
    pub(crate) fn new(role: OpenAiRole, content: String) -> Self {
        Self { role, content }
    }
}

pub(crate) fn validate_request(
    config: &ModelConnectionConfig,
    expected_protocol: ModelProtocol,
    request: &CompiledPromptRequest,
) -> Result<(), FairyError> {
    config.verify_integrity()?;
    if config.protocol() != expected_protocol {
        return Err(invalid_model_request("模型连接协议与当前 transport 不一致"));
    }
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
    Ok(())
}

pub(crate) fn protocol_url(
    config: &ModelConnectionConfig,
    expected_protocol: ModelProtocol,
) -> Result<Url, FairyError> {
    if config.protocol() != expected_protocol {
        return Err(invalid_model_request("模型连接协议与请求路径不一致"));
    }
    let mut url = Url::parse(config.endpoint())
        .map_err(|_| invalid_model_request("模型 endpoint 不是有效 URL"))?;
    let mut segments = url
        .path_segments_mut()
        .map_err(|_| invalid_model_request("模型 endpoint 不能作为层级 URL"))?;
    segments.pop_if_empty();
    match expected_protocol {
        ModelProtocol::Responses => {
            segments.push("responses");
        }
        ModelProtocol::ChatCompletions => {
            segments.push("chat").push("completions");
        }
    }
    drop(segments);
    Ok(url)
}

pub(crate) fn authenticated_post(
    client: &reqwest::Client,
    url: Url,
    config: &ModelConnectionConfig,
    api_key: Option<&SecretString>,
) -> Result<RequestBuilder, FairyError> {
    let mut builder = client.post(url).header(CONTENT_TYPE, "application/json");
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
    Ok(builder)
}

pub(crate) fn map_prompt_items(
    items: &[PromptItem],
    harness_role: OpenAiRole,
) -> Result<Vec<OpenAiMessage>, FairyError> {
    items
        .iter()
        .map(|item| map_prompt_item(item, harness_role))
        .collect()
}

fn map_prompt_item(
    item: &PromptItem,
    harness_role: OpenAiRole,
) -> Result<OpenAiMessage, FairyError> {
    match item {
        PromptItem::HarnessContext { .. } => {
            Ok(OpenAiMessage::new(harness_role, context_data(item)?))
        }
        PromptItem::UserMessage { content } => {
            Ok(OpenAiMessage::new(OpenAiRole::User, content.clone()))
        }
        PromptItem::AssistantMessage { content } => {
            Ok(OpenAiMessage::new(OpenAiRole::Assistant, content.clone()))
        }
        PromptItem::CharacterActivated { .. }
        | PromptItem::UserProfileUpdated { .. }
        | PromptItem::CompactionSummary { .. } => {
            Ok(OpenAiMessage::new(OpenAiRole::User, context_data(item)?))
        }
    }
}

fn context_data(item: &PromptItem) -> Result<String, FairyError> {
    #[derive(Serialize)]
    struct ContextData<'a> {
        fairy_context_data: &'a PromptItem,
    }

    serde_json::to_string(&ContextData {
        fairy_context_data: item,
    })
    .map_err(|_| invalid_model_request("无法序列化 FAIRY 上下文数据"))
}

pub(crate) fn invalid_model_request(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::ModelResponseInvalid, message, false)
}

pub(crate) fn map_http_status(
    status: reqwest::StatusCode,
    protocol: ModelProtocol,
    url: &Url,
) -> FairyError {
    let protocol = match protocol {
        ModelProtocol::Responses => "responses",
        ModelProtocol::ChatCompletions => "chat_completions",
    };
    let context = format!(
        "HTTP {}，协议 {protocol}，路径 {}",
        status.as_u16(),
        url.path()
    );
    match status {
        reqwest::StatusCode::UNAUTHORIZED | reqwest::StatusCode::FORBIDDEN => FairyError::new(
            ErrorCode::ModelAuthFailed,
            format!("模型认证失败：{context}"),
            false,
        ),
        reqwest::StatusCode::NOT_FOUND | reqwest::StatusCode::METHOD_NOT_ALLOWED => {
            FairyError::new(
                ErrorCode::ModelProtocolMismatch,
                format!("模型服务不支持所选协议路径：{context}"),
                false,
            )
        }
        _ => FairyError::new(
            ErrorCode::ModelStreamFailed,
            format!("模型服务返回非成功状态：{context}"),
            status.is_server_error() || status == reqwest::StatusCode::TOO_MANY_REQUESTS,
        ),
    }
}

#[cfg(test)]
mod http_error_tests {
    use super::*;

    #[test]
    fn http_diagnostics_expose_only_status_protocol_and_path() {
        let url =
            Url::parse("https://api.example.test/v1/chat/completions?api_key=secret-query#hidden")
                .expect("parse diagnostic fixture URL");
        let cases = [
            (401, ErrorCode::ModelAuthFailed),
            (403, ErrorCode::ModelAuthFailed),
            (404, ErrorCode::ModelProtocolMismatch),
            (405, ErrorCode::ModelProtocolMismatch),
            (429, ErrorCode::ModelStreamFailed),
            (500, ErrorCode::ModelStreamFailed),
        ];
        for (status, expected) in cases {
            let error = map_http_status(
                reqwest::StatusCode::from_u16(status).expect("valid fixture status"),
                ModelProtocol::ChatCompletions,
                &url,
            );
            assert_eq!(error.code, expected);
            assert!(error.message.contains(&status.to_string()));
            assert!(error.message.contains("chat_completions"));
            assert!(error.message.contains("/v1/chat/completions"));
            for forbidden in [
                "secret-query",
                "api_key",
                "#hidden",
                "Authorization",
                "Bearer",
            ] {
                assert!(!error.message.contains(forbidden));
                assert!(!format!("{error:?}").contains(forbidden));
                assert!(
                    !serde_json::to_string(&error)
                        .expect("serialize safe error")
                        .contains(forbidden)
                );
            }
        }
    }
}
