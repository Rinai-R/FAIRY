use std::time::{SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use fairy_domain::{
    AssistantSource, ErrorCode, FairyError, SearchConnectionConfig, WebSearchResponse,
};
use fairy_harness::WebSearchGateway;
use futures_util::StreamExt;
use reqwest::header::{ACCEPT, HeaderValue};
use secrecy::{ExposeSecret, SecretString};
use serde_json::Value;
use tokio_util::sync::CancellationToken;
use url::Url;

const SUBSCRIPTION_TOKEN_HEADER: &str = "X-Subscription-Token";
const MAX_QUERY_CHARS: usize = 300;
const MAX_RESULTS: usize = 5;
const MAX_TITLE_CHARS: usize = 200;
const MAX_SNIPPET_CHARS: usize = 600;
const MAX_RESPONSE_BYTES: usize = 1024 * 1024;

#[derive(Debug)]
pub struct BraveSearchGateway {
    client: reqwest::Client,
    config: SearchConnectionConfig,
    api_key: SecretString,
}

impl BraveSearchGateway {
    pub fn new(config: SearchConnectionConfig, api_key: SecretString) -> Result<Self, FairyError> {
        config.verify_integrity()?;
        validate_api_key(&api_key)?;
        let client = reqwest::Client::builder()
            .build()
            .map_err(|_| search_failed("无法创建搜索 HTTP client", true))?;
        Ok(Self {
            client,
            config,
            api_key,
        })
    }

    pub fn with_client(
        client: reqwest::Client,
        config: SearchConnectionConfig,
        api_key: SecretString,
    ) -> Result<Self, FairyError> {
        config.verify_integrity()?;
        validate_api_key(&api_key)?;
        Ok(Self {
            client,
            config,
            api_key,
        })
    }

    fn request(&self, query: &str) -> Result<reqwest::Request, FairyError> {
        let query = validate_query(query)?;
        let mut url = Url::parse(self.config.endpoint())
            .map_err(|_| search_failed("搜索 endpoint 不是有效 URL", false))?;
        url.query_pairs_mut()
            .append_pair("q", query)
            .append_pair("count", &MAX_RESULTS.to_string())
            .append_pair("safesearch", "moderate")
            .append_pair("text_decorations", "false");
        let mut token = HeaderValue::from_str(self.api_key.expose_secret())
            .map_err(|_| search_secret_unavailable("搜索密钥不能编码为认证 Header"))?;
        token.set_sensitive(true);
        self.client
            .get(url)
            .header(ACCEPT, "application/json")
            .header(SUBSCRIPTION_TOKEN_HEADER, token)
            .build()
            .map_err(|_| search_failed("无法构造 Brave Search 请求", false))
    }
}

#[async_trait]
impl WebSearchGateway for BraveSearchGateway {
    async fn search(
        &self,
        query: String,
        cancellation: CancellationToken,
    ) -> Result<WebSearchResponse, FairyError> {
        let request = self.request(&query)?;
        let response = tokio::select! {
            () = cancellation.cancelled() => return Err(search_cancelled()),
            result = self.client.execute(request) => {
                result.map_err(|_| search_failed("无法连接 Brave Search", true))?
            }
        };
        map_status(response.status())?;
        let bytes = collect_limited(response, cancellation).await?;
        let payload: Value = serde_json::from_slice(&bytes)
            .map_err(|_| search_failed("Brave Search 返回了无效 JSON", false))?;
        parse_response(query, &payload, now_unix_ms()?)
    }
}

async fn collect_limited(
    response: reqwest::Response,
    cancellation: CancellationToken,
) -> Result<Vec<u8>, FairyError> {
    let mut output = Vec::new();
    let mut stream = response.bytes_stream();
    while let Some(chunk) = tokio::select! {
        () = cancellation.cancelled() => return Err(search_cancelled()),
        chunk = stream.next() => chunk,
    } {
        let chunk = chunk.map_err(|_| search_failed("Brave Search 响应流中断", true))?;
        if output.len().saturating_add(chunk.len()) > MAX_RESPONSE_BYTES {
            return Err(search_failed("Brave Search 响应超过大小上限", false));
        }
        output.extend_from_slice(&chunk);
    }
    Ok(output)
}

fn parse_response(
    query: String,
    payload: &Value,
    fetched_at_unix_ms: i64,
) -> Result<WebSearchResponse, FairyError> {
    let results = payload
        .get("web")
        .and_then(|web| web.get("results"))
        .and_then(Value::as_array)
        .ok_or_else(|| search_failed("Brave Search 响应缺少 web.results", false))?;
    let mut sources = Vec::with_capacity(results.len().min(MAX_RESULTS));
    for (index, result) in results.iter().take(MAX_RESULTS).enumerate() {
        let title = required_string(result, "title", "搜索结果缺少 title")?;
        let raw_url = required_string(result, "url", "搜索结果缺少 url")?;
        let parsed_url =
            Url::parse(raw_url).map_err(|_| search_failed("搜索结果 URL 无效", false))?;
        if !matches!(parsed_url.scheme(), "http" | "https") {
            return Err(search_failed("搜索结果 URL 协议不受支持", false));
        }
        let snippet = required_string(result, "description", "搜索结果缺少 description")?;
        sources.push(AssistantSource {
            title: truncate_chars(title, MAX_TITLE_CHARS),
            url: parsed_url.to_string(),
            snippet: truncate_chars(snippet, MAX_SNIPPET_CHARS),
            rank: u8::try_from(index + 1)
                .map_err(|_| search_failed("搜索结果 rank 超出范围", false))?,
            fetched_at_unix_ms,
        });
    }
    Ok(WebSearchResponse { query, sources })
}

fn validate_query(query: &str) -> Result<&str, FairyError> {
    if query.is_empty() || query.trim() != query {
        return Err(tool_arguments_invalid(
            "web_search query 不能为空或包含首尾空白",
        ));
    }
    if query.chars().count() > MAX_QUERY_CHARS || query.chars().any(char::is_control) {
        return Err(tool_arguments_invalid(
            "web_search query 超长或包含控制字符",
        ));
    }
    Ok(query)
}

fn validate_api_key(api_key: &SecretString) -> Result<(), FairyError> {
    let value = api_key.expose_secret();
    if value.is_empty() || value.trim() != value {
        return Err(search_secret_unavailable("搜索密钥不能为空或包含首尾空白"));
    }
    Ok(())
}

fn map_status(status: reqwest::StatusCode) -> Result<(), FairyError> {
    if status.is_success() {
        return Ok(());
    }
    match status {
        reqwest::StatusCode::UNAUTHORIZED | reqwest::StatusCode::FORBIDDEN => Err(FairyError::new(
            ErrorCode::SearchAuthFailed,
            "Brave Search 认证失败",
            false,
        )),
        reqwest::StatusCode::TOO_MANY_REQUESTS => Err(FairyError::new(
            ErrorCode::SearchRateLimited,
            "Brave Search 请求受到限流",
            true,
        )),
        _ => Err(search_failed(
            "Brave Search 返回非成功状态",
            status.is_server_error(),
        )),
    }
}

fn required_string<'a>(
    value: &'a Value,
    field: &str,
    message: &'static str,
) -> Result<&'a str, FairyError> {
    value
        .get(field)
        .and_then(Value::as_str)
        .filter(|value| !value.is_empty())
        .ok_or_else(|| search_failed(message, false))
}

