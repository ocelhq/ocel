#[allow(dead_code)]
#[derive(ocel::Env)]
struct Web {
    #[ocel(group)]
    github: Option<WebGitHub>,
}

#[allow(dead_code)]
#[derive(ocel::Env)]
struct Api {
    #[ocel(group)]
    github: Option<ApiGitHub>,
}

#[allow(dead_code)]
#[derive(ocel::Group)]
struct WebGitHub {
    #[ocel(key = "WEB_GITHUB_CLIENT_ID")]
    client_id: String,
}

#[allow(dead_code)]
#[derive(ocel::Group)]
struct ApiGitHub {
    #[ocel(key = "API_GITHUB_CLIENT_ID")]
    client_id: String,
}

#[test]
fn one_group_name_claimed_by_two_structs_names_both_the_files() {
    std::env::set_var("OCEL_PHASE", "discovery");
    std::env::set_var("OCEL_DEV_SERVER", "http://127.0.0.1:1");

    let err = ocel::discover().expect_err("two structs claim the group 'github'");

    assert!(matches!(err, ocel::Error::Definition { .. }));
    assert!(
        err.to_string()
            .contains("A group is declared exactly once, in exactly one file."),
        "error = {err}, want the rule named"
    );
}
