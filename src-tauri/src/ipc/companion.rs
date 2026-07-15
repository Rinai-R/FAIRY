use fairy_domain::{
    ConversationBootstrap, ConversationId, ErrorCode, FairyError, HarnessEvent, TurnId,
    VisualStatePromptEntry,
};
use fairy_harness::{CompactionResult, HarnessEventSink, SessionSnapshot, TurnOutcome};
use fairy_storage::CharacterAppearanceRead;
use tauri::{State, ipc::Channel};

use crate::{app_error::AppError, app_state::AppState};

struct ChannelEventSink {
    channel: Channel<HarnessEvent>,
}

impl HarnessEventSink for ChannelEventSink {
    fn send(&mut self, event: HarnessEvent) -> Result<(), FairyError> {
        self.channel.send(event).map_err(|_| {
            FairyError::new(ErrorCode::IpcChannelClosed, "前端事件通道已经关闭", false)
        })
    }
}

#[tauri::command]
pub async fn create_companion_session(
    state: State<'_, AppState>,
) -> Result<ConversationBootstrap, AppError> {
    let runtime = state.runtime()?;
    let character = state
        .characters
        .active()
        .map_err(AppError::from)?
        .ok_or_else(|| {
            AppError::from(FairyError::new(
                ErrorCode::CharacterNotAvailable,
                "请先创建并激活角色",
                false,
            ))
        })?;
    let profile = state.user_profiles.current().map_err(AppError::from)?;
    runtime
        .open_or_create_character_session(character, profile)
        .await
        .map_err(AppError::from)
}

#[tauri::command]
pub fn get_companion_session(
    state: State<'_, AppState>,
    conversation_id: ConversationId,
) -> Result<SessionSnapshot, AppError> {
    state
        .runtime()?
        .session_snapshot(conversation_id)
        .map_err(AppError::from)
}

#[tauri::command]
pub async fn submit_companion_turn(
    state: State<'_, AppState>,
    conversation_id: ConversationId,
    input: String,
    speech_enabled: bool,
    on_event: Channel<HarnessEvent>,
) -> Result<TurnOutcome, AppError> {
    let runtime = state.runtime()?;
    let available_visual_states = active_visual_states(&state)?;
    let mut events = ChannelEventSink { channel: on_event };
    runtime
        .submit_turn_with_visual_states(
            conversation_id,
            input,
            speech_enabled,
            available_visual_states,
            &mut events,
        )
        .await
        .map_err(AppError::from)
}

#[tauri::command]
pub fn cancel_companion_turn(state: State<'_, AppState>, turn_id: TurnId) -> Result<(), AppError> {
    state
        .runtime()?
        .cancel_turn(turn_id)
        .map_err(AppError::from)
}

#[tauri::command]
pub async fn compact_companion_session(
    state: State<'_, AppState>,
    conversation_id: ConversationId,
) -> Result<CompactionResult, AppError> {
    state
        .runtime()?
        .compact_conversation(conversation_id)
        .await
        .map_err(AppError::from)
}

fn active_visual_states(state: &AppState) -> Result<Vec<VisualStatePromptEntry>, AppError> {
    let character = state
        .characters
        .active()
        .map_err(AppError::from)?
        .ok_or_else(|| AppError::from(character_not_available()))?;
    let binding = match state
        .character_appearances
        .read(character.character_id())
        .map_err(AppError::from)?
    {
        CharacterAppearanceRead::Assigned(binding) => binding,
        CharacterAppearanceRead::Unassigned => return Err(AppError::from(appearance_unassigned())),
    };
    let visual = state
        .visual_packs
        .get(&binding.visual_pack_id)
        .map_err(AppError::from)?;

    Ok(visual
        .states()
        .iter()
        .map(|state| VisualStatePromptEntry {
            id: state.id.clone(),
            description: state.description.clone(),
        })
        .collect())
}

fn appearance_unassigned() -> FairyError {
    FairyError::new(
        ErrorCode::CharacterAppearanceUnassigned,
        "该角色尚未选择外观",
        false,
    )
}

fn character_not_available() -> FairyError {
    FairyError::new(
        ErrorCode::CharacterNotAvailable,
        "请先创建并激活角色",
        false,
    )
}

