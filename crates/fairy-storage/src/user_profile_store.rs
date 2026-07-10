use std::fs;
use std::path::{Path, PathBuf};

use fairy_domain::{
    ErrorCode, FairyError, Revision, UserProfileCompiler, UserProfileInput, UserProfileSnapshot,
};
use serde::{Deserialize, Serialize};

use crate::{DocumentRead, StorageRoot};

const USER_PROFILE_DOCUMENT_SCHEMA: u32 = 1;
const USER_PROFILE_POINTER_SCHEMA: u32 = 1;
const USER_PROFILE_POINTER_PATH: &str = "user-profile/current.json";

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
struct UserProfilePointer {
    revision: Revision,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct UserProfileUpdate {
    pub snapshot: Option<UserProfileSnapshot>,
    pub changed: bool,
    pub recovered_corruption: bool,
}

#[derive(Clone, Debug)]
pub struct UserProfileStore {
    root: StorageRoot,
    compiler: UserProfileCompiler,
}

impl UserProfileStore {
    #[must_use]
    pub fn new(root: StorageRoot) -> Self {
        Self {
            root,
            compiler: UserProfileCompiler,
        }
    }

    pub fn current(&self) -> Result<Option<UserProfileSnapshot>, FairyError> {
        let pointer = match self
            .root
            .read::<UserProfilePointer>(USER_PROFILE_POINTER_PATH, USER_PROFILE_POINTER_SCHEMA)
        {
            Ok(DocumentRead::Missing) => return Ok(None),
            Ok(DocumentRead::Found(pointer)) => pointer,
            Err(_) => return Err(user_profile_unavailable()),
        };
        match self.root.read::<UserProfileSnapshot>(
            revision_relative_path(pointer.revision.get()),
            USER_PROFILE_DOCUMENT_SCHEMA,
        ) {
            Ok(DocumentRead::Found(snapshot))
                if snapshot.verify_integrity().is_ok()
                    && snapshot.revision() == pointer.revision =>
            {
                Ok(Some(snapshot))
            }
            Ok(DocumentRead::Found(_) | DocumentRead::Missing) | Err(_) => {
                Err(user_profile_unavailable())
            }
        }
    }

    pub fn update(&self, input: UserProfileInput) -> Result<UserProfileUpdate, FairyError> {
        let current_result = self.current();
        let recovered_corruption = current_result.is_err();
        let current = current_result.unwrap_or(None);

        let comparison_revision = current
            .as_ref()
            .map(UserProfileSnapshot::revision)
            .unwrap_or(Revision::INITIAL);
        let candidate = self.compiler.compile(comparison_revision, input.clone())?;
        if let Some(current) = current {
            if current.preferred_name() == candidate.preferred_name() {
                return Ok(UserProfileUpdate {
                    snapshot: Some(current),
                    changed: false,
                    recovered_corruption: false,
                });
            }
        } else if candidate.preferred_name().is_none() && !recovered_corruption {
            return Ok(UserProfileUpdate {
                snapshot: None,
                changed: false,
                recovered_corruption: false,
            });
        }

        let revision = self.next_revision()?;
        let snapshot = self.compiler.compile(revision, input)?;
        self.root.write_new(
            revision_relative_path(revision.get()),
            USER_PROFILE_DOCUMENT_SCHEMA,
            &snapshot,
        )?;
        self.root.write_replace(
            USER_PROFILE_POINTER_PATH,
            USER_PROFILE_POINTER_SCHEMA,
            &UserProfilePointer { revision },
        )?;
        Ok(UserProfileUpdate {
            snapshot: Some(snapshot),
            changed: true,
            recovered_corruption,
        })
    }

    pub fn clear(&self) -> Result<UserProfileUpdate, FairyError> {
        self.update(UserProfileInput {
            preferred_name: None,
        })
    }

