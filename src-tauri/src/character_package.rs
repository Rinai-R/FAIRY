use std::collections::BTreeSet;
use std::fs::{self, File};
use std::io::{Read, Seek, Write};
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use fairy_domain::{
    CharacterBriefInput, CharacterCompiler, CharacterId, CharacterSnapshot,
    CharacterVisualCompiler, ErrorCode, FairyError, FrameAnchor, FrameSize, Revision,
    VerifiedVisualPack, VisualPackId, VisualRenderer, VisualStateId, VisualStateImage,
};
use serde::{Deserialize, Serialize};
use zip::write::SimpleFileOptions;
use zip::{CompressionMethod, ZipArchive, ZipWriter};

const PACKAGE_MANIFEST_FILE: &str = "manifest.json";
const RUNTIME_VISUAL_SCHEMA_VERSION: u32 = 2;
const PACKAGE_SCHEMA_VERSION: u32 = 1;
const MAX_ARCHIVE_MANIFEST_BYTES: usize = 1024 * 1024;
const MAX_ARCHIVE_IMAGE_BYTES: usize = 64 * 1024 * 1024;
const PNG_SIGNATURE: &[u8; 8] = b"\x89PNG\r\n\x1a\n";

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ImportedCharacterPackage {
    pub brief: CharacterBriefInput,
    pub visual_pack_id: VisualPackId,
    pub visual: VerifiedVisualPack,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct CharacterPackageManifest {
    schema_version: u32,
    package_id: VisualPackId,
    character: CharacterBriefInput,
    visual: CharacterPackageVisual,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct CharacterPackageVisual {
    display_name: String,
    renderer: VisualRenderer,
    frame: FrameSize,
    scale: u8,
    anchor: FrameAnchor,
    states: Vec<CharacterPackageState>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct CharacterPackageState {
    id: VisualStateId,
    description: String,
    file: String,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct RuntimeVisualManifest<'a> {
    schema_version: u32,
    pack_id: &'a VisualPackId,
    display_name: &'a str,
    renderer: VisualRenderer,
    frame: FrameSize,
    scale: u8,
    anchor: FrameAnchor,
    states: Vec<VisualStateImage>,
}

pub fn import_character_package(
    visual_library_directory: impl AsRef<Path>,
    package_path: impl AsRef<Path>,
) -> Result<ImportedCharacterPackage, FairyError> {
    let visual_library_directory = visual_library_directory.as_ref();
    let package_path = package_path.as_ref();
    if package_path.is_dir() {
        return import_directory_package(visual_library_directory, package_path);
    }
    if !package_path.is_file() {
        return Err(invalid_package(
            "角色包必须是本地目录、.pack 文件或 .zip 文件",
        ));
    }
    if !is_supported_package_file(package_path) {
        return Err(invalid_package("角色包文件必须使用 .pack 或 .zip 后缀"));
    }
    import_archive_package(visual_library_directory, package_path)
}

pub fn export_character_package(
    visual_library_directory: impl AsRef<Path>,
    character: &CharacterSnapshot,
    visual: &VerifiedVisualPack,
    output_path: impl AsRef<Path>,
) -> Result<(), FairyError> {
    let visual_library_directory = visual_library_directory.as_ref();
    let output_path = output_path.as_ref();
    if output_path.exists() && output_path.is_dir() {
        return Err(invalid_package("角色包导出路径不能是目录"));
    }
    if output_path
        .extension()
        .and_then(|extension| extension.to_str())
        .map(str::to_ascii_lowercase)
        .as_deref()
        != Some("pack")
    {
        return Err(invalid_package("角色包导出文件必须使用 .pack 后缀"));
    }

    let mut exported_states = Vec::with_capacity(visual.states().len());
    let mut exported_files = Vec::with_capacity(visual.states().len());
    let mut seen_files = BTreeSet::new();
    for state in visual.states() {
        let (package_file, source_file) = resolve_export_asset(
            visual_library_directory,
            visual.pack_id(),
            &state.image_path,
        )?;
        if seen_files.insert(package_file.clone()) {
            exported_files.push((package_file.clone(), source_file));
        }
        exported_states.push(CharacterPackageState {
            id: state.id.clone(),
            description: state.description.clone(),
            file: package_file.to_string_lossy().replace('\\', "/"),
        });
    }

    let identity = character.identity();
    let manifest = CharacterPackageManifest {
        schema_version: PACKAGE_SCHEMA_VERSION,
        package_id: visual.pack_id().clone(),
        character: CharacterBriefInput {
            name: identity.name.clone(),
            description: identity.description.clone(),
            dialogue_style: identity.dialogue_style.clone(),
        },
        visual: CharacterPackageVisual {
            display_name: visual.display_name().to_owned(),
            renderer: visual.renderer(),
            frame: visual.frame(),
            scale: visual.scale(),
            anchor: visual.anchor(),
            states: exported_states,
        },
    };
    let manifest_source =
        serde_json::to_vec_pretty(&manifest).map_err(|_| invalid_package("角色包清单生成失败"))?;
    write_package_archive(output_path, &manifest_source, &exported_files)
}

pub fn import_directory_package(
    visual_library_directory: impl AsRef<Path>,
    package_directory: impl AsRef<Path>,
) -> Result<ImportedCharacterPackage, FairyError> {
    let visual_library_directory = visual_library_directory.as_ref();
    let package_directory = package_directory.as_ref();
    if !package_directory.is_dir() {
        return Err(invalid_package("角色包必须是本地目录"));
    }
    let manifest_path = package_directory.join(PACKAGE_MANIFEST_FILE);
    let manifest = parse_manifest(
        &fs::read_to_string(manifest_path).map_err(|_| invalid_package("无法读取角色包清单"))?,
    )?;
    install_manifest_package(
        visual_library_directory,
        manifest,
        |relative_file, destination| {
            let bytes = read_package_file_bytes(
                &package_directory.join(relative_file),
                MAX_ARCHIVE_IMAGE_BYTES,
                "无法读取角色状态图片",
            )?;
            validate_png_bytes(&bytes)?;
            fs::write(destination, bytes).map_err(|_| storage_io("无法写入角色状态图片"))
        },
    )
}

fn import_archive_package(
    visual_library_directory: &Path,
    package_file: &Path,
) -> Result<ImportedCharacterPackage, FairyError> {
    let file = File::open(package_file).map_err(|_| invalid_package("无法读取角色包文件"))?;
    let mut archive =
        ZipArchive::new(file).map_err(|_| invalid_package("角色包文件不是有效 zip 包"))?;
    let package_root = archive_root_prefix(&mut archive)?;
    let manifest_entry = format!("{package_root}{PACKAGE_MANIFEST_FILE}");
    let manifest_source = read_archive_entry_string(
        &mut archive,
        &manifest_entry,
        MAX_ARCHIVE_MANIFEST_BYTES,
        "无法读取角色包清单",
    )?;
    let manifest = parse_manifest(&manifest_source)?;

    install_manifest_package(
        visual_library_directory,
        manifest,
        |relative_file, destination| {
            let entry_name = format!(
                "{package_root}{}",
                relative_file.to_string_lossy().replace('\\', "/")
            );
            let bytes = read_archive_entry_bytes(
                &mut archive,
                &entry_name,
                MAX_ARCHIVE_IMAGE_BYTES,
                "无法读取角色状态图片",
            )?;
            validate_png_bytes(&bytes)?;
            fs::write(destination, bytes).map_err(|_| storage_io("无法写入角色状态图片"))
        },
    )
}

fn parse_manifest(source: &str) -> Result<CharacterPackageManifest, FairyError> {
    let manifest: CharacterPackageManifest =
        serde_json::from_str(source).map_err(|_| invalid_package("角色包清单格式无效"))?;
    if manifest.schema_version != PACKAGE_SCHEMA_VERSION {
        return Err(invalid_package("不支持该角色包版本"));
    }
    Ok(manifest)
}

fn install_manifest_package(
    visual_library_directory: &Path,
    manifest: CharacterPackageManifest,
    mut copy_asset: impl FnMut(&Path, &Path) -> Result<(), FairyError>,
) -> Result<ImportedCharacterPackage, FairyError> {
    CharacterCompiler.compile(
        CharacterId::new(),
        Revision::INITIAL,
        manifest.character.clone(),
    )?;

    let mut runtime_states = Vec::with_capacity(manifest.visual.states.len());
    let mut state_files = Vec::with_capacity(manifest.visual.states.len());
    for state in &manifest.visual.states {
        let relative_file = validate_package_file(&state.file)?.to_path_buf();
        runtime_states.push(VisualStateImage {
            id: state.id.clone(),
            description: state.description.clone(),
            image_path: format!(
                "fairy-character://localhost/{}/{}",
                manifest.package_id,
                relative_file.to_string_lossy().replace('\\', "/")
            ),
        });
        state_files.push(relative_file);
    }

    let runtime_manifest = RuntimeVisualManifest {
        schema_version: RUNTIME_VISUAL_SCHEMA_VERSION,
        pack_id: &manifest.package_id,
        display_name: &manifest.visual.display_name,
        renderer: manifest.visual.renderer,
        frame: manifest.visual.frame,
        scale: manifest.visual.scale,
        anchor: manifest.visual.anchor,
        states: runtime_states,
    };
    let runtime_source = serde_json::to_string_pretty(&runtime_manifest)
        .map_err(|_| invalid_package("角色视觉清单生成失败"))?;
    let visual = CharacterVisualCompiler.compile_json(&runtime_source)?;

    fs::create_dir_all(visual_library_directory)
        .map_err(|_| storage_io("无法创建角色视觉包库目录"))?;
    let target_directory = visual_library_directory.join(manifest.package_id.as_str());
    let staging_directory =
        visual_library_directory.join(unique_import_directory_name(&manifest.package_id));
    if staging_directory.exists() {
        fs::remove_dir_all(&staging_directory)
            .map_err(|_| storage_io("无法清理角色视觉包暂存目录"))?;
    }
    fs::create_dir_all(&staging_directory).map_err(|_| storage_io("无法创建角色视觉包目录"))?;

    let install_result = (|| {
        for relative_file in &state_files {
            let destination = staging_directory.join(relative_file);
            if let Some(parent) = destination.parent() {
                fs::create_dir_all(parent).map_err(|_| storage_io("无法创建角色图片目录"))?;
            }
            copy_asset(relative_file, &destination)?;
        }
        fs::write(
            staging_directory.join(PACKAGE_MANIFEST_FILE),
            runtime_source,
        )
        .map_err(|_| storage_io("无法写入角色视觉清单"))?;
        replace_visual_pack_directory(&staging_directory, &target_directory)?;
        Ok(())
    })();
    if let Err(error) = install_result {
        let _ = fs::remove_dir_all(&staging_directory);
        return Err(error);
    }

    Ok(ImportedCharacterPackage {
        brief: manifest.character,
        visual_pack_id: manifest.package_id,
        visual,
    })
}

fn replace_visual_pack_directory(staging: &Path, target: &Path) -> Result<(), FairyError> {
    if !target.exists() {
        fs::rename(staging, target).map_err(|_| storage_io("无法安装角色视觉包"))?;
        return Ok(());
    }

    let backup = target.with_file_name(format!(
        ".{}.backup.{}",
        target
            .file_name()
            .and_then(|name| name.to_str())
            .unwrap_or("visual-pack"),
        unique_suffix(),
    ));
    if backup.exists() {
        fs::remove_dir_all(&backup).map_err(|_| storage_io("无法清理角色视觉包备份目录"))?;
    }
    fs::rename(target, &backup).map_err(|_| storage_io("无法备份旧角色视觉包"))?;
    match fs::rename(staging, target) {
        Ok(()) => {
            let _ = fs::remove_dir_all(&backup);
            Ok(())
        }
        Err(_) => {
            let _ = fs::rename(&backup, target);
            Err(storage_io("无法替换角色视觉包"))
        }
    }
}

fn resolve_export_asset(
    visual_library_directory: &Path,
    pack_id: &VisualPackId,
    image_path: &str,
) -> Result<(PathBuf, PathBuf), FairyError> {
    let relative = local_visual_relative_path(pack_id, image_path)?;
    let package_file = validate_package_file(relative)?.to_path_buf();
    let source_file = visual_library_directory
        .join(pack_id.as_str())
        .join(&package_file);
    let bytes = read_package_file_bytes(
        &source_file,
        MAX_ARCHIVE_IMAGE_BYTES,
        "无法读取本地角色状态图片",
    )?;
    validate_png_bytes(&bytes)?;
    Ok((package_file, source_file))
}

fn local_visual_relative_path<'a>(
    pack_id: &VisualPackId,
    image_path: &'a str,
) -> Result<&'a str, FairyError> {
    let custom_prefix = format!("fairy-character://localhost/{pack_id}/");
    if let Some(relative) = image_path.strip_prefix(&custom_prefix) {
        return Ok(relative);
    }
    let legacy_prefix = format!("http://fairy-character.localhost/{pack_id}/");
    if let Some(relative) = image_path.strip_prefix(&legacy_prefix) {
        return Ok(relative);
    }
    Err(invalid_package("只能导出本地角色视觉资源"))
}

fn write_package_archive(
    output_path: &Path,
    manifest_source: &[u8],
    exported_files: &[(PathBuf, PathBuf)],
) -> Result<(), FairyError> {
    if let Some(parent) = output_path.parent()
        && !parent.as_os_str().is_empty()
    {
        fs::create_dir_all(parent).map_err(|_| storage_io("无法创建角色包导出目录"))?;
    }

    let staging_path = output_path.with_file_name(format!(
        ".{}.exporting.{}",
        output_path
            .file_name()
            .and_then(|name| name.to_str())
            .unwrap_or("character.pack"),
        unique_suffix()
    ));
    if staging_path.exists() {
        fs::remove_file(&staging_path).map_err(|_| storage_io("无法清理角色包导出暂存文件"))?;
    }

    let result = (|| {
        let file = File::create(&staging_path).map_err(|_| storage_io("无法创建角色包导出文件"))?;
        let mut archive = ZipWriter::new(file);
        let options = SimpleFileOptions::default().compression_method(CompressionMethod::Deflated);

        archive
            .start_file(PACKAGE_MANIFEST_FILE, options)
            .map_err(|_| storage_io("无法写入角色包清单"))?;
        archive
            .write_all(manifest_source)
            .map_err(|_| storage_io("无法写入角色包清单"))?;
        for (package_file, source_file) in exported_files {
            let archive_name = package_file.to_string_lossy().replace('\\', "/");
            let bytes = read_package_file_bytes(
                source_file,
                MAX_ARCHIVE_IMAGE_BYTES,
                "无法读取本地角色状态图片",
            )?;
            validate_png_bytes(&bytes)?;
            archive
                .start_file(archive_name, options)
                .map_err(|_| storage_io("无法写入角色状态图片"))?;
            archive
                .write_all(&bytes)
                .map_err(|_| storage_io("无法写入角色状态图片"))?;
        }
        archive
            .finish()
            .map_err(|_| storage_io("无法完成角色包导出"))?;
        Ok(())
    })();
    if let Err(error) = result {
        let _ = fs::remove_file(&staging_path);
        return Err(error);
    }

    if output_path.exists() {
        fs::remove_file(output_path).map_err(|_| storage_io("无法替换已有角色包文件"))?;
    }
    fs::rename(&staging_path, output_path).map_err(|_| {
        let _ = fs::remove_file(&staging_path);
        storage_io("无法保存角色包导出文件")
    })
}

fn unique_import_directory_name(package_id: &VisualPackId) -> String {
    format!(".{}.importing.{}", package_id, unique_suffix())
}

fn unique_suffix() -> u128 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|duration| duration.as_nanos())
        .unwrap_or(0)
}

