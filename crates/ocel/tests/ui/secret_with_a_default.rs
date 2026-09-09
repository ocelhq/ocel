#[derive(ocel::Env)]
struct Env {
    #[ocel(default = "x")]
    token: ocel::Secret,
}

fn main() {}
