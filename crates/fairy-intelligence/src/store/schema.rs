use super::*;

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
            conversations: count_all(&connection, "conversations")?,
            active_global_memories: count_memory_scope(&connection, "global", "ready")?,
            active_character_memories: count_memory_scope(&connection, "character", "ready")?,
            needs_review_memories: count_memory_scope(
                &connection,
                "unassigned_legacy",
                "needs_review",
            )?,
            pending_extraction_turns: count_turn_extraction_state(&connection, "pending")?,
            running_batches: count_where(&connection, "extraction_batches", "running")?,
            failed_batches: count_where(&connection, "extraction_batches", "failed")?,
            candidate_knowledge: count_where(&connection, "knowledge_entries", "candidate")?,
            verified_knowledge: count_where(&connection, "knowledge_entries", "verified")?,
        })
    }
}
