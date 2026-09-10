#[derive(ocel::Env)]
struct GitHub {
    #[ocel(key = "GITHUB_CLIENT_ID")]
    client_id: String,
}

#[derive(ocel::Env)]
struct Env {
    #[ocel(group)]
    github: GitHub,
}

fn main() {}