fn is_supported_package_file(path: &Path) -> bool {
    matches!(
        path.extension()
            .and_then(|extension| extension.to_str())
            .map(str::to_ascii_lowercase)
            .as_deref(),
        Some("pack" | "zip")
    )
}

fn archive_root_prefix<R: Read + Seek>(archive: &mut ZipArchive<R>) -> Result<String, FairyError> {
    if archive.index_for_name(PACKAGE_MANIFEST_FILE).is_some() {
        return Ok(String::new());
    }

    let mut prefixes = Vec::new();
    let suffix = format!("/{PACKAGE_MANIFEST_FILE}");
    for index in 0..archive.len() {
        let file = archive
            .by_index(index)
            .map_err(|_| invalid_package("无法读取角色包文件列表"))?;
        if !file.is_file() {
            continue;
        }
        let name = file.name();
        if let Some(prefix) = name.strip_suffix(&suffix)
            && !prefix.is_empty()
            && !prefix.contains('/')
            && !prefix.contains('\\')
        {
            prefixes.push(prefix.to_owned());
        }
    }
    prefixes.sort();
    prefixes.dedup();
    if prefixes.len() == 1 {
        Ok(format!("{}/", prefixes.remove(0)))
    } else {
        Err(invalid_package("角色包内必须包含唯一 manifest.json"))
    }
}

