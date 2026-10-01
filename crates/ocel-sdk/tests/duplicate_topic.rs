#[derive(serde::Serialize, serde::Deserialize)]
pub struct Order {
    pub id: u64,
}

#[allow(dead_code)]
#[derive(ocel::Resources)]
struct Infra {
    #[ocel(name = "orders")]
    orders: ocel::Topic<Order>,
    #[ocel(name = "orders")]
    bucket: ocel::Bucket,
    #[ocel(name = "orders")]
    worker: ocel::Worker,
}

#[ocel::task]
async fn orders(_order: Order, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

#[allow(dead_code)]
#[derive(ocel::Resources)]
struct Billing {
    invoices: ocel::Topic<Order>,
}

#[ocel::consumer(topic = Infra::ORDERS, name = "email")]
async fn email(_order: Order, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

#[ocel::consumer(topic = Billing::INVOICES, name = "email")]
async fn invoice_email(_order: Order, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

#[test]
fn a_task_may_not_share_a_topics_name_though_other_kinds_and_other_topics_consumers_may() {
    std::env::set_var("OCEL_PHASE", "discovery");

    let err = ocel::discover().expect_err("a topic and a task named alike");

    assert!(matches!(err, ocel::Error::Definition { .. }));
    let said = err.to_string();
    for want in [
        "'orders' is declared in ",
        "duplicate_topic.rs:10",
        "duplicate_topic.rs:18",
        "Topics and tasks share one namespace, and a name in it is declared exactly once, in exactly one file.",
    ] {
        assert!(said.contains(want), "error = {said}, want {want} in it");
    }
}
