#[derive(ocel::KvKey)]
#[ocel(pattern = "session/:id")]
struct Session {
    id: String,
}

fn main() {}
