use crate::app_error::AppError;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Capability {
    Audio,
}

impl Capability {
    pub const fn name(self) -> &'static str {
        match self {
            Self::Audio => "audio",
        }
    }

    pub fn unavailable(self) -> AppError {
        AppError::capability_unavailable(self.name())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn audio_boundary_has_a_stable_name() {
        assert_eq!(Capability::Audio.name(), "audio");
    }
}
