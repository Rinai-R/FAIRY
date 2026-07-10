use fairy_domain::{
    AuthMode, ErrorCode, FairyError, GatewayCapabilities, ModelConnectionCompiler,
    ModelConnectionConfig, ModelConnectionId, ModelConnectionInput, ModelProtocol,
};
use secrecy::SecretString;
use serde::{Deserialize, Serialize};

use crate::secret_store::validate_secret;
use crate::{DocumentRead, SecretStore, StorageRoot};

const MODEL_CONNECTION_DOCUMENT_SCHEMA: u32 = 1;
const MODEL_CONNECTION_PATH: &str = "model/connection.json";
const LEGACY_MODEL_CONNECTION_SCHEMA: u32 = 1;

#[derive(Debug, Deserialize)]
#[serde(untagged)]
enum StoredModelConnection {
    Current(ModelConnectionConfig),
    Legacy(LegacyModelConnectionConfigV1),
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct LegacyModelConnectionConfigV1 {
    schema_version: u32,
    connection_id: ModelConnectionId,
    endpoint: String,
    model: String,
    auth_mode: AuthMode,
    #[serde(rename = "capabilities")]
    _capabilities: GatewayCapabilities,
}

#[derive(Debug)]
pub struct ResolvedModelConnection {
    pub config: ModelConnectionConfig,
    pub api_key: Option<SecretString>,
}

#[derive(Debug)]
pub struct ModelConnectionStore<S> {
    root: StorageRoot,
    secrets: S,
    compiler: ModelConnectionCompiler,
}

impl<S: SecretStore> ModelConnectionStore<S> {
    #[must_use]
    pub fn new(root: StorageRoot, secrets: S) -> Self {
        Self {
            root,
            secrets,
            compiler: ModelConnectionCompiler,
        }
    }

    pub fn status(&self) -> Result<Option<ModelConnectionConfig>, FairyError> {
        match self.root.read::<StoredModelConnection>(
            MODEL_CONNECTION_PATH,
            MODEL_CONNECTION_DOCUMENT_SCHEMA,
        )? {
            DocumentRead::Missing => Ok(None),
            DocumentRead::Found(StoredModelConnection::Current(config)) => {
                config.verify_integrity()?;
                Ok(Some(config))
            }
            DocumentRead::Found(StoredModelConnection::Legacy(legacy)) => {
                self.migrate_legacy(legacy).map(Some)
            }
        }
    }

    fn migrate_legacy(
        &self,
        legacy: LegacyModelConnectionConfigV1,
    ) -> Result<ModelConnectionConfig, FairyError> {
        if legacy.schema_version != LEGACY_MODEL_CONNECTION_SCHEMA {
            return Err(FairyError::new(
                ErrorCode::StorageCorrupted,
                "旧模型连接配置版本不受支持",
                false,
            ));
        }
        let config = self.compiler.compile(
            legacy.connection_id,
            ModelConnectionInput {
                protocol: ModelProtocol::Responses,
                endpoint: legacy.endpoint,
                model: legacy.model,
                auth_mode: legacy.auth_mode,
            },
        )?;
        config.verify_integrity()?;
        self.root.write_replace(
            MODEL_CONNECTION_PATH,
            MODEL_CONNECTION_DOCUMENT_SCHEMA,
            &config,
        )?;
        Ok(config)
    }

    pub fn save(
        &self,
        input: ModelConnectionInput,
        api_key: Option<SecretString>,
    ) -> Result<ModelConnectionConfig, FairyError> {
        let connection_id = self
            .status()?
            .map(|config| config.connection_id())
            .unwrap_or_else(ModelConnectionId::new);
        let config = self.compiler.compile(connection_id, input)?;

        match config.auth_mode() {
            AuthMode::BearerKey => match api_key {
                Some(secret) => {
                    validate_secret(&secret)?;
                    self.secrets.save(connection_id, &secret)?;
                }
                None => {
                    if self.secrets.load(connection_id)?.is_none() {
                        return Err(secret_unavailable("BearerKey 连接需要模型密钥"));
                    }
                }
            },
            AuthMode::NoAuth => {
                if api_key.is_some() {
                    return Err(FairyError::new(
                        ErrorCode::InvalidModelConfig,
                        "NoAuth 连接不得同时提交模型密钥",
                        false,
                    ));
                }
                self.secrets.delete(connection_id)?;
            }
        }

        self.root.write_replace(
            MODEL_CONNECTION_PATH,
            MODEL_CONNECTION_DOCUMENT_SCHEMA,
            &config,
        )?;
        Ok(config)
    }

