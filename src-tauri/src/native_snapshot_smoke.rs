use std::{
    path::{Path, PathBuf},
    str::FromStr,
    time::Duration,
};

use fairy_domain::{CharacterBriefInput, CharacterId, Revision, VisualPackId};
use tauri::{App, AppHandle, Manager};

use crate::{app_error::AppError, app_state::AppState};

const SNAPSHOT_PATH_ENV: &str = "FAIRY_NATIVE_SNAPSHOT_PATH";
const SNAPSHOT_DELAY_MS_ENV: &str = "FAIRY_NATIVE_SNAPSHOT_DELAY_MS";
const SNAPSHOT_CHARACTER_ID_ENV: &str = "FAIRY_NATIVE_SNAPSHOT_CHARACTER_ID";
const SNAPSHOT_VISUAL_PACK_ID_ENV: &str = "FAIRY_NATIVE_SNAPSHOT_VISUAL_PACK_ID";
const DEFAULT_SNAPSHOT_CHARACTER_ID: &str = "019f59c8-3439-7130-8714-5629af2cebec";
const DEFAULT_SNAPSHOT_VISUAL_PACK_ID: &str = "fairy.atri";

pub fn seed_from_env(state: &AppState) -> Result<(), AppError> {
    if std::env::var_os(SNAPSHOT_PATH_ENV).is_none() {
        return Ok(());
    }
    let character_id = std::env::var(SNAPSHOT_CHARACTER_ID_ENV)
        .unwrap_or_else(|_| DEFAULT_SNAPSHOT_CHARACTER_ID.to_owned());
    let visual_pack_id = std::env::var(SNAPSHOT_VISUAL_PACK_ID_ENV)
        .unwrap_or_else(|_| DEFAULT_SNAPSHOT_VISUAL_PACK_ID.to_owned());
    let character_id = CharacterId::from_str(&character_id).map_err(|error| {
        AppError::from(fairy_domain::FairyError::new(
            fairy_domain::ErrorCode::StorageCorrupted,
            format!("原生快照 smoke 角色 ID 无效：{error}"),
            false,
        ))
    })?;
    let visual_pack_id = VisualPackId::from_str(&visual_pack_id).map_err(AppError::from)?;
    state
        .visual_packs
        .get(&visual_pack_id)
        .map_err(AppError::from)?;
    if state
        .characters
        .get(character_id, Revision::INITIAL)
        .is_err()
    {
        state
            .characters
            .create_with_id(
                character_id,
                CharacterBriefInput {
                    name: "亚托莉".to_owned(),
                    description: "温柔、敏锐、简洁回应的桌面陪伴角色。".to_owned(),
                    dialogue_style: None,
                },
            )
            .map_err(AppError::from)?;
    }
    state
        .character_appearances
        .assign(character_id, visual_pack_id)
        .map_err(AppError::from)?;
    state
        .characters
        .activate(character_id, Revision::INITIAL)
        .map_err(AppError::from)?;
    Ok(())
}

pub fn schedule_from_env(app: &App) {
    let Ok(output) = std::env::var(SNAPSHOT_PATH_ENV) else {
        return;
    };
    let delay = std::env::var(SNAPSHOT_DELAY_MS_ENV)
        .ok()
        .and_then(|value| value.parse::<u64>().ok())
        .unwrap_or(4_000);
    let app_handle = app.handle().clone();
    std::thread::spawn(move || {
        std::thread::sleep(Duration::from_millis(delay));
        let output_path = PathBuf::from(output);
        let error_path = output_path.with_extension("err.txt");
        let closure_error_path = error_path.clone();
        let main_thread = app_handle.clone();
        if let Err(error) = app_handle.run_on_main_thread(move || {
            if let Err(error) = take_companion_snapshot(&main_thread, output_path) {
                write_error(&closure_error_path, &error);
            }
        }) {
            write_error(&error_path, &format!("run_on_main_thread failed: {error}"));
        }
    });
}

fn take_companion_snapshot(app: &AppHandle, output_path: PathBuf) -> Result<(), String> {
    let Some(parent) = output_path.parent() else {
        return Err("snapshot output path has no parent directory".to_owned());
    };
    std::fs::create_dir_all(parent).map_err(|error| format!("create snapshot dir: {error}"))?;
    let error_path = output_path.with_extension("err.txt");
    let Some(window) = app.get_webview_window("companion") else {
        return Err("companion webview window not found".to_owned());
    };
    window
        .with_webview(move |webview| unsafe {
            let view: &objc2_web_kit::WKWebView = &*webview.inner().cast();
            let handler = block2::RcBlock::new(
                move |image: *mut objc2_app_kit::NSImage, error: *mut objc2_foundation::NSError| {
                    if !error.is_null() {
                        write_error(&error_path, "WKWebView snapshot returned NSError");
                        return;
                    }
                    if image.is_null() {
                        write_error(&error_path, "WKWebView snapshot returned null image");
                        return;
                    }
                    let image = &*image;
                    let Some(data) = image.TIFFRepresentation() else {
                        write_error(&error_path, "WKWebView snapshot produced no TIFF data");
                        return;
                    };
                    let output = objc2_foundation::NSString::from_str(
                        output_path.to_string_lossy().as_ref(),
                    );
                    if !data.writeToFile_atomically(&output, true) {
                        write_error(&error_path, "failed to write WKWebView TIFF snapshot");
                    }
                },
            );
            view.takeSnapshotWithConfiguration_completionHandler(None, &handler);
        })
        .map_err(|error| format!("with_webview failed: {error}"))
}

fn write_error(path: &Path, message: &str) {
    if let Some(parent) = path.parent() {
        let _ = std::fs::create_dir_all(parent);
    }
    let _ = std::fs::write(path, message);
    eprintln!("FAIRY_NATIVE_SNAPSHOT_FAILURE: {message}");
}
