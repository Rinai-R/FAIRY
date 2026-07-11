use super::*;

impl IntelligenceStore {
    pub fn retrieve(
        &self,
        character_id: CharacterId,
        query: &str,
    ) -> Result<RetrievalContext, FairyError> {
        let Some(fts_query) = build_fts_query(query)? else {
            return Ok(RetrievalContext::default());
        };
        let connection = self.lock()?;
        let mut remaining_chars = MAX_RETRIEVED_CONTEXT_CHARS;
        let personal_memories =
            retrieve_personal(&connection, character_id, &fts_query, &mut remaining_chars)?;
        let knowledge = retrieve_knowledge(&connection, &fts_query, &mut remaining_chars)?;
        Ok(RetrievalContext {
            personal_memories,
            knowledge,
        })
    }
}
