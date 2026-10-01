use super::Realtime;
use crate::proto::common::bindings::v1::RealtimeProperties;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine;
use serde::Serialize;
use serde_json::{json, Value};
use std::time::{SystemTime, UNIX_EPOCH};

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "kebab-case")]
pub(crate) enum Operation {
    Connect,
    Subscribe,
    Publish,
}

#[derive(Serialize)]
pub(crate) struct MintedToken {
    pub(crate) token: String,
    #[serde(rename = "expiresAt")]
    pub(crate) expires_at: u64,
}

fn write_canonical(value: &Value, out: &mut String) {
    match value {
        Value::Object(members) => {
            let mut keys: Vec<&String> = members.keys().collect();
            keys.sort();
            out.push('{');
            for (index, key) in keys.into_iter().enumerate() {
                if index > 0 {
                    out.push(',');
                }
                out.push_str(&Value::String(key.clone()).to_string());
                out.push(':');
                write_canonical(&members[key], out);
            }
            out.push('}');
        }
        Value::Array(items) => {
            out.push('[');
            for (index, item) in items.iter().enumerate() {
                if index > 0 {
                    out.push(',');
                }
                write_canonical(item, out);
            }
            out.push(']');
        }
        scalar => out.push_str(&scalar.to_string()),
    }
}

fn encode_canonical_json(value: &Value) -> String {
    let mut out = String::new();
    write_canonical(value, &mut out);
    out
}

pub(crate) fn sign_token(
    signing_key: &[u8],
    header: &Value,
    claims: &Value,
) -> Result<String, String> {
    let key = ring::signature::Ed25519KeyPair::from_seed_unchecked(signing_key).map_err(|_| {
        format!(
            "the realtime signing key is {} bytes, and an Ed25519 seed is 32",
            signing_key.len()
        )
    })?;
    let input = format!(
        "{}.{}",
        URL_SAFE_NO_PAD.encode(encode_canonical_json(header)),
        URL_SAFE_NO_PAD.encode(encode_canonical_json(claims))
    );
    let signature = key.sign(input.as_bytes());
    Ok(format!(
        "{input}.{}",
        URL_SAFE_NO_PAD.encode(signature.as_ref())
    ))
}

pub(crate) fn mint_token(
    rt: &Realtime,
    properties: &RealtimeProperties,
    subject: &str,
    operation: Operation,
    channel: &str,
) -> Result<MintedToken, String> {
    let namespace = &rt.inner.name;
    let issued_at = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs();
    let expires_at = issued_at + rt.inner.token_ttl.as_secs();
    let mut id = [0u8; 16];
    ring::rand::SecureRandom::fill(&ring::rand::SystemRandom::new(), &mut id)
        .map_err(|_| "the system gave no randomness for a token id".to_string())?;
    let token = sign_token(
        &properties.signing_key,
        &json!({ "alg": "EdDSA", "typ": "JWT" }),
        &json!({
            "iss": format!("ocel:rt:{namespace}"),
            "aud": properties.host,
            "sub": subject,
            "iat": issued_at,
            "exp": expires_at,
            "jti": URL_SAFE_NO_PAD.encode(id),
            "ocel": { "op": operation, "ch": channel, "ns": namespace },
        }),
    )?;
    Ok(MintedToken { token, expires_at })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::realtime::wire::tests::read_vectors;
    use base64::engine::general_purpose::STANDARD;

    #[test]
    fn every_mint_vector_signs_to_its_token_byte_for_byte() {
        let vectors = read_vectors();
        let signing_key = STANDARD
            .decode(vectors["tokens"]["signingKey"].as_str().unwrap())
            .unwrap();
        for vector in vectors["tokens"]["mint"].as_array().unwrap() {
            let token = sign_token(&signing_key, &vector["header"], &vector["claims"]).unwrap();
            assert_eq!(
                token,
                vector["token"].as_str().unwrap(),
                "{}",
                vector["name"]
            );
        }
    }

    #[test]
    fn line_and_paragraph_separators_stay_unescaped_as_json_stringify_writes_them() {
        let claims = json!({ "sub": "a\u{2028}b\u{2029}c", "aud": "x" });

        let token = sign_token(&[7u8; 32], &json!({}), &claims).unwrap();

        let encoded = token.split('.').nth(1).unwrap();
        assert_eq!(
            URL_SAFE_NO_PAD.decode(encoded).unwrap(),
            b"{\"aud\":\"x\",\"sub\":\"a\xe2\x80\xa8b\xe2\x80\xa9c\"}"
        );
    }
}
