use crate::proto::common::links::v1::link::Properties;
use crate::proto::common::links::v1::{Link, PostgresProperties};
use crate::Error;

pub(crate) fn postgres(name: &str) -> Result<PostgresProperties, Error> {
    let key = format!("OCEL_RESOURCE_POSTGRES_{name}");
    let raw = match std::env::var(&key) {
        Ok(raw) if !raw.is_empty() => raw,
        _ => return Err(Error::MissingLink { key }),
    };

    let delivered: Link = serde_json::from_str(&raw).map_err(|_| Error::Link {
        key: key.clone(),
        expected: "POSTGRES".to_string(),
    })?;

    match delivered.properties {
        Some(Properties::Postgres(properties)) => Ok(*properties),
        other => Err(Error::WrongLinkType {
            key,
            carried: carried(&other),
            expected: "POSTGRES".to_string(),
        }),
    }
}

fn carried(properties: &Option<Properties>) -> String {
    match properties {
        Some(Properties::Postgres(_)) => "POSTGRES",
        Some(Properties::Bucket(_)) => "BUCKET",
        Some(Properties::Custom(_)) => "CUSTOM",
        None => "UNSPECIFIED",
    }
    .to_string()
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
