use std::fs::{self, File};
use std::io::{self, Write};
use std::path::{Component, Path, PathBuf};

use fairy_domain::{ErrorCode, FairyError};
use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use tempfile::Builder;

const STORAGE_DIRECTORY: &str = "harness/v1";

#[derive(Clone, Debug)]
pub struct StorageRoot {
    directory: PathBuf,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub enum DocumentRead<T> {
    Missing,
    Found(T),
}

#[derive(Deserialize, Serialize)]
struct VersionedDocument<T> {
    schema_version: u32,
    data: T,
}

impl StorageRoot {
    pub fn new(app_config_directory: impl AsRef<Path>) -> Result<Self, FairyError> {
        let directory = app_config_directory.as_ref().join(STORAGE_DIRECTORY);
        fs::create_dir_all(&directory).map_err(|_| storage_io("无法创建 FAIRY 配置目录"))?;
        sync_directory(
            directory
                .parent()
                .ok_or_else(|| storage_io("FAIRY 配置目录缺少父目录"))?,
        )?;
        Ok(Self { directory })
    }

    #[must_use]
    pub fn directory(&self) -> &Path {
        &self.directory
    }

    pub fn read<T: DeserializeOwned>(
        &self,
        relative_path: impl AsRef<Path>,
        expected_schema_version: u32,
    ) -> Result<DocumentRead<T>, FairyError> {
        let path = self.resolve(relative_path.as_ref())?;
        let file = match File::open(path) {
            Ok(file) => file,
            Err(error) if error.kind() == io::ErrorKind::NotFound => {
                return Ok(DocumentRead::Missing);
            }
            Err(_) => return Err(storage_io("无法读取本地配置文件")),
        };
        let document: VersionedDocument<T> =
            serde_json::from_reader(file).map_err(|_| storage_corrupted("本地配置文件无法解析"))?;
        if document.schema_version != expected_schema_version {
            return Err(storage_corrupted("本地配置文件版本不受支持"));
        }
        Ok(DocumentRead::Found(document.data))
    }

    pub fn write_new<T: Serialize>(
        &self,
        relative_path: impl AsRef<Path>,
        schema_version: u32,
        data: &T,
    ) -> Result<(), FairyError> {
        self.write(
            relative_path.as_ref(),
            schema_version,
            data,
            WriteMode::CreateNew,
        )
    }

    pub fn write_replace<T: Serialize>(
        &self,
        relative_path: impl AsRef<Path>,
        schema_version: u32,
        data: &T,
    ) -> Result<(), FairyError> {
        self.write(
            relative_path.as_ref(),
            schema_version,
            data,
            WriteMode::Replace,
        )
    }

    pub fn remove(&self, relative_path: impl AsRef<Path>) -> Result<bool, FairyError> {
        let path = self.resolve(relative_path.as_ref())?;
        let parent = path
            .parent()
            .ok_or_else(|| storage_io("本地配置文件缺少父目录"))?;
        match fs::remove_file(&path) {
            Ok(()) => {
                sync_directory(parent)?;
                Ok(true)
            }
            Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(false),
            Err(_) => Err(storage_io("无法删除本地配置文件")),
        }
    }

    fn write<T: Serialize>(
        &self,
        relative_path: &Path,
        schema_version: u32,
        data: &T,
        mode: WriteMode,
    ) -> Result<(), FairyError> {
        let destination = self.resolve(relative_path)?;
        let parent = destination
            .parent()
            .ok_or_else(|| storage_io("本地配置文件缺少父目录"))?;
        fs::create_dir_all(parent).map_err(|_| storage_io("无法创建本地配置子目录"))?;

        let bytes = serde_json::to_vec(&VersionedDocument {
            schema_version,
            data,
        })
        .map_err(|_| storage_io("无法序列化本地配置"))?;
        let mut temporary = Builder::new()
            .prefix(".fairy-write-")
            .tempfile_in(parent)
            .map_err(|_| storage_io("无法创建原子写入临时文件"))?;
        temporary
            .write_all(&bytes)
            .map_err(|_| storage_io("无法写入本地配置临时文件"))?;
        temporary
            .as_file_mut()
            .flush()
            .map_err(|_| storage_io("无法刷新本地配置临时文件"))?;
        temporary
            .as_file()
            .sync_all()
            .map_err(|_| storage_io("无法同步本地配置临时文件"))?;

        match mode {
            WriteMode::CreateNew => temporary
                .persist_noclobber(&destination)
                .map_err(|_| storage_io("不可变配置 revision 已经存在"))?,
            WriteMode::Replace => temporary
                .persist(&destination)
                .map_err(|_| storage_io("无法原子替换本地配置"))?,
        };
        sync_directory(parent)
    }