fn read_archive_entry_string<R: Read + Seek>(
    archive: &mut ZipArchive<R>,
    entry_name: &str,
    limit: usize,
    missing_message: &'static str,
) -> Result<String, FairyError> {
    let bytes = read_archive_entry_bytes(archive, entry_name, limit, missing_message)?;
    String::from_utf8(bytes).map_err(|_| invalid_package("角色包清单必须是 UTF-8 JSON"))
}

fn read_archive_entry_bytes<R: Read + Seek>(
    archive: &mut ZipArchive<R>,
    entry_name: &str,
    limit: usize,
    missing_message: &'static str,
) -> Result<Vec<u8>, FairyError> {
    let entry = archive
        .by_name(entry_name)
        .map_err(|_| invalid_package(missing_message))?;
    if !entry.is_file() {
        return Err(invalid_package(missing_message));
    }
    read_limited(entry, limit)
}

fn read_package_file_bytes(
    path: &Path,
    limit: usize,
    missing_message: &'static str,
) -> Result<Vec<u8>, FairyError> {
    let file = File::open(path).map_err(|_| invalid_package(missing_message))?;
    read_limited(file, limit)
}

fn read_limited(reader: impl Read, limit: usize) -> Result<Vec<u8>, FairyError> {
    let mut bytes = Vec::new();
    let mut limited = reader.take(limit as u64 + 1);
    limited
        .read_to_end(&mut bytes)
        .map_err(|_| invalid_package("无法读取角色包内容"))?;
    if bytes.len() > limit {
        return Err(invalid_package("角色包文件过大"));
    }
    Ok(bytes)
}

