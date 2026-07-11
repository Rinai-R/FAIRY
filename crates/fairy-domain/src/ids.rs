use std::fmt;
use std::str::FromStr;

use serde::{Deserialize, Serialize};
use uuid::Uuid;

macro_rules! uuid_id {
    ($name:ident) => {
        #[derive(
            Clone, Copy, Debug, Deserialize, Eq, Hash, Ord, PartialEq, PartialOrd, Serialize,
        )]
        #[serde(transparent)]
        pub struct $name(Uuid);

        impl $name {
            #[must_use]
            pub fn new() -> Self {
                Self(Uuid::new_v4())
            }

            #[must_use]
            pub const fn as_uuid(&self) -> &Uuid {
                &self.0
            }
        }

        impl Default for $name {
            fn default() -> Self {
                Self::new()
            }
        }

        impl fmt::Display for $name {
            fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
                self.0.fmt(formatter)
            }
        }

        impl FromStr for $name {
            type Err = uuid::Error;

            fn from_str(value: &str) -> Result<Self, Self::Err> {
                Uuid::parse_str(value).map(Self)
            }
        }
    };
}

uuid_id!(ConversationId);
uuid_id!(TurnId);
uuid_id!(MessageId);
uuid_id!(CharacterId);
uuid_id!(ModelConnectionId);
uuid_id!(PersonalMemoryId);
uuid_id!(KnowledgeId);
uuid_id!(KnowledgeSourceId);
uuid_id!(ExtractionBatchId);

#[derive(Clone, Copy, Debug, Deserialize, Eq, Hash, Ord, PartialEq, PartialOrd, Serialize)]
#[serde(transparent)]
pub struct Revision(u64);

impl Revision {
    pub const INITIAL: Self = Self(1);

    #[must_use]
    pub const fn new(value: u64) -> Option<Self> {
        if value == 0 { None } else { Some(Self(value)) }
    }

    #[must_use]
    pub const fn get(self) -> u64 {
        self.0
    }

    #[must_use]
    pub fn checked_next(self) -> Option<Self> {
        self.0.checked_add(1).map(Self)
    }
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, Hash, Ord, PartialEq, PartialOrd, Serialize)]
#[serde(transparent)]
pub struct WindowRevision(u64);

impl WindowRevision {
    pub const INITIAL: Self = Self(1);

    #[must_use]
    pub const fn new(value: u64) -> Option<Self> {
        if value == 0 { None } else { Some(Self(value)) }
    }

    #[must_use]
    pub const fn get(self) -> u64 {
        self.0
    }

    #[must_use]
    pub fn checked_next(self) -> Option<Self> {
        self.0.checked_add(1).map(Self)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ids_round_trip_as_json_strings() {
        let conversation_id = ConversationId::new();
        let encoded = serde_json::to_string(&conversation_id).expect("serialize conversation id");
        let decoded: ConversationId =
            serde_json::from_str(&encoded).expect("deserialize conversation id");

        assert_eq!(decoded, conversation_id);
        assert_eq!(encoded, format!("\"{conversation_id}\""));
    }

    #[test]
    fn revisions_reject_zero_and_increment_explicitly() {
        assert_eq!(Revision::new(0), None);
        assert_eq!(Revision::INITIAL.checked_next().map(Revision::get), Some(2));
        assert_eq!(WindowRevision::new(0), None);
        assert_eq!(
            WindowRevision::INITIAL
                .checked_next()
                .map(WindowRevision::get),
            Some(2)
        );
    }
}
