use super::*;

impl IntelligenceStore {
    pub fn open_or_create_character_conversation(
        &self,
        character_id: CharacterId,
    ) -> Result<ConversationBootstrap, FairyError> {
        let now = now_unix_ms()?;
        let mut connection = self.lock()?;
        let transaction = connection
            .transaction()
            .map_err(|_| storage_error("无法开始角色会话事务"))?;
        let conversation_id = transaction
            .query_row(
                "SELECT id FROM conversations
                 WHERE character_id = ?1
                 ORDER BY updated_at_ms DESC, id ASC
                 LIMIT 1",
                [character_id.to_string()],
                |row| row.get::<_, String>(0),
            )
            .optional()
            .map_err(|_| storage_error("无法读取角色最近会话"))?
            .map(|value| {
                ConversationId::from_str(&value).map_err(|_| storage_error("角色会话 id 已损坏"))
            })
            .transpose()?
            .unwrap_or_else(ConversationId::new);
        let exists: bool = transaction
            .query_row(
                "SELECT EXISTS(SELECT 1 FROM conversations WHERE id = ?1)",
                [conversation_id.to_string()],
                |row| row.get(0),
            )
            .map_err(|_| storage_error("无法确认角色会话是否存在"))?;
        if !exists {
            transaction
                .execute(
                    "INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms)
                     VALUES (?1, ?2, ?3, ?3)",
                    params![conversation_id.to_string(), character_id.to_string(), now],
                )
                .map_err(|_| storage_error("无法创建角色会话"))?;
            transaction
                .execute(
                    "INSERT INTO prompt_windows(
                        conversation_id, revision, summary, cutoff_message_sequence, updated_at_ms
                     ) VALUES (?1, 1, NULL, 0, ?2)",
                    params![conversation_id.to_string(), now],
                )
                .map_err(|_| storage_error("无法创建角色会话窗口"))?;
        }
        transaction
            .commit()
            .map_err(|_| storage_error("无法提交角色会话事务"))?;
        load_conversation_bootstrap(&connection, conversation_id)
    }

    pub fn begin_persisted_turn(
        &self,
        conversation_id: ConversationId,
        turn_id: TurnId,
        user_message: String,
    ) -> Result<(PersistedTurnRecord, ConversationMessageRecord), FairyError> {
        validate_conversation_content(&user_message)?;
        let now = now_unix_ms()?;
        let message_id = MessageId::new();
        let mut connection = self.lock()?;
        let transaction = connection
            .transaction()
            .map_err(|_| storage_error("无法开始用户消息事务"))?;
        require_conversation(&transaction, conversation_id)?;
        let turn_sequence = next_sequence(&transaction, "conversation_turns", conversation_id)?;
        let message_sequence =
            next_sequence(&transaction, "conversation_messages", conversation_id)?;
        transaction
            .execute(
                "INSERT INTO conversation_turns(
                    id, conversation_id, sequence, status, extraction_state,
                    created_at_ms, updated_at_ms
                 ) VALUES (?1, ?2, ?3, 'interpreting', 'ineligible', ?4, ?4)",
                params![
                    turn_id.to_string(),
                    conversation_id.to_string(),
                    u64_to_i64(turn_sequence)?,
                    now
                ],
            )
            .map_err(|_| invalid_record("turn id 重复或会话 turn 顺序冲突"))?;
        transaction
            .execute(
                "INSERT INTO conversation_messages(
                    id, conversation_id, turn_id, sequence, role, content, created_at_ms
                 ) VALUES (?1, ?2, ?3, ?4, 'user', ?5, ?6)",
                params![
                    message_id.to_string(),
                    conversation_id.to_string(),
                    turn_id.to_string(),
                    u64_to_i64(message_sequence)?,
                    user_message,
                    now
                ],
            )
            .map_err(|_| storage_error("无法写入用户消息"))?;
        touch_conversation(&transaction, conversation_id, now)?;
        transaction
            .commit()
            .map_err(|_| storage_error("无法提交用户消息事务"))?;
        Ok((
            PersistedTurnRecord {
                id: turn_id,
                conversation_id,
                state: TurnState::Interpreting,
                error: None,
                created_at_unix_ms: now,
                updated_at_unix_ms: now,
            },
            ConversationMessageRecord {
                id: message_id,
                conversation_id,
                turn_id,
                sequence: message_sequence,
                role: ConversationMessageRole::User,
                content: user_message,
                created_at_unix_ms: now,
            },
        ))
    }

    pub fn complete_persisted_turn(
        &self,
        conversation_id: ConversationId,
        turn_id: TurnId,
        assistant_message: String,
    ) -> Result<ConversationMessageRecord, FairyError> {
        validate_conversation_content(&assistant_message)?;
        let now = now_unix_ms()?;
        let message_id = MessageId::new();
        let mut connection = self.lock()?;
        let transaction = connection
            .transaction()
            .map_err(|_| storage_error("无法开始助手消息事务"))?;
        let changed = transaction
            .execute(
                "UPDATE conversation_turns
                 SET status = 'completed', extraction_state = 'pending', updated_at_ms = ?3
                 WHERE id = ?1 AND conversation_id = ?2
                   AND status IN ('interpreting', 'planning', 'responding')",
                params![turn_id.to_string(), conversation_id.to_string(), now],
            )
            .map_err(|_| storage_error("无法更新持久 turn 状态"))?;
        if changed != 1 {
            return Err(invalid_record("turn 不属于当前会话或已进入终态"));
        }
        let message_sequence =
            next_sequence(&transaction, "conversation_messages", conversation_id)?;
        transaction
            .execute(
                "INSERT INTO conversation_messages(
                    id, conversation_id, turn_id, sequence, role, content, created_at_ms
                 ) VALUES (?1, ?2, ?3, ?4, 'assistant', ?5, ?6)",
                params![
                    message_id.to_string(),
                    conversation_id.to_string(),
                    turn_id.to_string(),
                    u64_to_i64(message_sequence)?,
                    assistant_message,
                    now
                ],
            )
            .map_err(|_| storage_error("无法写入助手消息"))?;
        touch_conversation(&transaction, conversation_id, now)?;
        transaction
            .commit()
            .map_err(|_| storage_error("无法提交助手消息事务"))?;
        Ok(ConversationMessageRecord {
            id: message_id,
            conversation_id,
            turn_id,
            sequence: message_sequence,
            role: ConversationMessageRole::Assistant,
            content: assistant_message,
            created_at_unix_ms: now,
        })
    }

    pub fn terminate_persisted_turn(
        &self,
        conversation_id: ConversationId,
        turn_id: TurnId,
        state: TurnState,
        error: Option<&FairyError>,
    ) -> Result<(), FairyError> {
        let status = match state {
            TurnState::Failed => "failed",
            TurnState::Interrupted => "interrupted",
            _ => {
                return Err(FairyError::new(
                    ErrorCode::InvalidConversationRecord,
                    "持久 turn 终止只接受 failed 或 interrupted",
                    false,
                ));
            }
        };
        if state == TurnState::Failed && error.is_none() {
            return Err(FairyError::new(
                ErrorCode::InvalidConversationRecord,
                "failed 持久 turn 必须携带安全错误",
                false,
            ));
        }
        if state == TurnState::Interrupted && error.is_some() {
            return Err(FairyError::new(
                ErrorCode::InvalidConversationRecord,
                "interrupted 持久 turn 不接受错误负载",
                false,
            ));
        }
        let now = now_unix_ms()?;
        let connection = self.lock()?;
        let changed = connection
            .execute(
                "UPDATE conversation_turns
                 SET status = ?3, extraction_state = 'ineligible',
                     error_code = ?4, error_message = ?5, error_retryable = ?6,
                     updated_at_ms = ?7
                 WHERE id = ?1 AND conversation_id = ?2
                   AND status IN ('interpreting', 'planning', 'responding')",
                params![
                    turn_id.to_string(),
                    conversation_id.to_string(),
                    status,
                    error.map(|value| value.code.as_str()),
                    error.map(|value| value.message.as_str()),
                    error.map(|value| i64::from(value.retryable)),
                    now,
                ],
            )
            .map_err(|_| storage_error("无法持久化 turn 终止状态"))?;
        if changed == 1 {
            Ok(())
        } else {
            Err(invalid_record("turn 不属于当前会话或已进入终态"))
        }
    }

    pub fn commit_prompt_window(
        &self,
        conversation_id: ConversationId,
        expected_revision: WindowRevision,
        summary: String,
    ) -> Result<PromptWindowRecord, FairyError> {
        let summary = summary.trim();
        if summary.is_empty()
            || summary.chars().count() > 12_000
            || summary.chars().any(|character| character == '\0')
        {
            return Err(FairyError::new(
                ErrorCode::CompactionFailed,
                "持久 compaction summary 无效",
                false,
            ));
        }
        let next_revision = expected_revision.checked_next().ok_or_else(|| {
            FairyError::new(
                ErrorCode::CompactionFailed,
                "prompt window revision 已耗尽",
                false,
            )
        })?;
        let now = now_unix_ms()?;
        let mut connection = self.lock()?;
        let transaction = connection
            .transaction()
            .map_err(|_| storage_error("无法开始 prompt window 事务"))?;
        let cutoff: i64 = transaction
            .query_row(
                "SELECT COALESCE(MAX(sequence), 0) FROM conversation_messages
                 WHERE conversation_id = ?1",
                [conversation_id.to_string()],
                |row| row.get(0),
            )
            .map_err(|_| storage_error("无法读取 prompt window cutoff"))?;
        let changed = transaction
            .execute(
                "UPDATE prompt_windows
                 SET revision = ?3, summary = ?4, cutoff_message_sequence = ?5,
                     updated_at_ms = ?6
                 WHERE conversation_id = ?1 AND revision = ?2",
                params![
                    conversation_id.to_string(),
                    u64_to_i64(expected_revision.get())?,
                    u64_to_i64(next_revision.get())?,
                    summary,
                    cutoff,
                    now,
                ],
            )
            .map_err(|_| storage_error("无法更新 prompt window"))?;
        if changed != 1 {
            return Err(FairyError::new(
                ErrorCode::CompactionFailed,
                "prompt window revision 已变化",
                false,
            ));
        }
        transaction
            .commit()
            .map_err(|_| storage_error("无法提交 prompt window 事务"))?;
        Ok(PromptWindowRecord {
            conversation_id,
            revision: next_revision,
            summary: Some(summary.to_owned()),
            cutoff_message_sequence: i64_to_u64(cutoff, "prompt window cutoff 已损坏")?,
            updated_at_unix_ms: now,
        })
    }
}