fn validate_png_bytes(bytes: &[u8]) -> Result<(), FairyError> {
    if bytes.starts_with(PNG_SIGNATURE) {
        Ok(())
    } else {
        Err(invalid_package("角色状态图片必须是 PNG 文件"))
    }
}

fn validate_package_file(value: &str) -> Result<&Path, FairyError> {
    if value.is_empty()
        || !value.ends_with(".png")
        || value.contains("://")
        || value.contains(['\\', '?', '#'])
    {
        return Err(invalid_package("角色状态图片必须是包内 PNG 相对路径"));
    }
    let path = Path::new(value);
    if path.is_absolute()
        || path
            .components()
            .any(|component| !matches!(component, std::path::Component::Normal(_)))
    {
        return Err(invalid_package("角色状态图片路径不能越出角色包目录"));
    }
    Ok(path)
}

fn invalid_package(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::InvalidVisualManifest, message, false)
}

fn storage_io(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::StorageIo, message, true)
}

#[cfg(test)]
pub fn write_test_character_package(directory: &Path, package_id: &str) {
    fs::create_dir_all(directory.join("images")).expect("create package image directory");
    fs::write(directory.join("images/idle.png"), PNG_SIGNATURE).expect("write idle image");
    fs::write(directory.join("images/happy.png"), PNG_SIGNATURE).expect("write happy image");
    fs::write(
        directory.join(PACKAGE_MANIFEST_FILE),
        format!(
            r#"{{
                "schemaVersion": 1,
                "packageId": "{package_id}",
                "character": {{
                    "name": "亚托莉",
                    "description": "温柔、敏锐、简洁回应的桌面陪伴角色。",
                    "dialogueStyle": "短句，先接住用户当下的话。"
                }},
                "visual": {{
                    "displayName": "亚托莉",
                    "renderer": "state_images",
                    "frame": {{ "width": 16, "height": 16 }},
                    "scale": 4,
                    "anchor": {{ "x": 8, "y": 15 }},
                    "states": [
                        {{
                            "id": "idle",
                            "description": "Quiet standing pose.",
                            "file": "images/idle.png"
                        }},
                        {{
                            "id": "happy",
                            "description": "Happy response pose.",
                            "file": "images/happy.png"
                        }}
                    ]
                }}
            }}"#
        ),
    )
    .expect("write package manifest");
}

