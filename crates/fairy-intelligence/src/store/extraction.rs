use super::*;

impl IntelligenceStore {
    pub fn pending_extraction_turn_count(
        &self,
        conversation_id: ConversationId,
    ) -> Result<u64, FairyError> {
        let count: i64 = self
            .lock()?
            .query_row(
                "SELECT COUNT(*) FROM conversation_turns
                 WHERE conversation_id = ?1 AND status = 'completed'
                   AND extraction_state = 'pending'",
                [conversation_id.to_string()],
                |row| row.get(0),
            )
            .map_err(|_| storage_error("无法统计待抽取 turn"))?;
        u64::try_from(count).map_err(|_| storage_error("待抽取 turn 数量已损坏"))
    }

    pub fn claim_extraction_batch(
        &self,
        conversation_id: ConversationId,
        limit: usize,
    ) -> Result<Option<ExtractionBatchInput>, FairyError> {
        if !(1..=12).contains(&limit) {
            return Err(invalid_record("抽取批次 limit 必须位于 1..=12"));
        }
        let now = now_unix_ms()?;
        let batch_id = ExtractionBatchId::new();
        let mut connection = self.lock()?;
        let transaction = connection
            .transaction()
            .map_err(|_| storage_error("无法开始抽取批次领取事务"))?;
        let character_id: String = transaction
            .query_row(
                "SELECT character_id FROM conversations WHERE id = ?1",
                [conversation_id.to_string()],
                |row| row.get(0),
            )
            .optional()
            .map_err(|_| storage_error("无法读取抽取批次角色"))?
            .ok_or_else(|| invalid_record("抽取 conversation 不存在"))?;
        let character_id = CharacterId::from_str(&character_id)
            .map_err(|_| storage_error("抽取批次 character id 已损坏"))?;
        let mut statement = transaction
            .prepare(
                "SELECT t.id, t.sequence, u.content, a.content
                 FROM conversation_turns t
                 JOIN conversation_messages u ON u.turn_id = t.id AND u.role = 'user'
                 JOIN conversation_messages a ON a.turn_id = t.id AND a.role = 'assistant'
                 WHERE t.conversation_id = ?1 AND t.status = 'completed'
                   AND t.extraction_state = 'pending'
                 ORDER BY t.sequence ASC
                 LIMIT ?2",
            )
            .map_err(|_| storage_error("无法准备待抽取 turn 查询"))?;
        let rows = statement
            .query_map(params![conversation_id.to_string(), limit as i64], |row| {
                Ok((
                    row.get::<_, String>(0)?,
                    row.get::<_, i64>(1)?,
                    row.get::<_, String>(2)?,
                    row.get::<_, String>(3)?,
                ))
            })
            .map_err(|_| storage_error("无法读取待抽取 turn"))?;
        let mut claimed = Vec::new();
        for row in rows {
            let (turn_id, sequence, user_message, assistant_message) =
                row.map_err(|_| storage_error("待抽取 turn 已损坏"))?;
            claimed.push((
                TurnId::from_str(&turn_id).map_err(|_| storage_error("待抽取 turn id 已损坏"))?,
                i64_to_u64(sequence, "待抽取 turn sequence 已损坏")?,
                user_message,
                assistant_message,
            ));
        }
        drop(statement);
        let Some(first) = claimed.first() else {
            return Ok(None);
        };
        let last = claimed.last().expect("claimed has first and last");
        transaction
            .execute(
                "INSERT INTO extraction_batches(
                    id, conversation_id, character_id, status,
                    first_turn_sequence, last_turn_sequence, created_at_ms, updated_at_ms
                 ) VALUES (?1, ?2, ?3, 'running', ?4, ?5, ?6, ?6)",
                params![
                    batch_id.to_string(),
                    conversation_id.to_string(),
                    character_id.to_string(),
                    u64_to_i64(first.1)?,
                    u64_to_i64(last.1)?,
                    now,
                ],
            )
            .map_err(|_| invalid_record("当前 conversation 已有 running 抽取批次"))?;
        for (turn_id, sequence, _, _) in &claimed {
            let changed = transaction
                .execute(
                    "UPDATE conversation_turns SET extraction_state = 'claimed', updated_at_ms = ?3
                     WHERE id = ?1 AND conversation_id = ?2 AND extraction_state = 'pending'",
                    params![turn_id.to_string(), conversation_id.to_string(), now],
                )
                .map_err(|_| storage_error("无法领取待抽取 turn"))?;
            if changed != 1 {
                return Err(invalid_record("待抽取 turn 已被其他批次领取"));
            }
            transaction
                .execute(
                    "INSERT INTO extraction_batch_turns(batch_id, turn_id, turn_sequence)
                     VALUES (?1, ?2, ?3)",
                    params![
                        batch_id.to_string(),
                        turn_id.to_string(),
                        u64_to_i64(*sequence)?
                    ],
                )
                .map_err(|_| storage_error("无法记录抽取批次 turn"))?;
        }
        let query = claimed
            .iter()
            .flat_map(|(_, _, user, assistant)| [user.as_str(), assistant.as_str()])
            .collect::<Vec<_>>()
            .join(" ");
        let mut remaining_chars = MAX_RETRIEVED_CONTEXT_CHARS;
        let existing_memories = match build_fts_query(&query)? {
            Some(fts_query) => {
                retrieve_personal(&transaction, character_id, &fts_query, &mut remaining_chars)?
            }
            None => Vec::new(),
        };
        transaction
            .commit()
            .map_err(|_| storage_error("无法提交抽取批次领取事务"))?;
        Ok(Some(ExtractionBatchInput {
            batch_id,
            conversation_id,
            character_id,
            turns: claimed
                .into_iter()
                .map(
                    |(turn_id, _, user_message, assistant_message)| ExtractionTurn {
                        turn_id,
                        user_message,
                        assistant_message,
                    },
                )
                .collect(),
            existing_memories,
        }))
    }

    pub fn fail_extraction_batch(
        &self,
        batch_id: ExtractionBatchId,
        error: &FairyError,
    ) -> Result<(), FairyError> {
        let changed = self
            .lock()?
            .execute(
                "UPDATE extraction_batches SET status = 'failed', error_code = ?2,
                    error_message = ?3, error_retryable = ?4, updated_at_ms = ?5
                 WHERE id = ?1 AND status = 'running'",
                params![
                    batch_id.to_string(),
                    error.code.as_str(),
                    error.message,
                    i64::from(error.retryable),
                    now_unix_ms()?
                ],
            )
            .map_err(|_| storage_error("无法记录抽取批次失败"))?;
        if changed == 1 {
            Ok(())
        } else {
            Err(invalid_record("抽取批次不存在或不是 running"))
        }
    }

    pub fn retry_failed_extraction_batch(
        &self,
        batch_id: ExtractionBatchId,
    ) -> Result<ConversationId, FairyError> {
        let now = now_unix_ms()?;
        let mut connection = self.lock()?;
        let transaction = connection
            .transaction()
            .map_err(|_| storage_error("无法开始失败批次重试事务"))?;
        let conversation_id: String = transaction
            .query_row(
                "SELECT conversation_id FROM extraction_batches
                 WHERE id = ?1 AND status = 'failed'",
                [batch_id.to_string()],
                |row| row.get(0),
            )
            .optional()
            .map_err(|_| storage_error("无法读取失败抽取批次"))?
            .ok_or_else(|| invalid_record("抽取批次不存在或不是 failed"))?;
        transaction
            .execute(
                "UPDATE conversation_turns SET extraction_state = 'pending', updated_at_ms = ?2
                 WHERE id IN (
                    SELECT turn_id FROM extraction_batch_turns WHERE batch_id = ?1
                 ) AND extraction_state = 'claimed'",
                params![batch_id.to_string(), now],
            )
            .map_err(|_| storage_error("无法释放失败批次 turn"))?;
        transaction
            .execute(
                "UPDATE extraction_batches SET status = 'cancelled', updated_at_ms = ?2
                 WHERE id = ?1 AND status = 'failed'",
                params![batch_id.to_string(), now],
            )
            .map_err(|_| storage_error("无法关闭失败抽取批次"))?;
        transaction
            .commit()
            .map_err(|_| storage_error("无法提交失败批次重试事务"))?;
        ConversationId::from_str(&conversation_id)
            .map_err(|_| storage_error("失败批次 conversation id 已损坏"))
    }
}
