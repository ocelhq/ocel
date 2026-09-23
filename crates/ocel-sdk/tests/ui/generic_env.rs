#[derive(ocel::Env)]
struct Env<T> {
    port: u16,
    marker: std::marker::PhantomData<T>,
}

fn main() {}
