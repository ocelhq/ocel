#[derive(ocel::KvKey)]
#[ocel(pattern = "session/:id", text, ttl = "10 seconds")]
struct Session {
    id: String,
}

fn main() {}
