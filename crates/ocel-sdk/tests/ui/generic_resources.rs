#[derive(ocel::Resources)]
struct Infra<'a> {
    db: ocel::Postgres,
    marker: std::marker::PhantomData<&'a ()>,
}

fn main() {}
