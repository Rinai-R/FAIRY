//! FAIRY 的本地持久化与秘密存储边界。

#![forbid(unsafe_code)]

mod atomic_json;
mod character_store;
mod model_config_store;
mod search_config_store;
mod secret_store;
mod user_profile_store;

pub use atomic_json::{DocumentRead, StorageRoot};
pub use character_store::{ActiveCharacter, CharacterCatalog, CharacterDiagnostic, CharacterStore};
pub use model_config_store::{ModelConnectionStore, ResolvedModelConnection};
pub use search_config_store::{ResolvedSearchConnection, SearchConnectionStore};
pub use secret_store::{
    SearchSecretStore, SecretStore, SystemSearchSecretStore, SystemSecretStore,
};
pub use user_profile_store::{UserProfileStore, UserProfileUpdate};