fn truncate_chars(value: &str, limit: usize) -> String {
    value.chars().take(limit).collect()
}

fn now_unix_ms() -> Result<i64, FairyError> {
    let duration = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_err(|_| search_failed("系统时间早于 Unix epoch", false))?;
    i64::try_from(duration.as_millis()).map_err(|_| search_failed("系统时间超出支持范围", false))
}

fn tool_arguments_invalid(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::ToolArgumentsInvalid, message, false)
}

fn search_secret_unavailable(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::SearchSecretUnavailable, message, false)
}

fn search_failed(message: &'static str, retryable: bool) -> FairyError {
    FairyError::new(ErrorCode::SearchFailed, message, retryable)
}

fn search_cancelled() -> FairyError {
    FairyError::new(ErrorCode::TurnInterrupted, "搜索请求已取消", false)
}

#[cfg(test)]
mod tests {
    use std::time::Duration;

    use fairy_domain::{
        SearchConnectionCompiler, SearchConnectionId, SearchConnectionInput, SearchProvider,
    };
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    use tokio::net::TcpListener;

    use super::*;

    fn config(endpoint: String) -> SearchConnectionConfig {
        SearchConnectionCompiler
            .compile(
                SearchConnectionId::new(),
                SearchConnectionInput {
                    provider: SearchProvider::Brave,
                    endpoint,
                },
            )
            .expect("compile test search config")
    }

