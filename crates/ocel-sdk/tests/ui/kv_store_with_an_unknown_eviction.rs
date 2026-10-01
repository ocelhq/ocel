#[derive(ocel::KvKey)]
#[ocel(pattern = "session/:id", text)]
struct Session {
    id: String,
}

#[derive(ocel::Resources)]
struct Infra {
    #[ocel(eviction = "lru", entries = [Session])]
    cache: ocel::Kv,
}

fn main() {}
