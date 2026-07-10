use fairy_domain::{ErrorCode, FairyError, ModelConnectionId};
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
