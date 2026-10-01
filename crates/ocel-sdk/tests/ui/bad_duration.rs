#[ocel::task(max_duration = "5 minutes")]
async fn resize(_image: String, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

fn main() {}
