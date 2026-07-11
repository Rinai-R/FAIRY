use std::path::Path;
use std::str::FromStr;
use std::sync::{Mutex, MutexGuard};
use std::time::{SystemTime, UNIX_EPOCH};

use fairy_domain::{
    ErrorCode, ExtractionJobId, ExtractionJobStatus, FairyError, IntelligenceStoreSummary,
    KnowledgeCatalog, KnowledgeId, KnowledgeRecord, KnowledgeSourceId, KnowledgeStatus,
    KnowledgeVerificationBasis, NewKnowledge, NewPersonalMemory, PersonalMemoryId,
    PersonalMemoryKind, PersonalMemoryRecord, PersonalMemoryStatus, RetrievalContext,
    RetrievedKnowledge, RetrievedPersonalMemory,
};
use fairy_harness::CompanionIntelligence;
use rusqlite::{Connection, OptionalExtension, Transaction, params};

const SCHEMA_VERSION: i64 = 2;
const MAX_RESULTS_PER_KIND: usize = 4;
const MAX_RETRIEVED_CONTEXT_CHARS: usize = 2400;
const MAX_CATALOG_RESULTS_PER_STATUS: usize = 20;

pub struct IntelligenceStore {
    connection: Mutex<Connection>,
}

#[async_trait::async_trait]
impl CompanionIntelligence for IntelligenceStore {
    async fn retrieve(&self, query: String) -> Result<RetrievalContext, FairyError> {
        IntelligenceStore::retrieve(self, &query)
    }

    async fn create_extraction_job(
        &self,
        conversation_id: fairy_domain::ConversationId,
        turn_id: fairy_domain::TurnId,
    ) -> Result<ExtractionJobId, FairyError> {
        IntelligenceStore::create_extraction_job(self, conversation_id, turn_id)
    }

    async fn mark_extraction_running(&self, job_id: ExtractionJobId) -> Result<(), FairyError> {
        self.mark_job_running(job_id)
    }

    async fn commit_extraction(
        &self,
        job_id: ExtractionJobId,
        personal_memories: Vec<NewPersonalMemory>,
        knowledge: Vec<NewKnowledge>,
    ) -> Result<(), FairyError> {
        self.commit_extraction_batch(job_id, personal_memories, knowledge)
    }

    async fn fail_extraction_job(
        &self,
        job_id: ExtractionJobId,
        error: FairyError,
    ) -> Result<(), FairyError> {
        self.mark_job_failed(job_id, &error)
    }
}

