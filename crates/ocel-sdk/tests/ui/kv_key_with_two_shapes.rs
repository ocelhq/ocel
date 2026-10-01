#[derive(ocel::KvKey)]
#[ocel(pattern = "session/:id", text, counter)]
struct Session {
    id: String,
}

fn main() {}
