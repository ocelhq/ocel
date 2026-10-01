use crate::Error;
use buffa_types::google::protobuf::Timestamp;
use serde::Serialize;
use std::time::{Duration, SystemTime};

pub(crate) const PAYLOAD_LIMIT: usize = 262_144;

pub(crate) fn encode_payload<P: Serialize>(payload: &P) -> Result<Vec<u8>, Error> {
    let bytes = serde_json::to_vec(payload).map_err(|err| Error::Payload {
        said: err.to_string(),
    })?;
    if bytes.len() > PAYLOAD_LIMIT {
        return Err(Error::PayloadTooLarge { size: bytes.len() });
    }
    Ok(bytes)
}

#[derive(Clone, Copy)]
pub(crate) enum Due {
    In(Duration),
    At(SystemTime),
}

impl Due {
    pub(crate) fn to_timestamp(self) -> Timestamp {
        match self {
            Self::In(delay) => Timestamp::from(SystemTime::now() + delay),
            Self::At(at) => Timestamp::from(at),
        }
    }
}