#[cfg(test)]
mod tests {
    use std::collections::HashMap;
    use std::fs;
    use std::path::{Path, PathBuf};
    use std::sync::{Arc, Mutex};

    use std::str::FromStr;

    use async_trait::async_trait;
    use fairy_domain::{
        AuthMode, CharacterBriefInput, CompiledPromptRequest, ConversationId,
        DEFAULT_MODEL_CONTEXT_WINDOW_TOKENS, GatewayCapabilities, HarnessEvent,
        HarnessEventPayload, ModelCompletion, ModelConnectionCompiler, ModelConnectionId,
        ModelConnectionInput, ModelProtocol, ModelStreamEvent, Revision, TurnLifecycle, TurnState,
        VisualPackId,
    };
    use fairy_harness::{HarnessRuntime, ModelEventSink, ModelGateway};
    use fairy_model_openai::build_openai_compatible_gateway;
    use secrecy::SecretString;
    use serde_json::json;
    use tauri::{
        WebviewWindowBuilder,
        ipc::{CallbackFn, InvokeBody, InvokeResponseBody},
        test::{INVOKE_KEY, get_ipc_response, mock_builder, mock_context, noop_assets},
        webview::InvokeRequest,
    };
    use tokio_util::sync::CancellationToken;

    use crate::visual_registry::write_test_visual_pack;

    use super::*;

    const LOCAL_ENV_FILE: &str = ".env.persona.local";
    const EVENT_CHANNEL_ID: u32 = 42;

    #[test]
    fn channel_sink_serializes_the_exact_harness_event() {
        let bodies = Arc::new(Mutex::new(Vec::new()));
        let captured = Arc::clone(&bodies);
        let channel = Channel::new(move |body| {
            captured.lock().expect("lock channel bodies").push(body);
            Ok(())
        });
        let conversation_id = ConversationId::new();
        let turn_id = TurnId::new();
        let event = TurnLifecycle::new(conversation_id, turn_id)
            .transition(TurnState::Interpreting)
            .expect("create state event");
        let mut sink = ChannelEventSink { channel };

        sink.send(event.clone()).expect("send event");
        let bodies = bodies.lock().expect("lock captured bodies");
        let InvokeResponseBody::Json(json) = &bodies[0] else {
            panic!("harness event must use JSON IPC")
        };
        let decoded: HarnessEvent = serde_json::from_str(json).expect("decode event");

        assert_eq!(decoded, event);
        assert!(json.contains("\"conversationId\""));
        assert!(json.contains("\"turnId\""));
        assert!(!json.contains("conversation_id"));
        assert_eq!(decoded.sequence, 1);
        assert_eq!(decoded.payload, HarnessEventPayload::StateChanged);
    }

    #[test]
    fn active_visual_states_follow_the_assigned_visual_pack() {
        let directory = tempfile::tempdir().expect("temp directory");
        let state = AppState::initialize(directory.path()).expect("app state");
        write_test_visual_pack(
            state
                .visual_packs
                .root()
                .parent()
                .expect("visual root parent"),
            "fairy.atri",
        );
        let snapshot = state
            .characters
            .create(CharacterBriefInput {
                name: "亚托莉".to_owned(),
                description: "自然回应用户。".to_owned(),
                dialogue_style: None,
            })
            .expect("character");
        state
            .character_appearances
            .assign(
                snapshot.character_id(),
                VisualPackId::from_str("fairy.atri").expect("pack id"),
            )
            .expect("assign appearance");
        state
            .characters
            .activate(snapshot.character_id(), Revision::INITIAL)
            .expect("activate character");

        let states = active_visual_states(&state).expect("active visual states");

        assert_eq!(
            states
                .iter()
                .map(|state| state.id.as_str())
                .collect::<Vec<_>>(),
            vec!["idle", "happy"]
        );
        assert!(states.iter().all(|state| !state.description.is_empty()));
    }

