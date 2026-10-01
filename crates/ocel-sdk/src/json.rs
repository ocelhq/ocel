use buffa_types::google::protobuf::Timestamp;
use serde::de::DeserializeOwned;
use std::time::SystemTime;

pub(crate) fn read_json<T: DeserializeOwned + Default>(
    text: &[u8],
) -> Result<T, serde_json::Error> {
    if text.is_empty() {
        return Ok(T::default());
    }
    serde_json::from_slice(text)
}

pub(crate) fn convert_timestamp(at: &Timestamp) -> Option<SystemTime> {
    SystemTime::try_from(at.clone()).ok()
}