    fn next_revision(&self) -> Result<Revision, FairyError> {
        let directory = self.root.directory().join("user-profile/revisions");
        let mut maximum = 0_u64;
        match fs::read_dir(directory) {
            Ok(entries) => {
                for entry in entries {
                    let path = entry
                        .map_err(|_| storage_io("无法读取用户资料 revision"))?
                        .path();
                    if let Some(revision) = revision_number(&path) {
                        maximum = maximum.max(revision);
                    }
                }
            }
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
            Err(_) => return Err(storage_io("无法读取用户资料目录")),
        }
        maximum
            .checked_add(1)
            .and_then(Revision::new)
            .ok_or_else(|| storage_io("用户资料 revision 已耗尽"))
    }
}

fn revision_relative_path(revision: u64) -> PathBuf {
    PathBuf::from("user-profile/revisions").join(format!("{revision}.json"))
}

fn revision_number(path: &Path) -> Option<u64> {
    if path.extension().and_then(|value| value.to_str()) != Some("json") {
        return None;
    }
    path.file_stem()?.to_str()?.parse().ok()
}

fn user_profile_unavailable() -> FairyError {
    FairyError::new(
        ErrorCode::UserProfileUnavailable,
        "本地用户资料不可用，请清除或重新设置",
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

    fn named(value: &str) -> UserProfileInput {
        UserProfileInput {
            preferred_name: Some(value.to_owned()),
        }
    }

    #[test]
    fn save_restart_clear_and_same_value_have_explicit_revision_semantics() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        let store = UserProfileStore::new(root.clone());

        let first = store.update(named("Rinai")).expect("save preferred name");
        assert!(first.changed);
        assert_eq!(
            first.snapshot.as_ref().expect("named snapshot").revision(),
            Revision::INITIAL
        );

        let same = store.update(named("  Rinai  ")).expect("save same name");
        assert!(!same.changed);
        assert_eq!(
            same.snapshot.expect("same snapshot").revision(),
            Revision::INITIAL
        );

        let restarted = UserProfileStore::new(root);
        assert_eq!(
            restarted
                .current()
                .expect("read profile after restart")
                .expect("profile exists")
                .preferred_name(),
            Some("Rinai")
        );
        let cleared = restarted.clear().expect("clear preferred name");
        let cleared_snapshot = cleared.snapshot.expect("cleared snapshot exists");
        assert!(cleared.changed);
        assert_eq!(cleared_snapshot.revision().get(), 2);
        assert_eq!(cleared_snapshot.preferred_name(), None);

        let same_clear = restarted.clear().expect("clear already empty name");
        assert!(!same_clear.changed);
        assert_eq!(
            same_clear
                .snapshot
                .expect("existing clear snapshot")
                .revision(),
            cleared_snapshot.revision()
        );
    }

    #[test]
    fn never_configured_and_clear_is_typed_absence_not_fabricated_profile() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        let store = UserProfileStore::new(root);

        assert_eq!(store.current().expect("read empty profile"), None);
        assert_eq!(
            store.clear().expect("clear absent profile"),
            UserProfileUpdate {
                snapshot: None,
                changed: false,
                recovered_corruption: false
            }
        );
    }

    #[test]
    fn corrupt_profile_is_reported_and_explicit_save_recovers_it() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        let store = UserProfileStore::new(root.clone());
        store.update(named("旧称呼")).expect("save initial profile");
        fs::write(root.directory().join(USER_PROFILE_POINTER_PATH), b"{broken")
            .expect("corrupt current pointer");

        let error = store.current().expect_err("corruption must be explicit");
        assert_eq!(error.code, ErrorCode::UserProfileUnavailable);

        let recovered = store
            .update(named("新称呼"))
            .expect("explicitly recover profile");
        assert!(recovered.changed);
        assert!(recovered.recovered_corruption);
        assert_eq!(
            recovered
                .snapshot
                .as_ref()
                .expect("recovered snapshot")
                .revision()
                .get(),
            2
        );
        assert_eq!(
            store
                .current()
                .expect("read recovered profile")
                .expect("recovered profile exists")
                .preferred_name(),
            Some("新称呼")
        );
    }

    #[test]
    fn invalid_profile_is_rejected_without_changing_current_pointer() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        let store = UserProfileStore::new(root);
        store.update(named("Rinai")).expect("save valid profile");

        let error = store
            .update(named("错误\n称呼"))
            .expect_err("invalid update must fail");
        assert_eq!(error.code, ErrorCode::InvalidUserProfile);
        assert_eq!(
            store
                .current()
                .expect("read unchanged profile")
                .expect("profile exists")
                .preferred_name(),
            Some("Rinai")
        );
    }
}
