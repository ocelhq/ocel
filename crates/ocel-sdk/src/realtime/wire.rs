use super::DenialCode;
use std::collections::BTreeMap;

const MAX_VALUE_BYTES: usize = 30;
pub(crate) const SEGMENT_RULE: &str =
    "letters, digits and -, at most 50 characters, starting and ending with a letter or digit";

pub(crate) enum Segment<'a> {
    Literal(&'a str),
    Parameter(&'a str),
}

pub(crate) struct ChannelPattern<'a> {
    segments: Vec<Segment<'a>>,
}

pub(crate) fn is_channel_segment(text: &str) -> bool {
    let bytes = text.as_bytes();
    !bytes.is_empty()
        && bytes.len() <= 50
        && bytes
            .iter()
            .all(|b| b.is_ascii_alphanumeric() || *b == b'-')
        && bytes[0].is_ascii_alphanumeric()
        && bytes[bytes.len() - 1].is_ascii_alphanumeric()
}

impl<'a> ChannelPattern<'a> {
    pub(crate) fn split(declared: &'a str) -> Self {
        Self {
            segments: declared
                .split('/')
                .map(|part| match part.strip_prefix(':') {
                    Some(name) => Segment::Parameter(name),
                    None => Segment::Literal(part),
                })
                .collect(),
        }
    }

    fn has_parameter(&self, name: &str) -> bool {
        self.segments
            .iter()
            .any(|segment| matches!(segment, Segment::Parameter(seen) if *seen == name))
    }
}

const BASE32: &[u8; 32] = b"abcdefghijklmnopqrstuvwxyz234567";

fn encode_lower_base32(bytes: &[u8]) -> String {
    let mut out = String::new();
    let (mut buffer, mut bits) = (0u32, 0u32);
    for byte in bytes {
        buffer = (buffer << 8) | u32::from(*byte);
        bits += 8;
        while bits >= 5 {
            bits -= 5;
            out.push(BASE32[((buffer >> bits) & 31) as usize] as char);
        }
        buffer &= (1 << bits) - 1;
    }
    if bits > 0 {
        out.push(BASE32[((buffer << (5 - bits)) & 31) as usize] as char);
    }
    out
}

fn encode_value(value: &str) -> String {
    if is_channel_segment(value) && !value.starts_with("0z") {
        return value.to_string();
    }
    format!("0z{}", encode_lower_base32(value.as_bytes()))
}

pub(crate) fn encode_wire_channel(
    namespace: &str,
    pattern: &ChannelPattern<'_>,
    params: &BTreeMap<String, String>,
    wildcard: bool,
) -> Result<String, DenialCode> {
    if params.keys().any(|name| !pattern.has_parameter(name)) {
        return Err(DenialCode::UnknownParam);
    }
    let mut parts = vec![String::new(), namespace.to_string()];
    for (index, segment) in pattern.segments.iter().enumerate() {
        let name = match segment {
            Segment::Literal(literal) => {
                parts.push(literal.to_string());
                continue;
            }
            Segment::Parameter(name) => *name,
        };
        let Some(value) = params.get(name) else {
            let later = pattern.segments[index..].iter().any(
                |later| matches!(later, Segment::Parameter(later) if params.contains_key(*later)),
            );
            if !wildcard || later {
                return Err(DenialCode::MissingParam);
            }
            parts.push("*".to_string());
            return Ok(parts.join("/"));
        };
        if value.is_empty() {
            return Err(DenialCode::EmptyValue);
        }
        if value.len() > MAX_VALUE_BYTES {
            return Err(DenialCode::ValueTooLong);
        }
        parts.push(encode_value(value));
    }
    Ok(parts.join("/"))
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;

    pub(crate) fn read_vectors() -> serde_json::Value {
        let path = concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../../proto/realtime/vectors.json"
        );
        serde_json::from_str(&std::fs::read_to_string(path).expect("the shared realtime vectors"))
            .expect("the vectors are JSON")
    }

    #[test]
    fn every_wire_vector_encodes_to_its_channel_or_is_refused_for_its_reason() {
        for vector in read_vectors()["wire"].as_array().expect("wire vectors") {
            let pattern = ChannelPattern::split(vector["pattern"].as_str().unwrap());
            let params: BTreeMap<String, String> =
                serde_json::from_value(vector["params"].clone()).unwrap();
            let got = encode_wire_channel(
                vector["namespace"].as_str().unwrap(),
                &pattern,
                &params,
                vector["wildcard"].as_bool().unwrap(),
            )
            .map_err(|code| code.to_string());
            let want = match vector["error"].as_str() {
                Some(reason) => Err(reason.to_string()),
                None => Ok(vector["channel"].as_str().unwrap().to_string()),
            };
            assert_eq!(got, want, "{}", vector["name"]);
        }
    }
}
