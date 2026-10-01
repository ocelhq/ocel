#[ocel::consumer(topic = "orders")]
async fn email(_order: String, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

fn main() {}
