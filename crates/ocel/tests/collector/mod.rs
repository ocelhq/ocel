use ocel::proto::app::resources::v1::DeclareRequest;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::mpsc::{channel, Receiver};
use std::thread;

pub struct Declared {
    pub path: String,
    pub protocol: Option<String>,
    pub request: DeclareRequest,
}

pub fn collector(requests: usize) -> (String, Receiver<Declared>) {
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
    let url = format!("http://{}", listener.local_addr().expect("addr"));
    let (sender, receiver) = channel();
    thread::spawn(move || {
        for _ in 0..requests {
            let (stream, _) = listener.accept().expect("accept");
            sender.send(serve(stream)).expect("send");
        }
    });
    (url, receiver)
}

fn serve(mut stream: TcpStream) -> Declared {
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
    stream
        .write_all(
            format!(
                "HTTP/1.1 200 OK\r\ncontent-type: {content_type}\r\ncontent-length: 0\r\nconnection: close\r\n\r\n"
            )
            .as_bytes(),
        )
        .expect("respond");
    Declared {
        path,
        protocol,
        request: decode(&content_type, &body),
    }
}

fn decode(content_type: &str, body: &[u8]) -> DeclareRequest {
    if content_type.ends_with("json") {
        return serde_json::from_slice(body).expect("a DeclareRequest in json");
    }
    buffa::DecodeOptions::new()
        .decode_from_slice(body)
        .expect("a DeclareRequest in binary proto")
}
