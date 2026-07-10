use fairy_domain::{
    CharacterId, CharacterSnapshot, ConversationId, ErrorCode, FairyError, PromptItem, PromptLane,
    Revision, UserProfileSnapshot, WindowRevision,
};

use crate::PromptCompiler;

#[derive(Clone, Debug)]
pub struct LaneHistory {
    conversation_id: ConversationId,
    lane: PromptLane,
    cache_key: String,
    window_revision: WindowRevision,
    items: Vec<PromptItem>,
    sealed_prefix: Vec<Vec<u8>>,
}

impl LaneHistory {
    fn new(conversation_id: ConversationId, lane: PromptLane) -> Self {
        Self {
            conversation_id,
            lane,
            cache_key: format!("fairy:{conversation_id}:{}", lane.as_str()),
            window_revision: WindowRevision::INITIAL,
            items: vec![PromptCompiler::canonical_harness_context()],
            sealed_prefix: Vec::new(),
        }
    }

    #[must_use]
    pub const fn conversation_id(&self) -> ConversationId {
        self.conversation_id
    }

    #[must_use]
    pub const fn lane(&self) -> PromptLane {
        self.lane
    }

    #[must_use]
    pub fn cache_key(&self) -> &str {
        &self.cache_key
    }

    #[must_use]
    pub const fn window_revision(&self) -> WindowRevision {
        self.window_revision
    }

    #[must_use]
    pub fn items(&self) -> &[PromptItem] {
        &self.items
    }

    pub fn append(&mut self, item: PromptItem) {
        self.items.push(item);
    }

    pub fn seal_current_prefix(&mut self) -> Result<(), FairyError> {
        self.sealed_prefix = canonical_item_bytes(&self.items)?;
        Ok(())
    }

    pub fn audit_current_prefix(&self) -> Result<(), FairyError> {
        self.audit_candidate(&self.items)
    }

    pub fn audit_candidate(&self, candidate: &[PromptItem]) -> Result<(), FairyError> {
        let candidate_bytes = canonical_item_bytes(candidate)?;
        for (index, expected) in self.sealed_prefix.iter().enumerate() {
            match candidate_bytes.get(index) {
                Some(actual) if actual == expected => {}
                _ => return Err(prefix_mismatch(index)),
            }
        }
        Ok(())
    }

    pub fn canonical_bytes(&self) -> Result<Vec<Vec<u8>>, FairyError> {
        canonical_item_bytes(&self.items)
    }
}

#[derive(Clone, Debug)]
pub struct ConversationHistory {
    interpret: LaneHistory,
    respond: LaneHistory,
    compact: LaneHistory,
    active_character: Option<(CharacterId, Revision)>,
    active_user_profile: Option<Revision>,
    pending_user_profile: Option<UserProfileSnapshot>,
}

impl ConversationHistory {
    #[must_use]
    pub fn new(conversation_id: ConversationId) -> Self {
        Self {
            interpret: LaneHistory::new(conversation_id, PromptLane::Interpret),
            respond: LaneHistory::new(conversation_id, PromptLane::Respond),
            compact: LaneHistory::new(conversation_id, PromptLane::Compact),
            active_character: None,
            active_user_profile: None,
            pending_user_profile: None,
        }
    }

    #[must_use]
    pub fn lane(&self, lane: PromptLane) -> &LaneHistory {
        match lane {
            PromptLane::Interpret => &self.interpret,
            PromptLane::Respond => &self.respond,
            PromptLane::Compact => &self.compact,
        }
    }

    #[must_use]
    pub fn lane_mut(&mut self, lane: PromptLane) -> &mut LaneHistory {
        match lane {
            PromptLane::Interpret => &mut self.interpret,
            PromptLane::Respond => &mut self.respond,
            PromptLane::Compact => &mut self.compact,
        }
    }

    pub fn activate_character(&mut self, snapshot: &CharacterSnapshot) -> bool {
        let identity = (snapshot.character_id(), snapshot.revision());
        if self.active_character == Some(identity) {
            return false;
        }
        self.append_all_lanes(PromptItem::CharacterActivated {
            snapshot: snapshot.clone(),
        });
        self.active_character = Some(identity);
        true
    }

    pub fn synchronize_user_profile(&mut self, snapshot: &UserProfileSnapshot) -> bool {
        if self.active_user_profile == Some(snapshot.revision()) {
            return false;
        }
        self.append_all_lanes(PromptItem::UserProfileUpdated {
            snapshot: Some(snapshot.clone()),
        });
        self.active_user_profile = Some(snapshot.revision());
        true
    }

