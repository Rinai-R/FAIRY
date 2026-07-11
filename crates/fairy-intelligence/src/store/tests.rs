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
        scope: MemoryScope::Global,
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

struct V1FixtureIds {
    candidate: KnowledgeId,
    verified: KnowledgeId,
    job: String,
    preference: PersonalMemoryId,
    relationship: PersonalMemoryId,
}

fn seed_v1_fixture(path: &Path, invalid_verified_without_source: bool) -> V1FixtureIds {
    let candidate = KnowledgeId::new();
    let verified = KnowledgeId::new();
    let job = ExtractionBatchId::new().to_string();
    let preference = PersonalMemoryId::new();
    let relationship = PersonalMemoryId::new();
    let conversation_id = ConversationId::new();
    let turn_id = TurnId::new();
    let connection = Connection::open(path).expect("open v1 fixture");
    connection
        .execute_batch(
            "CREATE TABLE schema_meta (
                singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
                version INTEGER NOT NULL
             );
             INSERT INTO schema_meta VALUES (1, 1);
             CREATE TABLE personal_memories (
                id TEXT PRIMARY KEY, kind TEXT NOT NULL, content TEXT NOT NULL,
                status TEXT NOT NULL, confidence_basis_points INTEGER NOT NULL,
                source_conversation_id TEXT NOT NULL, source_turn_id TEXT NOT NULL,
                supersedes_id TEXT REFERENCES personal_memories(id),
                created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL
             );
             CREATE TABLE knowledge_entries (
                id TEXT PRIMARY KEY, topic TEXT NOT NULL, statement TEXT NOT NULL,
                status TEXT NOT NULL, confidence_basis_points INTEGER NOT NULL,
                source_conversation_id TEXT NOT NULL, source_turn_id TEXT NOT NULL,
                supersedes_id TEXT REFERENCES knowledge_entries(id),
                created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL
             );
             CREATE TABLE knowledge_sources (
                knowledge_id TEXT NOT NULL REFERENCES knowledge_entries(id),
                source_id TEXT NOT NULL, title TEXT NOT NULL, url TEXT NOT NULL,
                snippet TEXT NOT NULL, rank INTEGER NOT NULL, fetched_at_ms INTEGER NOT NULL,
                PRIMARY KEY(knowledge_id, source_id)
             );
             CREATE TABLE extraction_jobs (
                id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, turn_id TEXT NOT NULL,
                status TEXT NOT NULL, error_code TEXT, error_message TEXT,
                error_retryable INTEGER, created_at_ms INTEGER NOT NULL,
                updated_at_ms INTEGER NOT NULL
             );",
        )
        .expect("create v1 schema");
    for (id, kind, content) in [
        (preference, "preference", "用户喜欢红茶"),
        (relationship, "relationship", "用户曾经信任某个旧角色"),
    ] {
        connection
            .execute(
                "INSERT INTO personal_memories VALUES (
                    ?1, ?2, ?3, 'active', 9000, ?4, ?5, NULL, 1, 1
                 )",
                params![
                    id.to_string(),
                    kind,
                    content,
                    conversation_id.to_string(),
                    turn_id.to_string()
                ],
            )
            .expect("seed v1 personal memory");
    }
    connection
        .execute(
            "INSERT INTO knowledge_entries VALUES (
                ?1, '候选主题', '候选陈述', 'candidate', 7000, ?3, ?4, NULL, 1, 1
             ), (
                ?2, '验证主题', '验证陈述', 'verified', 9000, ?3, ?4, NULL, 1, 1
             )",
            params![
                candidate.to_string(),
                verified.to_string(),
                conversation_id.to_string(),
                turn_id.to_string()
            ],
        )
        .expect("seed v1 knowledge");
    if !invalid_verified_without_source {
        connection
            .execute(
                "INSERT INTO knowledge_sources VALUES (
                    ?1, ?2, '来源', 'https://example.com', '摘要', 1, 1
                 )",
                params![verified.to_string(), KnowledgeSourceId::new().to_string()],
            )
            .expect("seed v1 source");
    }
    connection
        .execute(
            "INSERT INTO extraction_jobs VALUES (
                ?1, ?2, ?3, 'failed', 'INTELLIGENCE_EXTRACTION_FAILED',
                'test failure', 0, 1, 1
             )",
            params![job, conversation_id.to_string(), turn_id.to_string()],
        )
        .expect("seed v1 extraction job");
    V1FixtureIds {
        candidate,
        verified,
        job,
        preference,
        relationship,
    }
}

