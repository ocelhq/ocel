#[allow(dead_code)]
#[derive(ocel::Env)]
struct Env {
    #[ocel(key = "GITHUB_CLIENT_ID")]
    client_id: String,
    #[ocel(group)]
    github: Option<GitHub>,
}

#[allow(dead_code)]
#[derive(ocel::Group)]
struct GitHub {
    #[ocel(key = "GITHUB_CLIENT_ID")]
    client_id: String,
}

#[test]
fn a_key_declared_inside_a_group_and_beside_it_names_both_the_files() {
    std::env::set_var("OCEL_PHASE", "discovery");
    std::env::set_var("OCEL_DEV_SERVER", "http://127.0.0.1:1");

    let err = ocel::discover().expect_err("GITHUB_CLIENT_ID is declared twice");

    assert!(matches!(err, ocel::Error::Definition { .. }));
    assert!(
        err.to_string()
            .contains("A key is declared exactly once, in exactly one file."),
        "error = {err}, want the rule named"
    );
}
