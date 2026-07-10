//! OpenAI-compatible Responses 模型传输。

#![forbid(unsafe_code)]

mod request;
mod response_stream;
mod usage;

pub use request::build_responses_request;
pub use response_stream::OpenAiResponsesGateway;