#[cfg(test)]
mod tests {
    use std::io::{Read, Write};

    use tempfile::tempdir;
    use zip::write::SimpleFileOptions;
    use zip::{CompressionMethod, ZipWriter};

    use super::*;

    fn write_package_archive(path: &Path, package_directory: &Path, root_prefix: &str) {
        let file = File::create(path).expect("create package archive");
        let mut archive = ZipWriter::new(file);
        let options = SimpleFileOptions::default().compression_method(CompressionMethod::Deflated);
        let root_prefix = root_prefix.trim_matches('/');
        let root_prefix = if root_prefix.is_empty() {
            String::new()
        } else {
            format!("{root_prefix}/")
        };
        for relative in [PACKAGE_MANIFEST_FILE, "images/idle.png", "images/happy.png"] {
            archive
                .start_file(format!("{root_prefix}{relative}"), options)
                .expect("start package archive entry");
            archive
                .write_all(&fs::read(package_directory.join(relative)).expect("read package file"))
                .expect("write package archive entry");
        }
        archive.finish().expect("finish package archive");
    }

    #[test]
    fn imports_directory_package_into_app_config_visual_library() {
        let config = tempdir().expect("config tempdir");
        let package = tempdir().expect("package tempdir");
        write_test_character_package(package.path(), "fairy.local");

        let imported = import_character_package(config.path().join("visual-packs"), package.path())
            .expect("import package");

        assert_eq!(imported.brief.name, "亚托莉");
        assert_eq!(imported.visual_pack_id.as_str(), "fairy.local");
        assert_eq!(imported.visual.pack_id().as_str(), "fairy.local");
        assert!(
            config
                .path()
                .join("visual-packs/fairy.local/images/idle.png")
                .is_file()
        );
        assert!(imported.visual.states().iter().all(|state| {
            state
                .image_path
                .starts_with("fairy-character://localhost/fairy.local/")
        }));
    }

