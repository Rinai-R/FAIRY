use serde::{Deserialize, Serialize};

pub const ABI_VERSION: u32 = 1;
pub const ENTRY_MODULE: &str = "module.wasm";

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct AbiRange {
    pub min: u32,
    pub max: u32,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Manifest {
    pub schema_version: u32,
    pub id: String,
    pub version: String,
    pub abi: AbiRange,
    pub entry: String,
    pub exports: Vec<String>,
    pub capabilities: Vec<String>,
    pub config_schema_version: u32,
    pub data_schema_version: u32,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Correlation {
    pub plugin_instance_id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub trace_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub turn_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub external_message_id: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct CodedError {
    pub code: String,
    pub message: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Envelope {
    pub abi_version: u32,
    pub kind: String,
    pub correlation: Correlation,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub payload: Option<serde_json::Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<CodedError>,
}

impl Envelope {
    pub fn result(plugin_instance_id: impl Into<String>, payload: serde_json::Value) -> Self {
        Self {
            abi_version: ABI_VERSION,
            kind: "result".to_string(),
            correlation: Correlation {
                plugin_instance_id: plugin_instance_id.into(),
                trace_id: None,
                turn_id: None,
                external_message_id: None,
            },
            payload: Some(payload),
            error: None,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn envelope_round_trip_keeps_correlation() {
        let mut envelope = Envelope::result("event-1", serde_json::json!({"accepted": true}));
        envelope.correlation.trace_id = Some("trace-1".to_string());
        let encoded = serde_json::to_string(&envelope).unwrap();
        let parsed: Envelope = serde_json::from_str(&encoded).unwrap();
        assert_eq!(parsed.kind, "result");
        assert_eq!(parsed.correlation.plugin_instance_id, "event-1");
        assert_eq!(parsed.correlation.trace_id.as_deref(), Some("trace-1"));
        assert!(!encoded.contains("sk-live"));
        assert!(!encoded.contains("Bearer "));
    }

    #[test]
    fn manifest_uses_host_abi_v1_fields() {
        let manifest = Manifest {
            schema_version: 1,
            id: "fairy.plugin.example".to_string(),
            version: "1.0.0".to_string(),
            abi: AbiRange { min: 1, max: 1 },
            entry: ENTRY_MODULE.to_string(),
            exports: vec![
                "fairy_alloc".into(),
                "fairy_free".into(),
                "fairy_init".into(),
                "fairy_handle".into(),
                "fairy_shutdown".into(),
            ],
            capabilities: vec!["http.request".into()],
            config_schema_version: 1,
            data_schema_version: 1,
        };
        let encoded = serde_json::to_string(&manifest).unwrap();
        assert!(encoded.contains("schemaVersion"));
        assert!(encoded.contains("configSchemaVersion"));
        assert!(encoded.contains("fairy_handle"));
        let parsed: Manifest = serde_json::from_str(&encoded).unwrap();
        assert_eq!(parsed, manifest);
    }
}
