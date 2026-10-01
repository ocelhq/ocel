#[derive(ocel::Resources)]
struct Infra {
    orders: ocel::Topic<String>,
}

#[ocel::consumer(topic = Infra::ORDERS)]
async fn email(_order: u64, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

#[ocel::batch_consumer(topic = Infra::ORDERS, batch_size = 10)]
async fn digest(_orders: Vec<u64>, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

fn main() {}
