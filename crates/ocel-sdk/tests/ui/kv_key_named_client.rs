#[derive(ocel::KvKey)]
#[ocel(pattern = "clients/:id", text)]
struct Client {
    id: String,
}

fn main() {}
