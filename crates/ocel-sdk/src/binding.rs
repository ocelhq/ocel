use crate::env::live_file;
use crate::proto::common::bindings::v1::binding::Properties;
use crate::proto::common::bindings::v1::{
    Binding, BucketProperties, KvProperties, PostgresProperties,
};
use crate::Error;

pub(crate) fn read_postgres(name: &str) -> Result<PostgresProperties, Error> {
    match read_binding(&format!("OCEL_RESOURCE_POSTGRES_{name}"), "POSTGRES")? {
        (Some(Properties::Postgres(properties)), _) => Ok(*properties),
        (other, key) => Err(Error::WrongBindingType {
            key,
            found: describe_kind(&other),
            expected: "POSTGRES".to_string(),
        }),
    }
}

pub(crate) fn read_bucket(name: &str) -> Result<BucketProperties, Error> {
    match read_binding(&format!("OCEL_RESOURCE_BUCKET_{name}"), "BUCKET")? {
        (Some(Properties::Bucket(properties)), _) => Ok(*properties),
        (other, key) => Err(Error::WrongBindingType {
            key,
            found: describe_kind(&other),
            expected: "BUCKET".to_string(),
        }),
    }
}

pub(crate) fn read_kv(name: &str) -> Result<KvProperties, Error> {
    match read_binding(&format!("OCEL_RESOURCE_KV_{name}"), "KV")? {
        (Some(Properties::Kv(properties)), _) => Ok(*properties),
        (other, key) => Err(Error::WrongBindingType {
            key,
            found: describe_kind(&other),
            expected: "KV".to_string(),
        }),
    }
}

pub(crate) fn refuse_unbound_topic(name: &str) -> Result<(), Error> {
    match read_binding(&format!("OCEL_RESOURCE_TOPIC_{name}"), "TOPIC")? {
        (Some(Properties::Topic(_)), _) => Ok(()),
        (other, key) => Err(Error::WrongBindingType {
            key,
            found: describe_kind(&other),
            expected: "TOPIC".to_string(),
        }),
    }
}

pub(crate) fn refuse_unbound_task(name: &str) -> Result<(), Error> {
    match read_binding(&format!("OCEL_RESOURCE_TASK_{name}"), "TASK")? {
        (Some(Properties::Task(_)), _) => Ok(()),
        (other, key) => Err(Error::WrongBindingType {
            key,
            found: describe_kind(&other),
            expected: "TASK".to_string(),
        }),
    }
}

fn read_binding(key: &str, expected: &str) -> Result<(Option<Properties>, String), Error> {
    let key = key.to_string();
    let Some(raw) = read_delivered(&key) else {
        return Err(Error::MissingBinding { key });
    };
    let delivered: Binding = serde_json::from_str(&raw).map_err(|_| Error::Binding {
        key: key.clone(),
        expected: expected.to_string(),
    })?;
    Ok((delivered.properties, key))
}

fn describe_kind(properties: &Option<Properties>) -> String {
    match properties {
        Some(Properties::Postgres(_)) => "POSTGRES",
        Some(Properties::Bucket(_)) => "BUCKET",
        Some(Properties::Custom(_)) => "CUSTOM",
        Some(Properties::Topic(_)) => "TOPIC",
        Some(Properties::Task(_)) => "TASK",
        Some(Properties::Kv(_)) => "KV",
        Some(Properties::Realtime(_)) => "REALTIME",
        None => "UNSPECIFIED",
    }
    .to_string()
}

fn read_delivered(key: &str) -> Option<String> {
    std::env::var(key)
        .ok()
        .or_else(|| live_file(key))
        .filter(|raw| !raw.is_empty())
}

pub(crate) fn percent_encode(value: &str) -> String {
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