    #[test]
    fn manual_e2e_history_isolation_removes_only_copied_database_unless_opted_in() {
        let directory = tempfile::tempdir().expect("temp directory");
        let database = directory
            .path()
            .join("harness")
            .join("v1")
            .join("intelligence")
            .join("fairy.sqlite3");
        fs::create_dir_all(database.parent().expect("database parent"))
            .expect("create copied intelligence directory");
        fs::write(&database, b"copied conversation history").expect("write copied database");

        assert!(
            isolate_manual_e2e_history(directory.path(), false).expect("isolate default history")
        );
        assert!(
            !database.exists(),
            "default manual E2E must remove copied sqlite history"
        );

        fs::write(&database, b"copied conversation history").expect("restore copied database");
        assert!(!isolate_manual_e2e_history(directory.path(), true).expect("keep copied history"));
        assert!(
            database.exists(),
            "KEEP_HISTORY manual E2E must preserve copied sqlite history"
        );
    }

    #[tokio::test]
    #[ignore = "manual E2E effect: set FAIRY_PERSONA_TEST_BASE_URL / MODEL / API_KEY or .env.persona.local"]
    async fn manual_companion_frontend_command_effect() -> Result<(), Box<dyn std::error::Error>> {
        let Some(config) = ManualE2eConfig::load()? else {
            eprintln!(
                "skipping manual companion E2E: set FAIRY_PERSONA_TEST_BASE_URL and FAIRY_PERSONA_TEST_MODEL in env or {LOCAL_ENV_FILE}"
            );
            return Ok(());
        };
        let source_config = app_config_source(&config)?;
        let temp = tempfile::tempdir()?;
        let test_config = copy_app_config_to_temp(&source_config, temp.path())?;
        let isolated_history = isolate_manual_e2e_history(&test_config, config.keep_history)?;
        let (runtime, gateway) = build_runtime_from_manual_config(&config)?;
        let state = AppState::initialize_with_runtime(&test_config, runtime)?;
        let character = state
            .characters
            .active()
            .map_err(AppError::from)?
            .ok_or_else(|| "active character is required for companion E2E")?;
        let profile = state.user_profiles.current().map_err(AppError::from)?;
        let visual_states = active_visual_states(&state)?;
        let event_bodies = Arc::new(Mutex::new(Vec::new()));
        let captured = Arc::clone(&event_bodies);

        println!("\n=== FAIRY manual companion frontend-command E2E ===");
        println!("frontend command: create_companion_session");
        println!("frontend command: submit_companion_turn");
        println!("config source: {}", source_config.display());
        println!("test config copy: {}", test_config.display());
        println!(
            "conversation history: {}",
            if isolated_history {
                "isolated fresh sqlite"
            } else {
                "kept from config copy"
            }
        );
        println!("model: {:?} {}", config.protocol, config.model);
        println!(
            "active character: {} revision {}",
            character.identity().name,
            character.revision().get()
        );
        println!(
            "character prompt chars: {}",
            character.identity().description.chars().count()
        );
        println!(
            "profile preferred name: {}",
            profile
                .as_ref()
                .and_then(|profile| profile.preferred_name())
                .unwrap_or("<none>")
        );
        println!(
            "available visual states: {}",
            visual_states
                .iter()
                .map(|state| state.id.as_str())
                .collect::<Vec<_>>()
                .join(", ")
        );
        println!("turn count: {}", config.user_inputs.len());

        let app = mock_builder()
            .manage(state)
            .channel_interceptor(move |_webview, callback, _index, body| {
                if callback.0 == EVENT_CHANNEL_ID {
                    captured
                        .lock()
                        .expect("lock manual E2E channel bodies")
                        .push(body.clone());
                    true
                } else {
                    false
                }
            })
            .invoke_handler(tauri::generate_handler![
                create_companion_session,
                submit_companion_turn
            ])
            .build(mock_context(noop_assets()))?;
        let webview = WebviewWindowBuilder::new(&app, "main", Default::default()).build()?;

        let session: ConversationBootstrap = get_ipc_response(
            &webview,
            ipc_request("create_companion_session", InvokeBody::default()),
        )
        .map_err(|error| format!("create_companion_session failed: {error}"))?
        .deserialize()?;
        println!("conversation id: {}", session.conversation.id);

        let mut emitted_event = false;
        for (turn_index, user_input) in config.user_inputs.iter().enumerate() {
            let event_start = event_bodies
                .lock()
                .expect("lock manual E2E channel bodies")
                .len();
            let delta_start = gateway.raw_deltas().len();
            let output_start = gateway.raw_outputs().len();
            println!("\n--- turn {} ---", turn_index + 1);
            println!("user input: {user_input}");

            let submit_response = get_ipc_response(
                &webview,
                ipc_request(
                    "submit_companion_turn",
                    InvokeBody::Json(json!({
                        "conversationId": session.conversation.id,
                        "input": user_input.trim(),
                        "speechEnabled": false,
                        "onEvent": format!("__CHANNEL__:{EVENT_CHANNEL_ID}"),
                    })),
                ),
            );
            let outcome = match submit_response {
                Ok(body) => Some(body.deserialize::<serde_json::Value>()?),
                Err(error) => {
                    println!("submit_companion_turn error:\n{error}");
                    None
                }
            };

            let raw_deltas = gateway
                .raw_deltas()
                .into_iter()
                .skip(delta_start)
                .collect::<Vec<_>>()
                .join("");
            if !raw_deltas.trim().is_empty() {
                println!("model raw streamed text:\n{}", raw_deltas.trim());
            }
            for (index, output) in gateway.raw_outputs().iter().enumerate().skip(output_start) {
                println!(
                    "model raw completed text #{}:\n{}",
                    index + 1,
                    output.trim()
                );
            }

            println!("harness events:");
            let decoded_events = event_bodies
                .lock()
                .expect("lock manual E2E channel bodies")
                .iter()
                .skip(event_start)
                .filter_map(decode_harness_event)
                .collect::<Vec<_>>();
            emitted_event |= !decoded_events.is_empty();
            for event in &decoded_events {
                println!("{}", event_summary(event));
            }
            if let Some(outcome) = outcome {
                println!(
                    "final visual state: {}",
                    outcome["visualState"].as_str().unwrap_or("<missing>")
                );
                println!(
                    "final display reply:\n{}",
                    outcome["responseText"].as_str().unwrap_or("<missing>")
                );
                println!(
                    "final speech text:\n{}",
                    outcome["speechText"].as_str().unwrap_or("<missing>")
                );
                print_usage_summary(&outcome["usage"]);
            } else {
                println!("final visual state: <none - command failed>");
                println!("final display reply:\n<none - command failed>");
                println!("final speech text:\n<none - command failed>");
            }
        }
        println!("=== end manual companion frontend-command E2E ===\n");

        assert!(emitted_event, "submit_companion_turn must emit events");
        Ok(())
    }

