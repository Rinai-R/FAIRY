use std::path::PathBuf;

use fairy_domain::{CharacterId, ErrorCode, FairyError, Revision, VisualPackId};
use serde::{Deserialize, Serialize};

use crate::{DocumentRead, StorageRoot};

const CHARACTER_APPEARANCE_SCHEMA: u32 = 1;

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct CharacterAppearanceBinding {
    pub character_id: CharacterId,
    pub revision: Revision,
    pub visual_pack_id: VisualPackId,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub enum CharacterAppearanceRead {
    Unassigned,
    Assigned(CharacterAppearanceBinding),
}

#[derive(Clone, Debug)]
pub struct CharacterAppearanceStore {
    root: StorageRoot,
}

impl CharacterAppearanceStore {
    #[must_use]
    pub fn new(root: StorageRoot) -> Self {
        Self { root }
    }

    pub fn read(&self, character_id: CharacterId) -> Result<CharacterAppearanceRead, FairyError> {
        let path = appearance_relative_path(character_id);
        match self
            .root
            .read::<CharacterAppearanceBinding>(path, CHARACTER_APPEARANCE_SCHEMA)
        {
            Ok(DocumentRead::Missing) => Ok(CharacterAppearanceRead::Unassigned),
            Ok(DocumentRead::Found(binding)) => {
                if binding.character_id != character_id {
                    return Err(appearance_unavailable());
                }
                Ok(CharacterAppearanceRead::Assigned(binding))
            }
            Err(error) if error.code == ErrorCode::StorageCorrupted => {
                Err(appearance_unavailable())
            }
            Err(error) => Err(error),
        }
    }

    pub fn assign(
        &self,
        character_id: CharacterId,
        visual_pack_id: VisualPackId,
    ) -> Result<CharacterAppearanceBinding, FairyError> {
        let revision = match self.read(character_id)? {
            CharacterAppearanceRead::Unassigned => Revision::INITIAL,
            CharacterAppearanceRead::Assigned(current) => current
                .revision
                .get()
                .checked_add(1)
                .and_then(Revision::new)
                .ok_or_else(appearance_unavailable)?,
        };
        let binding = CharacterAppearanceBinding {
            character_id,
            revision,
            visual_pack_id,
        };
        self.root.write_replace(
            appearance_relative_path(character_id),
            CHARACTER_APPEARANCE_SCHEMA,
            &binding,
        )?;
        Ok(binding)
    }

    pub fn clear(&self, character_id: CharacterId) -> Result<bool, FairyError> {
        self.root.remove(appearance_relative_path(character_id))
    }
}

fn appearance_relative_path(character_id: CharacterId) -> PathBuf {
    PathBuf::from("character-appearances").join(format!("{character_id}.json"))
}

fn appearance_unavailable() -> FairyError {
    FairyError::new(
        ErrorCode::CharacterAppearanceUnavailable,
        "角色外观绑定已损坏或不可用",
        false,
    )
}

#[cfg(test)]
mod tests {
    use std::fs;
    use std::str::FromStr;

    use tempfile::tempdir;

    use super::*;

    fn pack_id(value: &str) -> VisualPackId {
        VisualPackId::from_str(value).expect("valid test pack id")
    }

    #[test]
    fn missing_binding_is_explicitly_unassigned() {
        let temp = tempdir().expect("temp directory");
        let store =
            CharacterAppearanceStore::new(StorageRoot::new(temp.path()).expect("storage root"));

        assert_eq!(
            store.read(CharacterId::new()).expect("read missing"),
            CharacterAppearanceRead::Unassigned
        );
    }

    #[test]
    fn assignment_is_revisioned_and_survives_restart() {
        let temp = tempdir().expect("temp directory");
        let root = StorageRoot::new(temp.path()).expect("storage root");
        let character_id = CharacterId::new();
        let store = CharacterAppearanceStore::new(root.clone());

        let first = store
            .assign(character_id, pack_id("fairy.first"))
            .expect("first assignment");
        let second = store
            .assign(character_id, pack_id("fairy.second"))
            .expect("second assignment");

        assert_eq!(first.revision, Revision::INITIAL);
        assert_eq!(second.revision.get(), 2);
        assert_eq!(second.visual_pack_id.as_str(), "fairy.second");
        assert_eq!(
            CharacterAppearanceStore::new(root)
                .read(character_id)
                .expect("read after restart"),
            CharacterAppearanceRead::Assigned(second)
        );
    }

    #[test]
    fn corrupt_binding_is_not_treated_as_missing_or_defaulted() {
        let temp = tempdir().expect("temp directory");
        let root = StorageRoot::new(temp.path()).expect("storage root");
        let character_id = CharacterId::new();
        let directory = root.directory().join("character-appearances");
        fs::create_dir_all(&directory).expect("appearance directory");
        fs::write(directory.join(format!("{character_id}.json")), b"{broken")
            .expect("corrupt fixture");

        let error = CharacterAppearanceStore::new(root)
            .read(character_id)
            .expect_err("corruption must be visible");
        assert_eq!(error.code, ErrorCode::CharacterAppearanceUnavailable);
    }

    #[test]
    fn mismatched_character_id_is_rejected() {
        let temp = tempdir().expect("temp directory");
        let root = StorageRoot::new(temp.path()).expect("storage root");
        let requested = CharacterId::new();
        let other = CharacterId::new();
        root.write_replace(
            appearance_relative_path(requested),
            CHARACTER_APPEARANCE_SCHEMA,
            &CharacterAppearanceBinding {
                character_id: other,
                revision: Revision::INITIAL,
                visual_pack_id: pack_id("fairy.first"),
            },
        )
        .expect("write mismatched fixture");

        let error = CharacterAppearanceStore::new(root)
            .read(requested)
            .expect_err("mismatch must fail");
        assert_eq!(error.code, ErrorCode::CharacterAppearanceUnavailable);
    }

    #[test]
    fn clear_removes_only_the_requested_binding() {
        let temp = tempdir().expect("temp directory");
        let root = StorageRoot::new(temp.path()).expect("storage root");
        let store = CharacterAppearanceStore::new(root);
        let first = CharacterId::new();
        let second = CharacterId::new();
        store
            .assign(first, pack_id("fairy.first"))
            .expect("assign first");
        store
            .assign(second, pack_id("fairy.second"))
            .expect("assign second");

        assert!(store.clear(first).expect("clear first"));
        assert_eq!(
            store.read(first).expect("read cleared"),
            CharacterAppearanceRead::Unassigned
        );
        assert!(matches!(
            store.read(second).expect("read untouched"),
            CharacterAppearanceRead::Assigned(_)
        ));
    }
}
