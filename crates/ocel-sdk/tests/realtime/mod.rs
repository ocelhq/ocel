#![allow(dead_code)]

use crate::runtime::{runtime, Runtime};
use base64::engine::general_purpose::{STANDARD, URL_SAFE_NO_PAD};
use base64::Engine;
use bytes::Bytes;
use ocel::proto::app::realtime::v1::{PublishRequest, PublishResponse};
use serde_json::Value;

pub fn read_fixture() -> Value {
    let path = concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../proto/common/bindings/v1/fixtures/realtime.json"
    );
    serde_json::from_str(&std::fs::read_to_string(path).expect("the realtime binding fixture"))
        .expect("the fixture is JSON")
}

pub fn read_claims(token: &str) -> Value {
    let parts: Vec<&str> = token.split('.').collect();
    assert_eq!(parts.len(), 3, "token {token} is no JWT");
    let verify_key = STANDARD
        .decode(read_fixture()["realtime"]["verifyKey"].as_str().unwrap())
        .unwrap();
    ring::signature::UnparsedPublicKey::new(&ring::signature::ED25519, verify_key)
        .verify(
            format!("{}.{}", parts[0], parts[1]).as_bytes(),
            &URL_SAFE_NO_PAD.decode(parts[2]).unwrap(),
        )
        .expect("the token verifies against the fixture's verify key");
    serde_json::from_slice(&URL_SAFE_NO_PAD.decode(parts[1]).unwrap()).unwrap()
}

pub struct Published {
    pub request: PublishRequest,
    pub envelope: Value,
}

pub struct FakeRuntime {
    runtime: Runtime,
}

impl FakeRuntime {
    pub fn serve() -> Self {
        let runtime = runtime();
        runtime.answer("Publish", PublishResponse::default());
        Self { runtime }
    }

    pub fn binding(&self) -> String {
        let mut binding = read_fixture();
        binding["realtime"]["transport"] = "REALTIME_TRANSPORT_OCEL_GATEWAY".into();
        binding["realtime"]["host"] = "realtime.shop.example".into();
        binding["realtime"]["url"] = "wss://realtime.shop.example/event/realtime".into();
        binding.to_string()
    }

    pub fn refuse(&self, said: &str) {
        self.runtime.refuse("Publish", 503, "unavailable", said);
    }

    pub fn published(&self) -> Vec<Published> {
        self.runtime
            .calls()
            .iter()
            .filter(|call| call.path == "/app.realtime.v1.RealtimeService/Publish")
            .map(|call| {
                let request: PublishRequest = call.decode();
                let envelope = serde_json::from_str(&request.event).expect("an envelope");
                Published { request, envelope }
            })
            .collect()
    }

    pub fn authorizations(&self) -> Vec<Option<String>> {
        self.runtime
            .calls()
            .iter()
            .map(|call| call.authorization.clone())
            .collect()
    }
}

pub struct Answered {
    pub status: u16,
    pub headers: http::HeaderMap,
    pub body: Value,
}

pub async fn call(
    rt: &ocel::realtime::Realtime,
    method: &str,
    headers: &[(&str, &str)],
    body: &str,
) -> Answered {
    let mut map = http::HeaderMap::new();
    map.insert("host", "shop.example".parse().unwrap());
    map.insert("content-type", "application/json".parse().unwrap());
    for (name, value) in headers {
        map.insert(
            http::HeaderName::from_bytes(name.as_bytes()).unwrap(),
            value.parse().unwrap(),
        );
    }
    let response = rt
        .handle(ocel::realtime::Request {
            method: method.parse().unwrap(),
            uri: "/api/realtime".parse().unwrap(),
            headers: map,
            body: Bytes::from(body.to_string()),
        })
        .await;
    let (parts, body) = response.into_parts();
    Answered {
        status: parts.status.as_u16(),
        headers: parts.headers,
        body: serde_json::from_slice(&body).unwrap_or(Value::Null),
    }
}

pub async fn post(rt: &ocel::realtime::Realtime, batch: Value, headers: &[(&str, &str)]) -> Value {
    let answered = call(rt, "POST", headers, &batch.to_string()).await;
    assert_eq!(answered.status, 200, "{}", answered.body);
    answered.body
}
