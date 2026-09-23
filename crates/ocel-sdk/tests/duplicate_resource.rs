#[allow(dead_code)]
#[derive(ocel::Resources)]
struct Web {
    #[ocel(name = "main")]
    db: ocel::Postgres,
}

#[allow(dead_code)]
#[derive(ocel::Resources)]
struct Worker {
    #[ocel(name = "main")]
    same_db: ocel::Postgres,
}

const WEB_LINE: &str = "5";
const WORKER_LINE: &str = "12";

#[test]
fn two_structs_may_not_declare_one_resource_name() {
    std::env::set_var("OCEL_PHASE", "discovery");

    let err = ocel::discover().expect_err("one name declared twice");

    assert!(matches!(err, ocel::Error::Definition { .. }));
    let said = err.to_string();
    for want in [
        &format!("duplicate_resource.rs:{WEB_LINE}"),
        &format!("duplicate_resource.rs:{WORKER_LINE}"),
        "'main' is declared in ",
        "A resource name is declared exactly once, in exactly one file.",
    ] {
        assert!(said.contains(want), "error = {said}, want {want} in it");
    }
}
