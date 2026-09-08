use std::io::Write;
use std::net::TcpListener;

#[path = "../infra/mod.rs"]
mod infra;

const DEFAULT_PORT: &str = "3105";

#[ocel::main]
fn main() {
    let port = std::env::var("PORT").unwrap_or_else(|_| DEFAULT_PORT.to_string());
    let listener = TcpListener::bind(format!("0.0.0.0:{port}")).expect("bind");
    println!("rust listening on http://localhost:{port}");

    for stream in listener.incoming() {
        let mut stream = stream.expect("accept");
        let body = format!("{{\"ok\":true,\"database\":\"{}\"}}", infra::DB.name());
        let response = format!(
            "HTTP/1.1 200 OK\r\ncontent-type: application/json\r\ncontent-length: {}\r\n\r\n{body}",
            body.len()
        );
        let _ = stream.write_all(response.as_bytes());
    }
}
