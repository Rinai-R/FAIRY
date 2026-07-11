use fairy_domain::{ErrorCode, FairyError, Revision};
use serde::Serialize;
use tauri::{AppHandle, Emitter, Runtime};

pub mod character;
pub mod companion;
pub mod settings;

pub const CONFIGURATION_CHANGED_EVENT: &str = "companion-configuration-changed";

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(tag = "category", rename_all = "snake_case")]
pub enum ConfigurationChange {
    Character { revision: Revision },
    UserProfile { revision: Option<Revision> },
    Model { configured: bool, ready: bool },
}

pub fn emit_configuration_change<R: Runtime>(
    app: &AppHandle<R>,
    change: ConfigurationChange,
) -> Result<(), crate::app_error::AppError> {
    app.emit(CONFIGURATION_CHANGED_EVENT, change).map_err(|_| {
        crate::app_error::AppError::from(FairyError::new(
            ErrorCode::IpcChannelClosed,
            "无法广播 Companion 配置变更",
            true,
        ))
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn configuration_change_payload_is_public_and_secret_free() {
        for change in [
            ConfigurationChange::Character {
                revision: Revision::INITIAL,
            },
            ConfigurationChange::UserProfile { revision: None },
            ConfigurationChange::Model {
                configured: true,
                ready: true,
            },
        ] {
            let json = serde_json::to_string(&change).expect("serialize configuration change");
            for forbidden in ["apiKey", "api_key", "secret", "connectionId", "endpoint"] {
                assert!(!json.contains(forbidden));
            }
        }
    }
}
