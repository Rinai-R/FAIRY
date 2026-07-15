use std::collections::BTreeMap;

use fairy_domain::{
    CharacterVisualCompiler, ErrorCode, FairyError, VerifiedVisualPack, VisualPackId,
};

const ATRI_MANIFEST: &str = include_str!("../../web/public/characters/atri/manifest.json");

#[derive(Clone, Debug)]
pub struct VisualPackRegistry {
    packs: BTreeMap<VisualPackId, VerifiedVisualPack>,
}

impl VisualPackRegistry {
    pub fn bundled() -> Result<Self, FairyError> {
        Self::compile(&[ATRI_MANIFEST])
    }

    pub fn compile(sources: &[&str]) -> Result<Self, FairyError> {
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
        Ok(Self { packs })
    }

    pub fn get(&self, pack_id: &VisualPackId) -> Result<&VerifiedVisualPack, FairyError> {
        self.packs.get(pack_id).ok_or_else(|| {
            FairyError::new(
                ErrorCode::VisualPackNotFound,
                "找不到指定的角色视觉包",
                false,
            )
        })
    }

    #[must_use]
    pub fn list(&self) -> Vec<&VerifiedVisualPack> {
        self.packs.values().collect()
    }
}

#[cfg(test)]
mod tests {
    use std::str::FromStr;

    use super::*;

    fn manifest(pack_id: &str) -> String {
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
                        "imagePath": "/characters/test/idle.png"
                    }},
                    {{
                        "id": "happy",
                        "description": "Happy response pose.",
                        "imagePath": "/characters/test/happy.png"
                    }}
                ]
            }}"#
        )
    }

    #[test]
    fn compiles_sorted_registry_and_resolves_exact_id() {
        let second = manifest("fairy.second");
        let first = manifest("fairy.first");
        let registry = VisualPackRegistry::compile(&[&second, &first]).expect("compile registry");

        assert_eq!(
            registry
                .list()
                .iter()
                .map(|pack| pack.pack_id().as_str())
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
        let source = manifest("fairy.same");
        assert_eq!(
            VisualPackRegistry::compile(&[&source, &source])
                .expect_err("duplicate must fail")
                .code,
            ErrorCode::InvalidVisualManifest
        );

        let registry = VisualPackRegistry::compile(&[&source]).expect("registry");
        assert_eq!(
            registry
                .get(&VisualPackId::from_str("fairy.missing").expect("pack id"))
                .expect_err("unknown pack")
                .code,
            ErrorCode::VisualPackNotFound
        );
    }

    #[test]
    fn bundled_manifests_compile_offline_with_expected_ids() {
        let registry = VisualPackRegistry::bundled().expect("bundled registry");

        assert_eq!(
            registry
                .list()
                .iter()
                .map(|pack| pack.pack_id().as_str())
                .collect::<Vec<_>>(),
            ["fairy.atri"]
        );
        assert!(
            registry
                .list()
                .iter()
                .flat_map(|pack| pack.states())
                .all(|state| state.image_path.starts_with("/characters/"))
        );
    }
}
