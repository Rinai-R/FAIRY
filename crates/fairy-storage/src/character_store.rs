use std::fs;
use std::path::{Path, PathBuf};
use std::str::FromStr;

use fairy_domain::{
    CharacterBriefInput, CharacterCompiler, CharacterId, CharacterSnapshot, ErrorCode, FairyError,
    Revision,
};
use serde::{Deserialize, Serialize};

use crate::{DocumentRead, StorageRoot};

const CHARACTER_DOCUMENT_SCHEMA: u32 = 1;
const ACTIVE_CHARACTER_SCHEMA: u32 = 1;
const ACTIVE_CHARACTER_PATH: &str = "active-character.json";

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct ActiveCharacter {
    pub character_id: CharacterId,
    pub revision: Revision,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct CharacterDiagnostic {
    pub character_id: Option<CharacterId>,
    pub revision: Option<u64>,
    pub code: ErrorCode,
    pub message: String,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct CharacterCatalog {
    pub characters: Vec<CharacterSnapshot>,
    pub diagnostics: Vec<CharacterDiagnostic>,
}

#[derive(Clone, Debug)]
pub struct CharacterStore {
    root: StorageRoot,
    compiler: CharacterCompiler,
}

impl CharacterStore {
    #[must_use]
    pub fn new(root: StorageRoot) -> Self {
        Self {
            root,
            compiler: CharacterCompiler,
        }
    }

    pub fn create(&self, brief: CharacterBriefInput) -> Result<CharacterSnapshot, FairyError> {
        self.create_with_id(CharacterId::new(), brief)
    }

    pub fn create_with_id(
        &self,
        character_id: CharacterId,
        brief: CharacterBriefInput,
    ) -> Result<CharacterSnapshot, FairyError> {
        let snapshot = self
            .compiler
            .compile(character_id, Revision::INITIAL, brief)?;
        self.write_snapshot(&snapshot)?;
        Ok(snapshot)
    }

    pub fn update(
        &self,
        character_id: CharacterId,
        brief: CharacterBriefInput,
    ) -> Result<CharacterSnapshot, FairyError> {
        let latest = self.latest_revision_number(character_id)?;
        let next = latest
            .checked_add(1)
            .and_then(Revision::new)
            .ok_or_else(|| storage_io("角色 revision 已耗尽"))?;
        let snapshot = self.compiler.compile(character_id, next, brief)?;
        self.write_snapshot(&snapshot)?;
        Ok(snapshot)
    }

    pub fn get(
        &self,
        character_id: CharacterId,
        revision: Revision,
    ) -> Result<CharacterSnapshot, FairyError> {
        let relative = snapshot_relative_path(character_id, revision.get());
        match self
            .root
            .read::<CharacterSnapshot>(relative, CHARACTER_DOCUMENT_SCHEMA)
        {
            Ok(DocumentRead::Found(snapshot)) => {
                snapshot
                    .verify_integrity()
                    .map_err(|_| character_not_available())?;
                if snapshot.character_id() != character_id || snapshot.revision() != revision {
                    return Err(character_not_available());
                }
                Ok(snapshot)
            }
            Ok(DocumentRead::Missing) | Err(_) => Err(character_not_available()),
        }
    }

    pub fn list(&self) -> Result<CharacterCatalog, FairyError> {
        let characters_directory = self.root.directory().join("characters");
        let mut character_directories = read_directory_paths(&characters_directory)?;
        character_directories.sort();

        let mut characters = Vec::new();
        let mut diagnostics = Vec::new();
        for directory in character_directories {
            if !directory.is_dir() {
                continue;
            }
            let character_id = match directory
                .file_name()
                .and_then(|name| name.to_str())
                .and_then(|name| CharacterId::from_str(name).ok())
            {
                Some(character_id) => character_id,
                None => {
                    diagnostics.push(diagnostic(None, None, "角色目录名称无效"));
                    continue;
                }
            };
            self.collect_latest_valid(character_id, &mut characters, &mut diagnostics)?;
        }
        characters.sort_by_key(|snapshot| snapshot.character_id());

        Ok(CharacterCatalog {
            characters,
            diagnostics,
        })
    }

    pub fn activate(
        &self,
        character_id: CharacterId,
        revision: Revision,
    ) -> Result<CharacterSnapshot, FairyError> {
        let snapshot = self.get(character_id, revision)?;
        self.root.write_replace(
            ACTIVE_CHARACTER_PATH,
            ACTIVE_CHARACTER_SCHEMA,
            &ActiveCharacter {
                character_id,
                revision,
            },
        )?;
        Ok(snapshot)
    }

    pub fn active(&self) -> Result<Option<CharacterSnapshot>, FairyError> {
        match self
            .root
            .read::<ActiveCharacter>(ACTIVE_CHARACTER_PATH, ACTIVE_CHARACTER_SCHEMA)?
        {
            DocumentRead::Missing => Ok(None),
            DocumentRead::Found(active) => self.get(active.character_id, active.revision).map(Some),
        }
    }

    pub fn clear_active(&self) -> Result<bool, FairyError> {
        self.root.remove(ACTIVE_CHARACTER_PATH)
    }

    fn write_snapshot(&self, snapshot: &CharacterSnapshot) -> Result<(), FairyError> {
        snapshot.verify_integrity()?;
        self.root.write_new(
            snapshot_relative_path(snapshot.character_id(), snapshot.revision().get()),
            CHARACTER_DOCUMENT_SCHEMA,
            snapshot,
        )
    }

    fn latest_revision_number(&self, character_id: CharacterId) -> Result<u64, FairyError> {
        let revisions = self.revision_files(character_id)?;
        revisions
            .iter()
            .filter_map(|path| revision_number(path))
            .max()
            .ok_or_else(character_not_available)
    }

    fn revision_files(&self, character_id: CharacterId) -> Result<Vec<PathBuf>, FairyError> {
        let directory = self
            .root
            .directory()
            .join("characters")
            .join(character_id.to_string())
            .join("revisions");
        let mut paths = read_directory_paths(&directory)?;
        paths.sort();
        Ok(paths)
    }

    fn collect_latest_valid(
        &self,
        character_id: CharacterId,
        characters: &mut Vec<CharacterSnapshot>,
        diagnostics: &mut Vec<CharacterDiagnostic>,
    ) -> Result<(), FairyError> {
        let mut revisions = self.revision_files(character_id)?;
        revisions.sort_by_key(|path| std::cmp::Reverse(revision_number(path).unwrap_or(0)));

        let mut found_valid = false;
        for path in revisions {
            let Some(revision_number) = revision_number(&path) else {
                diagnostics.push(diagnostic(
                    Some(character_id),
                    None,
                    "角色 revision 文件名无效",
                ));
                continue;
            };
            let Some(revision) = Revision::new(revision_number) else {
                diagnostics.push(diagnostic(
                    Some(character_id),
                    Some(revision_number),
                    "角色 revision 必须大于零",
                ));
                continue;
            };
            let relative = snapshot_relative_path(character_id, revision_number);
            match self
                .root
                .read::<CharacterSnapshot>(relative, CHARACTER_DOCUMENT_SCHEMA)
            {
                Ok(DocumentRead::Found(snapshot))
                    if snapshot.verify_integrity().is_ok()
                        && snapshot.character_id() == character_id
                        && snapshot.revision() == revision =>
                {
                    if !found_valid {
                        characters.push(snapshot);
                        found_valid = true;
                    }
                }
                Ok(DocumentRead::Found(_) | DocumentRead::Missing)
                | Err(FairyError {
                    code: ErrorCode::StorageCorrupted,
                    ..
                }) => diagnostics.push(diagnostic(
                    Some(character_id),
                    Some(revision_number),
                    "角色 revision 已损坏，已从列表结果中隔离",
                )),
                Err(error) => return Err(error),
            }
        }
        Ok(())
    }
}

fn snapshot_relative_path(character_id: CharacterId, revision: u64) -> PathBuf {
    PathBuf::from("characters")
        .join(character_id.to_string())
        .join("revisions")
        .join(format!("{revision}.json"))
}

fn revision_number(path: &Path) -> Option<u64> {
    if path.extension().and_then(|value| value.to_str()) != Some("json") {
        return None;
    }
    path.file_stem()?.to_str()?.parse().ok()
}

fn read_directory_paths(directory: &Path) -> Result<Vec<PathBuf>, FairyError> {
    match fs::read_dir(directory) {
        Ok(entries) => entries
            .map(|entry| {
                entry
                    .map(|value| value.path())
                    .map_err(|_| storage_io("无法读取角色配置目录项"))
            })
            .collect(),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(Vec::new()),
        Err(_) => Err(storage_io("无法读取角色配置目录")),
    }
}

fn diagnostic(
    character_id: Option<CharacterId>,
    revision: Option<u64>,
    message: &'static str,
) -> CharacterDiagnostic {
    CharacterDiagnostic {
        character_id,
        revision,
        code: ErrorCode::StorageCorrupted,
        message: message.to_owned(),
    }
}

fn character_not_available() -> FairyError {
    FairyError::new(
        ErrorCode::CharacterNotAvailable,
        "指定角色 revision 不存在或不可用",
        false,
    )
}

fn storage_io(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::StorageIo, message, true)
}

#[cfg(test)]
mod tests {
    use std::fs;

    use tempfile::tempdir;

    use super::*;

    fn brief(name: &str, description: &str) -> CharacterBriefInput {
        CharacterBriefInput {
            name: name.to_owned(),
            description: description.to_owned(),
            dialogue_style: None,
        }
    }

    #[test]
    fn create_update_and_restart_preserve_immutable_revisions() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        let store = CharacterStore::new(root.clone());

        let first = store
            .create(brief("亚托莉", "有一点骄傲，会认真听用户说话。"))
            .expect("create character");
        let second = store
            .update(
                first.character_id(),
                brief("亚托莉", "有一点骄傲，会先听完再轻轻回应。"),
            )
            .expect("update character");

        assert_eq!(first.revision(), Revision::INITIAL);
        assert_eq!(second.revision().get(), 2);
        assert_eq!(
            store
                .get(first.character_id(), Revision::INITIAL)
                .expect("old revision remains"),
            first
        );

        let restarted = CharacterStore::new(root);
        assert_eq!(
            restarted
                .get(second.character_id(), second.revision())
                .expect("new revision survives restart"),
            second
        );
    }

    #[test]
    fn list_isolates_corrupt_revision_and_keeps_other_characters() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        let store = CharacterStore::new(root.clone());
        let first = store
            .create(brief("角色一", "第一位有效角色。"))
            .expect("create first character");
        let second = store
            .create(brief("角色二", "第二位有效角色。"))
            .expect("create second character");
        let corrupt_path = root
            .directory()
            .join(snapshot_relative_path(first.character_id(), 2));
        fs::create_dir_all(corrupt_path.parent().expect("corrupt fixture parent"))
            .expect("create revision directory");
        fs::write(corrupt_path, b"{broken").expect("write corrupt revision");

        let catalog = store.list().expect("list with isolated corruption");

        assert_eq!(catalog.characters.len(), 2);
        assert!(
            catalog
                .characters
                .iter()
                .any(|snapshot| snapshot.character_id() == first.character_id())
        );
        assert!(
            catalog
                .characters
                .iter()
                .any(|snapshot| snapshot.character_id() == second.character_id())
        );
        assert_eq!(catalog.diagnostics.len(), 1);
        assert_eq!(catalog.diagnostics[0].revision, Some(2));
    }

    #[test]
    fn activation_validates_target_before_replacing_pointer() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        let store = CharacterStore::new(root);
        let valid = store
            .create(brief("有效角色", "可以被安全激活。"))
            .expect("create valid character");
        store
            .activate(valid.character_id(), valid.revision())
            .expect("activate valid character");

        let missing_error = store
            .activate(CharacterId::new(), Revision::INITIAL)
            .expect_err("missing target must fail");
        assert_eq!(missing_error.code, ErrorCode::CharacterNotAvailable);
        assert_eq!(store.active().expect("read active character"), Some(valid));

        assert!(store.clear_active().expect("clear active pointer"));
        assert_eq!(store.active().expect("read cleared pointer"), None);
        assert!(!store.clear_active().expect("clear absent pointer"));
    }

    #[test]
    fn empty_store_has_no_hidden_default_character() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        let store = CharacterStore::new(root);

        assert_eq!(store.active().expect("read empty active pointer"), None);
        assert!(
            store
                .list()
                .expect("list empty store")
                .characters
                .is_empty()
        );
    }
}
