use std::path::Path;
use std::str::FromStr;
use std::sync::{Mutex, MutexGuard};
use std::time::{SystemTime, UNIX_EPOCH};

use fairy_domain::{
    CharacterId, ConversationBootstrap, ConversationId, ConversationMessageRecord,
    ConversationMessageRole, ConversationRecord, ErrorCode, ExtractionBatchCatalog,
    ExtractionBatchId, ExtractionBatchInput, ExtractionBatchRecord, ExtractionBatchStatus,
    ExtractionTurn, FairyError, IntelligenceStoreSummary, KnowledgeCatalog, KnowledgeId,
    KnowledgeRecord, KnowledgeSourceId, KnowledgeStatus, KnowledgeVerificationBasis,
    MemoryMutation, MemoryMutationResult, MemoryScope, MessageId, NewKnowledge, NewPersonalMemory,
    PersistedTurnRecord, PersonalMemoryId, PersonalMemoryKind, PersonalMemoryRecord,
    PersonalMemoryReviewStatus, PersonalMemoryStatus, PromptWindowRecord, RetrievalContext,
    RetrievedKnowledge, RetrievedPersonalMemory, TurnId, TurnState, WindowRevision,
};
use fairy_harness::CompanionPersistence;
use rusqlite::{Connection, OptionalExtension, Transaction, params};

const SCHEMA_VERSION: i64 = 3;
const MAX_RESULTS_PER_KIND: usize = 4;
const MAX_RETRIEVED_CONTEXT_CHARS: usize = 2400;
const MAX_CATALOG_RESULTS_PER_STATUS: usize = 20;

pub struct IntelligenceStore {
    connection: Mutex<Connection>,
}

#[async_trait::async_trait]
impl CompanionPersistence for IntelligenceStore {
    async fn open_or_create_character_conversation(
        &self,
        character_id: CharacterId,
    ) -> Result<ConversationBootstrap, FairyError> {
        IntelligenceStore::open_or_create_character_conversation(self, character_id)
    }

    async fn begin_turn(
        &self,
        conversation_id: ConversationId,
        turn_id: TurnId,
        user_message: String,
    ) -> Result<(), FairyError> {
        self.begin_persisted_turn(conversation_id, turn_id, user_message)
            .map(|_| ())
    }

    async fn complete_turn(
        &self,
        conversation_id: ConversationId,
        turn_id: TurnId,
        assistant_message: String,
    ) -> Result<(), FairyError> {
        self.complete_persisted_turn(conversation_id, turn_id, assistant_message)
            .map(|_| ())
    }

    async fn terminate_turn(
        &self,
        conversation_id: ConversationId,
        turn_id: TurnId,
        state: TurnState,
        error: Option<FairyError>,
    ) -> Result<(), FairyError> {
        self.terminate_persisted_turn(conversation_id, turn_id, state, error.as_ref())
    }

    async fn retrieve(
        &self,
        character_id: CharacterId,
        query: String,
    ) -> Result<RetrievalContext, FairyError> {
        IntelligenceStore::retrieve(self, character_id, &query)
    }

    async fn pending_extraction_turn_count(
        &self,
        conversation_id: ConversationId,
    ) -> Result<u64, FairyError> {
        IntelligenceStore::pending_extraction_turn_count(self, conversation_id)
    }

    async fn claim_extraction_batch(
        &self,
        conversation_id: ConversationId,
        limit: usize,
    ) -> Result<Option<ExtractionBatchInput>, FairyError> {
        IntelligenceStore::claim_extraction_batch(self, conversation_id, limit)
    }

    async fn commit_memory_mutations(
        &self,
        batch_id: ExtractionBatchId,
        character_id: CharacterId,
        allowed_memory_ids: Vec<PersonalMemoryId>,
        mutations: Vec<MemoryMutation>,
    ) -> Result<Vec<MemoryMutationResult>, FairyError> {
        IntelligenceStore::commit_memory_mutations(
            self,
            batch_id,
            character_id,
            &allowed_memory_ids,
            mutations,
        )
    }

    async fn fail_extraction_batch(
        &self,
        batch_id: ExtractionBatchId,
        error: FairyError,
    ) -> Result<(), FairyError> {
        IntelligenceStore::fail_extraction_batch(self, batch_id, &error)
    }

    async fn retry_failed_extraction_batch(
        &self,
        batch_id: ExtractionBatchId,
    ) -> Result<ConversationId, FairyError> {
        IntelligenceStore::retry_failed_extraction_batch(self, batch_id)
    }

    async fn commit_prompt_window(
        &self,
        conversation_id: ConversationId,
        expected_revision: WindowRevision,
        summary: String,
    ) -> Result<PromptWindowRecord, FairyError> {
        IntelligenceStore::commit_prompt_window(self, conversation_id, expected_revision, summary)
    }
}

mod conversation;
mod extraction;
mod memory;
mod retrieval;
mod schema;

impl IntelligenceStore {
    fn lock(&self) -> Result<MutexGuard<'_, Connection>, FairyError> {
        self.connection
            .lock()
            .map_err(|_| storage_error("智能层数据库锁已损坏"))
    }
}
#[derive(Debug)]
struct RawKnowledgeRow {
    id: String,
    topic: String,
    statement: String,
    status: String,
    verification_basis: String,
    confidence_basis_points: i64,
    source_conversation_id: String,
    source_turn_id: String,
    supersedes_id: Option<String>,
    created_at_ms: i64,
    updated_at_ms: i64,
}

#[derive(Debug)]
struct RawPersonalMemoryRow {
    id: String,
    kind: String,
    scope_kind: String,
    character_id: Option<String>,
    review_status: String,
    content: String,
    status: String,
    confidence_basis_points: i64,
    source_conversation_id: String,
    source_turn_id: String,
    supersedes_id: Option<String>,
    created_at_ms: i64,
    updated_at_ms: i64,
}

fn list_extraction_batches(
    connection: &Connection,
    character_id: CharacterId,
    status: ExtractionBatchStatus,
) -> Result<Vec<ExtractionBatchRecord>, FairyError> {
    let status_name = match status {
        ExtractionBatchStatus::Running => "running",
        ExtractionBatchStatus::Failed => "failed",
        _ => return Err(invalid_record("批次目录只支持 running 或 failed")),
    };
    let mut statement = connection
        .prepare(
            "SELECT id, conversation_id, character_id, first_turn_sequence,
                    last_turn_sequence, error_code, error_message, error_retryable,
                    created_at_ms, updated_at_ms
             FROM extraction_batches
             WHERE character_id = ?1 AND status = ?2
             ORDER BY updated_at_ms DESC, id ASC LIMIT 50",
        )
        .map_err(|_| storage_error("无法准备抽取批次目录"))?;
    let rows = statement
        .query_map(params![character_id.to_string(), status_name], |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, String>(1)?,
                row.get::<_, String>(2)?,
                row.get::<_, i64>(3)?,
                row.get::<_, i64>(4)?,
                row.get::<_, Option<String>>(5)?,
                row.get::<_, Option<String>>(6)?,
                row.get::<_, Option<i64>>(7)?,
                row.get::<_, i64>(8)?,
                row.get::<_, i64>(9)?,
            ))
        })
        .map_err(|_| storage_error("无法查询抽取批次目录"))?;
    rows.map(|row| {
        let (
            id,
            conversation_id,
            stored_character_id,
            first,
            last,
            error_code,
            error_message,
            error_retryable,
            created,
            updated,
        ) = row.map_err(|_| storage_error("抽取批次目录已损坏"))?;
        let error = match (error_code, error_message, error_retryable) {
            (None, None, None) => None,
            (Some(code), Some(message), Some(retryable)) => Some(FairyError::new(
                ErrorCode::from_wire_name(&code)
                    .ok_or_else(|| storage_error("抽取批次 error code 已损坏"))?,
                message,
                retryable != 0,
            )),
            _ => return Err(storage_error("抽取批次 error 字段不完整")),
        };
        Ok(ExtractionBatchRecord {
            id: ExtractionBatchId::from_str(&id)
                .map_err(|_| storage_error("抽取批次 id 已损坏"))?,
            conversation_id: ConversationId::from_str(&conversation_id)
                .map_err(|_| storage_error("抽取批次 conversation id 已损坏"))?,
            character_id: CharacterId::from_str(&stored_character_id)
                .map_err(|_| storage_error("抽取批次 character id 已损坏"))?,
            status,
            first_turn_sequence: i64_to_u64(first, "抽取批次 first sequence 已损坏")?,
            last_turn_sequence: i64_to_u64(last, "抽取批次 last sequence 已损坏")?,
            error,
            created_at_unix_ms: created,
            updated_at_unix_ms: updated,
        })
    })
    .collect()
}

