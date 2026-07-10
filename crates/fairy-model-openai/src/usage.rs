use fairy_domain::{CachedTokenObservation, GatewayCapabilities, ModelUsage};
use serde_json::Value;

pub(crate) fn parse_usage(response: &Value, capabilities: GatewayCapabilities) -> ModelUsage {
    let usage = response.get("usage");
    let input_tokens = usage
        .and_then(|value| value.get("input_tokens"))
        .and_then(Value::as_u64);
    let output_tokens = usage
        .and_then(|value| value.get("output_tokens"))
        .and_then(Value::as_u64);
    let cached_input_tokens = usage
        .and_then(|value| value.get("input_tokens_details"))
        .and_then(|value| value.get("cached_tokens"))
        .and_then(Value::as_u64);
    let cache_write_tokens = usage.and_then(|value| {
        value
            .get("cache_write_tokens")
            .and_then(Value::as_u64)
            .or_else(|| {
                value
                    .get("input_tokens_details")
                    .and_then(|details| details.get("cache_write_tokens"))
                    .and_then(Value::as_u64)
            })
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
            cache_write_tokens,
        ),
    }
}

#[cfg(test)]
mod tests {
    use fairy_domain::GatewayCapabilities;
    use serde_json::json;

    use super::*;

    #[test]
    fn parses_hit_zero_and_write_tokens_from_real_fields() {
        let usage = parse_usage(
            &json!({
                "usage": {
                    "input_tokens": 120,
                    "output_tokens": 30,
                    "input_tokens_details": {
                        "cached_tokens": 0,
                        "cache_write_tokens": 18
                    }
                }
            }),
            GatewayCapabilities::responses_http(true, true),
        );

        assert_eq!(usage.input_tokens, Some(120));
        assert_eq!(usage.output_tokens, Some(30));
        assert_eq!(
            usage.cached_input_tokens,
            CachedTokenObservation::Observed(0)
        );
        assert_eq!(
            usage.cache_write_tokens,
            CachedTokenObservation::Observed(18)
        );
    }

    #[test]
    fn missing_and_unsupported_remain_distinct_even_if_payload_has_value() {
        let missing = parse_usage(
            &json!({"usage": {"input_tokens": 5}}),
            GatewayCapabilities::responses_http(true, true),
        );
        assert_eq!(missing.cached_input_tokens, CachedTokenObservation::Missing);

        let unsupported = parse_usage(
            &json!({"usage": {"input_tokens_details": {"cached_tokens": 99}}}),
            GatewayCapabilities::responses_http(false, false),
        );
        assert_eq!(
            unsupported.cached_input_tokens,
            CachedTokenObservation::Unsupported
        );
    }
}
