#[derive(ocel::Resources)]
struct Infra {
    #[ocel(public)]
    orders: ocel::Topic<String>,
}

fn main() {}