fn list_personal_memories(
    connection: &Connection,
    scope_kind: &str,
    character_id: Option<CharacterId>,
    review_status: &str,
) -> Result<Vec<PersonalMemoryRecord>, FairyError> {
    let mut statement = connection
        .prepare(
            "SELECT id, kind, scope_kind, character_id, review_status, content, status,
                    confidence_basis_points, source_conversation_id, source_turn_id,
                    supersedes_id, created_at_ms, updated_at_ms
             FROM personal_memories
             WHERE scope_kind = ?1 AND character_id IS ?2 AND review_status = ?3
               AND status = 'active'
             ORDER BY updated_at_ms DESC, id ASC
             LIMIT 100",
        )
        .map_err(|_| storage_error("无法准备个人记忆目录查询"))?;
    let rows = statement
        .query_map(
            params![
                scope_kind,
                character_id.map(|value| value.to_string()),
                review_status
            ],
            raw_personal_memory_row,
        )
        .map_err(|_| storage_error("无法查询个人记忆目录"))?;
    rows.map(|row| {
        row.map_err(|_| storage_error("个人记忆目录结果已损坏"))
            .and_then(personal_memory_from_raw)
    })
    .collect()
}

fn personal_memory_record(
    connection: &Connection,
    id: PersonalMemoryId,
) -> Result<PersonalMemoryRecord, FairyError> {
    let raw = connection
        .query_row(
            "SELECT id, kind, scope_kind, character_id, review_status, content, status,
                    confidence_basis_points, source_conversation_id, source_turn_id,
                    supersedes_id, created_at_ms, updated_at_ms
             FROM personal_memories WHERE id = ?1",
            [id.to_string()],
            raw_personal_memory_row,
        )
        .optional()
        .map_err(|_| storage_error("无法读取个人记忆"))?
        .ok_or_else(|| invalid_record("个人记忆不存在"))?;
    personal_memory_from_raw(raw)
}

fn raw_personal_memory_row(row: &rusqlite::Row<'_>) -> rusqlite::Result<RawPersonalMemoryRow> {
    Ok(RawPersonalMemoryRow {
        id: row.get(0)?,
        kind: row.get(1)?,
        scope_kind: row.get(2)?,
        character_id: row.get(3)?,
        review_status: row.get(4)?,
        content: row.get(5)?,
        status: row.get(6)?,
        confidence_basis_points: row.get(7)?,
        source_conversation_id: row.get(8)?,
        source_turn_id: row.get(9)?,
        supersedes_id: row.get(10)?,
        created_at_ms: row.get(11)?,
        updated_at_ms: row.get(12)?,
    })
}

fn personal_memory_from_raw(raw: RawPersonalMemoryRow) -> Result<PersonalMemoryRecord, FairyError> {
    Ok(PersonalMemoryRecord {
        id: PersonalMemoryId::from_str(&raw.id).map_err(|_| storage_error("个人记忆 id 已损坏"))?,
        kind: parse_memory_kind(&raw.kind)?,
        scope: parse_memory_scope(&raw.scope_kind, raw.character_id.as_deref())?,
        review_status: parse_review_status(&raw.review_status)?,
        content: raw.content,
        status: parse_personal_status(&raw.status)?,
        confidence_basis_points: u16::try_from(raw.confidence_basis_points)
            .map_err(|_| storage_error("个人记忆置信度已损坏"))?,
        source_conversation_id: ConversationId::from_str(&raw.source_conversation_id)
            .map_err(|_| storage_error("个人记忆 source conversation id 已损坏"))?,
        source_turn_id: TurnId::from_str(&raw.source_turn_id)
            .map_err(|_| storage_error("个人记忆 source turn id 已损坏"))?,
        supersedes_id: raw
            .supersedes_id
            .map(|value| {
                PersonalMemoryId::from_str(&value)
                    .map_err(|_| storage_error("个人记忆 supersedes id 已损坏"))
            })
            .transpose()?,
        created_at_unix_ms: raw.created_at_ms,
        updated_at_unix_ms: raw.updated_at_ms,
    })
}

fn list_knowledge(
    connection: &Connection,
    status: KnowledgeStatus,
) -> Result<Vec<KnowledgeRecord>, FairyError> {
    if !matches!(
        status,
        KnowledgeStatus::Candidate | KnowledgeStatus::Verified
    ) {
        return Err(invalid_record("知识目录只支持 candidate 或 verified 状态"));
    }
    let mut statement = connection
        .prepare(
            "SELECT id, topic, statement, status, verification_basis,
                    confidence_basis_points, source_conversation_id, source_turn_id,
                    supersedes_id, created_at_ms, updated_at_ms
             FROM knowledge_entries
             WHERE status = ?1
             ORDER BY updated_at_ms DESC, id ASC
             LIMIT ?2",
        )
        .map_err(|_| storage_error("无法准备知识目录查询"))?;
    let rows = statement
        .query_map(
            params![
                knowledge_status_name(status),
                MAX_CATALOG_RESULTS_PER_STATUS as i64
            ],
            raw_knowledge_row,
        )
        .map_err(|_| storage_error("无法查询知识目录"))?;
    let mut records = Vec::new();
    for row in rows {
        let raw = row.map_err(|_| storage_error("知识目录结果已损坏"))?;
        records.push(knowledge_record_from_raw(connection, raw)?);
    }
    Ok(records)
}

fn knowledge_record(
    connection: &Connection,
    id: KnowledgeId,
) -> Result<KnowledgeRecord, FairyError> {
    let raw = connection
        .query_row(
            "SELECT id, topic, statement, status, verification_basis,
                    confidence_basis_points, source_conversation_id, source_turn_id,
                    supersedes_id, created_at_ms, updated_at_ms
             FROM knowledge_entries
             WHERE id = ?1",
            [id.to_string()],
            raw_knowledge_row,
        )
        .optional()
        .map_err(|_| storage_error("无法读取知识条目"))?
        .ok_or_else(|| invalid_record("知识条目不存在"))?;
    knowledge_record_from_raw(connection, raw)
}

fn raw_knowledge_row(row: &rusqlite::Row<'_>) -> rusqlite::Result<RawKnowledgeRow> {
    Ok(RawKnowledgeRow {
        id: row.get(0)?,
        topic: row.get(1)?,
        statement: row.get(2)?,
        status: row.get(3)?,
        verification_basis: row.get(4)?,
        confidence_basis_points: row.get(5)?,
        source_conversation_id: row.get(6)?,
        source_turn_id: row.get(7)?,
        supersedes_id: row.get(8)?,
        created_at_ms: row.get(9)?,
        updated_at_ms: row.get(10)?,
    })
}

fn knowledge_record_from_raw(
    connection: &Connection,
    raw: RawKnowledgeRow,
) -> Result<KnowledgeRecord, FairyError> {
    let id = KnowledgeId::from_str(&raw.id).map_err(|_| storage_error("知识 id 已损坏"))?;
    Ok(KnowledgeRecord {
        id,
        topic: raw.topic,
        statement: raw.statement,
        status: parse_knowledge_status(&raw.status)?,
        verification_basis: parse_verification_basis(&raw.verification_basis)?,
        confidence_basis_points: u16::try_from(raw.confidence_basis_points)
            .map_err(|_| storage_error("知识置信度已损坏"))?,
        source_conversation_id: fairy_domain::ConversationId::from_str(&raw.source_conversation_id)
            .map_err(|_| storage_error("知识 source conversation id 已损坏"))?,
        source_turn_id: fairy_domain::TurnId::from_str(&raw.source_turn_id)
            .map_err(|_| storage_error("知识 source turn id 已损坏"))?,
        supersedes_id: raw
            .supersedes_id
            .map(|value| {
                KnowledgeId::from_str(&value)
                    .map_err(|_| storage_error("知识 supersedes id 已损坏"))
            })
            .transpose()?,
        sources: knowledge_sources(connection, id)?,
        created_at_unix_ms: raw.created_at_ms,
        updated_at_unix_ms: raw.updated_at_ms,
    })
}

