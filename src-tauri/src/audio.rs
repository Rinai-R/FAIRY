use crate::{app_error::AppError, capability::Capability};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AudioSession;

pub fn start() -> Result<AudioSession, AppError> {
    Err(Capability::Audio.unavailable())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn audio_start_does_not_claim_success_without_an_engine() {
        let error = start().expect_err("foundation build must not fake an audio session");

        assert_eq!(error.code, "CAPABILITY_UNAVAILABLE");
        assert!(error.message.contains("audio"));
    }
}
