use crate::Error;

pub(crate) fn link(name: &str, kind: &str) -> Result<serde_json::Value, Error> {
    let key = format!("OCEL_RESOURCE_{}_{}", kind.to_uppercase(), name);
    let raw = match std::env::var(&key) {
        Ok(raw) if !raw.is_empty() => raw,
        _ => return Err(Error::MissingLink { key }),
    };

    let delivered: serde_json::Value = serde_json::from_str(&raw).map_err(|_| Error::Link {
        key: key.clone(),
        expected: kind.to_uppercase(),
    })?;

    match delivered.get(kind) {
        Some(properties) if properties.is_object() => Ok(properties.clone()),
        _ => Err(Error::WrongLinkType {
            key,
            carried: carried(&delivered),
            expected: kind.to_uppercase(),
        }),
    }
}

fn carried(delivered: &serde_json::Value) -> String {
    delivered
        .as_object()
        .into_iter()
        .flatten()
        .find(|(field, _)| *field != "name")
        .map(|(field, _)| field.to_uppercase())
        .unwrap_or_else(|| "UNSPECIFIED".to_string())
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
