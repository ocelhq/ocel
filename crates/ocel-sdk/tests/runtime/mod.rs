#![allow(dead_code)]

use buffa::Message;
use std::collections::HashMap;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::{Arc, Mutex, MutexGuard, OnceLock};

pub const TOKEN: &str = "opensesame";

type Encode = Box<dyn Fn(bool) -> Vec<u8> + Send>;

pub struct Call {
    pub path: String,
    pub authorization: Option<String>,
    json: bool,
    body: Vec<u8>,
}

impl Call {
    pub fn decode<T>(&self) -> T
    where
        T: Message + serde::de::DeserializeOwned,
    {
        if self.json {
            return serde_json::from_slice(&self.body).expect("a request in json");
        }
        buffa::DecodeOptions::new()
            .decode_from_slice(&self.body)
            .expect("a request in binary proto")
    }
}

enum Answer {
    Message(Encode),
    Refusal(u16, String, String),
}

#[derive(Default)]
struct Shared {
    calls: Mutex<Vec<Call>>,
    answers: Mutex<HashMap<String, Answer>>,
}

pub struct Runtime {
    pub address: String,
    shared: Arc<Shared>,
    _turn: MutexGuard<'static, ()>,
}

impl Runtime {
    pub fn answer<M>(&self, method: &str, message: M)
    where
        M: Message + serde::Serialize + Send + 'static,
    {
        self.shared.answers.lock().expect("the answers").insert(
            method.to_string(),
            Answer::Message(Box::new(move |json| match json {
                true => serde_json::to_vec(&message).expect("a response in json"),
                false => message.encode_to_vec(),
            })),
        );
    }

    pub fn refuse(&self, method: &str, status: u16, code: &str, message: &str) {
        self.shared.answers.lock().expect("the answers").insert(
            method.to_string(),
            Answer::Refusal(status, code.to_string(), message.to_string()),
        );
    }

    pub fn calls(&self) -> MutexGuard<'_, Vec<Call>> {
        self.shared.calls.lock().expect("the calls")
    }

    pub fn only(&self, method: &str) -> Vec<Call> {
        let mut calls = self.calls();
        let taken = std::mem::take(&mut *calls);
        let (matching, rest): (Vec<Call>, Vec<Call>) = taken
            .into_iter()
            .partition(|call| call.path.ends_with(&format!("/{method}")));
        *calls = rest;
        matching
    }
}

pub fn runtime() -> Runtime {
    static TURN: Mutex<()> = Mutex::new(());
    static SERVED: OnceLock<(String, Arc<Shared>)> = OnceLock::new();
    let turn = TURN.lock().unwrap_or_else(|poisoned| poisoned.into_inner());
    let (address, shared) = SERVED.get_or_init(|| {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
        let address = format!("http://{}", listener.local_addr().expect("addr"));
        let shared = Arc::new(Shared::default());
        let served = shared.clone();
        std::thread::spawn(move || {
            for stream in listener.incoming() {
                let Ok(stream) = stream else { return };
                let shared = served.clone();
                std::thread::spawn(move || serve(stream, &shared));
            }
        });
        (address, shared)
    });
    shared.calls.lock().expect("the calls").clear();
    shared.answers.lock().expect("the answers").clear();
    std::env::remove_var("OCEL_PHASE");
    std::env::set_var("OCEL_RUNTIME_ADDRESS", address);
    std::env::set_var("OCEL_SESSION_TOKEN", TOKEN);
    Runtime {
        address: address.clone(),
        shared: shared.clone(),
        _turn: turn,
    }
}

fn serve(stream: TcpStream, shared: &Shared) {
    let mut reader = BufReader::new(stream.try_clone().expect("clone"));
    loop {
        let mut line = String::new();
        if reader.read_line(&mut line).unwrap_or(0) == 0 {
            return;
        }
        let path = line.split_whitespace().nth(1).unwrap_or("").to_string();
        let mut length = 0usize;
        let mut content_type = String::new();
        let mut authorization = None;
        loop {
            let mut header = String::new();
            if reader.read_line(&mut header).unwrap_or(0) == 0 {
                return;
            }
            if header.trim().is_empty() {
                break;
            }
            let Some((name, value)) = header.split_once(':') else {
                continue;
            };
            match name.trim().to_ascii_lowercase().as_str() {
                "content-length" => length = value.trim().parse().unwrap_or_default(),
                "content-type" => content_type = value.trim().to_string(),
                "authorization" => authorization = Some(value.trim().to_string()),
                _ => {}
            }
        }
        let mut body = vec![0u8; length];
        if reader.read_exact(&mut body).is_err() {
            return;
        }
        let json = content_type.ends_with("json");
        let method = path.rsplit('/').next().unwrap_or_default().to_string();
        shared.calls.lock().expect("the calls").push(Call {
            path,
            authorization,
            json,
            body,
        });
        let (status, kind, response) =
            match shared.answers.lock().expect("the answers").get(&method) {
                Some(Answer::Message(encode)) => (200, content_type.clone(), encode(json)),
                Some(Answer::Refusal(status, code, message)) => (
                    *status,
                    "application/json".to_string(),
                    serde_json::to_vec(&serde_json::json!({ "code": code, "message": message }))
                        .expect("an error body"),
                ),
                None => (
                    501,
                    "application/json".to_string(),
                    br#"{"code":"unimplemented","message":"the fake runtime has no answer"}"#
                        .to_vec(),
                ),
            };
        let mut writer = &stream;
        let head = format!(
            "HTTP/1.1 {status} OK\r\ncontent-type: {kind}\r\ncontent-length: {}\r\n\r\n",
            response.len()
        );
        if writer.write_all(head.as_bytes()).is_err() || writer.write_all(&response).is_err() {
            return;
        }
    }
}