#[test]
fn first_open_creates_schema_transactionally_and_reopens() {
    let directory = tempdir().expect("create intelligence tempdir");
    let path = directory.path().join("intelligence.sqlite3");
    let store = IntelligenceStore::open(&path).expect("create intelligence database");
    assert_eq!(store.schema_version().expect("schema version"), 3);
    drop(store);
    let reopened = IntelligenceStore::open(&path).expect("reopen intelligence database");
    assert_eq!(reopened.schema_version().expect("reopened version"), 3);
}

#[test]
fn character_conversations_persist_ordered_messages_and_remain_isolated() {
    let directory = tempdir().expect("create intelligence tempdir");
    let path = directory.path().join("conversations.sqlite3");
    let character_a = CharacterId::new();
    let character_b = CharacterId::new();
    let store = IntelligenceStore::open(&path).expect("create store");
    let conversation_a = store
        .open_or_create_character_conversation(character_a)
        .expect("create character a conversation");
    let conversation_b = store
        .open_or_create_character_conversation(character_b)
        .expect("create character b conversation");
    assert_ne!(
        conversation_a.conversation.id,
        conversation_b.conversation.id
    );

    let turn_id = TurnId::new();
    let (_, user_message) = store
        .begin_persisted_turn(
            conversation_a.conversation.id,
            turn_id,
            "今天有点累".to_owned(),
        )
        .expect("persist user message");
    let assistant_message = store
        .complete_persisted_turn(
            conversation_a.conversation.id,
            turn_id,
            "嗯，那先歇一会儿。".to_owned(),
        )
        .expect("persist assistant message");
    assert_eq!((user_message.sequence, assistant_message.sequence), (1, 2));
    drop(store);

    let reopened = IntelligenceStore::open(&path).expect("reopen store");
    let restored_a = reopened
        .open_or_create_character_conversation(character_a)
        .expect("restore character a");
    let restored_b = reopened
        .open_or_create_character_conversation(character_b)
        .expect("restore character b");
    assert_eq!(restored_a.messages.len(), 2);
    assert_eq!(restored_a.messages[0].content, "今天有点累");
    assert_eq!(restored_a.messages[1].content, "嗯，那先歇一会儿。");
    assert!(restored_b.messages.is_empty());
    restored_a
        .verify_integrity()
        .expect("restored transcript integrity");
}

#[test]
fn duplicate_turn_and_failed_assistant_validation_leave_no_partial_messages() {
    let directory = tempdir().expect("create intelligence tempdir");
    let store = IntelligenceStore::open(directory.path().join("turn-atomicity.sqlite3"))
        .expect("create store");
    let bootstrap = store
        .open_or_create_character_conversation(CharacterId::new())
        .expect("create conversation");
    let conversation_id = bootstrap.conversation.id;
    let turn_id = TurnId::new();
    store
        .begin_persisted_turn(conversation_id, turn_id, "第一条消息".to_owned())
        .expect("begin turn");
    assert!(
        store
            .begin_persisted_turn(conversation_id, turn_id, "重复消息".to_owned())
            .is_err()
    );
    assert!(
        store
            .complete_persisted_turn(conversation_id, turn_id, "含有\0NUL".to_owned())
            .is_err()
    );
    let restored = store
        .open_or_create_character_conversation(bootstrap.conversation.character_id)
        .expect("restore conversation");
    assert_eq!(restored.messages.len(), 1);
    assert_eq!(restored.messages[0].content, "第一条消息");

    let failed_turn = TurnId::new();
    store
        .begin_persisted_turn(conversation_id, failed_turn, "这轮会失败".to_owned())
        .expect("begin failed turn");
    store
        .terminate_persisted_turn(
            conversation_id,
            failed_turn,
            TurnState::Failed,
            Some(&FairyError::new(
                ErrorCode::ModelStreamFailed,
                "模型连接中断",
                true,
            )),
        )
        .expect("persist failed turn");
    assert!(
        store
            .complete_persisted_turn(conversation_id, failed_turn, "迟到的回复".to_owned())
            .is_err()
    );
    let (status, extraction_state, error_code): (String, String, Option<String>) = store
        .lock()
        .expect("lock store")
        .query_row(
            "SELECT status, extraction_state, error_code
             FROM conversation_turns WHERE id = ?1",
            [failed_turn.to_string()],
            |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?)),
        )
        .expect("read failed turn");
    assert_eq!(status, "failed");
    assert_eq!(extraction_state, "ineligible");
    assert_eq!(error_code.as_deref(), Some("MODEL_STREAM_FAILED"));
}

