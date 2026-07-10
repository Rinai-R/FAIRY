use serde::Serialize;
use tauri::Manager;

pub mod app_error;
pub mod app_state;
pub mod audio;
pub mod capability;
pub mod desktop;
pub mod ipc;
pub mod memory;

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
            app.manage(app_state::AppState::initialize(config_directory)?);
            desktop::setup(app);
            Ok(())
        })
        .on_window_event(desktop::handle_window_event)
        .invoke_handler(tauri::generate_handler![
            health,
            desktop::get_desktop_state,
            desktop::set_always_on_top,
            desktop::set_click_through,
            ipc::companion::create_companion_session,
            ipc::companion::get_companion_session,
            ipc::companion::submit_companion_turn,
            ipc::companion::cancel_companion_turn,
            ipc::companion::compact_companion_session,
            ipc::character::create_character,
            ipc::character::update_character,
            ipc::character::list_characters,
            ipc::character::activate_character,
            ipc::settings::get_user_profile,
            ipc::settings::set_user_profile,
            ipc::settings::clear_user_profile,
            ipc::settings::get_model_connection_status,
            ipc::settings::save_model_connection,
            ipc::settings::clear_model_connection
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
