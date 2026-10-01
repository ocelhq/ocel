#[derive(ocel::KvKey)]
#[ocel(pattern = "session/:id", text)]
struct Session {
    user: String,
}

fn main() {}