#[test]
fn prompt_window_commit_is_revision_checked_and_restores_summary() {
    let directory = tempdir().expect("create intelligence tempdir");
    let store = IntelligenceStore::open(directory.path().join("prompt-window.sqlite3"))
        .expect("create store");
    let character_id = CharacterId::new();
    let bootstrap = store
        .open_or_create_character_conversation(character_id)
        .expect("create conversation");
    let turn_id = TurnId::new();
    store
        .begin_persisted_turn(
            bootstrap.conversation.id,
            turn_id,
            "压缩前用户消息".to_owned(),
        )
        .expect("begin turn");
    store
        .complete_persisted_turn(
            bootstrap.conversation.id,
            turn_id,
            "压缩前助手消息".to_owned(),
        )
        .expect("complete turn");

    let window = store
        .commit_prompt_window(
            bootstrap.conversation.id,
            WindowRevision::INITIAL,
            "  已验证摘要  ".to_owned(),
        )
        .expect("commit prompt window");
    assert_eq!(window.revision.get(), 2);
    assert_eq!(window.cutoff_message_sequence, 2);
    assert_eq!(window.summary.as_deref(), Some("已验证摘要"));
    assert!(
        store
            .commit_prompt_window(
                bootstrap.conversation.id,
                WindowRevision::INITIAL,
                "过期写入".to_owned(),
            )
            .is_err()
    );
    let restored = store
        .open_or_create_character_conversation(character_id)
        .expect("restore prompt window");
    assert_eq!(restored.prompt_window, window);
}

#[test]
fn extraction_batch_claims_at_most_twelve_turns_and_never_double_claims() {
    let directory = tempdir().expect("create intelligence tempdir");
    let store = IntelligenceStore::open(directory.path().join("batch-queue.sqlite3"))
        .expect("create store");
    let bootstrap = store
        .open_or_create_character_conversation(CharacterId::new())
        .expect("create conversation");
    for index in 0..13 {
        let turn_id = TurnId::new();
        store
            .begin_persisted_turn(
                bootstrap.conversation.id,
                turn_id,
                format!("第 {index} 轮用户消息"),
            )
            .expect("begin queued turn");
        store
            .complete_persisted_turn(
                bootstrap.conversation.id,
                turn_id,
                format!("第 {index} 轮助手消息"),
            )
            .expect("complete queued turn");
    }
    assert_eq!(
        store
            .pending_extraction_turn_count(bootstrap.conversation.id)
            .expect("pending count"),
        13
    );

    let batch = store
        .claim_extraction_batch(bootstrap.conversation.id, 12)
        .expect("claim batch")
        .expect("nonempty batch");
    assert_eq!(batch.turns.len(), 12);
    assert!(
        store
            .claim_extraction_batch(bootstrap.conversation.id, 12)
            .is_err()
    );
    assert_eq!(
        store
            .pending_extraction_turn_count(bootstrap.conversation.id)
            .expect("remaining pending count"),
        1
    );
    store
        .fail_extraction_batch(
            batch.batch_id,
            &FairyError::new(ErrorCode::ExtractionBatchFailed, "测试批次失败", false),
        )
        .expect("mark batch failed");
    assert_eq!(
        store
            .pending_extraction_turn_count(bootstrap.conversation.id)
            .expect("failed batch is not automatically pending"),
        1
    );
    assert_eq!(
        store
            .retry_failed_extraction_batch(batch.batch_id)
            .expect("explicitly retry failed batch"),
        bootstrap.conversation.id
    );
    assert_eq!(
        store
            .pending_extraction_turn_count(bootstrap.conversation.id)
            .expect("retried turns return to pending"),
        13
    );
    assert!(
        store
            .claim_extraction_batch(bootstrap.conversation.id, 12)
            .expect("claim retried turns")
            .is_some()
    );
}

