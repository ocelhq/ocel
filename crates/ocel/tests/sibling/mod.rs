#[allow(dead_code)]
#[derive(ocel::Env)]
pub struct Worker {
    #[ocel(key = "SHARED")]
    shared: String,
}

pub const SHARED_LINE: &str = "5";
