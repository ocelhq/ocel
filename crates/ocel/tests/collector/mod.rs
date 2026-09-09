#![allow(dead_code)]

use buffa::Message;
use ocel::proto::app::resources::v1::{
    DeclareEnvRequest, DeclareEnvResponse, DeclareRequest, ReportEnvProblemsRequest, VariableCell,
};
use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::mpsc::{channel, Receiver};
use std::thread;

pub const DECLARE: &str = "/app.resources.v1.ResourceService/Declare";
pub const DECLARE_ENV: &str = "/app.resources.v1.ResourceService/DeclareEnv";
pub const REPORT_ENV_PROBLEMS: &str = "/app.resources.v1.ResourceService/ReportEnvProblems";

pub struct Received {
    pub path: String,
    pub protocol: Option<String>,
    json: bool,
    body: Vec<u8>,
}

impl Received {
    pub fn declare(&self) -> DeclareRequest {
        self.decode()
    }

    pub fn declare_env(&self) -> DeclareEnvRequest {
        self.decode()
    }

    pub fn problems(&self) -> ReportEnvProblemsRequest {
        self.decode()
    }

    fn decode<T>(&self) -> T
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

pub fn collector(requests: usize) -> (String, Receiver<Received>) {
    holding(requests, Vec::new())
}

pub fn holding(requests: usize, cells: Vec<VariableCell>) -> (String, Receiver<Received>) {
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
    let url = format!("http://{}", listener.local_addr().expect("addr"));
    let (sender, receiver) = channel();
    thread::spawn(move || {
        for _ in 0..requests {
            let (stream, _) = listener.accept().expect("accept");
            sender.send(serve(stream, &cells)).expect("send");
        }
    });
    (url, receiver)
}

fn serve(mut stream: TcpStream, cells: &[VariableCell]) -> Received {
    let mut reader = BufReader::new(stream.try_clone().expect("clone"));
    let mut request = String::new();
    reader.read_line(&mut request).expect("request line");
    let path = request.split_whitespace().nth(1).unwrap_or("").to_string();

    let mut length = 0usize;
    let mut content_type = String::new();
    let mut protocol = None;
    loop {
        let mut header = String::new();
        reader.read_line(&mut header).expect("header");
        if header.trim().is_empty() {
            break;
        }
        let lowered = header.to_ascii_lowercase();
        if let Some(value) = lowered.strip_prefix("content-length:") {
            length = value.trim().parse().expect("content length");
        }
        if let Some(value) = lowered.strip_prefix("content-type:") {
            content_type = value.trim().to_string();
        }
        if let Some(value) = lowered.strip_prefix("connect-protocol-version:") {
            protocol = Some(value.trim().to_string());
        }
    }

    let mut body = vec![0u8; length];
    reader.read_exact(&mut body).expect("body");

    let json = content_type.ends_with("json");
    let response = if path == DECLARE_ENV {
        encoded(json, cells)
    } else {
        Vec::new()
    };
    stream
        .write_all(
            format!(
                "HTTP/1.1 200 OK\r\ncontent-type: {content_type}\r\ncontent-length: {}\r\nconnection: close\r\n\r\n",
                response.len()
            )
            .as_bytes(),
        )
        .expect("respond");
    stream.write_all(&response).expect("respond with a body");

    Received {
        path,
        protocol,
        json,
        body,
    }
}

fn encoded(json: bool, cells: &[VariableCell]) -> Vec<u8> {
    let response = DeclareEnvResponse {
        cells: cells.to_vec(),
        ..Default::default()
    };
    if json {
        return serde_json::to_vec(&response).expect("a DeclareEnvResponse in json");
    }
    response.encode_to_vec()
}

pub fn cell(key: &str, folder: &str, value: &str) -> VariableCell {
    VariableCell {
        key: key.to_string(),
        folder: folder.to_string(),
        value: value.to_string(),
        ..Default::default()
    }
}