    #[test]
    fn imports_archive_pack_file_into_app_config_visual_library() {
        let config = tempdir().expect("config tempdir");
        let package = tempdir().expect("package tempdir");
        write_test_character_package(package.path(), "fairy.archive");
        let archive_path = package.path().join("atri.pack");
        write_package_archive(&archive_path, package.path(), "atri");

        let imported = import_character_package(config.path().join("visual-packs"), &archive_path)
            .expect("import package archive");

        assert_eq!(imported.brief.name, "亚托莉");
        assert_eq!(imported.visual_pack_id.as_str(), "fairy.archive");
        assert!(
            config
                .path()
                .join("visual-packs/fairy.archive/images/idle.png")
                .is_file()
        );
        assert!(imported.visual.states().iter().all(|state| {
            state
                .image_path
                .starts_with("fairy-character://localhost/fairy.archive/")
        }));
    }

    #[test]
    fn exports_character_package_archive_with_manifest_and_state_images() {
        let config = tempdir().expect("config tempdir");
        let source_package = tempdir().expect("source package tempdir");
        write_test_character_package(source_package.path(), "fairy.export");
        let library = config.path().join("visual-packs");
        let imported = import_directory_package(&library, source_package.path()).expect("import");
        let character = CharacterCompiler
            .compile(
                CharacterId::new(),
                Revision::INITIAL,
                imported.brief.clone(),
            )
            .expect("compile character");

        let export_path = config.path().join("atri.pack");
        export_character_package(&library, &character, &imported.visual, &export_path)
            .expect("export package");

        let file = File::open(&export_path).expect("open exported package");
        let mut archive = ZipArchive::new(file).expect("read exported package");
        let mut manifest_source = String::new();
        archive
            .by_name(PACKAGE_MANIFEST_FILE)
            .expect("manifest entry")
            .read_to_string(&mut manifest_source)
            .expect("read manifest");
        let manifest: CharacterPackageManifest =
            serde_json::from_str(&manifest_source).expect("package manifest");

        assert_eq!(manifest.schema_version, PACKAGE_SCHEMA_VERSION);
        assert_eq!(manifest.package_id.as_str(), "fairy.export");
        assert_eq!(manifest.character.name, "亚托莉");
        assert_eq!(manifest.visual.states[0].file, "images/idle.png");
        assert_eq!(manifest.visual.states[1].file, "images/happy.png");
        assert_eq!(
            read_limited(
                archive.by_name("images/idle.png").expect("idle entry"),
                MAX_ARCHIVE_IMAGE_BYTES,
            )
            .expect("read idle"),
            PNG_SIGNATURE
        );
        assert_eq!(
            read_limited(
                archive.by_name("images/happy.png").expect("happy entry"),
                MAX_ARCHIVE_IMAGE_BYTES,
            )
            .expect("read happy"),
            PNG_SIGNATURE
        );

        let roundtrip = tempdir().expect("roundtrip config tempdir");
        let reimported =
            import_character_package(roundtrip.path().join("visual-packs"), export_path)
                .expect("reimport exported package");
        assert_eq!(reimported.visual_pack_id.as_str(), "fairy.export");
        assert!(
            roundtrip
                .path()
                .join("visual-packs/fairy.export/images/idle.png")
                .is_file()
        );
    }

