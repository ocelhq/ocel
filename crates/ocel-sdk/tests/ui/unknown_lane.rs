#[derive(ocel::Resources)]
struct Infra {
    orders: ocel::Topic<String>,
}

#[ocel::consumer(topic = Infra::ORDERS, lanes = ["urgent"])]
async fn email(_order: String, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

fn main() {}
