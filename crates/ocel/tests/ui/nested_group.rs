#[derive(ocel::Env)]
struct Inner {
    #[ocel(key = "INNER")]
    inner: String,
}

#[derive(ocel::Env)]
struct Middle {
    #[ocel(key = "MIDDLE")]
    middle: String,
    #[ocel(group)]
    inner: Inner,
}

#[derive(ocel::Env)]
struct Env {
    #[ocel(group)]
    middle: Middle,
}

fn main() {}