fn cancel_interrupted_jobs(connection: &mut Connection) -> Result<(), FairyError> {
    let now = now_unix_ms()?;
    let transaction = connection
        .transaction()
        .map_err(|_| storage_error("无法开始恢复后台提取任务事务"))?;
    transaction
        .execute(
            "UPDATE extraction_jobs SET
                status = 'cancelled',
                error_code = ?1,
                error_message = ?2,
                error_retryable = 0,
                updated_at_ms = ?3
             WHERE status IN ('pending', 'running')",
            params![
                ErrorCode::TurnInterrupted.as_str(),
                "应用上次退出时后台提取任务尚未完成",
                now,
            ],
        )
        .map_err(|_| storage_error("无法恢复后台提取任务状态"))?;
    transaction
        .execute(
            "UPDATE conversation_turns
             SET extraction_state = 'pending', updated_at_ms = ?1
             WHERE extraction_state = 'claimed'
               AND id IN (
                    SELECT bt.turn_id
                    FROM extraction_batch_turns bt
                    JOIN extraction_batches b ON b.id = bt.batch_id
                    WHERE b.status = 'running'
               )",
            [now],
        )
        .map_err(|_| storage_error("无法释放中断抽取批次的 turn"))?;
    transaction
        .execute(
            "UPDATE extraction_batches
             SET status = 'cancelled', error_code = ?1, error_message = ?2,
                 error_retryable = 0, updated_at_ms = ?3
             WHERE status = 'running'",
            params![
                ErrorCode::TurnInterrupted.as_str(),
                "应用上次退出时抽取批次尚未完成",
                now
            ],
        )
        .map_err(|_| storage_error("无法取消中断抽取批次"))?;
    transaction
        .commit()
        .map_err(|_| storage_error("无法提交后台提取任务恢复事务"))
}

fn build_fts_query(query: &str) -> Result<Option<String>, FairyError> {
    if query.chars().any(char::is_control) || query.chars().count() > 2000 {
        return Err(invalid_record("检索 query 超长或包含控制字符"));
    }
    let mut terms = std::collections::BTreeSet::new();
    let mut chunk = Vec::new();
    let flush = |chunk: &mut Vec<char>, terms: &mut std::collections::BTreeSet<String>| {
        if chunk.len() >= 3 {
            for window in chunk.windows(3) {
                terms.insert(window.iter().collect());
            }
        }
        chunk.clear();
    };
    for character in query.chars() {
        if character.is_alphanumeric() {
            chunk.push(character);
        } else {
            flush(&mut chunk, &mut terms);
        }
    }
    flush(&mut chunk, &mut terms);
    if terms.is_empty() {
        return Ok(None);
    }
    Ok(Some(
        terms
            .into_iter()
            .map(|term| format!("\"{term}\""))
            .collect::<Vec<_>>()
            .join(" OR "),
    ))
}

fn retrieve_personal(
    connection: &Connection,
    character_id: CharacterId,
    fts_query: &str,
    remaining_chars: &mut usize,
) -> Result<Vec<RetrievedPersonalMemory>, FairyError> {
    let mut statement = connection
        .prepare(
            "SELECT p.id, p.kind, p.scope_kind, p.character_id,
                    p.content, p.confidence_basis_points, p.updated_at_ms
             FROM personal_memories_fts f
             JOIN personal_memories p ON p.rowid = f.rowid
             WHERE personal_memories_fts MATCH ?1
               AND p.status = 'active'
               AND p.review_status = 'ready'
               AND (
                    p.scope_kind = 'global'
                    OR (p.scope_kind = 'character' AND p.character_id = ?2)
               )
             ORDER BY bm25(personal_memories_fts) ASC,
                      p.confidence_basis_points DESC,
                      p.updated_at_ms DESC,
                      p.id ASC
             LIMIT 64",
        )
        .map_err(|_| storage_error("无法准备个人记忆检索"))?;
    let rows = statement
        .query_map(params![fts_query, character_id.to_string()], |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, String>(1)?,
                row.get::<_, String>(2)?,
                row.get::<_, Option<String>>(3)?,
                row.get::<_, String>(4)?,
                row.get::<_, i64>(5)?,
                row.get::<_, i64>(6)?,
            ))
        })
        .map_err(|_| storage_error("无法执行个人记忆检索"))?;
    let mut records = Vec::new();
    let mut per_kind = std::collections::HashMap::new();
    for row in rows {
        let (id, kind, scope_kind, character_id, content, confidence, updated_at) =
            row.map_err(|_| storage_error("个人记忆检索结果已损坏"))?;
        let parsed_kind = parse_memory_kind(&kind)?;
        let count = per_kind.entry(parsed_kind).or_insert(0_usize);
        if *count >= MAX_RESULTS_PER_KIND {
            continue;
        }
        let length = content.chars().count();
        if length > *remaining_chars {
            continue;
        }
        *remaining_chars -= length;
        records.push(RetrievedPersonalMemory {
            id: PersonalMemoryId::from_str(&id).map_err(|_| storage_error("个人记忆 id 已损坏"))?,
            kind: parsed_kind,
            scope: parse_memory_scope(&scope_kind, character_id.as_deref())?,
            content,
            confidence_basis_points: u16::try_from(confidence)
                .map_err(|_| storage_error("个人记忆置信度已损坏"))?,
            updated_at_unix_ms: updated_at,
        });
        *count += 1;
    }
    Ok(records)
}

fn retrieve_knowledge(
    connection: &Connection,
    fts_query: &str,
    remaining_chars: &mut usize,
) -> Result<Vec<RetrievedKnowledge>, FairyError> {
    let mut statement = connection
        .prepare(
            "SELECT k.id, k.topic, k.statement, k.verification_basis,
                    k.confidence_basis_points, k.updated_at_ms
             FROM knowledge_entries_fts f
             JOIN knowledge_entries k ON k.rowid = f.rowid
             WHERE knowledge_entries_fts MATCH ?1 AND k.status = 'verified'
             ORDER BY bm25(knowledge_entries_fts) ASC,
                      k.confidence_basis_points DESC,
                      k.updated_at_ms DESC,
                      k.id ASC
             LIMIT ?2",
        )
        .map_err(|_| storage_error("无法准备知识检索"))?;
    let rows = statement
        .query_map(params![fts_query, MAX_RESULTS_PER_KIND as i64], |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, String>(1)?,
                row.get::<_, String>(2)?,
                row.get::<_, String>(3)?,
                row.get::<_, i64>(4)?,
                row.get::<_, i64>(5)?,
            ))
        })
        .map_err(|_| storage_error("无法执行知识检索"))?;
    let mut records = Vec::new();
    for row in rows {
        let (raw_id, topic, statement, verification_basis, confidence, updated_at) =
            row.map_err(|_| storage_error("知识检索结果已损坏"))?;
        let length = topic.chars().count() + statement.chars().count();
        if length > *remaining_chars {
            continue;
        }
        *remaining_chars -= length;
        let id = KnowledgeId::from_str(&raw_id).map_err(|_| storage_error("知识 id 已损坏"))?;
        records.push(RetrievedKnowledge {
            id,
            topic,
            statement,
            verification_basis: parse_verification_basis(&verification_basis)?,
            confidence_basis_points: u16::try_from(confidence)
                .map_err(|_| storage_error("知识置信度已损坏"))?,
            sources: knowledge_sources(connection, id)?,
            updated_at_unix_ms: updated_at,
        });
    }
    Ok(records)
}

