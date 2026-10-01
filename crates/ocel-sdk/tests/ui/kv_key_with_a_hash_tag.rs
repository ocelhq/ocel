#[derive(ocel::KvKey)]
#[ocel(pattern = "session/{id}", text)]
struct Session {
    id: String,
}

fn main() {}
