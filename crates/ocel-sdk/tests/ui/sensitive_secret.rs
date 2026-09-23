#[derive(ocel::Env)]
struct Env {
    #[ocel(sensitive)]
    token: ocel::Secret,
}

fn main() {}
