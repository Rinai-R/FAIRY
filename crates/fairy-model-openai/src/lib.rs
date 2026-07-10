//! OpenAI-compatible Responses 模型传输。

#![forbid(unsafe_code)]

use std::sync::Arc;

use fairy_domain::{FairyError, ModelConnectionConfig, ModelProtocol};
use fairy_harness::ModelGateway;
use secrecy::SecretString;

mod chat_request;
mod chat_stream;
mod chat_usage;
mod request;
mod response_stream;
mod shared;
mod usage;

pub use chat_request::build_chat_completions_request;
pub use chat_stream::OpenAiChatCompletionsGateway;
pub use request::build_responses_request;
pub use response_stream::OpenAiResponsesGateway;

pub fn build_openai_compatible_gateway(
    config: ModelConnectionConfig,
    api_key: Option<SecretString>,
) -> Result<Arc<dyn ModelGateway + Send + Sync>, FairyError> {
    match config.protocol() {
        ModelProtocol::Responses => Ok(Arc::new(OpenAiResponsesGateway::new(config, api_key)?)),
        ModelProtocol::ChatCompletions => Ok(Arc::new(OpenAiChatCompletionsGateway::new(
            config, api_key,
        )?)),
    }
}

#[cfg(test)]
mod tests {
    use fairy_domain::{
        AuthMode, ModelConnectionCompiler, ModelConnectionId, ModelConnectionInput,
    };

    use super::*;

    fn config(protocol: ModelProtocol) -> ModelConnectionConfig {
        ModelConnectionCompiler
            .compile(
                ModelConnectionId::new(),
                ModelConnectionInput {
                    protocol,
                    endpoint: "http://127.0.0.1:11434/v1".to_owned(),
                    model: "test-model".to_owned(),
                    auth_mode: AuthMode::NoAuth,
                },
            )
            .expect("compile factory fixture")
    }

    #[test]
    fn factory_constructs_only_the_explicit_protocol_gateway() {
        let responses = build_openai_compatible_gateway(config(ModelProtocol::Responses), None)
            .expect("build Responses gateway");
        assert!(responses.capabilities().prompt_cache_key);

        let chat = build_openai_compatible_gateway(config(ModelProtocol::ChatCompletions), None)
            .expect("build Chat gateway");
        assert!(!chat.capabilities().prompt_cache_key);
        assert!(chat.capabilities().cached_tokens_usage);
    }
}
