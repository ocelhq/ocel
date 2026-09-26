use crate::env::live_file;
use crate::proto::common::bindings::v1::binding::Properties;
use crate::proto::common::bindings::v1::{Binding, BucketProperties, PostgresProperties};
use crate::Error;

pub(crate) fn postgres(name: &str) -> Result<PostgresProperties, Error> {
    match read(&format!("OCEL_RESOURCE_POSTGRES_{name}"), "POSTGRES")? {
        (Some(Properties::Postgres(properties)), _) => Ok(*properties),
        (other, key) => Err(Error::WrongBindingType {
            key,
            found: kind_of(&other),
            expected: "POSTGRES".to_string(),
        }),
    }
}

pub(crate) fn bucket(name: &str) -> Result<BucketProperties, Error> {
    match read(&format!("OCEL_RESOURCE_BUCKET_{name}"), "BUCKET")? {
        (Some(Properties::Bucket(properties)), _) => Ok(*properties),
        (other, key) => Err(Error::WrongBindingType {
            key,
            found: kind_of(&other),
            expected: "BUCKET".to_string(),
        }),
    }
}

fn read(key: &str, expected: &str) -> Result<(Option<Properties>, String), Error> {
    let key = key.to_string();
    let Some(raw) = delivered(&key) else {
        return Err(Error::MissingBinding { key });
    };
    let delivered: Binding = serde_json::from_str(&raw).map_err(|_| Error::Binding {
        key: key.clone(),
        expected: expected.to_string(),
    })?;
    Ok((delivered.properties, key))
}

fn kind_of(properties: &Option<Properties>) -> String {
    match properties {
        Some(Properties::Postgres(_)) => "POSTGRES",
        Some(Properties::Bucket(_)) => "BUCKET",
        Some(Properties::Custom(_)) => "CUSTOM",
        None => "UNSPECIFIED",
    }
    .to_string()
}

fn delivered(key: &str) -> Option<String> {
    std::env::var(key)
        .ok()
        .or_else(|| live_file(key))
        .filter(|raw| !raw.is_empty())
}

pub(crate) fn encoded(value: &str) -> String {
    let mut out = String::with_capacity(value.len());
    for byte in value.bytes() {
        match byte {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' => {
                out.push(byte as char)
            }
            _ => out.push_str(&format!("%{byte:02X}")),
        }
    }
    out
}
