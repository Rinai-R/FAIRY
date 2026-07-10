use std::collections::HashSet;

use fairy_domain::{
    CharacterSnapshot, DIALOGUE_POLICY_VERSION, ErrorCode, FairyError, ModelCompletion,
    ModelStreamEvent, PromptItem, PromptLane, ResponseAction, TurnPlan, UserProfileSnapshot,
};
use tokio_util::sync::CancellationToken;

use crate::{ModelEventSink, ModelGateway, PromptCompiler};

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ValidatedTurnPlan(TurnPlan);

impl ValidatedTurnPlan {
    #[must_use]
    pub fn as_plan(&self) -> &TurnPlan {
        &self.0
    }

    #[must_use]
    pub fn into_inner(self) -> TurnPlan {
        self.0
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct InterpretationResult {
    pub plan: ValidatedTurnPlan,
    pub completion: ModelCompletion,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct InterpretTurnRequest {
    pub model: String,
    pub input: Vec<PromptItem>,
    pub prompt_cache_key: Option<String>,
    pub user_input: String,
    pub character: CharacterSnapshot,
    pub user_profile: Option<UserProfileSnapshot>,
}

pub async fn interpret_turn(
    gateway: &(dyn ModelGateway + Send + Sync),
    request: InterpretTurnRequest,
    cancellation: CancellationToken,
) -> Result<InterpretationResult, FairyError> {
    let prompt = PromptCompiler.compile(
        PromptLane::Interpret,
        request.model,
        request.input,
        request.prompt_cache_key,
    );
    let mut sink = StructuredOutputSink::default();
    let completion = gateway.execute(prompt, cancellation, &mut sink).await?;
    if !sink.output.is_empty() && sink.output != completion.output_text {
        return Err(invalid_plan("Interpreter 流式文本与完成文本不一致"));
    }
    let plan = parse_turn_plan(&completion.output_text)?;
    let plan = validate_turn_plan(
        &request.user_input,
        &request.character,
        request.user_profile.as_ref(),
        plan,
    )?;
    Ok(InterpretationResult { plan, completion })
}

pub fn validate_turn_plan(
    user_input: &str,
    character: &CharacterSnapshot,
    user_profile: Option<&UserProfileSnapshot>,
    plan: TurnPlan,
) -> Result<ValidatedTurnPlan, FairyError> {
    character.verify_integrity()?;
    if let Some(profile) = user_profile {
        profile.verify_integrity()?;
    }
    validate_hypothesis(user_input, &plan)?;
    validate_character_perspective(&plan)?;
    validate_policy(user_input, user_profile, &plan)?;
    Ok(ValidatedTurnPlan(plan))
}

fn parse_turn_plan(output: &str) -> Result<TurnPlan, FairyError> {
    serde_json::from_str(output).map_err(|_| invalid_plan("Interpreter 返回的 TurnPlan 无法解析"))
}

fn validate_hypothesis(user_input: &str, plan: &TurnPlan) -> Result<(), FairyError> {
    let hypothesis = &plan.interaction_hypothesis;
    if hypothesis.explicit_request.trim().is_empty()
        || hypothesis.explicit_request.chars().count() > 500
        || hypothesis.confidence > 100
        || hypothesis.evidence.is_empty()
        || hypothesis.evidence.len() > 3
    {
        return Err(invalid_plan("InteractionHypothesis 不符合紧凑结构约束"));
    }
    for evidence in &hypothesis.evidence {
        if evidence.quote.is_empty()
            || evidence.quote.chars().count() > 300
            || !user_input.contains(&evidence.quote)
        {
            return Err(invalid_plan(
                "InteractionHypothesis evidence 未引用本轮输入",
            ));
        }
    }
    if hypothesis
        .ambiguity
        .as_ref()
        .is_some_and(|value| value.trim().is_empty() || value.chars().count() > 300)
    {
        return Err(invalid_plan("InteractionHypothesis ambiguity 不合法"));
    }
    if hypothesis.confidence < 60 && hypothesis.ambiguity.is_none() {
        return Err(invalid_plan("低置信度假设必须保留歧义"));
    }
    Ok(())
}

fn validate_character_perspective(plan: &TurnPlan) -> Result<(), FairyError> {
    let perspective = &plan.character_perspective;
    if perspective.attention_focus.is_empty()
        || perspective.attention_focus.len() > 3
        || perspective.character_intensity > 100
        || perspective.candidate_actions.is_empty()
        || perspective.candidate_actions.len() > 3
        || perspective
            .attention_focus
            .iter()
            .any(|value| value.trim().is_empty() || value.chars().count() > 200)
    {
        return Err(invalid_plan("CharacterPerspective 不符合受约束结构"));
    }
    let unique_actions: HashSet<_> = perspective.candidate_actions.iter().collect();
    if unique_actions.len() != perspective.candidate_actions.len() {
        return Err(invalid_plan("CharacterPerspective 包含重复候选动作"));
    }
    Ok(())
}

fn validate_policy(
    user_input: &str,
    user_profile: Option<&UserProfileSnapshot>,
    plan: &TurnPlan,
) -> Result<(), FairyError> {
    let policy = &plan.turn_policy;
    if policy.policy_version != DIALOGUE_POLICY_VERSION {
        return Err(invalid_plan("TurnPolicy 使用了未知优先级版本"));
    }
    if !plan
        .character_perspective
        .candidate_actions
        .contains(&policy.primary_action)
    {
        return Err(invalid_plan("TurnPolicy 主动作不在角色候选动作中"));
    }
    if policy.use_preferred_name
        && user_profile
            .and_then(UserProfileSnapshot::preferred_name)
            .is_none()
    {
        return Err(invalid_plan("TurnPolicy 请求使用不存在的偏好称呼"));
    }
    if plan.interaction_hypothesis.confidence < 60
        && matches!(
            policy.ambiguity_handling,
            fairy_domain::AmbiguityHandling::ProceedWithExplicitRequest
        )
    {
        return Err(invalid_plan("低置信度策略不能把猜测当作明确请求"));
    }
    if explicitly_refuses_advice(user_input)
        && matches!(
            policy.primary_action,
            ResponseAction::OfferPracticalHelp | ResponseAction::GiveDirectAnswer
        )
    {
        return Err(invalid_plan("TurnPolicy 违反了用户明确的非建议请求"));
    }
    Ok(())
}

fn explicitly_refuses_advice(user_input: &str) -> bool {
    ["别给建议", "不要建议", "只陪我聊", "只想被陪伴"]
        .iter()
        .any(|marker| user_input.contains(marker))
}

#[derive(Default)]
struct StructuredOutputSink {
    output: String,
}

impl ModelEventSink for StructuredOutputSink {
    fn send(&mut self, event: ModelStreamEvent) -> Result<(), FairyError> {
        match event {
            ModelStreamEvent::StructuredTextDelta { delta } => {
                self.output.push_str(&delta);
                Ok(())
            }
            ModelStreamEvent::TextDelta { .. } => {
                Err(invalid_plan("Interpreter 收到了非结构化文本增量"))
            }
        }
    }
}

fn invalid_plan(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::ModelResponseInvalid, message, false)
}

#[cfg(test)]
mod tests {
    use async_trait::async_trait;
    use fairy_domain::{
        AmbiguityHandling, CachedTokenObservation, CharacterBriefInput, CharacterCompiler,
        CharacterId, CharacterPerspective, CompiledPromptRequest, ConversationGoal,
        EvidenceReference, FactCommitment, GatewayCapabilities, InteractionHypothesis, ModelUsage,
        RelationshipIntent, ResponseLength, Revision, TurnPolicy, UserProfileCompiler,
        UserProfileInput,
    };

    use super::*;

    struct FakeGateway {
        output: String,
    }

    #[async_trait]
    impl ModelGateway for FakeGateway {
        fn capabilities(&self) -> GatewayCapabilities {
            GatewayCapabilities::responses_http(true, true)
        }

        async fn execute(
            &self,
            _request: CompiledPromptRequest,
            _cancellation: CancellationToken,
            sink: &mut (dyn ModelEventSink + Send),
        ) -> Result<ModelCompletion, FairyError> {
            sink.send(ModelStreamEvent::StructuredTextDelta {
                delta: self.output.clone(),
            })?;
            Ok(ModelCompletion {
                response_id: Some("fake-response".to_owned()),
                output_text: self.output.clone(),
                response_items: vec![PromptItem::AssistantMessage {
                    content: self.output.clone(),
                }],
                usage: ModelUsage {
                    input_tokens: Some(10),
                    output_tokens: Some(10),
                    cached_input_tokens: CachedTokenObservation::Observed(0),
                    cache_write_tokens: CachedTokenObservation::Missing,
                },
            })
        }
    }

    fn character(description: &str) -> CharacterSnapshot {
        CharacterCompiler
            .compile(
                CharacterId::new(),
                Revision::INITIAL,
                CharacterBriefInput {
                    name: "亚托莉".to_owned(),
                    description: description.to_owned(),
                },
            )
            .expect("compile character")
    }

    fn profile() -> UserProfileSnapshot {
        UserProfileCompiler
            .compile(
                Revision::INITIAL,
                UserProfileInput {
                    preferred_name: Some("Rinai".to_owned()),
                },
            )
            .expect("compile profile")
    }

    fn plan_for(
        user_input: &str,
        goal: ConversationGoal,
        primary_action: ResponseAction,
        confidence: u8,
    ) -> TurnPlan {
        TurnPlan {
            interaction_hypothesis: InteractionHypothesis {
                explicit_request: "回应用户本轮表达".to_owned(),
                goal,
                evidence: vec![EvidenceReference {
                    quote: user_input.to_owned(),
                }],
                confidence,
                ambiguity: (confidence < 60).then(|| "输入存在多种合理解释".to_owned()),
            },
            character_perspective: CharacterPerspective {
                attention_focus: vec!["用户明确表达的内容".to_owned()],
                relationship_intent: RelationshipIntent::Listen,
                candidate_actions: vec![primary_action],
                character_intensity: 55,
            },
            turn_policy: TurnPolicy {
                policy_version: DIALOGUE_POLICY_VERSION.to_owned(),
                primary_action,
                secondary_action: None,
                use_preferred_name: false,
                response_length: ResponseLength::Brief,
                fact_commitment: FactCommitment::EvidenceBound,
                ambiguity_handling: if confidence < 60 {
                    AmbiguityHandling::ClarifyNaturally
                } else {
                    AmbiguityHandling::ProceedWithExplicitRequest
                },
            },
        }
    }

    async fn run_plan(
        user_input: &str,
        role: &CharacterSnapshot,
        plan: TurnPlan,
    ) -> InterpretationResult {
        interpret_turn(
            &FakeGateway {
                output: serde_json::to_string(&plan).expect("serialize fake plan"),
            },
            InterpretTurnRequest {
                model: "test-model".to_owned(),
                input: vec![PromptItem::UserMessage {
                    content: user_input.to_owned(),
                }],
                prompt_cache_key: Some("fairy:test:interpret".to_owned()),
                user_input: user_input.to_owned(),
                character: role.clone(),
                user_profile: Some(profile()),
            },
            CancellationToken::new(),
        )
        .await
        .expect("validate fake interpretation")
    }

    #[tokio::test]
    async fn low_mood_and_technical_question_keep_evidence_bound_distinct_goals() {
        let role = character("会先倾听，再决定是否提供帮助。");
        let low = run_plan(
            "我好失败",
            &role,
            plan_for(
                "我好失败",
                ConversationGoal::NeedToBeHeard,
                ResponseAction::AcknowledgeFeeling,
                82,
            ),
        )
        .await;
        assert_eq!(
            low.plan.as_plan().interaction_hypothesis.goal,
            ConversationGoal::NeedToBeHeard
        );
        assert_eq!(
            low.plan.as_plan().interaction_hypothesis.evidence[0].quote,
            "我好失败"
        );

        let technical = run_plan(
            "Rust 的 Pin 是什么？",
            &role,
            plan_for(
                "Rust 的 Pin 是什么？",
                ConversationGoal::NeedPracticalHelp,
                ResponseAction::GiveDirectAnswer,
                96,
            ),
        )
        .await;
        assert_eq!(
            technical.plan.as_plan().interaction_hypothesis.goal,
            ConversationGoal::NeedPracticalHelp
        );
    }

    #[tokio::test]
    async fn ambiguity_requires_low_commitment_or_clarification() {
        let role = character("不确定时会自然问一句。");
        let accepted = run_plan(
            "行吧",
            &role,
            plan_for(
                "行吧",
                ConversationGoal::NeedClarification,
                ResponseAction::AskGentleQuestion,
                35,
            ),
        )
        .await;
        assert_eq!(
            accepted.plan.as_plan().interaction_hypothesis.confidence,
            35
        );

        let mut invalid = plan_for(
            "行吧",
            ConversationGoal::NeedClarification,
            ResponseAction::AskGentleQuestion,
            35,
        );
        invalid.turn_policy.ambiguity_handling = AmbiguityHandling::ProceedWithExplicitRequest;
        let error = validate_turn_plan("行吧", &role, Some(&profile()), invalid)
            .expect_err("low confidence cannot overcommit");
        assert_eq!(error.code, ErrorCode::ModelResponseInvalid);
    }

    #[test]
    fn explicit_no_advice_request_outranks_character_action() {
        let role = character("非常热衷于给出解决方案。");
        let invalid = plan_for(
            "别给建议，只陪我聊会儿",
            ConversationGoal::NeedToBeHeard,
            ResponseAction::OfferPracticalHelp,
            95,
        );
        let error = validate_turn_plan("别给建议，只陪我聊会儿", &role, Some(&profile()), invalid)
            .expect_err("explicit request must outrank role drive");
        assert_eq!(error.code, ErrorCode::ModelResponseInvalid);

        let valid = plan_for(
            "别给建议，只陪我聊会儿",
            ConversationGoal::NeedToBeHeard,
            ResponseAction::StayPresent,
            95,
        );
        validate_turn_plan("别给建议，只陪我聊会儿", &role, Some(&profile()), valid)
            .expect("stay-present policy is allowed");
    }

    #[tokio::test]
    async fn two_roles_can_choose_different_attention_without_changing_user_evidence() {
        let listener = character("优先留意用户是否想被听见。");
        let helper = character("优先留意用户是否明确需要解决方案。");
        let mut listener_plan = plan_for(
            "今天有点累",
            ConversationGoal::NeedToBeHeard,
            ResponseAction::ReflectContent,
            75,
        );
        listener_plan.character_perspective.attention_focus = vec!["用户是否希望被倾听".to_owned()];
        let mut helper_plan = listener_plan.clone();
        helper_plan.character_perspective.attention_focus = vec!["用户是否需要具体帮助".to_owned()];

        let listener_result = run_plan("今天有点累", &listener, listener_plan).await;
        let helper_result = run_plan("今天有点累", &helper, helper_plan).await;
        assert_ne!(
            listener_result
                .plan
                .as_plan()
                .character_perspective
                .attention_focus,
            helper_result
                .plan
                .as_plan()
                .character_perspective
                .attention_focus
        );
        assert_eq!(
            listener_result
                .plan
                .as_plan()
                .interaction_hypothesis
                .evidence,
            helper_result.plan.as_plan().interaction_hypothesis.evidence
        );
    }

    #[test]
    fn unknown_reasoning_field_and_non_input_evidence_are_rejected() {
        let role = character("保持事实边界。");
        let plan = plan_for(
            "原始输入",
            ConversationGoal::CasualConversation,
            ResponseAction::ShareLightReaction,
            80,
        );
        let mut value = serde_json::to_value(plan).expect("serialize plan");
        value["raw_reasoning"] = serde_json::json!("不应暴露的长推理");
        let error =
            parse_turn_plan(&serde_json::to_string(&value).expect("serialize invalid plan"))
                .expect_err("unknown reasoning field must fail");
        assert_eq!(error.code, ErrorCode::ModelResponseInvalid);

        let invalid_evidence = plan_for(
            "模型编造的证据",
            ConversationGoal::CasualConversation,
            ResponseAction::ShareLightReaction,
            80,
        );
        let error = validate_turn_plan("真实输入", &role, Some(&profile()), invalid_evidence)
            .expect_err("evidence must quote actual input");
        assert_eq!(error.code, ErrorCode::ModelResponseInvalid);
    }
}
