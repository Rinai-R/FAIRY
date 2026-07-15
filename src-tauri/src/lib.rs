use serde::Serialize;
use tauri::Manager;

pub mod app_error;
pub mod app_state;
pub mod audio;
pub mod capability;
pub mod desktop;
pub mod ipc;
#[cfg(all(debug_assertions, target_os = "macos"))]
mod native_snapshot_smoke;
pub mod visual_registry;

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
struct HealthResponse {
    status: &'static str,
    architecture: &'static str,
    version: &'static str,
}

#[tauri::command]
fn health() -> HealthResponse {
    HealthResponse {
        status: "ok",
        architecture: "tauri-rust",
        version: env!("CARGO_PKG_VERSION"),
    }
}

pub fn run() {
    tauri::Builder::default()
        .manage(desktop::DesktopStateStore::default())
        .setup(|app| {
            let config_directory = app.path().app_config_dir()?;
            let state = app_state::AppState::initialize(config_directory)?;
            #[cfg(all(debug_assertions, target_os = "macos"))]
            native_snapshot_smoke::seed_from_env(&state)?;
            app.manage(state);
            desktop::setup(app);
            #[cfg(all(debug_assertions, target_os = "macos"))]
            native_snapshot_smoke::schedule_from_env(app);
            Ok(())
        })
        .on_window_event(desktop::handle_window_event)
        .invoke_handler(tauri::generate_handler![
            health,
            desktop::get_desktop_state,
            desktop::set_always_on_top,
            desktop::set_click_through,
            desktop::show_control_panel,
            desktop::restore_companion_after_control_panel,
            desktop::show_companion,
            desktop::hide_companion,
            desktop::restore_companion_interaction,
            desktop::open_companion_chat,
            desktop::close_companion_chat,
            ipc::companion::create_companion_session,
            ipc::companion::get_companion_session,
            ipc::companion::submit_companion_turn,
            ipc::companion::cancel_companion_turn,
            ipc::companion::compact_companion_session,
            ipc::character::create_character,
            ipc::character::update_character,
            ipc::character::list_characters,
            ipc::character::list_visual_packs,
            ipc::character::set_character_appearance,
            ipc::character::activate_character,
            ipc::settings::get_user_profile,
            ipc::settings::set_user_profile,
            ipc::settings::clear_user_profile,
            ipc::settings::get_model_connection_status,
            ipc::settings::save_model_connection,
            ipc::settings::clear_model_connection,
            ipc::settings::get_intelligence_status,
            ipc::settings::get_knowledge_catalog,
            ipc::settings::confirm_knowledge_candidate,
            ipc::settings::tombstone_knowledge,
            ipc::settings::get_personal_memory_catalog,
            ipc::settings::get_extraction_batch_catalog,
            ipc::settings::create_personal_memory,
            ipc::settings::revise_personal_memory,
            ipc::settings::tombstone_personal_memory,
            ipc::settings::assign_legacy_relationship,
            ipc::settings::retry_extraction_batch
        ])
        .run(tauri::generate_context!())
        .expect("FAIRY desktop runtime failed");
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn health_reports_the_rust_architecture() {
        let response = health();

        assert_eq!(response.status, "ok");
        assert_eq!(response.architecture, "tauri-rust");
        assert_eq!(response.version, env!("CARGO_PKG_VERSION"));
    }
}
