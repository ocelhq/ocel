#[ocel::task(schema)]
async fn resize(_image: String, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

fn main() {}