fn knowledge_sources(
    connection: &Connection,
    id: KnowledgeId,
) -> Result<Vec<fairy_domain::AssistantSource>, FairyError> {
    let mut statement = connection
        .prepare(
            "SELECT title, url, snippet, rank, fetched_at_ms
             FROM knowledge_sources
             WHERE knowledge_id = ?1
             ORDER BY rank ASC, source_id ASC",
        )
        .map_err(|_| storage_error("无法准备知识来源查询"))?;
    let rows = statement
        .query_map([id.to_string()], |row| {
            Ok(fairy_domain::AssistantSource {
                title: row.get(0)?,
                url: row.get(1)?,
                snippet: row.get(2)?,
                rank: u8::try_from(row.get::<_, i64>(3)?).map_err(|error| {
                    rusqlite::Error::FromSqlConversionFailure(
                        3,
                        rusqlite::types::Type::Integer,
                        Box::new(error),
                    )
                })?,
                fetched_at_unix_ms: row.get(4)?,
            })
        })
        .map_err(|_| storage_error("无法查询知识来源"))?;
    rows.collect::<Result<Vec<_>, _>>()
        .map_err(|_| storage_error("知识来源结果已损坏"))
}

fn initialize_schema(connection: &mut Connection) -> Result<(), FairyError> {
    let has_meta: bool = connection
        .query_row(
            "SELECT EXISTS(
                SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'schema_meta'
             )",
            [],
            |row| row.get(0),
        )
        .map_err(|_| storage_error("无法检查智能层 schema"))?;
    if has_meta {
        let version: i64 = connection
            .query_row(
                "SELECT version FROM schema_meta WHERE singleton = 1",
                [],
                |row| row.get(0),
            )
            .map_err(|_| storage_error("智能层 schema_meta 已损坏"))?;
        match version {
            SCHEMA_VERSION => return Ok(()),
            1 => {
                migrate_schema_v1(connection)?;
                return migrate_schema_v2(connection);
            }
            2 => return migrate_schema_v2(connection),
            _ => {
                return Err(FairyError::new(
                    ErrorCode::StorageCorrupted,
                    "智能层数据库 schema 版本不受支持",
                    false,
                ));
            }
        }
    }

    let transaction = connection
        .transaction()
        .map_err(|_| storage_error("无法开始智能层 schema 事务"))?;
    transaction
        .execute_batch(
            "CREATE TABLE schema_meta (
                singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
                version INTEGER NOT NULL
             );
             INSERT INTO schema_meta(singleton, version) VALUES (1, 3);

             CREATE TABLE personal_memories (
                id TEXT PRIMARY KEY,
                kind TEXT NOT NULL,
                scope_kind TEXT NOT NULL CHECK(scope_kind IN ('global', 'character', 'unassigned_legacy')),
                character_id TEXT,
                review_status TEXT NOT NULL CHECK(review_status IN ('ready', 'needs_review')),
                content TEXT NOT NULL,
                status TEXT NOT NULL,
                confidence_basis_points INTEGER NOT NULL CHECK(confidence_basis_points BETWEEN 0 AND 10000),
                source_conversation_id TEXT NOT NULL,
                source_turn_id TEXT NOT NULL,
                supersedes_id TEXT REFERENCES personal_memories(id),
                created_at_ms INTEGER NOT NULL,
                updated_at_ms INTEGER NOT NULL,
                CHECK(
                    (kind = 'relationship' AND scope_kind IN ('character', 'unassigned_legacy'))
                    OR (kind != 'relationship' AND scope_kind = 'global')
                ),
                CHECK(
                    (scope_kind = 'character' AND character_id IS NOT NULL)
                    OR (scope_kind != 'character' AND character_id IS NULL)
                ),
                CHECK(
                    (scope_kind = 'unassigned_legacy' AND review_status = 'needs_review')
                    OR (scope_kind != 'unassigned_legacy' AND review_status = 'ready')
                )
             );

             CREATE TABLE knowledge_entries (
                id TEXT PRIMARY KEY,
                topic TEXT NOT NULL,
                statement TEXT NOT NULL,
                status TEXT NOT NULL,
                verification_basis TEXT NOT NULL CHECK(
                    verification_basis IN ('unverified', 'web_source', 'user_confirmed')
                ),
                confidence_basis_points INTEGER NOT NULL CHECK(confidence_basis_points BETWEEN 0 AND 10000),
                source_conversation_id TEXT NOT NULL,
                source_turn_id TEXT NOT NULL,
                supersedes_id TEXT REFERENCES knowledge_entries(id),
                created_at_ms INTEGER NOT NULL,
                updated_at_ms INTEGER NOT NULL
             );

             CREATE TABLE knowledge_sources (
                knowledge_id TEXT NOT NULL REFERENCES knowledge_entries(id),
                source_id TEXT NOT NULL,
                title TEXT NOT NULL,
                url TEXT NOT NULL,
                snippet TEXT NOT NULL,
                rank INTEGER NOT NULL,
                fetched_at_ms INTEGER NOT NULL,
                PRIMARY KEY(knowledge_id, source_id)
             );

             CREATE TABLE extraction_jobs (
                id TEXT PRIMARY KEY,
                conversation_id TEXT NOT NULL,
                turn_id TEXT NOT NULL,
                status TEXT NOT NULL,
                error_code TEXT,
                error_message TEXT,
                error_retryable INTEGER,
                created_at_ms INTEGER NOT NULL,
                updated_at_ms INTEGER NOT NULL
             );

             CREATE TABLE conversations (
                id TEXT PRIMARY KEY,
                character_id TEXT NOT NULL,
                created_at_ms INTEGER NOT NULL,
                updated_at_ms INTEGER NOT NULL
             );
             CREATE INDEX conversations_character_updated
                ON conversations(character_id, updated_at_ms DESC, id ASC);

             CREATE TABLE conversation_turns (
                id TEXT PRIMARY KEY,
                conversation_id TEXT NOT NULL REFERENCES conversations(id),
                sequence INTEGER NOT NULL CHECK(sequence > 0),
                status TEXT NOT NULL CHECK(status IN (
                    'interpreting', 'planning', 'responding', 'completed', 'interrupted', 'failed'
                )),
                error_code TEXT,
                error_message TEXT,
                error_retryable INTEGER,
                extraction_state TEXT NOT NULL DEFAULT 'ineligible' CHECK(extraction_state IN (
                    'ineligible', 'pending', 'claimed', 'processed'
                )),
                created_at_ms INTEGER NOT NULL,
                updated_at_ms INTEGER NOT NULL,
                UNIQUE(conversation_id, sequence)
             );

             CREATE TABLE conversation_messages (
                id TEXT PRIMARY KEY,
                conversation_id TEXT NOT NULL REFERENCES conversations(id),
                turn_id TEXT NOT NULL REFERENCES conversation_turns(id),
                sequence INTEGER NOT NULL CHECK(sequence > 0),
                role TEXT NOT NULL CHECK(role IN ('user', 'assistant')),
                content TEXT NOT NULL,
                created_at_ms INTEGER NOT NULL,
                UNIQUE(conversation_id, sequence),
                UNIQUE(turn_id, role)
             );

             CREATE TABLE prompt_windows (
                conversation_id TEXT PRIMARY KEY REFERENCES conversations(id),
                revision INTEGER NOT NULL CHECK(revision > 0),
                summary TEXT,
                cutoff_message_sequence INTEGER NOT NULL DEFAULT 0 CHECK(cutoff_message_sequence >= 0),
                updated_at_ms INTEGER NOT NULL
             );

             CREATE TABLE extraction_batches (
                id TEXT PRIMARY KEY,
                conversation_id TEXT NOT NULL REFERENCES conversations(id),
                character_id TEXT NOT NULL,
                status TEXT NOT NULL CHECK(status IN (
                    'pending', 'running', 'succeeded', 'failed', 'cancelled'
                )),
                first_turn_sequence INTEGER NOT NULL CHECK(first_turn_sequence > 0),
                last_turn_sequence INTEGER NOT NULL CHECK(last_turn_sequence >= first_turn_sequence),
                error_code TEXT,
                error_message TEXT,
                error_retryable INTEGER,
                created_at_ms INTEGER NOT NULL,
                updated_at_ms INTEGER NOT NULL
             );

             CREATE TABLE extraction_batch_turns (
                batch_id TEXT NOT NULL REFERENCES extraction_batches(id),
                turn_id TEXT NOT NULL REFERENCES conversation_turns(id),
                turn_sequence INTEGER NOT NULL CHECK(turn_sequence > 0),
                PRIMARY KEY(batch_id, turn_id),
                UNIQUE(batch_id, turn_sequence)
             );
             CREATE UNIQUE INDEX extraction_batches_one_running
                ON extraction_batches(conversation_id) WHERE status = 'running';

             CREATE VIRTUAL TABLE personal_memories_fts USING fts5(
                content,
                content = 'personal_memories',
                content_rowid = 'rowid',
                tokenize = 'trigram'
             );
             CREATE TRIGGER personal_memories_ai AFTER INSERT ON personal_memories BEGIN
                INSERT INTO personal_memories_fts(rowid, content) VALUES (new.rowid, new.content);
             END;
             CREATE TRIGGER personal_memories_ad AFTER DELETE ON personal_memories BEGIN
                INSERT INTO personal_memories_fts(personal_memories_fts, rowid, content)
                VALUES ('delete', old.rowid, old.content);
             END;
             CREATE TRIGGER personal_memories_au AFTER UPDATE OF content ON personal_memories BEGIN
                INSERT INTO personal_memories_fts(personal_memories_fts, rowid, content)
                VALUES ('delete', old.rowid, old.content);
                INSERT INTO personal_memories_fts(rowid, content) VALUES (new.rowid, new.content);
             END;

             CREATE VIRTUAL TABLE knowledge_entries_fts USING fts5(
                topic,
                statement,
                content = 'knowledge_entries',
                content_rowid = 'rowid',
                tokenize = 'trigram'
             );
             CREATE TRIGGER knowledge_entries_ai AFTER INSERT ON knowledge_entries BEGIN
                INSERT INTO knowledge_entries_fts(rowid, topic, statement)
                VALUES (new.rowid, new.topic, new.statement);
             END;
             CREATE TRIGGER knowledge_entries_ad AFTER DELETE ON knowledge_entries BEGIN
                INSERT INTO knowledge_entries_fts(knowledge_entries_fts, rowid, topic, statement)
                VALUES ('delete', old.rowid, old.topic, old.statement);
             END;
             CREATE TRIGGER knowledge_entries_au AFTER UPDATE OF topic, statement ON knowledge_entries BEGIN
                INSERT INTO knowledge_entries_fts(knowledge_entries_fts, rowid, topic, statement)
                VALUES ('delete', old.rowid, old.topic, old.statement);
                INSERT INTO knowledge_entries_fts(rowid, topic, statement)
                VALUES (new.rowid, new.topic, new.statement);
             END;",
        )
        .map_err(|_| storage_error("无法创建智能层 schema 或 FTS5 trigram"))?;
    transaction
        .commit()
        .map_err(|_| storage_error("无法提交智能层 schema 事务"))
}

