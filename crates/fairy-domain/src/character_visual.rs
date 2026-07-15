use std::collections::BTreeSet;
use std::fmt;
use std::str::FromStr;

use serde::{Deserialize, Serialize};

use crate::{ErrorCode, FairyError};

pub const CHARACTER_VISUAL_SCHEMA_VERSION: u32 = 2;
const MAX_PACK_ID_BYTES: usize = 64;
const MAX_DISPLAY_NAME_CHARS: usize = 48;
const MAX_FRAME_EDGE: u16 = 512;
const MAX_SCALE: u8 = 8;
const MAX_STATES: usize = 16;
const MAX_STATE_ID_BYTES: usize = 32;
const MAX_STATE_DESCRIPTION_CHARS: usize = 96;

#[derive(Clone, Debug, Eq, Ord, PartialEq, PartialOrd, Serialize)]
#[serde(transparent)]
pub struct VisualPackId(String);

impl VisualPackId {
    #[must_use]
    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl fmt::Display for VisualPackId {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.0)
    }
}

impl FromStr for VisualPackId {
    type Err = FairyError;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        if value.is_empty()
            || value.len() > MAX_PACK_ID_BYTES
            || !value.bytes().all(|byte| {
                byte.is_ascii_lowercase() || byte.is_ascii_digit() || byte == b'.' || byte == b'-'
            })
            || value.starts_with(['.', '-'])
            || value.ends_with(['.', '-'])
        {
            return Err(invalid_manifest(
                "视觉包 ID 必须是 1-64 个小写 ASCII 字母、数字、点或短横线",
            ));
        }
        Ok(Self(value.to_owned()))
    }
}

impl<'de> Deserialize<'de> for VisualPackId {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        let value = String::deserialize(deserializer)?;
        Self::from_str(&value).map_err(serde::de::Error::custom)
    }
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum VisualRenderer {
    StateImages,
}

#[derive(Clone, Debug, Eq, Ord, PartialEq, PartialOrd, Serialize)]
#[serde(transparent)]
pub struct VisualStateId(String);

impl VisualStateId {
    #[must_use]
    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl fmt::Display for VisualStateId {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.0)
    }
}

impl FromStr for VisualStateId {
    type Err = FairyError;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        let first_last_valid = value
            .bytes()
            .next()
            .zip(value.bytes().next_back())
            .is_some_and(|(first, last)| {
                first.is_ascii_lowercase() && (last.is_ascii_lowercase() || last.is_ascii_digit())
            });
        if value.is_empty()
            || value.len() > MAX_STATE_ID_BYTES
            || !first_last_valid
            || !value.bytes().all(|byte| {
                byte.is_ascii_lowercase() || byte.is_ascii_digit() || byte == b'_' || byte == b'-'
            })
        {
            return Err(invalid_manifest(
                "视觉状态 ID 必须是 1-32 个小写 ASCII 字母、数字、下划线或短横线，且以字母开头",
            ));
        }
        Ok(Self(value.to_owned()))
    }
}