    pub fn resolve(&self) -> Result<ResolvedModelConnection, FairyError> {
        let config = self.status()?.ok_or_else(model_config_required)?;
        let api_key = match config.auth_mode() {
            AuthMode::BearerKey => Some(
                self.secrets
                    .load(config.connection_id())?
                    .ok_or_else(|| secret_unavailable("系统 Keychain 中没有模型密钥"))?,
            ),
            AuthMode::NoAuth => None,
        };
        Ok(ResolvedModelConnection { config, api_key })
    }

    pub fn clear(&self) -> Result<bool, FairyError> {
        let Some(config) = self.status()? else {
            return Ok(false);
        };
        self.secrets.delete(config.connection_id())?;
        self.root.remove(MODEL_CONNECTION_PATH)
    }
}

fn model_config_required() -> FairyError {
    FairyError::new(ErrorCode::ModelConfigRequired, "请先配置模型连接", false)
}

fn secret_unavailable(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::ModelSecretUnavailable, message, false)
}

#[cfg(test)]
mod tests {
    use std::collections::HashMap;
    use std::sync::Mutex;
    use std::sync::atomic::{AtomicBool, Ordering};

    use secrecy::{ExposeSecret, SecretString};
    use tempfile::tempdir;

    use super::*;

    #[derive(Default)]
    struct FakeSecretStore {
        values: Mutex<HashMap<ModelConnectionId, String>>,
        fail_save: AtomicBool,
        fail_load: AtomicBool,
        fail_delete: AtomicBool,
    }

    impl FakeSecretStore {
        fn contains(&self, connection_id: ModelConnectionId) -> bool {
            self.values
                .lock()
                .expect("lock fake keychain")
                .contains_key(&connection_id)
        }
    }

    impl SecretStore for FakeSecretStore {
        fn save(
            &self,
            connection_id: ModelConnectionId,
            secret: &SecretString,
        ) -> Result<(), FairyError> {
            if self.fail_save.load(Ordering::SeqCst) {
                return Err(secret_unavailable("fake save failure"));
            }
            self.values
                .lock()
                .expect("lock fake keychain")
                .insert(connection_id, secret.expose_secret().to_owned());
            Ok(())
        }

        fn load(
            &self,
            connection_id: ModelConnectionId,
        ) -> Result<Option<SecretString>, FairyError> {
            if self.fail_load.load(Ordering::SeqCst) {
                return Err(secret_unavailable("fake load failure"));
            }
            Ok(self
                .values
                .lock()
                .expect("lock fake keychain")
                .get(&connection_id)
                .cloned()
                .map(SecretString::from))
        }

        fn delete(&self, connection_id: ModelConnectionId) -> Result<(), FairyError> {
            if self.fail_delete.load(Ordering::SeqCst) {
                return Err(secret_unavailable("fake delete failure"));
            }
            self.values
                .lock()
                .expect("lock fake keychain")
                .remove(&connection_id);
            Ok(())
        }
    }

    fn bearer_input() -> ModelConnectionInput {
        ModelConnectionInput {
            protocol: ModelProtocol::Responses,
            endpoint: "https://api.openai.com/v1".to_owned(),
            model: "gpt-5.4".to_owned(),
            auth_mode: AuthMode::BearerKey,
        }
    }

    fn no_auth_input() -> ModelConnectionInput {
        ModelConnectionInput {
            protocol: ModelProtocol::Responses,
            endpoint: "http://127.0.0.1:11434/v1".to_owned(),
            model: "local-model".to_owned(),
            auth_mode: AuthMode::NoAuth,
        }
    }

