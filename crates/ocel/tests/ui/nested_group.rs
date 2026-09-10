#[derive(ocel::Group)]
struct Inner {
    #[ocel(key = "INNER")]
    inner: String,
}

#[derive(ocel::Group)]
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
