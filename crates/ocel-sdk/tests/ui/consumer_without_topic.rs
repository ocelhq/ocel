#[ocel::consumer(name = "email")]
async fn email(_order: String, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

fn main() {}
