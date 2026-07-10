use fairy_domain::FairyError;
use serde::{Deserialize, Serialize};
use thiserror::Error;

#[derive(Debug, Clone, Deserialize, PartialEq, Eq, Error, Serialize)]
#[error("{message}")]
#[serde(rename_all = "camelCase")]
pub struct AppError {
    pub code: String,
    pub message: String,
    pub retryable: bool,
}

impl AppError {
    pub fn capability_unavailable(capability: &str) -> Self {
        Self {
            code: "CAPABILITY_UNAVAILABLE".to_owned(),
            message: format!("{capability} capability is not available in this foundation build"),
            retryable: false,
        }
    }

    pub fn tray_not_ready() -> Self {
        Self {
            code: "TRAY_NOT_READY".to_owned(),
            message: "click-through requires an active tray recovery entry".to_owned(),
            retryable: false,
        }
    }

    pub fn desktop_operation_failed(action: &str) -> Self {
        Self {
            code: "DESKTOP_OPERATION_FAILED".to_owned(),
            message: format!("desktop operation failed: {action}"),
            retryable: true,
        }
    }

    pub fn desktop_transition_rejected(phase: &str, action: &str) -> Self {
        Self {
            code: "DESKTOP_TRANSITION_REJECTED".to_owned(),
            message: format!("desktop transition rejected: {action} is unavailable during {phase}"),
            retryable: true,
        }
    }

    pub fn window_not_found() -> Self {
        Self {
            code: "WINDOW_NOT_FOUND".to_owned(),
            message: "the FAIRY companion window is unavailable".to_owned(),
            retryable: true,
        }
    }

    pub fn state_unavailable() -> Self {
        Self {
            code: "STATE_UNAVAILABLE".to_owned(),
            message: "the desktop state is temporarily unavailable".to_owned(),
            retryable: true,
        }
    }
}

impl From<FairyError> for AppError {
    fn from(error: FairyError) -> Self {
        Self {
            code: error.code.as_str().to_owned(),
            message: error.message,
            retryable: error.retryable,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn desktop_error_does_not_accept_or_expose_a_system_error() {
        let error = AppError::desktop_operation_failed("enable always-on-top");

        assert_eq!(error.code, "DESKTOP_OPERATION_FAILED");
        assert_eq!(
            error.message,
            "desktop operation failed: enable always-on-top"
        );
        assert!(!error.message.contains("token"));
        assert!(!error.message.contains("secret"));
        assert!(error.retryable);
    }

    #[test]
    fn unavailable_capability_is_not_an_ambiguous_empty_value() {
        let error = AppError::capability_unavailable("audio");

        assert_eq!(error.code, "CAPABILITY_UNAVAILABLE");
        assert!(!error.message.is_empty());
        assert!(!error.retryable);
    }

    #[test]
    fn domain_error_keeps_the_stable_code_and_retryability() {
        let error = AppError::from(FairyError::new(
            fairy_domain::ErrorCode::ModelStreamFailed,
            "模型流中断",
            true,
        ));

        assert_eq!(error.code, "MODEL_STREAM_FAILED");
        assert_eq!(error.message, "模型流中断");
        assert!(error.retryable);
    }
}
