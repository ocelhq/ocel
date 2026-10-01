#[ocel::task(batch(size = 10))]
async fn resize(_image: String, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

fn main() {}
