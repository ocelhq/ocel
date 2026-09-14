#[derive(ocel::Resources)]
struct Infra {
    #[ocel(name = "main", reference, version = "16")]
    db: ocel::Postgres,
}

fn main() {}
