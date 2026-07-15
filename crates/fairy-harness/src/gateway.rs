use async_trait::async_trait;
use fairy_domain::{
    CompiledPromptRequest, ErrorCode, FairyError, GatewayCapabilities, ModelCompletion,
    ModelStreamEvent, PromptContinuation, PromptItem,
};
use tokio_util::sync::CancellationToken;

pub trait ModelEventSink: Send {
    fn send(&mut self, event: ModelStreamEvent) -> Result<(), FairyError>;
}

#[async_trait]
pub trait ModelGateway: Send + Sync {
    fn capabilities(&self) -> GatewayCapabilities;

    async fn execute(
        &self,
        request: CompiledPromptRequest,
        cancellation: CancellationToken,
        sink: &mut (dyn ModelEventSink + Send),
    ) -> Result<ModelCompletion, FairyError>;
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ContinuationState {
    pub previous_response_id: String,
    pub previous_request: CompiledPromptRequest,
    pub response_items: Vec<PromptItem>,
    pub response_complete: bool,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum ContinuationFullRequestReason {
    CapabilityUnsupported,
    NoPreviousState,
    PreviousResponseIncomplete,
    RequestShapeChanged,
    PrefixMismatch,
    InputNotExtended,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub enum ContinuationDecision {
    Incremental {
        previous_response_id: String,
        new_items: Vec<PromptItem>,
    },
    FullRequest {
        reason: ContinuationFullRequestReason,
    },
}

#[must_use]
pub fn decide_continuation(
    continuation_supported: bool,
    previous: Option<&ContinuationState>,
    current: &CompiledPromptRequest,
) -> ContinuationDecision {
    if !continuation_supported {
        return full(ContinuationFullRequestReason::CapabilityUnsupported);
    }
    let Some(previous) = previous else {
        return full(ContinuationFullRequestReason::NoPreviousState);
    };
    if !previous.response_complete || previous.previous_response_id.is_empty() {
        return full(ContinuationFullRequestReason::PreviousResponseIncomplete);
    }
    if previous.previous_request.shape != current.shape {
        return full(ContinuationFullRequestReason::RequestShapeChanged);
    }

    let mut expected_prefix = previous.previous_request.input.clone();
    expected_prefix.extend(previous.response_items.clone());
    if !current.input.starts_with(&expected_prefix) {
        return full(ContinuationFullRequestReason::PrefixMismatch);
    }
    if current.input.len() == expected_prefix.len() {
        return full(ContinuationFullRequestReason::InputNotExtended);
    }

    ContinuationDecision::Incremental {
        previous_response_id: previous.previous_response_id.clone(),
        new_items: current.input[expected_prefix.len()..].to_vec(),
    }
}

pub fn materialize_continuation_request(
    mut current: CompiledPromptRequest,
    decision: ContinuationDecision,
) -> Result<CompiledPromptRequest, FairyError> {
    match decision {
        ContinuationDecision::Incremental {
            previous_response_id,
            new_items,
        } => {
            if new_items.is_empty() {
                return Err(FairyError::new(
                    ErrorCode::ModelResponseInvalid,
                    "continuation suffix input 不能为空",
                    false,
                ));
            }
            current.input = new_items;
            current.continuation = Some(PromptContinuation::new(previous_response_id)?);
        }
        ContinuationDecision::FullRequest { .. } => {
            current.continuation = None;
        }
    }
    Ok(current)
}

const fn full(reason: ContinuationFullRequestReason) -> ContinuationDecision {
    ContinuationDecision::FullRequest { reason }
}

#[cfg(test)]
mod tests {
    use fairy_domain::{ModelRequestShape, PromptLane, ReasoningMode};

    use super::*;

    fn item(content: &str) -> PromptItem {
        PromptItem::UserMessage {
            content: content.to_owned(),
        }
    }

    fn request(model: &str, input: Vec<PromptItem>) -> CompiledPromptRequest {
        CompiledPromptRequest {
            shape: ModelRequestShape {
                lane: PromptLane::Respond,
                model: model.to_owned(),
                instructions: "stable".to_owned(),
                max_output_tokens: 160,
                reasoning: ReasoningMode::ProviderDefault,
                prompt_cache_key: Some("fairy:c:respond".to_owned()),
            },
            input,
            continuation: None,
        }
    }

    fn complete_state() -> ContinuationState {
        ContinuationState {
            previous_response_id: "resp_1".to_owned(),
            previous_request: request("model", vec![item("first")]),
            response_items: vec![PromptItem::AssistantMessage {
                content: "answer".to_owned(),
            }],
            response_complete: true,
        }
    }

    #[test]
    fn all_compatibility_conditions_allow_only_new_suffix() {
        let previous = complete_state();
        let current = request(
            "model",
            vec![
                item("first"),
                PromptItem::AssistantMessage {
                    content: "answer".to_owned(),
                },
                item("second"),
            ],
        );

        assert_eq!(
            decide_continuation(true, Some(&previous), &current),
            ContinuationDecision::Incremental {
                previous_response_id: "resp_1".to_owned(),
                new_items: vec![item("second")]
            }
        );
    }

    #[test]
    fn unsupported_capability_always_uses_complete_request() {
        let previous = complete_state();
        let current = request("model", vec![item("first"), item("second")]);

        assert_eq!(
            decide_continuation(false, Some(&previous), &current),
            full(ContinuationFullRequestReason::CapabilityUnsupported)
        );
        assert_eq!(current.input.len(), 2);
    }

    #[test]
    fn shape_change_prefix_mismatch_and_incomplete_response_are_distinct() {
        let previous = complete_state();
        let changed_shape = request("other-model", vec![item("first"), item("second")]);
        assert_eq!(
            decide_continuation(true, Some(&previous), &changed_shape),
            full(ContinuationFullRequestReason::RequestShapeChanged)
        );

        let wrong_prefix = request("model", vec![item("rewritten"), item("second")]);
        assert_eq!(
            decide_continuation(true, Some(&previous), &wrong_prefix),
            full(ContinuationFullRequestReason::PrefixMismatch)
        );

        let mut incomplete = previous.clone();
        incomplete.response_complete = false;
        assert_eq!(
            decide_continuation(true, Some(&incomplete), &wrong_prefix),
            full(ContinuationFullRequestReason::PreviousResponseIncomplete)
        );
    }

    #[test]
    fn missing_state_and_non_extended_input_use_full_request() {
        let current = request("model", vec![item("first")]);
        assert_eq!(
            decide_continuation(true, None, &current),
            full(ContinuationFullRequestReason::NoPreviousState)
        );

        let previous = complete_state();
        let same_known = request(
            "model",
            vec![
                item("first"),
                PromptItem::AssistantMessage {
                    content: "answer".to_owned(),
                },
            ],
        );
        assert_eq!(
            decide_continuation(true, Some(&previous), &same_known),
            full(ContinuationFullRequestReason::InputNotExtended)
        );
    }

    #[test]
    fn materialize_incremental_request_uses_suffix_and_validated_previous_response_id() {
        let current = request(
            "model",
            vec![
                item("first"),
                PromptItem::AssistantMessage {
                    content: "answer".to_owned(),
                },
                item("second"),
            ],
        );
        let materialized = materialize_continuation_request(
            current.clone(),
            ContinuationDecision::Incremental {
                previous_response_id: "resp_1".to_owned(),
                new_items: vec![item("second")],
            },
        )
        .expect("materialize incremental request");

        assert_eq!(materialized.shape, current.shape);
        assert_eq!(materialized.input, vec![item("second")]);
        assert_eq!(
            materialized
                .continuation
                .as_ref()
                .expect("continuation")
                .previous_response_id(),
            "resp_1"
        );
    }

    #[test]
    fn materialize_full_request_clears_stale_continuation() {
        let mut current = request("model", vec![item("first"), item("second")]);
        current.continuation =
            Some(PromptContinuation::new("resp_stale".to_owned()).expect("valid response id"));
        let materialized = materialize_continuation_request(
            current.clone(),
            full(ContinuationFullRequestReason::CapabilityUnsupported),
        )
        .expect("materialize full request");

        assert_eq!(materialized.shape, current.shape);
        assert_eq!(materialized.input, current.input);
        assert_eq!(materialized.continuation, None);
    }

    #[test]
    fn materialize_incremental_rejects_invalid_state() {
        let current = request("model", vec![item("first"), item("second")]);
        let empty_suffix = materialize_continuation_request(
            current.clone(),
            ContinuationDecision::Incremental {
                previous_response_id: "resp_1".to_owned(),
                new_items: Vec::new(),
            },
        )
        .expect_err("empty suffix must fail");
        assert_eq!(empty_suffix.code, ErrorCode::ModelResponseInvalid);

        let invalid_response_id = materialize_continuation_request(
            current,
            ContinuationDecision::Incremental {
                previous_response_id: " resp_1".to_owned(),
                new_items: vec![item("second")],
            },
        )
        .expect_err("invalid response id must fail");
        assert_eq!(invalid_response_id.code, ErrorCode::ModelResponseInvalid);
    }
}
