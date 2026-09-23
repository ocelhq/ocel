#[derive(ocel::Env)]
struct Env {
    #[ocel(folders = ["apps/web"])]
    flag: bool,
}

fn main() {}
