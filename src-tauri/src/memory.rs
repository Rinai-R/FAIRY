use crate::{app_error::AppError, capability::Capability};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct MemoryStore;

pub fn open() -> Result<MemoryStore, AppError> {
    Err(Capability::Memory.unavailable())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn memory_open_does_not_scan_or_migrate_legacy_data() {
        let error = open().expect_err("foundation build must not open legacy storage");

        assert_eq!(error.code, "CAPABILITY_UNAVAILABLE");
        assert!(error.message.contains("memory"));
    }
}
