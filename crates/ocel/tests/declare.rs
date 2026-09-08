use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener;
use std::sync::mpsc::{channel, Receiver};
use std::thread;

pub static DB: ocel::Postgres = ocel::postgres!("main");
pub static CACHE: ocel::Postgres = ocel::postgres!("cache", version = "16");

#[test]
fn discovery_posts_a_declaration_carrying_an_absolute_source() {
    let (url, requests) = collector(2);
    let workspace = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .expect("the workspace above the crate");
    std::env::set_var("OCEL_PHASE", "discovery");
    std::env::set_var("OCEL_SOURCE_ROOT", workspace);
    std::env::set_var("OCEL_DEV_SERVER", &url);

    assert!(
        ocel::discover().expect("discover"),
        "discover ran the app on"
    );

    let mut declared = Vec::new();
    for _ in 0..2 {
        let (path, body) = requests.recv().expect("a declaration");
        assert_eq!(path, "/app.resources.v1.ResourceService/Declare");
        declared.push(serde_json::from_str::<serde_json::Value>(&body).expect("the body is json"));
    }
    let sent = declaration(&declared, "main");
    let cache = declaration(&declared, "cache");
    assert_eq!(cache["postgres"]["version"], "16");
    assert_eq!(CACHE.name(), "cache");
    assert_eq!(sent["resource"]["name"], "main");
    assert_eq!(sent["resource"]["type"], "LINK_TYPE_POSTGRES");
    assert_eq!(sent["postgres"]["version"], "17");

    let source = sent["source"].as_str().expect("a source");
    let (file, line) = source.rsplit_once(':').expect("a file and a line");
    assert!(
        std::path::Path::new(file).is_absolute(),
        "source = {source}, want an absolute path"
    );
    assert_eq!(
        std::path::Path::new(file),
        workspace.join("ocel/tests/declare.rs"),
        "source = {source}, want the file the declaration is written in"
    );
    assert_eq!(line, "6");
    assert_eq!(DB.name(), "main");
}

fn declaration<'a>(declared: &'a [serde_json::Value], name: &str) -> &'a serde_json::Value {
    declared
        .iter()
        .find(|sent| sent["resource"]["name"] == name)
        .unwrap_or_else(|| panic!("no declaration named {name} among {declared:?}"))
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
