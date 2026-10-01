#[derive(ocel::KvKey)]
#[ocel(pattern = "price/:amount", counter)]
struct Price {
    amount: f64,
}

fn main() {}
