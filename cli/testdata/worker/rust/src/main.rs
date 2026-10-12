use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::Mutex;

static STARTS: AtomicU32 = AtomicU32::new(0);
static WRAPPED_BY: Mutex<String> = Mutex::new(String::new());
static AUDITED: Mutex<String> = Mutex::new(String::new());

#[derive(Clone, serde::Deserialize)]
struct Greeting {
    name: String,
}

async fn count_start() -> Result<(), ocel::RunError> {
    STARTS.fetch_add(1, Ordering::SeqCst);
    Ok(())
}

async fn record_wrapper(run: &ocel::Run, next: ocel::Next<'_>) -> Result<(), ocel::RunError> {
    let kind = match run.kind() {
        ocel::RunKind::Task => "task",
        ocel::RunKind::Consumer => "consumer",
    };
    *WRAPPED_BY.lock().expect("the wrapper record") = format!("{kind}:{}", run.name());
    next.await
}

#[derive(ocel::Resources)]
#[allow(dead_code)]
struct Infra {
    #[ocel(name = "worker", on_start = count_start, middleware = record_wrapper)]
    background: ocel::Worker,
    orders: ocel::Topic<Greeting>,
}

#[ocel::task(worker = "worker")]
async fn greet(payload: Greeting, _run: &ocel::Run) -> Result<serde_json::Value, ocel::RunError> {
    Ok(serde_json::json!({
        "greeting": format!("hello {}", payload.name),
        "starts": STARTS.load(Ordering::SeqCst),
        "wrappedBy": WRAPPED_BY.lock().expect("the wrapper record").clone(),
        "audited": AUDITED.lock().expect("the audit record").clone(),
    }))
}

#[ocel::consumer(topic = Infra::ORDERS, worker = "worker")]
async fn audit(payload: Greeting, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    let wrapped = WRAPPED_BY.lock().expect("the wrapper record").clone();
    *AUDITED.lock().expect("the audit record") = format!("{wrapped} {}", payload.name);
    Ok(())
}

#[ocel::main]
fn main() {
    panic!("the worker role entered the body of main");
}
