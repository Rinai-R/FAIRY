use std::sync::{Mutex, MutexGuard};

use serde::Serialize;
use tauri::{
    App, AppHandle, Emitter, LogicalSize, Manager, PhysicalPosition, PhysicalRect, PhysicalSize,
    Runtime, State, WindowEvent, menu::MenuBuilder, tray::TrayIconBuilder,
};

use crate::app_error::AppError;

const COMPANION_WINDOW_LABEL: &str = "companion";
const CONTROL_PANEL_WINDOW_LABEL: &str = "control-panel";
#[cfg(test)]
const IDLE_WIDTH: f64 = 220.0;
#[cfg(test)]
const IDLE_HEIGHT: f64 = 344.0;
const CHAT_WIDTH: f64 = 552.0;
const CHAT_HEIGHT: f64 = 382.0;
const CONTROL_PANEL_WIDTH: f64 = 560.0;
const CONTROL_PANEL_HEIGHT: f64 = 620.0;
const TRAY_ID: &str = "fairy-tray";
const MENU_SHOW: &str = "show";
const MENU_HIDE: &str = "hide";
const MENU_OPEN_CONTROL_PANEL: &str = "open-control-panel";
const MENU_RESTORE_INTERACTION: &str = "restore-interaction";
const MENU_QUIT: &str = "quit";
const DESKTOP_STATE_CHANGED_EVENT: &str = "desktop-state-changed";

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum TrayAction {
    Show,
    Hide,
    OpenControlPanel,
    RestoreInteraction,
    Quit,
}

