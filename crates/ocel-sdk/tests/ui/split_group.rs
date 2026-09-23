#[derive(ocel::Group)]
struct Split {
    #[ocel(key = "SPLIT_WEB", folders = ["/web"])]
    web: String,
    #[ocel(key = "SPLIT_API", folders = ["/api"])]
    api: String,
}

fn main() {}
