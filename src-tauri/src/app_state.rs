use std::path::Path;
use std::sync::{Arc, Mutex, MutexGuard};

use fairy_domain::{
    AuthMode, CharacterId, ConversationId, ErrorCode, ExtractionBatchCatalog, ExtractionBatchId,
    FairyError, IntelligenceStoreSummary, KnowledgeCatalog, KnowledgeId, KnowledgeRecord,
    MemoryScope, ModelConnectionConfig, ModelConnectionInput, ModelProtocol, NewPersonalMemory,
    PersonalMemoryCatalog, PersonalMemoryId, PersonalMemoryKind, PersonalMemoryRecord, TurnId,
};
use fairy_harness::{CompactionPolicy, CompanionPersistence, HarnessRuntime, PersistenceBinding};
use fairy_intelligence::IntelligenceStore;
use fairy_model_openai::build_openai_compatible_gateway;
use fairy_storage::{
    CharacterAppearanceStore, CharacterStore, ModelConnectionStore, PlaintextSqliteSecretStore,
    StorageRoot, UserProfileStore, cleanup_legacy_search_artifacts,
};
use secrecy::SecretString;
use serde::Serialize;

use crate::{app_error::AppError, visual_registry::VisualPackRegistry};

pub struct AppState {
    pub characters: CharacterStore,
    pub character_appearances: CharacterAppearanceStore,
    pub visual_packs: VisualPackRegistry,
    pub user_profiles: UserProfileStore,
    model_connections: ModelConnectionStore<PlaintextSqliteSecretStore>,
    intelligence: Option<Arc<IntelligenceStore>>,
    intelligence_error: Option<FairyError>,
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
    pub protocol: ModelProtocol,
    pub endpoint: String,
    pub model: String,
    pub context_window_tokens: u64,
    pub auth_mode: AuthMode,
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
            context_window_tokens: config.context_window_tokens(),
            auth_mode: config.auth_mode(),
        }
    }
}

impl AppState {
    pub fn initialize(config_directory: impl AsRef<Path>) -> Result<Self, AppError> {
        Self::initialize_with_runtime_override(config_directory, None)
    }

    fn initialize_with_runtime_override(
        config_directory: impl AsRef<Path>,
        runtime_override: Option<HarnessRuntime>,
    ) -> Result<Self, AppError> {
        let root = StorageRoot::new(config_directory).map_err(AppError::from)?;
        let characters = CharacterStore::new(root.clone());
        let character_appearances = CharacterAppearanceStore::new(root.clone());
        let visual_packs = VisualPackRegistry::local(root.directory()).map_err(AppError::from)?;
        let user_profiles = UserProfileStore::new(root.clone());
        let model_connections = ModelConnectionStore::new(
            root.clone(),
            PlaintextSqliteSecretStore::new(root.directory().join("model/secrets.sqlite3")),
        );
        if let Err(error) = cleanup_legacy_search_artifacts(&root) {
            eprintln!("FAIRY legacy search cleanup warning: {error}");
        }
        let (intelligence, intelligence_error) =
            match IntelligenceStore::open(root.directory().join("intelligence/fairy.sqlite3")) {
                Ok(store) => (Some(Arc::new(store)), None),
                Err(error) => (None, Some(error)),
            };
        let (runtime, model_error) = if let Some(runtime) = runtime_override {
            (Some(Arc::new(runtime)), None)
        } else {
            match model_connections.resolve() {
                Ok(connection) => match build_runtime(connection.config, connection.api_key) {
                    Ok(runtime) => (Some(Arc::new(runtime)), None),
                    Err(error) => (None, Some(AppError::from(error))),
                },
                Err(error) => (None, Some(AppError::from(error))),
            }
        };
        if let Some(runtime) = runtime.as_ref() {
            runtime.replace_persistence_binding(persistence_binding(
                intelligence.as_ref(),
                intelligence_error.as_ref(),
            ));
        }

        Ok(Self {
            characters,
            character_appearances,
            visual_packs,
            user_profiles,
            model_connections,
            intelligence,
            intelligence_error,
            runtime: Mutex::new(runtime),
            model_error: Mutex::new(model_error),
        })
    }