    #[derive(Clone, Debug)]
    struct ManualE2eConfig {
        protocol: ModelProtocol,
        base_url: String,
        model: String,
        api_key: Option<String>,
        user_inputs: Vec<String>,
        app_config_dir: Option<PathBuf>,
        keep_history: bool,
    }

    impl ManualE2eConfig {
        fn load() -> Result<Option<Self>, Box<dyn std::error::Error>> {
            let file_values = load_local_env_file();
            let Some(base_url) = setting("FAIRY_PERSONA_TEST_BASE_URL", &file_values) else {
                return Ok(None);
            };
            let Some(model) = setting("FAIRY_PERSONA_TEST_MODEL", &file_values) else {
                return Ok(None);
            };
            let protocol = match setting("FAIRY_PERSONA_TEST_PROTOCOL", &file_values)
                .unwrap_or_else(|| "chat_completions".to_owned())
                .as_str()
            {
                "chat" | "chat_completions" => ModelProtocol::ChatCompletions,
                "responses" => ModelProtocol::Responses,
                other => {
                    return Err(format!(
                        "FAIRY_PERSONA_TEST_PROTOCOL must be chat_completions or responses, got {other}"
                    )
                    .into());
                }
            };
            Ok(Some(Self {
                protocol,
                base_url: normalize_base_url(&base_url),
                model,
                api_key: setting("FAIRY_PERSONA_TEST_API_KEY", &file_values),
                user_inputs: manual_user_inputs(&file_values),
                app_config_dir: setting("FAIRY_PERSONA_TEST_CONFIG_DIR", &file_values)
                    .map(PathBuf::from),
                keep_history: truthy_setting("FAIRY_PERSONA_TEST_KEEP_HISTORY", &file_values),
            }))
        }
    }

