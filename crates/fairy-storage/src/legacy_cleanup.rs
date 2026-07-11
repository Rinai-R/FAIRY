use fairy_domain::{ErrorCode, FairyError};
use serde_json::Value;

use crate::{DocumentRead, StorageRoot};

const LEGACY_SEARCH_CONNECTION_PATH: &str = "search/connection.json";
const LEGACY_SEARCH_DOCUMENT_SCHEMA: u32 = 1;
const LEGACY_SEARCH_KEYCHAIN_SERVICE: &str = "com.rinai.fairy.search";

/// Removes the retired search configuration and its referenced Keychain item.
///
/// The configuration file is kept when Keychain cleanup fails so the next
/// startup can retry with the original account identifier.
pub fn cleanup_legacy_search_artifacts(root: &StorageRoot) -> Result<bool, FairyError> {
    let document =
        match root.read::<Value>(LEGACY_SEARCH_CONNECTION_PATH, LEGACY_SEARCH_DOCUMENT_SCHEMA)? {
            DocumentRead::Missing => return Ok(false),
            DocumentRead::Found(document) => document,
        };
    let connection_id = document
        .get("connection_id")
        .or_else(|| document.get("connectionId"))
        .and_then(Value::as_str)
        .filter(|value| !value.is_empty())
        .ok_or_else(|| cleanup_failed("旧搜索配置缺少 connection id"))?;
    let entry = keyring::Entry::new(LEGACY_SEARCH_KEYCHAIN_SERVICE, connection_id)
        .map_err(|_| cleanup_failed("无法连接系统 Keychain 清理旧搜索密钥"))?;
    match entry.delete_credential() {
        Ok(()) | Err(keyring::Error::NoEntry) => {}
        Err(_) => return Err(cleanup_failed("无法从系统 Keychain 删除旧搜索密钥")),
    }
    root.remove(LEGACY_SEARCH_CONNECTION_PATH)
}

fn cleanup_failed(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::StorageIo, message, true)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_legacy_search_configuration_is_an_explicit_noop() {
        let directory = tempfile::tempdir().expect("create cleanup directory");
        let root = StorageRoot::new(directory.path()).expect("create storage root");

        assert!(!cleanup_legacy_search_artifacts(&root).expect("cleanup missing artifacts"));
    }
}
