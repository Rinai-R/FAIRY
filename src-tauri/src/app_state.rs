use std::path::Path;
use std::sync::{Arc, Mutex, MutexGuard};

use fairy_domain::{AuthMode, ErrorCode, FairyError, ModelConnectionConfig, ModelConnectionInput};
use fairy_harness::HarnessRuntime;
use fairy_model_openai::OpenAiResponsesGateway;
use fairy_storage::{
    CharacterStore, ModelConnectionStore, StorageRoot, SystemSecretStore, UserProfileStore,
};
use secrecy::SecretString;
use serde::Serialize;

use crate::app_error::AppError;

pub struct AppState {
    pub characters: CharacterStore,
    pub user_profiles: UserProfileStore,
    model_connections: ModelConnectionStore<SystemSecretStore>,
    runtime: Mutex<Option<Arc<HarnessRuntime>>>,
    model_error: Mutex<Option<AppError>>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ModelConnectionStatus {
    pub configured: bool,
    pub ready: bool,
    pub config: Option<PublicModelConnection>,
    pub error: Option<AppError>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PublicModelConnection {
    pub endpoint: String,
    pub model: String,
    pub auth_mode: AuthMode,
    pub prompt_cache_key: bool,
    pub cached_tokens_usage: bool,
}

impl From<&ModelConnectionConfig> for PublicModelConnection {
    fn from(config: &ModelConnectionConfig) -> Self {
        let capabilities = config.capabilities();
        Self {
            endpoint: config.endpoint().to_owned(),
            model: config.model().to_owned(),
            auth_mode: config.auth_mode(),
            prompt_cache_key: capabilities.prompt_cache_key,
            cached_tokens_usage: capabilities.cached_tokens_usage,
        }
    }
}

impl AppState {
    pub fn initialize(config_directory: impl AsRef<Path>) -> Result<Self, AppError> {
        let root = StorageRoot::new(config_directory).map_err(AppError::from)?;
        let characters = CharacterStore::new(root.clone());
        let user_profiles = UserProfileStore::new(root.clone());
        let model_connections = ModelConnectionStore::new(root, SystemSecretStore);
        let (runtime, model_error) = match model_connections.resolve() {
            Ok(connection) => match build_runtime(connection.config, connection.api_key) {
                Ok(runtime) => (Some(Arc::new(runtime)), None),
                Err(error) => (None, Some(AppError::from(error))),
            },
            Err(error) => (None, Some(AppError::from(error))),
        };

        Ok(Self {
            characters,
            user_profiles,
            model_connections,
            runtime: Mutex::new(runtime),
            model_error: Mutex::new(model_error),
        })
    }

    pub fn runtime(&self) -> Result<Arc<HarnessRuntime>, AppError> {
        if let Some(runtime) = self.runtime_lock()?.as_ref() {
            return Ok(Arc::clone(runtime));
        }
        Err(self.model_error_lock()?.clone().unwrap_or_else(|| {
            AppError::from(FairyError::new(
                ErrorCode::ModelConfigRequired,
                "请先配置模型连接",
                false,
            ))
        }))
    }

    pub fn model_status(&self) -> ModelConnectionStatus {
        match self.model_connections.status() {
            Ok(config) => {
                let ready = self
                    .runtime_lock()
                    .map(|runtime| runtime.is_some())
                    .unwrap_or(false);
                let error = if ready {
                    None
                } else {
                    self.model_error_lock()
                        .ok()
                        .and_then(|error| error.clone())
                        .or_else(|| Some(AppError::state_unavailable()))
                };
                ModelConnectionStatus {
                    configured: config.is_some(),
                    ready,
                    config: config.as_ref().map(PublicModelConnection::from),
                    error,
                }
            }
            Err(error) => ModelConnectionStatus {
                configured: false,
                ready: false,
                config: None,
                error: Some(AppError::from(error)),
            },
        }
    }

    pub fn save_model_connection(
        &self,
        input: ModelConnectionInput,
        api_key: Option<String>,
    ) -> Result<ModelConnectionStatus, AppError> {
        let config = self
            .model_connections
            .save(input, api_key.map(SecretString::from))
            .map_err(AppError::from)?;
        let connection = self.model_connections.resolve().map_err(AppError::from)?;
        let model = connection.config.model().to_owned();
        let gateway = Arc::new(
            OpenAiResponsesGateway::new(connection.config, connection.api_key)
                .map_err(AppError::from)?,
        );

        let mut runtime = self.runtime_lock()?;
        if let Some(current) = runtime.as_ref() {
            current
                .replace_gateway(model, gateway)
                .map_err(AppError::from)?;
        } else {
            *runtime = Some(Arc::new(
                HarnessRuntime::new(model, gateway).map_err(AppError::from)?,
            ));
        }
        *self.model_error_lock()? = None;

        Ok(ModelConnectionStatus {
            configured: true,
            ready: true,
            config: Some(PublicModelConnection::from(&config)),
            error: None,
        })
    }

    pub fn clear_model_connection(&self) -> Result<ModelConnectionStatus, AppError> {
        if self
            .runtime_lock()?
            .as_ref()
            .is_some_and(|runtime| runtime.has_active_work())
        {
            return Err(AppError::from(FairyError::new(
                ErrorCode::TurnInProgress,
                "活动轮次或会话压缩期间不能清除模型连接",
                false,
            )));
        }
        self.model_connections.clear().map_err(AppError::from)?;
        *self.runtime_lock()? = None;
        let error = AppError::from(FairyError::new(
            ErrorCode::ModelConfigRequired,
            "请先配置模型连接",
            false,
        ));
        *self.model_error_lock()? = Some(error.clone());
        Ok(ModelConnectionStatus {
            configured: false,
            ready: false,
            config: None,
            error: Some(error),
        })
    }

    fn runtime_lock(&self) -> Result<MutexGuard<'_, Option<Arc<HarnessRuntime>>>, AppError> {
        self.runtime
            .lock()
            .map_err(|_| AppError::state_unavailable())
    }

    fn model_error_lock(&self) -> Result<MutexGuard<'_, Option<AppError>>, AppError> {
        self.model_error
            .lock()
            .map_err(|_| AppError::state_unavailable())
    }
}