    fn manual_user_inputs(file_values: &HashMap<String, String>) -> Vec<String> {
        if let Some(raw) = setting("FAIRY_PERSONA_TEST_USER_PROMPTS", file_values) {
            let parsed = parse_user_inputs(&raw);
            if !parsed.is_empty() {
                return parsed;
            }
        }
        if let Some(single) = setting("FAIRY_PERSONA_TEST_USER_PROMPT", file_values) {
            let mut inputs = vec![single];
            inputs.extend(default_followup_inputs());
            return inputs;
        }
        let mut inputs = vec!["亚托莉，醒着吗？我今天有点烦，陪我待一会儿。".to_owned()];
        inputs.extend(default_followup_inputs());
        inputs
    }

    fn default_followup_inputs() -> Vec<String> {
        vec![
            "别讲大道理，也别像客服。就用你自己的语气跟我说两句。".to_owned(),
            "那你记住一下，我烦的时候更想听短一点、自然一点的话。".to_owned(),
        ]
    }

    fn parse_user_inputs(raw: &str) -> Vec<String> {
        if let Ok(values) = serde_json::from_str::<Vec<String>>(raw) {
            return values
                .into_iter()
                .map(|value| value.trim().to_owned())
                .filter(|value| !value.is_empty())
                .collect();
        }
        raw.split("|||")
            .map(|value| value.replace("\\n", "\n"))
            .flat_map(|value| {
                value
                    .lines()
                    .map(str::trim)
                    .filter(|line| !line.is_empty())
                    .map(ToOwned::to_owned)
                    .collect::<Vec<_>>()
            })
            .collect()
    }

    fn build_runtime_from_manual_config(
        config: &ManualE2eConfig,
    ) -> Result<(HarnessRuntime, Arc<RecordingGateway>), Box<dyn std::error::Error>> {
        let auth_mode = if config.api_key.is_some() {
            AuthMode::BearerKey
        } else {
            AuthMode::NoAuth
        };
        let model_config = ModelConnectionCompiler.compile(
            ModelConnectionId::new(),
            ModelConnectionInput {
                protocol: config.protocol,
                endpoint: config.base_url.clone(),
                model: config.model.clone(),
                context_window_tokens: DEFAULT_MODEL_CONTEXT_WINDOW_TOKENS,
                auth_mode,
            },
        )?;
        let gateway = build_openai_compatible_gateway(
            model_config,
            config.api_key.clone().map(SecretString::from),
        )?;
        let recording = Arc::new(RecordingGateway::new(gateway));
        let runtime_gateway: Arc<dyn ModelGateway + Send + Sync> = recording.clone();
        Ok((
            HarnessRuntime::new(config.model.clone(), runtime_gateway)?,
            recording,
        ))
    }

    struct RecordingGateway {
        inner: Arc<dyn ModelGateway + Send + Sync>,
        raw_deltas: Mutex<Vec<String>>,
        raw_outputs: Mutex<Vec<String>>,
    }

    impl RecordingGateway {
        fn new(inner: Arc<dyn ModelGateway + Send + Sync>) -> Self {
            Self {
                inner,
                raw_deltas: Mutex::new(Vec::new()),
                raw_outputs: Mutex::new(Vec::new()),
            }
        }

        fn raw_deltas(&self) -> Vec<String> {
            self.raw_deltas
                .lock()
                .expect("lock raw model deltas")
                .clone()
        }

        fn raw_outputs(&self) -> Vec<String> {
            self.raw_outputs
                .lock()
                .expect("lock raw model outputs")
                .clone()
        }
    }

    #[async_trait]
    impl ModelGateway for RecordingGateway {
        fn capabilities(&self) -> GatewayCapabilities {
            self.inner.capabilities()
        }

        async fn execute(
            &self,
            request: CompiledPromptRequest,
            cancellation: CancellationToken,
            sink: &mut (dyn ModelEventSink + Send),
        ) -> Result<ModelCompletion, FairyError> {
            let mut recording_sink = RecordingModelSink {
                downstream: sink,
                raw_deltas: &self.raw_deltas,
            };
            let completion = self
                .inner
                .execute(request, cancellation, &mut recording_sink)
                .await?;
            if let Some(text) = completion.output.text() {
                self.raw_outputs
                    .lock()
                    .expect("lock raw model outputs")
                    .push(text.to_owned());
            }
            Ok(completion)
        }
    }

