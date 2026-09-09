#[derive(ocel::Env)]
struct Env {
    port: u16,
    #[ocel(key = "PORT")]
    other: u16,
}

fn main() {}
