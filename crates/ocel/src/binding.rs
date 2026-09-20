use crate::env::live_file;
use crate::proto::common::bindings::v1::binding::Properties;
use crate::proto::common::bindings::v1::{Binding, PostgresProperties};
use crate::Error;

pub(crate) fn postgres(name: &str) -> Result<PostgresProperties, Error> {
    let key = format!("OCEL_RESOURCE_POSTGRES_{name}");
    let Some(raw) = delivered(&key) else {
        return Err(Error::MissingBinding { key });
    };

    let delivered: Binding = serde_json::from_str(&raw).map_err(|_| Error::Binding {
        key: key.clone(),
        expected: "POSTGRES".to_string(),
    })?;

    match delivered.properties {
        Some(Properties::Postgres(properties)) => Ok(*properties),
        other => Err(Error::WrongBindingType {
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
