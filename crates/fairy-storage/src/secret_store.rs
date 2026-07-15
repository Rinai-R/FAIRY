use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use fairy_domain::{ErrorCode, FairyError, ModelConnectionId};
use rusqlite::{Connection, OptionalExtension, params};
use secrecy::{ExposeSecret, SecretString};

const KEYCHAIN_SERVICE: &str = "com.rinai.fairy.model";

pub trait SecretStore: Send + Sync {
    fn save(
        &self,
        connection_id: ModelConnectionId,
        secret: &SecretString,
    ) -> Result<(), FairyError>;

    fn load(&self, connection_id: ModelConnectionId) -> Result<Option<SecretString>, FairyError>;

    fn delete(&self, connection_id: ModelConnectionId) -> Result<(), FairyError>;
}

#[derive(Clone, Debug)]
pub struct PlaintextSqliteSecretStore {
    path: PathBuf,
}

impl PlaintextSqliteSecretStore {
    #[must_use]
    pub fn new(path: impl Into<PathBuf>) -> Self {
        Self { path: path.into() }
    }

    #[must_use]
    pub fn path(&self) -> &Path {
        &self.path
    }

    fn connection(&self) -> Result<Connection, FairyError> {
        if let Some(parent) = self.path.parent() {
            std::fs::create_dir_all(parent)
                .map_err(|_| secret_unavailable("无法创建本地模型密钥库目录"))?;
        }
        let connection = Connection::open(&self.path)
            .map_err(|_| secret_unavailable("无法打开本地模型密钥库"))?;
        connection
            .execute_batch(
                "CREATE TABLE IF NOT EXISTS model_secrets (
                    connection_id TEXT PRIMARY KEY,
                    secret TEXT NOT NULL,
                    updated_at_ms INTEGER NOT NULL
                );",
            )
            .map_err(|_| secret_unavailable("无法初始化本地模型密钥库"))?;
        Ok(connection)
    }
}

impl SecretStore for PlaintextSqliteSecretStore {
    fn save(
        &self,
        connection_id: ModelConnectionId,
        secret: &SecretString,
    ) -> Result<(), FairyError> {
        validate_secret(secret)?;
        let connection = self.connection()?;
        connection
            .execute(
                "INSERT INTO model_secrets(connection_id, secret, updated_at_ms)
                 VALUES (?1, ?2, ?3)
                 ON CONFLICT(connection_id) DO UPDATE SET
                    secret = excluded.secret,
                    updated_at_ms = excluded.updated_at_ms",
                params![
                    connection_id.to_string(),
                    secret.expose_secret(),
                    current_unix_ms(),
                ],
            )
            .map_err(|_| secret_unavailable("无法保存本地模型密钥"))?;
        Ok(())
    }

    fn load(&self, connection_id: ModelConnectionId) -> Result<Option<SecretString>, FairyError> {
        let connection = self.connection()?;
        let secret = connection
            .query_row(
                "SELECT secret FROM model_secrets WHERE connection_id = ?1",
                params![connection_id.to_string()],
                |row| row.get::<_, String>(0),
            )
            .optional()
            .map_err(|_| secret_unavailable("无法读取本地模型密钥"))?;
        if let Some(secret) = secret.as_ref() {
            validate_secret(&SecretString::from(secret.clone()))?;
        }
        Ok(secret.map(SecretString::from))
    }

    fn delete(&self, connection_id: ModelConnectionId) -> Result<(), FairyError> {
        let connection = self.connection()?;
        connection
            .execute(
                "DELETE FROM model_secrets WHERE connection_id = ?1",
                params![connection_id.to_string()],
            )
            .map_err(|_| secret_unavailable("无法删除本地模型密钥"))?;
        Ok(())
    }
}

#[derive(Clone, Copy, Debug, Default)]
pub struct SystemSecretStore;

