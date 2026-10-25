#![allow(dead_code)]

use crate::runtime::{runtime, Runtime};
use bytes::Bytes;
use ocel::proto::app::realtime::v1::{
    Grant, MintRequest, MintResponse, MintedToken, Operation, PublishRequest, PublishResponse,
};
use serde_json::Value;

pub const MINTED_EXPIRES_AT: i64 = 1_790_000_030;

pub fn read_fixture() -> Value {
    let path = concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../proto/common/bindings/v1/fixtures/realtime.json"
    );
    let mut fixture: Value =
        serde_json::from_str(&std::fs::read_to_string(path).expect("the realtime binding fixture"))
            .expect("the fixture is JSON");
    let properties = fixture["realtime"]
        .as_object_mut()
        .expect("realtime properties");
    properties.remove("signingKey");
    properties.remove("verifyKey");
    fixture
}

fn name_operation(grant: &Grant) -> &'static str {
    match grant.operation.as_known() {
        Some(Operation::OPERATION_CONNECT) => "connect",
        Some(Operation::OPERATION_SUBSCRIBE) => "subscribe",
        _ => "unspecified",
    }
}

pub fn fake_token(operation: &str, channel: &str) -> String {
    format!("token {operation} {channel}")
}

pub fn grant(operation: &str, channel: &str) -> (String, String) {
    (operation.to_string(), channel.to_string())
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
        let fake = Self { runtime };
        fake.answer_mint(|channel| channel.to_string());
        fake
    }

    pub fn map_channels(&self, map: fn(&str) -> String) {
        self.answer_mint(map);
    }

    fn answer_mint(&self, map: fn(&str) -> String) {
        self.runtime
            .answer_with("Mint", move |request: MintRequest| MintResponse {
                tokens: request
                    .grants
                    .iter()
                    .map(|grant| MintedToken {
                        token: fake_token(name_operation(grant), &grant.channel),
                        channel: map(&grant.channel),
                        ..Default::default()
                    })
                    .collect(),
                expires_at: buffa_types::google::protobuf::Timestamp {
                    seconds: MINTED_EXPIRES_AT,
                    ..Default::default()
                }
                .into(),
                ..Default::default()
            });
    }

    pub fn refuse_mint(&self, said: &str) {
        self.runtime
            .refuse("Mint", 400, "failed_precondition", said);
    }

    pub fn mints(&self) -> Vec<MintRequest> {
        self.runtime
            .calls()
            .iter()
            .filter(|call| call.path == "/app.realtime.v1.RealtimeService/Mint")
            .map(|call| call.decode())
            .collect()
    }

    pub fn assert_minted(&self, subject: &str, ttl_seconds: i64, grants: &[(String, String)]) {
        let mints = self.mints();
        assert_eq!(
            mints.len(),
            1,
            "the runtime was asked to mint once for the whole batch"
        );
        let mint = &mints[0];
        assert_eq!(mint.realtime, "app");
        assert_eq!(mint.subject, subject);
        assert_eq!(
            mint.ttl.as_option().map(|ttl| ttl.seconds),
            Some(ttl_seconds)
        );
        let asked: Vec<(String, String)> = mint
            .grants
            .iter()
            .map(|grant| (name_operation(grant).to_string(), grant.channel.clone()))
            .collect();
        assert_eq!(asked, grants);
    }

    pub fn binding(&self) -> String {
        let mut binding = read_fixture();
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
