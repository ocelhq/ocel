#![allow(dead_code)]

use base64::engine::general_purpose::{STANDARD, URL_SAFE_NO_PAD};
use base64::Engine;
use bytes::Bytes;
use serde_json::Value;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener;
use std::sync::{Arc, Mutex};
use std::thread;

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
    pub path: String,
    pub authorization: String,
    pub envelope: Value,
}

pub struct FakeGateway {
    pub host: String,
    pub published: Arc<Mutex<Vec<Published>>>,
    pub status: Arc<Mutex<u16>>,
}

impl FakeGateway {
    pub fn serve() -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
        let host = listener.local_addr().expect("addr").to_string();
        let published = Arc::new(Mutex::new(Vec::new()));
        let status = Arc::new(Mutex::new(202u16));
        let (seen, answer) = (published.clone(), status.clone());
        thread::spawn(move || {
            for stream in listener.incoming() {
                let mut stream = stream.expect("accept");
                let mut reader = BufReader::new(stream.try_clone().expect("clone"));
                let mut line = String::new();
                reader.read_line(&mut line).expect("request line");
                let path = line.split_whitespace().nth(1).unwrap_or("").to_string();
                let (mut length, mut authorization) = (0usize, String::new());
                loop {
                    let mut header = String::new();
                    reader.read_line(&mut header).expect("header");
                    let header = header.trim_end();
                    if header.is_empty() {
                        break;
                    }
                    let (name, value) = header.split_once(':').unwrap_or_default();
                    match name.to_ascii_lowercase().as_str() {
                        "content-length" => length = value.trim().parse().unwrap_or_default(),
                        "authorization" => authorization = value.trim().to_string(),
                        _ => {}
                    }
                }
                let mut body = vec![0u8; length];
                reader.read_exact(&mut body).expect("body");
                seen.lock().unwrap().push(Published {
                    path,
                    authorization,
                    envelope: serde_json::from_slice(&body).expect("an envelope"),
                });
                let status = *answer.lock().unwrap();
                write!(
                    stream,
                    "HTTP/1.1 {status} OK\r\ncontent-length: 0\r\nconnection: close\r\n\r\n"
                )
                .expect("respond");
            }
        });
        Self {
            host,
            published,
            status,
        }
    }

    pub fn binding(&self) -> String {
        let mut binding = read_fixture();
        binding["realtime"]["transport"] = "REALTIME_TRANSPORT_OCEL_GATEWAY".into();
        binding["realtime"]["host"] = self.host.clone().into();
        binding["realtime"]["url"] = format!("ws://{}/realtime", self.host).into();
        binding.to_string()
    }
}

pub struct StalledGateway {
    pub host: String,
}

impl StalledGateway {
    pub fn serve() -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
        let host = listener.local_addr().expect("addr").to_string();
        thread::spawn(move || {
            let mut held = Vec::new();
            for stream in listener.incoming() {
                held.push(stream.expect("accept"));
            }
        });
        Self { host }
    }

    pub fn binding(&self) -> String {
        let mut binding = read_fixture();
        binding["realtime"]["transport"] = "REALTIME_TRANSPORT_OCEL_GATEWAY".into();
        binding["realtime"]["host"] = self.host.clone().into();
        binding["realtime"]["url"] = format!("ws://{}/realtime", self.host).into();
        binding.to_string()
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
