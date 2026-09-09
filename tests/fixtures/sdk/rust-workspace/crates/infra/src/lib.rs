#[derive(ocel::Resources)]
pub struct Infra {
    #[ocel(name = "main")]
    pub db: ocel::Postgres,
}

#[derive(ocel::Env)]
pub struct Env {
    #[ocel(default = "hello")]
    pub greeting: String,
}