impl IntelligenceStore {
    pub fn open(path: impl AsRef<Path>) -> Result<Self, FairyError> {
        let path = path.as_ref();
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent)
                .map_err(|_| storage_error("无法创建智能层数据库目录"))?;
        }
        let mut connection =
            Connection::open(path).map_err(|_| storage_error("无法打开智能层数据库"))?;
        connection
            .execute_batch("PRAGMA foreign_keys = ON;")
            .map_err(|_| storage_error("无法启用智能层数据库外键"))?;
        initialize_schema(&mut connection)?;
        cancel_interrupted_jobs(&mut connection)?;
        Ok(Self {
            connection: Mutex::new(connection),
        })
    }

    pub fn schema_version(&self) -> Result<i64, FairyError> {
        self.lock()?
            .query_row(
                "SELECT version FROM schema_meta WHERE singleton = 1",
                [],
                |row| row.get(0),
            )
            .map_err(|_| storage_error("无法读取智能层 schema 版本"))
    }

    pub fn summary(&self) -> Result<IntelligenceStoreSummary, FairyError> {
        let connection = self.lock()?;
        Ok(IntelligenceStoreSummary {
            active_personal_memories: count_where(&connection, "personal_memories", "active")?,
            candidate_knowledge: count_where(&connection, "knowledge_entries", "candidate")?,
            verified_knowledge: count_where(&connection, "knowledge_entries", "verified")?,
            pending_jobs: count_where(&connection, "extraction_jobs", "pending")?,
            running_jobs: count_where(&connection, "extraction_jobs", "running")?,
            failed_jobs: count_where(&connection, "extraction_jobs", "failed")?,
        })
    }

    pub fn append_personal_memory(
        &self,
        input: NewPersonalMemory,
    ) -> Result<PersonalMemoryRecord, FairyError> {
        validate_content("个人记忆", &input.content)?;
        validate_confidence(input.confidence_basis_points)?;
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
                    id, kind, content, status, confidence_basis_points,
                    source_conversation_id, source_turn_id, supersedes_id,
                    created_at_ms, updated_at_ms
                 ) VALUES (?1, ?2, ?3, 'active', ?4, ?5, ?6, ?7, ?8, ?8)",
                params![
                    id.to_string(),
                    memory_kind_name(input.kind),
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

    pub fn create_extraction_job(
        &self,
        conversation_id: fairy_domain::ConversationId,
        turn_id: fairy_domain::TurnId,
    ) -> Result<ExtractionJobId, FairyError> {
        let id = ExtractionJobId::new();
        let now = now_unix_ms()?;
        self.lock()?
            .execute(
                "INSERT INTO extraction_jobs (
                    id, conversation_id, turn_id, status, created_at_ms, updated_at_ms
                 ) VALUES (?1, ?2, ?3, 'pending', ?4, ?4)",
                params![
                    id.to_string(),
                    conversation_id.to_string(),
                    turn_id.to_string(),
                    now
                ],
            )
            .map_err(|_| storage_error("无法创建后台提取任务"))?;
        Ok(id)
    }

    pub fn mark_job_running(&self, id: ExtractionJobId) -> Result<(), FairyError> {
        let connection = self.lock()?;
        transition_job(&connection, id, "pending", "running", None)
    }

    pub fn mark_job_succeeded(&self, id: ExtractionJobId) -> Result<(), FairyError> {
        let connection = self.lock()?;
        transition_job(&connection, id, "running", "succeeded", None)
    }

    pub fn mark_job_failed(
        &self,
        id: ExtractionJobId,
        error: &FairyError,
    ) -> Result<(), FairyError> {
        let connection = self.lock()?;
        transition_job(&connection, id, "running", "failed", Some(error))
    }

    pub fn mark_job_cancelled(&self, id: ExtractionJobId) -> Result<(), FairyError> {
        let connection = self.lock()?;
        let now = now_unix_ms()?;
        let changed = connection
            .execute(
                "UPDATE extraction_jobs
                 SET status = 'cancelled', updated_at_ms = ?2
                 WHERE id = ?1 AND status IN ('pending', 'running')",
                params![id.to_string(), now],
            )
            .map_err(|_| storage_error("无法取消后台提取任务"))?;
        if changed != 1 {
            return Err(invalid_record("后台提取任务不能从当前状态取消"));
        }
        Ok(())
    }

    pub fn commit_extraction_batch(
        &self,
        job_id: ExtractionJobId,
        personal_memories: Vec<NewPersonalMemory>,
        knowledge: Vec<NewKnowledge>,
    ) -> Result<(), FairyError> {
        for memory in &personal_memories {
            validate_content("个人记忆", &memory.content)?;
            validate_confidence(memory.confidence_basis_points)?;
        }
        for entry in &knowledge {
            validate_content("知识主题", &entry.topic)?;
            validate_content("知识陈述", &entry.statement)?;
            validate_confidence(entry.confidence_basis_points)?;
            for source in &entry.sources {
                validate_content("知识来源标题", &source.title)?;
                validate_content("知识来源 URL", &source.url)?;
                validate_content("知识来源摘要", &source.snippet)?;
            }
        }

        let now = now_unix_ms()?;
        let mut connection = self.lock()?;
        let transaction = connection
            .transaction()
            .map_err(|_| storage_error("无法开始后台提取提交事务"))?;
        let job_status: Option<String> = transaction
            .query_row(
                "SELECT status FROM extraction_jobs WHERE id = ?1",
                [job_id.to_string()],
                |row| row.get(0),
            )
            .optional()
            .map_err(|_| storage_error("无法读取后台提取任务状态"))?;
        if job_status.as_deref() != Some("running") {
            return Err(invalid_record("只有 running 提取任务可以提交"));
        }

        for memory in personal_memories {
            let id = PersonalMemoryId::new();
            if let Some(previous_id) = memory.supersedes_id {
                supersede_personal(&transaction, previous_id, now)?;
            }
            transaction
                .execute(
                    "INSERT INTO personal_memories (
                        id, kind, content, status, confidence_basis_points,
                        source_conversation_id, source_turn_id, supersedes_id,
                        created_at_ms, updated_at_ms
                     ) VALUES (?1, ?2, ?3, 'active', ?4, ?5, ?6, ?7, ?8, ?8)",
                    params![
                        id.to_string(),
                        memory_kind_name(memory.kind),
                        memory.content,
                        i64::from(memory.confidence_basis_points),
                        memory.source_conversation_id.to_string(),
                        memory.source_turn_id.to_string(),
                        memory.supersedes_id.map(|value| value.to_string()),
                        now,
                    ],
                )
                .map_err(|_| storage_error("无法提交提取的个人记忆"))?;
        }

        for entry in knowledge {
            let id = KnowledgeId::new();
            let status = if entry.sources.is_empty() {
                KnowledgeStatus::Candidate
            } else {
                KnowledgeStatus::Verified
            };
            let verification_basis = if entry.sources.is_empty() {
                KnowledgeVerificationBasis::Unverified
            } else {
                KnowledgeVerificationBasis::WebSource
            };
            if let Some(previous_id) = entry.supersedes_id {
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
                        entry.topic,
                        entry.statement,
                        knowledge_status_name(status),
                        verification_basis_name(verification_basis),
                        i64::from(entry.confidence_basis_points),
                        entry.source_conversation_id.to_string(),
                        entry.source_turn_id.to_string(),
                        entry.supersedes_id.map(|value| value.to_string()),
                        now,
                    ],
                )
                .map_err(|_| storage_error("无法提交提取的知识"))?;
            for source in entry.sources {
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
                    .map_err(|_| storage_error("无法提交提取的知识来源"))?;
            }
        }

        let changed = transaction
            .execute(
                "UPDATE extraction_jobs
                 SET status = 'succeeded', updated_at_ms = ?2
                 WHERE id = ?1 AND status = 'running'",
                params![job_id.to_string(), now],
            )
            .map_err(|_| storage_error("无法完成后台提取任务"))?;
        if changed != 1 {
            return Err(invalid_record("后台提取任务状态在提交时发生变化"));
        }
        transaction
            .commit()
            .map_err(|_| storage_error("无法提交后台提取事务"))
    }

    pub fn job_status(&self, id: ExtractionJobId) -> Result<ExtractionJobStatus, FairyError> {
        let status: String = self
            .lock()?
            .query_row(
                "SELECT status FROM extraction_jobs WHERE id = ?1",
                [id.to_string()],
                |row| row.get(0),
            )
            .optional()
            .map_err(|_| storage_error("无法读取后台提取任务"))?
            .ok_or_else(|| invalid_record("后台提取任务不存在"))?;
        parse_job_status(&status)
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

    pub fn retrieve(&self, query: &str) -> Result<RetrievalContext, FairyError> {
        let Some(fts_query) = build_fts_query(query)? else {
            return Ok(RetrievalContext::default());
        };
        let connection = self.lock()?;
        let mut remaining_chars = MAX_RETRIEVED_CONTEXT_CHARS;
        let personal_memories = retrieve_personal(&connection, &fts_query, &mut remaining_chars)?;
        let knowledge = retrieve_knowledge(&connection, &fts_query, &mut remaining_chars)?;
        Ok(RetrievalContext {
            personal_memories,
            knowledge,
        })
    }

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
    fts_query: &str,
    remaining_chars: &mut usize,
) -> Result<Vec<RetrievedPersonalMemory>, FairyError> {
    let mut statement = connection
        .prepare(
            "SELECT p.id, p.kind, p.content, p.confidence_basis_points, p.updated_at_ms
             FROM personal_memories_fts f
             JOIN personal_memories p ON p.rowid = f.rowid
             WHERE personal_memories_fts MATCH ?1 AND p.status = 'active'
             ORDER BY bm25(personal_memories_fts) ASC,
                      p.confidence_basis_points DESC,
                      p.updated_at_ms DESC,
                      p.id ASC
             LIMIT ?2",
        )
        .map_err(|_| storage_error("无法准备个人记忆检索"))?;
    let rows = statement
        .query_map(params![fts_query, MAX_RESULTS_PER_KIND as i64], |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, String>(1)?,
                row.get::<_, String>(2)?,
                row.get::<_, i64>(3)?,
                row.get::<_, i64>(4)?,
            ))
        })
        .map_err(|_| storage_error("无法执行个人记忆检索"))?;
    let mut records = Vec::new();
    for row in rows {
        let (id, kind, content, confidence, updated_at) =
            row.map_err(|_| storage_error("个人记忆检索结果已损坏"))?;
        let length = content.chars().count();
        if length > *remaining_chars {
            continue;
        }
        *remaining_chars -= length;
        records.push(RetrievedPersonalMemory {
            id: PersonalMemoryId::from_str(&id).map_err(|_| storage_error("个人记忆 id 已损坏"))?,
            kind: parse_memory_kind(&kind)?,
            content,
            confidence_basis_points: u16::try_from(confidence)
                .map_err(|_| storage_error("个人记忆置信度已损坏"))?,
            updated_at_unix_ms: updated_at,
        });
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
            1 => return migrate_schema_v1(connection),
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
             INSERT INTO schema_meta(singleton, version) VALUES (1, 2);

             CREATE TABLE personal_memories (
                id TEXT PRIMARY KEY,
                kind TEXT NOT NULL,
                content TEXT NOT NULL,
                status TEXT NOT NULL,
                confidence_basis_points INTEGER NOT NULL CHECK(confidence_basis_points BETWEEN 0 AND 10000),
                source_conversation_id TEXT NOT NULL,
                source_turn_id TEXT NOT NULL,
                supersedes_id TEXT REFERENCES personal_memories(id),
                created_at_ms INTEGER NOT NULL,
                updated_at_ms INTEGER NOT NULL
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

fn transition_job(
    connection: &Connection,
    id: ExtractionJobId,
    expected: &str,
    next: &str,
    error: Option<&FairyError>,
) -> Result<(), FairyError> {
    let now = now_unix_ms()?;
    let changed = connection
        .execute(
            "UPDATE extraction_jobs SET
                status = ?3,
                error_code = ?4,
                error_message = ?5,
                error_retryable = ?6,
                updated_at_ms = ?7
             WHERE id = ?1 AND status = ?2",
            params![
                id.to_string(),
                expected,
                next,
                error.map(|value| value.code.as_str()),
                error.map(|value| value.message.as_str()),
                error.map(|value| i64::from(value.retryable)),
                now,
            ],
        )
        .map_err(|_| storage_error("无法更新后台提取任务"))?;
    if changed != 1 {
        return Err(invalid_record("后台提取任务状态转换无效"));
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
        "extraction_jobs" => "SELECT COUNT(*) FROM extraction_jobs WHERE status = ?1",
        _ => return Err(storage_error("未知智能层统计表")),
    };
    let count: i64 = connection
        .query_row(sql, [status], |row| row.get(0))
        .map_err(|_| storage_error("无法读取智能层统计"))?;
    u64::try_from(count).map_err(|_| storage_error("智能层统计超出支持范围"))
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

fn parse_knowledge_status(value: &str) -> Result<KnowledgeStatus, FairyError> {
    match value {
        "candidate" => Ok(KnowledgeStatus::Candidate),
        "verified" => Ok(KnowledgeStatus::Verified),
        "superseded" => Ok(KnowledgeStatus::Superseded),
        "tombstone" => Ok(KnowledgeStatus::Tombstone),
        _ => Err(storage_error("知识状态已损坏")),
    }
}

fn parse_job_status(value: &str) -> Result<ExtractionJobStatus, FairyError> {
    match value {
        "pending" => Ok(ExtractionJobStatus::Pending),
        "running" => Ok(ExtractionJobStatus::Running),
        "succeeded" => Ok(ExtractionJobStatus::Succeeded),
        "failed" => Ok(ExtractionJobStatus::Failed),
        "cancelled" => Ok(ExtractionJobStatus::Cancelled),
        _ => Err(storage_error("后台提取任务状态已损坏")),
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
mod tests {
    use fairy_domain::{AssistantSource, ConversationId, TurnId};
    use tempfile::tempdir;

    use super::*;

    fn personal(supersedes_id: Option<PersonalMemoryId>) -> NewPersonalMemory {
        personal_with(
            if supersedes_id.is_some() {
                "用户现在更喜欢咖啡"
            } else {
                "用户喜欢红茶"
            },
            supersedes_id,
        )
    }

    fn personal_with(content: &str, supersedes_id: Option<PersonalMemoryId>) -> NewPersonalMemory {
        NewPersonalMemory {
            kind: PersonalMemoryKind::Preference,
            content: content.to_owned(),
            confidence_basis_points: 9000,
            source_conversation_id: ConversationId::new(),
            source_turn_id: TurnId::new(),
            supersedes_id,
        }
    }

    fn knowledge(with_source: bool) -> NewKnowledge {
        NewKnowledge {
            topic: "Rust".to_owned(),
            statement: "Rust 具有所有权系统".to_owned(),
            confidence_basis_points: 9500,
            source_conversation_id: ConversationId::new(),
            source_turn_id: TurnId::new(),
            supersedes_id: None,
            sources: with_source
                .then(|| AssistantSource {
                    title: "Rust Book".to_owned(),
                    url: "https://doc.rust-lang.org/book/".to_owned(),
                    snippet: "Ownership is a set of rules.".to_owned(),
                    rank: 1,
                    fetched_at_unix_ms: 42,
                })
                .into_iter()
                .collect(),
        }
    }

    #[test]
    fn first_open_creates_schema_transactionally_and_reopens() {
        let directory = tempdir().expect("create intelligence tempdir");
        let path = directory.path().join("intelligence.sqlite3");
        let store = IntelligenceStore::open(&path).expect("create intelligence database");
        assert_eq!(store.schema_version().expect("schema version"), 2);
        drop(store);
        let reopened = IntelligenceStore::open(&path).expect("reopen intelligence database");
        assert_eq!(reopened.schema_version().expect("reopened version"), 2);
    }

    #[test]
    fn unknown_newer_schema_is_not_rebuilt_or_deleted() {
        let directory = tempdir().expect("create intelligence tempdir");
        let path = directory.path().join("future.sqlite3");
        let connection = Connection::open(&path).expect("open future fixture");
        connection
            .execute_batch(
                "CREATE TABLE schema_meta (
                    singleton INTEGER PRIMARY KEY,
                    version INTEGER NOT NULL
                 );
                 INSERT INTO schema_meta VALUES (1, 99);
                 CREATE TABLE user_future_data(value TEXT);
                 INSERT INTO user_future_data VALUES ('keep-me');",
            )
            .expect("seed future schema");
        drop(connection);

        let error = match IntelligenceStore::open(&path) {
            Ok(_) => panic!("future schema must fail"),
            Err(error) => error,
        };
        assert_eq!(error.code, ErrorCode::StorageCorrupted);
        let connection = Connection::open(path).expect("reopen future fixture");
        let value: String = connection
            .query_row("SELECT value FROM user_future_data", [], |row| row.get(0))
            .expect("future data remains");
        assert_eq!(value, "keep-me");
    }

    #[test]
    fn personal_memory_and_knowledge_use_distinct_append_only_evolution() {
        let directory = tempdir().expect("create intelligence tempdir");
        let store =
            IntelligenceStore::open(directory.path().join("store.sqlite3")).expect("create store");
        let first = store
            .append_personal_memory(personal(None))
            .expect("append first memory");
        let second = store
            .append_personal_memory(personal(Some(first.id)))
            .expect("append superseding memory");
        let candidate = store
            .append_knowledge(knowledge(false))
            .expect("append candidate knowledge");
        let verified = store
            .append_knowledge(knowledge(true))
            .expect("append verified knowledge");

        assert_ne!(first.id, second.id);
        assert_eq!(
            store.personal_memory_status(first.id).expect("old status"),
            PersonalMemoryStatus::Superseded
        );
        assert_eq!(
            store.personal_memory_status(second.id).expect("new status"),
            PersonalMemoryStatus::Active
        );
        assert_eq!(candidate.status, KnowledgeStatus::Candidate);
        assert_eq!(
            candidate.verification_basis,
            KnowledgeVerificationBasis::Unverified
        );
        assert_eq!(verified.status, KnowledgeStatus::Verified);
        assert_eq!(
            verified.verification_basis,
            KnowledgeVerificationBasis::WebSource
        );
        assert_eq!(verified.sources.len(), 1);
    }

    #[test]
    fn schema_v1_migrates_knowledge_basis_without_losing_records() {
        let directory = tempdir().expect("create intelligence tempdir");
        let path = directory.path().join("schema-v1.sqlite3");
        let store = IntelligenceStore::open(&path).expect("create v2 store");
        let candidate = store
            .append_knowledge(knowledge(false))
            .expect("append candidate");
        let verified = store
            .append_knowledge(knowledge(true))
            .expect("append verified");
        let job = store
            .create_extraction_job(ConversationId::new(), TurnId::new())
            .expect("create job");
        store.mark_job_running(job).expect("mark job running");
        store
            .mark_job_failed(
                job,
                &FairyError::new(
                    ErrorCode::IntelligenceExtractionFailed,
                    "test failure",
                    false,
                ),
            )
            .expect("mark job failed");
        drop(store);

        let connection = Connection::open(&path).expect("open migration fixture");
        connection
            .execute_batch(
                "ALTER TABLE knowledge_entries DROP COLUMN verification_basis;
                 UPDATE schema_meta SET version = 1 WHERE singleton = 1;",
            )
            .expect("downgrade fixture to v1 shape");
        drop(connection);

        let migrated = IntelligenceStore::open(&path).expect("migrate schema v1");
        assert_eq!(migrated.schema_version().expect("schema version"), 2);
        assert_eq!(
            migrated
                .knowledge_status(candidate.id)
                .expect("candidate status"),
            KnowledgeStatus::Candidate
        );
        assert_eq!(
            migrated
                .knowledge_status(verified.id)
                .expect("verified status"),
            KnowledgeStatus::Verified
        );
        assert_eq!(
            migrated.job_status(job).expect("job status"),
            ExtractionJobStatus::Failed
        );
        drop(migrated);

        let connection = Connection::open(&path).expect("inspect migrated database");
        let candidate_basis: String = connection
            .query_row(
                "SELECT verification_basis FROM knowledge_entries WHERE id = ?1",
                [candidate.id.to_string()],
                |row| row.get(0),
            )
            .expect("candidate basis");
        let verified_basis: String = connection
            .query_row(
                "SELECT verification_basis FROM knowledge_entries WHERE id = ?1",
                [verified.id.to_string()],
                |row| row.get(0),
            )
            .expect("verified basis");
        let source_count: i64 = connection
            .query_row(
                "SELECT COUNT(*) FROM knowledge_sources WHERE knowledge_id = ?1",
                [verified.id.to_string()],
                |row| row.get(0),
            )
            .expect("source count");
        assert_eq!(candidate_basis, "unverified");
        assert_eq!(verified_basis, "web_source");
        assert_eq!(source_count, 1);
    }

    #[test]
    fn schema_v1_verified_without_source_fails_and_rolls_back_migration() {
        let directory = tempdir().expect("create intelligence tempdir");
        let path = directory.path().join("invalid-schema-v1.sqlite3");
        let store = IntelligenceStore::open(&path).expect("create v2 store");
        let candidate = store
            .append_knowledge(knowledge(false))
            .expect("append candidate");
        drop(store);

        let connection = Connection::open(&path).expect("open invalid fixture");
        connection
            .execute(
                "UPDATE knowledge_entries SET status = 'verified' WHERE id = ?1",
                [candidate.id.to_string()],
            )
            .expect("create invalid verified row");
        connection
            .execute_batch(
                "ALTER TABLE knowledge_entries DROP COLUMN verification_basis;
                 UPDATE schema_meta SET version = 1 WHERE singleton = 1;",
            )
            .expect("downgrade invalid fixture");
        drop(connection);

        let error = match IntelligenceStore::open(&path) {
            Ok(_) => panic!("invalid v1 database must not migrate"),
            Err(error) => error,
        };
        assert_eq!(error.code, ErrorCode::StorageCorrupted);
        let connection = Connection::open(&path).expect("inspect rolled back fixture");
        let version: i64 = connection
            .query_row(
                "SELECT version FROM schema_meta WHERE singleton = 1",
                [],
                |row| row.get(0),
            )
            .expect("schema version after rollback");
        assert_eq!(version, 1);
    }

    #[test]
    fn knowledge_catalog_confirmation_and_tombstone_are_explicit() {
        let directory = tempdir().expect("create intelligence tempdir");
        let store = IntelligenceStore::open(directory.path().join("catalog.sqlite3"))
            .expect("create store");
        let mut candidate_input = knowledge(false);
        candidate_input.topic = "用户确认主题".to_owned();
        candidate_input.statement = "这是一条等待用户确认的知识".to_owned();
        let candidate = store
            .append_knowledge(candidate_input)
            .expect("append candidate");
        let mut web_input = knowledge(true);
        web_input.topic = "网页验证主题".to_owned();
        web_input.statement = "这是一条网页验证知识".to_owned();
        let web_verified = store
            .append_knowledge(web_input)
            .expect("append web knowledge");

        let initial = store.knowledge_catalog().expect("initial catalog");
        assert_eq!(initial.candidates.len(), 1);
        assert_eq!(initial.verified.len(), 1);
        assert_eq!(
            initial.verified[0].verification_basis,
            KnowledgeVerificationBasis::WebSource
        );

        let confirmed = store
            .confirm_knowledge_candidate(candidate.id)
            .expect("confirm candidate");
        assert_eq!(confirmed.status, KnowledgeStatus::Verified);
        assert_eq!(
            confirmed.verification_basis,
            KnowledgeVerificationBasis::UserConfirmed
        );
        assert!(confirmed.sources.is_empty());
        let retrieved = store.retrieve("等待用户确认").expect("retrieve confirmed");
        assert_eq!(retrieved.knowledge.len(), 1);
        assert_eq!(
            retrieved.knowledge[0].verification_basis,
            KnowledgeVerificationBasis::UserConfirmed
        );
        assert_eq!(
            store
                .confirm_knowledge_candidate(candidate.id)
                .expect_err("repeat confirmation must fail")
                .code,
            ErrorCode::InvalidIntelligenceRecord
        );

        store
            .tombstone_knowledge(web_verified.id)
            .expect("tombstone web knowledge");
        assert_eq!(
            store
                .tombstone_knowledge(web_verified.id)
                .expect_err("repeat tombstone must fail")
                .code,
            ErrorCode::InvalidIntelligenceRecord
        );
        let catalog = store.knowledge_catalog().expect("catalog after changes");
        assert!(catalog.candidates.is_empty());
        assert_eq!(catalog.verified.len(), 1);
        assert_eq!(catalog.verified[0].id, candidate.id);
        assert!(
            store
                .retrieve("网页验证知识")
                .expect("retrieve tombstoned topic")
                .knowledge
                .is_empty()
        );
        let source_count: i64 = store
            .lock()
            .expect("lock store")
            .query_row(
                "SELECT COUNT(*) FROM knowledge_sources WHERE knowledge_id = ?1",
                [web_verified.id.to_string()],
                |row| row.get(0),
            )
            .expect("count retained sources");
        assert_eq!(source_count, 1);
    }

    #[test]
    fn knowledge_catalog_is_bounded_deterministic_and_does_not_hide_query_failure() {
        let directory = tempdir().expect("create intelligence tempdir");
        let store = IntelligenceStore::open(directory.path().join("bounded-catalog.sqlite3"))
            .expect("create store");
        for index in 0..21 {
            let mut input = knowledge(false);
            input.topic = format!("候选主题 {index:02}");
            input.statement = format!("候选知识正文 {index:02}");
            store.append_knowledge(input).expect("append candidate");
        }
        let first = store.knowledge_catalog().expect("first catalog");
        let second = store.knowledge_catalog().expect("second catalog");
        assert_eq!(first, second);
        assert_eq!(first.candidates.len(), MAX_CATALOG_RESULTS_PER_STATUS);

        store
            .lock()
            .expect("lock store")
            .execute_batch("ALTER TABLE knowledge_entries RENAME TO knowledge_entries_broken;")
            .expect("break catalog table");
        assert_eq!(
            store
                .knowledge_catalog()
                .expect_err("query failure must stay explicit")
                .code,
            ErrorCode::StorageIo
        );
    }

    #[test]
    fn extraction_job_transitions_are_explicit() {
        let directory = tempdir().expect("create intelligence tempdir");
        let store =
            IntelligenceStore::open(directory.path().join("jobs.sqlite3")).expect("create store");
        let job = store
            .create_extraction_job(ConversationId::new(), TurnId::new())
            .expect("create job");
        assert_eq!(
            store.job_status(job).expect("pending status"),
            ExtractionJobStatus::Pending
        );
        store.mark_job_running(job).expect("mark running");
        store
            .mark_job_failed(
                job,
                &FairyError::new(
                    ErrorCode::IntelligenceExtractionFailed,
                    "严格 JSON 无效",
                    false,
                ),
            )
            .expect("mark failed");
        assert_eq!(
            store.job_status(job).expect("failed status"),
            ExtractionJobStatus::Failed
        );
        assert!(store.mark_job_succeeded(job).is_err());
    }

    #[test]
    fn reopening_cancels_jobs_interrupted_by_the_previous_process() {
        let directory = tempdir().expect("create intelligence tempdir");
        let path = directory.path().join("interrupted-jobs.sqlite3");
        let store = IntelligenceStore::open(&path).expect("create store");
        let pending = store
            .create_extraction_job(ConversationId::new(), TurnId::new())
            .expect("create pending job");
        let running = store
            .create_extraction_job(ConversationId::new(), TurnId::new())
            .expect("create running job");
        store.mark_job_running(running).expect("mark running");
        drop(store);

        let reopened = IntelligenceStore::open(&path).expect("reopen store");
        assert_eq!(
            reopened.job_status(pending).expect("pending job status"),
            ExtractionJobStatus::Cancelled
        );
        assert_eq!(
            reopened.job_status(running).expect("running job status"),
            ExtractionJobStatus::Cancelled
        );
        let summary = reopened.summary().expect("summary after recovery");
        assert_eq!(summary.pending_jobs, 0);
        assert_eq!(summary.running_jobs, 0);
        assert_eq!(summary.active_personal_memories, 0);
        assert_eq!(summary.candidate_knowledge, 0);
        assert_eq!(summary.verified_knowledge, 0);
    }

    #[test]
    fn extraction_batch_commits_candidates_and_job_in_one_transaction() {
        let directory = tempdir().expect("create intelligence tempdir");
        let store =
            IntelligenceStore::open(directory.path().join("batch.sqlite3")).expect("create store");
        let conversation_id = ConversationId::new();
        let turn_id = TurnId::new();
        let job = store
            .create_extraction_job(conversation_id, turn_id)
            .expect("create job");
        store.mark_job_running(job).expect("mark running");
        store
            .commit_extraction_batch(
                job,
                vec![NewPersonalMemory {
                    kind: PersonalMemoryKind::Preference,
                    content: "用户喜欢清爽柠檬饮料".to_owned(),
                    confidence_basis_points: 9000,
                    source_conversation_id: conversation_id,
                    source_turn_id: turn_id,
                    supersedes_id: None,
                }],
                vec![NewKnowledge {
                    topic: "柠檬饮料".to_owned(),
                    statement: "用户提到但尚未验证的客观陈述".to_owned(),
                    confidence_basis_points: 7000,
                    source_conversation_id: conversation_id,
                    source_turn_id: turn_id,
                    supersedes_id: None,
                    sources: Vec::new(),
                }],
            )
            .expect("commit extraction batch");

        assert_eq!(
            store.job_status(job).expect("succeeded job"),
            ExtractionJobStatus::Succeeded
        );
        let memory = store
            .retrieve("清爽柠檬饮料")
            .expect("retrieve extracted memory");
        assert_eq!(memory.personal_memories.len(), 1);
        assert!(memory.knowledge.is_empty());
    }

    #[test]
    fn invalid_extraction_batch_writes_no_partial_records() {
        let directory = tempdir().expect("create intelligence tempdir");
        let store = IntelligenceStore::open(directory.path().join("batch-rollback.sqlite3"))
            .expect("create store");
        let conversation_id = ConversationId::new();
        let turn_id = TurnId::new();
        let job = store
            .create_extraction_job(conversation_id, turn_id)
            .expect("create job");
        store.mark_job_running(job).expect("mark running");
        let error = store
            .commit_extraction_batch(
                job,
                vec![NewPersonalMemory {
                    kind: PersonalMemoryKind::Preference,
                    content: "用户喜欢红茶".to_owned(),
                    confidence_basis_points: 9000,
                    source_conversation_id: conversation_id,
                    source_turn_id: turn_id,
                    supersedes_id: None,
                }],
                vec![NewKnowledge {
                    topic: " 无效首尾空白".to_owned(),
                    statement: "不会写入".to_owned(),
                    confidence_basis_points: 7000,
                    source_conversation_id: conversation_id,
                    source_turn_id: turn_id,
                    supersedes_id: None,
                    sources: Vec::new(),
                }],
            )
            .expect_err("invalid batch must fail");
        assert_eq!(error.code, ErrorCode::InvalidIntelligenceRecord);
        assert_eq!(
            store.job_status(job).expect("job remains running"),
            ExtractionJobStatus::Running
        );
        let count: i64 = store
            .lock()
            .expect("connection")
            .query_row("SELECT COUNT(*) FROM personal_memories", [], |row| {
                row.get(0)
            })
            .expect("count memories");
        assert_eq!(count, 0);
    }

    #[test]
    fn invalid_supersedes_rolls_back_new_record() {
        let directory = tempdir().expect("create intelligence tempdir");
        let path = directory.path().join("rollback.sqlite3");
        let store = IntelligenceStore::open(&path).expect("create store");
        let error = store
            .append_personal_memory(personal(Some(PersonalMemoryId::new())))
            .expect_err("missing previous record must fail");
        assert_eq!(error.code, ErrorCode::InvalidIntelligenceRecord);
        let count: i64 = store
            .lock()
            .expect("connection")
            .query_row("SELECT COUNT(*) FROM personal_memories", [], |row| {
                row.get(0)
            })
            .expect("count memories");
        assert_eq!(count, 0);
    }

    #[test]
    fn trigram_retrieval_filters_status_and_is_deterministic() {
        let directory = tempdir().expect("create intelligence tempdir");
        let store = IntelligenceStore::open(directory.path().join("retrieval.sqlite3"))
            .expect("create store");
        let relevant = store
            .append_personal_memory(personal_with("用户不喜欢太甜的饮料", None))
            .expect("append relevant memory");
        store
            .append_personal_memory(personal_with("用户周末喜欢散步", None))
            .expect("append unrelated memory");
        let old = store
            .append_personal_memory(personal_with("用户以前喜欢红茶饮料", None))
            .expect("append old preference");
        store
            .append_personal_memory(personal_with("用户现在更喜欢咖啡", Some(old.id)))
            .expect("supersede old preference");
        store
            .append_knowledge(knowledge(false))
            .expect("append candidate knowledge");
        let verified = store
            .append_knowledge(knowledge(true))
            .expect("append verified knowledge");

        let first = store
            .retrieve("太甜的饮料推荐")
            .expect("retrieve personal memory");
        let second = store.retrieve("太甜的饮料推荐").expect("repeat retrieval");
        assert_eq!(first, second);
        assert_eq!(first.personal_memories.len(), 1);
        assert_eq!(first.personal_memories[0].id, relevant.id);
        assert!(first.knowledge.is_empty());

        let knowledge = store
            .retrieve("Rust所有权系统")
            .expect("retrieve verified knowledge");
        assert_eq!(knowledge.knowledge.len(), 1);
        assert_eq!(knowledge.knowledge[0].id, verified.id);
        assert_eq!(knowledge.knowledge[0].sources.len(), 1);
    }

    #[test]
    fn retrieval_has_per_kind_limit_and_short_queries_have_no_fake_fallback() {
        let directory = tempdir().expect("create intelligence tempdir");
        let store =
            IntelligenceStore::open(directory.path().join("limit.sqlite3")).expect("create store");
        for index in 0..6 {
            store
                .append_personal_memory(personal_with(
                    &format!("用户喜欢清爽柠檬饮料第{index}种"),
                    None,
                ))
                .expect("append limit fixture");
        }

        let context = store
            .retrieve("清爽柠檬饮料推荐")
            .expect("retrieve limited memories");
        assert_eq!(context.personal_memories.len(), 4);
        assert!(store.retrieve("饮料").expect("short query").is_empty());
    }
}