    fn resolve(&self, relative_path: &Path) -> Result<PathBuf, FairyError> {
        if relative_path.as_os_str().is_empty()
            || relative_path.is_absolute()
            || relative_path
                .components()
                .any(|component| !matches!(component, Component::Normal(_)))
        {
            return Err(storage_io("本地配置路径不合法"));
        }
        Ok(self.directory.join(relative_path))
    }
}

#[derive(Clone, Copy)]
enum WriteMode {
    CreateNew,
    Replace,
}

fn sync_directory(directory: &Path) -> Result<(), FairyError> {
    File::open(directory)
        .and_then(|file| file.sync_all())
        .map_err(|_| storage_io("无法同步本地配置目录"))
}

fn storage_io(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::StorageIo, message, true)
}

fn storage_corrupted(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::StorageCorrupted, message, false)
}

#[cfg(test)]
mod tests {
    use std::fs;

    use serde::ser::Error as _;
    use tempfile::tempdir;

    use super::*;

    #[derive(Debug, Deserialize, Eq, PartialEq, Serialize)]
    struct Example {
        value: String,
    }

    struct FailingSerialize;

    impl Serialize for FailingSerialize {
        fn serialize<S>(&self, _serializer: S) -> Result<S::Ok, S::Error>
        where
            S: serde::Serializer,
        {
            Err(S::Error::custom("intentional test failure"))
        }
    }

    #[test]
    fn writes_and_reads_versioned_document() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        let data = Example {
            value: "稳定内容".to_owned(),
        };

        root.write_new("characters/example.json", 1, &data)
            .expect("write document");

        assert_eq!(
            root.read("characters/example.json", 1)
                .expect("read document"),
            DocumentRead::Found(data)
        );
    }

    #[test]
    fn missing_is_distinct_from_corruption() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");

        assert_eq!(
            root.read::<Example>("missing.json", 1)
                .expect("missing is expected"),
            DocumentRead::Missing
        );

        fs::write(root.directory().join("broken.json"), b"{not-json")
            .expect("write broken fixture");
        let error = root
            .read::<Example>("broken.json", 1)
            .expect_err("broken json must fail");
        assert_eq!(error.code, ErrorCode::StorageCorrupted);
    }

    #[test]
    fn schema_mismatch_is_explicit_corruption() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        root.write_new(
            "versioned.json",
            1,
            &Example {
                value: "v1".to_owned(),
            },
        )
        .expect("write v1");

        let error = root
            .read::<Example>("versioned.json", 2)
            .expect_err("schema mismatch must fail");
        assert_eq!(error.code, ErrorCode::StorageCorrupted);
    }

    #[test]
    fn immutable_write_never_overwrites_existing_revision() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        root.write_new(
            "revision.json",
            1,
            &Example {
                value: "first".to_owned(),
            },
        )
        .expect("write first revision");

        let error = root
            .write_new(
                "revision.json",
                1,
                &Example {
                    value: "second".to_owned(),
                },
            )
            .expect_err("revision overwrite must fail");
        assert_eq!(error.code, ErrorCode::StorageIo);
        assert_eq!(
            root.read("revision.json", 1).expect("read original"),
            DocumentRead::Found(Example {
                value: "first".to_owned()
            })
        );
    }

    #[test]
    fn serialization_failure_keeps_previous_replacement() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        root.write_replace(
            "current.json",
            1,
            &Example {
                value: "valid".to_owned(),
            },
        )
        .expect("write valid current value");

        let error = root
            .write_replace("current.json", 1, &FailingSerialize)
            .expect_err("serialization failure must be returned");
        assert_eq!(error.code, ErrorCode::StorageIo);
        assert_eq!(
            root.read("current.json", 1).expect("read original"),
            DocumentRead::Found(Example {
                value: "valid".to_owned()
            })
        );
    }

    #[test]
    fn rejects_absolute_parent_and_empty_paths() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        let data = Example {
            value: "blocked".to_owned(),
        };

        for path in ["", "../escape.json", "/tmp/escape.json"] {
            let error = root
                .write_new(path, 1, &data)
                .expect_err("unsafe path must fail");
            assert_eq!(error.code, ErrorCode::StorageIo);
        }
    }

    #[test]
    fn creates_only_new_storage_boundary_and_leaves_legacy_data_untouched() {
        let temp = tempdir().expect("create temp directory");
        let legacy = temp.path().join("legacy/role.json");
        fs::create_dir_all(legacy.parent().expect("legacy parent")).expect("create legacy dir");
        fs::write(&legacy, b"legacy-data").expect("write legacy file");

        let root = StorageRoot::new(temp.path()).expect("create storage root");

        assert_eq!(root.directory(), temp.path().join("harness/v1"));
        assert_eq!(fs::read(legacy).expect("read legacy file"), b"legacy-data");
    }

    #[test]
    fn remove_distinguishes_existing_and_missing_documents() {
        let temp = tempdir().expect("create temp directory");
        let root = StorageRoot::new(temp.path()).expect("create storage root");
        root.write_new(
            "removable.json",
            1,
            &Example {
                value: "temporary".to_owned(),
            },
        )
        .expect("write removable document");

        assert!(root.remove("removable.json").expect("remove existing"));
        assert!(!root.remove("removable.json").expect("remove missing"));
    }
}