    fn legacy_config(
        connection_id: ModelConnectionId,
        endpoint: &str,
    ) -> LegacyModelConnectionConfigV1 {
        LegacyModelConnectionConfigV1 {
            schema_version: LEGACY_MODEL_CONNECTION_SCHEMA,
            connection_id,
            endpoint: endpoint.to_owned(),
            model: "legacy-model".to_owned(),
            auth_mode: AuthMode::BearerKey,
            _capabilities: GatewayCapabilities::responses_http(false, false),
        }
    }

    #[test]
    fn legacy_v1_migrates_to_responses_and_keeps_keychain_reference() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        let store = ModelConnectionStore::new(root.clone(), FakeSecretStore::default());
        let connection_id = ModelConnectionId::new();
        let secret = SecretString::from("sk-existing-v1".to_owned());
        store
            .secrets
            .save(connection_id, &secret)
            .expect("seed legacy keychain value");
        root.write_replace(
            MODEL_CONNECTION_PATH,
            MODEL_CONNECTION_DOCUMENT_SCHEMA,
            &legacy_config(connection_id, "https://api.openai.com/v1"),
        )
        .expect("write legacy fixture");
        let before = std::fs::read_to_string(root.directory().join(MODEL_CONNECTION_PATH))
            .expect("read legacy fixture");
        assert!(!before.contains("protocol"));

        let migrated = store
            .status()
            .expect("migrate legacy config")
            .expect("migrated config");
        assert_eq!(migrated.schema_version(), 2);
        assert_eq!(migrated.protocol(), ModelProtocol::Responses);
        assert_eq!(migrated.connection_id(), connection_id);
        assert!(migrated.capabilities().prompt_cache_key);
        assert!(migrated.capabilities().cached_tokens_usage);

