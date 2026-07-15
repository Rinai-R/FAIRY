use fairy_domain::FairyError;
use serde_json::Value;

use crate::{DocumentRead, StorageRoot};

const LEGACY_SEARCH_CONNECTION_PATH: &str = "search/connection.json";
const LEGACY_SEARCH_DOCUMENT_SCHEMA: u32 = 1;

/// Removes the retired search configuration without touching the old system credential.
///
/// The search feature is gone, and startup must not trigger macOS password prompts just to
/// delete historical secrets. Any old system credential is left as external OS state.
pub fn cleanup_legacy_search_artifacts(root: &StorageRoot) -> Result<bool, FairyError> {
    match root.read::<Value>(LEGACY_SEARCH_CONNECTION_PATH, LEGACY_SEARCH_DOCUMENT_SCHEMA)? {
        DocumentRead::Missing => Ok(false),
        DocumentRead::Found(_) => root.remove(LEGACY_SEARCH_CONNECTION_PATH),
    }
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    #[test]
    fn missing_legacy_search_configuration_is_an_explicit_noop() {
        let directory = tempfile::tempdir().expect("create cleanup directory");
        let root = StorageRoot::new(directory.path()).expect("create storage root");

        assert!(!cleanup_legacy_search_artifacts(&root).expect("cleanup missing artifacts"));
    }

    #[test]
    fn legacy_search_configuration_removes_local_file_without_secret_cleanup() {
        let directory = tempfile::tempdir().expect("create cleanup directory");
        let root = StorageRoot::new(directory.path()).expect("create storage root");
        root.write_replace(
            LEGACY_SEARCH_CONNECTION_PATH,
            LEGACY_SEARCH_DOCUMENT_SCHEMA,
            &json!({ "connection_id": "legacy-search" }),
        )
        .expect("write legacy search config");

        assert!(cleanup_legacy_search_artifacts(&root).expect("cleanup local legacy config"));
        assert!(matches!(
            root.read::<Value>(LEGACY_SEARCH_CONNECTION_PATH, LEGACY_SEARCH_DOCUMENT_SCHEMA)
                .expect("read removed config"),
            DocumentRead::Missing
        ));
    }
}
