use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};

use fairy_domain::{
    CharacterVisualCompiler, ErrorCode, FairyError, VerifiedVisualPack, VisualPackId,
};

const VISUAL_PACKS_DIRECTORY: &str = "visual-packs";
const VISUAL_MANIFEST_FILE: &str = "manifest.json";

#[derive(Clone, Debug)]
pub struct VisualPackRegistry {
    root: PathBuf,
}

impl VisualPackRegistry {
    pub fn local(config_directory: impl AsRef<Path>) -> Result<Self, FairyError> {
        let root = config_directory.as_ref().join(VISUAL_PACKS_DIRECTORY);
        fs::create_dir_all(&root).map_err(|_| registry_io("无法创建本地角色视觉包目录"))?;
        Ok(Self { root })
    }

    pub fn compile(
        sources: &[&str],
    ) -> Result<BTreeMap<VisualPackId, VerifiedVisualPack>, FairyError> {
        let compiler = CharacterVisualCompiler;
        let mut packs = BTreeMap::new();
        for source in sources {
            let pack = compiler.compile_json(source)?;
            let pack_id = pack.pack_id().clone();
            if packs.insert(pack_id, pack).is_some() {
                return Err(FairyError::new(
                    ErrorCode::InvalidVisualManifest,
                    "角色视觉注册表包含重复视觉包 ID",
                    false,
                ));
            }
        }
        Ok(packs)
    }

    pub fn get(&self, pack_id: &VisualPackId) -> Result<VerifiedVisualPack, FairyError> {
        let manifest = self.root.join(pack_id.as_str()).join(VISUAL_MANIFEST_FILE);
        let source = fs::read_to_string(manifest).map_err(|_| visual_pack_not_found())?;
        CharacterVisualCompiler.compile_json(&source)
    }

    pub fn list(&self) -> Result<Vec<VerifiedVisualPack>, FairyError> {
        Ok(self.load_all()?.into_values().collect())
    }

    pub fn remove(&self, pack_id: &VisualPackId) -> Result<bool, FairyError> {
        let target = self.root.join(pack_id.as_str());
        if !target.exists() {
            return Ok(false);
        }
        fs::remove_dir_all(target).map_err(|_| registry_io("无法删除本地角色视觉包"))?;
        Ok(true)
    }

    fn load_all(&self) -> Result<BTreeMap<VisualPackId, VerifiedVisualPack>, FairyError> {
        let mut sources = Vec::new();
        let entries =
            fs::read_dir(&self.root).map_err(|_| registry_io("无法读取本地角色视觉包目录"))?;
        for entry in entries {
            let entry = entry.map_err(|_| registry_io("无法读取本地角色视觉包条目"))?;
            let manifest = entry.path().join(VISUAL_MANIFEST_FILE);
            if manifest.is_file() {
                sources.push(
                    fs::read_to_string(manifest)
                        .map_err(|_| registry_io("无法读取角色视觉清单"))?,
                );
            }
        }
        let borrowed = sources.iter().map(String::as_str).collect::<Vec<_>>();
        Self::compile(&borrowed)
    }

    pub fn root(&self) -> &Path {
        &self.root
    }
}

fn visual_pack_not_found() -> FairyError {
    FairyError::new(
        ErrorCode::VisualPackNotFound,
        "找不到指定的角色视觉包",
        false,
    )
}

fn registry_io(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::StorageIo, message, true)
}

#[cfg(test)]
pub fn test_manifest(pack_id: &str) -> String {
    format!(
        r#"{{
            "schemaVersion": 2,
            "packId": "{pack_id}",
            "displayName": "Test",
            "renderer": "state_images",
            "frame": {{ "width": 16, "height": 16 }},
            "scale": 4,
            "anchor": {{ "x": 8, "y": 15 }},
            "states": [
                {{
                    "id": "idle",
                    "description": "Quiet standing pose.",
                    "imagePath": "fairy-character://localhost/{pack_id}/idle.png"
                }},
                {{
                    "id": "happy",
                    "description": "Happy response pose.",
                    "imagePath": "fairy-character://localhost/{pack_id}/happy.png"
                }}
            ]
        }}"#
    )
}