fn build_runtime(
    config: ModelConnectionConfig,
    api_key: Option<SecretString>,
) -> Result<HarnessRuntime, FairyError> {
    let model = config.model().to_owned();
    let gateway = Arc::new(OpenAiResponsesGateway::new(config, api_key)?);
    HarnessRuntime::new(model, gateway)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_model_is_queryable_without_a_mock_runtime() {
        let directory = tempfile::tempdir().expect("create app state directory");
        let state = AppState::initialize(directory.path()).expect("initialize app state");

        let status = state.model_status();
        assert!(!status.configured);
        assert!(!status.ready);
        assert_eq!(
            status.error.expect("explicit missing config error").code,
            "MODEL_CONFIG_REQUIRED"
        );
        let runtime_error = match state.runtime() {
            Ok(_) => panic!("must not create mock runtime"),
            Err(error) => error,
        };
        assert_eq!(runtime_error.code, "MODEL_CONFIG_REQUIRED");
    }

    #[test]
    fn configured_status_is_ready_and_never_serializes_secret_or_internal_id() {
        let directory = tempfile::tempdir().expect("create app state directory");
        let state = AppState::initialize(directory.path()).expect("initialize app state");
        let status = state
            .save_model_connection(
                ModelConnectionInput {
                    endpoint: "http://127.0.0.1:11434/v1".to_owned(),
                    model: "local-model".to_owned(),
                    auth_mode: AuthMode::NoAuth,
                    prompt_cache_key: false,
                    cached_tokens_usage: false,
                },
                None,
            )
            .expect("save no-auth connection");
        let json = serde_json::to_string(&status).expect("serialize status");

        assert!(status.configured);
        assert!(status.ready);
        assert!(state.runtime().is_ok());
        assert!(json.contains("authMode"));
        assert!(json.contains("promptCacheKey"));
        assert!(!json.contains("apiKey"));
        assert!(!json.contains("api_key"));
        assert!(!json.contains("connectionId"));
        assert!(!json.contains("connection_id"));
    }
}