    async fn gateway_for(status: u16, body: String) -> BraveSearchGateway {
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("bind search server");
        let address = listener.local_addr().expect("server address");
        tokio::spawn(async move {
            let (mut socket, _) = listener.accept().await.expect("accept search request");
            let mut bytes = Vec::new();
            let mut buffer = [0_u8; 4096];
            loop {
                let read = socket.read(&mut buffer).await.expect("read search request");
                if read == 0 {
                    break;
                }
                bytes.extend_from_slice(&buffer[..read]);
                if bytes.windows(4).any(|window| window == b"\r\n\r\n") {
                    break;
                }
            }
            let response = format!(
                "HTTP/1.1 {status} Test\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                body.len()
            );
            socket
                .write_all(response.as_bytes())
                .await
                .expect("write search response");
        });
        BraveSearchGateway::new(
            config(format!("http://{address}/res/v1/web/search")),
            SecretString::from("test-secret".to_owned()),
        )
        .expect("build search gateway")
    }

    #[tokio::test]
    async fn maps_up_to_five_normalized_sources() {
        let results = (0..7)
            .map(|index| {
                serde_json::json!({
                    "title": format!("Title {index}"),
                    "url": format!("https://example.test/{index}"),
                    "description": format!("Snippet {index}")
                })
            })
            .collect::<Vec<_>>();
        let gateway = gateway_for(
            200,
            serde_json::json!({"web": {"results": results}}).to_string(),
        )
        .await;

        let response = gateway
            .search("Rust 1.95".to_owned(), CancellationToken::new())
            .await
            .expect("search success");

        assert_eq!(response.query, "Rust 1.95");
        assert_eq!(response.sources.len(), 5);
        assert_eq!(response.sources[0].rank, 1);
        assert_eq!(response.sources[4].rank, 5);
        assert!(
            response
                .sources
                .iter()
                .all(|source| source.fetched_at_unix_ms > 0)
        );
    }

    #[tokio::test]
    async fn query_and_http_failures_keep_distinct_error_codes() {
        let gateway = gateway_for(200, "{}".to_owned()).await;
        for query in ["", " leading", "line\nbreak"] {
            let error = gateway
                .search(query.to_owned(), CancellationToken::new())
                .await
                .expect_err("invalid query");
            assert_eq!(error.code, ErrorCode::ToolArgumentsInvalid);
        }
        for (status, code) in [
            (401, ErrorCode::SearchAuthFailed),
            (403, ErrorCode::SearchAuthFailed),
            (429, ErrorCode::SearchRateLimited),
            (500, ErrorCode::SearchFailed),
        ] {
            let gateway = gateway_for(status, "secret response body".to_owned()).await;
            let error = gateway
                .search("valid".to_owned(), CancellationToken::new())
                .await
                .expect_err("status must fail");
            assert_eq!(error.code, code);
            assert!(!error.message.contains("secret response body"));
        }
    }

    #[tokio::test]
    async fn cancellation_is_not_reported_as_search_success() {
        let gateway = gateway_for(200, "{}".to_owned()).await;
        let cancellation = CancellationToken::new();
        cancellation.cancel();
        let error = gateway
            .search("valid".to_owned(), cancellation)
            .await
            .expect_err("cancelled search");
        assert_eq!(error.code, ErrorCode::TurnInterrupted);
        tokio::time::sleep(Duration::from_millis(1)).await;
    }
}