#[test]
fn reopening_cancels_running_batch_and_releases_claimed_turns() {
    let directory = tempdir().expect("create intelligence tempdir");
    let path = directory.path().join("batch-recovery.sqlite3");
    let store = IntelligenceStore::open(&path).expect("create store");
    let bootstrap = store
        .open_or_create_character_conversation(CharacterId::new())
        .expect("create conversation");
    for index in 0..3 {
        let turn_id = TurnId::new();
        store
            .begin_persisted_turn(
                bootstrap.conversation.id,
                turn_id,
                format!("恢复测试用户 {index}"),
            )
            .expect("begin turn");
        store
            .complete_persisted_turn(
                bootstrap.conversation.id,
                turn_id,
                format!("恢复测试助手 {index}"),
            )
            .expect("complete turn");
    }
    let running = store
        .claim_extraction_batch(bootstrap.conversation.id, 12)
        .expect("claim batch")
        .expect("running batch");
    drop(store);

    let reopened = IntelligenceStore::open(&path).expect("reopen store");
    assert_eq!(
        reopened
            .pending_extraction_turn_count(bootstrap.conversation.id)
            .expect("released pending count"),
        3
    );
    let status: String = reopened
        .lock()
        .expect("lock store")
        .query_row(
            "SELECT status FROM extraction_batches WHERE id = ?1",
            [running.batch_id.to_string()],
            |row| row.get(0),
        )
        .expect("read recovered batch");
    assert_eq!(status, "cancelled");
}