    #[test]
    fn rejects_export_when_visual_state_asset_is_missing_or_not_png() {
        let config = tempdir().expect("config tempdir");
        let character = CharacterCompiler
            .compile(
                CharacterId::new(),
                Revision::INITIAL,
                CharacterBriefInput {
                    name: "亚托莉".to_owned(),
                    description: "温柔、敏锐、简洁回应的桌面陪伴角色。".to_owned(),
                    dialogue_style: None,
                },
            )
            .expect("compile character");
        let visual = CharacterVisualCompiler
            .compile_json(
                r#"{
                    "schemaVersion": 2,
                    "packId": "fairy.remote",
                    "displayName": "Remote",
                    "renderer": "state_images",
                    "frame": { "width": 16, "height": 16 },
                    "scale": 4,
                    "anchor": { "x": 8, "y": 15 },
                    "states": [
                        {
                            "id": "idle",
                            "description": "Remote idle.",
                            "imagePath": "fairy-character://localhost/fairy.remote/idle.png"
                        }
                    ]
                }"#,
            )
            .expect("compile visual");

        assert_eq!(
            export_character_package(
                config.path().join("visual-packs"),
                &character,
                &visual,
                config.path().join("remote.pack"),
            )
            .expect_err("remote asset export")
            .code,
            ErrorCode::InvalidVisualManifest
        );
        assert!(!config.path().join("remote.pack").exists());
    }

    #[test]
    fn rejects_path_traversal_and_replaces_existing_pack_ids() {
        let config = tempdir().expect("config tempdir");
        let package = tempdir().expect("package tempdir");
        write_test_character_package(package.path(), "fairy.local");
        let library = config.path().join("visual-packs");
        import_directory_package(&library, package.path()).expect("first import");
        fs::write(
            package.path().join("images/idle.png"),
            [PNG_SIGNATURE.as_slice(), b"next"].concat(),
        )
        .expect("replace idle image");
        import_directory_package(&library, package.path()).expect("replace existing import");
        assert_eq!(
            fs::read(library.join("fairy.local/images/idle.png")).expect("read replaced image"),
            [PNG_SIGNATURE.as_slice(), b"next"].concat()
        );

        let bad = tempdir().expect("bad package tempdir");
        write_test_character_package(bad.path(), "fairy.bad");
        let manifest = fs::read_to_string(bad.path().join(PACKAGE_MANIFEST_FILE))
            .expect("read bad manifest")
            .replace("images/idle.png", "../idle.png");
        fs::write(bad.path().join(PACKAGE_MANIFEST_FILE), manifest).expect("write bad manifest");
        assert_eq!(
            import_directory_package(&library, bad.path())
                .expect_err("path traversal")
                .code,
            ErrorCode::InvalidVisualManifest
        );
    }

    #[test]
    fn rejects_state_images_that_are_not_png_files() {
        let config = tempdir().expect("config tempdir");
        let package = tempdir().expect("package tempdir");
        write_test_character_package(package.path(), "fairy.bad-image");
        fs::write(package.path().join("images/idle.png"), b"not png")
            .expect("replace idle image with invalid bytes");

        assert_eq!(
            import_character_package(config.path().join("visual-packs"), package.path())
                .expect_err("non-png state image")
                .code,
            ErrorCode::InvalidVisualManifest
        );
        assert!(!config.path().join("visual-packs/fairy.bad-image").exists());
    }

    #[test]
    fn rejects_invalid_character_brief_before_writing_visual_pack() {
        let config = tempdir().expect("config tempdir");
        let package = tempdir().expect("package tempdir");
        write_test_character_package(package.path(), "fairy.bad-character");
        let manifest = fs::read_to_string(package.path().join(PACKAGE_MANIFEST_FILE))
            .expect("read manifest")
            .replace("\"name\": \"亚托莉\"", "\"name\": \"\"");
        fs::write(package.path().join(PACKAGE_MANIFEST_FILE), manifest).expect("write manifest");

        assert_eq!(
            import_character_package(config.path().join("visual-packs"), package.path())
                .expect_err("invalid character brief")
                .code,
            ErrorCode::InvalidCharacterBrief
        );
        assert!(
            !config
                .path()
                .join("visual-packs/fairy.bad-character")
                .exists()
        );
    }
}
