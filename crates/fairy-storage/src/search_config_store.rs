use fairy_domain::{
    ErrorCode, FairyError, SearchConnectionCompiler, SearchConnectionConfig, SearchConnectionId,
    SearchConnectionInput,
};
use secrecy::SecretString;

use crate::secret_store::validate_search_secret;
use crate::{DocumentRead, SearchSecretStore, StorageRoot};

const SEARCH_CONNECTION_DOCUMENT_SCHEMA: u32 = 1;
const SEARCH_CONNECTION_PATH: &str = "search/connection.json";

#[derive(Debug)]
pub struct ResolvedSearchConnection {
    pub config: SearchConnectionConfig,
    pub api_key: SecretString,
}

#[derive(Debug)]
pub struct SearchConnectionStore<S> {
    root: StorageRoot,
    secrets: S,
    compiler: SearchConnectionCompiler,
}

impl<S: SearchSecretStore> SearchConnectionStore<S> {
    #[must_use]
    pub fn new(root: StorageRoot, secrets: S) -> Self {
        Self {
            root,
            secrets,
            compiler: SearchConnectionCompiler,
        }
    }

    pub fn status(&self) -> Result<Option<SearchConnectionConfig>, FairyError> {
        match self.root.read::<SearchConnectionConfig>(
            SEARCH_CONNECTION_PATH,
            SEARCH_CONNECTION_DOCUMENT_SCHEMA,
        )? {
            DocumentRead::Missing => Ok(None),
            DocumentRead::Found(config) => {
                config.verify_integrity()?;
                Ok(Some(config))
            }
        }
    }

    pub fn save(
        &self,
        input: SearchConnectionInput,
        api_key: Option<SecretString>,
    ) -> Result<SearchConnectionConfig, FairyError> {
        let connection_id = self
            .status()?
            .map(|config| config.connection_id())
            .unwrap_or_else(SearchConnectionId::new);
        let config = self.compiler.compile(connection_id, input)?;
        match api_key {
            Some(secret) => {
                validate_search_secret(&secret)?;
                self.secrets.save(connection_id, &secret)?;
            }
            None => {
                if self.secrets.load(connection_id)?.is_none() {
                    return Err(search_secret_unavailable("Brave Search 连接需要订阅密钥"));
                }
            }
        }
        self.root.write_replace(
            SEARCH_CONNECTION_PATH,
            SEARCH_CONNECTION_DOCUMENT_SCHEMA,
            &config,
        )?;
        Ok(config)
    }

    pub fn resolve(&self) -> Result<ResolvedSearchConnection, FairyError> {
        let config = self.status()?.ok_or_else(search_config_required)?;
        let api_key = self
            .secrets
            .load(config.connection_id())?
            .ok_or_else(|| search_secret_unavailable("系统 Keychain 中没有搜索密钥"))?;
        Ok(ResolvedSearchConnection { config, api_key })
    }

    pub fn clear(&self) -> Result<bool, FairyError> {
        let Some(config) = self.status()? else {
            return Ok(false);
        };
        self.secrets.delete(config.connection_id())?;
        self.root.remove(SEARCH_CONNECTION_PATH)
    }
}

fn search_config_required() -> FairyError {
    FairyError::new(
        ErrorCode::SearchConfigRequired,
        "请先配置 Brave Search 连接",
        false,
    )
}

fn search_secret_unavailable(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::SearchSecretUnavailable, message, false)
}

#[cfg(test)]
mod tests {
    use std::collections::HashMap;
    use std::sync::Mutex;

    use secrecy::{ExposeSecret, SecretString};
    use tempfile::tempdir;

    use super::*;

    #[derive(Default, Debug)]
    struct FakeSearchSecretStore {
        values: Mutex<HashMap<SearchConnectionId, String>>,
    }

    impl SearchSecretStore for FakeSearchSecretStore {
        fn save(
            &self,
            connection_id: SearchConnectionId,
            secret: &SecretString,
        ) -> Result<(), FairyError> {
            self.values
                .lock()
                .expect("lock fake search secrets")
                .insert(connection_id, secret.expose_secret().to_owned());
            Ok(())
        }

        fn load(
            &self,
            connection_id: SearchConnectionId,
        ) -> Result<Option<SecretString>, FairyError> {
            Ok(self
                .values
                .lock()
                .expect("lock fake search secrets")
                .get(&connection_id)
                .cloned()
                .map(SecretString::from))
        }

        fn delete(&self, connection_id: SearchConnectionId) -> Result<(), FairyError> {
            self.values
                .lock()
                .expect("lock fake search secrets")
                .remove(&connection_id);
            Ok(())
        }
    }

    #[test]
    fn missing_config_and_secret_fail_explicitly() {
        let directory = tempdir().expect("create search store tempdir");
        let root = StorageRoot::new(directory.path()).expect("create root");
        let store = SearchConnectionStore::new(root, FakeSearchSecretStore::default());

        assert_eq!(
            store.resolve().expect_err("missing config").code,
            ErrorCode::SearchConfigRequired
        );
        assert_eq!(
            store
                .save(SearchConnectionInput::default(), None)
                .expect_err("missing secret")
                .code,
            ErrorCode::SearchSecretUnavailable
        );
        assert_eq!(store.status().expect("status"), None);
    }

    #[test]
    fn search_secret_is_keychain_only_and_can_be_reused() {
        let directory = tempdir().expect("create search store tempdir");
        let root = StorageRoot::new(directory.path()).expect("create root");
        let store = SearchConnectionStore::new(root.clone(), FakeSearchSecretStore::default());
        let raw = "brave-secret-value";

        let first = store
            .save(
                SearchConnectionInput::default(),
                Some(SecretString::from(raw.to_owned())),
            )
            .expect("save search connection");
        let second = store
            .save(SearchConnectionInput::default(), None)
            .expect("reuse existing secret");
        let resolved = store.resolve().expect("resolve search connection");
        let document = std::fs::read_to_string(root.directory().join(SEARCH_CONNECTION_PATH))
            .expect("read search config");

        assert_eq!(first.connection_id(), second.connection_id());
        assert_eq!(resolved.api_key.expose_secret(), raw);
        assert!(!document.contains(raw));
        assert!(!format!("{resolved:?}").contains(raw));
    }

    #[test]
    fn clear_deletes_both_reference_and_secret() {
        let directory = tempdir().expect("create search store tempdir");
        let root = StorageRoot::new(directory.path()).expect("create root");
        let store = SearchConnectionStore::new(root, FakeSearchSecretStore::default());
        let config = store
            .save(
                SearchConnectionInput::default(),
                Some(SecretString::from("secret".to_owned())),
            )
            .expect("save search connection");

        assert!(store.clear().expect("clear search connection"));
        assert_eq!(store.status().expect("status after clear"), None);
        assert!(
            store
                .secrets
                .load(config.connection_id())
                .expect("read fake keychain")
                .is_none()
        );
        assert!(!store.clear().expect("idempotent missing clear"));
    }
}