impl SecretStore for SystemSecretStore {
    fn save(
        &self,
        connection_id: ModelConnectionId,
        secret: &SecretString,
    ) -> Result<(), FairyError> {
        validate_secret(secret)?;
        entry(connection_id)?
            .set_password(secret.expose_secret())
            .map_err(|_| secret_unavailable("系统 Keychain 拒绝保存模型密钥"))
    }

    fn load(&self, connection_id: ModelConnectionId) -> Result<Option<SecretString>, FairyError> {
        match entry(connection_id)?.get_password() {
            Ok(secret) => Ok(Some(SecretString::from(secret))),
            Err(keyring::Error::NoEntry) => Ok(None),
            Err(_) => Err(secret_unavailable("无法从系统 Keychain 读取模型密钥")),
        }
    }

    fn delete(&self, connection_id: ModelConnectionId) -> Result<(), FairyError> {
        match entry(connection_id)?.delete_credential() {
            Ok(()) | Err(keyring::Error::NoEntry) => Ok(()),
            Err(_) => Err(secret_unavailable("无法从系统 Keychain 删除模型密钥")),
        }
    }
}

pub(crate) fn validate_secret(secret: &SecretString) -> Result<(), FairyError> {
    let value = secret.expose_secret();
    if value.is_empty() || value.trim() != value {
        return Err(secret_unavailable(
            "模型密钥不能为空，也不能包含首尾空白字符",
        ));
    }
    Ok(())
}

fn entry(connection_id: ModelConnectionId) -> Result<keyring::Entry, FairyError> {
    keyring::Entry::new(KEYCHAIN_SERVICE, &connection_id.to_string())
        .map_err(|_| secret_unavailable("无法连接系统 Keychain"))
}

fn secret_unavailable(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::ModelSecretUnavailable, message, false)
}

fn current_unix_ms() -> i64 {
    let millis = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis();
    i64::try_from(millis).unwrap_or(i64::MAX)
}

#[cfg(test)]
mod tests {
    use super::*;

    use tempfile::tempdir;

    #[test]
    fn plaintext_sqlite_secret_persists_across_store_instances() {
        let directory = tempdir().expect("create temp directory");
        let path = directory.path().join("model/secrets.sqlite3");
        let connection_id = ModelConnectionId::new();
        let first = PlaintextSqliteSecretStore::new(&path);

        first
            .save(
                connection_id,
                &SecretString::from("sk-local-exact".to_owned()),
            )
            .expect("save local secret");

        let reopened = PlaintextSqliteSecretStore::new(&path);
        let loaded = reopened
            .load(connection_id)
            .expect("load local secret")
            .expect("secret exists");

        assert_eq!(loaded.expose_secret(), "sk-local-exact");
        assert!(reopened.path().ends_with("model/secrets.sqlite3"));
    }

    #[test]
    fn plaintext_sqlite_secret_delete_is_idempotent() {
        let directory = tempdir().expect("create temp directory");
        let path = directory.path().join("model/secrets.sqlite3");
        let connection_id = ModelConnectionId::new();
        let store = PlaintextSqliteSecretStore::new(path);

        store
            .save(connection_id, &SecretString::from("sk-remove".to_owned()))
            .expect("save local secret");
        store.delete(connection_id).expect("delete local secret");
        store
            .delete(connection_id)
            .expect("delete missing local secret");

        assert!(
            store
                .load(connection_id)
                .expect("load missing local secret")
                .is_none()
        );
    }

    #[test]
    fn plaintext_sqlite_secret_rejects_invalid_secret_without_creating_ready_value() {
        let directory = tempdir().expect("create temp directory");
        let path = directory.path().join("model/secrets.sqlite3");
        let connection_id = ModelConnectionId::new();
        let store = PlaintextSqliteSecretStore::new(path);

        let error = store
            .save(connection_id, &SecretString::from(" sk-trimmed".to_owned()))
            .expect_err("trimmed local secret must fail");

        assert_eq!(error.code, ErrorCode::ModelSecretUnavailable);
        assert!(
            store
                .load(connection_id)
                .expect("invalid save did not persist")
                .is_none()
        );
    }
}
