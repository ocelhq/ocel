#[ocel::task(batch(timeout = "1s"))]
async fn resize(_images: Vec<String>, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

fn main() {}