    pub fn queue_user_profile(&mut self, snapshot: UserProfileSnapshot) {
        self.pending_user_profile = Some(snapshot);
    }

    pub fn flush_pending_context(&mut self) -> bool {
        let Some(snapshot) = self.pending_user_profile.take() else {
            return false;
        };
        self.synchronize_user_profile(&snapshot)
    }

    #[must_use]
    pub const fn active_character(&self) -> Option<(CharacterId, Revision)> {
        self.active_character
    }

    #[must_use]
    pub const fn active_user_profile(&self) -> Option<Revision> {
        self.active_user_profile
    }

    pub(crate) fn install_compacted_window(
        &mut self,
        items: Vec<PromptItem>,
    ) -> Result<WindowRevision, FairyError> {
        canonical_item_bytes(&items)?;
        let interpret_revision = next_window(self.interpret.window_revision)?;
        let respond_revision = next_window(self.respond.window_revision)?;
        let compact_revision = next_window(self.compact.window_revision)?;
        if interpret_revision != respond_revision || interpret_revision != compact_revision {
            return Err(FairyError::new(
                ErrorCode::CompactionFailed,
                "三个缓存 lane 的 history window revision 不一致",
                false,
            ));
        }

        replace_lane_window(&mut self.interpret, items.clone(), interpret_revision);
        replace_lane_window(&mut self.respond, items.clone(), respond_revision);
        replace_lane_window(&mut self.compact, items, compact_revision);
        Ok(interpret_revision)
    }

    fn append_all_lanes(&mut self, item: PromptItem) {
        self.interpret.append(item.clone());
        self.respond.append(item.clone());
        self.compact.append(item);
    }
}

fn next_window(current: WindowRevision) -> Result<WindowRevision, FairyError> {
    current.checked_next().ok_or_else(|| {
        FairyError::new(
            ErrorCode::CompactionFailed,
            "history window revision 已耗尽",
            false,
        )
    })
}

fn replace_lane_window(lane: &mut LaneHistory, items: Vec<PromptItem>, revision: WindowRevision) {
    lane.items = items;
    lane.window_revision = revision;
    lane.sealed_prefix.clear();
}

fn canonical_item_bytes(items: &[PromptItem]) -> Result<Vec<Vec<u8>>, FairyError> {
    items
        .iter()
        .map(|item| {
            serde_json::to_vec(item).map_err(|_| {
                FairyError::new(
                    ErrorCode::PromptHistoryInvalid,
                    "无法序列化会话历史 item",
                    false,
                )
            })
        })
        .collect()
}

fn prefix_mismatch(index: usize) -> FairyError {
    FairyError::new(
        ErrorCode::PromptHistoryInvalid,
        format!("会话历史前缀在 item {index} 处发生变化"),
        false,
    )
}

#[cfg(test)]
mod tests {
    use fairy_domain::{
        CharacterBriefInput, CharacterCompiler, UserProfileCompiler, UserProfileInput,
    };

    use super::*;

    fn character(description: &str, revision: Revision) -> CharacterSnapshot {
        CharacterCompiler
            .compile(
                CharacterId::new(),
                revision,
                CharacterBriefInput {
                    name: "亚托莉".to_owned(),
                    description: description.to_owned(),
                },
            )
            .expect("compile character")
    }

    fn profile(name: Option<&str>, revision: Revision) -> UserProfileSnapshot {
        UserProfileCompiler
            .compile(
                revision,
                UserProfileInput {
                    preferred_name: name.map(str::to_owned),
                },
            )
            .expect("compile user profile")
    }

    #[test]
    fn cache_key_is_stable_per_conversation_lane_and_contains_no_turn_data() {
        let conversation_id = ConversationId::new();
        let first = ConversationHistory::new(conversation_id);
        let second = ConversationHistory::new(conversation_id);

        assert_eq!(
            first.lane(PromptLane::Respond).cache_key(),
            second.lane(PromptLane::Respond).cache_key()
        );
        assert_ne!(
            first.lane(PromptLane::Interpret).cache_key(),
            first.lane(PromptLane::Respond).cache_key()
        );
        assert_ne!(
            first.lane(PromptLane::Respond).cache_key(),
            ConversationHistory::new(ConversationId::new())
                .lane(PromptLane::Respond)
                .cache_key()
        );
        assert_eq!(
            first.lane(PromptLane::Respond).cache_key(),
            format!("fairy:{conversation_id}:respond")
        );
    }

