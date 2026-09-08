mod collector;

use collector::{collector, Declared};
use std::sync::{Mutex, MutexGuard};

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
    assert_eq!(declared.request.resource.name, "main");
}

#[test]
fn a_main_returning_a_result_posts_what_it_declares_and_returns() {
    let declared = under_discovery(|| returns_result().expect("discovery"));
    assert_eq!(declared.request.resource.name, "main");
}

#[test]
fn an_async_main_under_the_attribute_posts_what_it_declares_and_returns() {
    let declared = under_discovery(ocel_outside_tokio);
    assert_eq!(declared.request.resource.name, "main");
}

#[test]
fn an_async_main_over_the_attribute_posts_what_it_declares_and_returns() {
    let declared = under_discovery(tokio_outside_ocel);
    assert_eq!(declared.request.resource.name, "main");
}

fn under_discovery(app: impl FnOnce()) -> Declared {
    let _phase: MutexGuard<'_, ()> = PHASE.lock().unwrap_or_else(|held| held.into_inner());
    let (url, requests) = collector(1);
    std::env::set_var("OCEL_PHASE", "discovery");
    std::env::set_var("OCEL_DEV_SERVER", &url);

    app();

    let declared = requests.recv().expect("a declaration");
    assert_eq!(declared.path, "/app.resources.v1.ResourceService/Declare");
    assert_eq!(declared.protocol.as_deref(), Some("1"));
    declared
}