    #[cfg(test)]
    pub fn initialize_with_runtime(
        config_directory: impl AsRef<Path>,
        runtime: HarnessRuntime,
    ) -> Result<Self, AppError> {
        Self::initialize_with_runtime_override(config_directory, Some(runtime))
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

    pub fn personal_memory_catalog(
        &self,
        character_id: CharacterId,
    ) -> Result<PersonalMemoryCatalog, AppError> {
        self.intelligence_store()?
            .personal_memory_catalog(character_id)
            .map_err(AppError::from)
    }

    pub fn extraction_batch_catalog(
        &self,
        character_id: CharacterId,
    ) -> Result<ExtractionBatchCatalog, AppError> {
        self.intelligence_store()?
            .extraction_batch_catalog(character_id)
            .map_err(AppError::from)
    }

    pub fn create_personal_memory(
        &self,
        kind: PersonalMemoryKind,
        scope: MemoryScope,
        content: String,
        confidence_basis_points: u16,
    ) -> Result<PersonalMemoryRecord, AppError> {
        self.intelligence_store()?
            .append_personal_memory(NewPersonalMemory {
                kind,
                scope,
                content,
                confidence_basis_points,
                source_conversation_id: ConversationId::new(),
                source_turn_id: TurnId::new(),
                supersedes_id: None,
            })
            .map_err(AppError::from)
    }

    pub fn revise_personal_memory(
        &self,
        id: PersonalMemoryId,
        content: String,
        confidence_basis_points: u16,
    ) -> Result<PersonalMemoryRecord, AppError> {
        self.intelligence_store()?
            .revise_personal_memory(id, content, confidence_basis_points)
            .map_err(AppError::from)
    }

    pub fn tombstone_personal_memory(&self, id: PersonalMemoryId) -> Result<(), AppError> {
        self.intelligence_store()?
            .tombstone_personal_memory(id)
            .map_err(AppError::from)
    }

    pub fn assign_legacy_relationship(
        &self,
        id: PersonalMemoryId,
        character_id: CharacterId,
    ) -> Result<PersonalMemoryRecord, AppError> {
        self.intelligence_store()?
            .assign_legacy_relationship(id, character_id)
            .map_err(AppError::from)
    }

    pub fn retry_extraction_batch(
        &self,
        id: ExtractionBatchId,
    ) -> Result<ConversationId, AppError> {
        self.intelligence_store()?
            .retry_failed_extraction_batch(id)
            .map_err(AppError::from)
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
        let compaction_policy =
            CompactionPolicy::from_context_window_tokens(connection.config.context_window_tokens());
        let gateway = build_openai_compatible_gateway(connection.config, connection.api_key)
            .map_err(AppError::from)?;

        let mut runtime = self.runtime_lock()?;
        if let Some(current) = runtime.as_ref() {
            current
                .replace_gateway_with_compaction_policy(model, gateway, compaction_policy)
                .map_err(AppError::from)?;
        } else {
            let new_runtime = Arc::new(
                HarnessRuntime::new_with_compaction_policy(model, gateway, compaction_policy)
                    .map_err(AppError::from)?,
            );
            new_runtime.replace_persistence_binding(persistence_binding(
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

fn persistence_binding(
    store: Option<&Arc<IntelligenceStore>>,
    error: Option<&FairyError>,
) -> PersistenceBinding {
    if let Some(store) = store {
        let provider: Arc<dyn CompanionPersistence + Send + Sync> = store.clone();
        PersistenceBinding::Available(provider)
    } else if let Some(error) = error {
        PersistenceBinding::Unavailable(error.clone())
    } else {
        PersistenceBinding::Unavailable(FairyError::new(
            ErrorCode::IntelligenceUnavailable,
            "本地智能层不可用",
            false,
        ))
    }
}

fn build_runtime(
    config: ModelConnectionConfig,
    api_key: Option<SecretString>,
) -> Result<HarnessRuntime, FairyError> {
    let model = config.model().to_owned();
    let compaction_policy =
        CompactionPolicy::from_context_window_tokens(config.context_window_tokens());
    let gateway = build_openai_compatible_gateway(config, api_key)?;
    HarnessRuntime::new_with_compaction_policy(model, gateway, compaction_policy)
}

#[cfg(test)]
mod tests {
    use fairy_domain::{ConversationId, DEFAULT_MODEL_CONTEXT_WINDOW_TOKENS};

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
    fn intelligence_status_is_explicit_and_secret_free() {
        let directory = tempfile::tempdir().expect("create app state directory");
        let state = AppState::initialize(directory.path()).expect("initialize app state");

        let intelligence = state.intelligence_status();
        assert!(intelligence.ready);
        assert_eq!(intelligence.schema_version, Some(3));
        assert_eq!(
            intelligence
                .summary
                .as_ref()
                .expect("intelligence summary")
                .active_global_memories,
            0
        );

        let json = serde_json::to_string(&intelligence).expect("serialize status");
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
    fn personal_memory_management_is_scoped_and_secret_free() {
        let directory = tempfile::tempdir().expect("create app state directory");
        let state = AppState::initialize(directory.path()).expect("initialize app state");
        let character_id = CharacterId::new();
        let created = state
            .create_personal_memory(
                PersonalMemoryKind::Relationship,
                MemoryScope::Character { character_id },
                "用户愿意下次继续聊这个话题".to_owned(),
                9000,
            )
            .expect("create relationship memory");
        let catalog = state
            .personal_memory_catalog(character_id)
            .expect("read personal catalog");
        assert_eq!(catalog.character, vec![created.clone()]);
        let revised = state
            .revise_personal_memory(created.id, "用户明确说下次继续聊这个话题".to_owned(), 9500)
            .expect("revise relationship memory");
        assert_eq!(revised.supersedes_id, Some(created.id));
        state
            .tombstone_personal_memory(revised.id)
            .expect("tombstone relationship memory");
        assert!(
            state
                .personal_memory_catalog(character_id)
                .expect("catalog after tombstone")
                .character
                .is_empty()
        );
        let json = serde_json::to_string(&created).expect("serialize memory");
        for forbidden in ["apiKey", "api_key", "secret", "connectionId"] {
            assert!(!json.contains(forbidden));
        }
    }

    #[test]
    fn bearer_key_persists_in_local_sqlite_and_public_status_stays_secret_free() {
        let directory = tempfile::tempdir().expect("create app state directory");
        let state = AppState::initialize(directory.path()).expect("initialize app state");
        let raw_secret = "sk-local-sqlite-app-state";

        let status = state
            .save_model_connection(
                ModelConnectionInput {
                    protocol: ModelProtocol::ChatCompletions,
                    endpoint: "https://api.deepseek.com".to_owned(),
                    model: "deepseek-v4-flash".to_owned(),
                    context_window_tokens: DEFAULT_MODEL_CONTEXT_WINDOW_TOKENS,
                    auth_mode: AuthMode::BearerKey,
                },
                Some(raw_secret.to_owned()),
            )
            .expect("save bearer connection");
        let secret_database = directory.path().join("harness/v1/model/secrets.sqlite3");

        assert!(secret_database.is_file());
        assert!(status.configured);
        assert!(status.ready);
        let json = serde_json::to_string(&status).expect("serialize bearer status");
        assert!(!json.contains(raw_secret));
        for forbidden in ["apiKey", "api_key", "secret", "connectionId"] {
            assert!(!json.contains(forbidden));
        }

        let reopened = AppState::initialize(directory.path()).expect("reopen app state");
        let reopened_status = reopened.model_status();
        assert!(reopened_status.configured);
        assert!(reopened_status.ready);
        assert!(reopened.runtime().is_ok());
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
                    context_window_tokens: DEFAULT_MODEL_CONTEXT_WINDOW_TOKENS,
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
        assert!(json.contains("contextWindowTokens"));
        assert!(json.contains("responses"));
        assert!(!json.contains("promptCacheKey"));
        assert!(!json.contains("cachedTokensUsage"));
        assert!(!json.contains("apiKey"));
        assert!(!json.contains("api_key"));
        assert!(!json.contains("connectionId"));
        assert!(!json.contains("connection_id"));
    }
}
