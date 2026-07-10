use fairy_domain::{CachedTokenObservation, GatewayCapabilities, ModelUsage};
use serde_json::Value;

pub(crate) fn parse_chat_usage(
    usage: Option<&Value>,
    capabilities: GatewayCapabilities,
) -> ModelUsage {
    let input_tokens = usage
        .and_then(|value| value.get("prompt_tokens"))
        .and_then(Value::as_u64);
    let output_tokens = usage
        .and_then(|value| value.get("completion_tokens"))
        .and_then(Value::as_u64);
    let cached_input_tokens = usage.and_then(|value| {
        value
            .get("prompt_tokens_details")
            .and_then(|details| details.get("cached_tokens"))
            .and_then(Value::as_u64)
            .or_else(|| value.get("prompt_cache_hit_tokens").and_then(Value::as_u64))
    });

    ModelUsage {
        input_tokens,
        output_tokens,
        cached_input_tokens: CachedTokenObservation::from_provider(
            capabilities.cached_tokens_usage,
            cached_input_tokens,
        ),
        cache_write_tokens: CachedTokenObservation::from_provider(
            capabilities.cached_tokens_usage,
            None,
        ),
    }
}

#[cfg(test)]
mod tests {
    use fairy_domain::ModelProtocol;
    use serde_json::json;

    use super::*;

    #[test]
    fn maps_openai_and_deepseek_cache_hit_fields_without_inference() {
        for (usage, expected_cached) in [
            (
                json!({
                    "prompt_tokens": 120,
                    "completion_tokens": 14,
                    "prompt_tokens_details": {"cached_tokens": 64}
                }),
                64,
            ),
            (
                json!({
                    "prompt_tokens": 120,
                    "completion_tokens": 14,
                    "prompt_cache_hit_tokens": 80,
                    "prompt_cache_miss_tokens": 40
                }),
                80,
            ),
        ] {
            let parsed = parse_chat_usage(
                Some(&usage),
                GatewayCapabilities::for_protocol(ModelProtocol::ChatCompletions),
            );
            assert_eq!(parsed.input_tokens, Some(120));
            assert_eq!(parsed.output_tokens, Some(14));
            assert_eq!(
                parsed.cached_input_tokens,
                CachedTokenObservation::Observed(expected_cached)
            );
            assert_eq!(parsed.cache_write_tokens, CachedTokenObservation::Missing);
        }
    }

    #[test]
    fn preserves_observed_zero_missing_and_unsupported() {
        let capabilities = GatewayCapabilities::for_protocol(ModelProtocol::ChatCompletions);
        let observed_zero = parse_chat_usage(
            Some(&json!({"prompt_tokens_details": {"cached_tokens": 0}})),
            capabilities,
        );
        assert_eq!(
            observed_zero.cached_input_tokens,
            CachedTokenObservation::Observed(0)
        );

        let missing = parse_chat_usage(Some(&json!({"prompt_tokens": 10})), capabilities);
        assert_eq!(missing.cached_input_tokens, CachedTokenObservation::Missing);

        let unsupported = parse_chat_usage(
            Some(&json!({"prompt_cache_hit_tokens": 9})),
            GatewayCapabilities::responses_http(false, false),
        );
        assert_eq!(
            unsupported.cached_input_tokens,
            CachedTokenObservation::Unsupported
        );
        assert_eq!(
            unsupported.cache_write_tokens,
            CachedTokenObservation::Unsupported
        );
    }
}
