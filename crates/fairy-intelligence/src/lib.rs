//! FAIRY 的本地智能层与外部检索实现。

#![forbid(unsafe_code)]

mod search;
mod store;

pub use search::BraveSearchGateway;
pub use store::IntelligenceStore;