#[cfg(test)]
pub fn write_test_visual_pack(config_directory: &Path, pack_id: &str) {
    let root = config_directory.join(VISUAL_PACKS_DIRECTORY).join(pack_id);
    fs::create_dir_all(&root).expect("create test visual pack directory");
    fs::write(root.join(VISUAL_MANIFEST_FILE), test_manifest(pack_id))
        .expect("write test manifest");
    fs::write(root.join("idle.png"), b"not a real png for registry tests").expect("write idle png");
    fs::write(root.join("happy.png"), b"not a real png for registry tests")
        .expect("write happy png");
}

#[cfg(test)]
mod tests {
    use std::str::FromStr;

    use tempfile::tempdir;

    use super::*;

    #[test]
    fn compiles_sorted_registry_and_resolves_exact_id() {
        let second = test_manifest("fairy.second");
        let first = test_manifest("fairy.first");
        let registry = VisualPackRegistry::compile(&[&second, &first]).expect("compile registry");

        assert_eq!(
            registry
                .keys()
                .map(VisualPackId::as_str)
                .collect::<Vec<_>>(),
            ["fairy.first", "fairy.second"]
        );
        assert_eq!(
            registry
                .get(&VisualPackId::from_str("fairy.second").expect("pack id"))
                .expect("registered pack")
                .pack_id()
                .as_str(),
            "fairy.second"
        );
    }

    #[test]
    fn duplicate_and_unknown_pack_ids_are_explicit_failures() {
        let source = test_manifest("fairy.same");
        assert_eq!(
            VisualPackRegistry::compile(&[&source, &source])
                .expect_err("duplicate must fail")
                .code,
            ErrorCode::InvalidVisualManifest
        );

        let directory = tempdir().expect("temp directory");
        let registry = VisualPackRegistry::local(directory.path()).expect("registry");
        assert_eq!(
            registry
                .get(&VisualPackId::from_str("fairy.missing").expect("pack id"))
                .expect_err("unknown pack")
                .code,
            ErrorCode::VisualPackNotFound
        );
    }

    #[test]
    fn local_registry_scans_app_config_visual_packs() {
        let directory = tempdir().expect("temp directory");
        write_test_visual_pack(directory.path(), "fairy.local");
        let registry = VisualPackRegistry::local(directory.path()).expect("registry");

        assert_eq!(
            registry.root(),
            directory.path().join(VISUAL_PACKS_DIRECTORY)
        );
        assert_eq!(
            registry
                .list()
                .expect("list local visual packs")
                .iter()
                .map(|pack| pack.pack_id().as_str())
                .collect::<Vec<_>>(),
            ["fairy.local"]
        );
        assert!(
            registry
                .get(&VisualPackId::from_str("fairy.local").expect("pack id"))
                .expect("registered pack")
                .states()
                .iter()
                .all(|state| state
                    .image_path
                    .starts_with("fairy-character://localhost/fairy.local/"))
        );
    }

    #[test]
    fn local_registry_reports_corrupt_visual_pack_manifest() {
        let directory = tempdir().expect("temp directory");
        let root = directory
            .path()
            .join(VISUAL_PACKS_DIRECTORY)
            .join("fairy.bad");
        fs::create_dir_all(&root).expect("create corrupt pack directory");
        fs::write(root.join(VISUAL_MANIFEST_FILE), "{\"schemaVersion\":2}")
            .expect("write corrupt manifest");
        let registry = VisualPackRegistry::local(directory.path()).expect("registry");

        assert_eq!(
            registry
                .list()
                .expect_err("corrupt manifest must not be hidden")
                .code,
            ErrorCode::InvalidVisualManifest
        );
    }

    #[test]
    fn remove_deletes_only_the_requested_local_visual_pack() {
        let directory = tempdir().expect("temp directory");
        write_test_visual_pack(directory.path(), "fairy.first");
        write_test_visual_pack(directory.path(), "fairy.second");
        let registry = VisualPackRegistry::local(directory.path()).expect("registry");

        assert!(
            registry
                .remove(&VisualPackId::from_str("fairy.first").expect("pack id"))
                .expect("remove pack")
        );
        assert!(
            !directory
                .path()
                .join(VISUAL_PACKS_DIRECTORY)
                .join("fairy.first")
                .exists()
        );
        assert!(
            directory
                .path()
                .join(VISUAL_PACKS_DIRECTORY)
                .join("fairy.second")
                .exists()
        );
        assert!(
            !registry
                .remove(&VisualPackId::from_str("fairy.first").expect("pack id"))
                .expect("remove missing pack")
        );
    }
}
