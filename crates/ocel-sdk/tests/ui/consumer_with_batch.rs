#[derive(ocel::Resources)]
struct Infra {
    orders: ocel::Topic<String>,
}

#[ocel::consumer(topic = Infra::ORDERS, batch(size = 10))]
async fn email(_orders: Vec<String>, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

fn main() {}
