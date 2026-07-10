use std::sync::{Mutex, MutexGuard};

use serde::Serialize;
use tauri::{
    App, AppHandle, Emitter, Manager, Runtime, State, WindowEvent, menu::MenuBuilder,
    tray::TrayIconBuilder,
};

use crate::app_error::AppError;

const MAIN_WINDOW_LABEL: &str = "main";
const TRAY_ID: &str = "fairy-tray";
const MENU_SHOW: &str = "show";
const MENU_HIDE: &str = "hide";
const MENU_RESTORE_INTERACTION: &str = "restore-interaction";
const MENU_QUIT: &str = "quit";
const DESKTOP_STATE_CHANGED_EVENT: &str = "desktop-state-changed";

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum TrayAction {
    Show,
    Hide,
    RestoreInteraction,
    Quit,
}

fn tray_action(menu_id: &str) -> Option<TrayAction> {
    match menu_id {
        MENU_SHOW => Some(TrayAction::Show),
        MENU_HIDE => Some(TrayAction::Hide),
        MENU_RESTORE_INTERACTION => Some(TrayAction::RestoreInteraction),
        MENU_QUIT => Some(TrayAction::Quit),
        _ => None,
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct DesktopState {
    pub always_on_top: bool,
    pub click_through: bool,
    pub tray_ready: bool,
    pub visible: bool,
}

impl Default for DesktopState {
    fn default() -> Self {
        Self {
            always_on_top: true,
            click_through: false,
            tray_ready: false,
            visible: true,
        }
    }
}

impl DesktopState {
    pub fn ensure_click_through_allowed(self, enabled: bool) -> Result<(), AppError> {
        if enabled && !self.tray_ready {
            return Err(AppError::tray_not_ready());
        }

        Ok(())
    }
}

#[derive(Default)]
pub struct DesktopStateStore(Mutex<DesktopState>);

impl DesktopStateStore {
    fn lock(&self) -> Result<MutexGuard<'_, DesktopState>, AppError> {
        self.0.lock().map_err(|_| AppError::state_unavailable())
    }

    pub fn snapshot(&self) -> Result<DesktopState, AppError> {
        Ok(*self.lock()?)
    }

    fn update(&self, change: impl FnOnce(&mut DesktopState)) -> Result<DesktopState, AppError> {
        let mut state = self.lock()?;
        change(&mut state);
        Ok(*state)
    }
}

fn commit_after_operation<E>(
    store: &DesktopStateStore,
    operation: Result<(), E>,
    action: &str,
    change: impl FnOnce(&mut DesktopState),
) -> Result<DesktopState, AppError> {
    operation.map_err(|_| AppError::desktop_operation_failed(action))?;
    store.update(change)
}

fn main_window<R: Runtime>(app: &AppHandle<R>) -> Result<tauri::WebviewWindow<R>, AppError> {
    app.get_webview_window(MAIN_WINDOW_LABEL)
        .ok_or_else(AppError::window_not_found)
}

#[tauri::command]
pub fn get_desktop_state(state: State<'_, DesktopStateStore>) -> Result<DesktopState, AppError> {
    state.snapshot()
}

#[tauri::command]
pub fn set_always_on_top<R: Runtime>(
    app: AppHandle<R>,
    state: State<'_, DesktopStateStore>,
    enabled: bool,
) -> Result<DesktopState, AppError> {
    let window = main_window(&app)?;
    commit_after_operation(
        &state,
        window.set_always_on_top(enabled),
        "set always-on-top",
        |desktop| desktop.always_on_top = enabled,
    )
}

#[tauri::command]
pub fn set_click_through<R: Runtime>(
    app: AppHandle<R>,
    state: State<'_, DesktopStateStore>,
    enabled: bool,
) -> Result<DesktopState, AppError> {
    state.snapshot()?.ensure_click_through_allowed(enabled)?;
    let window = main_window(&app)?;
    commit_after_operation(
        &state,
        window.set_ignore_cursor_events(enabled),
        "set click-through",
        |desktop| desktop.click_through = enabled,
    )
}

fn show_window<R: Runtime>(app: &AppHandle<R>) -> Result<DesktopState, AppError> {
    let window = main_window(app)?;
    let state = app.state::<DesktopStateStore>();
    commit_after_operation(&state, window.show(), "show companion window", |desktop| {
        desktop.visible = true;
    })
}

fn hide_window<R: Runtime>(app: &AppHandle<R>) -> Result<DesktopState, AppError> {
    let window = main_window(app)?;
    let state = app.state::<DesktopStateStore>();
    commit_after_operation(&state, window.hide(), "hide companion window", |desktop| {
        desktop.visible = false;
    })
}

fn restore_interaction<R: Runtime>(app: &AppHandle<R>) -> Result<DesktopState, AppError> {
    let window = main_window(app)?;
    let state = app.state::<DesktopStateStore>();

    commit_after_operation(
        &state,
        window.set_ignore_cursor_events(false),
        "restore pointer interaction",
        |desktop| desktop.click_through = false,
    )?;

    commit_after_operation(&state, window.show(), "show companion window", |desktop| {
        desktop.visible = true;
    })
}

fn report_tray_error(action: &str, error: &AppError) {
    eprintln!(
        "FAIRY_TRAY_ACTION_FAILED action={action} code={} message={}",
        error.code, error.message
    );
}

fn emit_desktop_state<R: Runtime>(app: &AppHandle<R>, state: DesktopState) -> Result<(), AppError> {
    app.emit(DESKTOP_STATE_CHANGED_EVENT, state)
        .map_err(|_| AppError::desktop_operation_failed("emit desktop state"))
}

fn handle_tray_menu<R: Runtime>(app: &AppHandle<R>, menu_id: &str) {
    let Some(action) = tray_action(menu_id) else {
        return;
    };

    let result = match action {
        TrayAction::Show => show_window(app),
        TrayAction::Hide => hide_window(app),
        TrayAction::RestoreInteraction => restore_interaction(app),
        TrayAction::Quit => {
            app.exit(0);
            return;
        }
    };

    match result {
        Ok(state) => {
            if let Err(error) = emit_desktop_state(app, state) {
                report_tray_error(menu_id, &error);
            }
        }
        Err(error) => report_tray_error(menu_id, &error),
    }
}

fn build_tray(app: &mut App) -> Result<(), AppError> {
    let menu = MenuBuilder::new(app)
        .text(MENU_SHOW, "显示角色")
        .text(MENU_HIDE, "隐藏角色")
        .text(MENU_RESTORE_INTERACTION, "恢复交互")
        .separator()
        .text(MENU_QUIT, "退出 FAIRY")
        .build()
        .map_err(|_| AppError::desktop_operation_failed("build tray menu"))?;

    let icon = app
        .default_window_icon()
        .cloned()
        .ok_or_else(|| AppError::desktop_operation_failed("load tray icon"))?;

    TrayIconBuilder::with_id(TRAY_ID)
        .icon(icon)
        .tooltip("FAIRY 桌面陪伴")
        .menu(&menu)
        .show_menu_on_left_click(true)
        .on_menu_event(|app, event| handle_tray_menu(app, event.id().as_ref()))
        .build(app)
        .map_err(|_| AppError::desktop_operation_failed("build tray entry"))?;

    app.state::<DesktopStateStore>()
        .update(|state| state.tray_ready = true)?;
    Ok(())
}

pub fn setup(app: &mut App) {
    if let Err(error) = build_tray(app) {
        eprintln!(
            "FAIRY_TRAY_SETUP_FAILED code={} message={}",
            error.code, error.message
        );
    }
}

pub fn handle_window_event<R: Runtime>(window: &tauri::Window<R>, event: &WindowEvent) {
    if window.label() != MAIN_WINDOW_LABEL {
        return;
    }

    let WindowEvent::CloseRequested { api, .. } = event else {
        return;
    };

    let state = window.app_handle().state::<DesktopStateStore>();
    match state.snapshot() {
        Ok(snapshot) if snapshot.tray_ready => {
            api.prevent_close();
            match commit_after_operation(
                &state,
                window.hide(),
                "hide companion window on close",
                |desktop| desktop.visible = false,
            ) {
                Ok(snapshot) => {
                    if let Err(error) = emit_desktop_state(window.app_handle(), snapshot) {
                        report_tray_error("close-to-tray", &error);
                    }
                }
                Err(error) => report_tray_error("close-to-tray", &error),
            }
        }
        Ok(_) => {}
        Err(error) => report_tray_error("read state before close", &error),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn click_through_is_rejected_without_a_tray_recovery_path() {
        let state = DesktopState::default();

        let error = state
            .ensure_click_through_allowed(true)
            .expect_err("click-through must require a tray");
        assert_eq!(error.code, "TRAY_NOT_READY");
    }

    #[test]
    fn disabling_click_through_never_requires_a_tray() {
        let state = DesktopState {
            click_through: true,
            ..DesktopState::default()
        };

        assert!(state.ensure_click_through_allowed(false).is_ok());
    }

    #[test]
    fn click_through_is_allowed_after_the_tray_is_ready() {
        let state = DesktopState {
            tray_ready: true,
            ..DesktopState::default()
        };

        assert!(state.ensure_click_through_allowed(true).is_ok());
    }

    #[test]
    fn failed_platform_operation_does_not_write_a_success_state() {
        let store = DesktopStateStore::default();

        let error = commit_after_operation(
            &store,
            Err::<(), _>("platform refused"),
            "set always-on-top",
            |state| state.always_on_top = false,
        )
        .expect_err("a platform failure must remain a failure");

        assert_eq!(error.code, "DESKTOP_OPERATION_FAILED");
        assert!(
            store
                .snapshot()
                .expect("state remains readable")
                .always_on_top
        );
    }

    #[test]
    fn successful_platform_operation_commits_the_returned_state() {
        let store = DesktopStateStore::default();

        let snapshot =
            commit_after_operation(&store, Ok::<(), &str>(()), "set always-on-top", |state| {
                state.always_on_top = false
            })
            .expect("successful platform operation should commit state");

        assert!(!snapshot.always_on_top);
        assert!(
            !store
                .snapshot()
                .expect("state remains readable")
                .always_on_top
        );
    }

    #[test]
    fn tray_menu_ids_map_to_every_required_lifecycle_action() {
        assert_eq!(tray_action(MENU_SHOW), Some(TrayAction::Show));
        assert_eq!(tray_action(MENU_HIDE), Some(TrayAction::Hide));
        assert_eq!(
            tray_action(MENU_RESTORE_INTERACTION),
            Some(TrayAction::RestoreInteraction)
        );
        assert_eq!(tray_action(MENU_QUIT), Some(TrayAction::Quit));
        assert_eq!(tray_action("unknown"), None);
    }

    #[test]
    fn successful_recovery_sequence_restores_pointer_and_visibility() {
        let store = DesktopStateStore::default();
        store
            .update(|state| {
                state.tray_ready = true;
                state.click_through = true;
                state.visible = false;
            })
            .expect("test state should be writable");

        commit_after_operation(
            &store,
            Ok::<(), &str>(()),
            "restore pointer interaction",
            |state| state.click_through = false,
        )
        .expect("pointer recovery should commit");
        let recovered = commit_after_operation(
            &store,
            Ok::<(), &str>(()),
            "show companion window",
            |state| state.visible = true,
        )
        .expect("show should commit");

        assert!(recovered.tray_ready);
        assert!(!recovered.click_through);
        assert!(recovered.visible);
    }
}