fn migrate_schema_v1(connection: &mut Connection) -> Result<(), FairyError> {
    let transaction = connection
        .transaction()
        .map_err(|_| storage_error("无法开始智能层 v1 到 v2 迁移事务"))?;
    let invalid_verified: i64 = transaction
        .query_row(
            "SELECT COUNT(*)
             FROM knowledge_entries k
             WHERE k.status = 'verified'
               AND NOT EXISTS (
                    SELECT 1 FROM knowledge_sources s WHERE s.knowledge_id = k.id
               )",
            [],
            |row| row.get(0),
        )
        .map_err(|_| storage_error("无法校验 v1 已验证知识来源"))?;
    if invalid_verified != 0 {
        return Err(FairyError::new(
            ErrorCode::StorageCorrupted,
            "v1 数据库包含没有来源的已验证知识",
            false,
        ));
    }
    transaction
        .execute_batch(
            "ALTER TABLE knowledge_entries ADD COLUMN verification_basis TEXT NOT NULL
                DEFAULT 'unverified' CHECK(
                    verification_basis IN ('unverified', 'web_source', 'user_confirmed')
                );
             UPDATE knowledge_entries
             SET verification_basis = 'web_source'
             WHERE EXISTS (
                SELECT 1 FROM knowledge_sources s
                WHERE s.knowledge_id = knowledge_entries.id
             );
             UPDATE schema_meta SET version = 2 WHERE singleton = 1;",
        )
        .map_err(|_| storage_error("无法迁移智能层 schema v1 到 v2"))?;
    transaction
        .commit()
        .map_err(|_| storage_error("无法提交智能层 schema v2 迁移"))
}

fn migrate_schema_v2(connection: &mut Connection) -> Result<(), FairyError> {
    let transaction = connection
        .transaction()
        .map_err(|_| storage_error("无法开始智能层 v2 到 v3 迁移事务"))?;
    transaction
        .execute_batch(
            "ALTER TABLE personal_memories ADD COLUMN scope_kind TEXT NOT NULL
                DEFAULT 'global' CHECK(scope_kind IN ('global', 'character', 'unassigned_legacy'));
             ALTER TABLE personal_memories ADD COLUMN character_id TEXT;
             ALTER TABLE personal_memories ADD COLUMN review_status TEXT NOT NULL
                DEFAULT 'ready' CHECK(review_status IN ('ready', 'needs_review'));

             UPDATE personal_memories
             SET scope_kind = 'unassigned_legacy', review_status = 'needs_review'
             WHERE kind = 'relationship';

             CREATE TRIGGER personal_memories_scope_insert
             BEFORE INSERT ON personal_memories
             WHEN NOT (
                ((NEW.kind = 'relationship' AND NEW.scope_kind IN ('character', 'unassigned_legacy'))
                 OR (NEW.kind != 'relationship' AND NEW.scope_kind = 'global'))
                AND ((NEW.scope_kind = 'character' AND NEW.character_id IS NOT NULL)
                 OR (NEW.scope_kind != 'character' AND NEW.character_id IS NULL))
                AND ((NEW.scope_kind = 'unassigned_legacy' AND NEW.review_status = 'needs_review')
                 OR (NEW.scope_kind != 'unassigned_legacy' AND NEW.review_status = 'ready'))
             )
             BEGIN
                SELECT RAISE(ABORT, 'invalid personal memory scope');
             END;

             CREATE TRIGGER personal_memories_scope_update
             BEFORE UPDATE OF kind, scope_kind, character_id, review_status ON personal_memories
             WHEN NOT (
                ((NEW.kind = 'relationship' AND NEW.scope_kind IN ('character', 'unassigned_legacy'))
                 OR (NEW.kind != 'relationship' AND NEW.scope_kind = 'global'))
                AND ((NEW.scope_kind = 'character' AND NEW.character_id IS NOT NULL)
                 OR (NEW.scope_kind != 'character' AND NEW.character_id IS NULL))
                AND ((NEW.scope_kind = 'unassigned_legacy' AND NEW.review_status = 'needs_review')
                 OR (NEW.scope_kind != 'unassigned_legacy' AND NEW.review_status = 'ready'))
             )
             BEGIN
                SELECT RAISE(ABORT, 'invalid personal memory scope');
             END;

             CREATE TABLE conversations (
                id TEXT PRIMARY KEY,
                character_id TEXT NOT NULL,
                created_at_ms INTEGER NOT NULL,
                updated_at_ms INTEGER NOT NULL
             );
             CREATE INDEX conversations_character_updated
                ON conversations(character_id, updated_at_ms DESC, id ASC);

             CREATE TABLE conversation_turns (
                id TEXT PRIMARY KEY,
                conversation_id TEXT NOT NULL REFERENCES conversations(id),
                sequence INTEGER NOT NULL CHECK(sequence > 0),
                status TEXT NOT NULL CHECK(status IN (
                    'interpreting', 'planning', 'responding', 'completed', 'interrupted', 'failed'
                )),
                error_code TEXT,
                error_message TEXT,
                error_retryable INTEGER,
                extraction_state TEXT NOT NULL DEFAULT 'ineligible' CHECK(extraction_state IN (
                    'ineligible', 'pending', 'claimed', 'processed'
                )),
                created_at_ms INTEGER NOT NULL,
                updated_at_ms INTEGER NOT NULL,
                UNIQUE(conversation_id, sequence)
             );

             CREATE TABLE conversation_messages (
                id TEXT PRIMARY KEY,
                conversation_id TEXT NOT NULL REFERENCES conversations(id),
                turn_id TEXT NOT NULL REFERENCES conversation_turns(id),
                sequence INTEGER NOT NULL CHECK(sequence > 0),
                role TEXT NOT NULL CHECK(role IN ('user', 'assistant')),
                content TEXT NOT NULL,
                created_at_ms INTEGER NOT NULL,
                UNIQUE(conversation_id, sequence),
                UNIQUE(turn_id, role)
             );

             CREATE TABLE prompt_windows (
                conversation_id TEXT PRIMARY KEY REFERENCES conversations(id),
                revision INTEGER NOT NULL CHECK(revision > 0),
                summary TEXT,
                cutoff_message_sequence INTEGER NOT NULL DEFAULT 0 CHECK(cutoff_message_sequence >= 0),
                updated_at_ms INTEGER NOT NULL
             );

             CREATE TABLE extraction_batches (
                id TEXT PRIMARY KEY,
                conversation_id TEXT NOT NULL REFERENCES conversations(id),
                character_id TEXT NOT NULL,
                status TEXT NOT NULL CHECK(status IN (
                    'pending', 'running', 'succeeded', 'failed', 'cancelled'
                )),
                first_turn_sequence INTEGER NOT NULL CHECK(first_turn_sequence > 0),
                last_turn_sequence INTEGER NOT NULL CHECK(last_turn_sequence >= first_turn_sequence),
                error_code TEXT,
                error_message TEXT,
                error_retryable INTEGER,
                created_at_ms INTEGER NOT NULL,
                updated_at_ms INTEGER NOT NULL
             );

             CREATE TABLE extraction_batch_turns (
                batch_id TEXT NOT NULL REFERENCES extraction_batches(id),
                turn_id TEXT NOT NULL REFERENCES conversation_turns(id),
                turn_sequence INTEGER NOT NULL CHECK(turn_sequence > 0),
                PRIMARY KEY(batch_id, turn_id),
                UNIQUE(batch_id, turn_sequence)
             );
             CREATE UNIQUE INDEX extraction_batches_one_running
                ON extraction_batches(conversation_id) WHERE status = 'running';

             UPDATE schema_meta SET version = 3 WHERE singleton = 1;",
        )
        .map_err(|_| storage_error("无法迁移智能层 schema v2 到 v3"))?;
    transaction
        .commit()
        .map_err(|_| storage_error("无法提交智能层 schema v3 迁移"))
}