        let resolved = store.resolve().expect("resolve migrated config");
        assert_eq!(resolved.config.connection_id(), connection_id);
        assert_eq!(
            resolved
                .api_key
                .expect("migrated keychain value")
                .expose_secret(),
            "sk-existing-v1"
        );
        let after = std::fs::read_to_string(root.directory().join(MODEL_CONNECTION_PATH))
            .expect("read migrated fixture");
        assert!(after.contains("\"schema_version\":2"));
        assert!(after.contains("\"protocol\":\"responses\""));
    }

    #[test]
    fn invalid_legacy_config_fails_before_atomic_replacement() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        let store = ModelConnectionStore::new(root.clone(), FakeSecretStore::default());
        root.write_replace(
            MODEL_CONNECTION_PATH,
            MODEL_CONNECTION_DOCUMENT_SCHEMA,
            &legacy_config(
                ModelConnectionId::new(),
                "https://api.deepseek.com/chat/completions",
            ),
        )
        .expect("write invalid legacy fixture");
        let path = root.directory().join(MODEL_CONNECTION_PATH);
        let before = std::fs::read(&path).expect("read invalid legacy bytes");

        let error = store
            .status()
            .expect_err("invalid legacy endpoint must not migrate");
        assert_eq!(error.code, ErrorCode::InvalidModelConfig);
        assert_eq!(std::fs::read(path).expect("read unchanged bytes"), before);
    }

    #[test]
    fn unknown_legacy_inner_schema_is_not_overwritten() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        let store = ModelConnectionStore::new(root.clone(), FakeSecretStore::default());
        let mut legacy = legacy_config(ModelConnectionId::new(), "https://api.openai.com/v1");
        legacy.schema_version = 99;
        root.write_replace(
            MODEL_CONNECTION_PATH,
            MODEL_CONNECTION_DOCUMENT_SCHEMA,
            &legacy,
        )
        .expect("write unknown legacy fixture");
        let path = root.directory().join(MODEL_CONNECTION_PATH);
        let before = std::fs::read(&path).expect("read unknown schema bytes");

        let error = store.status().expect_err("unknown legacy schema must fail");
        assert_eq!(error.code, ErrorCode::StorageCorrupted);
        assert_eq!(std::fs::read(path).expect("read unchanged bytes"), before);
    }

    #[test]
    fn missing_config_and_missing_bearer_secret_fail_explicitly() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        let store = ModelConnectionStore::new(root, FakeSecretStore::default());

        assert_eq!(
            store.resolve().expect_err("missing config must fail").code,
            ErrorCode::ModelConfigRequired
        );
        assert_eq!(
            store
                .save(bearer_input(), None)
                .expect_err("missing bearer secret must fail")
                .code,
            ErrorCode::ModelSecretUnavailable
        );
        assert_eq!(store.status().expect("read status"), None);
    }

    #[test]
    fn bearer_secret_is_keychain_only_and_debug_is_redacted() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        let store = ModelConnectionStore::new(root.clone(), FakeSecretStore::default());
        let raw_secret = "sk-test-exact-value";

        let config = store
            .save(
                bearer_input(),
                Some(SecretString::from(raw_secret.to_owned())),
            )
            .expect("save bearer connection");
        let resolved = store.resolve().expect("resolve bearer connection");
        let config_json = std::fs::read_to_string(root.directory().join(MODEL_CONNECTION_PATH))
            .expect("read model config fixture");

        assert!(store.secrets.contains(config.connection_id()));
        assert_eq!(
            resolved
                .api_key
                .as_ref()
                .expect("resolved bearer key")
                .expose_secret(),
            raw_secret
        );
        assert!(!config_json.contains(raw_secret));
        assert!(!format!("{resolved:?}").contains(raw_secret));
        assert!(
            !serde_json::to_string(&config)
                .expect("serialize public config")
                .contains(raw_secret)
        );
    }

    #[test]
    fn secret_is_rejected_not_trimmed() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        let store = ModelConnectionStore::new(root, FakeSecretStore::default());

        for secret in ["", " sk-leading", "sk-trailing "] {
            let error = store
                .save(bearer_input(), Some(SecretString::from(secret.to_owned())))
                .expect_err("invalid exact secret must fail");
            assert_eq!(error.code, ErrorCode::ModelSecretUnavailable);
        }
        assert_eq!(store.status().expect("read status"), None);
    }

    #[test]
    fn keychain_save_failure_does_not_write_ready_config() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        let secrets = FakeSecretStore::default();
        secrets.fail_save.store(true, Ordering::SeqCst);
        let store = ModelConnectionStore::new(root, secrets);

        let error = store
            .save(
                bearer_input(),
                Some(SecretString::from("sk-valid".to_owned())),
            )
            .expect_err("fake keychain failure must propagate");

        assert_eq!(error.code, ErrorCode::ModelSecretUnavailable);
        assert_eq!(store.status().expect("read status"), None);
    }

    #[test]
    fn explicit_no_auth_removes_old_secret_and_rejects_supplied_key() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        let store = ModelConnectionStore::new(root, FakeSecretStore::default());
        let bearer = store
            .save(
                bearer_input(),
                Some(SecretString::from("sk-existing".to_owned())),
            )
            .expect("save bearer config");

        let error = store
            .save(
                no_auth_input(),
                Some(SecretString::from("must-not-ignore".to_owned())),
            )
            .expect_err("NoAuth must reject submitted key");
        assert_eq!(error.code, ErrorCode::InvalidModelConfig);
        assert!(store.secrets.contains(bearer.connection_id()));

        let no_auth = store
            .save(no_auth_input(), None)
            .expect("switch to explicit NoAuth");
        assert_eq!(no_auth.connection_id(), bearer.connection_id());
        assert!(!store.secrets.contains(no_auth.connection_id()));
        assert!(store.resolve().expect("resolve NoAuth").api_key.is_none());
    }

    #[test]
    fn clear_deletes_secret_before_config_and_is_idempotent_when_missing() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        let store = ModelConnectionStore::new(root, FakeSecretStore::default());
        let config = store
            .save(
                bearer_input(),
                Some(SecretString::from("sk-remove".to_owned())),
            )
            .expect("save bearer config");

        assert!(store.clear().expect("clear model connection"));
        assert!(!store.secrets.contains(config.connection_id()));
        assert_eq!(store.status().expect("config was removed"), None);
        assert!(!store.clear().expect("clear missing connection"));
    }
}
