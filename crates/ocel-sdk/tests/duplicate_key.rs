#[allow(dead_code)]
#[derive(ocel::Env)]
struct Web {
    #[ocel(key = "CONFLICT")]
    conflict: String,
}

#[allow(dead_code)]
#[derive(ocel::Env)]
struct Worker {
    #[ocel(key = "CONFLICT")]
    also_conflict: String,
}

const WEB_LINE: &str = "5";
const WORKER_LINE: &str = "12";

#[test]
fn two_structs_in_one_file_may_not_declare_one_key() {
    std::env::set_var("OCEL_PHASE", "discovery");

    let err = ocel::discover().expect_err("one key declared twice");

    assert!(matches!(err, ocel::Error::Definition { .. }));
    let said = err.to_string();
    for want in [
        &format!("duplicate_key.rs:{WEB_LINE}"),
        &format!("duplicate_key.rs:{WORKER_LINE}"),
        "'CONFLICT' is declared in ",
        "A key is declared exactly once, in exactly one file.",
    ] {
        assert!(said.contains(want), "error = {said}, want {want} in it");
    }
}