fn supersede_personal(
    transaction: &Transaction<'_>,
    id: PersonalMemoryId,
    now: i64,
) -> Result<(), FairyError> {
    let changed = transaction
        .execute(
            "UPDATE personal_memories SET status = 'superseded', updated_at_ms = ?2
             WHERE id = ?1 AND status = 'active'",
            params![id.to_string(), now],
        )
        .map_err(|_| storage_error("无法更新被取代的个人记忆"))?;
    if changed != 1 {
        return Err(invalid_record("被取代的个人记忆不存在或不是 active"));
    }
    Ok(())
}

fn supersede_knowledge(
    transaction: &Transaction<'_>,
    id: KnowledgeId,
    now: i64,
) -> Result<(), FairyError> {
    let changed = transaction
        .execute(
            "UPDATE knowledge_entries SET status = 'superseded', updated_at_ms = ?2
             WHERE id = ?1 AND status IN ('candidate', 'verified')",
            params![id.to_string(), now],
        )
        .map_err(|_| storage_error("无法更新被取代的知识"))?;
    if changed != 1 {
        return Err(invalid_record("被取代的知识不存在或不可更新"));
    }
    Ok(())
}

fn update_record_status(
    connection: &Connection,
    table: &str,
    id: &str,
    status: &str,
) -> Result<(), FairyError> {
    let sql = match table {
        "personal_memories" => {
            "UPDATE personal_memories SET status = ?2, updated_at_ms = ?3 WHERE id = ?1"
        }
        "knowledge_entries" => {
            "UPDATE knowledge_entries SET status = ?2, updated_at_ms = ?3 WHERE id = ?1"
        }
        _ => return Err(storage_error("未知智能层记录表")),
    };
    let changed = connection
        .execute(sql, params![id, status, now_unix_ms()?])
        .map_err(|_| storage_error("无法更新智能层记录状态"))?;
    if changed != 1 {
        return Err(invalid_record("智能层记录不存在"));
    }
    Ok(())
}

fn record_status(connection: &Connection, table: &str, id: &str) -> Result<String, FairyError> {
    let sql = match table {
        "personal_memories" => "SELECT status FROM personal_memories WHERE id = ?1",
        "knowledge_entries" => "SELECT status FROM knowledge_entries WHERE id = ?1",
        _ => return Err(storage_error("未知智能层记录表")),
    };
    connection
        .query_row(sql, [id], |row| row.get(0))
        .optional()
        .map_err(|_| storage_error("无法读取智能层记录状态"))?
        .ok_or_else(|| invalid_record("智能层记录不存在"))
}

fn count_where(connection: &Connection, table: &str, status: &str) -> Result<u64, FairyError> {
    let sql = match table {
        "personal_memories" => "SELECT COUNT(*) FROM personal_memories WHERE status = ?1",
        "knowledge_entries" => "SELECT COUNT(*) FROM knowledge_entries WHERE status = ?1",
        "extraction_batches" => "SELECT COUNT(*) FROM extraction_batches WHERE status = ?1",
        _ => return Err(storage_error("未知智能层统计表")),
    };
    let count: i64 = connection
        .query_row(sql, [status], |row| row.get(0))
        .map_err(|_| storage_error("无法读取智能层统计"))?;
    u64::try_from(count).map_err(|_| storage_error("智能层统计超出支持范围"))
}

fn count_all(connection: &Connection, table: &str) -> Result<u64, FairyError> {
    let sql = match table {
        "conversations" => "SELECT COUNT(*) FROM conversations",
        _ => return Err(storage_error("未知智能层全表统计")),
    };
    let count: i64 = connection
        .query_row(sql, [], |row| row.get(0))
        .map_err(|_| storage_error("无法读取智能层全表统计"))?;
    u64::try_from(count).map_err(|_| storage_error("智能层全表统计超出支持范围"))
}

fn count_memory_scope(
    connection: &Connection,
    scope_kind: &str,
    review_status: &str,
) -> Result<u64, FairyError> {
    let count: i64 = connection
        .query_row(
            "SELECT COUNT(*) FROM personal_memories
             WHERE status = 'active' AND scope_kind = ?1 AND review_status = ?2",
            params![scope_kind, review_status],
            |row| row.get(0),
        )
        .map_err(|_| storage_error("无法读取个人记忆 scope 统计"))?;
    u64::try_from(count).map_err(|_| storage_error("个人记忆 scope 统计超出支持范围"))
}

fn count_turn_extraction_state(
    connection: &Connection,
    extraction_state: &str,
) -> Result<u64, FairyError> {
    let count: i64 = connection
        .query_row(
            "SELECT COUNT(*) FROM conversation_turns WHERE extraction_state = ?1",
            [extraction_state],
            |row| row.get(0),
        )
        .map_err(|_| storage_error("无法读取 turn 抽取状态统计"))?;
    u64::try_from(count).map_err(|_| storage_error("turn 抽取状态统计超出支持范围"))
}

