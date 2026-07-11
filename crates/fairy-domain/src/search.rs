use serde::{Deserialize, Serialize};
use std::net::IpAddr;

use url::{Host, Url};

use crate::{AssistantSource, ErrorCode, FairyError, SearchConnectionId};

const SEARCH_CONNECTION_SCHEMA_VERSION: u32 = 1;
pub const DEFAULT_BRAVE_SEARCH_ENDPOINT: &str = "https://api.search.brave.com/res/v1/web/search";

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum SearchProvider {
    Brave,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct WebSearchResponse {
    pub query: String,
    pub sources: Vec<AssistantSource>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SearchConnectionInput {
    pub provider: SearchProvider,
    pub endpoint: String,
}

impl Default for SearchConnectionInput {
    fn default() -> Self {
        Self {
            provider: SearchProvider::Brave,
            endpoint: DEFAULT_BRAVE_SEARCH_ENDPOINT.to_owned(),
        }
    }
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct SearchConnectionConfig {
    schema_version: u32,
    connection_id: SearchConnectionId,
    provider: SearchProvider,
    endpoint: String,
}

#[derive(Clone, Copy, Debug, Default)]
pub struct SearchConnectionCompiler;

impl SearchConnectionCompiler {
    pub fn compile(
        self,
        connection_id: SearchConnectionId,
        input: SearchConnectionInput,
    ) -> Result<SearchConnectionConfig, FairyError> {
        let endpoint = validate_endpoint(&input.endpoint)?;
        Ok(SearchConnectionConfig {
            schema_version: SEARCH_CONNECTION_SCHEMA_VERSION,
            connection_id,
            provider: input.provider,
            endpoint,
        })
    }
}

impl SearchConnectionConfig {
    #[must_use]
    pub const fn schema_version(&self) -> u32 {
        self.schema_version
    }

    #[must_use]
    pub const fn connection_id(&self) -> SearchConnectionId {
        self.connection_id
    }

    #[must_use]
    pub const fn provider(&self) -> SearchProvider {
        self.provider
    }

    #[must_use]
    pub fn endpoint(&self) -> &str {
        &self.endpoint
    }

    pub fn verify_integrity(&self) -> Result<(), FairyError> {
        if self.schema_version != SEARCH_CONNECTION_SCHEMA_VERSION {
            return Err(invalid_search_config("搜索连接配置版本不受支持"));
        }
        let rebuilt = SearchConnectionCompiler.compile(
            self.connection_id,
            SearchConnectionInput {
                provider: self.provider,
                endpoint: self.endpoint.clone(),
            },
        )?;
        if rebuilt != *self {
            return Err(invalid_search_config("搜索连接配置完整性校验失败"));
        }
        Ok(())
    }
}

fn validate_endpoint(raw: &str) -> Result<String, FairyError> {
    let value = raw.trim();
    if value != raw || value.is_empty() {
        return Err(invalid_search_config(
            "搜索 endpoint 不能为空或包含首尾空白",
        ));
    }
    let parsed =
        Url::parse(value).map_err(|_| invalid_search_config("搜索 endpoint 不是有效 URL"))?;
    match parsed.scheme() {
        "https" if parsed.host().is_some() => {}
        "http" if is_loopback_host(parsed.host()) => {}
        _ => {
            return Err(invalid_search_config(
                "搜索 endpoint 必须使用 HTTPS；本机 loopback 可以使用 HTTP",
            ));
        }
    }
    if !parsed.username().is_empty()
        || parsed.password().is_some()
        || parsed.query().is_some()
        || parsed.fragment().is_some()
    {
        return Err(invalid_search_config(
            "搜索 endpoint 不得包含认证信息、query 或 fragment",
        ));
    }
    if !parsed.path().ends_with("/res/v1/web/search") {
        return Err(invalid_search_config(
            "Brave endpoint 必须指向 /res/v1/web/search",
        ));
    }
    Ok(parsed.to_string())
}

fn is_loopback_host(host: Option<Host<&str>>) -> bool {
    match host {
        Some(Host::Domain(domain)) => domain.eq_ignore_ascii_case("localhost"),
        Some(Host::Ipv4(address)) => IpAddr::V4(address).is_loopback(),
        Some(Host::Ipv6(address)) => IpAddr::V6(address).is_loopback(),
        None => false,
    }
}

fn invalid_search_config(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::InvalidSearchConfig, message, false)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn default_brave_connection_is_valid_and_versioned() {
        let config = SearchConnectionCompiler
            .compile(SearchConnectionId::new(), SearchConnectionInput::default())
            .expect("compile default Brave config");
        assert_eq!(config.schema_version(), 1);
        assert_eq!(config.provider(), SearchProvider::Brave);
        assert_eq!(config.endpoint(), DEFAULT_BRAVE_SEARCH_ENDPOINT);
        config.verify_integrity().expect("verify search config");
    }

    #[test]
    fn endpoint_rejects_non_https_credentials_query_and_wrong_path() {
        for endpoint in [
            "http://api.search.brave.com/res/v1/web/search",
            "https://user:pass@api.search.brave.com/res/v1/web/search",
            "https://api.search.brave.com/res/v1/web/search?q=secret",
            "https://api.search.brave.com/search",
            " https://api.search.brave.com/res/v1/web/search",
        ] {
            let error = SearchConnectionCompiler
                .compile(
                    SearchConnectionId::new(),
                    SearchConnectionInput {
                        provider: SearchProvider::Brave,
                        endpoint: endpoint.to_owned(),
                    },
                )
                .expect_err("invalid search endpoint must fail");
            assert_eq!(error.code, ErrorCode::InvalidSearchConfig);
        }
    }

    #[test]
    fn loopback_http_is_explicitly_allowed_for_local_proxy_testing() {
        for endpoint in [
            "http://127.0.0.1:8080/res/v1/web/search",
            "http://localhost:8080/res/v1/web/search",
            "http://[::1]:8080/res/v1/web/search",
        ] {
            SearchConnectionCompiler
                .compile(
                    SearchConnectionId::new(),
                    SearchConnectionInput {
                        provider: SearchProvider::Brave,
                        endpoint: endpoint.to_owned(),
                    },
                )
                .expect("loopback search endpoint");
        }
    }
}
