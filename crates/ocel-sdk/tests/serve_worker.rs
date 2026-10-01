use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::Mutex;
use std::time::{Duration, Instant};

static HEARD: Mutex<Vec<String>> = Mutex::new(Vec::new());

#[derive(ocel::Resources)]
#[allow(dead_code)]
struct Infra {
    words: ocel::Topic<String>,
}

#[ocel::consumer(topic = Infra::WORDS)]
async fn listen(word: String, run: &ocel::Run) -> Result<(), ocel::RunError> {
    let kind = match run.kind() {
        ocel::RunKind::Task => "task",
        ocel::RunKind::Consumer => "consumer",
    };
    HEARD
        .lock()
        .expect("the heard words")
        .push(format!("{kind} {} {word}", run.name()));
    Ok(())
}

#[ocel::task]
async fn shout(word: String, _run: &ocel::Run) -> Result<String, ocel::RunError> {
    Ok(word.to_uppercase())
}

fn find_free_port() -> u16 {
    TcpListener::bind("127.0.0.1:0")
        .expect("a free port")
        .local_addr()
        .expect("its address")
        .port()
}

fn connect(port: u16) -> TcpStream {
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        match TcpStream::connect(("127.0.0.1", port)) {
            Ok(stream) => return stream,
            Err(err) if Instant::now() > deadline => panic!("the worker never listened: {err}"),
            Err(_) => std::thread::sleep(Duration::from_millis(20)),
        }
    }
}

fn post(port: u16, body: &str) -> String {
    let mut stream = connect(port);
    write!(
        stream,
        "POST / HTTP/1.1\r\nhost: 127.0.0.1\r\ncontent-type: application/json\r\ncontent-length: {}\r\nconnection: close\r\n\r\n{body}",
        body.len()
    )
    .expect("send the delivery");
    let mut answer = String::new();
    stream.read_to_string(&mut answer).expect("read the answer");
    answer
}

#[test]
fn a_process_whose_environment_names_a_worker_serves_its_deliveries_over_http() {
    let port = find_free_port();
    std::env::set_var("OCEL_WORKER", "worker");
    std::env::set_var("PORT", port.to_string());
    std::thread::spawn(|| ocel::serve_worker().expect("the worker serves"));

    let answer = post(
        port,
        r#"{"v":1,"topic":"shout","consumer":"shout","execution":"run_1","payload":"hey"}"#,
    );

    assert!(answer.starts_with("HTTP/1.1 200"), "{answer}");
    assert!(answer.ends_with("\"HEY\""), "{answer}");

    let answer = post(
        port,
        r#"{"v":1,"topic":"words","consumer":"listen","execution":"msg_1-listen","payload":"hey"}"#,
    );

    assert!(answer.starts_with("HTTP/1.1 200"), "{answer}");
    assert_eq!(
        *HEARD.lock().expect("the heard words"),
        vec!["consumer listen hey".to_string()]
    );
}