fn load_conversation_bootstrap(
    connection: &Connection,
    conversation_id: ConversationId,
) -> Result<ConversationBootstrap, FairyError> {
    let conversation = connection
        .query_row(
            "SELECT character_id, created_at_ms, updated_at_ms
             FROM conversations WHERE id = ?1",
            [conversation_id.to_string()],
            |row| {
                Ok((
                    row.get::<_, String>(0)?,
                    row.get::<_, i64>(1)?,
                    row.get::<_, i64>(2)?,
                ))
            },
        )
        .optional()
        .map_err(|_| storage_error("无法读取持久会话"))?
        .ok_or_else(|| invalid_record("持久会话不存在"))?;
    let mut statement = connection
        .prepare(
            "SELECT id, turn_id, sequence, role, content, created_at_ms
             FROM conversation_messages
             WHERE conversation_id = ?1
             ORDER BY sequence ASC",
        )
        .map_err(|_| storage_error("无法准备持久消息查询"))?;
    let rows = statement
        .query_map([conversation_id.to_string()], |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, String>(1)?,
                row.get::<_, i64>(2)?,
                row.get::<_, String>(3)?,
                row.get::<_, String>(4)?,
                row.get::<_, i64>(5)?,
            ))
        })
        .map_err(|_| storage_error("无法读取持久消息"))?;
    let mut messages = Vec::new();
    for row in rows {
        let (id, turn_id, sequence, role, content, created_at) =
            row.map_err(|_| storage_error("持久消息已损坏"))?;
        messages.push(ConversationMessageRecord {
            id: MessageId::from_str(&id).map_err(|_| storage_error("持久消息 id 已损坏"))?,
            conversation_id,
            turn_id: TurnId::from_str(&turn_id)
                .map_err(|_| storage_error("持久消息 turn id 已损坏"))?,
            sequence: i64_to_u64(sequence, "持久消息 sequence 已损坏")?,
            role: parse_message_role(&role)?,
            content,
            created_at_unix_ms: created_at,
        });
    }
    let (revision, summary, cutoff, window_updated): (i64, Option<String>, i64, i64) = connection
        .query_row(
            "SELECT revision, summary, cutoff_message_sequence, updated_at_ms
             FROM prompt_windows WHERE conversation_id = ?1",
            [conversation_id.to_string()],
            |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?, row.get(3)?)),
        )
        .map_err(|_| storage_error("持久 prompt window 已损坏"))?;
    let bootstrap = ConversationBootstrap {
        conversation: ConversationRecord {
            id: conversation_id,
            character_id: CharacterId::from_str(&conversation.0)
                .map_err(|_| storage_error("持久会话 character id 已损坏"))?,
            created_at_unix_ms: conversation.1,
            updated_at_unix_ms: conversation.2,
        },
        messages,
        prompt_window: PromptWindowRecord {
            conversation_id,
            revision: WindowRevision::new(i64_to_u64(revision, "window revision 已损坏")?)
                .ok_or_else(|| storage_error("window revision 不能为零"))?,
            summary,
            cutoff_message_sequence: i64_to_u64(cutoff, "window cutoff 已损坏")?,
            updated_at_unix_ms: window_updated,
        },
    };
    bootstrap.verify_integrity()?;
    Ok(bootstrap)
}

fn require_conversation(
    transaction: &Transaction<'_>,
    conversation_id: ConversationId,
) -> Result<(), FairyError> {
    let exists: bool = transaction
        .query_row(
            "SELECT EXISTS(SELECT 1 FROM conversations WHERE id = ?1)",
            [conversation_id.to_string()],
            |row| row.get(0),
        )
        .map_err(|_| storage_error("无法确认持久会话"))?;
    if exists {
        Ok(())
    } else {
        Err(invalid_record("持久会话不存在"))
    }
}

fn next_sequence(
    transaction: &Transaction<'_>,
    table: &str,
    conversation_id: ConversationId,
) -> Result<u64, FairyError> {
    let sql = match table {
        "conversation_turns" => {
            "SELECT COALESCE(MAX(sequence), 0) + 1 FROM conversation_turns WHERE conversation_id = ?1"
        }
        "conversation_messages" => {
            "SELECT COALESCE(MAX(sequence), 0) + 1 FROM conversation_messages WHERE conversation_id = ?1"
        }
        _ => return Err(storage_error("未知持久会话 sequence 表")),
    };
    let sequence: i64 = transaction
        .query_row(sql, [conversation_id.to_string()], |row| row.get(0))
        .map_err(|_| storage_error("无法分配持久会话 sequence"))?;
    i64_to_u64(sequence, "持久会话 sequence 已损坏")
}

fn touch_conversation(
    transaction: &Transaction<'_>,
    conversation_id: ConversationId,
    now: i64,
) -> Result<(), FairyError> {
    let changed = transaction
        .execute(
            "UPDATE conversations SET updated_at_ms = ?2 WHERE id = ?1",
            params![conversation_id.to_string(), now],
        )
        .map_err(|_| storage_error("无法更新持久会话时间"))?;
    if changed == 1 {
        Ok(())
    } else {
        Err(invalid_record("持久会话不存在"))
    }
}

fn parse_message_role(value: &str) -> Result<ConversationMessageRole, FairyError> {
    match value {
        "user" => Ok(ConversationMessageRole::User),
        "assistant" => Ok(ConversationMessageRole::Assistant),
        _ => Err(storage_error("持久消息 role 已损坏")),
    }
}

fn validate_conversation_content(value: &str) -> Result<(), FairyError> {
    if value.is_empty() || value.chars().any(|character| character == '\0') {
        Err(FairyError::new(
            ErrorCode::InvalidConversationRecord,
            "持久消息正文为空或包含 NUL",
            false,
        ))
    } else {
        Ok(())
    }
}

fn u64_to_i64(value: u64) -> Result<i64, FairyError> {
    i64::try_from(value).map_err(|_| storage_error("sequence 超出 SQLite 支持范围"))
}

fn i64_to_u64(value: i64, message: &'static str) -> Result<u64, FairyError> {
    u64::try_from(value).map_err(|_| storage_error(message))
}

fn memory_kind_name(kind: PersonalMemoryKind) -> &'static str {
    match kind {
        PersonalMemoryKind::Preference => "preference",
        PersonalMemoryKind::Profile => "profile",
        PersonalMemoryKind::Relationship => "relationship",
        PersonalMemoryKind::Experience => "experience",
    }
}

fn parse_memory_kind(value: &str) -> Result<PersonalMemoryKind, FairyError> {
    match value {
        "preference" => Ok(PersonalMemoryKind::Preference),
        "profile" => Ok(PersonalMemoryKind::Profile),
        "relationship" => Ok(PersonalMemoryKind::Relationship),
        "experience" => Ok(PersonalMemoryKind::Experience),
        _ => Err(storage_error("个人记忆 kind 已损坏")),
    }
}

fn validate_new_memory_scope(
    kind: PersonalMemoryKind,
    scope: MemoryScope,
) -> Result<(), FairyError> {
    scope.validate_for(kind)?;
    if scope == MemoryScope::UnassignedLegacy {
        return Err(invalid_record(
            "unassigned_legacy 仅用于迁移旧关系记忆，不能写入新记忆",
        ));
    }
    Ok(())
}

fn validate_mutation_character(
    mutation: &MemoryMutation,
    character_id: CharacterId,
) -> Result<(), FairyError> {
    let (kind, scope) = match mutation {
        MemoryMutation::Create { kind, scope, .. }
        | MemoryMutation::Supersede { kind, scope, .. } => (*kind, *scope),
    };
    scope.validate_for(kind)?;
    if kind == PersonalMemoryKind::Relationship
        && scope != (MemoryScope::Character { character_id })
    {
        return Err(invalid_record("relationship mutation 不属于当前角色"));
    }
    if scope == MemoryScope::UnassignedLegacy {
        return Err(invalid_record("自动抽取不能创建或修改待处理旧关系记忆"));
    }
    Ok(())
}