#[test]
fn memory_mutation_batch_deduplicates_and_rejects_unknown_supersede_atomically() {
    let directory = tempdir().expect("create intelligence tempdir");
    let store = IntelligenceStore::open(directory.path().join("memory-mutations.sqlite3"))
        .expect("create store");
    let character_id = CharacterId::new();
    let bootstrap = store
        .open_or_create_character_conversation(character_id)
        .expect("create conversation");
    let old = store
        .append_personal_memory(NewPersonalMemory {
            kind: PersonalMemoryKind::Preference,
            scope: MemoryScope::Global,
            content: "用户喜欢 红茶".to_owned(),
            confidence_basis_points: 9000,
            source_conversation_id: bootstrap.conversation.id,
            source_turn_id: TurnId::new(),
            supersedes_id: None,
        })
        .expect("append old preference");
    let first_turn = TurnId::new();
    store
        .begin_persisted_turn(
            bootstrap.conversation.id,
            first_turn,
            "我还是喜欢红茶".to_owned(),
        )
        .expect("begin first turn");
    store
        .complete_persisted_turn(
            bootstrap.conversation.id,
            first_turn,
            "嗯，红茶。".to_owned(),
        )
        .expect("complete first turn");
    let first_batch = store
        .claim_extraction_batch(bootstrap.conversation.id, 12)
        .expect("claim first batch")
        .expect("first batch");
    let results = store
        .commit_memory_mutations(
            first_batch.batch_id,
            character_id,
            &[old.id],
            vec![MemoryMutation::Create {
                kind: PersonalMemoryKind::Preference,
                scope: MemoryScope::Global,
                content: "用户喜欢  红茶".to_owned(),
                confidence_basis_points: 9500,
            }],
        )
        .expect("deduplicate mutation");
    assert_eq!(
        results,
        vec![MemoryMutationResult::NoChange {
            existing_memory_id: old.id
        }]
    );

    let second_turn = TurnId::new();
    store
        .begin_persisted_turn(
            bootstrap.conversation.id,
            second_turn,
            "现在改喝咖啡".to_owned(),
        )
        .expect("begin second turn");
    store
        .complete_persisted_turn(
            bootstrap.conversation.id,
            second_turn,
            "记住了。".to_owned(),
        )
        .expect("complete second turn");
    let second_batch = store
        .claim_extraction_batch(bootstrap.conversation.id, 12)
        .expect("claim second batch")
        .expect("second batch");
    let before_count: i64 = store
        .lock()
        .expect("lock store")
        .query_row("SELECT COUNT(*) FROM personal_memories", [], |row| {
            row.get(0)
        })
        .expect("count memories before failed batch");
    let cross_character_error = store
        .commit_memory_mutations(
            second_batch.batch_id,
            character_id,
            &[],
            vec![MemoryMutation::Create {
                kind: PersonalMemoryKind::Relationship,
                scope: MemoryScope::Character {
                    character_id: CharacterId::new(),
                },
                content: "用户和另一个角色的关系".to_owned(),
                confidence_basis_points: 8000,
            }],
        )
        .expect_err("cross-character relationship must fail");
    assert_eq!(
        cross_character_error.code,
        ErrorCode::InvalidIntelligenceRecord
    );
    let error = store
        .commit_memory_mutations(
            second_batch.batch_id,
            character_id,
            &[old.id],
            vec![
                MemoryMutation::Create {
                    kind: PersonalMemoryKind::Experience,
                    scope: MemoryScope::Global,
                    content: "用户今天去过书店".to_owned(),
                    confidence_basis_points: 8000,
                },
                MemoryMutation::Supersede {
                    memory_id: PersonalMemoryId::new(),
                    kind: PersonalMemoryKind::Preference,
                    scope: MemoryScope::Global,
                    content: "用户喜欢咖啡".to_owned(),
                    confidence_basis_points: 9000,
                },
            ],
        )
        .expect_err("unknown supersede must roll back whole batch");
    assert_eq!(error.code, ErrorCode::InvalidIntelligenceRecord);
    let after_count: i64 = store
        .lock()
        .expect("lock store")
        .query_row("SELECT COUNT(*) FROM personal_memories", [], |row| {
            row.get(0)
        })
        .expect("count memories after failed batch");
    assert_eq!(after_count, before_count);
    assert_eq!(
        store.personal_memory_status(old.id).expect("old status"),
        PersonalMemoryStatus::Active
    );
    store
        .fail_extraction_batch(
            second_batch.batch_id,
            &FairyError::new(
                ErrorCode::ExtractionBatchFailed,
                "测试失败后显式结束批次",
                false,
            ),
        )
        .expect("finish failed batch");

    let third_turn = TurnId::new();
    store
        .begin_persisted_turn(
            bootstrap.conversation.id,
            third_turn,
            "确认现在喜欢咖啡".to_owned(),
        )
        .expect("begin third turn");
    store
        .complete_persisted_turn(bootstrap.conversation.id, third_turn, "好。".to_owned())
        .expect("complete third turn");
    let third_batch = store
        .claim_extraction_batch(bootstrap.conversation.id, 12)
        .expect("claim third batch")
        .expect("third batch");
    let applied = store
        .commit_memory_mutations(
            third_batch.batch_id,
            character_id,
            &[old.id],
            vec![MemoryMutation::Supersede {
                memory_id: old.id,
                kind: PersonalMemoryKind::Preference,
                scope: MemoryScope::Global,
                content: "用户喜欢咖啡".to_owned(),
                confidence_basis_points: 9300,
            }],
        )
        .expect("apply valid supersede");
    assert!(matches!(
        applied.as_slice(),
        [MemoryMutationResult::Applied { .. }]
    ));
    assert_eq!(
        store.personal_memory_status(old.id).expect("old status"),
        PersonalMemoryStatus::Superseded
    );
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
fn personal_memory_catalog_and_user_edits_are_scoped_and_append_only() {
    let directory = tempdir().expect("create intelligence tempdir");
    let store = IntelligenceStore::open(directory.path().join("personal-catalog.sqlite3"))
        .expect("create store");
    let character_a = CharacterId::new();
    let character_b = CharacterId::new();
    let global = store
        .append_personal_memory(NewPersonalMemory {
            kind: PersonalMemoryKind::Preference,
            scope: MemoryScope::Global,
            content: "用户喜欢白茶".to_owned(),
            confidence_basis_points: 9000,
            source_conversation_id: ConversationId::new(),
            source_turn_id: TurnId::new(),
            supersedes_id: None,
        })
        .expect("append global memory");
    store
        .append_personal_memory(NewPersonalMemory {
            kind: PersonalMemoryKind::Relationship,
            scope: MemoryScope::Character {
                character_id: character_a,
            },
            content: "用户答应角色甲下次继续聊".to_owned(),
            confidence_basis_points: 8500,
            source_conversation_id: ConversationId::new(),
            source_turn_id: TurnId::new(),
            supersedes_id: None,
        })
        .expect("append character memory");

    let for_a = store
        .personal_memory_catalog(character_a)
        .expect("catalog for a");
    assert_eq!(for_a.global.len(), 1);
    assert_eq!(for_a.character.len(), 1);
    let for_b = store
        .personal_memory_catalog(character_b)
        .expect("catalog for b");
    assert_eq!(for_b.global.len(), 1);
    assert!(for_b.character.is_empty());

    let revised = store
        .revise_personal_memory(global.id, "用户现在喜欢咖啡".to_owned(), 9300)
        .expect("revise memory");
    assert_eq!(revised.supersedes_id, Some(global.id));
    assert_eq!(
        store.personal_memory_status(global.id).expect("old status"),
        PersonalMemoryStatus::Superseded
    );
    store
        .tombstone_personal_memory(revised.id)
        .expect("tombstone revised memory");
    assert!(
        store
            .personal_memory_catalog(character_a)
            .expect("catalog after tombstone")
            .global
            .is_empty()
    );
}

#[test]
fn schema_v1_migrates_knowledge_basis_without_losing_records() {
    let directory = tempdir().expect("create intelligence tempdir");
    let path = directory.path().join("schema-v1.sqlite3");
    let ids = seed_v1_fixture(&path, false);

    let migrated = IntelligenceStore::open(&path).expect("migrate schema v1");
    assert_eq!(migrated.schema_version().expect("schema version"), 3);
    assert_eq!(
        migrated
            .knowledge_status(ids.candidate)
            .expect("candidate status"),
        KnowledgeStatus::Candidate
    );
    assert_eq!(
        migrated
            .knowledge_status(ids.verified)
            .expect("verified status"),
        KnowledgeStatus::Verified
    );
    assert_eq!(
        Connection::open(&path)
            .expect("inspect migrated legacy job")
            .query_row(
                "SELECT status FROM extraction_jobs WHERE id = ?1",
                [ids.job],
                |row| row.get::<_, String>(0),
            )
            .expect("legacy job status"),
        "failed"
    );
    let assigned_character = CharacterId::new();
    let legacy_catalog = migrated
        .personal_memory_catalog(assigned_character)
        .expect("legacy memory catalog");
    assert_eq!(legacy_catalog.needs_review.len(), 1);
    assert_eq!(legacy_catalog.needs_review[0].id, ids.relationship);
    let assigned = migrated
        .assign_legacy_relationship(ids.relationship, assigned_character)
        .expect("assign legacy relationship");
    assert_eq!(
        assigned.scope,
        MemoryScope::Character {
            character_id: assigned_character
        }
    );
    drop(migrated);

    let connection = Connection::open(&path).expect("inspect migrated database");
    let candidate_basis: String = connection
        .query_row(
            "SELECT verification_basis FROM knowledge_entries WHERE id = ?1",
            [ids.candidate.to_string()],
            |row| row.get(0),
        )
        .expect("candidate basis");
    let verified_basis: String = connection
        .query_row(
            "SELECT verification_basis FROM knowledge_entries WHERE id = ?1",
            [ids.verified.to_string()],
            |row| row.get(0),
        )
        .expect("verified basis");
    let source_count: i64 = connection
        .query_row(
            "SELECT COUNT(*) FROM knowledge_sources WHERE knowledge_id = ?1",
            [ids.verified.to_string()],
            |row| row.get(0),
        )
        .expect("source count");
    assert_eq!(candidate_basis, "unverified");
    assert_eq!(verified_basis, "web_source");
    assert_eq!(source_count, 1);
    let preference_scope: (String, String) = connection
        .query_row(
            "SELECT scope_kind, review_status FROM personal_memories WHERE id = ?1",
            [ids.preference.to_string()],
            |row| Ok((row.get(0)?, row.get(1)?)),
        )
        .expect("global memory migration");
    assert_eq!(preference_scope, ("global".to_owned(), "ready".to_owned()));
    let relationship_scope: (String, Option<String>, String, String) = connection
        .query_row(
            "SELECT scope_kind, character_id, review_status, content
             FROM personal_memories WHERE id = ?1",
            [ids.relationship.to_string()],
            |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?, row.get(3)?)),
        )
        .expect("legacy relationship migration");
    assert_eq!(
        relationship_scope,
        (
            "unassigned_legacy".to_owned(),
            None,
            "needs_review".to_owned(),
            "用户曾经信任某个旧角色".to_owned()
        )
    );
}

