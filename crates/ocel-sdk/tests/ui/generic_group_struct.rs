#[derive(ocel::Group)]
struct GitHub<T> {
    #[ocel(key = "GITHUB_CLIENT_ID")]
    client_id: String,
    marker: std::marker::PhantomData<T>,
}

fn main() {}