    #[test]
    fn ordinary_next_turn_strictly_extends_sealed_known_prefix() {
        let mut history = ConversationHistory::new(ConversationId::new());
        let role = character("会认真倾听。", Revision::INITIAL);
        history.activate_character(&role);
        let respond = history.lane_mut(PromptLane::Respond);
        respond.append(PromptItem::UserMessage {
            content: "第一轮".to_owned(),
        });
        respond.append(PromptItem::AssistantMessage {
            content: "第一轮回复".to_owned(),
        });
        respond.seal_current_prefix().expect("seal first turn");
        let previous_len = respond.items().len();

        respond.append(PromptItem::UserMessage {
            content: "第二轮".to_owned(),
        });

        respond
            .audit_current_prefix()
            .expect("prefix remains exact");
        assert_eq!(respond.items().len(), previous_len + 1);
    }

    #[test]
    fn rewritten_or_truncated_old_item_reports_first_mismatch() {
        let mut history = ConversationHistory::new(ConversationId::new());
        let respond = history.lane_mut(PromptLane::Respond);
        respond.append(PromptItem::UserMessage {
            content: "不可修改的旧消息".to_owned(),
        });
        respond.seal_current_prefix().expect("seal history");
        let mut rewritten = respond.items().to_vec();
        rewritten[1] = PromptItem::UserMessage {
            content: "被重写".to_owned(),
        };

        let error = respond
            .audit_candidate(&rewritten)
            .expect_err("rewritten prefix must fail");
        assert_eq!(error.code, ErrorCode::PromptHistoryInvalid);
        assert!(error.message.contains("item 1"));
        assert!(respond.audit_candidate(&rewritten[..1]).is_err());
    }

    #[test]
    fn unchanged_context_is_not_duplicated_and_changed_context_only_appends() {
        let mut history = ConversationHistory::new(ConversationId::new());
        let first_role = character("先倾听。", Revision::INITIAL);
        assert!(history.activate_character(&first_role));
        assert!(!history.activate_character(&first_role));
        let before = history
            .lane(PromptLane::Respond)
            .canonical_bytes()
            .expect("canonical old prefix");

        let updated_role = CharacterCompiler
            .compile(
                first_role.character_id(),
                Revision::new(2).expect("revision two"),
                CharacterBriefInput {
                    name: "亚托莉".to_owned(),
                    description: "先倾听，再轻轻询问。".to_owned(),
                },
            )
            .expect("compile updated role");
        assert!(history.activate_character(&updated_role));
        let after = history
            .lane(PromptLane::Respond)
            .canonical_bytes()
            .expect("canonical extended history");

        assert_eq!(&after[..before.len()], before.as_slice());
        assert_eq!(after.len(), before.len() + 1);
        assert_eq!(
            history.active_character(),
            Some((updated_role.character_id(), updated_role.revision()))
        );
    }

    #[test]
    fn active_turn_profile_update_stays_pending_until_next_turn_boundary() {
        let mut history = ConversationHistory::new(ConversationId::new());
        let first = profile(Some("Rinai"), Revision::INITIAL);
        let second = profile(Some("凛"), Revision::new(2).expect("revision two"));
        assert!(history.synchronize_user_profile(&first));
        assert!(!history.synchronize_user_profile(&first));
        let length_before_queue = history.lane(PromptLane::Respond).items().len();

        history.queue_user_profile(second.clone());
        assert_eq!(
            history.lane(PromptLane::Respond).items().len(),
            length_before_queue
        );
        assert_eq!(history.active_user_profile(), Some(first.revision()));

        assert!(history.flush_pending_context());
        assert!(!history.flush_pending_context());
        assert_eq!(history.active_user_profile(), Some(second.revision()));
        assert_eq!(
            history.lane(PromptLane::Respond).items().len(),
            length_before_queue + 1
        );
    }

    #[test]
    fn all_lanes_receive_same_context_revision_but_keep_independent_histories() {
        let mut history = ConversationHistory::new(ConversationId::new());
        let role = character("关注用户明确表达。", Revision::INITIAL);
        history.activate_character(&role);

        for lane in [
            PromptLane::Interpret,
            PromptLane::Respond,
            PromptLane::Compact,
        ] {
            assert!(matches!(
                history.lane(lane).items().last(),
                Some(PromptItem::CharacterActivated { snapshot })
                    if snapshot.revision() == role.revision()
            ));
        }

        history
            .lane_mut(PromptLane::Interpret)
            .append(PromptItem::UserMessage {
                content: "只进入 interpret".to_owned(),
            });
        assert_eq!(history.lane(PromptLane::Interpret).items().len(), 3);
        assert_eq!(history.lane(PromptLane::Respond).items().len(), 2);
    }
}