impl<'de> Deserialize<'de> for VisualStateId {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        let value = String::deserialize(deserializer)?;
        Self::from_str(&value).map_err(serde::de::Error::custom)
    }
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct FrameSize {
    pub width: u16,
    pub height: u16,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct FrameAnchor {
    pub x: u16,
    pub y: u16,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct VisualStateImage {
    pub id: VisualStateId,
    pub description: String,
    pub image_path: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct CharacterVisualManifestInput {
    schema_version: u32,
    pack_id: VisualPackId,
    display_name: String,
    renderer: VisualRenderer,
    frame: FrameSize,
    scale: u8,
    anchor: FrameAnchor,
    states: Vec<VisualStateImage>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct VerifiedVisualPack {
    schema_version: u32,
    pack_id: VisualPackId,
    display_name: String,
    renderer: VisualRenderer,
    frame: FrameSize,
    scale: u8,
    anchor: FrameAnchor,
    states: Vec<VisualStateImage>,
}

impl VerifiedVisualPack {
    #[must_use]
    pub const fn schema_version(&self) -> u32 {
        self.schema_version
    }

    #[must_use]
    pub fn pack_id(&self) -> &VisualPackId {
        &self.pack_id
    }

    #[must_use]
    pub fn display_name(&self) -> &str {
        &self.display_name
    }

    #[must_use]
    pub const fn renderer(&self) -> VisualRenderer {
        self.renderer
    }

    #[must_use]
    pub const fn frame(&self) -> FrameSize {
        self.frame
    }

    #[must_use]
    pub const fn scale(&self) -> u8 {
        self.scale
    }

    #[must_use]
    pub const fn anchor(&self) -> FrameAnchor {
        self.anchor
    }

    #[must_use]
    pub fn states(&self) -> &[VisualStateImage] {
        &self.states
    }

    #[must_use]
    pub fn state_by_id(&self, id: &str) -> Option<&VisualStateImage> {
        self.states.iter().find(|state| state.id.as_str() == id)
    }
}

#[derive(Clone, Copy, Debug, Default)]
pub struct CharacterVisualCompiler;

impl CharacterVisualCompiler {
    pub fn compile_json(&self, source: &str) -> Result<VerifiedVisualPack, FairyError> {
        let input: CharacterVisualManifestInput =
            serde_json::from_str(source).map_err(|_| invalid_manifest("角色视觉清单格式无效"))?;
        self.compile(input)
    }

    fn compile(
        &self,
        input: CharacterVisualManifestInput,
    ) -> Result<VerifiedVisualPack, FairyError> {
        if input.schema_version != CHARACTER_VISUAL_SCHEMA_VERSION {
            return Err(invalid_manifest("不支持该角色视觉清单版本"));
        }
        validate_display_name(&input.display_name)?;
        validate_frame_geometry(input.frame, input.scale, input.anchor)?;
        validate_states(&input.states)?;

        Ok(VerifiedVisualPack {
            schema_version: input.schema_version,
            pack_id: input.pack_id,
            display_name: input.display_name,
            renderer: input.renderer,
            frame: input.frame,
            scale: input.scale,
            anchor: input.anchor,
            states: input.states,
        })
    }
}

fn validate_display_name(value: &str) -> Result<(), FairyError> {
    let length = value.chars().count();
    if length == 0
        || length > MAX_DISPLAY_NAME_CHARS
        || value.trim() != value
        || value.chars().any(char::is_control)
    {
        return Err(invalid_manifest(
            "视觉包名称必须是 1-48 个无首尾空白或控制字符的 Unicode 字符",
        ));
    }
    Ok(())
}

fn validate_image_path(value: &str) -> Result<(), FairyError> {
    let valid_segments = value.strip_prefix("/characters/").is_some_and(|relative| {
        !relative.is_empty()
            && relative
                .split('/')
                .all(|segment| !segment.is_empty() && segment != "." && segment != "..")
    });
    if !valid_segments
        || !value.ends_with(".png")
        || value.contains("://")
        || value.contains(['\\', '?', '#'])
    {
        return Err(invalid_manifest(
            "角色状态图片必须是 /characters/ 下的本地 PNG 路径",
        ));
    }
    Ok(())
}

fn validate_frame_geometry(
    frame: FrameSize,
    scale: u8,
    anchor: FrameAnchor,
) -> Result<(), FairyError> {
    let frame_valid =
        (1..=MAX_FRAME_EDGE).contains(&frame.width) && (1..=MAX_FRAME_EDGE).contains(&frame.height);
    if !frame_valid || !(1..=MAX_SCALE).contains(&scale) {
        return Err(invalid_manifest("角色显示尺寸或整数缩放倍率无效"));
    }
    if anchor.x >= frame.width || anchor.y >= frame.height {
        return Err(invalid_manifest("角色锚点必须位于显示尺寸范围内"));
    }
    Ok(())
}

fn validate_states(states: &[VisualStateImage]) -> Result<(), FairyError> {
    if states.is_empty() || states.len() > MAX_STATES {
        return Err(invalid_manifest("角色视觉包必须声明 1-16 个状态图片"));
    }

    let mut ids = BTreeSet::new();
    let mut image_paths = BTreeSet::new();
    let mut has_idle = false;
    for state in states {
        validate_state_description(&state.description)?;
        validate_image_path(&state.image_path)?;
        if !ids.insert(state.id.as_str()) || !image_paths.insert(state.image_path.as_str()) {
            return Err(invalid_manifest("角色视觉状态 ID 和图片路径不得重复"));
        }
        has_idle |= state.id.as_str() == "idle";
    }
    if !has_idle {
        return Err(invalid_manifest("角色视觉包必须声明 idle 状态"));
    }
    Ok(())
}

fn validate_state_description(value: &str) -> Result<(), FairyError> {
    let length = value.chars().count();
    if length == 0
        || length > MAX_STATE_DESCRIPTION_CHARS
        || value.trim() != value
        || value.chars().any(char::is_control)
    {
        return Err(invalid_manifest(
            "视觉状态描述必须是 1-96 个无首尾空白或控制字符的 Unicode 字符",
        ));
    }
    Ok(())
}

fn invalid_manifest(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::InvalidVisualManifest, message, false)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn valid_manifest() -> serde_json::Value {
        serde_json::json!({
            "schemaVersion": 2,
            "packId": "fairy.atri",
            "displayName": "亚托莉",
            "renderer": "state_images",
            "frame": { "width": 128, "height": 192 },
            "scale": 1,
            "anchor": { "x": 64, "y": 190 },
            "states": [
                {
                    "id": "idle",
                    "description": "安静站立，适合普通等待。",
                    "imagePath": "/characters/atri/idle.png"
                },
                {
                    "id": "happy",
                    "description": "开心微笑，适合轻松回应。",
                    "imagePath": "/characters/atri/happy.png"
                }
            ]
        })
    }

    fn compile(value: &serde_json::Value) -> Result<VerifiedVisualPack, FairyError> {
        CharacterVisualCompiler.compile_json(&serde_json::to_string(value).expect("serialize"))
    }

    #[test]
    fn compiles_minimal_local_state_images_manifest() {
        let mut manifest = valid_manifest();
        manifest["states"] = serde_json::json!([
            {
                "id": "idle",
                "description": "安静站立，适合普通等待。",
                "imagePath": "/characters/atri/idle.png"
            }
        ]);

        let pack = compile(&manifest).expect("valid manifest");

        assert_eq!(pack.schema_version(), 2);
        assert_eq!(pack.pack_id().as_str(), "fairy.atri");
        assert_eq!(pack.renderer(), VisualRenderer::StateImages);
        assert_eq!(
            pack.frame(),
            FrameSize {
                width: 128,
                height: 192
            }
        );
        assert_eq!(
            pack.state_by_id("idle").expect("idle state").image_path,
            "/characters/atri/idle.png"
        );
        assert!(pack.state_by_id("thinking").is_none());
    }

    #[test]
    fn rejects_unknown_fields_missing_idle_and_renderer() {
        let mut extra = valid_manifest();
        extra["script"] = serde_json::json!("role.js");
        assert_eq!(
            compile(&extra).expect_err("extra field").code,
            ErrorCode::InvalidVisualManifest
        );

        let mut missing_idle = valid_manifest();
        missing_idle["states"] = serde_json::json!([
            {
                "id": "happy",
                "description": "开心微笑，适合轻松回应。",
                "imagePath": "/characters/atri/happy.png"
            }
        ]);
        assert_eq!(
            compile(&missing_idle).expect_err("missing idle").code,
            ErrorCode::InvalidVisualManifest
        );

        let mut renderer = valid_manifest();
        renderer["renderer"] = serde_json::json!("sprite_sheet");
        assert_eq!(
            compile(&renderer).expect_err("unknown renderer").code,
            ErrorCode::InvalidVisualManifest
        );
    }

    #[test]
    fn rejects_remote_traversal_and_non_png_image_paths() {
        for path in [
            "https://example.com/idle.png",
            "/characters/../secret.png",
            "/characters/atri/idle.webp",
            "/characters/atri/idle.png?cache=1",
        ] {
            let mut manifest = valid_manifest();
            manifest["states"][0]["imagePath"] = serde_json::json!(path);
            assert_eq!(
                compile(&manifest).expect_err("invalid path").code,
                ErrorCode::InvalidVisualManifest,
                "path={path}"
            );
        }
    }

    #[test]
    fn rejects_invalid_geometry_anchor_scale_and_duplicate_states() {
        let cases = [
            ("/frame/width", serde_json::json!(0)),
            ("/scale", serde_json::json!(0)),
            ("/anchor/x", serde_json::json!(128)),
        ];

        for (pointer, replacement) in cases {
            let mut manifest = valid_manifest();
            *manifest.pointer_mut(pointer).expect("test pointer") = replacement;
            assert_eq!(
                compile(&manifest)
                    .expect_err("invalid numeric boundary")
                    .code,
                ErrorCode::InvalidVisualManifest,
                "pointer={pointer}"
            );
        }

        let mut duplicate_id = valid_manifest();
        duplicate_id["states"][1]["id"] = serde_json::json!("idle");
        assert_eq!(
            compile(&duplicate_id).expect_err("duplicate id").code,
            ErrorCode::InvalidVisualManifest
        );

        let mut duplicate_image = valid_manifest();
        duplicate_image["states"][1]["imagePath"] = serde_json::json!("/characters/atri/idle.png");
        assert_eq!(
            compile(&duplicate_image)
                .expect_err("duplicate image path")
                .code,
            ErrorCode::InvalidVisualManifest
        );
    }

    #[test]
    fn visual_pack_and_state_ids_are_strict_and_do_not_trim() {
        for value in ["", "Fairy.atri", ".atri", "atri-", "atri role", " atri"] {
            assert_eq!(
                VisualPackId::from_str(value)
                    .expect_err("invalid pack id")
                    .code,
                ErrorCode::InvalidVisualManifest,
                "value={value:?}"
            );
        }
        assert_eq!(
            VisualPackId::from_str("fairy.atri-2")
                .expect("valid pack id")
                .as_str(),
            "fairy.atri-2"
        );

        for value in ["", "Idle", "_idle", "idle_", "idle state", " idle"] {
            assert_eq!(
                VisualStateId::from_str(value)
                    .expect_err("invalid state id")
                    .code,
                ErrorCode::InvalidVisualManifest,
                "value={value:?}"
            );
        }
        assert_eq!(
            VisualStateId::from_str("happy-2")
                .expect("valid state id")
                .as_str(),
            "happy-2"
        );
    }
}
