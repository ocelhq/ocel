#[derive(ocel::Resources)]
struct Infra {
    #[ocel(retry(attempts = 3))]
    orders: ocel::Topic<String>,
}

fn main() {}
