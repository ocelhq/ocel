#[derive(ocel::Resources)]
struct Infra {
    orders: ocel::Topic<String>,
}

#[ocel::batch_consumer(topic = Infra::ORDERS, batch_timeout = "1s")]
async fn email(_orders: Vec<String>, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

fn main() {}
