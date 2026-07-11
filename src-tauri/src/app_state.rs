use std::path::Path;
use std::sync::{Arc, Mutex, MutexGuard};

use fairy_domain::{
    AuthMode, ConversationId, ErrorCode, FairyError, IntelligenceStoreSummary, KnowledgeCatalog,
    KnowledgeId, KnowledgeRecord, ModelConnectionConfig, ModelConnectionInput, ModelProtocol,
    SearchConnectionConfig, SearchConnectionInput, SearchProvider,
};
use fairy_harness::{CompanionIntelligence, HarnessRuntime, IntelligenceBinding, WebSearchGateway};
use fairy_intelligence::{BraveSearchGateway, IntelligenceStore};
use fairy_model_openai::build_openai_compatible_gateway;
use fairy_storage::{
    CharacterStore, ModelConnectionStore, SearchConnectionStore, StorageRoot,
    SystemSearchSecretStore, SystemSecretStore, UserProfileStore,
};
use secrecy::SecretString;
use serde::Serialize;

use crate::app_error::AppError;

pub struct AppState {
    pub characters: CharacterStore,
    pub user_profiles: UserProfileStore,
    model_connections: ModelConnectionStore<SystemSecretStore>,
    search_connections: SearchConnectionStore<SystemSearchSecretStore>,
    search_gateway: Mutex<Option<Arc<dyn WebSearchGateway + Send + Sync>>>,
    search_error: Mutex<Option<FairyError>>,
    intelligence: Option<Arc<IntelligenceStore>>,
    intelligence_error: Option<FairyError>,
    runtime: Mutex<Option<Arc<HarnessRuntime>>>,
    model_error: Mutex<Option<AppError>>,
    active_conversation_id: Mutex<Option<ConversationId>>,
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
    pub protocol: ModelProtocol,
    pub endpoint: String,
    pub model: String,
    pub auth_mode: AuthMode,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SearchConnectionStatus {
    pub configured: bool,
    pub ready: bool,
    pub config: Option<PublicSearchConnection>,
    pub error: Option<AppError>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PublicSearchConnection {
    pub provider: SearchProvider,
    pub endpoint: String,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct IntelligenceStatus {
    pub ready: bool,
    pub schema_version: Option<i64>,
    pub summary: Option<IntelligenceStoreSummary>,
    pub active_background_jobs: usize,
    pub error: Option<AppError>,
}

impl From<&ModelConnectionConfig> for PublicModelConnection {
    fn from(config: &ModelConnectionConfig) -> Self {
        Self {
            protocol: config.protocol(),
            endpoint: config.endpoint().to_owned(),
            model: config.model().to_owned(),
            auth_mode: config.auth_mode(),
        }
    }
}

impl From<&SearchConnectionConfig> for PublicSearchConnection {
    fn from(config: &SearchConnectionConfig) -> Self {
        Self {
            provider: config.provider(),
            endpoint: config.endpoint().to_owned(),
        }
    }
}

impl AppState {
    pub fn initialize(config_directory: impl AsRef<Path>) -> Result<Self, AppError> {
        let root = StorageRoot::new(config_directory).map_err(AppError::from)?;
        let characters = CharacterStore::new(root.clone());
        let user_profiles = UserProfileStore::new(root.clone());
        let model_connections = ModelConnectionStore::new(root.clone(), SystemSecretStore);
        let search_connections = SearchConnectionStore::new(root.clone(), SystemSearchSecretStore);
        let (intelligence, intelligence_error) =
            match IntelligenceStore::open(root.directory().join("intelligence/fairy.sqlite3")) {
                Ok(store) => (Some(Arc::new(store)), None),
                Err(error) => (None, Some(error)),
            };
        let (search_gateway, search_error) = match search_connections.resolve() {
            Ok(connection) => match build_search_gateway(connection.config, connection.api_key) {
                Ok(gateway) => (Some(gateway), None),
                Err(error) => (None, Some(error)),
            },
            Err(error) => (None, Some(error)),
        };
        let (runtime, model_error) = match model_connections.resolve() {
            Ok(connection) => match build_runtime(connection.config, connection.api_key) {
                Ok(runtime) => (Some(Arc::new(runtime)), None),
                Err(error) => (None, Some(AppError::from(error))),
            },
            Err(error) => (None, Some(AppError::from(error))),
        };
        if let Some(runtime) = runtime.as_ref() {
            runtime.replace_web_search_gateway(search_gateway.clone());
            runtime.replace_intelligence_binding(intelligence_binding(
                intelligence.as_ref(),
                intelligence_error.as_ref(),
            ));
        }

        Ok(Self {
            characters,
            user_profiles,
            model_connections,
            search_connections,
            search_gateway: Mutex::new(search_gateway),
            search_error: Mutex::new(search_error),
            intelligence,
            intelligence_error,
            runtime: Mutex::new(runtime),
            model_error: Mutex::new(model_error),
            active_conversation_id: Mutex::new(None),
        })
    }

    pub fn runtime(&self) -> Result<Arc<HarnessRuntime>, AppError> {
        if let Some(runtime) = self.runtime_lock()?.as_ref() {
            return Ok(Arc::clone(runtime));
        }
        match self.model_error_lock()?.clone() {
            Some(error) => Err(error),
            None => Err(AppError::from(FairyError::new(
                ErrorCode::ModelConfigRequired,
                "请先配置模型连接",
                false,
            ))),
        }
    }

    pub fn register_active_conversation(
        &self,
        conversation_id: ConversationId,
    ) -> Result<(), AppError> {
        *self.active_conversation_lock()? = Some(conversation_id);
        Ok(())
    }

    pub fn active_conversation_id(&self) -> Result<Option<ConversationId>, AppError> {
        Ok(*self.active_conversation_lock()?)
    }

    pub fn model_status(&self) -> ModelConnectionStatus {
        match self.model_connections.status() {
            Ok(config) => {
                let (ready, error) = match self.runtime_lock() {
                    Ok(runtime) if runtime.is_some() => (true, None),
                    Ok(_) => match self.model_error_lock() {
                        Ok(error) => match error.clone() {
                            Some(error) => (false, Some(error)),
                            None => (false, Some(AppError::state_unavailable())),
                        },
                        Err(error) => (false, Some(error)),
                    },
                    Err(error) => (false, Some(error)),
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

    pub fn search_status(&self) -> SearchConnectionStatus {
        match self.search_connections.status() {
            Ok(Some(config)) => {
                let (ready, error) = match self.search_gateway_lock() {
                    Ok(gateway) if gateway.is_some() => (true, None),
                    Ok(_) => match self.search_error_lock() {
                        Ok(error) => match error.clone() {
                            Some(error) => (false, Some(AppError::from(error))),
                            None => (false, Some(AppError::state_unavailable())),
                        },
                        Err(error) => (false, Some(error)),
                    },
                    Err(error) => (false, Some(error)),
                };
                SearchConnectionStatus {
                    configured: true,
                    ready,
                    config: Some(PublicSearchConnection::from(&config)),
                    error,
                }
            }
            Ok(None) => SearchConnectionStatus {
                configured: false,
                ready: false,
                config: None,
                error: Some(AppError::from(FairyError::new(
                    ErrorCode::SearchConfigRequired,
                    "请先配置 Brave Search 连接",
                    false,
                ))),
            },
            Err(error) => SearchConnectionStatus {
                configured: false,
                ready: false,
                config: None,
                error: Some(AppError::from(error)),
            },
        }
    }

    pub fn intelligence_status(&self) -> IntelligenceStatus {
        let active_runtime = match self.runtime_lock() {
            Ok(runtime) => runtime.as_ref().cloned(),
            Err(error) => {
                return IntelligenceStatus {
                    ready: false,
                    schema_version: None,
                    summary: None,
                    active_background_jobs: 0,
                    error: Some(error),
                };
            }
        };
        let (active_background_jobs, background_error) = match active_runtime.as_ref() {
            Some(runtime) => (
                runtime.active_background_jobs(),
                runtime.last_intelligence_background_error(),
            ),
            None => (0, None),
        };
        let Some(store) = self.intelligence.as_ref() else {
            let error = match (self.intelligence_error.clone(), background_error) {
                (Some(error), _) => AppError::from(error),
                (None, Some(error)) => AppError::from(error),
                (None, None) => AppError::state_unavailable(),
            };
            return IntelligenceStatus {
                ready: false,
                schema_version: None,
                summary: None,
                active_background_jobs,
                error: Some(error),
            };
        };
        match (store.schema_version(), store.summary()) {
            (Ok(schema_version), Ok(summary)) => IntelligenceStatus {
                ready: true,
                schema_version: Some(schema_version),
                summary: Some(summary),
                active_background_jobs,
                error: background_error.map(AppError::from),
            },
            (Err(error), _) | (_, Err(error)) => IntelligenceStatus {
                ready: false,
                schema_version: None,
                summary: None,
                active_background_jobs,
                error: Some(AppError::from(error)),
            },
        }
    }

    pub fn knowledge_catalog(&self) -> Result<KnowledgeCatalog, AppError> {
        self.intelligence_store()?
            .knowledge_catalog()
            .map_err(AppError::from)
    }

    pub fn confirm_knowledge_candidate(
        &self,
        id: KnowledgeId,
    ) -> Result<KnowledgeRecord, AppError> {
        self.intelligence_store()?
            .confirm_knowledge_candidate(id)
            .map_err(AppError::from)
    }

    pub fn tombstone_knowledge(&self, id: KnowledgeId) -> Result<(), AppError> {
        self.intelligence_store()?
            .tombstone_knowledge(id)
            .map_err(AppError::from)
    }

    pub fn save_search_connection(
        &self,
        input: SearchConnectionInput,
        api_key: Option<String>,
    ) -> Result<SearchConnectionStatus, AppError> {
        let config = self
            .search_connections
            .save(input, api_key.map(SecretString::from))
            .map_err(AppError::from)?;
        let resolved = self.search_connections.resolve().map_err(AppError::from)?;
        let gateway =
            build_search_gateway(resolved.config, resolved.api_key).map_err(AppError::from)?;
        *self.search_gateway_lock()? = Some(gateway.clone());
        *self.search_error_lock()? = None;
        if let Some(runtime) = self.runtime_lock()?.as_ref() {
            runtime.replace_web_search_gateway(Some(gateway));
        }
        Ok(SearchConnectionStatus {
            configured: true,
            ready: true,
            config: Some(PublicSearchConnection::from(&config)),
            error: None,
        })
    }

    pub fn clear_search_connection(&self) -> Result<SearchConnectionStatus, AppError> {
        self.search_connections.clear().map_err(AppError::from)?;
        *self.search_gateway_lock()? = None;
        let error = FairyError::new(
            ErrorCode::SearchConfigRequired,
            "请先配置 Brave Search 连接",
            false,
        );
        *self.search_error_lock()? = Some(error.clone());
        if let Some(runtime) = self.runtime_lock()?.as_ref() {
            runtime.replace_web_search_gateway(None);
        }
        Ok(SearchConnectionStatus {
            configured: false,
            ready: false,
            config: None,
            error: Some(AppError::from(error)),
        })
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
        let gateway = build_openai_compatible_gateway(connection.config, connection.api_key)
            .map_err(AppError::from)?;

        let mut runtime = self.runtime_lock()?;
        if let Some(current) = runtime.as_ref() {
            current
                .replace_gateway(model, gateway)
                .map_err(AppError::from)?;
        } else {
            let new_runtime =
                Arc::new(HarnessRuntime::new(model, gateway).map_err(AppError::from)?);
            new_runtime.replace_web_search_gateway(self.search_gateway_lock()?.clone());
            new_runtime.replace_intelligence_binding(intelligence_binding(
                self.intelligence.as_ref(),
                self.intelligence_error.as_ref(),
            ));
            *runtime = Some(new_runtime);
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
        *self.active_conversation_lock()? = None;
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

    fn search_gateway_lock(
        &self,
    ) -> Result<MutexGuard<'_, Option<Arc<dyn WebSearchGateway + Send + Sync>>>, AppError> {
        self.search_gateway
            .lock()
            .map_err(|_| AppError::state_unavailable())
    }

    fn search_error_lock(&self) -> Result<MutexGuard<'_, Option<FairyError>>, AppError> {
        self.search_error
            .lock()
            .map_err(|_| AppError::state_unavailable())
    }

    fn active_conversation_lock(&self) -> Result<MutexGuard<'_, Option<ConversationId>>, AppError> {
        self.active_conversation_id
            .lock()
            .map_err(|_| AppError::state_unavailable())
    }

    fn intelligence_store(&self) -> Result<&Arc<IntelligenceStore>, AppError> {
        match self.intelligence.as_ref() {
            Some(store) => Ok(store),
            None => match self.intelligence_error.clone() {
                Some(error) => Err(AppError::from(error)),
                None => Err(AppError::state_unavailable()),
            },
        }
    }
}

fn intelligence_binding(
    store: Option<&Arc<IntelligenceStore>>,
    error: Option<&FairyError>,
) -> IntelligenceBinding {
    if let Some(store) = store {
        let provider: Arc<dyn CompanionIntelligence + Send + Sync> = store.clone();
        IntelligenceBinding::Available(provider)
    } else if let Some(error) = error {
        IntelligenceBinding::Unavailable(error.clone())
    } else {
        IntelligenceBinding::Unavailable(FairyError::new(
            ErrorCode::IntelligenceUnavailable,
            "本地智能层不可用",
            false,
        ))
    }
}

fn build_search_gateway(
    config: SearchConnectionConfig,
    api_key: SecretString,
) -> Result<Arc<dyn WebSearchGateway + Send + Sync>, FairyError> {
    Ok(Arc::new(BraveSearchGateway::new(config, api_key)?))
}

fn build_runtime(
    config: ModelConnectionConfig,
    api_key: Option<SecretString>,
) -> Result<HarnessRuntime, FairyError> {
    let model = config.model().to_owned();
    let gateway = build_openai_compatible_gateway(config, api_key)?;
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
    fn search_and_intelligence_statuses_are_explicit_and_secret_free() {
        let directory = tempfile::tempdir().expect("create app state directory");
        let state = AppState::initialize(directory.path()).expect("initialize app state");

        let search = state.search_status();
        assert!(!search.configured);
        assert!(!search.ready);
        assert_eq!(
            search.error.as_ref().expect("missing search error").code,
            "SEARCH_CONFIG_REQUIRED"
        );

        let intelligence = state.intelligence_status();
        assert!(intelligence.ready);
        assert_eq!(intelligence.schema_version, Some(2));
        assert_eq!(
            intelligence
                .summary
                .as_ref()
                .expect("intelligence summary")
                .active_personal_memories,
            0
        );

        let json = serde_json::to_string(&(search, intelligence)).expect("serialize statuses");
        for forbidden in [
            "apiKey",
            "api_key",
            "secret",
            "connectionId",
            "connection_id",
        ] {
            assert!(
                !json.contains(forbidden),
                "leaked forbidden field: {forbidden}"
            );
        }
    }

    #[test]
    fn intelligence_open_failure_is_reported_without_blocking_model_status() {
        let directory = tempfile::tempdir().expect("create app state directory");
        std::fs::create_dir_all(
            directory
                .path()
                .join("harness/v1/intelligence/fairy.sqlite3"),
        )
        .expect("create path collision");

        let state = AppState::initialize(directory.path()).expect("initialize app state");
        let intelligence = state.intelligence_status();

        assert!(!intelligence.ready);
        assert_eq!(intelligence.schema_version, None);
        assert_eq!(intelligence.summary, None);
        assert_eq!(
            intelligence
                .error
                .expect("explicit intelligence open error")
                .code,
            "STORAGE_IO"
        );
        assert_eq!(
            state
                .model_status()
                .error
                .expect("model status remains queryable")
                .code,
            "MODEL_CONFIG_REQUIRED"
        );
        assert_eq!(
            state
                .knowledge_catalog()
                .expect_err("catalog failure remains explicit")
                .code,
            "STORAGE_IO"
        );
    }

    #[test]
    fn knowledge_management_returns_public_records_after_store_success() {
        let directory = tempfile::tempdir().expect("create app state directory");
        let state = AppState::initialize(directory.path()).expect("initialize app state");
        let candidate = state
            .intelligence_store()
            .expect("intelligence store")
            .append_knowledge(fairy_domain::NewKnowledge {
                topic: "本地知识".to_owned(),
                statement: "这是一条由用户确认后可用的知识".to_owned(),
                confidence_basis_points: 8500,
                source_conversation_id: ConversationId::new(),
                source_turn_id: fairy_domain::TurnId::new(),
                supersedes_id: None,
                sources: Vec::new(),
            })
            .expect("append candidate");

        let catalog = state.knowledge_catalog().expect("knowledge catalog");
        assert_eq!(catalog.candidates.len(), 1);
        assert!(catalog.verified.is_empty());
        let confirmed = state
            .confirm_knowledge_candidate(candidate.id)
            .expect("confirm candidate");
        assert_eq!(
            confirmed.verification_basis,
            fairy_domain::KnowledgeVerificationBasis::UserConfirmed
        );
        let json = serde_json::to_string(&confirmed).expect("serialize confirmed record");
        for forbidden in ["apiKey", "api_key", "secret", "connectionId"] {
            assert!(!json.contains(forbidden));
        }

        state
            .tombstone_knowledge(candidate.id)
            .expect("tombstone confirmed knowledge");
        let empty = state.knowledge_catalog().expect("catalog after tombstone");
        assert!(empty.candidates.is_empty());
        assert!(empty.verified.is_empty());
    }

    #[test]
    fn configured_status_is_ready_and_never_serializes_secret_or_internal_id() {
        let directory = tempfile::tempdir().expect("create app state directory");
        let state = AppState::initialize(directory.path()).expect("initialize app state");
        let status = state
            .save_model_connection(
                ModelConnectionInput {
                    protocol: ModelProtocol::Responses,
                    endpoint: "http://127.0.0.1:11434/v1".to_owned(),
                    model: "local-model".to_owned(),
                    auth_mode: AuthMode::NoAuth,
                },
                None,
            )
            .expect("save no-auth connection");
        let json = serde_json::to_string(&status).expect("serialize status");

        assert!(status.configured);
        assert!(status.ready);
        assert!(state.runtime().is_ok());
        assert!(json.contains("authMode"));
        assert!(json.contains("protocol"));
        assert!(json.contains("responses"));
        assert!(!json.contains("promptCacheKey"));
        assert!(!json.contains("cachedTokensUsage"));
        assert!(!json.contains("apiKey"));
        assert!(!json.contains("api_key"));
        assert!(!json.contains("connectionId"));
        assert!(!json.contains("connection_id"));
    }

    #[test]
    fn active_conversation_is_explicit_and_clears_with_model_runtime() {
        let directory = tempfile::tempdir().expect("create app state directory");
        let state = AppState::initialize(directory.path()).expect("initialize app state");
        assert_eq!(
            state.active_conversation_id().expect("read active id"),
            None
        );
        let conversation_id = ConversationId::new();
        state
            .register_active_conversation(conversation_id)
            .expect("register active conversation");
        assert_eq!(
            state.active_conversation_id().expect("read active id"),
            Some(conversation_id)
        );

        state
            .clear_model_connection()
            .expect("clear model connection");
        assert_eq!(
            state.active_conversation_id().expect("read active id"),
            None
        );
    }
}