#[test]
fn schema_v1_verified_without_source_fails_and_rolls_back_migration() {
    let directory = tempdir().expect("create intelligence tempdir");
    let path = directory.path().join("invalid-schema-v1.sqlite3");
    seed_v1_fixture(&path, true);

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
    let store =
        IntelligenceStore::open(directory.path().join("catalog.sqlite3")).expect("create store");
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
    let retrieved = store
        .retrieve(CharacterId::new(), "等待用户确认")
        .expect("retrieve confirmed");
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
            .retrieve(CharacterId::new(), "网页验证知识")
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
    let store =
        IntelligenceStore::open(directory.path().join("retrieval.sqlite3")).expect("create store");
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
        .retrieve(CharacterId::new(), "太甜的饮料推荐")
        .expect("retrieve personal memory");
    let second = store
        .retrieve(CharacterId::new(), "太甜的饮料推荐")
        .expect("repeat retrieval");
    assert_eq!(first, second);
    assert_eq!(first.personal_memories.len(), 1);
    assert_eq!(first.personal_memories[0].id, relevant.id);
    assert!(first.knowledge.is_empty());

    let knowledge = store
        .retrieve(CharacterId::new(), "Rust所有权系统")
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
        .retrieve(CharacterId::new(), "清爽柠檬饮料推荐")
        .expect("retrieve limited memories");
    assert_eq!(context.personal_memories.len(), 4);
    assert!(
        store
            .retrieve(CharacterId::new(), "饮料")
            .expect("short query")
            .is_empty()
    );
}

