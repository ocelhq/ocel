use std::io::Write;
use std::net::TcpListener;

#[derive(ocel::Resources)]
struct Infra {
    #[ocel(name = "main")]
    db: ocel::Postgres,
}

#[derive(ocel::Env)]
struct Env {
    #[ocel(default = "hello")]
    greeting: String,
}

const DEFAULT_PORT: &str = "3105";

#[ocel::main]
fn main() {
    let infra = Infra::load().expect("the resources this app declares");
    let env = Env::load().expect("the environment this app declares");

    let port = std::env::var("PORT").unwrap_or_else(|_| DEFAULT_PORT.to_string());
    let listener = TcpListener::bind(format!("0.0.0.0:{port}")).expect("bind");
    println!("rust listening on http://localhost:{port}");

    for stream in listener.incoming() {
        let mut stream = stream.expect("accept");
        let body = format!(
            "{{\"ok\":true,\"database\":\"{}\",\"greeting\":\"{}\"}}",
            infra.db.name(),
            env.greeting
        );
        let response = format!(
            "HTTP/1.1 200 OK\r\ncontent-type: application/json\r\ncontent-length: {}\r\n\r\n{body}",
            body.len()
        );
        let _ = stream.write_all(response.as_bytes());
    }
}
