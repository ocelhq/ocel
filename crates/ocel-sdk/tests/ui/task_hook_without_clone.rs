#[derive(serde::Deserialize, serde::Serialize)]
struct Image {
    url: String,
}

async fn notify(_image: &Image, _output: &(), _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

#[ocel::task(on_success = notify)]
async fn resize(_image: Image, _run: &ocel::Run) -> Result<(), ocel::RunError> {
    Ok(())
}

fn main() {}