#[test]
fn retrieval_shares_global_memory_but_isolates_character_relationships() {
    let directory = tempdir().expect("create intelligence tempdir");
    let store = IntelligenceStore::open(directory.path().join("scoped-retrieval.sqlite3"))
        .expect("create store");
    let character_a = CharacterId::new();
    let character_b = CharacterId::new();
    let conversation_id = ConversationId::new();
    let turn_id = TurnId::new();
    for (scope, kind, content) in [
        (
            MemoryScope::Global,
            PersonalMemoryKind::Preference,
            "用户喜欢共同话题里的红茶",
        ),
        (
            MemoryScope::Character {
                character_id: character_a,
            },
            PersonalMemoryKind::Relationship,
            "用户和角色甲约定继续聊共同话题",
        ),
        (
            MemoryScope::Character {
                character_id: character_b,
            },
            PersonalMemoryKind::Relationship,
            "用户和角色乙约定继续聊共同话题",
        ),
    ] {
        store
            .append_personal_memory(NewPersonalMemory {
                kind,
                scope,
                content: content.to_owned(),
                confidence_basis_points: 9000,
                source_conversation_id: conversation_id,
                source_turn_id: turn_id,
                supersedes_id: None,
            })
            .expect("append scoped memory");
    }

    let for_a = store
        .retrieve(character_a, "共同话题")
        .expect("retrieve character a");
    assert_eq!(for_a.personal_memories.len(), 2);
    assert!(
        for_a
            .personal_memories
            .iter()
            .any(|memory| memory.scope == MemoryScope::Global)
    );
    assert!(for_a.personal_memories.iter().any(|memory| {
        memory.scope
            == MemoryScope::Character {
                character_id: character_a,
            }
    }));
    assert!(for_a.personal_memories.iter().all(|memory| {
        memory.scope
            != MemoryScope::Character {
                character_id: character_b,
            }
    }));
}
