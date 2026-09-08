use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener;
use std::sync::mpsc::{channel, Receiver};
use std::sync::{Mutex, MutexGuard};
use std::thread;

pub static DB: ocel::Postgres = ocel::postgres!("main");

static PHASE: Mutex<()> = Mutex::new(());

#[ocel::main]
fn returns_unit() {
    panic!("the app ran under discovery");
}

#[ocel::main]
fn returns_result() -> Result<(), ocel::Error> {
    panic!("the app ran under discovery");
}

#[ocel::main]
#[tokio::main(flavor = "current_thread")]
async fn ocel_outside_tokio() {
    panic!("the app ran under discovery");
}

#[tokio::main(flavor = "current_thread")]
#[ocel::main]
async fn tokio_outside_ocel() {
    panic!("the app ran under discovery");
}

#[test]
fn a_sync_main_posts_what_it_declares_and_returns() {
    let declared = under_discovery(returns_unit);
    assert_eq!(declared["resource"]["name"], "main");
}

#[test]
fn a_main_returning_a_result_posts_what_it_declares_and_returns() {
    let declared = under_discovery(|| returns_result().expect("discovery"));
    assert_eq!(declared["resource"]["name"], "main");
}

#[test]
fn an_async_main_under_the_attribute_posts_what_it_declares_and_returns() {
    let declared = under_discovery(ocel_outside_tokio);
    assert_eq!(declared["resource"]["name"], "main");
}

#[test]
fn an_async_main_over_the_attribute_posts_what_it_declares_and_returns() {
    let declared = under_discovery(tokio_outside_ocel);
    assert_eq!(declared["resource"]["name"], "main");
}

fn under_discovery(app: impl FnOnce()) -> serde_json::Value {
    let _phase: MutexGuard<'_, ()> = PHASE.lock().unwrap_or_else(|held| held.into_inner());
    let (url, requests) = collector(1);
    std::env::set_var("OCEL_PHASE", "discovery");
    std::env::set_var("OCEL_DEV_SERVER", &url);

    app();

    let (path, body) = requests.recv().expect("a declaration");
    assert_eq!(path, "/app.resources.v1.ResourceService/Declare");
    serde_json::from_str(&body).expect("the body is json")
}

fn collector(requests: usize) -> (String, Receiver<(String, String)>) {
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

fn serve(mut stream: std::net::TcpStream) -> (String, String) {
    let mut reader = BufReader::new(stream.try_clone().expect("clone"));
    let mut request = String::new();
    reader.read_line(&mut request).expect("request line");
    let path = request.split_whitespace().nth(1).unwrap_or("").to_string();

    let mut length = 0usize;
    loop {
        let mut header = String::new();
        reader.read_line(&mut header).expect("header");
        if header.trim().is_empty() {
            break;
        }
        if let Some(value) = header.to_ascii_lowercase().strip_prefix("content-length:") {
            length = value.trim().parse().expect("content length");
        }
    }

    let mut body = vec![0u8; length];
    reader.read_exact(&mut body).expect("body");
    stream
        .write_all(b"HTTP/1.1 200 OK\r\ncontent-type: application/json\r\ncontent-length: 2\r\nconnection: close\r\n\r\n{}")
        .expect("respond");
    (path, String::from_utf8(body).expect("utf8 body"))
}