    struct RecordingModelSink<'a> {
        downstream: &'a mut (dyn ModelEventSink + Send),
        raw_deltas: &'a Mutex<Vec<String>>,
    }

    impl ModelEventSink for RecordingModelSink<'_> {
        fn send(&mut self, event: ModelStreamEvent) -> Result<(), FairyError> {
            match &event {
                ModelStreamEvent::TextDelta { delta } => self
                    .raw_deltas
                    .lock()
                    .expect("lock raw model deltas")
                    .push(delta.clone()),
            }
            self.downstream.send(event)
        }
    }

    fn app_config_source(config: &ManualE2eConfig) -> Result<PathBuf, Box<dyn std::error::Error>> {
        if let Some(path) = config.app_config_dir.as_ref() {
            return Ok(path.clone());
        }
        let home = std::env::var("HOME")?;
        Ok(PathBuf::from(home)
            .join("Library")
            .join("Application Support")
            .join("dev.rinai.fairy"))
    }

    fn copy_app_config_to_temp(
        source: &Path,
        temp_root: &Path,
    ) -> Result<PathBuf, Box<dyn std::error::Error>> {
        let destination = temp_root.join("app-config");
        if source.join("active-character.json").exists() {
            let harness_root = destination.join("harness").join("v1");
            copy_directory(source, &harness_root)?;
            return Ok(destination);
        }
        if source
            .join("harness")
            .join("v1")
            .join("active-character.json")
            .exists()
        {
            copy_directory(source, &destination)?;
            return Ok(destination);
        }
        Err(format!(
            "FAIRY config source does not contain active-character.json: {}",
            source.display()
        )
        .into())
    }

    fn isolate_manual_e2e_history(
        config_root: &Path,
        keep_history: bool,
    ) -> Result<bool, Box<dyn std::error::Error>> {
        if keep_history {
            return Ok(false);
        }
        let database = config_root
            .join("harness")
            .join("v1")
            .join("intelligence")
            .join("fairy.sqlite3");
        if database.exists() {
            fs::remove_file(database)?;
            return Ok(true);
        }
        Ok(true)
    }

    fn copy_directory(source: &Path, destination: &Path) -> std::io::Result<()> {
        fs::create_dir_all(destination)?;
        for entry in fs::read_dir(source)? {
            let entry = entry?;
            let source_path = entry.path();
            let destination_path = destination.join(entry.file_name());
            if source_path.is_dir() {
                copy_directory(&source_path, &destination_path)?;
            } else {
                if let Some(parent) = destination_path.parent() {
                    fs::create_dir_all(parent)?;
                }
                fs::copy(&source_path, &destination_path)?;
            }
        }
        Ok(())
    }

    fn truthy_setting(name: &str, file_values: &HashMap<String, String>) -> bool {
        setting(name, file_values)
            .map(|value| matches!(value.as_str(), "1" | "true" | "yes" | "on"))
            .unwrap_or(false)
    }

    fn setting(name: &str, file_values: &HashMap<String, String>) -> Option<String> {
        std::env::var(name)
            .ok()
            .or_else(|| file_values.get(name).cloned())
            .map(|value| value.trim().to_owned())
            .filter(|value| !value.is_empty())
    }

    fn load_local_env_file() -> HashMap<String, String> {
        let Some(text) = local_env_file_candidates()
            .into_iter()
            .find_map(|path| fs::read_to_string(path).ok())
        else {
            return HashMap::new();
        };
        text.lines()
            .filter_map(|line| {
                let line = line.trim();
                if line.is_empty() || line.starts_with('#') {
                    return None;
                }
                let (key, value) = line.split_once('=')?;
                let key = key.trim();
                if key.is_empty() {
                    return None;
                }
                Some((key.to_owned(), unquote(value.trim()).to_owned()))
            })
            .collect()
    }

    fn local_env_file_candidates() -> Vec<PathBuf> {
        let mut paths = Vec::new();
        if let Ok(path) = std::env::var("FAIRY_PERSONA_TEST_ENV_FILE") {
            paths.push(PathBuf::from(path));
        }
        paths.push(PathBuf::from(LOCAL_ENV_FILE));
        let manifest_dir = Path::new(env!("CARGO_MANIFEST_DIR"));
        paths.push(manifest_dir.join(LOCAL_ENV_FILE));
        if let Some(workspace_root) = manifest_dir.parent() {
            paths.push(workspace_root.join(LOCAL_ENV_FILE));
        }
        paths
    }

    fn unquote(value: &str) -> &str {
        value
            .strip_prefix('"')
            .and_then(|value| value.strip_suffix('"'))
            .or_else(|| {
                value
                    .strip_prefix('\'')
                    .and_then(|value| value.strip_suffix('\''))
            })
            .unwrap_or(value)
    }

    fn normalize_base_url(value: &str) -> String {
        let mut value = value.trim().trim_end_matches('/').to_owned();
        for suffix in ["/chat/completions", "/responses"] {
            if value.ends_with(suffix) {
                value.truncate(value.len() - suffix.len());
                break;
            }
        }
        value
    }

    fn ipc_request(cmd: &str, body: InvokeBody) -> InvokeRequest {
        InvokeRequest {
            cmd: cmd.to_owned(),
            callback: CallbackFn(0),
            error: CallbackFn(1),
            url: "tauri://localhost".parse().expect("test ipc url"),
            body,
            headers: Default::default(),
            invoke_key: INVOKE_KEY.to_owned(),
        }
    }

    fn decode_harness_event(body: &InvokeResponseBody) -> Option<HarnessEvent> {
        let InvokeResponseBody::Json(json) = body else {
            return None;
        };
        serde_json::from_str(json).ok()
    }

    fn event_summary(event: &HarnessEvent) -> String {
        match &event.payload {
            HarnessEventPayload::StateChanged => {
                format!("#{} state={:?}", event.sequence, event.state)
            }
            HarnessEventPayload::TextDelta { delta } => {
                format!("#{} text_delta={}", event.sequence, delta.trim())
            }
            HarnessEventPayload::ReplyChain {
                index,
                delta,
                visual_state,
                ..
            } => format!(
                "#{} chain[{}] state={} delta={}",
                event.sequence,
                index,
                visual_state.as_str(),
                delta.trim()
            ),
            HarnessEventPayload::Completed {
                text, visual_state, ..
            } => format!(
                "#{} completed state={} text={}",
                event.sequence,
                visual_state.as_str(),
                text.as_str().trim()
            ),
            HarnessEventPayload::SpeechRequested { text, .. } => {
                format!("#{} speech={}", event.sequence, text.as_str().trim())
            }
            HarnessEventPayload::Failed { error } => {
                format!("#{} failed={}", event.sequence, error.message)
            }
        }
    }

    fn print_usage_summary(usage: &serde_json::Value) {
        let Some(entries) = usage.as_array() else {
            println!("usage: <missing>");
            return;
        };
        if entries.is_empty() {
            println!("usage: <empty>");
            return;
        }

        for entry in entries {
            let lane = entry["lane"].as_str().unwrap_or("<unknown>");
            let detail = &entry["usage"];
            let input_tokens = detail["inputTokens"].as_u64();
            let output_tokens = detail["outputTokens"].as_u64();
            let (cached_input, cached_input_tokens) =
                cache_observation_label(&detail["cachedInputTokens"]);
            let (cache_write, _) = cache_observation_label(&detail["cacheWriteTokens"]);
            let hit_rate = match (cached_input_tokens, input_tokens) {
                (Some(cached), Some(input)) if input > 0 => {
                    format!("{:.1}%", (cached as f64 / input as f64) * 100.0)
                }
                _ => "n/a".to_owned(),
            };

            println!(
                "usage {lane}: input={} output={} cached_input={cached_input} hit_rate={hit_rate} cache_write={cache_write}",
                token_label(input_tokens),
                token_label(output_tokens),
            );
        }
    }

    fn token_label(tokens: Option<u64>) -> String {
        tokens
            .map(|tokens| tokens.to_string())
            .unwrap_or_else(|| "missing".to_owned())
    }

    fn cache_observation_label(value: &serde_json::Value) -> (String, Option<u64>) {
        match value["status"].as_str() {
            Some("observed") => {
                let tokens = value["tokens"].as_u64();
                (token_label(tokens), tokens)
            }
            Some("missing") => ("missing".to_owned(), None),
            Some("unsupported") => ("unsupported".to_owned(), None),
            _ => ("invalid".to_owned(), None),
        }
    }
}