fn find_duplicate_memory(
    transaction: &Transaction<'_>,
    kind: PersonalMemoryKind,
    scope: MemoryScope,
    content: &str,
) -> Result<Option<PersonalMemoryId>, FairyError> {
    let (scope_kind, character_id) = memory_scope_columns(scope);
    let mut statement = transaction
        .prepare(
            "SELECT id, content FROM personal_memories
             WHERE kind = ?1 AND scope_kind = ?2
               AND character_id IS ?3 AND status = 'active' AND review_status = 'ready'
             ORDER BY updated_at_ms DESC, id ASC",
        )
        .map_err(|_| storage_error("无法准备重复记忆查询"))?;
    let rows = statement
        .query_map(
            params![memory_kind_name(kind), scope_kind, character_id],
            |row| Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?)),
        )
        .map_err(|_| storage_error("无法查询重复记忆"))?;
    let normalized = normalize_memory_content(content);
    for row in rows {
        let (id, existing) = row.map_err(|_| storage_error("重复记忆结果已损坏"))?;
        if normalize_memory_content(&existing) == normalized {
            return PersonalMemoryId::from_str(&id)
                .map(Some)
                .map_err(|_| storage_error("重复记忆 id 已损坏"));
        }
    }
    Ok(None)
}

fn require_active_memory_scope(
    transaction: &Transaction<'_>,
    memory_id: PersonalMemoryId,
    kind: PersonalMemoryKind,
    scope: MemoryScope,
) -> Result<(), FairyError> {
    let row: Option<(String, String, Option<String>, String)> = transaction
        .query_row(
            "SELECT kind, scope_kind, character_id, status
             FROM personal_memories WHERE id = ?1",
            [memory_id.to_string()],
            |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?, row.get(3)?)),
        )
        .optional()
        .map_err(|_| storage_error("无法读取 supersede 目标记忆"))?;
    let Some((actual_kind, scope_kind, character_id, status)) = row else {
        return Err(invalid_record("supersede 目标记忆不存在"));
    };
    if status != "active"
        || parse_memory_kind(&actual_kind)? != kind
        || parse_memory_scope(&scope_kind, character_id.as_deref())? != scope
    {
        return Err(invalid_record(
            "supersede 目标记忆状态、kind 或 scope 不匹配",
        ));
    }
    Ok(())
}

#[allow(clippy::too_many_arguments)]
fn insert_batch_memory(
    transaction: &Transaction<'_>,
    kind: PersonalMemoryKind,
    scope: MemoryScope,
    content: &str,
    confidence_basis_points: u16,
    conversation_id: ConversationId,
    source_turn_id: TurnId,
    supersedes_id: Option<PersonalMemoryId>,
    now: i64,
) -> Result<PersonalMemoryId, FairyError> {
    validate_content("个人记忆", content)?;
    validate_confidence(confidence_basis_points)?;
    validate_new_memory_scope(kind, scope)?;
    let id = PersonalMemoryId::new();
    let (scope_kind, character_id) = memory_scope_columns(scope);
    transaction
        .execute(
            "INSERT INTO personal_memories(
                id, kind, scope_kind, character_id, review_status, content, status,
                confidence_basis_points, source_conversation_id, source_turn_id,
                supersedes_id, created_at_ms, updated_at_ms
             ) VALUES (?1, ?2, ?3, ?4, 'ready', ?5, 'active', ?6, ?7, ?8, ?9, ?10, ?10)",
            params![
                id.to_string(),
                memory_kind_name(kind),
                scope_kind,
                character_id,
                content,
                i64::from(confidence_basis_points),
                conversation_id.to_string(),
                source_turn_id.to_string(),
                supersedes_id.map(|value| value.to_string()),
                now,
            ],
        )
        .map_err(|_| storage_error("无法提交抽取记忆"))?;
    Ok(id)
}

fn normalize_memory_content(content: &str) -> String {
    content.split_whitespace().collect::<Vec<_>>().join(" ")
}

fn memory_scope_columns(scope: MemoryScope) -> (&'static str, Option<String>) {
    match scope {
        MemoryScope::Global => ("global", None),
        MemoryScope::Character { character_id } => ("character", Some(character_id.to_string())),
        MemoryScope::UnassignedLegacy => ("unassigned_legacy", None),
    }
}

fn parse_memory_scope(
    scope_kind: &str,
    character_id: Option<&str>,
) -> Result<MemoryScope, FairyError> {
    match (scope_kind, character_id) {
        ("global", None) => Ok(MemoryScope::Global),
        ("character", Some(character_id)) => Ok(MemoryScope::Character {
            character_id: CharacterId::from_str(character_id)
                .map_err(|_| storage_error("个人记忆 character id 已损坏"))?,
        }),
        ("unassigned_legacy", None) => Ok(MemoryScope::UnassignedLegacy),
        _ => Err(storage_error("个人记忆 scope 已损坏")),
    }
}

fn knowledge_status_name(status: KnowledgeStatus) -> &'static str {
    match status {
        KnowledgeStatus::Candidate => "candidate",
        KnowledgeStatus::Verified => "verified",
        KnowledgeStatus::Superseded => "superseded",
        KnowledgeStatus::Tombstone => "tombstone",
    }
}

fn verification_basis_name(basis: KnowledgeVerificationBasis) -> &'static str {
    match basis {
        KnowledgeVerificationBasis::Unverified => "unverified",
        KnowledgeVerificationBasis::WebSource => "web_source",
        KnowledgeVerificationBasis::UserConfirmed => "user_confirmed",
    }
}

fn parse_verification_basis(value: &str) -> Result<KnowledgeVerificationBasis, FairyError> {
    match value {
        "unverified" => Ok(KnowledgeVerificationBasis::Unverified),
        "web_source" => Ok(KnowledgeVerificationBasis::WebSource),
        "user_confirmed" => Ok(KnowledgeVerificationBasis::UserConfirmed),
        _ => Err(storage_error("知识验证依据已损坏")),
    }
}

fn parse_personal_status(value: &str) -> Result<PersonalMemoryStatus, FairyError> {
    match value {
        "active" => Ok(PersonalMemoryStatus::Active),
        "superseded" => Ok(PersonalMemoryStatus::Superseded),
        "tombstone" => Ok(PersonalMemoryStatus::Tombstone),
        _ => Err(storage_error("个人记忆状态已损坏")),
    }
}

fn parse_review_status(value: &str) -> Result<PersonalMemoryReviewStatus, FairyError> {
    match value {
        "ready" => Ok(PersonalMemoryReviewStatus::Ready),
        "needs_review" => Ok(PersonalMemoryReviewStatus::NeedsReview),
        _ => Err(storage_error("个人记忆 review status 已损坏")),
    }
}

fn parse_knowledge_status(value: &str) -> Result<KnowledgeStatus, FairyError> {
    match value {
        "candidate" => Ok(KnowledgeStatus::Candidate),
        "verified" => Ok(KnowledgeStatus::Verified),
        "superseded" => Ok(KnowledgeStatus::Superseded),
        "tombstone" => Ok(KnowledgeStatus::Tombstone),
        _ => Err(storage_error("知识状态已损坏")),
    }
}

fn validate_content(label: &'static str, value: &str) -> Result<(), FairyError> {
    if value.is_empty()
        || value.trim() != value
        || value.chars().any(char::is_control)
        || value.chars().count() > 4000
    {
        return Err(invalid_record(format!("{label} 为空、超长或包含无效字符")));
    }
    Ok(())
}

fn validate_confidence(value: u16) -> Result<(), FairyError> {
    if value > 10_000 {
        return Err(invalid_record("置信度必须位于 0..=10000"));
    }
    Ok(())
}

fn now_unix_ms() -> Result<i64, FairyError> {
    let duration = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_err(|_| storage_error("系统时间早于 Unix epoch"))?;
    i64::try_from(duration.as_millis()).map_err(|_| storage_error("系统时间超出支持范围"))
}

fn invalid_record(message: impl Into<String>) -> FairyError {
    FairyError::new(ErrorCode::InvalidIntelligenceRecord, message, false)
}

fn storage_error(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::StorageIo, message, false)
}

#[cfg(test)]
mod tests;
