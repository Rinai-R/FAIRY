use super::*;

impl IntelligenceStore {
    pub fn commit_memory_mutations(
        &self,
        batch_id: ExtractionBatchId,
        character_id: CharacterId,
        allowed_memory_ids: &[PersonalMemoryId],
        mutations: Vec<MemoryMutation>,
    ) -> Result<Vec<MemoryMutationResult>, FairyError> {
        for mutation in &mutations {
            mutation.verify_integrity()?;
            validate_mutation_character(mutation, character_id)?;
        }
        let now = now_unix_ms()?;
        let mut connection = self.lock()?;
        let transaction = connection
            .transaction()
            .map_err(|_| storage_error("无法开始记忆 mutation 事务"))?;
        let (conversation_id, batch_character, source_turn_id): (String, String, String) =
            transaction
                .query_row(
                    "SELECT b.conversation_id, b.character_id, bt.turn_id
                     FROM extraction_batches b
                     JOIN extraction_batch_turns bt ON bt.batch_id = b.id
                     WHERE b.id = ?1 AND b.status = 'running'
                     ORDER BY bt.turn_sequence DESC LIMIT 1",
                    [batch_id.to_string()],
                    |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?)),
                )
                .optional()
                .map_err(|_| storage_error("无法读取 running 抽取批次"))?
                .ok_or_else(|| invalid_record("抽取批次不存在或不是 running"))?;
        if batch_character != character_id.to_string() {
            return Err(invalid_record("抽取批次不属于当前角色"));
        }
        let conversation_id = ConversationId::from_str(&conversation_id)
            .map_err(|_| storage_error("抽取批次 conversation id 已损坏"))?;
        let source_turn_id = TurnId::from_str(&source_turn_id)
            .map_err(|_| storage_error("抽取批次 source turn id 已损坏"))?;
        let allowed: std::collections::HashSet<_> = allowed_memory_ids.iter().copied().collect();
        let mut results = Vec::with_capacity(mutations.len());

        for mutation in mutations {
            match mutation {
                MemoryMutation::Create {
                    kind,
                    scope,
                    content,
                    confidence_basis_points,
                } => {
                    if let Some(existing_memory_id) =
                        find_duplicate_memory(&transaction, kind, scope, &content)?
                    {
                        results.push(MemoryMutationResult::NoChange { existing_memory_id });
                        continue;
                    }
                    let memory_id = insert_batch_memory(
                        &transaction,
                        kind,
                        scope,
                        &content,
                        confidence_basis_points,
                        conversation_id,
                        source_turn_id,
                        None,
                        now,
                    )?;
                    results.push(MemoryMutationResult::Applied { memory_id });
                }
                MemoryMutation::Supersede {
                    memory_id: supersedes_id,
                    kind,
                    scope,
                    content,
                    confidence_basis_points,
                } => {
                    if !allowed.contains(&supersedes_id) {
                        return Err(invalid_record(
                            "supersede 引用了未提供给当前批次的 memory id",
                        ));
                    }
                    require_active_memory_scope(&transaction, supersedes_id, kind, scope)?;
                    if let Some(existing_memory_id) =
                        find_duplicate_memory(&transaction, kind, scope, &content)?
                        && existing_memory_id != supersedes_id
                    {
                        results.push(MemoryMutationResult::NoChange { existing_memory_id });
                        continue;
                    }
                    supersede_personal(&transaction, supersedes_id, now)?;
                    let memory_id = insert_batch_memory(
                        &transaction,
                        kind,
                        scope,
                        &content,
                        confidence_basis_points,
                        conversation_id,
                        source_turn_id,
                        Some(supersedes_id),
                        now,
                    )?;
                    results.push(MemoryMutationResult::Applied { memory_id });
                }
            }
        }

        let changed = transaction
            .execute(
                "UPDATE extraction_batches SET status = 'succeeded', updated_at_ms = ?2
                 WHERE id = ?1 AND status = 'running'",
                params![batch_id.to_string(), now],
            )
            .map_err(|_| storage_error("无法完成抽取批次"))?;
        if changed != 1 {
            return Err(invalid_record("抽取批次状态在提交时发生变化"));
        }
        transaction
            .execute(
                "UPDATE conversation_turns SET extraction_state = 'processed', updated_at_ms = ?2
                 WHERE id IN (
                    SELECT turn_id FROM extraction_batch_turns WHERE batch_id = ?1
                 ) AND extraction_state = 'claimed'",
                params![batch_id.to_string(), now],
            )
            .map_err(|_| storage_error("无法完成批次 turn 抽取状态"))?;
        transaction
            .commit()
            .map_err(|_| storage_error("无法提交记忆 mutation 事务"))?;
        Ok(results)
    }

    pub fn append_personal_memory(
        &self,
        input: NewPersonalMemory,
    ) -> Result<PersonalMemoryRecord, FairyError> {
        validate_content("个人记忆", &input.content)?;
        validate_confidence(input.confidence_basis_points)?;
        validate_new_memory_scope(input.kind, input.scope)?;
        let (scope_kind, character_id) = memory_scope_columns(input.scope);
        let id = PersonalMemoryId::new();
        let now = now_unix_ms()?;
        let mut connection = self.lock()?;
        let transaction = connection
            .transaction()
            .map_err(|_| storage_error("无法开始个人记忆事务"))?;
        if let Some(previous_id) = input.supersedes_id {
            supersede_personal(&transaction, previous_id, now)?;
        }
        transaction
            .execute(
                "INSERT INTO personal_memories (
                    id, kind, scope_kind, character_id, review_status,
                    content, status, confidence_basis_points,
                    source_conversation_id, source_turn_id, supersedes_id,
                    created_at_ms, updated_at_ms
                 ) VALUES (?1, ?2, ?3, ?4, 'ready', ?5, 'active', ?6, ?7, ?8, ?9, ?10, ?10)",
                params![
                    id.to_string(),
                    memory_kind_name(input.kind),
                    scope_kind,
                    character_id,
                    input.content,
                    i64::from(input.confidence_basis_points),
                    input.source_conversation_id.to_string(),
                    input.source_turn_id.to_string(),
                    input.supersedes_id.map(|value| value.to_string()),
                    now,
                ],
            )
            .map_err(|_| storage_error("无法写入个人记忆"))?;
        transaction
            .commit()
            .map_err(|_| storage_error("无法提交个人记忆事务"))?;
        Ok(PersonalMemoryRecord {
            id,
            kind: input.kind,
            scope: input.scope,
            review_status: PersonalMemoryReviewStatus::Ready,
            content: input.content,
            status: PersonalMemoryStatus::Active,
            confidence_basis_points: input.confidence_basis_points,
            source_conversation_id: input.source_conversation_id,
            source_turn_id: input.source_turn_id,
            supersedes_id: input.supersedes_id,
            created_at_unix_ms: now,
            updated_at_unix_ms: now,
        })
    }

    pub fn append_knowledge(&self, input: NewKnowledge) -> Result<KnowledgeRecord, FairyError> {
        validate_content("知识主题", &input.topic)?;
        validate_content("知识陈述", &input.statement)?;
        validate_confidence(input.confidence_basis_points)?;
        for source in &input.sources {
            validate_content("知识来源标题", &source.title)?;
            validate_content("知识来源 URL", &source.url)?;
            validate_content("知识来源摘要", &source.snippet)?;
        }
        let id = KnowledgeId::new();
        let now = now_unix_ms()?;
        let status = if input.sources.is_empty() {
            KnowledgeStatus::Candidate
        } else {
            KnowledgeStatus::Verified
        };
        let verification_basis = if input.sources.is_empty() {
            KnowledgeVerificationBasis::Unverified
        } else {
            KnowledgeVerificationBasis::WebSource
        };
        let mut connection = self.lock()?;
        let transaction = connection
            .transaction()
            .map_err(|_| storage_error("无法开始知识事务"))?;
        if let Some(previous_id) = input.supersedes_id {
            supersede_knowledge(&transaction, previous_id, now)?;
        }
        transaction
            .execute(
                "INSERT INTO knowledge_entries (
                    id, topic, statement, status, verification_basis, confidence_basis_points,
                    source_conversation_id, source_turn_id, supersedes_id,
                    created_at_ms, updated_at_ms
                 ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?10)",
                params![
                    id.to_string(),
                    input.topic,
                    input.statement,
                    knowledge_status_name(status),
                    verification_basis_name(verification_basis),
                    i64::from(input.confidence_basis_points),
                    input.source_conversation_id.to_string(),
                    input.source_turn_id.to_string(),
                    input.supersedes_id.map(|value| value.to_string()),
                    now,
                ],
            )
            .map_err(|_| storage_error("无法写入知识条目"))?;
        for source in &input.sources {
            transaction
                .execute(
                    "INSERT INTO knowledge_sources (
                        knowledge_id, source_id, title, url, snippet, rank, fetched_at_ms
                     ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)",
                    params![
                        id.to_string(),
                        KnowledgeSourceId::new().to_string(),
                        source.title,
                        source.url,
                        source.snippet,
                        i64::from(source.rank),
                        source.fetched_at_unix_ms,
                    ],
                )
                .map_err(|_| storage_error("无法写入知识来源"))?;
        }
        transaction
            .commit()
            .map_err(|_| storage_error("无法提交知识事务"))?;
        Ok(KnowledgeRecord {
            id,
            topic: input.topic,
            statement: input.statement,
            status,
            verification_basis,
            confidence_basis_points: input.confidence_basis_points,
            source_conversation_id: input.source_conversation_id,
            source_turn_id: input.source_turn_id,
            supersedes_id: input.supersedes_id,
            sources: input.sources,
            created_at_unix_ms: now,
            updated_at_unix_ms: now,
        })
    }

    pub fn tombstone_personal_memory(&self, id: PersonalMemoryId) -> Result<(), FairyError> {
        let connection = self.lock()?;
        update_record_status(
            &connection,
            "personal_memories",
            &id.to_string(),
            "tombstone",
        )
    }

    pub fn tombstone_knowledge(&self, id: KnowledgeId) -> Result<(), FairyError> {
        let connection = self.lock()?;
        let changed = connection
            .execute(
                "UPDATE knowledge_entries
                 SET status = 'tombstone', updated_at_ms = ?2
                 WHERE id = ?1 AND status IN ('candidate', 'verified')",
                params![id.to_string(), now_unix_ms()?],
            )
            .map_err(|_| storage_error("无法 tombstone 知识条目"))?;
        if changed != 1 {
            return Err(invalid_record("知识条目不存在或不能从当前状态删除"));
        }
        Ok(())
    }

    pub fn knowledge_catalog(&self) -> Result<KnowledgeCatalog, FairyError> {
        let connection = self.lock()?;
        Ok(KnowledgeCatalog {
            candidates: list_knowledge(&connection, KnowledgeStatus::Candidate)?,
            verified: list_knowledge(&connection, KnowledgeStatus::Verified)?,
        })
    }

    pub fn personal_memory_catalog(
        &self,
        character_id: CharacterId,
    ) -> Result<fairy_domain::PersonalMemoryCatalog, FairyError> {
        let connection = self.lock()?;
        Ok(fairy_domain::PersonalMemoryCatalog {
            global: list_personal_memories(&connection, "global", None, "ready")?,
            character: list_personal_memories(
                &connection,
                "character",
                Some(character_id),
                "ready",
            )?,
            needs_review: list_personal_memories(
                &connection,
                "unassigned_legacy",
                None,
                "needs_review",
            )?,
        })
    }

    pub fn extraction_batch_catalog(
        &self,
        character_id: CharacterId,
    ) -> Result<ExtractionBatchCatalog, FairyError> {
        let connection = self.lock()?;
        Ok(ExtractionBatchCatalog {
            running: list_extraction_batches(
                &connection,
                character_id,
                ExtractionBatchStatus::Running,
            )?,
            failed: list_extraction_batches(
                &connection,
                character_id,
                ExtractionBatchStatus::Failed,
            )?,
        })
    }

    pub fn revise_personal_memory(
        &self,
        id: PersonalMemoryId,
        content: String,
        confidence_basis_points: u16,
    ) -> Result<PersonalMemoryRecord, FairyError> {
        let existing = {
            let connection = self.lock()?;
            personal_memory_record(&connection, id)?
        };
        if existing.status != PersonalMemoryStatus::Active
            || existing.review_status != PersonalMemoryReviewStatus::Ready
            || existing.scope == MemoryScope::UnassignedLegacy
        {
            return Err(invalid_record("只有 ready active 记忆可以修正"));
        }
        self.append_personal_memory(NewPersonalMemory {
            kind: existing.kind,
            scope: existing.scope,
            content,
            confidence_basis_points,
            source_conversation_id: existing.source_conversation_id,
            source_turn_id: existing.source_turn_id,
            supersedes_id: Some(existing.id),
        })
    }

    pub fn assign_legacy_relationship(
        &self,
        id: PersonalMemoryId,
        character_id: CharacterId,
    ) -> Result<PersonalMemoryRecord, FairyError> {
        let existing = {
            let connection = self.lock()?;
            personal_memory_record(&connection, id)?
        };
        if existing.kind != PersonalMemoryKind::Relationship
            || existing.scope != MemoryScope::UnassignedLegacy
            || existing.review_status != PersonalMemoryReviewStatus::NeedsReview
            || existing.status != PersonalMemoryStatus::Active
        {
            return Err(invalid_record("记录不是可分配的旧关系记忆"));
        }
        let now = now_unix_ms()?;
        let mut connection = self.lock()?;
        let transaction = connection
            .transaction()
            .map_err(|_| storage_error("无法开始旧关系记忆分配事务"))?;
        supersede_personal(&transaction, id, now)?;
        let new_id = insert_batch_memory(
            &transaction,
            PersonalMemoryKind::Relationship,
            MemoryScope::Character { character_id },
            &existing.content,
            existing.confidence_basis_points,
            existing.source_conversation_id,
            existing.source_turn_id,
            Some(id),
            now,
        )?;
        transaction
            .commit()
            .map_err(|_| storage_error("无法提交旧关系记忆分配事务"))?;
        Ok(PersonalMemoryRecord {
            id: new_id,
            kind: PersonalMemoryKind::Relationship,
            scope: MemoryScope::Character { character_id },
            review_status: PersonalMemoryReviewStatus::Ready,
            content: existing.content,
            status: PersonalMemoryStatus::Active,
            confidence_basis_points: existing.confidence_basis_points,
            source_conversation_id: existing.source_conversation_id,
            source_turn_id: existing.source_turn_id,
            supersedes_id: Some(id),
            created_at_unix_ms: now,
            updated_at_unix_ms: now,
        })
    }

    pub fn confirm_knowledge_candidate(
        &self,
        id: KnowledgeId,
    ) -> Result<KnowledgeRecord, FairyError> {
        let connection = self.lock()?;
        let changed = connection
            .execute(
                "UPDATE knowledge_entries
                 SET status = 'verified',
                     verification_basis = 'user_confirmed',
                     updated_at_ms = ?2
                 WHERE id = ?1
                   AND status = 'candidate'
                   AND verification_basis = 'unverified'
                   AND NOT EXISTS (
                       SELECT 1 FROM knowledge_sources s
                       WHERE s.knowledge_id = knowledge_entries.id
                   )",
                params![id.to_string(), now_unix_ms()?],
            )
            .map_err(|_| storage_error("无法确认知识候选"))?;
        if changed != 1 {
            return Err(invalid_record("知识条目不存在或不是可确认候选"));
        }
        knowledge_record(&connection, id)
    }

    pub fn personal_memory_status(
        &self,
        id: PersonalMemoryId,
    ) -> Result<PersonalMemoryStatus, FairyError> {
        let connection = self.lock()?;
        let status = record_status(&connection, "personal_memories", &id.to_string())?;
        parse_personal_status(&status)
    }

    pub fn knowledge_status(&self, id: KnowledgeId) -> Result<KnowledgeStatus, FairyError> {
        let connection = self.lock()?;
        let status = record_status(&connection, "knowledge_entries", &id.to_string())?;
        parse_knowledge_status(&status)
    }
}
