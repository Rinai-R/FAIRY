use std::fs;
use std::path::{Path, PathBuf};

use tauri::http::{Response, StatusCode, header::CONTENT_TYPE};
use tauri::{Manager, Runtime, UriSchemeContext};

use crate::app_state::AppState;

pub fn serve_character_asset<R: Runtime>(
    context: UriSchemeContext<'_, R>,
    request: tauri::http::Request<Vec<u8>>,
) -> Response<Vec<u8>> {
    let Some(state) = context.app_handle().try_state::<AppState>() else {
        return text_response(StatusCode::INTERNAL_SERVER_ERROR, "app state unavailable");
    };
    let Some(relative) = request.uri().path().strip_prefix('/') else {
        return text_response(StatusCode::BAD_REQUEST, "invalid character asset path");
    };
    let Some(path) = resolve_character_asset_path(state.visual_packs.root(), relative) else {
        return text_response(StatusCode::BAD_REQUEST, "invalid character asset path");
    };
    match fs::read(path) {
        Ok(bytes) => Response::builder()
            .status(StatusCode::OK)
            .header(CONTENT_TYPE, "image/png")
            .header("Access-Control-Allow-Origin", "*")
            .body(bytes)
            .expect("image response should build"),
        Err(_) => text_response(StatusCode::NOT_FOUND, "character asset not found"),
    }
}

fn resolve_character_asset_path(visual_packs_root: &Path, relative: &str) -> Option<PathBuf> {
    validate_relative_png_path(relative).map(|path| visual_packs_root.join(path))
}

fn validate_relative_png_path(value: &str) -> Option<PathBuf> {
    if value.is_empty()
        || !value.ends_with(".png")
        || value.contains("://")
        || value.contains(['\\', '?', '#'])
    {
        return None;
    }
    let path = Path::new(value);
    if path.is_absolute()
        || path
            .components()
            .any(|component| !matches!(component, std::path::Component::Normal(_)))
    {
        return None;
    }
    Some(path.to_path_buf())
}

fn text_response(status: StatusCode, body: &'static str) -> Response<Vec<u8>> {
    Response::builder()
        .status(status)
        .header(CONTENT_TYPE, "text/plain; charset=utf-8")
        .header("Access-Control-Allow-Origin", "*")
        .body(body.as_bytes().to_vec())
        .expect("text response should build")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn validates_only_relative_png_asset_paths() {
        assert_eq!(
            validate_relative_png_path("fairy.local/images/idle.png"),
            Some(PathBuf::from("fairy.local/images/idle.png"))
        );
        assert_eq!(validate_relative_png_path("../idle.png"), None);
        assert_eq!(validate_relative_png_path("fairy.local/../idle.png"), None);
        assert_eq!(validate_relative_png_path("fairy.local/idle.webp"), None);
        assert_eq!(validate_relative_png_path("fairy.local/idle.png?x=1"), None);
        assert_eq!(validate_relative_png_path("/fairy.local/idle.png"), None);
    }

    #[test]
    fn protocol_responses_allow_pixi_fetch_from_the_tauri_webview() {
        let response = text_response(StatusCode::NOT_FOUND, "missing");

        assert_eq!(
            response
                .headers()
                .get("Access-Control-Allow-Origin")
                .expect("cors header"),
            "*"
        );
    }

    #[test]
    fn resolves_assets_against_the_state_visual_registry_root() {
        assert_eq!(
            resolve_character_asset_path(
                Path::new("/tmp/fairy/harness/v1/visual-packs"),
                "fairy.local/images/idle.png"
            ),
            Some(PathBuf::from(
                "/tmp/fairy/harness/v1/visual-packs/fairy.local/images/idle.png"
            ))
        );
    }
}