fn tray_action(menu_id: &str) -> Option<TrayAction> {
    match menu_id {
        MENU_SHOW => Some(TrayAction::Show),
        MENU_HIDE => Some(TrayAction::Hide),
        MENU_OPEN_CONTROL_PANEL => Some(TrayAction::OpenControlPanel),
        MENU_RESTORE_INTERACTION => Some(TrayAction::RestoreInteraction),
        MENU_QUIT => Some(TrayAction::Quit),
        _ => None,
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum CompanionSurface {
    Idle,
    Chat,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum DesktopPhase {
    CompanionIdle,
    CompanionChatOpening,
    CompanionChatOpen,
    CompanionChatClosing,
    TransitioningToSettings,
    ControlPanelVisible,
    TransitioningToCompanion,
}

impl DesktopPhase {
    fn as_str(self) -> &'static str {
        match self {
            Self::CompanionIdle => "companion_idle",
            Self::CompanionChatOpening => "companion_chat_opening",
            Self::CompanionChatOpen => "companion_chat_open",
            Self::CompanionChatClosing => "companion_chat_closing",
            Self::TransitioningToSettings => "transitioning_to_settings",
            Self::ControlPanelVisible => "control_panel_visible",
            Self::TransitioningToCompanion => "transitioning_to_companion",
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct DesktopState {
    pub always_on_top: bool,
    pub click_through: bool,
    pub tray_ready: bool,
    pub visible: bool,
    pub companion_surface: CompanionSurface,
    pub control_panel_visible: bool,
    pub phase: DesktopPhase,
}

impl Default for DesktopState {
    fn default() -> Self {
        Self {
            always_on_top: true,
            click_through: false,
            tray_ready: false,
            visible: true,
            companion_surface: CompanionSurface::Idle,
            control_panel_visible: false,
            phase: DesktopPhase::CompanionIdle,
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

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct CompanionAnchor {
    position: PhysicalPosition<i32>,
    size: PhysicalSize<u32>,
}

#[derive(Debug, Default)]
struct StoredDesktopState {
    public: DesktopState,
    companion_anchor: Option<CompanionAnchor>,
    replacement_anchor: Option<CompanionAnchor>,
}

#[derive(Default)]
pub struct DesktopStateStore(Mutex<StoredDesktopState>);

impl DesktopStateStore {
    fn lock(&self) -> Result<MutexGuard<'_, StoredDesktopState>, AppError> {
        self.0.lock().map_err(|_| AppError::state_unavailable())
    }

    pub fn snapshot(&self) -> Result<DesktopState, AppError> {
        Ok(self.lock()?.public)
    }

    fn update(&self, change: impl FnOnce(&mut DesktopState)) -> Result<DesktopState, AppError> {
        let mut stored = self.lock()?;
        change(&mut stored.public);
        Ok(stored.public)
    }

    fn transition(
        &self,
        expected: DesktopPhase,
        next: DesktopPhase,
        action: &str,
    ) -> Result<DesktopState, AppError> {
        let mut stored = self.lock()?;
        if stored.public.phase != expected {
            return Err(AppError::desktop_transition_rejected(
                stored.public.phase.as_str(),
                action,
            ));
        }
        stored.public.phase = next;
        Ok(stored.public)
    }

    fn companion_anchor(&self) -> Result<Option<CompanionAnchor>, AppError> {
        Ok(self.lock()?.companion_anchor)
    }

    fn mark_chat_open(
        &self,
        position: PhysicalPosition<i32>,
        size: PhysicalSize<u32>,
    ) -> Result<DesktopState, AppError> {
        let mut stored = self.lock()?;
        stored.companion_anchor = Some(CompanionAnchor { position, size });
        stored.public.companion_surface = CompanionSurface::Chat;
        stored.public.phase = DesktopPhase::CompanionChatOpen;
        Ok(stored.public)
    }

    fn mark_chat_closed(&self) -> Result<DesktopState, AppError> {
        let mut stored = self.lock()?;
        stored.companion_anchor = None;
        stored.public.companion_surface = CompanionSurface::Idle;
        stored.public.phase = DesktopPhase::CompanionIdle;
        Ok(stored.public)
    }

    fn mark_control_panel_visible(
        &self,
        position: PhysicalPosition<i32>,
        size: PhysicalSize<u32>,
    ) -> Result<DesktopState, AppError> {
        let mut stored = self.lock()?;
        stored.replacement_anchor = Some(CompanionAnchor { position, size });
        stored.public.visible = false;
        stored.public.control_panel_visible = true;
        stored.public.companion_surface = CompanionSurface::Idle;
        stored.public.phase = DesktopPhase::ControlPanelVisible;
        Ok(stored.public)
    }

    fn replacement_anchor(&self) -> Result<Option<CompanionAnchor>, AppError> {
        Ok(self.lock()?.replacement_anchor)
    }

    fn mark_companion_restored(&self) -> Result<DesktopState, AppError> {
        let mut stored = self.lock()?;
        stored.replacement_anchor = None;
        stored.public.visible = true;
        stored.public.control_panel_visible = false;
        stored.public.companion_surface = CompanionSurface::Idle;
        stored.public.phase = DesktopPhase::CompanionIdle;
        Ok(stored.public)
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

fn companion_window<R: Runtime>(app: &AppHandle<R>) -> Result<tauri::WebviewWindow<R>, AppError> {
    app.get_webview_window(COMPANION_WINDOW_LABEL)
        .ok_or_else(AppError::window_not_found)
}

fn control_panel_window<R: Runtime>(
    app: &AppHandle<R>,
) -> Result<tauri::WebviewWindow<R>, AppError> {
    app.get_webview_window(CONTROL_PANEL_WINDOW_LABEL)
        .ok_or_else(AppError::window_not_found)
}

fn clamp_to_i32(value: i64) -> i32 {
    value.clamp(i32::MIN as i64, i32::MAX as i64) as i32
}

fn chat_window_position(
    current_position: PhysicalPosition<i32>,
    current_size: PhysicalSize<u32>,
    target_size: PhysicalSize<u32>,
    work_area: PhysicalRect<i32, u32>,
) -> PhysicalPosition<i32> {
    let work_left = i64::from(work_area.position.x);
    let work_top = i64::from(work_area.position.y);
    let work_right = work_left + i64::from(work_area.size.width);
    let work_bottom = work_top + i64::from(work_area.size.height);
    let current_left = i64::from(current_position.x);
    let current_top = i64::from(current_position.y);
    let current_right = current_left + i64::from(current_size.width);
    let current_bottom = current_top + i64::from(current_size.height);
    let width_growth = i64::from(target_size.width.saturating_sub(current_size.width));
    let height_growth = i64::from(target_size.height.saturating_sub(current_size.height));
    let left_space = current_left.saturating_sub(work_left);
    let right_space = work_right.saturating_sub(current_right);
    let top_space = current_top.saturating_sub(work_top);
    let bottom_space = work_bottom.saturating_sub(current_bottom);

    let proposed_left = if left_space >= width_growth || left_space >= right_space {
        current_left - width_growth
    } else {
        current_left
    };
    let proposed_top = if top_space >= height_growth || top_space >= bottom_space {
        current_top - height_growth
    } else {
        current_top
    };
    let max_left = (work_right - i64::from(target_size.width)).max(work_left);
    let max_top = (work_bottom - i64::from(target_size.height)).max(work_top);

    PhysicalPosition::new(
        clamp_to_i32(proposed_left.clamp(work_left, max_left)),
        clamp_to_i32(proposed_top.clamp(work_top, max_top)),
    )
}

fn replacement_window_position(
    companion_position: PhysicalPosition<i32>,
    companion_size: PhysicalSize<u32>,
    replacement_size: PhysicalSize<u32>,
    work_area: PhysicalRect<i32, u32>,
) -> PhysicalPosition<i32> {
    let work_left = i64::from(work_area.position.x);
    let work_top = i64::from(work_area.position.y);
    let work_right = work_left + i64::from(work_area.size.width);
    let work_bottom = work_top + i64::from(work_area.size.height);
    let companion_right = i64::from(companion_position.x) + i64::from(companion_size.width);
    let companion_bottom = i64::from(companion_position.y) + i64::from(companion_size.height);
    let proposed_left = companion_right - i64::from(replacement_size.width);
    let proposed_top = companion_bottom - i64::from(replacement_size.height);
    let max_left = (work_right - i64::from(replacement_size.width)).max(work_left);
    let max_top = (work_bottom - i64::from(replacement_size.height)).max(work_top);

    PhysicalPosition::new(
        clamp_to_i32(proposed_left.clamp(work_left, max_left)),
        clamp_to_i32(proposed_top.clamp(work_top, max_top)),
    )
}

fn restore_window_geometry<R: Runtime>(
    window: &tauri::WebviewWindow<R>,
    position: PhysicalPosition<i32>,
    size: PhysicalSize<u32>,
    action: &str,
) -> Result<(), AppError> {
    window
        .set_size(size)
        .map_err(|_| AppError::desktop_operation_failed(action))?;
    window
        .set_position(position)
        .map_err(|_| AppError::desktop_operation_failed(action))
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
    let window = companion_window(&app)?;
    let snapshot = commit_after_operation(
        &state,
        window.set_always_on_top(enabled),
        "set always-on-top",
        |desktop| desktop.always_on_top = enabled,
    )?;
    emit_desktop_state(&app, snapshot)?;
    Ok(snapshot)
}

#[tauri::command]
pub fn set_click_through<R: Runtime>(
    app: AppHandle<R>,
    state: State<'_, DesktopStateStore>,
    enabled: bool,
) -> Result<DesktopState, AppError> {
    state.snapshot()?.ensure_click_through_allowed(enabled)?;
    let window = companion_window(&app)?;
    let snapshot = commit_after_operation(
        &state,
        window.set_ignore_cursor_events(enabled),
        "set click-through",
        |desktop| desktop.click_through = enabled,
    )?;
    emit_desktop_state(&app, snapshot)?;
    Ok(snapshot)
}

fn show_companion_window<R: Runtime>(app: &AppHandle<R>) -> Result<DesktopState, AppError> {
    if app
        .state::<DesktopStateStore>()
        .snapshot()?
        .control_panel_visible
    {
        return restore_companion_after_control_panel_window(app);
    }
    let window = companion_window(app)?;
    let state = app.state::<DesktopStateStore>();
    commit_after_operation(&state, window.show(), "show companion window", |desktop| {
        desktop.visible = true;
    })
}

fn hide_companion_window<R: Runtime>(app: &AppHandle<R>) -> Result<DesktopState, AppError> {
    let window = companion_window(app)?;
    let state = app.state::<DesktopStateStore>();
    commit_after_operation(&state, window.hide(), "hide companion window", |desktop| {
        desktop.visible = false;
    })
}

fn restore_companion_window<R: Runtime>(app: &AppHandle<R>) -> Result<DesktopState, AppError> {
    if app
        .state::<DesktopStateStore>()
        .snapshot()?
        .control_panel_visible
    {
        restore_companion_after_control_panel_window(app)?;
    }
    let window = companion_window(app)?;
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

#[tauri::command]
pub fn show_companion<R: Runtime>(app: AppHandle<R>) -> Result<DesktopState, AppError> {
    let snapshot = show_companion_window(&app)?;
    emit_desktop_state(&app, snapshot)?;
    Ok(snapshot)
}

#[tauri::command]
pub fn hide_companion<R: Runtime>(app: AppHandle<R>) -> Result<DesktopState, AppError> {
    let snapshot = hide_companion_window(&app)?;
    emit_desktop_state(&app, snapshot)?;
    Ok(snapshot)
}

#[tauri::command]
pub fn restore_companion_interaction<R: Runtime>(
    app: AppHandle<R>,
) -> Result<DesktopState, AppError> {
    let snapshot = restore_companion_window(&app)?;
    emit_desktop_state(&app, snapshot)?;
    Ok(snapshot)
}

#[tauri::command]
pub fn show_control_panel<R: Runtime>(app: AppHandle<R>) -> Result<DesktopState, AppError> {
    let snapshot = show_control_panel_window(&app)?;
    emit_desktop_state(&app, snapshot)?;
    Ok(snapshot)
}

fn show_control_panel_window<R: Runtime>(app: &AppHandle<R>) -> Result<DesktopState, AppError> {
    let state = app.state::<DesktopStateStore>();
    let current = state.snapshot()?;
    if current.phase == DesktopPhase::ControlPanelVisible {
        control_panel_window(app)?
            .set_focus()
            .map_err(|_| AppError::desktop_operation_failed("focus control panel"))?;
        return Ok(current);
    }
    if current.phase == DesktopPhase::CompanionChatOpen {
        close_companion_chat(app.clone(), app.state::<DesktopStateStore>())?;
    }

    let companion = companion_window(app)?;
    let panel = control_panel_window(app)?;
    let companion_position = companion
        .outer_position()
        .map_err(|_| AppError::desktop_operation_failed("read companion replacement position"))?;
    let companion_size = companion
        .outer_size()
        .map_err(|_| AppError::desktop_operation_failed("read companion replacement size"))?;
    let monitor = companion
        .current_monitor()
        .map_err(|_| AppError::desktop_operation_failed("read companion replacement monitor"))?
        .ok_or_else(|| {
            AppError::desktop_operation_failed("locate companion replacement monitor")
        })?;
    let panel_size: PhysicalSize<u32> = LogicalSize::new(CONTROL_PANEL_WIDTH, CONTROL_PANEL_HEIGHT)
        .to_physical(monitor.scale_factor());
    let panel_position = replacement_window_position(
        companion_position,
        companion_size,
        panel_size,
        *monitor.work_area(),
    );
    let transitioning = state.transition(
        DesktopPhase::CompanionIdle,
        DesktopPhase::TransitioningToSettings,
        "show control panel",
    )?;
    if let Err(error) = emit_desktop_state(app, transitioning) {
        state.update(|desktop| desktop.phase = DesktopPhase::CompanionIdle)?;
        return Err(error);
    }

    if panel
        .set_size(LogicalSize::new(CONTROL_PANEL_WIDTH, CONTROL_PANEL_HEIGHT))
        .is_err()
    {
        state.update(|desktop| desktop.phase = DesktopPhase::CompanionIdle)?;
        return Err(AppError::desktop_operation_failed(
            "resize replacement control panel",
        ));
    }
    if panel.set_position(panel_position).is_err() {
        state.update(|desktop| desktop.phase = DesktopPhase::CompanionIdle)?;
        return Err(AppError::desktop_operation_failed(
            "position replacement control panel",
        ));
    }
    if companion.hide().is_err() {
        state.update(|desktop| desktop.phase = DesktopPhase::CompanionIdle)?;
        return Err(AppError::desktop_operation_failed(
            "hide companion for replacement",
        ));
    }
    if panel.show().is_err() {
        companion.show().map_err(|_| {
            AppError::desktop_operation_failed("rollback companion after control panel failure")
        })?;
        state.update(|desktop| desktop.phase = DesktopPhase::CompanionIdle)?;
        return Err(AppError::desktop_operation_failed(
            "show replacement control panel",
        ));
    }
    if panel.set_focus().is_err() {
        panel.hide().map_err(|_| {
            AppError::desktop_operation_failed("hide control panel during focus rollback")
        })?;
        companion.show().map_err(|_| {
            AppError::desktop_operation_failed("show companion during focus rollback")
        })?;
        state.update(|desktop| desktop.phase = DesktopPhase::CompanionIdle)?;
        return Err(AppError::desktop_operation_failed(
            "focus replacement control panel",
        ));
    }

    match state.mark_control_panel_visible(companion_position, companion_size) {
        Ok(snapshot) => Ok(snapshot),
        Err(error) => {
            panel.hide().map_err(|_| {
                AppError::desktop_operation_failed("hide control panel after state failure")
            })?;
            companion.show().map_err(|_| {
                AppError::desktop_operation_failed("restore companion after state failure")
            })?;
            Err(error)
        }
    }
}

#[tauri::command]
pub fn restore_companion_after_control_panel<R: Runtime>(
    app: AppHandle<R>,
) -> Result<DesktopState, AppError> {
    let snapshot = restore_companion_after_control_panel_window(&app)?;
    emit_desktop_state(&app, snapshot)?;
    Ok(snapshot)
}

fn restore_companion_after_control_panel_window<R: Runtime>(
    app: &AppHandle<R>,
) -> Result<DesktopState, AppError> {
    let state = app.state::<DesktopStateStore>();
    let current = state.snapshot()?;
    if current.phase == DesktopPhase::CompanionIdle && !current.control_panel_visible {
        return Ok(current);
    }
    let anchor = state
        .replacement_anchor()?
        .ok_or_else(AppError::state_unavailable)?;
    let companion = companion_window(app)?;
    let panel = control_panel_window(app)?;
    let transitioning = state.transition(
        DesktopPhase::ControlPanelVisible,
        DesktopPhase::TransitioningToCompanion,
        "restore companion",
    )?;
    if let Err(error) = emit_desktop_state(app, transitioning) {
        state.update(|desktop| desktop.phase = DesktopPhase::ControlPanelVisible)?;
        return Err(error);
    }

    if panel.hide().is_err() {
        state.update(|desktop| desktop.phase = DesktopPhase::ControlPanelVisible)?;
        return Err(AppError::desktop_operation_failed(
            "hide replacement control panel",
        ));
    }
    if let Err(error) = restore_window_geometry(
        &companion,
        anchor.position,
        anchor.size,
        "restore companion replacement geometry",
    ) {
        panel.show().map_err(|_| {
            AppError::desktop_operation_failed("rollback control panel after geometry failure")
        })?;
        state.update(|desktop| desktop.phase = DesktopPhase::ControlPanelVisible)?;
        return Err(error);
    }
    if companion.show().is_err() {
        panel.show().map_err(|_| {
            AppError::desktop_operation_failed("rollback control panel after companion failure")
        })?;
        state.update(|desktop| desktop.phase = DesktopPhase::ControlPanelVisible)?;
        return Err(AppError::desktop_operation_failed(
            "show companion after replacement",
        ));
    }
    if companion.set_focus().is_err() {
        companion.hide().map_err(|_| {
            AppError::desktop_operation_failed("hide companion during focus rollback")
        })?;
        panel.show().map_err(|_| {
            AppError::desktop_operation_failed("show control panel during focus rollback")
        })?;
        state.update(|desktop| desktop.phase = DesktopPhase::ControlPanelVisible)?;
        return Err(AppError::desktop_operation_failed(
            "focus companion after replacement",
        ));
    }

    state.mark_companion_restored()
}

#[tauri::command]
pub fn open_companion_chat<R: Runtime>(
    app: AppHandle<R>,
    state: State<'_, DesktopStateStore>,
) -> Result<DesktopState, AppError> {
    let current = state.snapshot()?;
    if current.phase == DesktopPhase::CompanionChatOpen {
        return Ok(current);
    }
    let window = companion_window(&app)?;
    let current_position = window
        .outer_position()
        .map_err(|_| AppError::desktop_operation_failed("read companion position"))?;
    let current_size = window
        .outer_size()
        .map_err(|_| AppError::desktop_operation_failed("read companion size"))?;
    let monitor = window
        .current_monitor()
        .map_err(|_| AppError::desktop_operation_failed("read companion monitor"))?
        .ok_or_else(|| AppError::desktop_operation_failed("locate companion monitor"))?;
    let target_size: PhysicalSize<u32> =
        LogicalSize::new(CHAT_WIDTH, CHAT_HEIGHT).to_physical(monitor.scale_factor());
    let target_position = chat_window_position(
        current_position,
        current_size,
        target_size,
        *monitor.work_area(),
    );
    let opening = state.transition(
        DesktopPhase::CompanionIdle,
        DesktopPhase::CompanionChatOpening,
        "open companion chat",
    )?;
    if let Err(error) = emit_desktop_state(&app, opening) {
        state.update(|desktop| desktop.phase = DesktopPhase::CompanionIdle)?;
        return Err(error);
    }

    if window.set_position(target_position).is_err() {
        state.update(|desktop| desktop.phase = DesktopPhase::CompanionIdle)?;
        return Err(AppError::desktop_operation_failed(
            "position companion chat",
        ));
    }
    if window
        .set_size(LogicalSize::new(CHAT_WIDTH, CHAT_HEIGHT))
        .is_err()
    {
        window.set_position(current_position).map_err(|_| {
            AppError::desktop_operation_failed("rollback companion position after resize failure")
        })?;
        state.update(|desktop| desktop.phase = DesktopPhase::CompanionIdle)?;
        return Err(AppError::desktop_operation_failed(
            "resize companion for chat",
        ));
    }

    let snapshot = match state.mark_chat_open(current_position, current_size) {
        Ok(snapshot) => snapshot,
        Err(error) => {
            restore_window_geometry(
                &window,
                current_position,
                current_size,
                "rollback companion after state failure",
            )?;
            return Err(error);
        }
    };
    emit_desktop_state(&app, snapshot)?;
    Ok(snapshot)
}

#[tauri::command]
pub fn close_companion_chat<R: Runtime>(
    app: AppHandle<R>,
    state: State<'_, DesktopStateStore>,
) -> Result<DesktopState, AppError> {
    let current = state.snapshot()?;
    if current.phase == DesktopPhase::CompanionIdle {
        return Ok(current);
    }
    let anchor = state
        .companion_anchor()?
        .ok_or_else(AppError::state_unavailable)?;
    let window = companion_window(&app)?;
    let chat_position = window
        .outer_position()
        .map_err(|_| AppError::desktop_operation_failed("read companion chat position"))?;
    let chat_size = window
        .outer_size()
        .map_err(|_| AppError::desktop_operation_failed("read companion chat size"))?;
    let closing = state.transition(
        DesktopPhase::CompanionChatOpen,
        DesktopPhase::CompanionChatClosing,
        "close companion chat",
    )?;
    if let Err(error) = emit_desktop_state(&app, closing) {
        state.update(|desktop| desktop.phase = DesktopPhase::CompanionChatOpen)?;
        return Err(error);
    }

    if window.set_size(anchor.size).is_err() {
        state.update(|desktop| desktop.phase = DesktopPhase::CompanionChatOpen)?;
        return Err(AppError::desktop_operation_failed(
            "restore companion idle size",
        ));
    }
    if window.set_position(anchor.position).is_err() {
        window.set_size(chat_size).map_err(|_| {
            AppError::desktop_operation_failed("rollback companion size after position failure")
        })?;
        state.update(|desktop| desktop.phase = DesktopPhase::CompanionChatOpen)?;
        return Err(AppError::desktop_operation_failed(
            "restore companion idle position",
        ));
    }

    let snapshot = match state.mark_chat_closed() {
        Ok(snapshot) => snapshot,
        Err(error) => {
            restore_window_geometry(
                &window,
                chat_position,
                chat_size,
                "rollback companion chat after state failure",
            )?;
            return Err(error);
        }
    };
    emit_desktop_state(&app, snapshot)?;
    Ok(snapshot)
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
        TrayAction::Show => show_companion_window(app),
        TrayAction::Hide => hide_companion_window(app),
        TrayAction::OpenControlPanel => {
            match show_control_panel_window(app) {
                Ok(state) => {
                    if let Err(error) = emit_desktop_state(app, state) {
                        report_tray_error(menu_id, &error);
                    }
                }
                Err(error) => report_tray_error(menu_id, &error),
            }
            return;
        }
        TrayAction::RestoreInteraction => restore_companion_window(app),
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
        .text(MENU_OPEN_CONTROL_PANEL, "打开控制面板")
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
    let WindowEvent::CloseRequested { api, .. } = event else {
        return;
    };

    if window.label() == CONTROL_PANEL_WINDOW_LABEL {
        api.prevent_close();
        match restore_companion_after_control_panel_window(window.app_handle()) {
            Ok(snapshot) => {
                if let Err(error) = emit_desktop_state(window.app_handle(), snapshot) {
                    report_tray_error("restore companion on control panel close", &error);
                }
            }
            Err(error) => report_tray_error("restore companion on control panel close", &error),
        }
        return;
    }
    if window.label() != COMPANION_WINDOW_LABEL {
        return;
    }

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
            tray_action(MENU_OPEN_CONTROL_PANEL),
            Some(TrayAction::OpenControlPanel)
        );
        assert_eq!(
            tray_action(MENU_RESTORE_INTERACTION),
            Some(TrayAction::RestoreInteraction)
        );
        assert_eq!(tray_action(MENU_QUIT), Some(TrayAction::Quit));
        assert_eq!(tray_action("unknown"), None);
    }

    #[test]
    fn tauri_config_contains_exactly_two_product_windows() {
        let config: serde_json::Value =
            serde_json::from_str(include_str!("../tauri.conf.json")).expect("parse Tauri config");
        let windows = config["app"]["windows"]
            .as_array()
            .expect("Tauri windows array");
        let labels = windows
            .iter()
            .map(|window| window["label"].as_str().expect("window label"))
            .collect::<Vec<_>>();
        assert_eq!(
            labels,
            vec![COMPANION_WINDOW_LABEL, CONTROL_PANEL_WINDOW_LABEL]
        );
        assert_eq!(windows[0]["width"], IDLE_WIDTH);
        assert_eq!(windows[0]["height"], IDLE_HEIGHT);
        assert_eq!(windows[0]["minWidth"], IDLE_WIDTH);
        assert_eq!(windows[0]["minHeight"], IDLE_HEIGHT);
        assert_eq!(windows[0]["maxWidth"], CHAT_WIDTH);
        assert_eq!(windows[0]["maxHeight"], CHAT_HEIGHT);
        assert_eq!(windows[1]["width"], CONTROL_PANEL_WIDTH);
        assert_eq!(windows[1]["height"], CONTROL_PANEL_HEIGHT);
        assert_eq!(windows[1]["decorations"], false);
        assert_eq!(windows[1]["transparent"], true);

        let capabilities: serde_json::Value =
            serde_json::from_str(include_str!("../capabilities/default.json"))
                .expect("parse Tauri capabilities");
        assert_eq!(
            capabilities["windows"],
            serde_json::json!([COMPANION_WINDOW_LABEL, CONTROL_PANEL_WINDOW_LABEL])
        );
        let dialog_capability: serde_json::Value =
            serde_json::from_str(include_str!("../capabilities/control-panel-dialog.json"))
                .expect("parse Tauri dialog capability");
        assert_eq!(
            dialog_capability["windows"],
            serde_json::json!([CONTROL_PANEL_WINDOW_LABEL])
        );
        assert_eq!(
            dialog_capability["permissions"],
            serde_json::json!(["dialog:allow-open", "dialog:allow-save"])
        );
    }

    #[test]
    fn tauri_csp_allows_local_pixi_image_detection_and_worker_decoding() {
        let config: serde_json::Value =
            serde_json::from_str(include_str!("../tauri.conf.json")).expect("parse Tauri config");
        let csp = config["app"]["security"]["csp"]
            .as_str()
            .expect("Tauri CSP string");

        assert!(csp.contains("connect-src 'self' ipc: data: blob: tauri: fairy-character: http://fairy-character.localhost"));
        assert!(csp.contains("img-src 'self' data: blob: asset: http://asset.localhost fairy-character: http://fairy-character.localhost"));
        assert!(csp.contains("worker-src 'self' blob:"));
        assert!(!csp.contains("unsafe-eval"));
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

    #[test]
    fn chat_window_expands_left_and_up_when_the_pet_is_near_the_right_edge() {
        let position = chat_window_position(
            PhysicalPosition::new(1500, 600),
            PhysicalSize::new(220, 344),
            PhysicalSize::new(552, 382),
            PhysicalRect {
                position: PhysicalPosition::new(0, 0),
                size: PhysicalSize::new(1920, 1080),
            },
        );

        assert_eq!(position, PhysicalPosition::new(1168, 562));
    }

    #[test]
    fn chat_window_expands_right_when_the_pet_is_near_the_left_edge() {
        let position = chat_window_position(
            PhysicalPosition::new(24, 500),
            PhysicalSize::new(220, 344),
            PhysicalSize::new(552, 382),
            PhysicalRect {
                position: PhysicalPosition::new(0, 0),
                size: PhysicalSize::new(1920, 1080),
            },
        );

        assert_eq!(position, PhysicalPosition::new(24, 462));
    }

    #[test]
    fn chat_window_is_clamped_to_a_monitor_with_a_negative_origin() {
        let position = chat_window_position(
            PhysicalPosition::new(-1200, -20),
            PhysicalSize::new(220, 344),
            PhysicalSize::new(552, 382),
            PhysicalRect {
                position: PhysicalPosition::new(-1440, 0),
                size: PhysicalSize::new(1440, 900),
            },
        );

        assert_eq!(position, PhysicalPosition::new(-1200, 0));
    }

    #[test]
    fn replacement_panel_keeps_the_pet_bottom_right_anchor() {
        let position = replacement_window_position(
            PhysicalPosition::new(1500, 600),
            PhysicalSize::new(220, 344),
            PhysicalSize::new(560, 620),
            PhysicalRect {
                position: PhysicalPosition::new(0, 0),
                size: PhysicalSize::new(1920, 1080),
            },
        );

        assert_eq!(position, PhysicalPosition::new(1160, 324));
    }

    #[test]
    fn replacement_panel_is_clamped_on_a_negative_origin_monitor() {
        let position = replacement_window_position(
            PhysicalPosition::new(-1380, 420),
            PhysicalSize::new(220, 344),
            PhysicalSize::new(560, 620),
            PhysicalRect {
                position: PhysicalPosition::new(-1440, 0),
                size: PhysicalSize::new(1440, 900),
            },
        );

        assert_eq!(position, PhysicalPosition::new(-1440, 144));
    }

    #[test]
    fn duplicate_desktop_transition_is_rejected_without_changing_phase() {
        let store = DesktopStateStore::default();
        let transitioning = store
            .transition(
                DesktopPhase::CompanionIdle,
                DesktopPhase::TransitioningToSettings,
                "show control panel",
            )
            .expect("first transition should start");
        assert_eq!(transitioning.phase, DesktopPhase::TransitioningToSettings);

        let error = store
            .transition(
                DesktopPhase::CompanionIdle,
                DesktopPhase::TransitioningToSettings,
                "show control panel",
            )
            .expect_err("duplicate transition must be rejected");
        assert_eq!(error.code, "DESKTOP_TRANSITION_REJECTED");
        assert_eq!(
            store
                .snapshot()
                .expect("state should remain readable")
                .phase,
            DesktopPhase::TransitioningToSettings
        );
    }

    #[test]
    fn replacement_state_returns_to_one_visible_companion_terminal_state() {
        let store = DesktopStateStore::default();
        store
            .transition(
                DesktopPhase::CompanionIdle,
                DesktopPhase::TransitioningToSettings,
                "show control panel",
            )
            .expect("transition should start");
        let settings = store
            .mark_control_panel_visible(
                PhysicalPosition::new(100, 200),
                PhysicalSize::new(220, 344),
            )
            .expect("settings should become visible");
        assert!(!settings.visible);
        assert!(settings.control_panel_visible);
        assert_eq!(settings.phase, DesktopPhase::ControlPanelVisible);

        store
            .transition(
                DesktopPhase::ControlPanelVisible,
                DesktopPhase::TransitioningToCompanion,
                "restore companion",
            )
            .expect("restore should start");
        let restored = store
            .mark_companion_restored()
            .expect("companion should be restored");
        assert!(restored.visible);
        assert!(!restored.control_panel_visible);
        assert_eq!(restored.phase, DesktopPhase::CompanionIdle);
        assert!(
            store
                .replacement_anchor()
                .expect("anchor should be readable")
                .is_none()
        );
    }
}
