use fairy_domain::{
    CharacterSnapshot, DEFAULT_MODEL_CONTEXT_WINDOW_TOKENS, ErrorCode, FairyError, ModelUsage,
    PromptItem, UserProfileSnapshot, WindowRevision,
};
use serde::Serialize;

use crate::ConversationHistory;

const MAX_COMPACTION_SUMMARY_CHARS: usize = 12_000;
const AUTO_COMPACTION_THRESHOLD_BASIS_POINTS: u64 = 8_000;
const BASIS_POINTS_DENOMINATOR: u64 = 10_000;
const RESPOND_OUTPUT_RESERVE_TOKENS: u64 = 640;

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct CompactionPolicy {
    pub auto_input_token_threshold: Option<u64>,
}

impl Default for CompactionPolicy {
    fn default() -> Self {
        Self::from_context_window_tokens(DEFAULT_MODEL_CONTEXT_WINDOW_TOKENS)
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum CompactionTrigger {
    Manual,
    AfterCompletedTurn,
}

impl CompactionPolicy {
    #[must_use]
    pub const fn from_context_window_tokens(context_window_tokens: u64) -> Self {
        let threshold = context_window_tokens
            .saturating_mul(AUTO_COMPACTION_THRESHOLD_BASIS_POINTS)
            / BASIS_POINTS_DENOMINATOR;
        Self {
            auto_input_token_threshold: Some(threshold.saturating_sub(RESPOND_OUTPUT_RESERVE_TOKENS)),
        }
    }

    #[must_use]
    pub fn should_compact(self, trigger: CompactionTrigger, usage: Option<&ModelUsage>) -> bool {
        match trigger {
            CompactionTrigger::Manual => true,
            CompactionTrigger::AfterCompletedTurn => {
                let Some(threshold) = self.auto_input_token_threshold else {
                    return false;
                };
                usage
                    .and_then(|usage| usage.input_tokens)
                    .is_some_and(|tokens| tokens >= threshold)
            }
        }
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CompactionCandidate {
    pub summary: String,
    pub replacement_items: Vec<PromptItem>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CompactionResult {
    pub window_revision: WindowRevision,
    pub retained_dialogue_items: usize,
}

pub fn install_compaction(
    history: &mut ConversationHistory,
    candidate: CompactionCandidate,
    current_character: &CharacterSnapshot,
    current_user_profile: Option<&UserProfileSnapshot>,
) -> Result<CompactionResult, FairyError> {
    validate_current_context(history, current_character, current_user_profile)?;
    let summary = normalize_summary(candidate.summary)?;

    let mut replacement = vec![PromptItem::CharacterActivated {
        snapshot: current_character.clone(),
    }];
    if let Some(profile) = current_user_profile {
        replacement.push(PromptItem::UserProfileUpdated {
            snapshot: Some(profile.clone()),
        });
    }

    let retained_dialogue_items = candidate
        .replacement_items
        .into_iter()
        .filter(|item| {
            matches!(
                item,
                PromptItem::UserMessage { .. }
                    | PromptItem::AssistantMessage { .. }
                    | PromptItem::CompactionSummary { .. }
            )
        })
        .inspect(|item| replacement.push(item.clone()))
        .count();
    replacement.push(PromptItem::CompactionSummary { summary });

    let window_revision = history.install_compacted_window(replacement)?;
    Ok(CompactionResult {
        window_revision,
        retained_dialogue_items,
    })
}

fn validate_current_context(
    history: &ConversationHistory,
    current_character: &CharacterSnapshot,
    current_user_profile: Option<&UserProfileSnapshot>,
) -> Result<(), FairyError> {
    current_character.verify_integrity()?;
    if history.active_character()
        != Some((
            current_character.character_id(),
            current_character.revision(),
        ))
    {
        return Err(compaction_failed(
            "compaction 角色快照与当前会话 revision 不一致",
        ));
    }

    match current_user_profile {
        Some(profile) => {
            profile.verify_integrity()?;
            if history.active_user_profile() != Some(profile.revision()) {
                return Err(compaction_failed(
                    "compaction 用户资料与当前会话 revision 不一致",
                ));
            }
        }
        None if history.active_user_profile().is_some() => {
            return Err(compaction_failed("compaction 缺少当前用户资料 revision"));
        }
        None => {}
    }
    Ok(())
}

fn normalize_summary(summary: String) -> Result<String, FairyError> {
    let value = summary.trim();
    let length = value.chars().count();
    if length == 0 || length > MAX_COMPACTION_SUMMARY_CHARS {
        return Err(compaction_failed(
            "compaction summary 必须是 1–12000 个字符",
        ));
    }
    Ok(value.to_owned())
}

fn compaction_failed(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::CompactionFailed, message, false)
}

#[cfg(test)]
mod tests {
    use fairy_domain::{
        CachedTokenObservation, CharacterBriefInput, CharacterCompiler, CharacterId,
        ConversationId, PromptLane, Revision, UserProfileCompiler, UserProfileInput,
    };

    use super::*;

    fn character(
        character_id: CharacterId,
        revision: Revision,
        description: &str,
    ) -> CharacterSnapshot {
        CharacterCompiler
            .compile(
                character_id,
                revision,
                CharacterBriefInput {
                    name: "亚托莉".to_owned(),
                    description: description.to_owned(),
                    dialogue_style: None,
                },
            )
            .expect("compile character")
    }

    fn profile(revision: Revision, name: Option<&str>) -> UserProfileSnapshot {
        UserProfileCompiler
            .compile(
                revision,
                UserProfileInput {
                    preferred_name: name.map(str::to_owned),
                },
            )
            .expect("compile profile")
    }

    #[test]
    fn successful_compaction_keeps_keys_and_reinjects_only_current_context() {
        let mut history = ConversationHistory::new(ConversationId::new());
        let character_id = CharacterId::new();
        let old_character = character(character_id, Revision::INITIAL, "旧角色描述");
        let current_character = character(
            character_id,
            Revision::new(2).expect("revision two"),
            "当前角色描述",
        );
        let old_profile = profile(Revision::INITIAL, Some("旧称呼"));
        let current_profile = profile(Revision::new(2).expect("revision two"), Some("Rinai"));
        history.activate_character(&old_character);
        history.synchronize_user_profile(&old_profile);
        history.activate_character(&current_character);
        history.synchronize_user_profile(&current_profile);
        let keys_before = [
            history.lane(PromptLane::Respond).cache_key().to_owned(),
            history.lane(PromptLane::Compact).cache_key().to_owned(),
        ];

        let result = install_compaction(
            &mut history,
            CompactionCandidate {
                summary: "  用户正在讨论自己的近况，角色先倾听。  ".to_owned(),
                replacement_items: vec![
                    PromptItem::CharacterActivated {
                        snapshot: old_character,
                    },
                    PromptItem::UserProfileUpdated {
                        snapshot: Some(old_profile),
                    },
                    PromptItem::UserMessage {
                        content: "保留的一条真实对话".to_owned(),
                    },
                ],
            },
            &current_character,
            Some(&current_profile),
        )
        .expect("install valid compaction");

        assert_eq!(result.window_revision.get(), 2);
        assert_eq!(result.retained_dialogue_items, 1);
        for (index, lane) in [PromptLane::Respond, PromptLane::Compact]
            .into_iter()
            .enumerate()
        {
            let lane_history = history.lane(lane);
            assert_eq!(lane_history.cache_key(), keys_before[index]);
            assert_eq!(lane_history.window_revision(), result.window_revision);
            assert_eq!(
                lane_history
                    .items()
                    .iter()
                    .filter(|item| matches!(item, PromptItem::CharacterActivated { .. }))
                    .count(),
                1
            );
            assert!(lane_history.items().iter().any(|item| matches!(
                item,
                PromptItem::CharacterActivated { snapshot }
                    if snapshot.revision() == current_character.revision()
            )));
            assert!(lane_history.items().iter().any(|item| matches!(
                item,
                PromptItem::UserProfileUpdated { snapshot: Some(snapshot) }
                    if snapshot.revision() == current_profile.revision()
            )));
        }
    }

    #[test]
    fn failed_candidate_leaves_all_windows_and_bytes_unchanged() {
        let mut history = ConversationHistory::new(ConversationId::new());
        let current_character = character(CharacterId::new(), Revision::INITIAL, "当前角色");
        history.activate_character(&current_character);
        let before = [
            history
                .lane(PromptLane::Respond)
                .canonical_bytes()
                .expect("respond bytes"),
            history
                .lane(PromptLane::Compact)
                .canonical_bytes()
                .expect("compact bytes"),
        ];

        let error = install_compaction(
            &mut history,
            CompactionCandidate {
                summary: "   ".to_owned(),
                replacement_items: vec![],
            },
            &current_character,
            None,
        )
        .expect_err("empty summary must fail");

        assert_eq!(error.code, ErrorCode::CompactionFailed);
        for (index, lane) in [PromptLane::Respond, PromptLane::Compact]
            .into_iter()
            .enumerate()
        {
            assert_eq!(history.lane(lane).window_revision().get(), 1);
            assert_eq!(
                history
                    .lane(lane)
                    .canonical_bytes()
                    .expect("unchanged bytes"),
                before[index]
            );
        }
    }

    #[test]
    fn trigger_policy_never_guesses_unknown_usage() {
        let policy = CompactionPolicy {
            auto_input_token_threshold: Some(1_000),
        };
        let below = ModelUsage {
            input_tokens: Some(999),
            output_tokens: Some(10),
            cached_input_tokens: CachedTokenObservation::Missing,
            cache_write_tokens: CachedTokenObservation::Unsupported,
        };
        let threshold = ModelUsage {
            input_tokens: Some(1_000),
            ..below.clone()
        };
        let unknown = ModelUsage {
            input_tokens: None,
            ..below.clone()
        };

        assert!(!policy.should_compact(CompactionTrigger::AfterCompletedTurn, Some(&below)));
        assert!(policy.should_compact(CompactionTrigger::AfterCompletedTurn, Some(&threshold)));
        assert!(!policy.should_compact(CompactionTrigger::AfterCompletedTurn, Some(&unknown)));
        assert!(!policy.should_compact(CompactionTrigger::AfterCompletedTurn, None));
        assert!(policy.should_compact(CompactionTrigger::Manual, None));
    }

    #[test]
    fn default_policy_derives_threshold_from_context_window() {
        let policy = CompactionPolicy::from_context_window_tokens(128_000);

        assert_eq!(policy.auto_input_token_threshold, Some(101_760));
        assert_eq!(policy, CompactionPolicy::default());
    }

    #[test]
    fn mismatched_current_snapshot_fails_before_install() {
        let mut history = ConversationHistory::new(ConversationId::new());
        let active = character(CharacterId::new(), Revision::INITIAL, "当前角色");
        let wrong = character(CharacterId::new(), Revision::INITIAL, "另一个角色");
        history.activate_character(&active);

        let error = install_compaction(
            &mut history,
            CompactionCandidate {
                summary: "有效摘要".to_owned(),
                replacement_items: vec![],
            },
            &wrong,
            None,
        )
        .expect_err("snapshot mismatch must fail");

        assert_eq!(error.code, ErrorCode::CompactionFailed);
        assert_eq!(
            history.active_character(),
            Some((active.character_id(), active.revision()))
        );
    }
}
