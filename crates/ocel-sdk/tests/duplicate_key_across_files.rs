mod sibling;

#[allow(dead_code)]
#[derive(ocel::Env)]
struct Web {
    #[ocel(key = "SHARED")]
    shared: String,
}

const WEB_LINE: &str = "7";

#[test]
fn two_files_may_not_declare_one_key() {
    std::env::set_var("OCEL_PHASE", "discovery");

    let err = ocel::discover().expect_err("one key declared in two files");

    assert!(matches!(err, ocel::Error::Definition { .. }));
    let said = err.to_string();
    for want in [
        &format!("duplicate_key_across_files.rs:{WEB_LINE}"),
        &format!("sibling/mod.rs:{}", sibling::SHARED_LINE),
        "'SHARED' is declared in ",
        "A key is declared exactly once, in exactly one file.",
    ] {
        assert!(said.contains(want), "error = {said}, want {want} in it");
    }
}
