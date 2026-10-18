#[derive(ocel::Resources)]
struct Infra {
    #[ocel(public = false)]
    uploads: ocel::Bucket,
}

fn main() {}
