use std::net::IpAddr;

use serde::{Deserialize, Serialize};
use url::{Host, Url};

use crate::{
    ErrorCode, FairyError, ModelConnectionId, PromptItem, PromptLane, ToolCall, WindowRevision,
};

const MODEL_CONNECTION_SCHEMA_VERSION: u32 = 2;
const MAX_MODEL_NAME_CHARS: usize = 200;

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ModelProtocol {
    Responses,
    ChatCompletions,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum AuthMode {
    BearerKey,
    NoAuth,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct GatewayCapabilities {
    pub prompt_cache_key: bool,
    pub cached_tokens_usage: bool,
    pub explicit_breakpoints: bool,
    pub cache_retention: bool,
    pub websocket_continuation: bool,
}

impl GatewayCapabilities {
    #[must_use]
    pub const fn responses_http(prompt_cache_key: bool, cached_tokens_usage: bool) -> Self {
        Self {
            prompt_cache_key,
            cached_tokens_usage,
            explicit_breakpoints: false,
            cache_retention: false,
            websocket_continuation: false,
        }
    }

    #[must_use]
    pub const fn for_protocol(protocol: ModelProtocol) -> Self {
        match protocol {
            ModelProtocol::Responses => Self::responses_http(true, true),
            ModelProtocol::ChatCompletions => Self::responses_http(false, true),
        }
    }
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ModelConnectionInput {
    pub protocol: ModelProtocol,
    pub endpoint: String,
    pub model: String,
    pub auth_mode: AuthMode,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct ModelConnectionConfig {
    schema_version: u32,
    connection_id: ModelConnectionId,
    protocol: ModelProtocol,
    endpoint: String,
    model: String,
    auth_mode: AuthMode,
    capabilities: GatewayCapabilities,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(tag = "status", content = "tokens", rename_all = "snake_case")]
pub enum CachedTokenObservation {
    Unsupported,
    Missing,
    Observed(u64),
}

impl CachedTokenObservation {
    #[must_use]
    pub const fn from_provider(capability_supported: bool, value: Option<u64>) -> Self {
        if !capability_supported {
            Self::Unsupported
        } else if let Some(tokens) = value {
            Self::Observed(tokens)
        } else {
            Self::Missing
        }
    }
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ModelUsage {
    pub input_tokens: Option<u64>,
    pub output_tokens: Option<u64>,
    pub cached_input_tokens: CachedTokenObservation,
    pub cache_write_tokens: CachedTokenObservation,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct LaneModelUsage {
    pub lane: PromptLane,
    pub history_window: WindowRevision,
    pub usage: ModelUsage,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum ModelStreamEvent {
    TextDelta { delta: String },
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct ModelCompletion {
    pub response_id: Option<String>,
    pub output: ModelTurnOutput,
    pub response_items: Vec<PromptItem>,
    pub usage: ModelUsage,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum ModelTurnOutput {
    Text { text: String },
    ToolCalls { calls: Vec<ToolCall> },
}

impl ModelTurnOutput {
    pub fn into_text(self) -> Result<String, FairyError> {
        match self {
            Self::Text { text } => Ok(text),
            Self::ToolCalls { .. } => Err(FairyError::new(
                ErrorCode::ModelResponseInvalid,
                "当前模型阶段需要文本，但收到工具调用",
                false,
            )),
        }
    }

    #[must_use]
    pub fn text(&self) -> Option<&str> {
        match self {
            Self::Text { text } => Some(text),
            Self::ToolCalls { .. } => None,
        }
    }
}

#[derive(Clone, Copy, Debug, Default)]
pub struct ModelConnectionCompiler;

impl ModelConnectionCompiler {
    pub fn compile(
        &self,
        connection_id: ModelConnectionId,
        input: ModelConnectionInput,
    ) -> Result<ModelConnectionConfig, FairyError> {
        let endpoint = validate_endpoint(&input.endpoint)?;
        let model = validate_model(&input.model)?;
        Ok(ModelConnectionConfig {
            schema_version: MODEL_CONNECTION_SCHEMA_VERSION,
            connection_id,
            protocol: input.protocol,
            endpoint,
            model,
            auth_mode: input.auth_mode,
            capabilities: GatewayCapabilities::for_protocol(input.protocol),
        })
    }
}

impl ModelConnectionConfig {
    #[must_use]
    pub const fn schema_version(&self) -> u32 {
        self.schema_version
    }

    #[must_use]
    pub const fn connection_id(&self) -> ModelConnectionId {
        self.connection_id
    }

    #[must_use]
    pub const fn protocol(&self) -> ModelProtocol {
        self.protocol
    }

    #[must_use]
    pub fn endpoint(&self) -> &str {
        &self.endpoint
    }

    #[must_use]
    pub fn model(&self) -> &str {
        &self.model
    }

    #[must_use]
    pub const fn auth_mode(&self) -> AuthMode {
        self.auth_mode
    }

    #[must_use]
    pub const fn capabilities(&self) -> GatewayCapabilities {
        self.capabilities
    }

    pub fn verify_integrity(&self) -> Result<(), FairyError> {
        if self.schema_version != MODEL_CONNECTION_SCHEMA_VERSION {
            return Err(invalid_model_config("模型连接配置版本不受支持"));
        }
        let rebuilt = ModelConnectionCompiler.compile(
            self.connection_id,
            ModelConnectionInput {
                protocol: self.protocol,
                endpoint: self.endpoint.clone(),
                model: self.model.clone(),
                auth_mode: self.auth_mode,
            },
        )?;
        if rebuilt != *self {
            return Err(invalid_model_config("模型连接 capability 不受支持"));
        }
        Ok(())
    }
}

fn validate_endpoint(raw: &str) -> Result<String, FairyError> {
    let value = raw.trim();
    let parsed =
        Url::parse(value).map_err(|_| invalid_model_config("模型 endpoint 不是有效 URL"))?;
    if parsed.username() != "" || parsed.password().is_some() {
        return Err(invalid_model_config("模型 endpoint 不得包含用户名或密码"));
    }
    if parsed.query().is_some() || parsed.fragment().is_some() {
        return Err(invalid_model_config(
            "模型 endpoint 不得包含 query 或 fragment",
        ));
    }

    match parsed.scheme() {
        "https" if parsed.host().is_some() => {}
        "http" if is_loopback_host(parsed.host()) => {}
        _ => {
            return Err(invalid_model_config(
                "模型 endpoint 必须使用 HTTPS；本机 loopback 服务可以使用 HTTP",
            ));
        }
    }
    let path_segments = parsed
        .path_segments()
        .ok_or_else(|| invalid_model_config("模型 endpoint 不能作为层级 URL"))?
        .filter(|segment| !segment.is_empty())
        .collect::<Vec<_>>();
    if path_segments
        .last()
        .is_some_and(|segment| *segment == "responses")
        || path_segments.ends_with(&["chat", "completions"])
    {
        return Err(invalid_model_config(
            "模型 endpoint 只能填写 Base URL，不得包含协议资源路径",
        ));
    }

    Ok(value.trim_end_matches('/').to_owned())
}

fn is_loopback_host(host: Option<Host<&str>>) -> bool {
    match host {
        Some(Host::Domain("localhost")) => true,
        Some(Host::Ipv4(address)) => address.is_loopback(),
        Some(Host::Ipv6(address)) => address.is_loopback(),
        Some(Host::Domain(domain)) => domain
            .parse::<IpAddr>()
            .is_ok_and(|address| address.is_loopback()),
        None => false,
    }
}

fn validate_model(raw: &str) -> Result<String, FairyError> {
    let value = raw.trim();
    let length = value.chars().count();
    if length == 0 || length > MAX_MODEL_NAME_CHARS || value.chars().any(char::is_control) {
        return Err(invalid_model_config(
            "模型名称必须是 1–200 个不含控制字符的 Unicode 字符",
        ));
    }
    Ok(value.to_owned())
}

fn invalid_model_config(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::InvalidModelConfig, message, false)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn input(endpoint: &str) -> ModelConnectionInput {
        ModelConnectionInput {
            protocol: ModelProtocol::Responses,
            endpoint: endpoint.to_owned(),
            model: "gpt-5.4".to_owned(),
            auth_mode: AuthMode::BearerKey,
        }
    }

    #[test]
    fn accepts_https_and_explicit_loopback_http_only() {
        for endpoint in [
            "https://api.openai.com/v1",
            "https://api.deepseek.com/",
            "http://localhost:11434/v1",
            "http://127.0.0.1:8080/v1",
            "http://[::1]:8080/v1",
        ] {
            ModelConnectionCompiler
                .compile(ModelConnectionId::new(), input(endpoint))
                .expect("allowed endpoint");
        }

        for endpoint in [
            "http://example.com/v1",
            "ftp://localhost/model",
            "https://user:password@example.com/v1",
            "https://example.com/v1?token=value",
            "not-a-url",
        ] {
            let error = ModelConnectionCompiler
                .compile(ModelConnectionId::new(), input(endpoint))
                .expect_err("unsafe endpoint must fail");
            assert_eq!(error.code, ErrorCode::InvalidModelConfig);
        }
    }

    #[test]
    fn normalizes_trailing_slashes_and_rejects_protocol_resource_paths() {
        let normalized = ModelConnectionCompiler
            .compile(
                ModelConnectionId::new(),
                input("https://api.deepseek.com/v1///"),
            )
            .expect("normalize base URL");
        assert_eq!(normalized.endpoint(), "https://api.deepseek.com/v1");

        for endpoint in [
            "https://api.openai.com/v1/responses",
            "https://api.deepseek.com/chat/completions/",
        ] {
            let error = ModelConnectionCompiler
                .compile(ModelConnectionId::new(), input(endpoint))
                .expect_err("protocol resource path must be rejected");
            assert_eq!(error.code, ErrorCode::InvalidModelConfig);
            assert!(error.message.contains("Base URL"));
        }
    }

    #[test]
    fn rejects_blank_control_and_oversized_model_names() {
        for model in [
            " ".to_owned(),
            "model\nother".to_owned(),
            "m".repeat(MAX_MODEL_NAME_CHARS + 1),
        ] {
            let mut invalid = input("https://example.com/v1");
            invalid.model = model;
            let error = ModelConnectionCompiler
                .compile(ModelConnectionId::new(), invalid)
                .expect_err("invalid model must fail");
            assert_eq!(error.code, ErrorCode::InvalidModelConfig);
        }
    }

    #[test]
    fn protocol_computes_cache_capabilities_without_user_switches() {
        let responses = ModelConnectionCompiler
            .compile(ModelConnectionId::new(), input("https://example.com/v1"))
            .expect("compile Responses config");

        assert_eq!(responses.protocol(), ModelProtocol::Responses);
        assert!(responses.capabilities().prompt_cache_key);
        assert!(responses.capabilities().cached_tokens_usage);
        assert!(!responses.capabilities().explicit_breakpoints);
        assert!(!responses.capabilities().cache_retention);
        assert!(!responses.capabilities().websocket_continuation);
        responses
            .verify_integrity()
            .expect("valid config integrity");

        let mut chat_input = input("https://api.deepseek.com");
        chat_input.protocol = ModelProtocol::ChatCompletions;
        let chat = ModelConnectionCompiler
            .compile(ModelConnectionId::new(), chat_input)
            .expect("compile Chat config");
        assert_eq!(chat.protocol(), ModelProtocol::ChatCompletions);
        assert!(!chat.capabilities().prompt_cache_key);
        assert!(chat.capabilities().cached_tokens_usage);
    }

    #[test]
    fn protocol_wire_values_are_exact_and_unknown_values_fail() {
        assert_eq!(
            serde_json::to_value(ModelProtocol::Responses).expect("serialize Responses"),
            serde_json::json!("responses")
        );
        assert_eq!(
            serde_json::to_value(ModelProtocol::ChatCompletions).expect("serialize Chat"),
            serde_json::json!("chat_completions")
        );
        assert!(serde_json::from_str::<ModelProtocol>(r#""auto""#).is_err());
    }

    #[test]
    fn cache_observation_distinguishes_unsupported_missing_zero_and_hit() {
        assert_eq!(
            CachedTokenObservation::from_provider(false, Some(99)),
            CachedTokenObservation::Unsupported
        );
        assert_eq!(
            CachedTokenObservation::from_provider(true, None),
            CachedTokenObservation::Missing
        );
        assert_eq!(
            CachedTokenObservation::from_provider(true, Some(0)),
            CachedTokenObservation::Observed(0)
        );
        assert_eq!(
            CachedTokenObservation::from_provider(true, Some(128)),
            CachedTokenObservation::Observed(128)
        );
    }
}
